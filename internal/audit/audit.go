// Package audit provides a durable, structured access-audit log that is
// independent of any external OpenTelemetry/OTLP backend.
//
// Motivation (Gap 8): the OTel gen_ai serving span carries tenant/user but is a
// no-op unless an OTLP endpoint is wired, and the cost meter is in-memory and
// per-process. Neither yields a durable, retained "who-accessed-what-tenant-when"
// record that a SOC 2 (CC6.1 / CC7.2) or ISO 27001 (A.12.4) auditor can sample.
// This package fills that gap: one append-only Event per served /v1 request,
// carrying identity + request metadata ONLY.
//
// Privacy contract (hard boundary): an Event records identity (tenant_id,
// user_id), the request line (method, route path), the outcome (status code),
// the correlation id, and latency. It NEVER records the prompt body, the model
// response, request/response headers (which can carry the bearer token), query
// strings, or any other payload. The route field is the URL PATH only — never the
// raw query — so no caller-supplied value can smuggle PII/secrets into the log.
//
// Multi-tenancy: the audit log is an OPERATOR artifact. It records tenant_id and
// user_id so an operator/auditor can attribute access, and it is NOT a
// per-tenant-readable surface — no request path exposes another tenant's (or any
// tenant's) audit entries. Retention, rotation, and access control of the sink
// (the JSONL file or the log pipeline) are operator responsibilities.
//
// Tamper-evidence (honest residual): the JSONL file sink is append-only and
// retained, but it is NOT cryptographically tamper-evident — an operator (or an
// attacker with write access to the file) can edit or truncate it, and nothing in
// the file proves that it was not. A hash-chained / WORM sink (each line commits
// to the prior line's digest, or an append-only object store with an
// object-lock/retention policy) is the named follow-on for deployments that need
// tamper-EVIDENCE, not merely retention. Do not represent this sink as tamper-proof.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"
)

// EnvLogPath is the environment variable that selects the durable JSONL file
// sink. When set to a writable path, FromEnv returns a JSONLSink appending to it;
// when unset, FromEnv falls back to the slog sink (which still produces an access
// trail on the process log stream).
const EnvLogPath = "AISOTROPY_AUDIT_LOG"

// EnvDisable, when truthy (per strconv.ParseBool), makes FromEnv return a NopSink
// — the explicit "no access trail" escape hatch. It is deliberately opt-in: the
// whole point of this package is that the DEFAULT always produces a trail, so an
// operator must set this consciously to turn auditing off.
const EnvDisable = "AISOTROPY_AUDIT_DISABLE"

// Event is one access-audit record: identity + request metadata for a single
// served request. It carries NO prompt body, NO model response, NO headers, NO
// query string, and NO other payload — only the fields below. See the package doc
// privacy contract.
type Event struct {
	// Timestamp is when the request began being served, in UTC. This is the
	// access time an auditor samples on.
	Timestamp time.Time `json:"timestamp"`
	// TenantID / UserID are the already-validated request identity (the URL path
	// identity, which the auth middleware's must-agree bind proves equals the
	// verified principal). They are identity, not secrets.
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
	// Method is the HTTP method (GET/POST/...).
	Method string `json:"method"`
	// Route is the request URL PATH (never the raw query string). It names what
	// was accessed; it embeds the tenant/user segments, which are identity.
	Route string `json:"route"`
	// Status is the HTTP status code the handler returned.
	Status int `json:"status"`
	// RequestID is the correlation id (chi RequestID middleware) tying this audit
	// entry to the request's application log lines. Empty when no id was set.
	RequestID string `json:"request_id,omitempty"`
	// LatencyMS is the served latency in whole milliseconds.
	LatencyMS int64 `json:"latency_ms"`
}

// Sink is an append-only destination for audit Events. Record MUST be safe for
// concurrent use by multiple goroutines (one per in-flight request). Record does
// not return an error: it is called AFTER a request has been served, so it cannot
// fail the request — a sink that cannot persist an event logs the failure (a
// broken audit sink is itself an operational alert) and drops it best-effort.
// Close flushes and releases the sink's resources.
type Sink interface {
	Record(Event)
	io.Closer
}

