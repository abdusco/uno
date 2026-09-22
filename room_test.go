package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func joinRoom(t *testing.T, r *room, name string) *player {
	t.Helper()
	resultCh := make(chan *joinResult, 1)
	r.joinCh <- &joinReq{name: name, asHost: false, result: resultCh}
	res := <-resultCh
	require.Empty(t, res.err)
	return res.player
}

func TestHandleJoinHostPromotion(t *testing.T) {
	t.Run("first joiner becomes host even though asHost is false", func(t *testing.T) {
		r := newRoom("TEST1", "Test Room")
		p := joinRoom(t, r, "Alice")
		assert.True(t, p.isHost)
	})

	t.Run("second joiner is not host while the first is still connected", func(t *testing.T) {
		r := newRoom("TEST2", "Test Room")
		joinRoom(t, r, "Alice")
		p2 := joinRoom(t, r, "Bob")
		assert.False(t, p2.isHost)
	})

	t.Run("next joiner is promoted once the room has nobody with host status", func(t *testing.T) {
		r := newRoom("TEST3", "Test Room")
		p1 := joinRoom(t, r, "Alice")
		r.leaveCh <- p1.id
		p2 := joinRoom(t, r, "Bob")
		assert.True(t, p2.isHost, "room was left hostless after its only player disconnected")
	})
}
