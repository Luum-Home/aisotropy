package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// runStream drives aisotropy's REAL SSE streaming route (POST /v1/.../chat/stream)
// and records time-to-first-token. It boots App.Run, opens the event stream, and
// concatenates the named "token" events into the reply, timing the first one.
// Token usage comes from the product's own usage meter (App.Usage() via the
// booted server's App — commit be945de; the streaming path traverses the same
// metered base client as every other provider call).
func runStream(req *request, out *result) error {
	prompt, err := lastUserMessage(req.Messages)
	if err != nil {
		return err
	}

	agnes, err := newProviderClient()
	if err != nil {
		return err
	}
	srv, err := bootServer(agnes, req.System)
	if err != nil {
		return err
	}
	defer srv.stop()

	streamBody := map[string]any{
		"prompt":     prompt,
		"product_id": productID,
		"session_id": sessionID(req),
	}
	body, _ := json.Marshal(streamBody)

	url := fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat/stream", srv.baseURL, arenaTenant, arenaUser)
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := (&http.Client{Timeout: 115 * time.Second}).Do(httpReq)
	if err != nil {
		return fmt.Errorf("POST /chat/stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/chat/stream returned status %d", resp.StatusCode)
	}

	var (
		reply        strings.Builder
		gotFirst     bool
		currentEvent string
	)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if currentEvent == "token" {
				var ev struct {
					Text string `json:"text"`
				}
				if json.Unmarshal([]byte(data), &ev) == nil && ev.Text != "" {
					if !gotFirst {
						gotFirst = true
						out.TTFTMs = time.Since(start).Milliseconds()
					}
					reply.WriteString(ev.Text)
				}
			}
		}
	}
	out.LatencyMs = time.Since(start).Milliseconds()
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading SSE stream: %w", err)
	}
	if !gotFirst {
		return fmt.Errorf("/chat/stream produced no token events")
	}

	out.Reply = reply.String()
	setUsage(out, srv.app)
	logf("stream: ttft=%dms, reply=%d chars", out.TTFTMs, reply.Len())
	return nil
}
