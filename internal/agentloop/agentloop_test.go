package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// fakeClient is a canned llm.Client: it ignores system/prompt and always
// streams back the configured response (or fails to open the stream, or
// mid-stream errors, depending on which field is set).
type fakeClient struct {
	response  string
	openErr   error
	streamErr error
}

func (f *fakeClient) Name() string { return "fake" }

func (f *fakeClient) StreamChat(_ context.Context, _, _ string) (<-chan llm.Token, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	ch := make(chan llm.Token, 2)
	go func() {
		defer close(ch)
		if f.streamErr != nil {
			ch <- llm.Token{Err: f.streamErr}
			return
		}
		ch <- llm.Token{Text: f.response, Done: true}
	}()
	return ch, nil
}

// fakeExecutor records every call it receives and returns a canned result.
type fakeExecutor struct {
	calls  []executorCall
	result string
	err    error
}

type executorCall struct {
	name string
	args map[string]string
}

func (f *fakeExecutor) Execute(_ context.Context, name string, args map[string]string) (string, error) {
	f.calls = append(f.calls, executorCall{name: name, args: args})
	return f.result, f.err
}

func testRegistry() *tools.Registry {
	return tools.NewRegistry(
		tools.Tool{Name: "search", Description: "search the web", RequiredScope: "search:read"},
		tools.Tool{Name: "admin_delete", Description: "delete a tenant", RequiredScope: "admin:write"},
		tools.Tool{Name: "echo", Description: "always available", RequiredScope: ""},
	)
}

// (a) the model picks a permitted tool: SelectTool returns it, and Run
// executes it via the ToolExecutor.
func TestSelectTool_PermittedToolIsReturnedAndExecuted(t *testing.T) {
	client := &fakeClient{response: `{"tool":"search","args":{"query":"golang"}}`}
	exec := &fakeExecutor{result: "3 results found"}
	// MaxSteps: 1 — this fakeClient always returns the same canned Decision
	// regardless of the (growing) prompt Run feeds back, so with a higher
	// MaxSteps Run would keep "finding" the same tool forever; that behavior
	// is covered separately by TestRun_RespectsMaxSteps.
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 1}

	dec, err := loop.SelectTool(context.Background(), []string{"search:read"}, "find golang docs")
	if err != nil {
		t.Fatalf("SelectTool returned error: %v", err)
	}
	if dec.Tool != "search" {
		t.Fatalf("expected tool %q, got %q", "search", dec.Tool)
	}
	if dec.Args["query"] != "golang" {
		t.Fatalf("expected args[query]=golang, got %v", dec.Args)
	}

	steps, err := loop.Run(context.Background(), []string{"search:read"}, "find golang docs")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].Result != "3 results found" {
		t.Fatalf("expected tool result to be returned, got %q", steps[0].Result)
	}
	if len(exec.calls) != 1 || exec.calls[0].name != "search" {
		t.Fatalf("expected executor to be called once with 'search', got %+v", exec.calls)
	}
}

// (b) the model tries to pick a tool NOT in the caller's scopes: it must be
// rejected — treated as no-tool — proving the model cannot escalate beyond
// what Registry.Permitted(scopes) allowed it to even see.
func TestSelectTool_RejectsToolOutsideGrantedScopes(t *testing.T) {
	client := &fakeClient{response: `{"tool":"admin_delete","args":{"tenant_id":"42"}}`}
	exec := &fakeExecutor{result: "should never run"}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 3}

	// Caller only has search:read — NOT admin:write.
	scopes := []string{"search:read"}

	dec, err := loop.SelectTool(context.Background(), scopes, "please delete tenant 42")
	if err != nil {
		t.Fatalf("SelectTool returned error: %v", err)
	}
	if dec.Tool != "" {
		t.Fatalf("expected the non-permitted tool to be downgraded to no-tool, got %q", dec.Tool)
	}
	if dec.Args != nil {
		t.Fatalf("expected no args on a rejected decision, got %v", dec.Args)
	}

	// Run must not execute anything either.
	steps, err := loop.Run(context.Background(), scopes, "please delete tenant 42")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(steps) != 1 || steps[0].Decision.Tool != "" {
		t.Fatalf("expected a single no-tool step, got %+v", steps)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("expected the executor to NEVER be called for a non-permitted tool, got %+v", exec.calls)
	}
}

// A defense-in-depth variant of (b): even a tool name that exists nowhere in
// the registry at all must be rejected the same way.
func TestSelectTool_RejectsUnknownToolName(t *testing.T) {
	client := &fakeClient{response: `{"tool":"drop_database","args":{}}`}
	exec := &fakeExecutor{}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 1}

	dec, err := loop.SelectTool(context.Background(), []string{"search:read", "admin:write"}, "wipe everything")
	if err != nil {
		t.Fatalf("SelectTool returned error: %v", err)
	}
	if dec.Tool != "" {
		t.Fatalf("expected unknown tool name to be rejected, got %q", dec.Tool)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("expected executor never called, got %+v", exec.calls)
	}
}

