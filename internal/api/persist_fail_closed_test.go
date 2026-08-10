package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	badger "github.com/dgraph-io/badger/v4"

	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// What this file pins, and why the obvious test would have passed for the wrong
// reason.
//
// A failed user-turn write used to be logged and stepped over. The turn then ran
// to completion, so the model was called, the reply was returned with a 200, and
// chatRecordTurn appended that reply to working memory — leaving an assistant
// utterance with no prompt in front of it. Every later turn in the session
// hydrates from that. The bug is not the lost write; it is the corrupted
// transcript the caller keeps using afterwards, and the 200 that hides it.
//
// The lever matters. The tempting way to force the failure is a cancelled ctx,
// but hydration reads working memory FIRST and treats its failure as fatal, so
// the request would die at hydrate with a 500 and the assertion would never
// reach the code under test. A closed database has the same problem. So these
// tests use a READ-ONLY BadgerDB: reads succeed and return empty, hydration is
// happy, and only the write fails — which is precisely the state being claimed.
//
// Found by scripts/robustness-audit.sh (BLOCKER PERSIST_AND_CONTINUE).

// newReadOnlyWorkingMemory returns a WorkingMemory whose reads work and whose
// writes fail with badger.ErrReadOnlyTxn, on a live (never cancelled) context.
func newReadOnlyWorkingMemory(t *testing.T) *memory.WorkingMemory {
	t.Helper()
	dir := t.TempDir()

	// Create the store, then release it: badger refuses read-only mode on a
	// directory that has no manifest, and holds a lock while open.
	seed, err := badger.Open(badger.DefaultOptions(dir))
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	db, err := badger.Open(badger.DefaultOptions(dir).WithReadOnly(true))
	if err != nil {
		t.Skipf("read-only badger unavailable in this environment: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return memory.NewWorkingMemory(db, 0, memory.NoopSummarizer{})
}

// TestReadOnlyWorkingMemory_ReadsSucceedWritesFail proves the lever itself is
// what the other tests assume. Without this, a passing fail-closed test could be
// passing because hydration broke, not because the persist did.
func TestReadOnlyWorkingMemory_ReadsSucceedWritesFail(t *testing.T) {
	wm := newReadOnlyWorkingMemory(t)
	ctx := context.Background()
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}

	if _, err := wm.Summary(ctx, key); err != nil {
		t.Fatalf("read must succeed on a read-only store, got %v", err)
	}
	if _, err := wm.Recent(ctx, key, 10); err != nil {
		t.Fatalf("read must succeed on a read-only store, got %v", err)
	}
	if err := wm.Append(ctx, key, memory.Message{Role: roleUser, Content: "hi"}); err == nil {
		t.Fatal("write must fail on a read-only store; the fail-closed tests would be vacuous")
	}
}

// TestChatCore_UserTurnPersistFailureRejectsBeforeTheModelCall is the whole
// point: a 503, and a model that was never called.
func TestChatCore_UserTurnPersistFailureRejectsBeforeTheModelCall(t *testing.T) {
	client := &countingClient{reply: "an answer nobody should ever receive"}
	deps := ChatDeps{
		Client: client,
		WM:     newReadOnlyWorkingMemory(t),
		Guard:  &Guard{G: guardrails.Default()},
	}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	out, err := ChatCore(context.Background(), deps, in)
	if err == nil {
		t.Fatalf("a turn whose user message could not be recorded must not succeed; got output %+v", out)
	}
	se, ok := err.(huma.StatusError)
	if !ok || se.GetStatus() != http.StatusServiceUnavailable {
		t.Fatalf("error = %v, want a 503 StatusError (retryable: nothing was spent and nothing was written)", err)
	}
	// The reason this is a 503 and not a 500: the failure is pre-spend, so the
	// client can retry for free. That property is only real if the model was
	// genuinely not called.
	if n := client.callCount(); n != 0 {
		t.Errorf("the model was called %d time(s) after the user turn failed to persist; "+
			"the abort must happen BEFORE any token is spent", n)
	}
}

// TestStreamCore_UserTurnPersistFailureAbortsBeforeTheModelCall is the same
// contract on the streaming path, which had the identical shape and was not the
// line the audit cited — fixing only the cited one would have left the defect
// live on the route that carries most traffic.
func TestStreamCore_UserTurnPersistFailureAbortsBeforeTheModelCall(t *testing.T) {
	client := &countingClient{reply: "an answer nobody should ever receive"}
	deps := StreamDeps{
		Client: client,
		WM:     newReadOnlyWorkingMemory(t),
		Guard:  &Guard{G: guardrails.Default()},
	}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	var tokens []string
	sink := StreamSink{OnToken: func(tok string) error { tokens = append(tokens, tok); return nil }}

	err := StreamCore(context.Background(), deps, in, sink)
	if err == nil {
		t.Fatal("a stream whose user message could not be recorded must not complete cleanly")
	}
	if !strings.Contains(err.Error(), "could not record the user turn") {
		t.Errorf("error = %q, want it to name the cause the client can act on", err.Error())
	}
	if n := client.callCount(); n != 0 {
		t.Errorf("the model was called %d time(s) after the user turn failed to persist", n)
	}
	if len(tokens) != 0 {
		t.Errorf("streamed %d token(s) despite the aborted turn: %q", len(tokens), tokens)
	}
}
