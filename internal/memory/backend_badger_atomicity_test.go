package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The conformance suite states it cannot prove AppendMessageWithEvent's
// atomicity, and that is true of a BLACK-BOX suite: it cannot kill a process
// mid-commit and it cannot make a driver's write fail on demand.
//
// Inside the driver's own package that limit is smaller than it first looks.
// Crash-atomicity is still out of reach, but TRANSACTION atomicity — the half
// that distinguishes "one transaction" from "two writes in a row" — is testable,
// and it is the half a wrong implementation actually gets wrong. Nobody writes a
// driver that survives a kill -9 by accident; plenty write two sequential Sets
// and call it atomic.
//
// The lever is the transaction size limit. Make the EVENT oversized, not the
// message, and the two implementations diverge cleanly:
//
//	one transaction  -> Set(message) ok, Set(event) fails, whole txn discarded
//	                    => NEITHER is stored
//	two transactions -> Set(message) COMMITS, Set(event) fails
//	                    => the message survives without its event
//
// That surviving message is exactly the corruption ADR-0081's first constraint
// forbids: a turn in working memory with no pending consolidation event, which
// the drain can never notice is missing.

func TestBadgerBackend_AppendMessageWithEventIsOneTransaction(t *testing.T) {
	b, err := OpenBadgerWorkingBackend()
	if err != nil {
		t.Fatalf("OpenBadgerWorkingBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	ctx := context.Background()
	const ns, session = "ns-atomic", "s1"

	// An event whose encoded form cannot fit in one Badger transaction.
	oversized := strings.Repeat("x", int(b.db.MaxBatchSize())+(1<<20))
	msg := BackendMessage{Seq: 1, Role: "user", Content: "a small message", Timestamp: time.Unix(1_700_000_000, 0).UTC()}
	event := BackendEvent{
		ID:        "evt-1",
		Tenant:    "acme",
		User:      "alice",
		Session:   session,
		Content:   oversized,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}

	if err := b.AppendMessageWithEvent(ctx, ns, session, msg, event); err == nil {
		t.Fatal("an event too large for one transaction must surface an error, not be silently dropped")
	}

	// The assertion: the message must NOT be there. If it is, the driver
	// committed it before attempting the event, and the pair is not atomic.
	msgs, err := b.Recent(ctx, ns, session, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("the message survived a failed paired write (%d stored): the driver committed it "+
			"separately from the event, so a turn can exist with no consolidation event behind it — "+
			"the exact split ADR-0081's first constraint forbids", len(msgs))
	}

	events, err := b.PendingEvents(ctx)
	if err != nil {
		t.Fatalf("PendingEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("an event that could not be written is present anyway (%d)", len(events))
	}

	// And the store is still usable afterwards: a rolled-back transaction must
	// not poison the session it touched.
	if err := b.AppendMessageWithEvent(ctx, ns, session, msg, BackendEvent{
		ID: "evt-2", Tenant: "acme", User: "alice", Session: session,
		Content: "small", CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}); err != nil {
		t.Fatalf("the backend must still accept writes after a rolled-back transaction: %v", err)
	}
	if msgs, err := b.Recent(ctx, ns, session, 10); err != nil || len(msgs) != 1 {
		t.Fatalf("after recovery: %d message(s), err=%v; want 1", len(msgs), err)
	}
}
