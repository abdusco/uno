package main

import (
	"crypto/rand"
	"fmt"

	"github.com/samber/lo"
)

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:] // ASCII a-z -> A-Z; all our color names are lowercase ascii
}

// Card is one physical UNO card. Color is one of red/yellow/green/blue for
// number and action cards, or "wild" for the two wild cards (which get
// assigned an effective color when played).
type Card struct {
	ID    string `json:"id"`
	Color string `json:"color"`
	Value string `json:"value"` // "0".."9", "skip", "reverse", "draw2", "wild", "wild4"
}

var colors = []string{"red", "yellow", "green", "blue"}

// newDeck builds a 112-card UNO deck: the standard 108 (per color one 0,
// two each of 1-9, two skips, two reverses, two draw-twos (4*25=100), plus 4
// wilds and 4 wild-draw-fours (8) = 108), plus 4 house-rule "Color Bomb"
// wilds - when played, the chosen color also clears every remaining card of
// that color from the player's hand.
func newDeck() []Card {
	deck := make([]Card, 0, 112)
	add := func(color, value string) {
		deck = append(deck, Card{ID: randomID(), Color: color, Value: value})
	}
	for _, c := range colors {
		add(c, "0")
		for n := 1; n <= 9; n++ {
			add(c, itoa(n))
			add(c, itoa(n))
		}
		for i := 0; i < 2; i++ {
			add(c, "skip")
			add(c, "reverse")
			add(c, "draw2")
		}
	}
	for i := 0; i < 4; i++ {
		add("wild", "wild")
		add("wild", "wild4")
		add("wild", "colorbomb")
	}
	shuffle(deck)
	return deck
}

func itoa(n int) string {
	return string(rune('0' + n))
}

func shuffle(c []Card) {
	for i := len(c) - 1; i > 0; i-- {
		j := int(randUint32()) % (i + 1)
		c[i], c[j] = c[j], c[i]
	}
}

