package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"aisotropy.local/bench/streaming/internal/tokens"
)

// wsHandler streams the deterministic token stub over a WebSocket. Each token is
// one text frame. Cancellation: reading is not required for the server to notice
// a gone client — a failed write cancels the stub — but we also start a reader
// goroutine so a client-initiated close frame propagates promptly to ctx.
func (s *server) wsHandler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Bench is same-origin/localhost; skip origin check for the load tool.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	stream := streamFromQuery(r)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reader pump: surfaces client close / disconnect as ctx cancellation.
	go func() {
		for {
			if _, _, rerr := c.Read(ctx); rerr != nil {
				cancel()
				return
			}
		}
	}()

	s.metrics.open()
	completed := false
	defer func() { s.metrics.close(completed) }()

	buf := make([]byte, 0, 64)
	err = stream.Emit(ctx, func(t tokens.Token) error {
		buf = buf[:0]
		buf = strconv.AppendInt(buf, int64(t.Index), 10)
		buf = append(buf, '|')
		buf = append(buf, t.Text...)
		buf = append(buf, '|')
		buf = strconv.AppendInt(buf, time.Now().UnixNano(), 10)
		if werr := c.Write(ctx, websocket.MessageText, buf); werr != nil {
			return werr
		}
		s.metrics.tokensSent.Add(1)
		return nil
	})
	if err == nil {
		completed = true
		c.Close(websocket.StatusNormalClosure, "done")
	}
}
