package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nameOfStub(id string) string { return id }

func assertIllegalMove(t *testing.T, err error, message string) {
	t.Helper()
	illegalMove, ok := errors.AsType[ErrIllegalMove](err)
	require.True(t, ok, "error should be an ErrIllegalMove: %v", err)
	assert.Equal(t, message, illegalMove.Message)
}

func TestNewDeck(t *testing.T) {
	deck := newDeck()
	require.Len(t, deck, 112)

	counts := map[string]int{}
	for _, c := range deck {
		counts[c.Color+":"+c.Value]++
	}

	tests := []struct {
		name string
		key  string
		want int
	}{
		{"one red zero", "red:0", 1},
		{"two red sevens", "red:7", 2},
		{"two yellow skips", "yellow:skip", 2},
		{"two green reverses", "green:reverse", 2},
		{"two blue draw twos", "blue:draw2", 2},
		{"four wilds", "wild:wild", 4},
		{"four wild draw fours", "wild:wild4", 4},
		{"four color bombs", "wild:colorbomb", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, counts[tt.key])
		})
	}
}

func TestCardLabel(t *testing.T) {
	tests := []struct {
		name string
		card Card
		want string
	}{
		{"number card", Card{Color: "red", Value: "7"}, "Red 7"},
		{"skip", Card{Color: "yellow", Value: "skip"}, "Yellow Skip"},
		{"reverse", Card{Color: "green", Value: "reverse"}, "Green Reverse"},
		{"draw two", Card{Color: "blue", Value: "draw2"}, "Blue Draw Two"},
		{"wild", Card{Color: "wild", Value: "wild"}, "Wild"},
		{"wild draw four", Card{Color: "wild", Value: "wild4"}, "Wild Draw Four"},
		{"color bomb", Card{Color: "wild", Value: "colorbomb"}, "Color Bomb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cardLabel(tt.card))
		})
	}
}

func TestIsPlayable(t *testing.T) {
	g := &gameState{
		discard:  []Card{{ID: "top", Color: "red", Value: "7"}},
		topColor: "red",
	}

	tests := []struct {
		name string
		card Card
		want bool
	}{
		{"matching color", Card{Color: "red", Value: "3"}, true},
		{"matching value", Card{Color: "blue", Value: "7"}, true},
		{"no match", Card{Color: "blue", Value: "3"}, false},
		{"wild always playable", Card{Color: "wild", Value: "wild"}, true},
		{"wild4 always playable", Card{Color: "wild", Value: "wild4"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, g.isPlayable(tt.card))
		})
	}
}

// newTestGame builds a minimal 3-player gameState with a known hand for
// "p1" so playCard scenarios are deterministic.
func newTestGame(p1Hand []Card) *gameState {
	return &gameState{
		hands: map[string][]Card{
			"p1": p1Hand,
			"p2": {{ID: "p2c1", Color: "red", Value: "1"}},
			"p3": {{ID: "p3c1", Color: "red", Value: "1"}},
		},
		order:     []string{"p1", "p2", "p3"},
		turnIdx:   0,
		direction: 1,
		discard:   []Card{{ID: "top", Color: "red", Value: "5"}},
		topColor:  "red",
		deck:      []Card{{ID: "d1", Color: "blue", Value: "9"}, {ID: "d2", Color: "blue", Value: "8"}},
		unoCalled: make(map[string]bool),
	}
}

