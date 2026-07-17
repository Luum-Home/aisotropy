package aisotropy

import "github.com/Luum-Home/aisotropy/internal/audit"

// audit.go exposes the public access-audit surface (Gap 8): a durable, structured
// "who-accessed-what-tenant-when" trail emitted once per served /v1 request,
// independent of any external OTLP backend. The wiring (the chi middleware and the
// env-driven sink selection) lives in app.go's Run; this file exposes the types a
// third-party consumer needs to bring their own sink, since internal/audit is not
// importable from outside the module.
//
// Default behavior (no WithAuditSink): the runtime builds a sink from the
// environment via audit.FromEnv — a durable JSONL file when AISOTROPY_AUDIT_LOG is
// set, otherwise a dedicated slog "access_audit" logger. Either way there is
// always an access trail; setting the path upgrades it to a retained durable file.
// AISOTROPY_AUDIT_DISABLE=true is the explicit opt-out. See internal/audit for the
// full privacy contract (identity + request metadata ONLY — never the prompt body,
// response, headers, query string, or any PII/secret) and the honest
// tamper-evidence residual (append-only and retained, but NOT cryptographically
// tamper-proof — a hash-chain/WORM sink is the named follow-on).

// AuditEvent is one access-audit record: identity + request metadata for a single
// served request (never the prompt body/response/secrets/PII). It is a re-export
// of the internal type so a consumer implementing AuditSink can read the fields.
type AuditEvent = audit.Event

// AuditSink is an append-only destination for AuditEvents. Implementations MUST be
// safe for concurrent use. Supply one via WithAuditSink to ship the access trail
// to a custom durable/tamper-evident backend (a hash-chained store, a WORM object
// store, a SIEM pipeline); otherwise the runtime uses the env-driven default.
type AuditSink = audit.Sink

// WithAuditSink installs a custom access-audit sink, overriding the env-driven
// default (audit.FromEnv). Pass this to route the per-/v1-request access trail to
// your own durable or tamper-evident backend. Passing nil is ignored (the runtime
// keeps the env-driven default).
func WithAuditSink(sink AuditSink) Option {
	return func(a *App) {
		if sink != nil {
			a.auditSink = sink
		}
	}
}
