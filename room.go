package main

import (
	"crypto/rand"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"
)

// roomCodeAlphabet excludes visually ambiguous characters (0/O, 1/I/L).
const roomCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newRoomCode() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	out := make([]byte, 5)
	for i, c := range b {
		out[i] = roomCodeAlphabet[int(c)%len(roomCodeAlphabet)]
	}
	return string(out)
}

// outMsg is anything the server sends to a client over the socket. Fields
// are grouped by which message Type they belong to; unused ones are simply
// omitted from the JSON via omitempty.
type outMsg struct {
	Type     string       `json:"type"`
	RoomID   string       `json:"roomId,omitempty"`
	RoomName string       `json:"roomName,omitempty"`
	Self     *playerView  `json:"self,omitempty"`
	Players  []playerView `json:"players,omitempty"`
	Message  string       `json:"message,omitempty"`

	// type "state" - a personalized snapshot of an in-progress game.
	Hand            []Card           `json:"hand,omitempty"`
	DiscardTop      *Card            `json:"discardTop,omitempty"`
	TopColor        string           `json:"topColor,omitempty"`
	GamePlayers     []gamePlayerView `json:"gamePlayers,omitempty"`
	CurrentPlayerID string           `json:"currentPlayerId,omitempty"`
	YourTurn        bool             `json:"yourTurn,omitempty"`
	DeckCount       int              `json:"deckCount,omitempty"`
	Log             []string         `json:"log,omitempty"`
	// YourDrawnCard is set only for the player whose draw decision is
	// pending (see gameState.drawPending) - the client shows a "play it or
	// keep it" prompt for exactly this card.
	YourDrawnCard *Card `json:"yourDrawnCard,omitempty"`

	// type "gameOver"
	WinnerID   string `json:"winnerId,omitempty"`
	WinnerName string `json:"winnerName,omitempty"`
}

// inMsg is anything a client sends to the server.
type inMsg struct {
	Type     string `json:"type"`
	CardID   string `json:"cardId,omitempty"`
	Color    string `json:"color,omitempty"`
	TargetID string `json:"targetId,omitempty"` // "catchUno"
}

type playerView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsHost    bool   `json:"isHost"`
	Connected bool   `json:"connected"`
}

type gamePlayerView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	HandCount     int    `json:"handCount"`
	IsCurrentTurn bool   `json:"isCurrentTurn"`
	UnoCalled     bool   `json:"unoCalled"`
}

// player is a single connected (or disconnected-but-not-yet-reaped) client.
type player struct {
	id     string
	name   string
	isHost bool
	send   chan outMsg
	closed bool
}

// room owns all state for one game. Every field below is only ever touched
// from inside run(), so no mutex is needed - mutation happens by sending on
// a channel and letting the room's own goroutine apply it.
type room struct {
	id      string
	name    string
	status  string // "lobby" | "playing"
	players map[string]*player
	order   []string // join order, for stable display

	game *gameState // nil until the first "start"

	joinCh   chan *joinReq
	leaveCh  chan string
	actionCh chan roomAction

	createdAt time.Time
}

type joinReq struct {
	name   string
	asHost bool
	result chan *joinResult
}

type joinResult struct {
	player *player
	err    string
}

type roomAction struct {
	playerID string
	msg      inMsg
}

func newRoom(id, name string) *room {
	r := &room{
		id:        id,
		name:      name,
		status:    "lobby",
		players:   make(map[string]*player),
		joinCh:    make(chan *joinReq),
		leaveCh:   make(chan string),
		actionCh:  make(chan roomAction),
		createdAt: time.Now(),
	}
	go r.run()
	return r
}

func (r *room) run() {
	for {
		select {
		case req := <-r.joinCh:
			r.handleJoin(req)
		case pid := <-r.leaveCh:
			r.handleLeave(pid)
		case act := <-r.actionCh:
			r.handleAction(act)
		}
	}
}

