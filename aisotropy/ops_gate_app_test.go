package aisotropy

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// getStatus issues GET base+path with an optional bearer token and returns the
// HTTP status code, draining and closing the body.
func getStatus(t *testing.T, ctx context.Context, base, path, bearer string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatalf("build request %s: %v", path, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// bootOpsApp boots the full runtime on a free port with the stub LLM, waits for
// liveness, and returns the base URL plus a stop func. AISOTROPY_BITRATE_BIN is
// set so the /bitrate operator route is registered.
func bootOpsApp(t *testing.T) (base string, stop func()) {
	t.Helper()
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

	base = "http://" + addr
	waitForServer(t, base+"/healthz")

	return base, func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run returned error on shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Run did not return after ctx cancellation")
		}
	}
}

// TestOpsEndpointsAreTokenGated proves the operator endpoints /costs and
// /bitrate are default-closed and gated by AISOTROPY_OPS_TOKEN once set:
//   - token unset  -> 503 (endpoint disabled, no all-tenant data leak)
//   - token set    -> 401 with no/wrong bearer, 200 with the right bearer
//
// This is the app-level acceptance test for the ops-gate wiring in app.go.
func TestOpsEndpointsAreTokenGated(t *testing.T) {
	// The /bitrate route only registers when AISOTROPY_BITRATE_BIN is set; the
	// value need not be a real binary for route registration + gating.
	t.Setenv("AISOTROPY_BITRATE_BIN", "/nonexistent-bitrate-bin")

	t.Run("token unset returns 503", func(t *testing.T) {
		t.Setenv("AISOTROPY_OPS_TOKEN", "")
		base, stop := bootOpsApp(t)
		defer stop()
		ctx := context.Background()

		if got := getStatus(t, ctx, base, "/costs", ""); got != http.StatusServiceUnavailable {
			t.Errorf("GET /costs (ops disabled) = %d, want 503", got)
		}
		if got := getStatus(t, ctx, base, "/bitrate", ""); got != http.StatusServiceUnavailable {
			t.Errorf("GET /bitrate (ops disabled) = %d, want 503", got)
		}
	})

	t.Run("token set gates by bearer", func(t *testing.T) {
		const opsToken = "operator-secret-token"
		t.Setenv("AISOTROPY_OPS_TOKEN", opsToken)
		base, stop := bootOpsApp(t)
		defer stop()
		ctx := context.Background()

		for _, path := range []string{"/costs", "/bitrate"} {
			if got := getStatus(t, ctx, base, path, ""); got != http.StatusUnauthorized {
				t.Errorf("GET %s (no bearer) = %d, want 401", path, got)
			}
			if got := getStatus(t, ctx, base, path, "wrong-token"); got != http.StatusUnauthorized {
				t.Errorf("GET %s (wrong bearer) = %d, want 401", path, got)
			}
			if got := getStatus(t, ctx, base, path, opsToken); got != http.StatusOK {
				t.Errorf("GET %s (right bearer) = %d, want 200", path, got)
			}
		}
	})
}