// (c) malformed JSON from the model must be handled gracefully: no panic,
// and a documented error (wrapping ErrMalformedDecision) rather than a
// fabricated Decision.
func TestSelectTool_MalformedJSONIsHandledGracefully(t *testing.T) {
	cases := []struct {
		name     string
		response string
	}{
		{"prose, no JSON at all", "I think you should call the search tool with query golang."},
		{"truncated JSON", `{"tool":"search","args":{"query":"golan`},
		{"empty response", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{response: tc.response}
			exec := &fakeExecutor{}
			loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 1}

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("SelectTool panicked on malformed input %q: %v", tc.response, r)
				}
			}()

			dec, err := loop.SelectTool(context.Background(), []string{"search:read"}, "find golang docs")
			if err == nil {
				t.Fatalf("expected an error for malformed model output %q, got Decision %+v", tc.response, dec)
			}
			if !errors.Is(err, ErrMalformedDecision) {
				t.Fatalf("expected error to wrap ErrMalformedDecision, got: %v", err)
			}
			if dec.Tool != "" {
				t.Fatalf("expected zero-value Decision on error, got %+v", dec)
			}
		})
	}
}

// A tool-selection reply wrapped in a markdown code fence must still parse —
// models frequently do this even when told not to.
func TestSelectTool_ToleratesMarkdownCodeFence(t *testing.T) {
	client := &fakeClient{response: "```json\n{\"tool\":\"echo\",\"args\":{\"msg\":\"hi\"}}\n```"}
	exec := &fakeExecutor{result: "hi"}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 1}

	dec, err := loop.SelectTool(context.Background(), nil, "say hi")
	if err != nil {
		t.Fatalf("SelectTool returned error: %v", err)
	}
	if dec.Tool != "echo" {
		t.Fatalf("expected tool 'echo' (no RequiredScope), got %q", dec.Tool)
	}
}

// An explicit no-tool decision from the model must be respected as-is.
func TestSelectTool_NoToolMeansAnswerDirectly(t *testing.T) {
	client := &fakeClient{response: `{"tool":"","args":{}}`}
	exec := &fakeExecutor{}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 1}

	dec, err := loop.SelectTool(context.Background(), []string{"search:read"}, "hello")
	if err != nil {
		t.Fatalf("SelectTool returned error: %v", err)
	}
	if dec.Tool != "" {
		t.Fatalf("expected empty tool, got %q", dec.Tool)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("expected executor never called, got %+v", exec.calls)
	}
}

// Errors opening the stream and mid-stream errors both propagate instead of
// being silently swallowed.
func TestSelectTool_PropagatesStreamErrors(t *testing.T) {
	openErrClient := &fakeClient{openErr: errors.New("provider unavailable")}
	loop := &Loop{Client: openErrClient, Registry: testRegistry(), Executor: &fakeExecutor{}, MaxSteps: 1}
	if _, err := loop.SelectTool(context.Background(), []string{"search:read"}, "hi"); err == nil {
		t.Fatal("expected an error when the stream fails to open")
	}

	midErrClient := &fakeClient{streamErr: errors.New("connection reset")}
	loop2 := &Loop{Client: midErrClient, Registry: testRegistry(), Executor: &fakeExecutor{}, MaxSteps: 1}
	if _, err := loop2.SelectTool(context.Background(), []string{"search:read"}, "hi"); err == nil {
		t.Fatal("expected an error when the stream fails mid-flight")
	}
}

// Run must stop as soon as Executor.Execute fails, without looping forever
// or panicking, and must surface the failure.
func TestRun_StopsOnExecutorError(t *testing.T) {
	client := &fakeClient{response: `{"tool":"search","args":{"query":"golang"}}`}
	exec := &fakeExecutor{err: errors.New("tool exploded")}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 5}

	steps, err := loop.Run(context.Background(), []string{"search:read"}, "find golang docs")
	if err == nil {
		t.Fatal("expected Run to surface the executor error")
	}
	if len(steps) != 1 {
		t.Fatalf("expected exactly 1 recorded step before stopping, got %d", len(steps))
	}
	if len(exec.calls) != 1 {
		t.Fatalf("expected the executor to be called exactly once, got %d calls", len(exec.calls))
	}
}

// Run must respect MaxSteps even if the model keeps picking a tool forever.
func TestRun_RespectsMaxSteps(t *testing.T) {
	client := &fakeClient{response: `{"tool":"echo","args":{"msg":"again"}}`}
	exec := &fakeExecutor{result: "again"}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 3}

	steps, err := loop.Run(context.Background(), nil, "loop forever")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("expected exactly MaxSteps=3 steps, got %d", len(steps))
	}
	if len(exec.calls) != 3 {
		t.Fatalf("expected the executor to be called exactly 3 times, got %d", len(exec.calls))
	}
}
