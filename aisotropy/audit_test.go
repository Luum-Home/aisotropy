package aisotropy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/audit"
)

// TestRun_AuditLogWritesDurableAccessTrail exercises the full Gap-8 wiring
// through Run: AISOTROPY_AUDIT_LOG selects the durable JSONL file sink, the /v1
// audit middleware records one Event per served request with the validated
// identity + request metadata, and NO prompt body / response / secret is written.
func TestRun_AuditLogWritesDurableAccessTrail(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "access-audit.jsonl")
	t.Setenv("AISOTROPY_AUDIT_LOG", auditPath)
	t.Setenv("AISOTROPY_AUDIT_DISABLE", "")
	t.Setenv("AISOTROPY_REQUIRE_TLS", "")

	addr := freeAddr(t)
	app := New(WithAddr(addr), WithLLM(fastStub()), WithAuthenticator(NoAuth()))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	// A /v1 chat request carrying a "secret" prompt and an Authorization bearer.
	reqBody := `{"prompt":"remember my secret passphrase hunter2","product_id":"p1","session_id":"s1"}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/v1/tenant/acme/user/alice/chat", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer super-secret-token-xyz")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /chat: %v", err)
	}
	_ = resp.Body.Close()

	// A liveness poll must NOT appear in the audit trail (non-/v1).
	livReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if lr, lerr := http.DefaultClient.Do(livReq); lerr == nil {
		_ = lr.Body.Close()
	}

	cancel()
	if rerr := <-errCh; rerr != nil {
		t.Fatalf("Run returned error on shutdown: %v", rerr)
	}

	// Shutdown fsync+closed the sink; the file must hold the durable trail.
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Fatal("audit log is empty; expected at least one /v1 access record")
	}

	var events []audit.Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var e audit.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("audit line not valid JSON %q: %v", sc.Text(), err)
		}
		events = append(events, e)
	}

	var chat *audit.Event
	for i := range events {
		if strings.HasSuffix(events[i].Route, "/chat") {
			chat = &events[i]
		}
		// No non-/v1 path (e.g. /healthz) should ever be in the trail.
		if !strings.HasPrefix(events[i].Route, "/v1/") {
			t.Errorf("non-/v1 route audited: %q", events[i].Route)
		}
	}
	if chat == nil {
		t.Fatalf("no audit event for the /chat request; got routes %v", routes(events))
	}
	if chat.TenantID != "acme" || chat.UserID != "alice" {
		t.Errorf("audit identity = (%q,%q), want (acme,alice)", chat.TenantID, chat.UserID)
	}
	if chat.Method != http.MethodPost {
		t.Errorf("audit method = %q, want POST", chat.Method)
	}
	if chat.Status == 0 {
		t.Errorf("audit status not recorded")
	}
	if chat.RequestID == "" {
		t.Errorf("audit request_id should be populated")
	}

	// The whole serialized trail must not leak the prompt body or the bearer token.
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"hunter2", "passphrase", "super-secret-token", "bearer", "authorization"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("audit log leaked forbidden content %q", forbidden)
		}
	}
}

// TestRun_UnopenableAuditLogFailsBoot proves the fail-closed posture: a set-but-
// unopenable AISOTROPY_AUDIT_LOG makes Run return an error rather than silently
// serving without the requested durable audit trail.
func TestRun_UnopenableAuditLogFailsBoot(t *testing.T) {
	// A path under a file (not a dir) cannot be created.
	base := t.TempDir()
	notADir := filepath.Join(base, "iamafile")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Setenv("AISOTROPY_AUDIT_DISABLE", "")
	t.Setenv("AISOTROPY_AUDIT_LOG", filepath.Join(notADir, "audit.jsonl"))

	app := New(WithAddr(freeAddr(t)), WithLLM(fastStub()), WithAuthenticator(NoAuth()))
	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("Run should fail closed when the durable audit log cannot be opened")
	}
	if !strings.Contains(err.Error(), "audit") {
		t.Errorf("error should mention audit: %v", err)
	}
}

func routes(events []audit.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Route)
	}
	return out
}

// recordingAuditSink is a minimal AuditSink a test can install via
// WithAuditSink; closeErr lets a test force the Close() error branch Run's
// shutdown defer must log rather than panic on.
type recordingAuditSink struct {
	closeErr error
	records  int
}

func (s *recordingAuditSink) Record(audit.Event) { s.records++ }
func (s *recordingAuditSink) Close() error       { return s.closeErr }

// TestWithAuditSink_InstallsCustomSink covers WithAuditSink's non-nil branch: a
// consumer-supplied sink wins over the env-driven default and observes the
// requests Run serves.
func TestWithAuditSink_InstallsCustomSink(t *testing.T) {
	sink := &recordingAuditSink{}
	addr := freeAddr(t)
	app := New(WithAddr(addr), WithLLM(fastStub()), WithAuthenticator(NoAuth()), WithAuditSink(sink))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	waitForServer(t, "http://"+addr+"/healthz")

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/v1/tenant/acme/user/alice/chat",
		strings.NewReader(`{"prompt":"hi","product_id":"p1","session_id":"s1"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /chat: %v", err)
	}
	_ = resp.Body.Close()

	cancel()
	if rerr := <-errCh; rerr != nil {
		t.Fatalf("Run returned error on shutdown: %v", rerr)
	}
	if sink.records == 0 {
		t.Fatal("custom audit sink installed via WithAuditSink recorded nothing; the env-driven default must not have been used instead")
	}
}

// TestWithAuditSink_NilIgnored covers WithAuditSink's nil-ignored branch: a nil
// sink leaves the App on the env-driven default rather than crashing later.
func TestWithAuditSink_NilIgnored(t *testing.T) {
	app := New(WithLLM(fastStub()), WithAuditSink(nil))
	if app.auditSink != nil {
		t.Fatal("WithAuditSink(nil) must be a no-op, not install a nil sink")
	}
}

// TestRun_AuditSinkCloseErrorLogged covers Run's shutdown-defer branch where
// auditSink.Close() itself errors: Run must still return the (nil) shutdown
// error rather than propagate the close failure, logging it instead.
func TestRun_AuditSinkCloseErrorLogged(t *testing.T) {
	sink := &recordingAuditSink{closeErr: errors.New("sink close boom")}
	addr := freeAddr(t)
	app := New(WithAddr(addr), WithLLM(fastStub()), WithAuthenticator(NoAuth()), WithAuditSink(sink))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	waitForServer(t, "http://"+addr+"/healthz")

	cancel()
	if rerr := <-errCh; rerr != nil {
		t.Fatalf("Run returned error on shutdown: %v (a failed audit-sink close must only be logged)", rerr)
	}
}