func TestPlayCard(t *testing.T) {
	t.Run("rejects when it's not your turn", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		err := g.playCard("p2", "p2c1", "", nameOfStub)
		assertIllegalMove(t, err, "it's not your turn")
	})

	t.Run("rejects a card the player doesn't hold", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		err := g.playCard("p1", "not-a-card", "", nameOfStub)
		assertIllegalMove(t, err, "you don't have that card")
	})

	t.Run("rejects a card that doesn't match the discard pile", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "blue", Value: "3"}})
		err := g.playCard("p1", "c1", "", nameOfStub)
		assertIllegalMove(t, err, "that card doesn't match the discard pile")
	})

	t.Run("rejects a wild with no color chosen", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "wild", Value: "wild"}})
		err := g.playCard("p1", "c1", "", nameOfStub)
		assertIllegalMove(t, err, "pick a color for the wild card")
	})

	// filler is an unrelated card kept in p1's hand so that playing the
	// card under test doesn't also trigger the empty-hand win condition,
	// which would short-circuit before the turn-advance logic runs.
	filler := Card{ID: "filler", Color: "blue", Value: "9"}

	t.Run("plays a normal number card and advances one seat", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}, filler})
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		assert.Equal(t, "p2", g.currentPlayer())
		assert.Equal(t, "red", g.topColor)
		assert.Equal(t, []Card{filler}, g.hands["p1"])
	})

	t.Run("skip passes over the next player", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "skip"}, filler})
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		assert.Equal(t, "p3", g.currentPlayer())
	})

	t.Run("reverse flips direction with 3+ players", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "reverse"}, filler})
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		assert.Equal(t, -1, g.direction)
		assert.Equal(t, "p3", g.currentPlayer())
	})

	t.Run("reverse behaves like skip with exactly 2 players", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "reverse"}, filler})
		g.order = []string{"p1", "p2"}
		delete(g.hands, "p3")
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		assert.Equal(t, 1, g.direction)
		assert.Equal(t, "p1", g.currentPlayer())
	})

	t.Run("draw2 gives the next player two cards and skips them", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "draw2"}, filler})
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		assert.Len(t, g.hands["p2"], 3) // 1 starting + 2 drawn
		assert.Equal(t, "p3", g.currentPlayer())
	})

	t.Run("wild4 gives the next player four cards and skips them", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "wild", Value: "wild4"}, filler})
		g.deck = []Card{
			{ID: "d1"}, {ID: "d2"}, {ID: "d3"}, {ID: "d4"},
		}
		require.NoError(t, g.playCard("p1", "c1", "blue", nameOfStub))
		assert.Len(t, g.hands["p2"], 5) // 1 starting + 4 drawn
		assert.Equal(t, "blue", g.topColor)
		assert.Equal(t, "p3", g.currentPlayer())
	})

	t.Run("colorbomb dumps every remaining card of the chosen color", func(t *testing.T) {
		g := newTestGame([]Card{
			{ID: "c1", Color: "wild", Value: "colorbomb"},
			{ID: "keep1", Color: "blue", Value: "4"},
			{ID: "dump1", Color: "green", Value: "2"},
			{ID: "dump2", Color: "green", Value: "skip"},
		})
		require.NoError(t, g.playCard("p1", "c1", "green", nameOfStub))
		assert.Equal(t, "green", g.topColor)
		assert.Equal(t, []Card{{ID: "keep1", Color: "blue", Value: "4"}}, g.hands["p1"])
		assert.Contains(t, g.discard, Card{ID: "dump1", Color: "green", Value: "2"})
		assert.Contains(t, g.discard, Card{ID: "dump2", Color: "green", Value: "skip"})
		assert.Equal(t, "p2", g.currentPlayer())
	})

	t.Run("colorbomb with nothing else to dump just clears the hand normally", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "wild", Value: "colorbomb"}, filler})
		require.NoError(t, g.playCard("p1", "c1", "green", nameOfStub))
		assert.Equal(t, []Card{filler}, g.hands["p1"])
	})

	t.Run("colorbomb that empties the hand wins the game", func(t *testing.T) {
		g := newTestGame([]Card{
			{ID: "c1", Color: "wild", Value: "colorbomb"},
			{ID: "dump1", Color: "green", Value: "2"},
		})
		require.NoError(t, g.playCard("p1", "c1", "green", nameOfStub))
		assert.Equal(t, "p1", g.winnerID)
	})

	t.Run("wild sets the chosen color", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "wild", Value: "wild"}, filler})
		require.NoError(t, g.playCard("p1", "c1", "green", nameOfStub))
		assert.Equal(t, "green", g.topColor)
		assert.Equal(t, "p2", g.currentPlayer())
	})

	t.Run("emptying your hand wins the game", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		g.hands["p1"] = []Card{{ID: "c1", Color: "red", Value: "3"}}
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		assert.Equal(t, "p1", g.winnerID)
	})

	t.Run("dropping to one card preserves a call made before the play", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}, filler})
		g.unoCalled["p1"] = true
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		require.Len(t, g.hands["p1"], 1)
		assert.True(t, g.unoCalled["p1"])
		assert.Empty(t, g.unoCatchableID)
	})

	t.Run("dropping to one card without calling opens the catch window", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}, filler})
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		assert.False(t, g.unoCalled["p1"])
		assert.Equal(t, "p1", g.unoCatchableID)
	})
}

