// Graceful-shutdown in-flight coverage (ADR-0076). http.Server.Shutdown drains
// idle connections but never cancels an in-flight request's context and never
// tells a handler its grace period expired. This file pins the close: after the
// drain window, Run cancels the request contexts and JOINS the handlers before
// any store is closed, so a still-streaming SSE handler can never write to
// working memory after BadgerDB shut down.
package aisotropy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// blockingClient is the pathological handler the old shutdown window could not
// reach: a model call that never returns on its own. It parks until its context
// is cancelled — which, before ADR-0076, only happened if the CLIENT hung up,
// never because the server was shutting down.
type blockingClient struct {
	entered   chan struct{} // closed once StreamChat is parked (the request is in-flight)
	cancelled chan struct{} // closed when the handler's ctx is cancelled
}

func newBlockingClient() *blockingClient {
	return &blockingClient{entered: make(chan struct{}), cancelled: make(chan struct{})}
}

func (c *blockingClient) Name() string { return "blocking-test-client" }

func (c *blockingClient) StreamChat(ctx context.Context, _, _ string) (<-chan llm.Token, error) {
	close(c.entered)
	<-ctx.Done()
	close(c.cancelled)
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}

// TestRun_ShutdownCancelsAndJoinsInFlightHandlers asserts the two halves of the
// fix together:
//
//  1. the in-flight handler's context IS cancelled by shutdown (before the fix
//     it was rooted at context.Background() and nothing cancelled it), and
//  2. Run does not return until that handler has unwound — so rt.close cannot
//     race the handler's memory writes.
//
// Before the fix, `cancelled` is never closed: the handler's context was rooted
// at context.Background(), so Shutdown waited out its whole grace period, gave
// up, and rt.close ran with the handler still parked — exactly the "best-effort
// window" capabilities-and-gaps.md documented.
//
// The grace period is shortened for the test rather than slept through. That is
// not the behavior under test: honoring the full window is CORRECT (a live
// stream deserves its drain time); what is under test is what happens when the
// window expires with a handler still running.
func TestRun_ShutdownCancelsAndJoinsInFlightHandlers(t *testing.T) {
	prevTimeout := shutdownTimeout
	shutdownTimeout = 500 * time.Millisecond
	t.Cleanup(func() { shutdownTimeout = prevTimeout })

	addr := freeAddr(t)
	client := newBlockingClient()
	app := New(
		WithAddr(addr),
		WithLLM(client),
		WithAuthenticator(NoAuth()),
		WithPersona("demo", Persona{BasePrompt: "You are a demo."}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	// Fire the streaming turn on its own goroutine and leave it hanging: the
	// handler parks inside the model call and the connection never goes idle.
	// reqCtx is deliberately NOT the Run ctx — a client-side cancellation would
	// unwind the handler for the wrong reason and mask the regression.
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		body := bytes.NewReader([]byte(`{"prompt":"hello","product_id":"demo","session_id":"s1"}`))
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			base+"/v1/tenant/acme/user/alice/chat/stream", body)
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	select {
	case <-client.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("the streaming handler never reached the model call; test cannot exercise shutdown")
	}

	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run never returned: shutdown is stuck on an in-flight handler instead of " +
			"cancelling and joining it after the grace period (ADR-0076)")
	}

	// Run has returned, so rt.close already ran. The handler MUST have been
	// cancelled and joined strictly before that — otherwise its next working
	// memory write would land on a closed BadgerDB.
	select {
	case <-client.cancelled:
	default:
		t.Fatal("Run returned while the in-flight handler was still parked: shutdown neither " +
			"cancelled its context nor joined it before closing the stores (ADR-0076)")
	}

	<-reqDone
}
