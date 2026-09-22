package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func joinRoom(t *testing.T, r *room, name string) *joinResult {
	t.Helper()
	resultCh := make(chan *joinResult, 1)
	r.joinCh <- &joinReq{name: name, asHost: false, result: resultCh}
	res := <-resultCh
	require.Empty(t, res.err)
	return res
}

func reconnectRoom(t *testing.T, r *room, token string) *joinResult {
	t.Helper()
	resultCh := make(chan *joinResult, 1)
	r.joinCh <- &joinReq{token: token, result: resultCh}
	res := <-resultCh
	require.Empty(t, res.err)
	return res
}

// leave sends a disconnect signal and then blocks until it's fully
// processed, via a throwaway synchronizing round-trip. Without this, a test
// that inspects room/game state right after calling leave races the room's
// own goroutine: the leaveCh send unblocks the instant it's *received*, not
// once handleLeave finishes running.
func leave(t *testing.T, r *room, jr *joinResult) {
	t.Helper()
	r.leaveCh <- leaveReq{playerID: jr.player.id, connGen: jr.connGen}
	syncRoom(t, r)
}

func syncRoom(t *testing.T, r *room) {
	t.Helper()
	done := make(chan struct{})
	r.syncCh <- done
	<-done
}

func drainMessages(ch chan outMsg) []outMsg {
	var messages []outMsg
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return messages
			}
			messages = append(messages, msg)
		default:
			return messages
		}
	}
}

func TestHandleJoinHostPromotion(t *testing.T) {
	t.Run("first joiner becomes host even though asHost is false", func(t *testing.T) {
		r := newRoom("TEST1", "Test Room")
		jr := joinRoom(t, r, "Alice")
		assert.True(t, jr.player.isHost)
	})

	t.Run("second joiner is not host while the first is still connected", func(t *testing.T) {
		r := newRoom("TEST2", "Test Room")
		joinRoom(t, r, "Alice")
		bob := joinRoom(t, r, "Bob")
		assert.False(t, bob.player.isHost)
	})

	t.Run("next joiner is promoted once the room has nobody with host status", func(t *testing.T) {
		r := newRoom("TEST3", "Test Room")
		alice := joinRoom(t, r, "Alice")
		leave(t, r, alice)
		bob := joinRoom(t, r, "Bob")
		assert.True(t, bob.player.isHost, "room was left hostless after its only player disconnected")
	})

	t.Run("a merely-disconnected host still blocks promotion once they're back", func(t *testing.T) {
		// Regression test for a "two hosts at once" bug: a disconnected
		// player is no longer deleted (needed so reconnect can resume
		// them), so promotion must check connectedness, not presence.
		r := newRoom("TEST4", "Test Room")
		alice := joinRoom(t, r, "Alice")
		leave(t, r, alice)
		bob := joinRoom(t, r, "Bob")
		require.True(t, bob.player.isHost)

		resumed := reconnectRoom(t, r, alice.player.token)
		assert.False(t, resumed.player.isHost, "the promoted host keeps the role on the original host's return")
		assert.True(t, bob.player.isHost)
	})

	t.Run("promoted host is announced during a game", func(t *testing.T) {
		r := newRoom("TEST5", "Test Room")
		alice := joinRoom(t, r, "Alice")
		bob := joinRoom(t, r, "Bob")
		r.status = "playing"
		r.game = startGame([]string{alice.player.id, bob.player.id}, r.nameOf)
		drainMessages(bob.sendCh)

		leave(t, r, alice)

		var promoted bool
		for _, msg := range drainMessages(bob.sendCh) {
			if msg.Type != "players" {
				continue
			}
			for _, player := range msg.Players {
				if player.ID == bob.player.id && player.IsHost {
					promoted = true
				}
			}
		}
		assert.True(t, promoted, "the promoted client must learn it can start the rematch")
	})
}

