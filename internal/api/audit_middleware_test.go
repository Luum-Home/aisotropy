package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Luum-Home/aisotropy/internal/audit"
)

// captureSink is a concurrency-safe test Sink recording every Event.
type captureSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (s *captureSink) Record(e audit.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *captureSink) Close() error { return nil }

func (s *captureSink) snapshot() []audit.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.Event, len(s.events))
	copy(out, s.events)
	return out
}

func newAuditRouter(sink audit.Sink) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(AuditMiddleware(sink))
	return r
}

func TestParseTenantUser(t *testing.T) {
	cases := []struct {
		path, tenant, user string
	}{
		{"/v1/tenant/acme/user/alice/chat", "acme", "alice"},
		{"/v1/tenant/acme/user/alice/chat/stream", "acme", "alice"},
		{"/v1/tenant/t1/user/u1/chat/agent", "t1", "u1"},
		{"/v1/tenant/t1/user/u1/ask", "t1", "u1"},
		{"/v1/tenant/t1/user/u1/knowledge", "t1", "u1"},
		{"/healthz", "", ""},
		{"/v1/tenant/acme", "acme", ""},
	}
	for _, c := range cases {
		gt, gu := parseTenantUser(c.path)
		if gt != c.tenant || gu != c.user {
			t.Errorf("parseTenantUser(%q) = (%q,%q), want (%q,%q)", c.path, gt, gu, c.tenant, c.user)
		}
	}
}

func TestAuditMiddleware_EmitsOneEventPerRequest(t *testing.T) {
	sink := &captureSink{}
	r := newAuditRouter(sink)

	// A handler that reads a "prompt" from the body and writes a "response" — the
	// audit Event must record NEITHER.
	r.Post("/v1/tenant/{tenant_id}/user/{user_id}/chat", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"answer":"super-secret-model-response"}`))
	})

	body := strings.NewReader(`{"prompt":"my-secret-prompt-body","scopes":["x"]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/acme/user/alice/chat", body)
	req.Header.Set("Authorization", "Bearer super-secret-token")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("want exactly 1 audit event, got %d", len(evs))
	}
	e := evs[0]
	if e.TenantID != "acme" || e.UserID != "alice" {
		t.Errorf("identity mismatch: %+v", e)
	}
	if e.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", e.Method)
	}
	if e.Route != "/v1/tenant/acme/user/alice/chat" {
		t.Errorf("route = %q", e.Route)
	}
	if e.Status != http.StatusCreated {
		t.Errorf("status = %d, want 201", e.Status)
	}
	if e.RequestID == "" {
		t.Errorf("request_id should be populated from chi RequestID middleware")
	}
	if e.Timestamp.IsZero() {
		t.Errorf("timestamp should be set")
	}

	// No body/response/secret leaked into the recorded event.
	fields := fmt.Sprintf("%+v", e)
	for _, forbidden := range []string{"secret", "prompt", "answer", "Bearer", "Authorization"} {
		if strings.Contains(fields, forbidden) {
			t.Errorf("audit event leaked %q: %s", forbidden, fields)
		}
	}
}

func TestAuditMiddleware_SkipsNonV1(t *testing.T) {
	sink := &captureSink{}
	r := newAuditRouter(sink)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("non-/v1 path must not be audited, got %d events", got)
	}
}

// TestAuditMiddleware_SSE proves a streaming (chunked, flushed) handler is
// audited on completion WITHOUT breaking the stream: every chunk is flushed and
// received, and exactly one Event is recorded after the stream ends.
func TestAuditMiddleware_SSE(t *testing.T) {
	sink := &captureSink{}
	r := newAuditRouter(sink)

	r.Get("/v1/tenant/{tenant_id}/user/{user_id}/chat/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("wrapped ResponseWriter must still implement http.Flusher for SSE")
			return
		}
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			fl.Flush()
		}
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tenant/acme/user/alice/chat/stream", nil))

	// Stream body intact (all three chunks flushed through the wrapper).
	for i := 0; i < 3; i++ {
		want := fmt.Sprintf("data: chunk-%d", i)
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("stream missing %q; body=%q", want, rec.Body.String())
		}
	}

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("want 1 audit event for the SSE request, got %d", len(evs))
	}
	if evs[0].Route != "/v1/tenant/acme/user/alice/chat/stream" || evs[0].Status != http.StatusOK {
		t.Errorf("SSE audit event mismatch: %+v", evs[0])
	}
}

func TestAuditMiddleware_ConcurrentRequests(t *testing.T) {
	sink := &captureSink{}
	r := newAuditRouter(sink)
	r.Get("/v1/tenant/{tenant_id}/user/{user_id}/ask", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/v1/tenant/t%d/user/u%d/ask", i, i)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		}(i)
	}
	wg.Wait()

	if got := len(sink.snapshot()); got != n {
		t.Fatalf("want %d audit events, got %d", n, got)
	}
}

func TestAuditMiddleware_NilSinkPassesThrough(t *testing.T) {
	r := newAuditRouter(nil)
	called := false
	r.Get("/v1/tenant/{tenant_id}/user/{user_id}/ask", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tenant/t/user/u/ask", nil))
	if !called {
		t.Fatal("handler should still run with a nil sink")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
