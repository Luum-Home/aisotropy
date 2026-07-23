// ADR-0068 Layer 2, stage 1: the boundary bridge. Exactly once per request —
// at the same Huma-handler boundary where tenant_id/user_id enter ctx
// (chatIdentity, StreamCore) — the active span's TraceID/SpanID plus the
// request/session/identity ids are drained into the internal/o11y/keys ctx
// metadata. Everything downstream then auto-hydrates: the CtxHandler stamps
// them on every *Context log line, and later layers stamp them on child spans
// and metric labels, with no per-call plumbing.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/trace"

	"github.com/Luum-Home/aisotropy/internal/o11y/keys"
)

// stampTelemetry populates the ctx observability metadata for one request:
//
//   - trace_id/span_id from the active span context, ONLY when it is valid
//     (no span, or a zeroed/noop span context, stamps nothing — log lines
//     then simply carry no trace correlation rather than a bogus one);
//   - request_id: chi's RequestID middleware value when the request came
//     through the HTTP router, a generated id otherwise (in-process callers
//     like App.Turn bypass the router and still get a correlation id);
//   - session_id, when the request carries one;
//   - tenant_id/user_id as OBSERVABILITY COPIES of the identity the caller
//     already validated and injected via withIdentity. These copies are read
//     by log/span/metric hydration ONLY — the authoritative identity for any
//     authorization or storage decision remains internal/api's own carrier
//     (TenantFromContext/UserFromContext) per the constitutional gates; this
//     bridge introduces no alternative identity path.
//
// Call it AFTER withIdentity has validated the identity, so unvalidated input
// never lands in the metadata either.
func stampTelemetry(ctx context.Context, tenant, user, session string) context.Context {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		ctx = keys.With(ctx, keys.TraceID, sc.TraceID().String())
		ctx = keys.With(ctx, keys.SpanID, sc.SpanID().String())
	}
	reqID := middleware.GetReqID(ctx)
	if reqID == "" {
		reqID = newRequestID()
	}
	ctx = keys.With(ctx, keys.RequestID, reqID)
	if session != "" {
		ctx = keys.With(ctx, keys.SessionID, session)
	}
	if tenant != "" {
		ctx = keys.With(ctx, keys.TenantID, tenant)
	}
	if user != "" {
		ctx = keys.With(ctx, keys.UserID, user)
	}
	return ctx
}

// newRequestID generates a random 128-bit hex correlation id for requests
// that did not pass through chi's RequestID middleware. On the (effectively
// impossible) failure of crypto/rand it degrades to a fixed marker rather
// than failing the request — a correlation id is telemetry, never load-
// bearing.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(b[:])
}