func TestReconnect(t *testing.T) {
	t.Run("restores the same id and host status in the lobby", func(t *testing.T) {
		r := newRoom("TEST5", "Test Room")
		alice := joinRoom(t, r, "Alice")
		leave(t, r, alice)

		resumed := reconnectRoom(t, r, alice.player.token)
		assert.True(t, resumed.reconnected)
		assert.Same(t, alice.player, resumed.player)
		assert.True(t, resumed.player.isHost)
		assert.True(t, resumed.player.connected)
	})

	t.Run("restores the same hand and turn slot mid-game", func(t *testing.T) {
		r := newRoom("TEST6", "Test Room")
		alice := joinRoom(t, r, "Alice")
		bob := joinRoom(t, r, "Bob")
		r.status = "playing"
		r.game = startGame([]string{alice.player.id, bob.player.id}, r.nameOf)
		handBefore := append([]Card(nil), r.game.hands[alice.player.id]...)

		leave(t, r, alice)
		require.False(t, r.players[alice.player.id].connected)
		require.Equal(t, handBefore, r.game.hands[alice.player.id], "hand must survive a mere disconnect")

		resumed := reconnectRoom(t, r, alice.player.token)
		assert.True(t, resumed.reconnected)
		assert.Equal(t, handBefore, r.game.hands[alice.player.id])
		assert.Contains(t, r.game.order, alice.player.id)
	})

	t.Run("unknown token falls back to a fresh join", func(t *testing.T) {
		r := newRoom("TEST7", "Test Room")
		joinRoom(t, r, "Alice")

		resultCh := make(chan *joinResult, 1)
		r.joinCh <- &joinReq{name: "Mallory", token: "not-a-real-token", result: resultCh}
		res := <-resultCh
		require.Empty(t, res.err)
		assert.False(t, res.reconnected)
		assert.Equal(t, "Mallory", res.player.name)
	})

	t.Run("a stale leave signal from a superseded connection is a no-op", func(t *testing.T) {
		r := newRoom("TEST8", "Test Room")
		alice := joinRoom(t, r, "Alice")
		leave(t, r, alice)                                 // alice disconnects
		resumed := reconnectRoom(t, r, alice.player.token) // and reconnects, bumping connGen

		// The original connection's leave signal finally arrives, late,
		// carrying the old (now superseded) connGen.
		leave(t, r, alice)

		assert.True(t, r.players[alice.player.id].connected, "the resumed session must survive a stale leave")
		assert.Equal(t, resumed.connGen, r.players[alice.player.id].connGen)
	})
}

func TestRoomPlayerLimit(t *testing.T) {
	r := newRoom("FULL1", "Full Room")
	var first *joinResult
	for i := 0; i < maxRoomPlayers; i++ {
		joined := joinRoom(t, r, "Player")
		if i == 0 {
			first = joined
		}
	}

	resultCh := make(chan *joinResult, 1)
	r.joinCh <- &joinReq{name: "One too many", result: resultCh}
	assert.Equal(t, "this room is full", (<-resultCh).err)

	leave(t, r, first)
	resumed := reconnectRoom(t, r, first.player.token)
	assert.True(t, resumed.reconnected, "a reserved seat must remain reconnectable when the room is full")
}

func TestRegistryExpiresEmptyRooms(t *testing.T) {
	reg := newRegistry()
	reg.idleTTL = 20 * time.Millisecond
	r := reg.create("Short lived")
	joined := joinRoom(t, r, "Alice")

	select {
	case <-r.doneCh:
		t.Fatal("room expired while a player was connected")
	case <-time.After(3 * reg.idleTTL):
	}

	leave(t, r, joined)
	select {
	case <-r.doneCh:
	case <-time.After(time.Second):
		t.Fatal("empty room did not expire")
	}
	assert.Nil(t, reg.get(r.id), "expired room must be removed from the registry")
}