func TestCallUno(t *testing.T) {
	t.Run("can call with two cards before playing", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}, {ID: "c2"}})
		require.NoError(t, g.callUno("p1", nameOfStub))
		assert.True(t, g.unoCalled["p1"])
	})

	t.Run("can call with one card during the catch window", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}})
		g.unoCatchableID = "p1"
		require.NoError(t, g.callUno("p1", nameOfStub))
		assert.True(t, g.unoCalled["p1"])
		assert.Empty(t, g.unoCatchableID)
	})

	t.Run("cannot call with one card after the window closes", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}})
		err := g.callUno("p1", nameOfStub)
		assertIllegalMove(t, err, "you don't need to call UNO yet")
	})

	t.Run("cannot call with three or more cards", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}, {ID: "c2"}, {ID: "c3"}})
		err := g.callUno("p1", nameOfStub)
		assertIllegalMove(t, err, "you don't need to call UNO yet")
	})

	t.Run("rejects a player who isn't in the game", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}})
		err := g.callUno("ghost", nameOfStub)
		assertIllegalMove(t, err, "you're not in this game")
	})
}

func TestCatchUno(t *testing.T) {
	t.Run("penalizes a player sitting on one card who hasn't called", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}})
		g.unoCatchableID = "p1"
		require.NoError(t, g.catchUno("p2", "p1", nameOfStub))
		assert.Len(t, g.hands["p1"], 3) // 1 + 2 penalty cards
		assert.Empty(t, g.unoCatchableID)
	})

	t.Run("rejects catching someone who already called UNO", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}})
		g.unoCalled["p1"] = true
		g.unoCatchableID = "p1"
		err := g.catchUno("p2", "p1", nameOfStub)
		assertIllegalMove(t, err, "they already called UNO")
		assert.Len(t, g.hands["p1"], 1, "no penalty applied")
	})

	t.Run("rejects catching a player who doesn't have exactly one card", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}, {ID: "c2"}})
		err := g.catchUno("p2", "p1", nameOfStub)
		assertIllegalMove(t, err, "that player doesn't need to call UNO")
	})

	t.Run("rejects a target who isn't in the game", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}})
		err := g.catchUno("p2", "ghost", nameOfStub)
		assertIllegalMove(t, err, "that player isn't in this game")
	})

	t.Run("rejects catching yourself", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1"}})
		g.unoCatchableID = "p1"
		err := g.catchUno("p1", "p1", nameOfStub)
		assertIllegalMove(t, err, "you can't catch yourself")
	})
}

func TestDrawCard(t *testing.T) {
	t.Run("the next player's draw closes the UNO catch window", func(t *testing.T) {
		g := newTestGame([]Card{
			{ID: "c1", Color: "red", Value: "3"},
			{ID: "c2", Color: "blue", Value: "4"},
		})
		require.NoError(t, g.playCard("p1", "c1", "", nameOfStub))
		require.Equal(t, "p1", g.unoCatchableID)

		_, err := g.drawCard("p2", nameOfStub)
		require.NoError(t, err)
		assert.Empty(t, g.unoCatchableID)
	})

	t.Run("rejects when it's not your turn", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		_, err := g.drawCard("p2", nameOfStub)
		assertIllegalMove(t, err, "it's not your turn")
	})

	t.Run("adds one card and leaves the turn pending a play-or-pass decision", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		drawn, err := g.drawCard("p1", nameOfStub)
		require.NoError(t, err)
		assert.Equal(t, "d2", drawn.ID) // draw() pops from the end of the deck
		assert.Len(t, g.hands["p1"], 2)
		assert.Equal(t, "p1", g.currentPlayer(), "turn doesn't advance until the player decides")
		assert.True(t, g.drawPending)
	})

	t.Run("rejects a second draw before the first is resolved", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		_, err := g.drawCard("p1", nameOfStub)
		require.NoError(t, err)
		_, err = g.drawCard("p1", nameOfStub)
		assertIllegalMove(t, err, "you already drew a card - play it or keep it")
	})

	t.Run("rejects drawing when both the deck and discard pile are exhausted", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		g.deck = nil
		g.discard = []Card{{ID: "top", Color: "red", Value: "5"}}
		_, err := g.drawCard("p1", nameOfStub)
		assertIllegalMove(t, err, "no cards left to draw")
	})
}

