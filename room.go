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

// maxRoomPlayers caps persistent seats, not just currently connected players.
// Disconnected players retain their seat so their reconnect token can restore
// the same hand and identity without letting a room grow beyond this limit.
const maxRoomPlayers = 8

// Empty rooms remain available briefly so every player can reconnect after a
// network outage, but they must not occupy the registry or a goroutine forever.
const roomIdleTTL = time.Hour

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
	Code     string       `json:"code,omitempty"`
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

	// type "joined" - Token lets the client resume this identity after a
	// drop/reload (see inMsg.Token); Resumed is true only when this
	// "joined" is the result of a successful reconnect, not a fresh join.
	Token   string `json:"token,omitempty"`
	Resumed bool   `json:"resumed,omitempty"`

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
	UnoCatchable  bool   `json:"unoCatchable"`
	Connected     bool   `json:"connected"`
}

// player is a room member, connected or not - a disconnected player stays
// in the room (hand, seat, host status all intact) so a reconnect with a
// matching token can resume them instead of joining as someone new.
type player struct {
	id     string
	name   string
	isHost bool
	token  string // opaque reconnect secret, set once at creation

	send      chan outMsg
	connected bool
	// connGen is bumped on every (re)connect. handleLeave compares it
	// against the generation it was handed at connect time, so a leave
	// signal from a since-superseded connection (e.g. a stale socket that
	// only errors out after a reconnect already took over) is a no-op
	// instead of tearing down the session that just resumed.
	connGen int
	// kick force-closes whatever socket is currently attached to this
	// player, if any - used to take over on reconnect rather than leaving
	// two live sockets fighting over one `send` channel.
	kick func()
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
	leaveCh  chan leaveReq
	actionCh chan roomAction
	doneCh   chan struct{}
	// syncCh is test-only: closing the channel it's handed proves every
	// message sent to the room before this one has been fully processed,
	// not merely received (channel sends unblock the instant the select
	// statement receives them, before the corresponding handler returns).
	syncCh chan chan struct{}

	idleTTL  time.Duration
	onExpire func(*room)
}

type joinReq struct {
	name   string
	asHost bool
	token  string // non-empty means "try to resume this identity first"
	kick   func()
	result chan *joinResult
}

type joinResult struct {
	player *player
	// sendCh is the channel to range over for this connection - callers
	// must use this, not player.send, since the room goroutine may later
	// replace player.send on a future reconnect (an unsynchronized field
	// access otherwise).
	sendCh      chan outMsg
	connGen     int
	reconnected bool
	err         error
}

// leaveReq reports that one specific connection ended. connGen scopes it to
// that connection's generation, so a leave signal from a superseded
// connection can't clobber a session that already reconnected.
type leaveReq struct {
	playerID string
	connGen  int
}

type roomAction struct {
	playerID string
	msg      inMsg
}

func newRoom(id, name string) *room {
	return newRoomWithExpiry(id, name, roomIdleTTL, nil)
}

func newRoomWithExpiry(id, name string, idleTTL time.Duration, onExpire func(*room)) *room {
	r := &room{
		id:       id,
		name:     name,
		status:   "lobby",
		players:  make(map[string]*player),
		joinCh:   make(chan *joinReq),
		leaveCh:  make(chan leaveReq),
		actionCh: make(chan roomAction),
		doneCh:   make(chan struct{}),
		syncCh:   make(chan chan struct{}),
		idleTTL:  idleTTL,
		onExpire: onExpire,
	}
	go r.run()
	return r
}

