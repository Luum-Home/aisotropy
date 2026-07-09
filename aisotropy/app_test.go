package aisotropy

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestNewDefaults verifies the zero-option builder falls back to the documented
// defaults (rate 10/30, empty-but-non-nil persona map, no executor resolved yet).
func TestNewDefaults(t *testing.T) {
	a := New()
	if a.ratePerSec != defaultRatePerSec || a.burst != defaultBurst {
		t.Errorf("default rate = %v/%v, want %v/%v", a.ratePerSec, a.burst, defaultRatePerSec, defaultBurst)
	}
	if a.personas == nil {
		t.Error("personas map must be initialized, not nil")
	}
	if a.executor != nil {
		t.Error("executor should be nil until Run resolves the demo default")
	}
	if len(a.toolset) != 0 {
		t.Errorf("expected no tools by default, got %d", len(a.toolset))
	}
}

// TestNewAppliesOptions verifies each functional option lands on the App, and
// that WithTool accumulates rather than replaces.
func TestNewAppliesOptions(t *testing.T) {
	stub := llm.NewStubClient()
	exec := DemoStubExecutor{}
	a := New(
		WithAddr("127.0.0.1:9999"),
		WithTool(Tool{Name: "a"}),
		WithTool(Tool{Name: "b", RequiredScope: "s"}),
		WithToolExecutor(exec),
		WithPersona("p1", Persona{BasePrompt: "prompt-1"}),
		WithPersona("p2", Persona{BasePrompt: "prompt-2"}),
		WithLLM(stub),
		WithRateLimit(5, 7),
		WithCostPricing(1.5, 3.0),
	)

	if a.addr != "127.0.0.1:9999" {
		t.Errorf("addr = %q, want 127.0.0.1:9999", a.addr)
	}
	if len(a.toolset) != 2 || a.toolset[0].Name != "a" || a.toolset[1].Name != "b" {
		t.Errorf("WithTool did not accumulate in order: %+v", a.toolset)
	}
	if a.executor == nil {
		t.Error("WithToolExecutor did not set the executor")
	}
	if a.personas["p1"] != "prompt-1" || a.personas["p2"] != "prompt-2" {
		t.Errorf("WithPersona did not register both personas: %+v", a.personas)
	}
	if a.llmClient != stub {
		t.Error("WithLLM did not set the client")
	}
	if a.ratePerSec != 5 || a.burst != 7 {
		t.Errorf("WithRateLimit = %v/%v, want 5/7", a.ratePerSec, a.burst)
	}
	if a.inPricePer1M != 1.5 || a.outPricePer1M != 3.0 {
		t.Errorf("WithCostPricing = %v/%v, want 1.5/3.0", a.inPricePer1M, a.outPricePer1M)
	}
}

// DemoStubExecutor is a trivial ToolExecutor used only by the builder tests.
type DemoStubExecutor struct{}

// Execute returns a fixed string and never fails.
func (DemoStubExecutor) Execute(context.Context, string, map[string]string) (string, error) {
	return "ok", nil
}

// TestRunServesAndShutsDown boots the full runtime on a free port with the stub
// LLM and default (in-memory) stores, confirms the liveness, cost, and OpenAPI
// endpoints respond, then cancels ctx and asserts Run returns a clean nil.
func TestRunServesAndShutsDown(t *testing.T) {
	addr := freeAddr(t)
	app := New(
		WithAddr(addr),
		WithLLM(llm.NewStubClient()),
		WithTool(Tool{Name: "search_docs", Description: "search"}),
		WithToolExecutor(DemoStubExecutor{}),
		WithPersona("demo", Persona{BasePrompt: "You are a demo."}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	for _, path := range []string{"/healthz", "/costs", "/openapi.json"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

// freeAddr reserves an ephemeral loopback port and returns its address, closing
// the listener so Run can bind it. The small reuse window is acceptable for a
// local test.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// waitForServer polls url until it responds 200 or a timeout elapses.
func waitForServer(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready", url)
}
