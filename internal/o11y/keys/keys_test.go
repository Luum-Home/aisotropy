package keys

import (
	"context"
	"testing"
)

// TestWithValueRoundTrip covers the basic carry/read contract: a value set
// with With is readable with Value, and an unset key reports ok=false.
func TestWithValueRoundTrip(t *testing.T) {
	ctx := context.Background()

	if _, ok := Value(ctx, TraceID); ok {
		t.Fatal("bare ctx must report no value")
	}

	ctx = With(ctx, TraceID, "abc123")
	ctx = With(ctx, TenantID, "acme")

	if v, ok := Value(ctx, TraceID); !ok || v != "abc123" {
		t.Fatalf("TraceID round-trip: got (%q, %v), want (\"abc123\", true)", v, ok)
	}
	if v, ok := Value(ctx, TenantID); !ok || v != "acme" {
		t.Fatalf("TenantID round-trip: got (%q, %v), want (\"acme\", true)", v, ok)
	}
	if _, ok := Value(ctx, UserID); ok {
		t.Fatal("unset UserID must report ok=false")
	}
}

// TestWithIsImmutable proves copy-on-write: deriving a child ctx never
// changes what the parent ctx reports, and overwriting a key in the child
// leaves the parent's value intact.
func TestWithIsImmutable(t *testing.T) {
	parent := With(context.Background(), TenantID, "acme")
	child := With(parent, TenantID, "globex")
	child = With(child, UserID, "alice")

	if v, _ := Value(parent, TenantID); v != "acme" {
		t.Fatalf("parent TenantID mutated: got %q, want \"acme\"", v)
	}
	if _, ok := Value(parent, UserID); ok {
		t.Fatal("parent must not see the child's UserID")
	}
	if v, _ := Value(child, TenantID); v != "globex" {
		t.Fatalf("child TenantID: got %q, want \"globex\"", v)
	}
}

// TestWithEmptyKeyIsNoOp: an empty key returns ctx unchanged (same pointer),
// so unconditional call sites need no branch.
func TestWithEmptyKeyIsNoOp(t *testing.T) {
	ctx := context.Background()
	if got := With(ctx, "", "v"); got != ctx {
		t.Fatal("With(ctx, \"\", v) must return ctx unchanged")
	}
}

// TestAll covers the iterate-all hydration helper: nil on a bare ctx, a full
// copy otherwise, and mutation of the returned map must not leak back.
func TestAll(t *testing.T) {
	if All(context.Background()) != nil {
		t.Fatal("All on a bare ctx must return nil")
	}

	ctx := With(context.Background(), TraceID, "t1")
	ctx = With(ctx, SpanID, "s1")
	ctx = With(ctx, RequestID, "r1")

	got := All(ctx)
	want := map[Key]string{TraceID: "t1", SpanID: "s1", RequestID: "r1"}
	if len(got) != len(want) {
		t.Fatalf("All returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("All[%s] = %q, want %q", k, got[k], v)
		}
	}

	// The returned map is a copy: writing to it must not affect ctx.
	got[TraceID] = "tampered"
	if v, _ := Value(ctx, TraceID); v != "t1" {
		t.Fatalf("mutating All's result leaked into ctx: TraceID = %q", v)
	}
}

// TestKeyConstantsAreSnakeCase pins the ADR-0066 convention: every constant
// in every bucket is snake_case (lowercase letters, digits, underscores).
func TestKeyConstantsAreSnakeCase(t *testing.T) {
	for _, bucket := range [][]Key{TelemetryBucket, ServiceBucket, IdentityBucket, ExecutionBucket} {
		for _, k := range bucket {
			if k == "" {
				t.Fatal("empty key constant in a bucket")
			}
			for _, r := range string(k) {
				if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
					t.Fatalf("key %q is not snake_case (offending rune %q)", k, r)
				}
			}
		}
	}
}

// TestCounterName pins the {domain}_{outcome}_total convention.
func TestCounterName(t *testing.T) {
	if got := CounterName("chat_request", OutcomeOK); got != "chat_request_ok_total" {
		t.Fatalf("CounterName ok: got %q", got)
	}
	if got := CounterName("facts_query", OutcomeError); got != "facts_query_error_total" {
		t.Fatalf("CounterName error: got %q", got)
	}
}
