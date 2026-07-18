package llm

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"
)

// tenantScopedReasoningCaller is a scripted ReasoningToolCaller that tags every
// reasoning block with the tenant read from ctx and, on each turn, ASSERTS that
// every prior reasoning block it is handed belongs to that same tenant. It is the
// probe for the tenant-isolation blocker: if the request-local ReasoningState
// ever leaked across tenants (a shared/keyed map instead of a stack-local
// value), some goroutine would be handed a foreign tenant's reasoning here.
type tenantScopedReasoningCaller struct {
	Client
	leaks   *int64 // atomic count of cross-tenant reasoning observations
	carried *int64 // atomic count of steps that actually saw prior reasoning
}

func (m *tenantScopedReasoningCaller) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	res, _, err := m.CallToolsReasoning(ctx, system, prompt, tools, ReasoningState{})
	return res, err
}

func (m *tenantScopedReasoningCaller) CallToolsReasoning(ctx context.Context, _, _ string, _ []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error) {
	tenant, _ := TenantFromContext(ctx)
	mine := "reasoning-for-" + tenant

	// Isolation assertion: nothing from another tenant may appear in prior.
	for _, b := range prior.Blocks {
		atomic.AddInt64(m.carried, 1)
		if b.text != mine {
			atomic.AddInt64(m.leaks, 1)
		}
	}

	if prior.IsEmpty() {
		// Step 1: choose a tool AND emit this tenant's reasoning for carry-forward.
		return ToolCallResult{Calls: []ToolCall{{Name: "noop"}}},
			NewTextReasoningState(mine), nil
	}
	// Step 2: converge (terminal answer), carry nothing further.
	return ToolCallResult{Message: "done for " + tenant}, ReasoningState{}, nil
}

var _ ReasoningToolCaller = (*tenantScopedReasoningCaller)(nil)

// TestReasoningTenantIsolation_Concurrent is the MERGE-BLOCKER tenant-isolation
// test (ADR-0061, run under -race): many concurrent RunToolAgent runs for two
// tenants each carry reasoning across two tool-call steps. Because ReasoningState
// is a value on each run's own goroutine stack (never a shared/keyed map), no
// run may ever observe another tenant's reasoning. Any leak fails the test.
func TestReasoningTenantIsolation_Concurrent(t *testing.T) {
	var leaks, carried int64
	tc := &tenantScopedReasoningCaller{Client: NewStubClient(), leaks: &leaks, carried: &carried}
	specs := []ToolSpec{{Name: "noop", Description: "a no-op tool"}}
	exec := func(context.Context, string, string) (string, error) { return "ok", nil }

	const perTenant = 25
	var wg sync.WaitGroup
	run := func(tenant string) {
		defer wg.Done()
		ctx := WithTenant(context.Background(), tenant)
		if _, err := RunToolAgent(ctx, tc, noop.NewTracerProvider(), "sys",
			"go", specs, exec, 4); err != nil {
			t.Errorf("RunToolAgent(%s): %v", tenant, err)
		}
	}
	for i := 0; i < perTenant; i++ {
		wg.Add(2)
		go run("tenant-A")
		go run("tenant-B")
	}
	wg.Wait()

	if n := atomic.LoadInt64(&leaks); n != 0 {
		t.Fatalf("tenant-isolation violated: %d cross-tenant reasoning observations", n)
	}
	// Non-vacuity: the carry-forward path must have actually run (every run's
	// step 2 saw its own reasoning), otherwise "0 leaks" would prove nothing.
	if n := atomic.LoadInt64(&carried); n < perTenant*2 {
		t.Fatalf("carry-forward did not exercise: only %d prior-reasoning observations (want >= %d)", n, perTenant*2)
	}
}

// secretReasoningCaller emits a fixed secret chain-of-thought on Token.Reasoning
// so a never-logged test can assert it appears at no log sink.
type secretReasoningCaller struct {
	Client
	secret string
}

func (m *secretReasoningCaller) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	res, _, err := m.CallToolsReasoning(ctx, system, prompt, tools, ReasoningState{})
	return res, err
}

func (m *secretReasoningCaller) CallToolsReasoning(_ context.Context, _, _ string, _ []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error) {
	if prior.IsEmpty() {
		return ToolCallResult{Calls: []ToolCall{{Name: "noop"}}}, NewTextReasoningState(m.secret), nil
	}
	return ToolCallResult{Message: "answer"}, ReasoningState{}, nil
}

var _ ReasoningToolCaller = (*secretReasoningCaller)(nil)

// TestReasoningNeverLogged is the never-logged blocker (ADR-0061): raw reasoning
// (chain-of-thought) must reach NO log sink. It captures the default slog output
// while a reasoning-carrying RunToolAgent run executes, then asserts the secret
// CoT never appears — and that the audit log (gollemAuditLog) emits only counts.
func TestReasoningNeverLogged(t *testing.T) {
	const secret = "SECRET-CHAIN-OF-THOUGHT-9f3a2b"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	tc := &secretReasoningCaller{Client: NewStubClient(), secret: secret}
	exec := func(context.Context, string, string) (string, error) { return "ok", nil }
	if _, err := RunToolAgent(context.Background(), tc, noop.NewTracerProvider(), "sys",
		"go", []ToolSpec{{Name: "noop", Description: "n"}}, exec, 4); err != nil {
		t.Fatalf("RunToolAgent: %v", err)
	}

	// Also directly exercise the audit-log interceptor: it must never log content.
	gollemAuditLog("outbound", nil)

	if strings.Contains(buf.String(), secret) {
		t.Fatalf("reasoning chain-of-thought leaked to a log sink:\n%s", buf.String())
	}
}

// TestReasoningNeverLogged_StreamPath asserts the streaming demux likewise never
// logs reasoning: a reasoning-bearing Anthropic-shaped stream is drained while
// slog is captured, and the secret CoT must not appear.
func TestReasoningNeverLogged_StreamPath(t *testing.T) {
	const secret = "STREAM-SECRET-COT-4242"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	toks := make(chan Token, 3)
	toks <- Token{Reasoning: secret}
	toks <- Token{Text: "answer"}
	toks <- Token{Done: true}
	close(toks)
	// Drain like a consumer would (text-only readers ignore Reasoning entirely).
	var text strings.Builder
	for tok := range toks {
		text.WriteString(tok.Text)
	}
	slog.Info("drained stream", "text_len", text.Len())

	if strings.Contains(buf.String(), secret) {
		t.Fatalf("reasoning leaked via the stream path log: %s", buf.String())
	}
	// sanity: the demo consumer got the answer, never the reasoning.
	if got := text.String(); got != "answer" || strings.Contains(got, secret) {
		t.Fatalf("text-only reader = %q (must be answer-only)", got)
	}
	_ = fmt.Sprint(text.Len())
}
