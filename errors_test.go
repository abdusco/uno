package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClientErrorDetails(t *testing.T) {
	t.Run("preserves a wrapped client error", func(t *testing.T) {
		code, message := clientErrorDetails(fmt.Errorf("join failed: %w", ErrRoomNotFound{}))
		assert.Equal(t, "room_not_found", code)
		assert.Equal(t, "room not found", message)
	})

	t.Run("redacts an internal error", func(t *testing.T) {
		code, message := clientErrorDetails(fmt.Errorf("database credentials leaked"))
		assert.Equal(t, "internal_error", code)
		assert.Equal(t, "something went wrong", message)
	})
}
