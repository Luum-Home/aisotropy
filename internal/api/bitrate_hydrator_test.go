package api

import (
	"context"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// TestHydrateNilWMExcludesLiveBuffer locks the Bet 6 leakage guard: the bpt
// "with memory" context (built via a nil working memory) must NOT contain the
// live session buffer, because that buffer still holds the held-out turn being
// scored. The test first establishes the leak baseline (a real wm DOES surface
// the turn), then proves the nil-wm path (BitrateHydrator.ContextWith) does not.
func TestHydrateNilWMExcludesLiveBuffer(t *testing.T) {
	ctx := context.Background()
	wm := newTestWorkingMemory(t)
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	const heldOut = "SECRET_HELDOUT_meeting_tuesday_3pm_finance"

	if err := wm.Append(ctx, key, memory.Message{Role: "user", Content: heldOut}); err != nil {
		t.Fatalf("Append held-out turn: %v", err)
	}
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}

	// Baseline: with the live working memory, the held-out turn leaks into the
	// prompt (this is exactly what the request path does, and why bpt must not).
	withWM, err := hydrateSystemPrompt(ctx, wm, nil, nil, nil, nil, nil, key, scope, "p1", "meeting")
	if err != nil {
		t.Fatalf("hydrate with wm: %v", err)
	}
	if !strings.Contains(withWM, heldOut) {
		t.Fatalf("baseline: expected live buffer to include the held-out turn, got: %q", withWM)
	}

	// The bpt path (nil wm): the live buffer — and thus the held-out turn — must
	// be absent. Consolidated memory (facts/semantic) is nil here, so the result
	// is the bare persona base with no session content.
	nilWM, err := hydrateSystemPrompt(ctx, nil, nil, nil, nil, nil, nil, key, scope, "p1", "meeting")
	if err != nil {
		t.Fatalf("hydrate nil wm: %v", err)
	}
	if strings.Contains(nilWM, heldOut) {
		t.Fatalf("leakage: held-out turn appeared with nil wm: %q", nilWM)
	}
}
