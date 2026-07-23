// Package keys is ADR-0068 Layer 1: the typed observability tag vocabulary.
//
// Every cross-signal field name (span attribute, log field, metric label)
// that is NOT already covered by an OTel semantic convention (http.*, db.*,
// gen_ai.* are used verbatim where they exist) comes from the compile-time
// constants below — snake_case throughout, one deliberate convention, no
// mixed dotted/flat drift. The constants are grouped in the ADR's buckets
// (telemetry, service, identity, execution); the auth/device/locale buckets
// are schema-reserved and get constants only when a surface needs them.
//
// The package also carries the vocabulary through context.Context (see
// context.go) and names the metric conventions (MeterScope, MetricName) so
// later layers (the slog ctx-handler, span hydration, the MeterProvider)
// share one spelling of every tag. Types and constants only — no
// MeterProvider, no exporter, no I/O lives here.
package keys

// Key is a typed observability field name from the ADR-0068 vocabulary.
// Using a distinct type (not bare string) makes call sites compile-time
// checked: a handler hydrating "the telemetry bucket" iterates Key constants,
// never ad-hoc literals.
type Key string

// Telemetry bucket: correlation identifiers that tie a log line or metric to
// its trace and request. These are on the masking contract's never-mask
// allowlist — correlation must survive redaction (ADR-0068 §Masking).
const (
	// TraceID is the W3C trace id of the active span, hex-encoded.
	TraceID Key = "trace_id"
	// SpanID is the span id of the active span, hex-encoded.
	SpanID Key = "span_id"
	// RequestID is the per-HTTP-request correlation id (chi's RequestID
	// middleware value when present, generated otherwise).
	RequestID Key = "request_id"
	// SessionID is the client-chosen conversation identifier (the
	// working-memory session key); it has no authorization meaning.
	SessionID Key = "session_id"
	// CorrelationID is a caller-supplied cross-system correlation id for
	// flows that span more than one request (e.g. an async job kicked off by
	// an earlier request).
	CorrelationID Key = "correlation_id"
)

// Service bucket: where in the codebase a signal was emitted. Set explicitly
// at module boundaries — the ADR deliberately rejects per-call stack-walking
// as too costly.
const (
	ServiceModule    Key = "service_module"
	ServicePackage   Key = "service_package"
	ServiceComponent Key = "service_component"
	ServiceFunction  Key = "service_function"
)

// Identity bucket: the constitutional pair. These are OBSERVABILITY COPIES of
// the identity that internal/api injects at the Huma boundary — the
// authoritative identity for any authorization or storage decision remains
// internal/api's own ctx carrier (TenantFromContext/UserFromContext), never
// this vocabulary (see .claude/rules/constitutional-gates.md). Never masked.
const (
	TenantID Key = "tenant_id"
	UserID   Key = "user_id"
)

// Execution bucket: async/job attribution for work detached from the
// originating request (outbox consolidation, sleep cycle).
const (
	ExecutionJobID   Key = "execution_job_id"
	ExecutionIsAsync Key = "execution_is_async"
)

// Bucket slices give hydration loops (the slog ctx-handler, the boundary-span
// attribute loop, the masking allowlist) a stable, ordered view of each
// bucket. They are package variables for iteration convenience; treat them as
// read-only.
var (
	TelemetryBucket = []Key{TraceID, SpanID, RequestID, SessionID, CorrelationID}
	ServiceBucket   = []Key{ServiceModule, ServicePackage, ServiceComponent, ServiceFunction}
	IdentityBucket  = []Key{TenantID, UserID}
	ExecutionBucket = []Key{ExecutionJobID, ExecutionIsAsync}
)

// MeterScope names the per-layer meter a metric instrument is created under
// (ADR-0068: one meter per architectural layer, low-cardinality by
// construction). Constants only — the MeterProvider itself is Layer 4.
type MeterScope string

// The meter-per-layer scopes.
const (
	MeterAPI      MeterScope = "api"
	MeterLLM      MeterScope = "llm"
	MeterMemory   MeterScope = "memory"
	MeterFacts    MeterScope = "facts"
	MeterSemantic MeterScope = "semantic"
	MeterOutbox   MeterScope = "outbox"
)

// MetricName is a counter/histogram name following the ADR's
// {domain}_{outcome}_total convention (the _total suffix marks a monotonic
// counter). Compose RED counter names with CounterName rather than string
// literals so the convention cannot drift per call site.
type MetricName string

// Outcome is the {outcome} segment of a RED counter name.
type Outcome string

// The RED outcomes.
const (
	OutcomeOK    Outcome = "ok"
	OutcomeError Outcome = "error"
)

// CounterName composes a {domain}_{outcome}_total MetricName, e.g.
// CounterName("chat_request", OutcomeOK) == "chat_request_ok_total".
func CounterName(domain string, outcome Outcome) MetricName {
	return MetricName(domain + "_" + string(outcome) + "_total")
}
