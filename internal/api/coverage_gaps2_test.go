package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/lang"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// coverage_gaps2_test.go closes a further round of branch gaps left after the
// MCP-serve pass: WithIdentity's exported seam, the non-fatal error-log
// branches in chat.go's turn-persistence helpers, resolveSubjectLanguage's
// nil/degraded-store paths, hydrateSystemPrompt's working-memory error and
// stored-summary branches, and StreamCore's quota-check-error and
// held-out-sample (BPT) branches.

// ---- WithIdentity (chat.go) -----------------------------------------------

// TestWithIdentity_ValidIdentityInjectsBothCarriers covers WithIdentity's
// success path: both internal/api's own ctx carrier (TenantFromContext/
// UserFromContext) and internal/llm's carrier (for OTel attribution) are set
// from the same validated identity.
func TestWithIdentity_ValidIdentityInjectsBothCarriers(t *testing.T) {
	ctx, err := WithIdentity(context.Background(), "acme", "alice", "s1")
	if err != nil {
		t.Fatalf("WithIdentity with a valid identity returned an error: %v", err)
	}
	if tenant, ok := TenantFromContext(ctx); !ok || tenant != "acme" {
		t.Errorf("TenantFromContext = (%q,%v), want (acme,true)", tenant, ok)
	}
	if user, ok := UserFromContext(ctx); !ok || user != "alice" {
		t.Errorf("UserFromContext = (%q,%v), want (alice,true)", user, ok)
	}
	if tenant, ok := llm.TenantFromContext(ctx); !ok || tenant != "acme" {
		t.Errorf("llm.TenantFromContext = (%q,%v), want (acme,true) — OTel carrier must also be set", tenant, ok)
	}
	if user, ok := llm.UserFromContext(ctx); !ok || user != "alice" {
		t.Errorf("llm.UserFromContext = (%q,%v), want (alice,true) — OTel carrier must also be set", user, ok)
	}
}

// TestWithIdentity_InvalidIdentityReturnsErrorWithoutInjecting covers the
// validation-failure branch: an empty tenant is rejected by memory.Key.Validate
// before either carrier is touched.
func TestWithIdentity_InvalidIdentityReturnsErrorWithoutInjecting(t *testing.T) {
	ctx, err := WithIdentity(context.Background(), "", "alice", "s1")
	if err == nil {
		t.Fatal("WithIdentity with an empty tenant must return an error")
	}
	if _, ok := llm.TenantFromContext(ctx); ok {
		t.Error("an invalid identity must not inject the llm ctx carrier")
	}
}

// ---- chatPersistUserTurn / chatRecordTurn non-fatal error-log branches ----

// TestChatPersistUserTurn_AppendFailureIsNonFatal covers the
// "log and continue" branch: an AppendWithOutbox failure (forced here via an
// already-cancelled ctx) does not panic or otherwise surface — the function
// has no error return, by design (a persistence hiccup must never fail the
// turn that already happened).
func TestChatPersistUserTurn_AppendFailureIsNonFatal(t *testing.T) {
	wm := newTestWorkingMemory(t)
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chatPersistUserTurn(ctx, wm, key, "hello") // must not panic despite the forced failure
}

// TestChatRecordTurn_AppendFailureIsNonFatal covers chatRecordTurn's
// equivalent "log and continue" branch for the assistant turn (wm.Append
// failing via a cancelled ctx), and also exercises the meter-nil skip.
func TestChatRecordTurn_AppendFailureIsNonFatal(t *testing.T) {
	wm := newTestWorkingMemory(t)
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chatRecordTurn(ctx, wm, key, "acme", "final reply", llm.Usage{}, nil) // must not panic
}

// ---- resolveSubjectLanguage (core.go) --------------------------------------

// TestResolveSubjectLanguage_NilWorkingMemoryReturnsDetected covers the nil-wm
// short-circuit: with no working memory configured, the request-scoped
// detection is returned unchanged (no subject-persistence attempted).
func TestResolveSubjectLanguage_NilWorkingMemoryReturnsDetected(t *testing.T) {
	got := resolveSubjectLanguage(context.Background(), nil, "acme", "alice", lang.Language("es"))
	if got != lang.Language("es") {
		t.Fatalf("resolveSubjectLanguage(nil wm) = %q, want the detected language unchanged", got)
	}
}