func TestAutoSkipDisconnected(t *testing.T) {
	t.Run("disconnecting the current player advances the turn immediately", func(t *testing.T) {
		r := newRoom("TEST9", "Test Room")
		alice := joinRoom(t, r, "Alice")
		bob := joinRoom(t, r, "Bob")
		carol := joinRoom(t, r, "Carol")
		r.status = "playing"
		r.game = startGame([]string{alice.player.id, bob.player.id, carol.player.id}, r.nameOf)
		require.Equal(t, alice.player.id, r.game.currentPlayer())

		leave(t, r, alice)
		assert.Equal(t, bob.player.id, r.game.currentPlayer())
	})

	t.Run("a chain of consecutive disconnected seats is skipped in one pass", func(t *testing.T) {
		// Four players, not three: disconnecting two of them must still
		// leave two connected (carol and dave), so this exercises a
		// multi-seat skip without also tripping the separate "one
		// connected player left" auto-win path.
		r := newRoom("TEST10", "Test Room")
		alice := joinRoom(t, r, "Alice")
		bob := joinRoom(t, r, "Bob")
		carol := joinRoom(t, r, "Carol")
		dave := joinRoom(t, r, "Dave")
		r.status = "playing"
		r.game = startGame([]string{alice.player.id, bob.player.id, carol.player.id, dave.player.id}, r.nameOf)
		require.Equal(t, alice.player.id, r.game.currentPlayer())

		leave(t, r, bob) // disconnect the next-in-line seat first
		leave(t, r, alice)
		assert.Equal(t, carol.player.id, r.game.currentPlayer())
		assert.Empty(t, r.game.winnerID, "two players are still connected - the round isn't over")
	})

	t.Run("dropping to one connected player freezes the round instead of ending it", func(t *testing.T) {
		r := newRoom("TEST11", "Test Room")
		alice := joinRoom(t, r, "Alice")
		bob := joinRoom(t, r, "Bob")
		carol := joinRoom(t, r, "Carol")
		r.status = "playing"
		r.game = startGame([]string{alice.player.id, bob.player.id, carol.player.id}, r.nameOf)
		turnBefore := r.game.currentPlayer()

		leave(t, r, bob)
		leave(t, r, carol)
		assert.Empty(t, r.game.winnerID, "the round pauses, it doesn't declare a winner")
		assert.Equal(t, "playing", r.status)
		assert.Equal(t, turnBefore, r.game.currentPlayer(), "the turn freezes wherever it was")
		assert.Equal(t, 3, len(r.game.order), "disconnecting must not prune anyone from the game")
	})

	t.Run("game actions are rejected while fewer than two players are connected", func(t *testing.T) {
		r := newRoom("TEST12", "Test Room")
		alice := joinRoom(t, r, "Alice")
		bob := joinRoom(t, r, "Bob")
		r.status = "playing"
		r.game = startGame([]string{alice.player.id, bob.player.id}, r.nameOf)
		current := r.game.currentPlayer()

		leave(t, r, bob) // only one player (whoever's left) is connected now
		deckBefore := len(r.game.deck)

		r.actionCh <- roomAction{playerID: current, msg: inMsg{Type: "draw"}}
		syncRoom(t, r)

		assert.Equal(t, current, r.game.currentPlayer(), "a blocked draw must not advance the turn")
		assert.Equal(t, deckBefore, len(r.game.deck), "a blocked draw must not actually draw a card")
	})

	t.Run("reconnecting resumes a frozen round without ever having ended it", func(t *testing.T) {
		r := newRoom("TEST13", "Test Room")
		alice := joinRoom(t, r, "Alice")
		bob := joinRoom(t, r, "Bob")
		carol := joinRoom(t, r, "Carol")
		r.status = "playing"
		r.game = startGame([]string{alice.player.id, bob.player.id, carol.player.id}, r.nameOf)

		leave(t, r, bob)
		leave(t, r, carol)
		require.Empty(t, r.game.winnerID)

		reconnectRoom(t, r, bob.player.token)
		assert.Empty(t, r.game.winnerID, "still only two connected - stays frozen")

		reconnectRoom(t, r, carol.player.token)
		assert.Empty(t, r.game.winnerID)
		assert.Equal(t, "playing", r.status, "the round continues rather than having ended")
	})
}