func randUint32() uint32 {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func mod(a, n int) int {
	return ((a % n) + n) % n
}

// gameState holds everything about an in-progress round. It is only ever
// touched from the owning room's single goroutine, same as room itself.
type gameState struct {
	deck      []Card
	discard   []Card
	hands     map[string][]Card // playerID -> hand
	order     []string          // playerID turn order, snapshotted when the game starts
	turnIdx   int
	direction int // +1 or -1
	// effective color of the top of the discard pile - equal to the top
	// card's color, except when the top card is a wild, in which case it's
	// whatever color the player chose.
	topColor string
	log      []string
	winnerID string

	// unoCalled tracks, per player, whether they've called "UNO" since last
	// dropping to exactly one card. A true value at two cards means the
	// current player called while playing their penultimate card.
	unoCalled map[string]bool
	// unoCatchableID is the player who just reached one card without calling.
	// The window closes as soon as the next player successfully plays or draws.
	unoCatchableID string

	// drawPending is true from the moment the current player draws a card
	// until they either play that card or explicitly pass -
	// this is what lets a player decide whether to play a just-drawn card
	// immediately instead of it silently ending their turn. lastDrawnCard
	// is only meaningful while drawPending is true.
	drawPending   bool
	lastDrawnCard *Card

	// wildDrawFour is set after a Wild Draw Four is played and remains until
	// the affected player accepts the draw or challenges the play. legal is
	// deliberately kept server-side so the victim cannot inspect the hand by
	// starting a challenge.
	wildDrawFour *wildDrawFourChallenge
}

type wildDrawFourChallenge struct {
	offenderID string
	victimID   string
	legal      bool
}

func startGame(order []string, nameOf func(string) string) *gameState {
	g := &gameState{
		deck:      newDeck(),
		hands:     make(map[string][]Card),
		order:     append([]string{}, order...),
		direction: 1,
		unoCalled: make(map[string]bool),
	}
	for _, pid := range g.order {
		g.hands[pid] = g.draw(7)
	}
	// A Wild Draw Four cannot start the discard pile. Return it to the
	// bottom of the draw pile and reveal another card.
	top := g.drawOpeningCard()
	g.discard = []Card{top}
	if top.Color == "wild" {
		g.topColor = colors[int(randUint32())%len(colors)]
	} else {
		g.topColor = top.Color
	}
	g.applyOpeningCard(top, nameOf)
	return g
}

func (g *gameState) drawOpeningCard() Card {
	for {
		top := g.draw(1)[0]
		if top.Value != "wild4" {
			return top
		}
		// draw removes from the end, so index zero is the bottom. Placing the
		// rejected card there also guarantees the replacement is different.
		g.deck = append([]Card{top}, g.deck...)
	}
}

func (g *gameState) applyOpeningCard(top Card, nameOf func(string) string) {
	opening := fmt.Sprintf("Game started with %s", cardLabel(top))
	switch top.Value {
	case "skip":
		skipped := g.currentPlayer()
		g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
		opening += fmt.Sprintf(" \u2014 %s is skipped", nameOf(skipped))
	case "reverse":
		g.direction = -1
		g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
		opening += " \u2014 direction reversed"
	case "draw2":
		victim := g.currentPlayer()
		g.hands[victim] = append(g.hands[victim], g.draw(2)...)
		g.unoCalled[victim] = false
		g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
		opening += fmt.Sprintf(" \u2014 %s draws 2 and is skipped", nameOf(victim))
	}
	g.addLog(fmt.Sprintf("%s \u2014 %s goes first", opening, nameOf(g.currentPlayer())))
}

// draw takes n cards off the deck, reshuffling the discard pile (minus its
// top card) back into the deck if it runs out.
func (g *gameState) draw(n int) []Card {
	out := make([]Card, 0, n)
	for i := 0; i < n; i++ {
		if len(g.deck) == 0 {
			g.reshuffle()
		}
		if len(g.deck) == 0 {
			// Both piles exhausted (only possible with a tiny deck edge
			// case) - stop rather than panic.
			break
		}
		last := len(g.deck) - 1
		out = append(out, g.deck[last])
		g.deck = g.deck[:last]
	}
	return out
}

func (g *gameState) currentPlayer() string {
	return g.order[g.turnIdx]
}

func (g *gameState) addLog(msg string) {
	g.log = append(g.log, msg)
	if len(g.log) > 8 {
		g.log = g.log[len(g.log)-8:]
	}
}

func cardLabel(c Card) string {
	names := map[string]string{
		"skip": "Skip", "reverse": "Reverse", "draw2": "Draw Two",
		"wild": "Wild", "wild4": "Wild Draw Four", "colorbomb": "Color Bomb",
	}
	valueLabel := c.Value
	if n, ok := names[c.Value]; ok {
		valueLabel = n
	}
	if c.Color == "wild" {
		return valueLabel
	}
	return capitalize(c.Color) + " " + valueLabel
}

func (g *gameState) isPlayable(c Card) bool {
	if c.Color == "wild" {
		return true
	}
	top := g.discard[len(g.discard)-1]
	return c.Color == g.topColor || c.Value == top.Value
}

// playCard removes the named card from playerID's hand and applies it.
// chosenColor is required (and only used) for wild / wild4 cards. nameOf
// resolves a playerID to a display name, used only for the activity log.
func (g *gameState) playCard(playerID, cardID, chosenColor string, nameOf func(string) string) error {
	if g.wildDrawFour != nil {
		return ErrIllegalMove{Message: "resolve the Wild Draw Four first"}
	}
	if g.currentPlayer() != playerID {
		return ErrIllegalMove{Message: "it's not your turn"}
	}
	hand := g.hands[playerID]
	idx := -1
	for i, c := range hand {
		if c.ID == cardID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrIllegalMove{Message: "you don't have that card"}
	}
	card := hand[idx]
	if g.drawPending && (g.lastDrawnCard == nil || card.ID != g.lastDrawnCard.ID) {
		return ErrIllegalMove{Message: "after drawing, you may only play the card you drew"}
	}
	if !g.isPlayable(card) {
		return ErrIllegalMove{Message: "that card doesn't match the discard pile"}
	}
	if card.Color == "wild" {
		if !lo.Contains(colors, chosenColor) {
			return ErrIllegalMove{Message: "pick a color for the wild card"}
		}
	}
	wildDrawFourLegal := true
	if card.Value == "wild4" {
		for i, held := range hand {
			if i != idx && held.Color == g.topColor {
				wildDrawFourLegal = false
				break
			}
		}
	}
	g.drawPending = false
	g.lastDrawnCard = nil
	calledBeforePlay := g.unoCalled[playerID]
	g.closeUnoCatchWindow()

	// Remove from hand, place on discard.
	g.hands[playerID] = append(hand[:idx], hand[idx+1:]...)
	g.discard = append(g.discard, card)
	if card.Color == "wild" {
		g.topColor = chosenColor
	} else {
		g.topColor = card.Color
	}

	label := cardLabel(card)
	if card.Color == "wild" {
		label = fmt.Sprintf("%s \u2192 %s", label, capitalize(chosenColor))
	}
	logLine := fmt.Sprintf("%s played %s", nameOf(playerID), label)

	n := len(g.order)
	skip := 0
	switch card.Value {
	case "skip":
		skip = 1
	case "reverse":
		if n > 2 {
			g.direction *= -1
		} else {
			skip = 1 // with 2 players, reverse behaves like skip
		}
	case "draw2":
		victim := g.order[mod(g.turnIdx+g.direction, n)]
		g.hands[victim] = append(g.hands[victim], g.draw(2)...)
		g.unoCalled[victim] = false
		skip = 1
		logLine += fmt.Sprintf(" \u2014 %s draws 2", nameOf(victim))
	case "wild4":
		victim := g.order[mod(g.turnIdx+g.direction, n)]
		g.wildDrawFour = &wildDrawFourChallenge{
			offenderID: playerID,
			victimID:   victim,
			legal:      wildDrawFourLegal,
		}
		logLine += fmt.Sprintf(" \u2014 %s may challenge", nameOf(victim))
	case "colorbomb":
		dumped, kept := lo.FilterReject(g.hands[playerID], func(hc Card, _ int) bool { return hc.Color == chosenColor })
		if len(dumped) > 0 {
			g.hands[playerID] = kept
			g.discard = append(g.discard, dumped...)
			logLine += fmt.Sprintf(" \u2014 dumps %d more %s card(s)", len(dumped), capitalize(chosenColor))
		}
	}
	g.addLog(logLine)

	if len(g.hands[playerID]) == 0 && g.wildDrawFour == nil {
		g.unoCalled[playerID] = false
		g.winnerID = playerID
		return nil
	}
	if len(g.hands[playerID]) == 1 {
		g.unoCalled[playerID] = calledBeforePlay
		if !calledBeforePlay {
			g.unoCatchableID = playerID
		}
	} else {
		g.unoCalled[playerID] = false
	}

	g.turnIdx = mod(g.turnIdx+g.direction*(1+skip), n)
	return nil
}

// drawCard gives the current player one card. It does not end their turn by
// itself - the caller (room.go) checks whether the drawn card is playable
// and, if so, leaves drawPending set so the player can choose to play it
// immediately or keep it (passTurn ends the turn for the latter case).
func (g *gameState) drawCard(playerID string, nameOf func(string) string) (Card, error) {
	if g.wildDrawFour != nil {
		return Card{}, ErrIllegalMove{Message: "resolve the Wild Draw Four first"}
	}
	if g.currentPlayer() != playerID {
		return Card{}, ErrIllegalMove{Message: "it's not your turn"}
	}
	if g.drawPending {
		return Card{}, ErrIllegalMove{Message: "you already drew a card - play it or keep it"}
	}
	cards := g.draw(1)
	if len(cards) == 0 {
		return Card{}, ErrIllegalMove{Message: "no cards left to draw"}
	}
	card := cards[0]
	g.closeUnoCatchWindow()
	g.hands[playerID] = append(g.hands[playerID], card)
	g.unoCalled[playerID] = false
	g.drawPending = true
	g.lastDrawnCard = &card
	g.addLog(fmt.Sprintf("%s drew a card", nameOf(playerID)))
	return card, nil
}

// passTurn ends the current player's turn after they've drawn a card and
// decided not to play it (or it wasn't playable to begin with).
func (g *gameState) passTurn(playerID string) error {
	if g.wildDrawFour != nil {
		return ErrIllegalMove{Message: "resolve the Wild Draw Four first"}
	}
	if g.currentPlayer() != playerID {
		return ErrIllegalMove{Message: "it's not your turn"}
	}
	if !g.drawPending {
		return ErrIllegalMove{Message: "you haven't drawn a card yet"}
	}
	g.drawPending = false
	g.lastDrawnCard = nil
	g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
	return nil
}

// acceptWildDrawFour makes the affected player take four cards and lose
// their turn without exposing whether the card could legally have been used.
func (g *gameState) acceptWildDrawFour(playerID string, nameOf func(string) string) error {
	pending := g.wildDrawFour
	if pending == nil {
		return ErrIllegalMove{Message: "there is no Wild Draw Four to resolve"}
	}
	if pending.victimID != playerID {
		return ErrIllegalMove{Message: "only the affected player can resolve the Wild Draw Four"}
	}
	g.hands[playerID] = append(g.hands[playerID], g.draw(4)...)
	g.unoCalled[playerID] = false
	g.wildDrawFour = nil
	g.addLog(fmt.Sprintf("%s accepts the Wild Draw Four and draws 4", nameOf(playerID)))
	g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
	g.finishWildDrawFourWin(pending.offenderID)
	return nil
}

// challengeWildDrawFour resolves the official bluff challenge. A legal play
// costs the challenger six cards and their turn; an illegal play costs the
// offender four cards while the challenger keeps their turn.
func (g *gameState) challengeWildDrawFour(playerID string, nameOf func(string) string) error {
	pending := g.wildDrawFour
	if pending == nil {
		return ErrIllegalMove{Message: "there is no Wild Draw Four to challenge"}
	}
	if pending.victimID != playerID {
		return ErrIllegalMove{Message: "only the affected player can challenge the Wild Draw Four"}
	}
	g.wildDrawFour = nil
	if pending.legal {
		g.hands[playerID] = append(g.hands[playerID], g.draw(6)...)
		g.unoCalled[playerID] = false
		g.addLog(fmt.Sprintf("%s challenged unsuccessfully and draws 6", nameOf(playerID)))
		g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
	} else {
		g.hands[pending.offenderID] = append(g.hands[pending.offenderID], g.draw(4)...)
		g.unoCalled[pending.offenderID] = false
		g.addLog(fmt.Sprintf("%s challenged successfully \u2014 %s draws 4", nameOf(playerID), nameOf(pending.offenderID)))
	}
	g.finishWildDrawFourWin(pending.offenderID)
	return nil
}

func (g *gameState) finishWildDrawFourWin(offenderID string) {
	if len(g.hands[offenderID]) == 0 {
		g.unoCalled[offenderID] = false
		g.winnerID = offenderID
	}
}

// autoSkip moves the turn to the next seat with no other effect - no card
// draw, no hand mutation - clearing any pending draw decision along the
// way. Used by room.go to step past a player who's currently disconnected,
// so the game doesn't stall waiting for someone who dropped mid-turn; their
// hand is untouched and they resume normally whenever they reconnect.
func (g *gameState) autoSkip() {
	g.drawPending = false
	g.lastDrawnCard = nil
	g.unoCalled[g.currentPlayer()] = false
	g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
}

// callUno lets the current player call while holding two cards, immediately
// before playing their penultimate card. A player who has just reached one
// card can also call during the catch window, until another card action wins
// the race and closes that window.
func (g *gameState) callUno(playerID string, nameOf func(string) string) error {
	hand, ok := g.hands[playerID]
	if !ok {
		return ErrIllegalMove{Message: "you're not in this game"}
	}
	if g.unoCalled[playerID] {
		return ErrIllegalMove{Message: "you already called UNO"}
	}
	if len(hand) == 2 && g.currentPlayer() != playerID {
		return ErrIllegalMove{Message: "call UNO when you play your second-to-last card"}
	}
	if len(hand) != 2 && !(len(hand) == 1 && g.unoCatchableID == playerID) {
		return ErrIllegalMove{Message: "you don't need to call UNO yet"}
	}
	g.unoCalled[playerID] = true
	if g.unoCatchableID == playerID {
		g.unoCatchableID = ""
	}
	g.addLog(fmt.Sprintf("%s called UNO!", nameOf(playerID)))
	return nil
}

// catchUno lets another player call out the one player whose catch window is
// currently open, penalizing them two cards.
func (g *gameState) catchUno(catcherID, targetID string, nameOf func(string) string) error {
	hand, ok := g.hands[targetID]
	if !ok {
		return ErrIllegalMove{Message: "that player isn't in this game"}
	}
	if catcherID == targetID {
		return ErrIllegalMove{Message: "you can't catch yourself"}
	}
	if len(hand) != 1 || g.unoCatchableID != targetID {
		return ErrIllegalMove{Message: "that player doesn't need to call UNO"}
	}
	if g.unoCalled[targetID] {
		return ErrIllegalMove{Message: "they already called UNO"}
	}
	g.hands[targetID] = append(g.hands[targetID], g.draw(2)...)
	g.unoCalled[targetID] = false
	g.unoCatchableID = ""
	g.addLog(fmt.Sprintf("%s got caught without calling UNO - draws 2", nameOf(targetID)))
	return nil
}

func (g *gameState) closeUnoCatchWindow() {
	if g.unoCatchableID != "" {
		g.unoCatchableID = ""
	}
}

func (g *gameState) reshuffle() {
	if len(g.discard) <= 1 {
		return
	}
	top := g.discard[len(g.discard)-1]
	rest := g.discard[:len(g.discard)-1]
	shuffle(rest)
	g.deck = append(g.deck, rest...)
	g.discard = []Card{top}
}

// removePlayer drops a disconnected player mid-game so the turn order
// doesn't get stuck pointing at someone who's gone. Their cards are
// discarded into the void - there's no persistence to reconcile against
// anyway.
func (g *gameState) removePlayer(playerID string) {
	idx := lo.IndexOf(g.order, playerID)
	if idx == -1 {
		return
	}
	wasCurrent := idx == g.turnIdx
	g.order = append(g.order[:idx], g.order[idx+1:]...)
	delete(g.hands, playerID)
	delete(g.unoCalled, playerID)
	if len(g.order) == 0 {
		return
	}
	if idx < g.turnIdx || (idx == g.turnIdx && !wasCurrent) {
		g.turnIdx--
	}
	g.turnIdx = mod(g.turnIdx, len(g.order))
}
