// server_boot.go boots aisotropy's REAL HTTP runtime (App.Run) on an ephemeral
// loopback port so the `tools` and `stream` modes can drive the exact production
// routes — POST /v1/.../chat/agent (real tool execution, ADR-0012) and POST
// /v1/.../chat/stream (real SSE) — that App.Turn cannot express. The server runs
// with NoAuth (allowed because the listener is loopback-only) so the adapter does
// not need to mint a JWT; identity still travels via the path params and every
// memory tier is scoped from it. The agnes API key stays host-side in the runtime
// process; it is never placed in a URL or logged.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// bootedServer is a running aisotropy HTTP runtime plus the handle to stop it.
type bootedServer struct {
	baseURL string
	cancel  context.CancelFunc
	done    chan error
}

// freeLoopbackAddr reserves an ephemeral loopback port and returns it as
// "127.0.0.1:PORT". The listener is closed immediately; App.Run rebinds it. The
// small race window is acceptable for a single-shot benchmark adapter.
func freeLoopbackAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("reserving ephemeral port: %w", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr, nil
}

// bootServer builds the App with the given extra options (registered tools,
// executor, persona), runs it on an ephemeral loopback port, and blocks until the
// /healthz liveness endpoint responds. Call stop() (returned) to shut it down.
func bootServer(agnes *agnesClient, persona string, extra ...aisotropy.Option) (*bootedServer, error) {
	addr, err := freeLoopbackAddr()
	if err != nil {
		return nil, err
	}
	opts := append([]aisotropy.Option{
		aisotropy.WithAddr(addr),
		aisotropy.WithAuthenticator(aisotropy.NoAuth()),
	}, extra...)
	app := newApp(agnes, persona, opts...)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	baseURL := "http://" + addr
	if err := waitHealthy(baseURL, 30*time.Second); err != nil {
		cancel()
		<-done
		return nil, err
	}
	logf("aisotropy runtime booted on %s (NoAuth, loopback)", baseURL)
	return &bootedServer{baseURL: baseURL, cancel: cancel, done: done}, nil
}

func (s *bootedServer) stop() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(12 * time.Second):
	}
}

func waitHealthy(baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("aisotropy runtime did not become healthy within %s", timeout)
}
