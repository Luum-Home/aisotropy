package llm

// OWASP LLM02:2025 — Sensitive Information Disclosure (telemetry redaction), and
// the span/telemetry half of OWASP LLM07:2025 — System Prompt Leakage.
//
// aisotropy's constitutional posture is NO user/system content on telemetry. The
// redactingExporter strips content-bearing span attributes (the user prompt, tool
// args/results, and a reserved gollem.*.messages shape) before any span leaves the
// process, while the CORRELATION allowlist — tenant_id, user_id, gen_ai.system,
// model — SURVIVES so traces stay useful. This suite asserts BOTH halves against
// an in-memory exporter: a planted secret / system-prompt sentinel in a content
// attribute never leaves; the identity attributes do. Deterministic: real SDK
// spans through the real redacting exporter, no OTLP collector, no network.
// See docs/security/owasp-llm-coverage.md.

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	llm02SecretSentinel  = "sk-SECRET-TELEMETRY-LEAK-0000000000"
	llm07SysPromptSecret = "SYSTEM-PROMPT-SENTINEL-do-not-log"
)

// TestOWASP_LLM02_ContentStrippedIdentitySurvives: a span carrying a secret in
// the prompt attribute plus tool args, alongside tenant_id/user_id/gen_ai.system,
// comes out of the redacting exporter with ALL content gone and ALL identity
// intact — the allowlist property.
func TestOWASP_LLM02_ContentStrippedIdentitySurvives(t *testing.T) {
	span := endedSpanWithAttrs(t,
		attribute.String("gollem.agent.prompt", "my SSN is 111-22-3333 and key "+llm02SecretSentinel),
		attribute.String("gollem.tool.args", `{"api_key":"`+llm02SecretSentinel+`"}`),
		attribute.String("gollem.tool.result", "sensitive tool output"),
		// Correlation/identity — must SURVIVE.
		attribute.String(attrTenantID, "acme"),
		attribute.String(attrUserID, "alice"),
		attribute.String(attrGenAISystem, "anthropic"),
	)

	next := &capturingExporter{}
	exp := &redactingExporter{next: next}
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	if len(next.got) != 1 {
		t.Fatalf("expected 1 forwarded span, got %d", len(next.got))
	}
	attrs := next.got[0].Attributes()

	// Content attributes must be gone entirely.
	for _, k := range []attribute.Key{"gollem.agent.prompt", "gollem.tool.args", "gollem.tool.result"} {
		if _, ok := attrValue(attrs, k); ok {
			t.Errorf("content attribute %q leaked through redaction", k)
		}
	}
	// And the secret must not survive under ANY attribute key.
	for _, a := range attrs {
		if a.Value.Type() == attribute.STRING && strings.Contains(a.Value.AsString(), llm02SecretSentinel) {
			t.Errorf("secret sentinel leaked via attribute %q", a.Key)
		}
	}

	// Identity/correlation must survive.
	for _, want := range []struct {
		key attribute.Key
		val string
	}{
		{attrTenantID, "acme"},
		{attrUserID, "alice"},
		{attrGenAISystem, "anthropic"},
	} {
		v, ok := attrValue(attrs, want.key)
		if !ok || v.AsString() != want.val {
			t.Errorf("correlation attribute %q = %v (ok=%v), want %q — redaction must preserve identity", want.key, v, ok, want.val)
		}
	}
}

// TestOWASP_LLM07_SystemPromptSentinelNotOnSpans: a sentinel placed in a model
// message-history attribute (the reserved gollem.*.messages shape) never leaves
// the process — the telemetry half of system-prompt-leak defense.
func TestOWASP_LLM07_SystemPromptSentinelNotOnSpans(t *testing.T) {
	span := endedSpanWithAttrs(t,
		attribute.String("gollem.chat.messages", "system: "+llm07SysPromptSecret),
		attribute.String(attrTenantID, "acme"),
	)
	next := &capturingExporter{}
	exp := &redactingExporter{next: next}
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	for _, a := range next.got[0].Attributes() {
		if a.Value.Type() == attribute.STRING && strings.Contains(a.Value.AsString(), llm07SysPromptSecret) {
			t.Errorf("system-prompt sentinel leaked onto telemetry via attribute %q", a.Key)
		}
	}
}