func (r *room) handleJoin(req *joinReq) {
	if r.status == "playing" {
		req.result <- &joinResult{err: "this game has already started"}
		return
	}
	// If nobody currently in the room is host - a brand new room, or one
	// where the host disconnected and rejoined as a fresh (non-host)
	// connection since there's no session identity to restore their old
	// seat - the next person in gets promoted, so the room is never stuck
	// hostless.
	hasHost := false
	for _, existing := range r.players {
		if existing.isHost {
			hasHost = true
			break
		}
	}
	id := randomID()
	p := &player{
		id:     id,
		name:   req.name,
		isHost: req.asHost || !hasHost,
		send:   make(chan outMsg, 8),
	}
	r.players[id] = p
	r.order = append(r.order, id)
	req.result <- &joinResult{player: p}

	// Tell the new player who they are and who else is here.
	p.send <- outMsg{Type: "joined", RoomID: r.id, RoomName: r.name, Self: r.viewOf(p), Players: r.playerViews()}
	r.broadcastPlayers()
}

func (r *room) handleLeave(pid string) {
	p, ok := r.players[pid]
	if !ok {
		return
	}
	// Keep the player in the roster but mark them disconnected, so a
	// refresh/host restart doesn't silently vanish - matches "no
	// persistence" while still being forgiving of a dropped connection
	// during the lobby.
	p.closed = true
	close(p.send)
	delete(r.players, pid)
	for i, oid := range r.order {
		if oid == pid {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	// If the host left, promote the longest-waiting remaining player.
	if p.isHost && len(r.order) > 0 {
		if next, ok := r.players[r.order[0]]; ok {
			next.isHost = true
		}
	}

	if r.status == "playing" && r.game != nil {
		r.game.removePlayer(pid)
		if len(r.game.order) == 1 {
			r.game.winnerID = r.game.order[0]
			r.broadcastGameOver()
			r.status = "lobby"
		} else if len(r.game.order) > 1 {
			r.broadcastState()
		}
		return
	}
	r.broadcastPlayers()
}

func (r *room) handleAction(act roomAction) {
	p, ok := r.players[act.playerID]
	if !ok {
		return
	}
	switch act.msg.Type {
	case "start":
		if !p.isHost {
			p.send <- outMsg{Type: "error", Message: "only the host can start the game"}
			return
		}
		if len(r.players) < 2 {
			p.send <- outMsg{Type: "error", Message: "need at least 2 players to start"}
			return
		}
		if r.status == "playing" {
			return
		}
		r.status = "playing"
		r.game = startGame(r.order, r.nameOf)
		r.broadcastAll(outMsg{Type: "started"})
		r.broadcastState()

	case "play":
		if r.status != "playing" || r.game == nil {
			p.send <- outMsg{Type: "error", Message: "the game hasn't started"}
			return
		}
		ok, errMsg := r.game.playCard(p.id, act.msg.CardID, act.msg.Color, r.nameOf)
		if !ok {
			p.send <- outMsg{Type: "error", Message: errMsg}
			return
		}
		if r.game.winnerID != "" {
			r.broadcastGameOver()
			r.status = "lobby" // allow the host to start a rematch
			return
		}
		r.broadcastState()

	case "draw":
		if r.status != "playing" || r.game == nil {
			p.send <- outMsg{Type: "error", Message: "the game hasn't started"}
			return
		}
		drawn, ok, errMsg := r.game.drawCard(p.id, r.nameOf)
		if !ok {
			p.send <- outMsg{Type: "error", Message: errMsg}
			return
		}
		// Nothing to decide if the drawn card can't be played anyway - end
		// the turn immediately instead of making the player pass manually.
		if !r.game.isPlayable(drawn) {
			r.game.passTurn(p.id)
		}
		r.broadcastState()

	case "pass":
		if r.status != "playing" || r.game == nil {
			p.send <- outMsg{Type: "error", Message: "the game hasn't started"}
			return
		}
		ok, errMsg := r.game.passTurn(p.id)
		if !ok {
			p.send <- outMsg{Type: "error", Message: errMsg}
			return
		}
		r.broadcastState()

	case "callUno":
		if r.status != "playing" || r.game == nil {
			p.send <- outMsg{Type: "error", Message: "the game hasn't started"}
			return
		}
		ok, errMsg := r.game.callUno(p.id, r.nameOf)
		if !ok {
			p.send <- outMsg{Type: "error", Message: errMsg}
			return
		}
		r.broadcastState()

	case "catchUno":
		if r.status != "playing" || r.game == nil {
			p.send <- outMsg{Type: "error", Message: "the game hasn't started"}
			return
		}
		ok, errMsg := r.game.catchUno(act.msg.TargetID, r.nameOf)
		if !ok {
			p.send <- outMsg{Type: "error", Message: errMsg}
			return
		}
		r.broadcastState()

	default:
		log.Printf("room %s: unknown action %q from %s", r.id, act.msg.Type, act.playerID)
	}
}

// nameOf resolves a player ID to a display name, falling back to something
// inert if the player has since disconnected. Used by the game engine for
// activity-log lines.
func (r *room) nameOf(id string) string {
	if p, ok := r.players[id]; ok {
		return p.name
	}
	return "Someone"
}

func (r *room) broadcastState() {
	g := r.game
	if g == nil {
		return
	}
	gamePlayers := make([]gamePlayerView, 0, len(g.order))
	for _, id := range g.order {
		gamePlayers = append(gamePlayers, gamePlayerView{
			ID:            id,
			Name:          r.nameOf(id),
			HandCount:     len(g.hands[id]),
			IsCurrentTurn: id == g.currentPlayer(),
			UnoCalled:     g.unoCalled[id],
		})
	}
	top := g.discard[len(g.discard)-1]
	// Copy the log once per broadcast; every player's message can safely
	// share this one copy since nothing mutates it after this point.
	logCopy := append([]string(nil), g.log...)
	for _, id := range g.order {
		p, ok := r.players[id]
		if !ok {
			continue
		}
		// Hand slices must be copied - they alias the same backing array
		// that a later action (e.g. someone else's draw-two landing on
		// this player) will mutate, and that mutation happens in this
		// goroutine while a per-connection writer goroutine may still be
		// marshaling this message concurrently.
		handCopy := append([]Card(nil), g.hands[id]...)
		var yourDrawn *Card
		if g.drawPending && g.lastDrawnCard != nil && id == g.currentPlayer() {
			c := *g.lastDrawnCard
			yourDrawn = &c
		}
		msg := outMsg{
			Type:            "state",
			Hand:            handCopy,
			DiscardTop:      &top,
			TopColor:        g.topColor,
			GamePlayers:     gamePlayers,
			CurrentPlayerID: g.currentPlayer(),
			YourTurn:        id == g.currentPlayer(),
			DeckCount:       len(g.deck),
			Log:             logCopy,
			YourDrawnCard:   yourDrawn,
		}
		select {
		case p.send <- msg:
		default:
			// slow/dead client, drop the message rather than block the room
		}
	}
}

func (r *room) broadcastGameOver() {
	r.broadcastAll(outMsg{
		Type:       "gameOver",
		WinnerID:   r.game.winnerID,
		WinnerName: r.nameOf(r.game.winnerID),
	})
}

func (r *room) viewOf(p *player) *playerView {
	return &playerView{ID: p.id, Name: p.name, IsHost: p.isHost, Connected: true}
}

func (r *room) playerViews() []playerView {
	views := make([]playerView, 0, len(r.order))
	for _, id := range r.order {
		if p, ok := r.players[id]; ok {
			views = append(views, *r.viewOf(p))
		}
	}
	return views
}

func (r *room) broadcastPlayers() {
	r.broadcastAll(outMsg{Type: "players", Players: r.playerViews()})
}

func (r *room) broadcastAll(m outMsg) {
	for _, p := range r.players {
		if !p.closed {
			select {
			case p.send <- m:
			default:
				// slow/dead client, drop the message rather than block the room
			}
		}
	}
}

// --- room registry -------------------------------------------------------

type registry struct {
	mu    sync.Mutex
	rooms map[string]*room
}

func newRegistry() *registry {
	return &registry{rooms: make(map[string]*room)}
}

func (reg *registry) create(name string) *room {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	var code string
	for {
		code = newRoomCode()
		if _, exists := reg.rooms[code]; !exists {
			break
		}
	}
	r := newRoom(code, name)
	reg.rooms[code] = r
	return r
}

func (reg *registry) get(code string) *room {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.rooms[strings.ToUpper(code)]
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hexEncode(b)
}

func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0xF]
	}
	return string(out)
}

// jsonMustMarshal is only used for log lines / debugging.
func jsonMustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<marshal error>"
	}
	return string(b)
}