// FromEnv selects the audit sink from the environment:
//
//   - AISOTROPY_AUDIT_DISABLE truthy            -> NopSink (explicit opt-out).
//   - AISOTROPY_AUDIT_LOG=<path> set            -> durable JSONLSink at <path>.
//   - neither                                   -> SlogSink on a dedicated
//     "access_audit" logger (the default: always an access trail).
//
// A set-but-unopenable AISOTROPY_AUDIT_LOG returns an error rather than silently
// downgrading to slog: an operator who asked for a durable, retained audit file
// must not unknowingly run WITHOUT it. Callers should treat that error as a boot
// failure (fail closed), consistent with closing a compliance gap.
func FromEnv() (Sink, error) {
	if disabled, _ := strconv.ParseBool(os.Getenv(EnvDisable)); disabled {
		slog.Warn("access-audit log DISABLED via " + EnvDisable + ": no durable who-accessed-what-tenant-when trail is being recorded")
		return NopSink{}, nil
	}
	if path := os.Getenv(EnvLogPath); path != "" {
		sink, err := NewJSONLSink(path)
		if err != nil {
			return nil, fmt.Errorf("audit: open durable log %q (%s): %w", path, EnvLogPath, err)
		}
		slog.Info("access-audit log: durable JSONL file sink enabled", "path", path)
		return sink, nil
	}
	slog.Info("access-audit log: slog sink enabled (set " + EnvLogPath + "=<path> to upgrade to a durable, retained JSONL file)")
	return NewSlogSink(nil), nil
}

// JSONLSink is a durable, append-only JSON-Lines audit sink: one Event per line,
// written to an O_APPEND file under a mutex so concurrent requests cannot
// interleave partial lines. Each Record is a single write() reaching the OS,
// so an entry survives a process crash (it is in the OS page cache); Close
// fsyncs to bound the power-loss window. It is NOT cryptographically
// tamper-evident (see the package doc).
type JSONLSink struct {
	mu sync.Mutex
	f  *os.File
}

// NewJSONLSink opens (creating if needed) an append-only audit log at path.
func NewJSONLSink(path string) (*JSONLSink, error) {
	// 0o600: the audit log records identity metadata and is an operator artifact;
	// it should not be world-readable.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLSink{f: f}, nil
}

// Record appends one JSON line for e. A marshal or write failure is logged (a
// broken audit sink is an operational signal) and the event is dropped
// best-effort — Record never blocks a served request behind a fsync.
func (s *JSONLSink) Record(e Event) {
	line, err := json.Marshal(e)
	if err != nil {
		slog.Error("audit: marshal event failed; dropping", "err", err)
		return
	}
	line = append(line, '\n')
	s.mu.Lock()
	_, werr := s.f.Write(line)
	s.mu.Unlock()
	if werr != nil {
		slog.Error("audit: write event failed; dropping", "err", werr)
	}
}

// Close fsyncs and closes the underlying file. A Sync error is logged but does
// not prevent the Close.
func (s *JSONLSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	if err := s.f.Sync(); err != nil {
		slog.Error("audit: fsync on close failed", "err", err)
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// SlogSink emits each Event as one structured slog record on a dedicated audit
// logger. It is the always-available fallback: even with no durable file
// configured there is an access trail on the process log stream (which an
// operator can ship to a retained log pipeline). slog handlers are safe for
// concurrent use, so Record needs no additional locking.
type SlogSink struct {
	logger *slog.Logger
}

// NewSlogSink builds a SlogSink. A nil logger uses slog.Default(); the sink tags
// every record with component=access_audit and audit=true so the access trail is
// filterable in a shared log stream.
func NewSlogSink(logger *slog.Logger) *SlogSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogSink{logger: logger.With(slog.String("component", "access_audit"), slog.Bool("audit", true))}
}

// Record logs one access event. Fields mirror Event exactly (identity + request
// metadata only).
func (s *SlogSink) Record(e Event) {
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "access",
		slog.Time("timestamp", e.Timestamp),
		slog.String("tenant_id", e.TenantID),
		slog.String("user_id", e.UserID),
		slog.String("method", e.Method),
		slog.String("route", e.Route),
		slog.Int("status", e.Status),
		slog.String("request_id", e.RequestID),
		slog.Int64("latency_ms", e.LatencyMS),
	)
}

// Close is a no-op: the slog handler owns its own writer's lifecycle.
func (s *SlogSink) Close() error { return nil }

// NopSink discards every event. It backs the explicit AISOTROPY_AUDIT_DISABLE
// opt-out; it is never the default, because the point of this package is that the
// default always produces an access trail.
type NopSink struct{}

// Record discards e.
func (NopSink) Record(Event) {}

// Close is a no-op.
func (NopSink) Close() error { return nil }
