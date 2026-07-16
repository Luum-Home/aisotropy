package aisotropy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// bootDevUIApp boots the full runtime on a free port with the stub LLM and the
// given options merged in, waits for liveness, and returns the base URL plus a
// stop func. It mirrors bootOpsApp but lets each test decide whether WithDevUI
// is present, so the "opted in" and "not opted in" cases exercise the SAME boot
// path and only differ by that one option.
func bootDevUIApp(t *testing.T, extra ...Option) (base string, stop func()) {
	t.Helper()
	addr := freeAddr(t)
	opts := append([]Option{
		WithAddr(addr),
		WithLLM(llm.NewStubClient()),
	}, extra...)
	app := New(opts...)

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

// getDevUI issues GET base+path and returns status, Content-Type, and body.
func getDevUI(t *testing.T, ctx context.Context, base, path string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatalf("build request %s: %v", path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", path, err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
}

// TestDevUIMountedWhenEnabled proves WithDevUI mounts the page at GET /ui and
// serves the embedded HTML: 200, a text/html content type, and a recognizable
// marker from the embedded asset.
func TestDevUIMountedWhenEnabled(t *testing.T) {
	base, stop := bootDevUIApp(t, WithDevUI())
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, ctype, body := getDevUI(t, ctx, base, "/ui")
	if status != http.StatusOK {
		t.Fatalf("GET /ui status = %d, want 200", status)
	}
	if !strings.HasPrefix(ctype, "text/html") {
		t.Errorf("GET /ui Content-Type = %q, want text/html…", ctype)
	}
	// A stable marker embedded in devui.html — both the machine comment and the
	// visible title must survive the //go:embed round-trip.
	for _, marker := range []string{"aisotropy-devui", "Aisotropy Dev Chat", "/chat/stream"} {
		if !strings.Contains(body, marker) {
			t.Errorf("GET /ui body missing marker %q", marker)
		}
	}
}

// TestDevUIAtCustomPath proves WithDevUIAt mounts the page on a caller-chosen
// non-/v1 route and that the default /ui is then NOT registered.
func TestDevUIAtCustomPath(t *testing.T) {
	base, stop := bootDevUIApp(t, WithDevUIAt("/playground"))
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if status, _, _ := getDevUI(t, ctx, base, "/playground"); status != http.StatusOK {
		t.Fatalf("GET /playground status = %d, want 200", status)
	}
	if status, _, _ := getDevUI(t, ctx, base, "/ui"); status != http.StatusNotFound {
		t.Errorf("GET /ui status = %d with custom path, want 404 (no drift)", status)
	}
}

// TestDevUIAbsentWhenNotEnabled proves the inert-safe default: with no WithDevUI
// option, GET /ui 404s, so a stock build exposes no dev surface.
func TestDevUIAbsentWhenNotEnabled(t *testing.T) {
	base, stop := bootDevUIApp(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if status, _, _ := getDevUI(t, ctx, base, "/ui"); status != http.StatusNotFound {
		t.Fatalf("GET /ui status = %d without WithDevUI, want 404", status)
	}
}

// TestNormalizeDevUIPath covers the path-coercion helper directly: leading-slash
// normalization and the empty/slash-only fallback to the default.
func TestNormalizeDevUIPath(t *testing.T) {
	cases := map[string]string{
		"ui":         "/ui",
		"/ui":        "/ui",
		"//ui":       "/ui",
		"  /chat  ":  "/chat",
		"playground": "/playground",
		"":           defaultDevUIPath,
		"/":          defaultDevUIPath,
		"///":        defaultDevUIPath,
	}
	for in, want := range cases {
		if got := normalizeDevUIPath(in); got != want {
			t.Errorf("normalizeDevUIPath(%q) = %q, want %q", in, got, want)
		}
	}
}