func TestPassTurn(t *testing.T) {
	t.Run("rejects when it's not your turn", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		err := g.passTurn("p2")
		assertIllegalMove(t, err, "it's not your turn")
	})

	t.Run("rejects passing without having drawn first", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		err := g.passTurn("p1")
		assertIllegalMove(t, err, "you haven't drawn a card yet")
	})

	t.Run("ends the turn and clears the pending draw", func(t *testing.T) {
		g := newTestGame([]Card{{ID: "c1", Color: "red", Value: "3"}})
		_, err := g.drawCard("p1", nameOfStub)
		require.NoError(t, err)
		require.NoError(t, g.passTurn("p1"))
		assert.Equal(t, "p2", g.currentPlayer())
		assert.False(t, g.drawPending)
		assert.Nil(t, g.lastDrawnCard)
	})
}

func TestReshuffle(t *testing.T) {
	t.Run("moves everything but the top card back into the deck", func(t *testing.T) {
		g := &gameState{
			discard: []Card{
				{ID: "d1", Color: "red", Value: "1"},
				{ID: "d2", Color: "blue", Value: "2"},
				{ID: "top", Color: "green", Value: "3"},
			},
		}
		g.reshuffle()
		assert.Len(t, g.deck, 2)
		require.Len(t, g.discard, 1)
		assert.Equal(t, "top", g.discard[0].ID)
	})

	t.Run("no-ops when the discard pile only has the top card", func(t *testing.T) {
		g := &gameState{discard: []Card{{ID: "top"}}}
		g.reshuffle()
		assert.Empty(t, g.deck)
		assert.Len(t, g.discard, 1)
	})
}

func TestDrawReshufflesWhenDeckIsEmpty(t *testing.T) {
	g := &gameState{
		deck: nil,
		discard: []Card{
			{ID: "d1", Color: "red", Value: "1"},
			{ID: "top", Color: "green", Value: "3"},
		},
	}
	drawn := g.draw(1)
	require.Len(t, drawn, 1)
	assert.Equal(t, "d1", drawn[0].ID)
	assert.Len(t, g.discard, 1, "top card stays on the discard pile")
}

func TestRemovePlayer(t *testing.T) {
	tests := []struct {
		name        string
		order       []string
		turnIdx     int
		removeID    string
		wantOrder   []string
		wantTurnIdx int
	}{
		{
			name:        "removing a player before the current turn shifts the index back",
			order:       []string{"p1", "p2", "p3"},
			turnIdx:     2,
			removeID:    "p1",
			wantOrder:   []string{"p2", "p3"},
			wantTurnIdx: 1,
		},
		{
			name:        "removing the current player keeps the index pointing at the next one",
			order:       []string{"p1", "p2", "p3"},
			turnIdx:     1,
			removeID:    "p2",
			wantOrder:   []string{"p1", "p3"},
			wantTurnIdx: 1,
		},
		{
			name:        "removing the last player in turn order wraps back to the start",
			order:       []string{"p1", "p2", "p3"},
			turnIdx:     2,
			removeID:    "p3",
			wantOrder:   []string{"p1", "p2"},
			wantTurnIdx: 0,
		},
		{
			name:        "removing a player after the current turn doesn't move the index",
			order:       []string{"p1", "p2", "p3"},
			turnIdx:     0,
			removeID:    "p3",
			wantOrder:   []string{"p1", "p2"},
			wantTurnIdx: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &gameState{
				order:   append([]string{}, tt.order...),
				turnIdx: tt.turnIdx,
				hands:   map[string][]Card{"p1": {}, "p2": {}, "p3": {}},
			}
			g.removePlayer(tt.removeID)
			assert.Equal(t, tt.wantOrder, g.order)
			assert.Equal(t, tt.wantTurnIdx, g.turnIdx)
			assert.NotContains(t, g.hands, tt.removeID)
		})
	}
}
