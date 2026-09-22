package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

//go:embed web
var embeddedWeb embed.FS

func main() {
	var webFS fs.FS
	if os.Getenv("DEBUG") == "1" {
		log.Print("DEBUG=1: serving web/ from disk, no rebuild needed for frontend changes")
		webFS = os.DirFS("web")
	} else {
		sub, err := fs.Sub(embeddedWeb, "web")
		if err != nil {
			log.Fatal(err)
		}
		webFS = sub
	}

	reg := newRegistry()
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())

	// Websocket endpoint must be registered before the SPA static
	// fallback so it isn't swallowed by the HTML5 catch-all.
	e.GET("/ws", func(c echo.Context) error {
		return handleWS(c, reg)
	})

	e.GET("/*", echo.WrapHandler(http.FileServer(http.FS(webFS))), staticFallbackMiddleware(webFS))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("uno-party listening on :%s", port)
	if err := e.Start(":" + port); err != nil {
		log.Fatal(err)
	}
}

// staticFallbackMiddleware serves index.html for any path that isn't a real
// file in webFS (e.g. /r/ABCDE), so client-side routing works on refresh.
func staticFallbackMiddleware(webFS fs.FS) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := c.Request().URL.Path
			if p == "/" {
				p = "/index.html"
			}
			if f, err := webFS.Open(p[1:]); err == nil {
				_ = f.Close()
				return next(c)
			}
			c.Request().URL.Path = "/"
			return next(c)
		}
	}
}

// --- websocket -------------------------------------------------------

// clientMsg mirrors inMsg but is decoded straight off the wire.
type clientMsg struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Room     string `json:"room"`
	Create   bool   `json:"create"`
	RoomName string `json:"roomName"`
	// Token, if the client has one cached from a previous "joined" message,
	// lets it resume that identity instead of joining as someone new -
	// see joinReq.token in room.go.
	Token string `json:"token"`
}

func handleWS(c echo.Context, reg *registry) error {
	w := c.Response()
	r := c.Request()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Any origin is allowed - this is a small self-hosted app with no
		// cookies/credentials to protect, and it's meant to be played from
		// whatever device a friend opens the shared link on.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return err
	}
	ctx := r.Context()
	// Every exit path below closes conn exactly once, however it gets
	// there - a graceful close with a status/reason if one fires first, a
	// bare CloseNow() (via the deferred call, or via a reconnect kicking
	// this same connection from the room goroutine) otherwise. Without this
	// guard, e.g. the "room not found" graceful close below followed by
	// the deferred CloseNow() double-closes the same conn, which the
	// browser reports as "WebSocket ... failed: Close received after
	// close" - a real, easy-to-hit bug on practically any error path, not
	// just the reconnect-takeover case closeConn also serves.
	var closeOnce sync.Once
	closeConn := func(status websocket.StatusCode, reason string) {
		closeOnce.Do(func() { conn.Close(status, reason) })
	}
	defer closeOnce.Do(func() { conn.CloseNow() })

	// First message from the client must declare intent: create a room,
	// or join one by code, along with the player's display name.
	var first clientMsg
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = wsjson.Read(readCtx, conn, &first)
	cancel()
	if err != nil {
		return nil
	}
	if first.Type != "hello" || first.Name == "" {
		writeClientError(ctx, conn, ErrProtocolViolation{Message: "expected hello with a name"})
		closeConn(websocket.StatusPolicyViolation, "bad hello")
		return nil
	}

	var rm *room
	if first.Create {
		name := strings.TrimSpace(first.RoomName)
		if name == "" {
			name = first.Name + "'s game"
		}
		rm = reg.create(name)
	} else {
		rm = reg.get(first.Room)
		if rm == nil {
			writeClientError(ctx, conn, ErrRoomNotFound{})
			closeConn(websocket.StatusNormalClosure, "room not found")
			return nil
		}
	}

	joinResultCh := make(chan *joinResult, 1)
	join := &joinReq{
		name: first.Name, asHost: first.Create, token: first.Token,
		kick:   func() { closeOnce.Do(func() { conn.CloseNow() }) },
		result: joinResultCh,
	}
	select {
	case rm.joinCh <- join:
	case <-rm.doneCh:
		writeClientError(ctx, conn, ErrRoomNotFound{})
		closeConn(websocket.StatusNormalClosure, "room not found")
		return nil
	}
	jr := <-joinResultCh
	if jr.err != nil {
		writeClientError(ctx, conn, jr.err)
		closeConn(websocket.StatusNormalClosure, jr.err.Error())
		return nil
	}
	p := jr.player

	// Pump outgoing messages from the room to this socket. This exits once
	// jr.sendCh is closed, which the room does inside handleLeave. Must use
	// jr.sendCh here, not p.send - the room goroutine may replace p.send on
	// a future reconnect, and reading that field from this goroutine after
	// the initial handoff would be an unsynchronized access.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range jr.sendCh {
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(writeCtx, conn, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}()

	// Pump incoming messages from this socket to the room.
	for {
		var m inMsg
		err := wsjson.Read(ctx, conn, &m)
		if err != nil {
			break
		}
		select {
		case rm.actionCh <- roomAction{playerID: p.id, msg: m}:
		case <-rm.doneCh:
			return nil
		}
	}

	// Tell the room this connection is gone. This must happen before
	// waiting on `done`: handleLeave is what closes jr.sendCh, and the
	// writer goroutine above can't exit its range loop until that close
	// happens. Doing this as a deferred call instead would deadlock, since
	// the defer wouldn't run until after the wait below already unblocked.
	// connGen scopes the signal to this specific connection, so if a
	// reconnect has already superseded it, the room ignores it instead of
	// tearing down the session that took over.
	select {
	case rm.leaveCh <- leaveReq{playerID: p.id, connGen: jr.connGen}:
	case <-rm.doneCh:
		return nil
	}
	<-done
	return nil
}

func writeClientError(ctx context.Context, conn *websocket.Conn, err error) {
	code, message := clientErrorDetails(err)
	_ = wsjson.Write(ctx, conn, outMsg{Type: "error", Code: code, Message: message})
}
