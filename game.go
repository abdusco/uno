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

// newDeck builds a standard 108-card UNO deck: per color one 0, two each of
// 1-9, two skips, two reverses, two draw-twos (4*25=100), plus 4 wilds and
// 4 wild-draw-fours (8) = 108.
func newDeck() []Card {
	deck := make([]Card, 0, 108)
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
}

func startGame(order []string, nameOf func(string) string) *gameState {
	g := &gameState{
		deck:      newDeck(),
		hands:     make(map[string][]Card),
		order:     append([]string{}, order...),
		direction: 1,
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
		"wild": "Wild", "wild4": "Wild Draw Four",
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
	}
	g.addLog(logLine)

	if len(g.hands[playerID]) == 0 {
		g.winnerID = playerID
		return true, ""
	}

	g.turnIdx = mod(g.turnIdx+g.direction*(1+skip), n)
	return true, ""
}

// drawCard gives the current player one card and ends their turn. This is a
// deliberate simplification (no forced-play-if-drawable, no draw stacking)
// to keep the ruleset small for a casual game with friends.
func (g *gameState) drawCard(playerID string, nameOf func(string) string) (ok bool, errMsg string) {
	if g.currentPlayer() != playerID {
		return false, "it's not your turn"
	}
	g.hands[playerID] = append(g.hands[playerID], g.draw(1)...)
	g.addLog(fmt.Sprintf("%s drew a card", nameOf(playerID)))
	g.turnIdx = mod(g.turnIdx+g.direction, len(g.order))
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
	if len(g.order) == 0 {
		return
	}
	if idx < g.turnIdx || (idx == g.turnIdx && !wasCurrent) {
		g.turnIdx--
	}
	g.turnIdx = mod(g.turnIdx, len(g.order))
}