// TestResolveSubjectLanguage_ReadFailureFallsBackToDetected covers the
// GetSubjectLanguage-error branch: a closed store's read fails, and the
// function degrades to the request-scoped detection rather than failing the
// turn over an advisory hint.
func TestResolveSubjectLanguage_ReadFailureFallsBackToDetected(t *testing.T) {
	wm, err := memory.OpenInMemory(0, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	if err := wm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := resolveSubjectLanguage(context.Background(), wm, "acme", "alice", lang.Language("fr"))
	if got != lang.Language("fr") {
		t.Fatalf("resolveSubjectLanguage(closed wm) = %q, want the detected language as a fallback", got)
	}
}

// ---- hydrateSystemPrompt (chat_stream.go) ----------------------------------

// TestHydrateSystemPrompt_SummaryReadErrorPropagates covers the
// wm.Summary error branch: an already-cancelled ctx makes the working-memory
// read fail, and hydrateSystemPrompt propagates it (a working-memory failure
// IS fatal, unlike the facts/semantic enrichment failures below it).
func TestHydrateSystemPrompt_SummaryReadErrorPropagates(t *testing.T) {
	wm := newTestWorkingMemory(t)
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = hydrateSystemPrompt(ctx, wm, nil, nil, nil, nil, nil, key, scope, "analytics-v1", "hi")
	if err == nil {
		t.Fatal("hydrateSystemPrompt must propagate a working-memory summary read failure")
	}
	if !strings.Contains(err.Error(), "summary") {
		t.Errorf("error = %v, want it to name the summary read", err)
	}
}

// TestHydrateSystemPrompt_IncludesStoredSummary covers the "summary != ""
// injection branch: once background condensation has produced a stored
// summary for the session (forced here by a 1-message active window plus
// wm.Sync to deterministically drain the async condensation goroutine), the
// hydrated prompt includes the "Summary of the conversation so far" section.
func TestHydrateSystemPrompt_IncludesStoredSummary(t *testing.T) {
	wm, err := memory.OpenInMemory(1, memory.NoopSummarizer{}) // window of 1 forces condensation on the 2nd append
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() {
		if err := wm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	ctx := context.Background()
	if err := wm.Append(ctx, key, memory.Message{Role: roleUser, Content: "turn one"}); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if err := wm.Append(ctx, key, memory.Message{Role: roleAssistant, Content: "turn two"}); err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	wm.Sync() // deterministically wait for the background condensation this triggers

	summary, err := wm.Summary(ctx, key)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary == "" {
		t.Fatal("test setup: expected condensation to have produced a non-empty summary")
	}

	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	system, err := hydrateSystemPrompt(ctx, wm, nil, nil, nil, nil, nil, key, scope, "analytics-v1", "hi")
	if err != nil {
		t.Fatalf("hydrateSystemPrompt: %v", err)
	}
	if !strings.Contains(system, "Summary of the conversation so far") {
		t.Errorf("hydrated system prompt = %q, want it to include the stored summary section", system)
	}
}

// ---- StreamCore (stream_core.go) -------------------------------------------

// TestStreamCore_QuotaCheckErrorSurfaces covers the Limiter.Allow error branch
// (distinct from a clean quota denial): a cancelled ctx makes the quota check
// itself fail, and StreamCore surfaces that as a "quota check failed" error
// rather than treating it as an ordinary rate-limit rejection.
func TestStreamCore_QuotaCheckErrorSurfaces(t *testing.T) {
	wm := newTestWorkingMemory(t)
	limiter := wm.NewLimiter(10, 10)
	deps := StreamDeps{Client: llm.NewStubClient(), WM: wm, Limiter: limiter}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out strings.Builder
	err := StreamCore(ctx, deps, in, discardSink(&out))
	if err == nil || !strings.Contains(err.Error(), "quota check failed") {
		t.Fatalf("StreamCore with a cancelled ctx and a configured limiter, error = %v, want a quota-check-failed error", err)
	}
}

// fakeSampleRecorder is a scripted bitrate.SampleRecorder: it records the call
// and returns a canned error, so StreamCore's held-out-sample error-log branch
// is directly observable.
type fakeSampleRecorder struct {
	calls int
	err   error
}

func (f *fakeSampleRecorder) Reserve(_ context.Context, tenantID, userID, sessionID, sampleID, text string) error {
	f.calls++
	return f.err
}

// findSampledPrompt brute-forces a prompt whose outboxRef the deterministic
// ~5% bitrate.ShouldSample selects for the given key, so the BPT held-out
// branch in StreamCore can be driven without depending on a hardcoded magic
// string.
func findSampledPrompt(t *testing.T, key memory.Key) string {
	t.Helper()
	for i := 0; i < 10000; i++ {
		prompt := "probe-" + string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('0'+i/260))
		if bitrate.ShouldSample(outboxRef(key, prompt)) {
			return prompt
		}
	}
	t.Fatal("could not find a prompt sampled by bitrate.ShouldSample within the search budget")
	return ""
}

// TestStreamCore_HeldOutSampleReserveErrorIsNonFatal covers the BPT
// held-out-sample branch (Guard nil, so forwarded==prompt and the ref is
// computable up front): a turn selected by bitrate.ShouldSample is appended to
// working memory AND reserved via BPTSampler.Reserve INSTEAD OF the ordinary
// outbox enqueue (the anti-leakage contract); a Reserve failure is logged, not
// fatal, and the stream still completes normally.
func TestStreamCore_HeldOutSampleReserveErrorIsNonFatal(t *testing.T) {
	wm := newTestWorkingMemory(t)
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	prompt := findSampledPrompt(t, key)

	sampler := &fakeSampleRecorder{err: errors.New("reserve backend unavailable")}
	deps := StreamDeps{Client: llm.NewStubClient(), WM: wm, BPTSampler: sampler}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: prompt, SessionID: "s1"}}

	var out strings.Builder
	if err := StreamCore(context.Background(), deps, in, discardSink(&out)); err != nil {
		t.Fatalf("StreamCore should complete despite a Reserve failure (non-fatal, logged): %v", err)
	}
	if sampler.calls != 1 {
		t.Fatalf("BPTSampler.Reserve calls = %d, want 1 (the sampled turn must be reserved, not consolidation-enqueued)", sampler.calls)
	}
}
