package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestSummarizerPromptCarriesSupersessionContract pins the supersession clause
// into the condensation system prompt. Without it the summarizer has no rule
// for a later message overriding an earlier fact, and condenseAndPrune folds
// the resulting (wrong) paragraph in irreversibly — the mechanism behind the
// observed "seat 14C" summary for a session whose seat had since changed twice
// (docs/architecture/arena-memory-forensics.md §4).
func TestSummarizerPromptCarriesSupersessionContract(t *testing.T) {
	for _, want := range []string{
		"record ONLY the latest value",
		"never carry a superseded value forward",
	} {
		if !strings.Contains(summarizerSystemPrompt, want) {
			t.Errorf("summarizerSystemPrompt is missing the supersession instruction %q; prompt = %q", want, summarizerSystemPrompt)
		}
	}

	// The clause must actually reach the model, not just exist as a constant:
	// LLMSummarizer passes it as the system prompt on every condensation.
	client := &scriptedClient{tokens: []llm.Token{{Text: "ok"}, {Done: true}}}
	if _, err := (LLMSummarizer{Client: client}).Condense(context.Background(), "",
		[]Message{{Role: "user", Content: "seat 14C"}, {Role: "user", Content: "change it to 9A"}}); err != nil {
		t.Fatalf("Condense: %v", err)
	}
	if !strings.Contains(client.gotSystem, "record ONLY the latest value") {
		t.Errorf("system prompt handed to the client = %q, want it to carry the supersession instruction", client.gotSystem)
	}
}

// TestSummarizerPromptCarriesUniquenessContract pins the needle-preservation
// clause into the condensation system prompt. Without it the summarizer is free
// to paraphrase away a value that cannot be reconstructed from what survives —
// the mechanism behind mem-h03-longhaul, where "Vermillion Bravo" (stated once
// at turn 1) was condensed into "a relaxed, rail-based coastal itinerary"
// (docs/architecture/arena-memory-forensics.md §4).
func TestSummarizerPromptCarriesUniquenessContract(t *testing.T) {
	for _, want := range []string{
		"Carry over VERBATIM",
		"could not be reconstructed",
		"never a unique identifier",
	} {
		if !strings.Contains(summarizerSystemPrompt, want) {
			t.Errorf("summarizerSystemPrompt is missing the needle-preservation instruction %q; prompt = %q", want, summarizerSystemPrompt)
		}
	}

	// The clause must actually reach the model, not just exist as a constant.
	client := &scriptedClient{tokens: []llm.Token{{Text: "ok"}, {Done: true}}}
	if _, err := (LLMSummarizer{Client: client}).Condense(context.Background(), "",
		[]Message{{Role: "user", Content: "book me on the Vermillion Bravo"}, {Role: "assistant", Content: "sure"}}); err != nil {
		t.Fatalf("Condense: %v", err)
	}
	if !strings.Contains(client.gotSystem, "Carry over VERBATIM") {
		t.Errorf("system prompt handed to the client = %q, want it to carry the needle-preservation instruction", client.gotSystem)
	}
}

// TestSummarizerPromptStaysShort guards the cost side of the supersession fix:
// this prompt is re-sent on every condensation, which is a real billable model
// call, so the instruction must stay one short sentence rather than growing
// into a policy document. The bound is generous (a ~2x headroom over the
// current length) — it exists to catch unbounded growth, not to freeze wording.
func TestSummarizerPromptStaysShort(t *testing.T) {
	const maxPromptBytes = 1024
	if got := len(summarizerSystemPrompt); got > maxPromptBytes {
		t.Errorf("summarizerSystemPrompt is %d bytes, want <= %d — it is re-sent on every billable condensation call", got, maxPromptBytes)
	}
}

// blockingSummarizer is a Summarizer double whose Condense takes a controllable
// amount of time and honors context cancellation exactly the way LLMSummarizer
// does (internal/llm clients abort their stream on ctx.Done). It signals when
// it has entered Condense so a test can call Close with a condensation
// genuinely in flight rather than racing the goroutine scheduler.
type blockingSummarizer struct {
	entered chan struct{}
	delay   time.Duration
	result  string
}

func newBlockingSummarizer(delay time.Duration, result string) *blockingSummarizer {
	return &blockingSummarizer{entered: make(chan struct{}, 1), delay: delay, result: result}
}

func (b *blockingSummarizer) Condense(ctx context.Context, _ string, _ []Message) (string, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	timer := time.NewTimer(b.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return b.result, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// waitEntered blocks until Condense has been entered, failing the test rather
// than hanging if the condensation goroutine never starts.
func (b *blockingSummarizer) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("condensation never started; test setup did not trigger the background goroutine")
	}
}

