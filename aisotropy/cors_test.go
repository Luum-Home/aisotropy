package aisotropy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler is the terminal handler the CORS middleware wraps in these tests. It
// records whether it was reached (a preflight must NOT reach it) and writes 200.
type okHandler struct{ reached bool }

func (h *okHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.reached = true
	w.WriteHeader(http.StatusOK)
}

// TestCORSAllowedOriginEchoed: an origin on the allowlist gets its exact origin
// echoed in Access-Control-Allow-Origin on a normal (non-preflight) request, and
// the request still flows through to the handler.
func TestCORSAllowedOriginEchoed(t *testing.T) {
	term := &okHandler{}
	mw := newCORSMiddleware(corsAllowlist([]string{"https://app.example.com", "http://localhost:5173"}))
	h := mw(term)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/t/user/u/chat", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the exact echoed origin", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
	if !term.reached {
		t.Error("normal request must pass through to the handler")
	}
	// A multi-tenant bearer-auth API must never reflect a wildcard.
	if rec.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Error("must never emit Access-Control-Allow-Origin: *")
	}
}

// TestCORSDisallowedOriginGetsNoHeaders: an origin NOT on the allowlist receives
// no Access-Control-Allow-Origin, so the browser blocks the cross-origin read.
func TestCORSDisallowedOriginGetsNoHeaders(t *testing.T) {
	term := &okHandler{}
	mw := newCORSMiddleware(corsAllowlist([]string{"https://app.example.com"}))
	h := mw(term)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/t/user/u/chat", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none for a non-allowlisted origin", got)
	}
	// The server still processes the request; only the BROWSER blocks the read.
	if !term.reached {
		t.Error("a disallowed origin must still reach the handler server-side (browser enforces)")
	}
}

// TestCORSUnsetEmitsNoHeaders: an empty allowlist (env unset) is a pass-through
// that emits no CORS headers at all — same-origin only, the secure default.
func TestCORSUnsetEmitsNoHeaders(t *testing.T) {
	term := &okHandler{}
	mw := newCORSMiddleware(corsAllowlist(nil))
	h := mw(term)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/t/user/u/chat", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want NONE when no allowlist is configured", got)
	}
	if got := rec.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want none when CORS is off", got)
	}
	if !term.reached {
		t.Error("with CORS off the request must pass straight through")
	}
}

// TestCORSPreflightReturnsHeaders: a preflight OPTIONS from an allowed origin is
// answered with 204 and the method/header allowances, WITHOUT reaching the
// handler. A preflight from a disallowed origin gets no allow-origin header.
func TestCORSPreflightReturnsHeaders(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		term := &okHandler{}
		mw := newCORSMiddleware(corsAllowlist([]string{"https://app.example.com"}))
		h := mw(term)

		req := httptest.NewRequest(http.MethodOptions, "/v1/tenant/t/user/u/chat/stream", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "Authorization, Last-Event-ID")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Errorf("preflight status = %d, want 204", rec.Code)
		}
		if term.reached {
			t.Error("preflight must be short-circuited, never reach the handler")
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Errorf("preflight Access-Control-Allow-Origin = %q, want the echoed origin", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Methods"); got != corsAllowedMethods {
			t.Errorf("preflight Access-Control-Allow-Methods = %q, want %q", got, corsAllowedMethods)
		}
		allowHdrs := rec.Header().Get("Access-Control-Allow-Headers")
		for _, want := range []string{"Content-Type", "Authorization", "Last-Event-ID"} {
			if !containsHeader(allowHdrs, want) {
				t.Errorf("preflight Access-Control-Allow-Headers %q missing %q", allowHdrs, want)
			}
		}
		if rec.Header().Get("Access-Control-Max-Age") == "" {
			t.Error("preflight should advertise Access-Control-Max-Age")
		}
	})

	t.Run("disallowed", func(t *testing.T) {
		term := &okHandler{}
		mw := newCORSMiddleware(corsAllowlist([]string{"https://app.example.com"}))
		h := mw(term)

		req := httptest.NewRequest(http.MethodOptions, "/v1/tenant/t/user/u/chat/stream", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Errorf("preflight status = %d, want 204", rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("disallowed preflight Access-Control-Allow-Origin = %q, want none", got)
		}
		if term.reached {
			t.Error("preflight must never reach the handler")
		}
	})
}

func containsHeader(list, want string) bool {
	// The allow-headers value is a comma-separated list; a substring check is
	// sufficient here because the fixed header names do not overlap.
	return len(list) > 0 && (list == want || contains(list, want))
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
