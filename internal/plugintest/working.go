package plugintest

import (
	"context"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// WorkingBackendFactory builds a fresh, empty memory.WorkingBackend for one
// conformance run.
type WorkingBackendFactory func() memory.WorkingBackend

// RawWorkingInspector is an OPTIONAL interface a memory.WorkingBackend may
// implement to let the conformance suite demonstrate the isolation SEAL
// directly, exactly as RawDocInspector and RawVectorInspector do for the other
// two tiers. Namespaces returns every namespace the backend currently holds
// data under.
type RawWorkingInspector interface {
	Namespaces() []string
}

// RunWorkingBackendSuite runs the memory.WorkingBackend conformance suite
// (ADR-0081) against backends produced by factory. It fails t if the backend,
// once sealed by memory.NewWorkingBackendStore, violates the working-tier
// contract or the multi-tenant isolation gate.
//
// WHAT THIS SUITE CANNOT PROVE, stated because the gap is the interesting part:
// AppendMessageWithEvent promises atomicity under a crash mid-commit, and no
// black-box suite can kill a process at the right instant. What is checked here
// is the observable half — both records appear together, and re-writing the same
// event id does not duplicate it. A driver that satisfies this suite has NOT
// been shown to be crash-atomic; that obligation is the implementer's, provable
// only inside its own store. Reporting this suite as proof of the ADR's first
// constraint would be exactly the vacuous pass the repository's gates exist to
// refuse.
func RunWorkingBackendSuite(t *testing.T, factory WorkingBackendFactory) {
	t.Helper()
	t.Run("RoundTrip", func(t *testing.T) { workingRoundTrip(t, factory) })
	t.Run("Ordering", func(t *testing.T) { workingOrdering(t, factory) })
	t.Run("Summary", func(t *testing.T) { workingSummary(t, factory) })
	t.Run("OutboxLifecycle", func(t *testing.T) { workingOutboxLifecycle(t, factory) })
	t.Run("EventIdempotency", func(t *testing.T) { workingEventIdempotency(t, factory) })
	t.Run("AppendWithEventBothVisible", func(t *testing.T) { workingAppendWithEvent(t, factory) })
	t.Run("EraseSubject", func(t *testing.T) { workingEraseSubject(t, factory) })
	t.Run("InvalidIdentity", func(t *testing.T) { workingInvalidIdentity(t, factory) })
	// The central proof: the decorator seals tenant isolation.
	t.Run("Isolation", func(t *testing.T) { workingIsolation(t, factory) })
}

func workingStore(t *testing.T, factory WorkingBackendFactory) interface {
	Append(context.Context, memory.Key, memory.Message) error
	AppendWithOutbox(context.Context, memory.Key, memory.Message, memory.OutboxEvent) error
	Recent(context.Context, memory.Key, int) ([]memory.Message, error)
	LookupSummary(context.Context, memory.Key) (string, bool, error)
	SessionExists(context.Context, memory.Key) (bool, error)
	EraseSubject(context.Context, string, string) (int, error)
	PendingOutboxEvents(context.Context) ([]memory.OutboxEvent, error)
	DeadOutboxEvents(context.Context) ([]memory.OutboxEvent, error)
	PutOutboxEvent(context.Context, memory.OutboxEvent, bool) error
	DeleteOutboxEvent(context.Context, memory.OutboxEvent) error
	Close() error
} {
	t.Helper()
	st, err := memory.NewWorkingBackendStore(factory())
	if err != nil {
		t.Fatalf("NewWorkingBackendStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func workingKey(tenant, user, session string) memory.Key {
	return memory.Key{Tenant: tenant, User: user, Session: session}
}

func workingEvent(k memory.Key, id, content string) memory.OutboxEvent {
	return memory.NewOutboxEvent(k, id, content, time.Unix(1_700_000_000, 0).UTC())
}

func workingRoundTrip(t *testing.T, factory WorkingBackendFactory) {
	st := workingStore(t, factory)
	ctx := context.Background()
	k := workingKey("acme", "alice", "s1")

	if exists, err := st.SessionExists(ctx, k); err != nil || exists {
		t.Fatalf("a fresh session must not exist: exists=%v err=%v", exists, err)
	}
	if err := st.Append(ctx, k, memory.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := st.Recent(ctx, k, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Content != "hello" || got[0].Role != "user" {
		t.Fatalf("Recent = %+v, want one user/hello message", got)
	}
	if got[0].Timestamp.IsZero() {
		t.Error("a zero timestamp must be filled in before persisting")
	}
	if exists, err := st.SessionExists(ctx, k); err != nil || !exists {
		t.Fatalf("session must exist after a write: exists=%v err=%v", exists, err)
	}
}

func workingOrdering(t *testing.T, factory WorkingBackendFactory) {
	st := workingStore(t, factory)
	ctx := context.Background()
	k := workingKey("acme", "alice", "s1")

	for _, c := range []string{"first", "second", "third"} {
		if err := st.Append(ctx, k, memory.Message{Role: "user", Content: c}); err != nil {
			t.Fatalf("Append(%s): %v", c, err)
		}
	}
	got, err := st.Recent(ctx, k, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 || got[0].Content != "first" || got[2].Content != "third" {
		t.Fatalf("Recent must return oldest-first, got %+v", got)
	}
	// The n cap takes the MOST RECENT, still oldest-first among them: a backend
	// that truncates from the wrong end returns a plausible-looking slice that
	// silently drops the newest turn from hydration.
	tail, err := st.Recent(ctx, k, 2)
	if err != nil {
		t.Fatalf("Recent(2): %v", err)
	}
	if len(tail) != 2 || tail[0].Content != "second" || tail[1].Content != "third" {
		t.Fatalf("Recent(2) = %+v, want the two NEWEST oldest-first", tail)
	}
	// Sessions are separate inside one subject.
	other := workingKey("acme", "alice", "s2")
	if got, err := st.Recent(ctx, other, 10); err != nil || len(got) != 0 {
		t.Fatalf("a different session must be empty: got %+v err=%v", got, err)
	}
}

func workingSummary(t *testing.T, factory WorkingBackendFactory) {
	st := workingStore(t, factory)
	ctx := context.Background()
	k := workingKey("acme", "alice", "s1")

	if _, ok, err := st.LookupSummary(ctx, k); err != nil || ok {
		t.Fatalf("no summary should exist yet: ok=%v err=%v", ok, err)
	}
}

func workingAppendWithEvent(t *testing.T, factory WorkingBackendFactory) {
	st := workingStore(t, factory)
	ctx := context.Background()
	k := workingKey("acme", "alice", "s1")

	if err := st.AppendWithOutbox(ctx, k, memory.Message{Role: "user", Content: "hi"}, workingEvent(k, "e1", "hi")); err != nil {
		t.Fatalf("AppendWithOutbox: %v", err)
	}
	msgs, err := st.Recent(ctx, k, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	events, err := st.PendingOutboxEvents(ctx)
	if err != nil {
		t.Fatalf("PendingOutboxEvents: %v", err)
	}
	// Both halves, or the guarantee the consolidation pipeline rests on is not
	// there. (Crash-atomicity is NOT what this asserts — see the suite doc.)
	if len(msgs) != 1 || len(events) != 1 {
		t.Fatalf("message and event must both land: %d message(s), %d event(s)", len(msgs), len(events))
	}
	if events[0].Tenant != "acme" || events[0].User != "alice" || events[0].Session != "s1" {
		t.Errorf("the event must round-trip its identity verbatim (the cross-tenant drain reconstructs scope from it), got %+v", events[0])
	}
}

func workingEventIdempotency(t *testing.T, factory WorkingBackendFactory) {
	st := workingStore(t, factory)
	ctx := context.Background()
	k := workingKey("acme", "alice", "s1")
	ev := workingEvent(k, "same-id", "hi")

	for i := 0; i < 3; i++ {
		if err := st.PutOutboxEvent(ctx, ev, false); err != nil {
			t.Fatalf("PutOutboxEvent #%d: %v", i, err)
		}
	}
	events, err := st.PendingOutboxEvents(ctx)
	if err != nil {
		t.Fatalf("PendingOutboxEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("writing the same event id 3x produced %d rows, want 1 — "+
			"ADR-0081 makes this idempotency load-bearing rather than a nicety", len(events))
	}
}

func workingOutboxLifecycle(t *testing.T, factory WorkingBackendFactory) {
	st := workingStore(t, factory)
	ctx := context.Background()
	k := workingKey("acme", "alice", "s1")
	ev := workingEvent(k, "e1", "hi")

	if err := st.PutOutboxEvent(ctx, ev, false); err != nil {
		t.Fatalf("put pending: %v", err)
	}
	// Park it: it must leave pending and appear in dead, in exactly one place.
	ev.Attempts = 10
	ev.LastError = "sink refused"
	if err := st.PutOutboxEvent(ctx, ev, true); err != nil {
		t.Fatalf("park: %v", err)
	}
	pending, err := st.PendingOutboxEvents(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	dead, err := st.DeadOutboxEvents(ctx)
	if err != nil {
		t.Fatalf("dead: %v", err)
	}
	if len(pending) != 0 || len(dead) != 1 {
		t.Fatalf("a parked event must be in dead only: pending=%d dead=%d", len(pending), len(dead))
	}
	if dead[0].Attempts != 10 || dead[0].LastError != "sink refused" {
		t.Errorf("retry state must round-trip verbatim; a parked event is undiagnosable without it, got %+v", dead[0])
	}
	if err := st.DeleteOutboxEvent(ctx, ev); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if dead, err := st.DeadOutboxEvents(ctx); err != nil || len(dead) != 0 {
		t.Fatalf("after delete dead=%d err=%v", len(dead), err)
	}
	// Deleting an unknown id is not an error: the drain is at-least-once, so a
	// second drainer WILL delete an event the first already removed. A backend
	// that errors here turns a benign race into a failed pass.
	if err := st.DeleteOutboxEvent(ctx, ev); err != nil {
		t.Errorf("deleting an already-deleted event must be a no-op, got %v", err)
	}
}

func workingEraseSubject(t *testing.T, factory WorkingBackendFactory) {
	st := workingStore(t, factory)
	ctx := context.Background()
	victim := workingKey("acme", "alice", "s1")
	bystander := workingKey("acme", "bob", "s1")

	if err := st.AppendWithOutbox(ctx, victim, memory.Message{Role: "user", Content: "hi"}, workingEvent(victim, "e1", "hi")); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	if err := st.Append(ctx, bystander, memory.Message{Role: "user", Content: "unrelated"}); err != nil {
		t.Fatalf("seed bystander: %v", err)
	}

	n, err := st.EraseSubject(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("EraseSubject: %v", err)
	}
	if n <= 0 {
		t.Errorf("EraseSubject reported %d records removed; a receipt that undercounts overstates nothing but proves nothing either", n)
	}
	if got, err := st.Recent(ctx, victim, 10); err != nil || len(got) != 0 {
		t.Errorf("erased subject still has %d message(s), err=%v", len(got), err)
	}
	// The erase must drop the subject's OUTBOX rows too. This is the failure an
	// enumerate-and-filter erase makes: it deletes the shapes its author
	// remembered and leaves the rest, so tenant data survives a deletion receipt
	// that said it was gone.
	if events, err := st.PendingOutboxEvents(ctx); err != nil || len(events) != 0 {
		t.Errorf("erased subject left %d outbox event(s) behind, err=%v", len(events), err)
	}
	if got, err := st.Recent(ctx, bystander, 10); err != nil || len(got) != 1 {
		t.Errorf("erasing one subject must not touch another: bystander has %d message(s), err=%v", len(got), err)
	}
}

func workingInvalidIdentity(t *testing.T, factory WorkingBackendFactory) {
	st := workingStore(t, factory)
	ctx := context.Background()

	if err := st.Append(ctx, workingKey("", "alice", "s1"), memory.Message{Role: "user", Content: "x"}); err == nil {
		t.Error("an empty tenant must be rejected before the backend is reached")
	}
	if _, err := st.EraseSubject(ctx, "acme", ""); err == nil {
		t.Error("an empty user must be rejected: an unvalidated erase could target a namespace nobody owns")
	}
}

func workingIsolation(t *testing.T, factory WorkingBackendFactory) {
	backend := factory()
	st, err := memory.NewWorkingBackendStore(backend)
	if err != nil {
		t.Fatalf("NewWorkingBackendStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	a := workingKey("tenant-a", "alice", "s1")
	b := workingKey("tenant-b", "alice", "s1") // same user id, different tenant

	if err := st.AppendWithOutbox(ctx, a, memory.Message{Role: "user", Content: "tenant A secret"}, workingEvent(a, "e-a", "tenant A secret")); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := st.Append(ctx, b, memory.Message{Role: "user", Content: "tenant B message"}); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	// Identical user and session strings under a different tenant must not
	// collide. This is the case a naive key scheme gets wrong.
	got, err := st.Recent(ctx, b, 10)
	if err != nil {
		t.Fatalf("Recent(B): %v", err)
	}
	for _, m := range got {
		if m.Content == "tenant A secret" {
			t.Fatal("ISOLATION BREACH: tenant B read tenant A's message")
		}
	}
	if len(got) != 1 || got[0].Content != "tenant B message" {
		t.Fatalf("tenant B should see exactly its own message, got %+v", got)
	}

	// The seal itself, when the backend lets us look: the two subjects landed in
	// DIFFERENT namespaces, and no namespace contains a recoverable tenant id.
	// The backend is handed a hash it cannot decode — that is what makes a buggy
	// driver unable to widen a read past its namespace, rather than merely
	// unlikely to.
	//
	// Inspected BEFORE the erase below, and the ordering is not incidental: this
	// assertion first ran after it and failed reporting "1 namespace", because
	// erasing A had legitimately removed one. A seal check that runs after a
	// deletion measures the deletion.
	if insp, ok := backend.(RawWorkingInspector); ok {
		seen := map[string]bool{}
		for _, ns := range insp.Namespaces() {
			if ns == "" {
				t.Error("a backend must never be handed an empty namespace")
			}
			for _, leak := range []string{"tenant-a", "tenant-b", "alice"} {
				if containsSubstring(ns, leak) {
					t.Errorf("namespace %q leaks the identity %q to the driver; the seal exists to prevent exactly this", ns, leak)
				}
			}
			seen[ns] = true
		}
		if len(seen) < 2 {
			t.Errorf("two distinct subjects produced %d namespace(s); they must not share one", len(seen))
		}
	}

	// Erasing A must not touch B.
	if _, err := st.EraseSubject(ctx, "tenant-a", "alice"); err != nil {
		t.Fatalf("EraseSubject(A): %v", err)
	}
	if got, err := st.Recent(ctx, b, 10); err != nil || len(got) != 1 {
		t.Fatalf("erasing tenant A damaged tenant B: %d message(s), err=%v", len(got), err)
	}
}

func containsSubstring(haystack, needle string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
