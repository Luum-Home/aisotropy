package aisotropy

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// countingLLM wraps a client and counts calls. IngestHistory's central claim is
// "zero model calls"; a comment claiming that is worth nothing, a counter that
// fails the build is worth something.
type countingLLM struct {
	inner llm.Client
	calls atomic.Int64
}

func (c *countingLLM) StreamChat(ctx context.Context, system, prompt string) (<-chan llm.Token, error) {
	c.calls.Add(1)
	return c.inner.StreamChat(ctx, system, prompt)
}

func (c *countingLLM) Name() string { return c.inner.Name() }

func newHistoryApp(t *testing.T) (*App, *countingLLM) {
	t.Helper()
	counter := &countingLLM{inner: llm.NewStubClient()}
	app := New(WithLLM(counter))
	t.Cleanup(func() { _ = app.Close() })
	return app, counter
}

// TestIngestHistory_CostsZeroModelCalls is the whole point of the API: loading
// prior context must not pay an inference bill for text the caller already has.
func TestIngestHistory_CostsZeroModelCalls(t *testing.T) {
	app, counter := newHistoryApp(t)

	msgs := []HistoryMessage{
		{Role: HistoryRoleUser, Content: "My spare key is under the blue flowerpot."},
		{Role: HistoryRoleAssistant, Content: "Noted."},
		{Role: HistoryRoleUser, Content: "Also, my allergy is shellfish."},
		{Role: HistoryRoleAssistant, Content: "Understood."},
	}

	n, err := app.IngestHistory(context.Background(), "acme", "alice", "s1", msgs...)
	if err != nil {
		t.Fatalf("IngestHistory: %v", err)
	}
	if n != len(msgs) {
		t.Errorf("persisted %d turns, want %d", n, len(msgs))
	}
	if got := counter.calls.Load(); got != 0 {
		t.Errorf("IngestHistory made %d model call(s); want 0 — the entire reason "+
			"this API exists is that App.Turn charges one call per turn", got)
	}
}

// TestIngestHistory_RejectsInvalidScope — the compound key is the isolation
// boundary; an ingest that could write with an empty or malformed scope
// component would be a hole in it.
func TestIngestHistory_RejectsInvalidScope(t *testing.T) {
	app, counter := newHistoryApp(t)
	msg := HistoryMessage{Role: HistoryRoleUser, Content: "hello"}

	cases := []struct{ name, tenant, user, session string }{
		{"empty tenant", "", "alice", "s1"},
		{"empty user", "acme", "", "s1"},
		{"empty session", "acme", "alice", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := app.IngestHistory(context.Background(), tc.tenant, tc.user, tc.session, msg)
			if err == nil {
				t.Fatalf("expected an error for %s, persisted %d", tc.name, n)
			}
			if n != 0 {
				t.Errorf("wrote %d turn(s) despite an invalid scope", n)
			}
		})
	}
	if got := counter.calls.Load(); got != 0 {
		t.Errorf("a rejected ingest still made %d model call(s)", got)
	}
}

// TestIngestHistory_RejectsUnknownRole — a typo'd role must fail loudly rather
// than persisting a turn the rest of the pipeline cannot classify.
func TestIngestHistory_RejectsUnknownRole(t *testing.T) {
	app, _ := newHistoryApp(t)

	n, err := app.IngestHistory(context.Background(), "acme", "alice", "s1",
		HistoryMessage{Role: HistoryRoleUser, Content: "first"},
		HistoryMessage{Role: HistoryRole("system"), Content: "second"},
	)
	if err == nil {
		t.Fatal("expected an error for an unknown role")
	}
	if !strings.Contains(err.Error(), "unknown role") {
		t.Errorf("error should name the problem, got: %v", err)
	}
	// Partial-progress contract: the caller must be able to tell "nothing
	// landed" from "the tail failed".
	if n != 1 {
		t.Errorf("expected the count of turns that DID persist (1), got %d", n)
	}
}

// TestIngestHistory_SkipsEmptyTurns — a blank turn carries no context and no
// fact; persisting it would only pollute the buffer and the outbox.
func TestIngestHistory_SkipsEmptyTurns(t *testing.T) {
	app, _ := newHistoryApp(t)

	n, err := app.IngestHistory(context.Background(), "acme", "alice", "s1",
		HistoryMessage{Role: HistoryRoleUser, Content: "  "},
		HistoryMessage{Role: HistoryRoleUser, Content: "real content"},
		HistoryMessage{Role: HistoryRoleAssistant, Content: ""},
	)
	if err != nil {
		t.Fatalf("IngestHistory: %v", err)
	}
	if n != 1 {
		t.Errorf("persisted %d turns, want 1 (the two blank ones are skipped)", n)
	}
}

// TestIngestHistory_EmptyInputIsNoop — calling with nothing is not an error;
// a migration loop over an empty transcript should not have to special-case it.
func TestIngestHistory_EmptyInputIsNoop(t *testing.T) {
	app, counter := newHistoryApp(t)

	n, err := app.IngestHistory(context.Background(), "acme", "alice", "s1")
	if err != nil || n != 0 {
		t.Errorf("empty ingest: got (%d, %v), want (0, nil)", n, err)
	}
	if got := counter.calls.Load(); got != 0 {
		t.Errorf("empty ingest made %d model call(s)", got)
	}
}

// TestHistoryEventID_PositionDisambiguatesRepeats — a transcript legitimately
// repeats lines ("yes"). Two identical turns at different positions are two
// consolidation intents, not one retried write, so their event IDs must differ;
// the same turn at the same position must stay stable so a re-ingest overwrites
// instead of duplicating.
func TestHistoryEventID_PositionDisambiguatesRepeats(t *testing.T) {
	key := historyTestKey()

	a := historyEventID(key, "yes", 3)
	b := historyEventID(key, "yes", 7)
	if a == b {
		t.Error("identical content at different positions collided — the second turn would overwrite the first's consolidation intent")
	}
	if again := historyEventID(key, "yes", 3); again != a {
		t.Error("event id is not stable for the same (scope, content, position) — a re-ingest would duplicate instead of overwrite")
	}

	other := historyEventID(historyTestKeyOtherTenant(), "yes", 3)
	if other == a {
		t.Error("event id ignores the tenant — two tenants' turns would share an outbox key")
	}
}

func historyTestKey() memory.Key {
	return memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
}

func historyTestKeyOtherTenant() memory.Key {
	return memory.Key{Tenant: "globex", User: "alice", Session: "s1"}
}
