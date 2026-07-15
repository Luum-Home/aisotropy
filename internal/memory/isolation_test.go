package memory

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// This file is the store-level adversarial isolation suite for the
// working-memory tier. It locks the constitutional multi-tenant gate into CI at
// the BadgerDB key-prefix layer: Recent and Summary reads scoped to one
// (tenant,user,session) Key never observe another Key's messages or summary —
// including the tenantA/userB and tenantB/userA cross combinations and a shared
// session id across tenants. Isolation here is structural: every read/write is
// bounded to its Key's prefix, so a foreign scope is unreachable by an
// iterator, not merely filtered out.
//
// All tests use OpenInMemory (no on-disk footprint, no external service). Run
// with -race to catch cross-scope bleed and data races in the concurrent path.

func newWorkingMemory(t *testing.T, maxActive int) *WorkingMemory {
	t.Helper()
	w, err := OpenInMemory(maxActive, NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// TestIsolationMemoryRecentPrefixIsolation writes distinct, scope-tagged
// messages under four Keys that deliberately SHARE the same session id but
// differ on the tenant and/or user axis, then asserts each Key's Recent returns
// only its own messages. A prefix that omitted either the tenant or the user
// segment would let the cross combinations bleed together.
func TestIsolationMemoryRecentPrefixIsolation(t *testing.T) {
	w := newWorkingMemory(t, 100)
	ctx := context.Background()

	const session = "shared-session-id"
	keys := map[string]Key{
		"A":       {Tenant: "acme", User: "alice", Session: session},
		"B":       {Tenant: "globex", User: "bob", Session: session},
		"crossTU": {Tenant: "acme", User: "bob", Session: session},
		"crossUT": {Tenant: "globex", User: "alice", Session: session},
	}
	// Each scope writes messages whose content embeds its own tag.
	for tag, key := range keys {
		for i := 0; i < 5; i++ {
			msg := Message{Role: "user", Content: fmt.Sprintf("secret-%s-%d", tag, i)}
			if err := w.Append(ctx, key, msg); err != nil {
				t.Fatalf("Append %s-%d: %v", tag, i, err)
			}
		}
	}

	for tag, key := range keys {
		got, err := w.Recent(ctx, key, 100)
		if err != nil {
			t.Fatalf("Recent %s: %v", tag, err)
		}
		if len(got) != 5 {
			t.Fatalf("Recent under %s returned %d messages, want 5 (cross-scope bleed): %+v", tag, len(got), got)
		}
		wantPrefix := "secret-" + tag + "-"
		for _, m := range got {
			if !strings.HasPrefix(m.Content, wantPrefix) {
				t.Fatalf("Recent under %s returned a foreign message %q (want prefix %q) — key-prefix isolation leak", tag, m.Content, wantPrefix)
			}
		}
	}
}

// TestIsolationMemorySummaryIsolation forces a condensation under scope A (by
// overflowing a tiny active window) so A's summary buffer is populated with A's
// message content, then asserts scope B's Summary is empty and never contains
// A's content. Summary lives at a Key-scoped key (summaryKey), so B's read
// cannot reach A's summary.
func TestIsolationMemorySummaryIsolation(t *testing.T) {
	w := newWorkingMemory(t, 2) // tiny window forces condensation
	ctx := context.Background()

	a := Key{Tenant: "acme", User: "alice", Session: "s"}
	b := Key{Tenant: "globex", User: "bob", Session: "s"}

	for i := 0; i < 8; i++ {
		if err := w.Append(ctx, a, Message{Role: "user", Content: fmt.Sprintf("alice-private-note-%d", i)}); err != nil {
			t.Fatalf("Append a-%d: %v", i, err)
		}
	}
	w.Sync() // wait for background condensation to converge

	sumA, err := w.Summary(ctx, a)
	if err != nil {
		t.Fatalf("Summary A: %v", err)
	}
	if sumA == "" {
		t.Fatal("expected scope A to have a non-empty summary after overflow-driven condensation")
	}
	if !strings.Contains(sumA, "alice-private-note") {
		t.Fatalf("A's summary should contain A's condensed content, got %q", sumA)
	}

	sumB, err := w.Summary(ctx, b)
	if err != nil {
		t.Fatalf("Summary B: %v", err)
	}
	if sumB != "" {
		t.Fatalf("scope B's summary is non-empty (%q) though only A was written — cross-scope summary leak", sumB)
	}
	if strings.Contains(sumB, "alice-private-note") {
		t.Fatalf("A's private content leaked into B's summary: %q", sumB)
	}
}

// TestIsolationMemoryConcurrent runs parallel writers and readers under scopes A
// and B (each using its own set of sessions, honoring the single-writer-per-key
// contract) and asserts no reader ever observes the other scope's content.
// Under -race it also guards the shared BadgerDB instance's concurrent access.
func TestIsolationMemoryConcurrent(t *testing.T) {
	w := newWorkingMemory(t, 100)
	ctx := context.Background()

	const perScope = 40
	var wg sync.WaitGroup

	// Each (scope,i) pair is a distinct session key: one writer per key,
	// preserving the tier's single-writer-per-key invariant.
	writer := func(tenant, user, tag string) {
		defer wg.Done()
		for i := 0; i < perScope; i++ {
			key := Key{Tenant: tenant, User: user, Session: fmt.Sprintf("sess-%d", i)}
			msg := Message{Role: "user", Content: fmt.Sprintf("payload-%s-%d", tag, i)}
			if err := w.Append(ctx, key, msg); err != nil {
				t.Errorf("Append %s-%d: %v", tag, i, err)
				return
			}
		}
	}
	reader := func(tenant, user, foreignTag string) {
		defer wg.Done()
		for i := 0; i < perScope; i++ {
			key := Key{Tenant: tenant, User: user, Session: fmt.Sprintf("sess-%d", i)}
			got, err := w.Recent(ctx, key, 100)
			if err != nil {
				t.Errorf("Recent %s/%s sess-%d: %v", tenant, user, i, err)
				return
			}
			for _, m := range got {
				if strings.Contains(m.Content, "payload-"+foreignTag+"-") {
					t.Errorf("CROSS-SCOPE LEAK: reader (%s,%s) saw foreign content %q", tenant, user, m.Content)
					return
				}
			}
		}
	}

	wg.Add(4)
	go writer("acme", "alice", "A")
	go writer("globex", "bob", "B")
	go reader("acme", "alice", "B")
	go reader("globex", "bob", "A")
	wg.Wait()
}

// TestIsolationMemoryEveryReadTakesKey is the structural omitted-filter guard
// for the working-memory tier: it has NO unscoped read (no ExportAll
// equivalent). Every method that returns stored messages MUST take a Key, and
// Summary MUST take a Key — so scope always travels with the read. If a new
// message- or summary-returning method is added without a Key parameter, this
// fails, forcing the isolation intent to be reviewed.
func TestIsolationMemoryEveryReadTakesKey(t *testing.T) {
	wmType := reflect.TypeOf(&WorkingMemory{})
	keyType := reflect.TypeOf(Key{})
	msgSlice := reflect.TypeOf([]Message(nil))

	takesKey := func(m reflect.Method) bool {
		for i := 0; i < m.Type.NumIn(); i++ {
			if m.Type.In(i) == keyType {
				return true
			}
		}
		return false
	}

	// 1. Every method returning []Message must take a Key.
	var leaky []string
	for i := 0; i < wmType.NumMethod(); i++ {
		m := wmType.Method(i)
		returnsMsgs := false
		for j := 0; j < m.Type.NumOut(); j++ {
			if m.Type.Out(j) == msgSlice {
				returnsMsgs = true
			}
		}
		if returnsMsgs && !takesKey(m) {
			leaky = append(leaky, m.Name)
		}
	}
	if len(leaky) != 0 {
		t.Fatalf("working-memory methods return []Message without a Key (unscoped read = isolation leak): %v", leaky)
	}

	// 2. The two named scoped reads must each take a Key (belt-and-suspenders
	// against a signature change that drops the Key).
	for _, name := range []string{"Recent", "Summary"} {
		m, ok := wmType.MethodByName(name)
		if !ok {
			t.Fatalf("expected WorkingMemory to expose a %q read method", name)
		}
		if !takesKey(m) {
			t.Fatalf("WorkingMemory.%s no longer takes a Key — its read would be unscoped", name)
		}
	}
}
