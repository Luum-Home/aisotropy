package orbit

import (
	"context"
	"strings"
	"testing"
)

// TestOrbitChat drives the real compiled Eino chain and asserts working-memory
// notes are echoed.
func TestOrbitChat(t *testing.T) {
	ctx := context.Background()
	ob, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ob.Remember("acme", "alice", "s1", "gold-tier customer")
	reply, err := ob.Chat(ctx, "acme", "alice", "s1", "hello")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(reply, "gold-tier") {
		t.Fatalf("expected memory note echoed, got %q", reply)
	}
}

// TestOrbitStream asserts the real runnable Stream() yields multiple chunks.
func TestOrbitStream(t *testing.T) {
	ctx := context.Background()
	ob, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	full, n, err := ob.Stream(ctx, "acme", "bob", "s2", "where is my package now")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if n < 2 || full == "" {
		t.Fatalf("expected multiple non-empty chunks, got n=%d full=%q", n, full)
	}
}

// TestOrbitAgentTool drives the real ReAct agent + real Eino tool.
func TestOrbitAgentTool(t *testing.T) {
	ctx := context.Background()
	ob, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ans, err := ob.Agent(ctx, "what is the status of my order")
	if err != nil {
		t.Fatalf("Agent: %v", err)
	}
	if !strings.Contains(strings.ToLower(ans), "shipped") {
		t.Fatalf("expected tool result folded in, got %q", ans)
	}
}

// TestIsolationPositiveControl: a tenant reading its OWN checkpoint ID sees its
// secret (through Eino's real checkpoint resume).
func TestIsolationPositiveControl(t *testing.T) {
	ctx := context.Background()
	h, err := NewIsolationHarness(ctx)
	if err != nil {
		t.Fatalf("NewIsolationHarness: %v", err)
	}
	const secret = "SECRET-abc123"
	if err := h.Seed(ctx, "tenantA:sess", secret); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got, hit, err := h.Read(ctx, "tenantA:sess")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !hit || got != secret {
		t.Fatalf("positive control failed: hit=%v got=%q", hit, got)
	}
}

// TestIsolationCorrectPathNoLeak: distinct tenant-scoped IDs do not cross-read.
func TestIsolationCorrectPathNoLeak(t *testing.T) {
	ctx := context.Background()
	h, err := NewIsolationHarness(ctx)
	if err != nil {
		t.Fatalf("NewIsolationHarness: %v", err)
	}
	if err := h.Seed(ctx, "tenantA:sess", "SECRET-A"); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got, hit, _ := h.Read(ctx, "tenantB:sess")
	if hit && got == "SECRET-A" {
		t.Fatalf("unexpected cross-read on correct path: got %q", got)
	}
}

// TestIsolationSharedIDLeaks: THE crux. Two tenants sharing one checkpoint ID
// cross-read via Eino's real runtime. Eino provides no mechanism to prevent it,
// so this leak is the EXPECTED (documented) outcome for feature #1.
func TestIsolationSharedIDLeaks(t *testing.T) {
	ctx := context.Background()
	h, err := NewIsolationHarness(ctx)
	if err != nil {
		t.Fatalf("NewIsolationHarness: %v", err)
	}
	const secret = "SECRET-tenantA-only"
	// Tenant A seeds under a bare, non-tenant-scoped session id.
	if err := h.Seed(ctx, "shared-session", secret); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	// Tenant B presents the same session id -> Eino resumes tenant A's state.
	got, hit, err := h.Read(ctx, "shared-session")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !hit || got != secret {
		t.Fatalf("expected the documented cross-tenant leak, but did not reproduce: hit=%v got=%q", hit, got)
	}
	// Evidence: the real checkpoint store keys state by a bare string.
	keys := h.Store().Keys()
	if len(keys) != 1 || keys[0] != "shared-session" {
		t.Fatalf("expected bare-string keyspace [shared-session], got %v", keys)
	}
}
