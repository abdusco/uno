package main

import (
	"crypto/rand"
	"fmt"
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
	// dropping to exactly one card. Only meaningful while a player's hand
	// size is 1 - reset to false the moment they land on one card, and
	// irrelevant (ignored) at any other hand size.
	unoCalled map[string]bool

	// drawPending is true from the moment the current player draws a card
	// until they either play it (or any other card) or explicitly pass -
	// this is what lets a player decide whether to play a just-drawn card
	// immediately instead of it silently ending their turn. lastDrawnCard
	// is only meaningful while drawPending is true.
	drawPending   bool
	lastDrawnCard *Card
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
	// Flip the first card. If it happens to be a wild, just assign it a
	// random color rather than re-drawing - keeps startup simple and it's
	// a one-in-27 edge case players won't notice.
	top := g.draw(1)[0]
	g.discard = []Card{top}
	if top.Color == "wild" {
		g.topColor = colors[int(randUint32())%len(colors)]
	} else {
		g.topColor = top.Color
	}
	g.addLog(fmt.Sprintf("Game started - %s goes first", nameOf(g.currentPlayer())))
	return g
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
func (g *gameState) playCard(playerID, cardID, chosenColor string, nameOf func(string) string) (ok bool, errMsg string) {
	if g.currentPlayer() != playerID {
		return false, "it's not your turn"
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
		return false, "you don't have that card"
	}
	card := hand[idx]
	if !g.isPlayable(card) {
		return false, "that card doesn't match the discard pile"
	}
	g.drawPending = false
	g.lastDrawnCard = nil
	if card.Color == "wild" {
		valid := false
		for _, c := range colors {
			if c == chosenColor {
				valid = true
			}
		}
		if !valid {
			return false, "pick a color for the wild card"
		}
	}

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
		skip = 1
		logLine += fmt.Sprintf(" \u2014 %s draws 2", nameOf(victim))
	case "wild4":
		victim := g.order[mod(g.turnIdx+g.direction, n)]
		g.hands[victim] = append(g.hands[victim], g.draw(4)...)
		skip = 1
		logLine += fmt.Sprintf(" \u2014 %s draws 4", nameOf(victim))
	case "colorbomb":
		var kept, dumped []Card
		for _, hc := range g.hands[playerID] {
			if hc.Color == chosenColor {
				dumped = append(dumped, hc)
			} else {
				kept = append(kept, hc)
			}
		}
		if len(dumped) > 0 {
			g.hands[playerID] = kept
			g.discard = append(g.discard, dumped...)
			logLine += fmt.Sprintf(" \u2014 dumps %d more %s card(s)", len(dumped), capitalize(chosenColor))
		}
	}
	g.addLog(logLine)

	if len(g.hands[playerID]) == 0 {
		g.winnerID = playerID
		return true, ""
	}
	if len(g.hands[playerID]) == 1 {
		g.unoCalled[playerID] = false
	}

	g.turnIdx = mod(g.turnIdx+g.direction*(1+skip), n)
	return true, ""
}

// drawCard gives the current player one card. It does not end their turn by
// itself - the caller (room.go) checks whether the drawn card is playable
// and, if so, leaves drawPending set so the player can choose to play it
// immediately or keep it (passTurn ends the turn for the latter case). This
// is still a deliberate simplification versus real UNO: no forced play, and
// no draw-stacking on draw-twos/wild-fours.
func (g *gameState) drawCard(playerID string, nameOf func(string) string) (drawn Card, ok bool, errMsg string) {
	if g.currentPlayer() != playerID {
		return Card{}, false, "it's not your turn"
	}
	if g.drawPending {
		return Card{}, false, "you already drew a card - play it or keep it"
	}
	cards := g.draw(1)
	if len(cards) == 0 {
		return Card{}, false, "no cards left to draw"
	}
	card := cards[0]
	g.hands[playerID] = append(g.hands[playerID], card)
	g.drawPending = true
	g.lastDrawnCard = &card
	g.addLog(fmt.Sprintf("%s drew a card", nameOf(playerID)))
	return card, true, ""
}

// passTurn ends the current player's turn after they've drawn a card and
// decided not to play it (or it wasn't playable to begin with).
func (g *gameState) passTurn(playerID string) (ok bool, errMsg string) {
	if g.currentPlayer() != playerID {
		return false, "it's not your turn"
	}
	if !g.drawPending {
		return false, "you haven't drawn a card yet"
	}
	g.drawPending = false
	g.lastDrawnCard = nil
	g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
	return true, ""
}

// callUno lets a player declare UNO once they're down to one or two cards
// (the latter covers calling right before playing their second-to-last
// card, which is how it's usually done at the table).
func (g *gameState) callUno(playerID string, nameOf func(string) string) (ok bool, errMsg string) {
	hand, ok := g.hands[playerID]
	if !ok {
		return false, "you're not in this game"
	}
	if len(hand) > 2 {
		return false, "you don't need to call UNO yet"
	}
	g.unoCalled[playerID] = true
	g.addLog(fmt.Sprintf("%s called UNO!", nameOf(playerID)))
	return true, ""
}

// catchUno lets any player call out someone sitting on exactly one card who
// hasn't declared UNO, penalizing them two cards.
func (g *gameState) catchUno(targetID string, nameOf func(string) string) (ok bool, errMsg string) {
	hand, ok := g.hands[targetID]
	if !ok {
		return false, "that player isn't in this game"
	}
	if len(hand) != 1 {
		return false, "that player doesn't need to call UNO"
	}
	if g.unoCalled[targetID] {
		return false, "they already called UNO"
	}
	g.hands[targetID] = append(g.hands[targetID], g.draw(2)...)
	g.unoCalled[targetID] = true // safe now - they've paid the penalty
	g.addLog(fmt.Sprintf("%s got caught without calling UNO - draws 2", nameOf(targetID)))
	return true, ""
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
	idx := -1
	for i, pid := range g.order {
		if pid == playerID {
			idx = i
			break
		}
	}
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
