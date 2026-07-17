package aisotropy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestRun_RequireTLSRejectsPlaintext covers the AISOTROPY_REQUIRE_TLS middleware
// branch in Run: with it truthy, every plaintext request (r.TLS == nil) is
// rejected 400 before any route — including /healthz, since the middleware is
// registered ahead of all routes. This is the plaintext-token-leak guard.
func TestRun_RequireTLSRejectsPlaintext(t *testing.T) {
	t.Setenv("AISOTROPY_REQUIRE_TLS", "true")
	addr := freeAddr(t)
	app := New(WithAddr(addr), WithLLM(fastStub()), WithAuthenticator(NoAuth()))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	// With REQUIRE_TLS on, plaintext /healthz answers 400, so we cannot use
	// waitForServer (which waits for 200). Retry until we get any HTTP response.
	deadline := time.Now().Add(10 * time.Second)
	var code int
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			code = resp.StatusCode
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if code != http.StatusBadRequest {
		t.Errorf("plaintext /healthz with REQUIRE_TLS on = %d, want 400", code)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error on shutdown: %v", err)
	}
}

// TestRun_HealthReadinessProbes covers the GET /health readiness handler and its
// two probe closures (facts + semantic) in Run, running them over the default
// in-process stores. Both respond, so /health is 200 with status "ok".
func TestRun_HealthReadinessProbes(t *testing.T) {
	t.Setenv("AISOTROPY_REQUIRE_TLS", "")
	addr := freeAddr(t)
	app := New(WithAddr(addr), WithLLM(llm.NewStubClient()), WithAuthenticator(NoAuth()))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health = %d (%s), want 200", resp.StatusCode, body)
	}
	var out struct {
		Status string `json:"status"`
		Checks []struct {
			Name string `json:"name"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode /health body %q: %v", body, err)
	}
	if out.Status != "ok" {
		t.Errorf("/health status = %q, want ok", out.Status)
	}
	if len(out.Checks) < 2 {
		t.Errorf("/health checks = %+v, want at least the facts + semantic probes", out.Checks)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error on shutdown: %v", err)
	}
}
