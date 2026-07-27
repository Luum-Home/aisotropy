package main

import (
	"os"
	"testing"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// realLaneEnv points newProviderClient at a syntactically valid but unreachable
// provider (no network call is ever made — only construction is under test)
// and forces the real lane (ARENA_STUB unset).
func realLaneEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ARENA_STUB", "")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("OPENAI_MODEL", "agnes-2.0-flash")
}

// TestNewAgnesClient_ToolCapabilities is the regression guard for the bug
// commit 48dbf62 diagnosed: the client injected via WithLLM MUST satisfy
// aisotropy.ToolCaller when viewed through the narrow LLMClient interface the
// way WithLLM stores it — otherwise /chat/agent silently downgrades to the
// agentloop JSON fallback (the tools dimension's run-to-run variance source).
//
// It now ALSO asserts aisotropy.ReasoningToolCaller — a strict capability
// UPGRADE over the deleted usage decorator (commit 674c0e8's agnesClient),
// which could not forward that interface because the façade did not export it
// (be945de exports it): with the raw façade client injected directly, the
// runtime's `client.(ReasoningToolCaller)` assertion succeeds and reasoning is
// carried across agent turns instead of being silently dropped at the wrap.
func TestNewAgnesClient_ToolCapabilities(t *testing.T) {
	realLaneEnv(t)

	client, err := newProviderClient()
	if err != nil {
		t.Fatalf("newProviderClient: %v", err)
	}
	if client == nil {
		t.Fatal("newProviderClient returned nil client in the real lane")
	}

	// The load-bearing assertions run on the interface value exactly as
	// WithLLM holds it.
	var c aisotropy.LLMClient = client
	if _, ok := c.(aisotropy.ToolCaller); !ok {
		t.Fatal("injected client does not satisfy aisotropy.ToolCaller — /chat/agent would use the agentloop JSON fallback (the exact downgrade this guard prevents)")
	}
	if _, ok := c.(aisotropy.ReasoningToolCaller); !ok {
		t.Fatal("injected client does not satisfy aisotropy.ReasoningToolCaller — the deleted decorator's known capability loss has regressed (be945de made the raw client's reasoning capability reachable; injecting it directly must keep it)")
	}
}

// TestNewProviderClient_NameComposition proves the injected façade client
// composes the "<provider-label>:<model>" identity string the arena reports. The
// label is the PROVIDER-NEUTRAL "openai-compat", not a vendor name: this client
// speaks the OpenAI-compatible protocol and does not know which company is on
// the other end (see docs/architecture/model-provider.md).
func TestNewProviderClient_NameComposition(t *testing.T) {
	realLaneEnv(t)

	client, err := newProviderClient()
	if err != nil {
		t.Fatalf("newProviderClient: %v", err)
	}
	if got, want := client.Name(), "openai-compat:agnes-2.0-flash"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

// TestNewAgnesClient_StubModeReturnsNil pins the ARENA_STUB=1 contract every
// mode's call site relies on: (nil, nil) — a normal, successful "run offline"
// signal, never an error on the missing OPENAI_API_KEY — with the provider and
// backend-tier env vars cleared so aisotropy.New() falls through to its native
// keyless stub and in-process stores (see newProviderClient's doc comment).
func TestNewAgnesClient_StubModeReturnsNil(t *testing.T) {
	t.Setenv("ARENA_STUB", "1")
	t.Setenv("OPENAI_API_KEY", "leftover-from-run-sh") // must be cleared, not used

	client, err := newProviderClient()
	if err != nil {
		t.Fatalf("newProviderClient in stub mode returned error: %v", err)
	}
	if client != nil {
		t.Fatalf("newProviderClient in stub mode returned a non-nil client (%T) — stub runs must not build a provider client", client)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Fatalf("stub mode left OPENAI_API_KEY set (%q) — a surviving provider key makes a \"stub\" run silently call the real model (see providerEnvVars in app.go)", got)
	}
}
