package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
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
	e.Use(middleware.Logger())
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
	defer conn.CloseNow()

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
		_ = wsjson.Write(ctx, conn, outMsg{Type: "error", Message: "expected hello with a name"})
		conn.Close(websocket.StatusPolicyViolation, "bad hello")
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
			_ = wsjson.Write(ctx, conn, outMsg{Type: "error", Message: "room not found"})
			conn.Close(websocket.StatusNormalClosure, "room not found")
			return nil
		}
	}

	joinResultCh := make(chan *joinResult, 1)
	rm.joinCh <- &joinReq{name: first.Name, asHost: first.Create, result: joinResultCh}
	jr := <-joinResultCh
	if jr.err != "" {
		_ = wsjson.Write(ctx, conn, outMsg{Type: "error", Message: jr.err})
		conn.Close(websocket.StatusNormalClosure, jr.err)
		return nil
	}
	p := jr.player

	// Pump outgoing messages from the room to this socket. This exits once
	// p.send is closed, which the room does inside handleLeave.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range p.send {
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
		rm.actionCh <- roomAction{playerID: p.id, msg: m}
	}

	// Tell the room we're gone. This must happen before waiting on `done`:
	// handleLeave is what closes p.send, and the writer goroutine above
	// can't exit its range loop until that close happens. Doing this as a
	// deferred call instead would deadlock, since the defer wouldn't run
	// until after the wait below already unblocked.
	rm.leaveCh <- p.id
	<-done
	return nil
}
