package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestMonitorWSClosesUnresponsivePeer(t *testing.T) {
	failed := make(chan struct{})
	exited := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer close(exited)
		defer conn.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go monitorWS(ctx, conn, 10*time.Millisecond, 30*time.Millisecond, func() {
			conn.CloseNow()
			close(failed)
		})
		var message inMsg
		_ = wsjson.Read(ctx, conn, &message)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	// No client reader: protocol pings cannot receive a pong.
	select {
	case <-failed:
	case <-ctx.Done():
		t.Fatal("heartbeat did not detect the unresponsive peer")
	}
	select {
	case <-exited:
	case <-ctx.Done():
		t.Fatal("heartbeat did not unblock the connection reader")
	}
}

func TestHandleWSRepliesToApplicationHeartbeat(t *testing.T) {
	reg := newRegistry()
	e := echo.New()
	e.GET("/ws", func(c echo.Context) error { return handleWS(c, reg) })
	server := httptest.NewServer(e)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	require.NoError(t, wsjson.Write(ctx, conn, clientMsg{Type: "hello", Name: "Alice", Create: true}))
	for _, want := range []string{"joined", "players"} {
		var msg outMsg
		require.NoError(t, wsjson.Read(ctx, conn, &msg))
		require.Equal(t, want, msg.Type)
	}
	require.NoError(t, wsjson.Write(ctx, conn, inMsg{Type: "ping"}))
	var reply outMsg
	require.NoError(t, wsjson.Read(ctx, conn, &reply))
	require.Equal(t, "pong", reply.Type)
}
