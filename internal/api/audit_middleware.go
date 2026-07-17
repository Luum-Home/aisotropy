package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Luum-Home/aisotropy/internal/audit"
)

// audit_middleware.go emits exactly one durable access-audit Event per served
// /v1 request (Gap 8: a retained who-accessed-what-tenant-when trail independent
// of any OTLP backend). It is a chi middleware, wired on the router in
// aisotropy/app.go; the Event is written AFTER the handler completes, so it
// captures the real status and latency.
//
// SSE safety: the middleware wraps the ResponseWriter with chi's
// WrapResponseWriter, which passes Write/Flush straight through (and preserves
// http.Flusher / http.Hijacker / Unwrap) — it records the status and byte count
// WITHOUT buffering the body. A streaming (/chat/stream) response is therefore
// audited on completion with its token-by-token flushing intact.
//
// Identity source: tenant_id/user_id are parsed from the URL PATH
// (/v1/tenant/{tenant_id}/user/{user_id}/...), which is the already-validated
// identity — the auth middleware's must-agree bind proves the path equals the
// verified principal. The request BODY is never read here (no payload, and the
// body is single-read and owned by the handler).

// AuditMiddleware returns a chi middleware that records one audit.Event per /v1
// request. Non-/v1 paths (liveness, readiness, ops endpoints, docs) pass through
// unaudited so the trail is scoped to tenant-serving traffic. A nil sink disables
// auditing (pass-through) — callers should supply audit.NopSink for an explicit
// no-op instead, but nil is tolerated defensively.
func AuditMiddleware(sink audit.Sink) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sink == nil || !strings.HasPrefix(r.URL.Path, "/v1/") {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			// Wrap to observe the status without buffering the body (SSE-safe).
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			tenant, user := parseTenantUser(r.URL.Path)
			status := ww.Status()
			if status == 0 {
				// A handler that wrote a body without an explicit WriteHeader
				// implicitly sent 200 OK.
				status = http.StatusOK
			}

			sink.Record(audit.Event{
				Timestamp: start.UTC(),
				TenantID:  tenant,
				UserID:    user,
				Method:    r.Method,
				Route:     r.URL.Path, // PATH only — never r.URL.RawQuery
				Status:    status,
				RequestID: middleware.GetReqID(r.Context()),
				LatencyMS: time.Since(start).Milliseconds(),
			})
		})
	}
}

// parseTenantUser extracts the tenant and user identifiers from a request path of
// the form /v1/tenant/{tenant_id}/user/{user_id}/... . It scans for the "tenant"
// and "user" path segments and returns the segment that follows each, so it is
// robust to the trailing route (/chat, /chat/stream, /chat/agent, /ask, /knowledge,
// ...). Missing segments yield empty strings (the Event still records method/
// route/status). Parsing the path — not the body — keeps this to the validated
// identity.
func parseTenantUser(path string) (tenant, user string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		switch parts[i] {
		case "tenant":
			tenant = parts[i+1]
		case "user":
			user = parts[i+1]
		}
	}
	return tenant, user
}