func (r *room) run() {
	timer := time.NewTimer(r.idleTTL)
	expiry := timer.C
	expiryArmed := true
	defer func() {
		timer.Stop()
		close(r.doneCh)
	}()
	refreshExpiry := func() {
		empty := r.connectedPlayers() == 0
		if empty == expiryArmed {
			return
		}
		if empty {
			timer.Reset(r.idleTTL)
			expiry = timer.C
			expiryArmed = true
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		expiry = nil
		expiryArmed = false
	}

	for {
		select {
		case req := <-r.joinCh:
			r.handleJoin(req)
			refreshExpiry()
		case done := <-r.syncCh:
			close(done)
		case lr := <-r.leaveCh:
			r.handleLeave(lr)
			refreshExpiry()
		case act := <-r.actionCh:
			r.handleAction(act)
		case <-expiry:
			if r.onExpire != nil {
				r.onExpire(r)
			}
			return
		}
	}
}

func (r *room) connectedPlayers() int {
	n := 0
	for _, p := range r.players {
		if p.connected {
			n++
		}
	}
	return n
}

func (r *room) handleJoin(req *joinReq) {
	// A token match resumes an existing player - name/hand/seat/host status
	// all untouched - and, unlike a fresh join, this is allowed even mid
	// game (that's the whole point: this is how a dropped connection gets
	// back into a game already in progress). An unknown/expired/wrong-room
	// token just falls through to the normal fresh-join path below, so the
	// caller never needs special-case retry logic.
	if req.token != "" {
		if p, ok := r.findByToken(req.token); ok {
			if p.connected {
				// A connection is still nominally attached (e.g. a stale
				// half-dead socket that hasn't errored out yet - there's no
				// ping/read-deadline to notice that on its own). Force it
				// closed on both ends: kick() breaks its reader loop, and
				// closing its send channel breaks its writer goroutine's
				// range loop - otherwise that goroutine leaks forever,
				// since once we replace p.send below the room never writes
				// to the old channel again for anything to unblock it.
				if p.kick != nil {
					p.kick()
				}
				close(p.send)
			}
			p.send = make(chan outMsg, 8)
			p.connected = true
			p.connGen++
			p.kick = req.kick
			req.result <- &joinResult{player: p, sendCh: p.send, connGen: p.connGen, reconnected: true}

			p.send <- outMsg{
				Type: "joined", RoomID: r.id, RoomName: r.name,
				Self: r.viewOf(p), Players: r.playerViews(),
				Token: p.token, Resumed: true,
			}
			r.broadcastPlayers()
			if r.status == "playing" && r.game != nil {
				// Reconnecting might restore enough players for a frozen
				// turn to resume - route through the same check a
				// disconnect would.
				r.autoSkipDisconnected()
				r.sendStateTo(p)
			} else if r.game != nil && r.game.winnerID != "" {
				// post-game, pre-rematch lobby window - replay the banner.
				p.send <- outMsg{Type: "gameOver", WinnerID: r.game.winnerID, WinnerName: r.nameOf(r.game.winnerID)}
			}
			return
		}
		// fall through: no such token here, treat as a fresh join
	}

	if r.status == "playing" {
		req.result <- &joinResult{err: ErrGameInProgress{}}
		return
	}
	if len(r.players) >= maxRoomPlayers {
		req.result <- &joinResult{err: ErrRoomFull{}}
		return
	}
	// If nobody currently connected is host - a brand new room, or one
	// where the host disconnected and never made it back - the next person
	// in gets promoted, so the room is never stuck hostless.
	hasHost := false
	for _, existing := range r.players {
		if existing.isHost && existing.connected {
			hasHost = true
			break
		}
	}
	isHost := req.asHost || !hasHost
	if isHost {
		// Enforce "at most one host": if promotion is happening because
		// nobody connected currently holds it, clear the flag on any
		// disconnected player still carrying it from before - otherwise
		// they'd silently become a second host the moment they reconnect.
		for _, existing := range r.players {
			existing.isHost = false
		}
	}
	id := randomID()
	p := &player{
		id:        id,
		name:      req.name,
		isHost:    isHost,
		token:     newToken(),
		send:      make(chan outMsg, 8),
		connected: true,
		connGen:   1,
		kick:      req.kick,
	}
	r.players[id] = p
	r.order = append(r.order, id)
	req.result <- &joinResult{player: p, sendCh: p.send, connGen: p.connGen}

	// Tell the new player who they are and who else is here.
	p.send <- outMsg{Type: "joined", RoomID: r.id, RoomName: r.name, Self: r.viewOf(p), Players: r.playerViews(), Token: p.token}
	r.broadcastPlayers()
}

// findByToken looks up a room member (connected or not) by their reconnect
// token. Rooms are small (a handful of players), so a linear scan is fine
// and avoids keeping a second map in sync.
func (r *room) findByToken(token string) (*player, bool) {
	for _, p := range r.players {
		if p.token == token {
			return p, true
		}
	}
	return nil, false
}

func (r *room) handleLeave(lr leaveReq) {
	p, ok := r.players[lr.playerID]
	if !ok || p.connGen != lr.connGen {
		// Either they're already gone, or this signal is from a connection
		// that's since been superseded by a reconnect - either way, acting
		// on it now would tear down a session that's already moved on.
		return
	}
	p.connected = false
	close(p.send)

	// A disconnected player stays a full room/game member - their name,
	// hand, seat and host status are all left intact so a reconnect with a
	// matching token can resume them. If they were host, hand it to the
	// next *connected* player instead (r.order[0] itself might now be a
	// long-disconnected seat) - but only if such a player actually exists;
	// otherwise leave host status where it is, so a lone disconnected host
	// gets it back automatically on reconnect instead of the room becoming
	// permanently hostless.
	if p.isHost {
		for _, oid := range r.order {
			if oid == lr.playerID {
				continue
			}
			if next, ok := r.players[oid]; ok && next.connected {
				p.isHost = false
				next.isHost = true
				break
			}
		}
	}

	// Keep lobby metadata (especially host ownership) current even while the
	// game screen is showing. Clients retain this snapshot for the rematch
	// lobby, and use it to update their local isHost flag immediately.
	r.broadcastPlayers()
	if r.status == "playing" && r.game != nil {
		r.autoSkipDisconnected()
		r.broadcastState()
		return
	}
}

// connectedInGame counts how many of the current game's players still have
// a live connection. Below 2, there's nobody to play against - the round
// pauses rather than ending, and resumes on its own once enough players
// are back (no explicit "unpause" needed: reconnecting doesn't touch turn
// order, so whoever's turn it already was just continues).
func (r *room) connectedInGame() int {
	g := r.game
	if g == nil {
		return 0
	}
	n := 0
	for _, id := range g.order {
		if p, ok := r.players[id]; ok && p.connected {
			n++
		}
	}
	return n
}

// autoSkipDisconnected advances the turn past any current player who's
// disconnected (no timers involved - this only runs right after something
// already changed, i.e. a disconnect or a turn advance). A disconnected
// player's hand and seat are never touched - if they reconnect, they
// resume exactly where they left off, having possibly missed a go-around.
// If fewer than two players are left connected, skipping would either spin
// forever (nobody to land on) or just bounce between empty seats and the
// same lone player - so it does nothing instead, freezing the turn where
// it is until enough players reconnect.
func (r *room) autoSkipDisconnected() {
	g := r.game
	if g == nil || r.connectedInGame() < 2 {
		return
	}
	for {
		cur, ok := r.players[g.currentPlayer()]
		if ok && cur.connected {
			return
		}
		g.autoSkip()
	}
}

func (r *room) handleAction(act roomAction) {
	p, ok := r.players[act.playerID]
	if !ok {
		return
	}
	switch act.msg.Type {
	case "start":
		if !p.isHost {
			r.sendError(p, ErrIllegalMove{Message: "only the host can start the game"})
			return
		}
		if len(r.players) < 2 {
			r.sendError(p, ErrIllegalMove{Message: "need at least 2 players to start"})
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
			r.sendError(p, ErrIllegalMove{Message: "the game hasn't started"})
			return
		}
		if r.connectedInGame() < 2 {
			r.sendError(p, ErrIllegalMove{Message: "waiting for other players to reconnect"})
			return
		}
		if err := r.game.playCard(p.id, act.msg.CardID, act.msg.Color, r.nameOf); err != nil {
			r.sendError(p, err)
			return
		}
		r.afterTurnAdvance()

	case "draw":
		if r.status != "playing" || r.game == nil {
			r.sendError(p, ErrIllegalMove{Message: "the game hasn't started"})
			return
		}
		if r.connectedInGame() < 2 {
			r.sendError(p, ErrIllegalMove{Message: "waiting for other players to reconnect"})
			return
		}
		drawn, err := r.game.drawCard(p.id, r.nameOf)
		if err != nil {
			r.sendError(p, err)
			return
		}
		// Nothing to decide if the drawn card can't be played anyway - end
		// the turn immediately instead of making the player pass manually.
		if !r.game.isPlayable(drawn) {
			if err := r.game.passTurn(p.id); err != nil {
				r.sendError(p, err)
				return
			}
			r.afterTurnAdvance()
		} else {
			r.broadcastState()
		}

	case "pass":
		if r.status != "playing" || r.game == nil {
			r.sendError(p, ErrIllegalMove{Message: "the game hasn't started"})
			return
		}
		if r.connectedInGame() < 2 {
			r.sendError(p, ErrIllegalMove{Message: "waiting for other players to reconnect"})
			return
		}
		if err := r.game.passTurn(p.id); err != nil {
			r.sendError(p, err)
			return
		}
		r.afterTurnAdvance()

	case "callUno":
		if r.status != "playing" || r.game == nil {
			r.sendError(p, ErrIllegalMove{Message: "the game hasn't started"})
			return
		}
		if err := r.game.callUno(p.id, r.nameOf); err != nil {
			r.sendError(p, err)
			return
		}
		r.broadcastState()

	case "catchUno":
		if r.status != "playing" || r.game == nil {
			r.sendError(p, ErrIllegalMove{Message: "the game hasn't started"})
			return
		}
		if err := r.game.catchUno(p.id, act.msg.TargetID, r.nameOf); err != nil {
			r.sendError(p, err)
			return
		}
		r.broadcastState()

	default:
		log.Printf("room %s: unknown action %q from %s", r.id, act.msg.Type, act.playerID)
	}
}

func (r *room) sendError(p *player, err error) {
	code, message := clientErrorDetails(err)
	p.send <- outMsg{Type: "error", Code: code, Message: message}
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

// afterTurnAdvance is the common tail of every action that can move the
// turn forward: skip past anyone currently disconnected, then either
// declare the game over (if that left at most one connected player) or
// broadcast the resulting state.
func (r *room) afterTurnAdvance() {
	r.autoSkipDisconnected()
	if r.game.winnerID != "" {
		r.broadcastGameOver()
		r.status = "lobby" // allow the host to start a rematch
		return
	}
	r.broadcastState()
}

// stateFor builds the personalized "state" snapshot for one player. Shared
// by broadcastState (every player, on any game event) and sendStateTo (one
// player, right after they reconnect).
func (r *room) stateFor(id string, gamePlayers []gamePlayerView, top Card, logCopy []string) outMsg {
	g := r.game
	// Hand slices must be copied - they alias the same backing array that a
	// later action (e.g. someone else's draw-two landing on this player)
	// will mutate, and that mutation happens in this goroutine while a
	// per-connection writer goroutine may still be marshaling this message
	// concurrently.
	handCopy := append([]Card(nil), g.hands[id]...)
	var yourDrawn *Card
	if g.drawPending && g.lastDrawnCard != nil && id == g.currentPlayer() {
		c := *g.lastDrawnCard
		yourDrawn = &c
	}
	return outMsg{
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
}

func (r *room) gamePlayerViews() []gamePlayerView {
	g := r.game
	views := make([]gamePlayerView, 0, len(g.order))
	for _, id := range g.order {
		views = append(views, gamePlayerView{
			ID:            id,
			Name:          r.nameOf(id),
			HandCount:     len(g.hands[id]),
			IsCurrentTurn: id == g.currentPlayer(),
			UnoCalled:     g.unoCalled[id],
			UnoCatchable:  g.unoCatchableID == id,
			Connected:     r.players[id] != nil && r.players[id].connected,
		})
	}
	return views
}

func (r *room) broadcastState() {
	g := r.game
	if g == nil {
		return
	}
	gamePlayers := r.gamePlayerViews()
	top := g.discard[len(g.discard)-1]
	// Copy the log once per broadcast; every player's message can safely
	// share this one copy since nothing mutates it after this point.
	logCopy := append([]string(nil), g.log...)
	for _, id := range g.order {
		p, ok := r.players[id]
		if !ok || !p.connected {
			continue
		}
		select {
		case p.send <- r.stateFor(id, gamePlayers, top, logCopy):
		default:
			// slow/dead client, drop the message rather than block the room
		}
	}
}

// sendStateTo sends one player their personalized state snapshot directly,
// bypassing the broadcast loop - used right after a reconnect, since that
// player needs their hand/turn/etc. immediately rather than waiting for the
// next unrelated game event to trigger a broadcast.
func (r *room) sendStateTo(p *player) {
	g := r.game
	if g == nil {
		return
	}
	gamePlayers := r.gamePlayerViews()
	top := g.discard[len(g.discard)-1]
	logCopy := append([]string(nil), g.log...)
	p.send <- r.stateFor(p.id, gamePlayers, top, logCopy)
}

func (r *room) broadcastGameOver() {
	r.broadcastAll(outMsg{
		Type:       "gameOver",
		WinnerID:   r.game.winnerID,
		WinnerName: r.nameOf(r.game.winnerID),
	})
}

func (r *room) viewOf(p *player) *playerView {
	return &playerView{ID: p.id, Name: p.name, IsHost: p.isHost, Connected: p.connected}
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
		if p.connected {
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
	mu      sync.Mutex
	rooms   map[string]*room
	idleTTL time.Duration
}

func newRegistry() *registry {
	return &registry{rooms: make(map[string]*room), idleTTL: roomIdleTTL}
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
	r := newRoomWithExpiry(code, name, reg.idleTTL, reg.remove)
	reg.rooms[code] = r
	return r
}

func (reg *registry) remove(r *room) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.rooms[r.id] == r {
		delete(reg.rooms, r.id)
	}
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

// newToken generates an opaque reconnect secret. Same shape as randomID -
// kept as a separate function since the two mean different things: an id is
// public information (broadcast to every player in the room), a token is a
// secret that must only ever be sent to the player it belongs to.
func newToken() string {
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