// TestCloseLetsInFlightCondensationFinishWithinGrace proves the fix for the
// shutdown-ordering defect: Close no longer cancels shutdownCtx before waiting,
// so a condensation already running when Close fires gets to durably apply BOTH
// halves of its work — the new summary AND the prune of the evicted messages.
// Durability is checked by reopening the on-disk store after Close, which is
// the only way to observe state that survived the shutdown.
func TestCloseLetsInFlightCondensationFinishWithinGrace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wm")
	sum := newBlockingSummarizer(150*time.Millisecond, "CONDENSED-STATE")

	wm, err := Open(dir, 2, sum)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	wm.SetCloseGrace(5 * time.Second)

	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	for _, c := range []string{"m1", "m2", "m3"} {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: c}); err != nil {
			t.Fatalf("Append(%q): %v", c, err)
		}
	}
	sum.waitEntered(t)

	if err := wm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir, 2, NoopSummarizer{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("reopened Close: %v", err)
		}
	}()

	got, err := reopened.Summary(ctx, key)
	if err != nil {
		t.Fatalf("Summary after reopen: %v", err)
	}
	if got != "CONDENSED-STATE" {
		t.Errorf("summary after Close = %q, want the in-flight condensation's result to have landed", got)
	}

	recent, err := reopened.Recent(ctx, key, 10)
	if err != nil {
		t.Fatalf("Recent after reopen: %v", err)
	}
	if len(recent) > 2 {
		t.Errorf("active buffer holds %d messages after Close, want <= 2 — the prune half of the condensation did not land", len(recent))
	}
}

// TestCloseDoesNotWaitPastGraceBound proves the wait is BOUNDED: a condensation
// slower than the grace period must not hold shutdown open for its full
// duration. Close cancels once the deadline expires and returns, at the cost of
// that condensation's work — the explicit tradeoff documented on
// defaultCloseCondensationGrace.
func TestCloseDoesNotWaitPastGraceBound(t *testing.T) {
	const (
		grace            = 100 * time.Millisecond
		condenseDelay    = 30 * time.Second // stands in for a hung provider call
		closeMustBeUnder = 10 * time.Second
	)

	sum := newBlockingSummarizer(condenseDelay, "NEVER-LANDS")
	wm, err := OpenInMemory(2, sum)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	wm.SetCloseGrace(grace)

	ctx := context.Background()
	key := Key{Tenant: "acme", User: "bob", Session: "s1"}
	for _, c := range []string{"m1", "m2", "m3"} {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: c}); err != nil {
			t.Fatalf("Append(%q): %v", c, err)
		}
	}
	sum.waitEntered(t)

	start := time.Now()
	if err := wm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed >= closeMustBeUnder {
		t.Errorf("Close took %v, want it to abandon the condensation shortly after the %v grace bound rather than waiting out the %v call", elapsed, grace, condenseDelay)
	}
	if elapsed < grace {
		t.Errorf("Close took %v, want it to have waited at least the %v grace period before cancelling", elapsed, grace)
	}
}

// TestCloseWithZeroGraceCancelsImmediately covers the opt-out: a deployment
// with no shutdown budget can set a non-positive grace and get the original
// cancel-first behavior back.
func TestCloseWithZeroGraceCancelsImmediately(t *testing.T) {
	sum := newBlockingSummarizer(30*time.Second, "NEVER-LANDS")
	wm, err := OpenInMemory(2, sum)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	wm.SetCloseGrace(0)

	ctx := context.Background()
	key := Key{Tenant: "acme", User: "carol", Session: "s1"}
	for _, c := range []string{"m1", "m2", "m3"} {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: c}); err != nil {
			t.Fatalf("Append(%q): %v", c, err)
		}
	}
	sum.waitEntered(t)

	start := time.Now()
	if err := wm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Errorf("Close with zero grace took %v, want an immediate cancel", elapsed)
	}
}

// TestCloseIsIdempotent guards the double-Close path introduced by the grace
// logic: the second call must not re-run the grace wait or double-close the db.
func TestCloseIsIdempotent(t *testing.T) {
	wm, err := OpenInMemory(4, NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	if err := wm.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// BadgerDB reports an error on a second db.Close; what matters here is that
	// Close returns promptly instead of re-entering the grace wait.
	start := time.Now()
	_ = wm.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("second Close took %v, want it to short-circuit the grace wait", elapsed)
	}
}
