package agentloop

import (
	"context"
	"testing"
)

// TestRun_FinalAnswerWhenNoTool proves the fallback (StreamChat-only) path can
// now surface the model's natural-language answer when it declines every tool:
// the terminating no-tool Decision carries it in "answer", Run records it as the
// last step, and FinalAnswer extracts it. This is the agentloop analog of the
// native RunToolAgentWithOutput's result.Output — the "Canberra" scenario for a
// non-ToolCaller provider.
func TestRun_FinalAnswerWhenNoTool(t *testing.T) {
	client := &fakeClient{response: `{"tool":"","args":{},"answer":"Canberra"}`}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: &fakeExecutor{}, MaxSteps: 3}

	steps, err := loop.Run(context.Background(), []string{"search:read"}, "what's the capital of Australia")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1 (the terminating no-tool step): %+v", len(steps), steps)
	}
	if steps[0].Decision.Tool != "" {
		t.Fatalf("terminating step tool = %q, want empty", steps[0].Decision.Tool)
	}
	if got := FinalAnswer(steps); got != "Canberra" {
		t.Fatalf("FinalAnswer = %q, want %q", got, "Canberra")
	}
}

// TestFinalAnswer_EmptyWhenNoSteps / non-terminating: FinalAnswer is "" when the
// run recorded no steps, and when the last recorded step is a tool step (a
// non-converged / still-calling stop), mirroring the native path's "" on a
// max-steps graceful stop.
func TestFinalAnswer_EmptyEdgeCases(t *testing.T) {
	if got := FinalAnswer(nil); got != "" {
		t.Fatalf("FinalAnswer(nil) = %q, want empty", got)
	}
	toolTail := []StepResult{{Decision: Decision{Tool: "search", Args: map[string]string{"q": "x"}}, Result: "r"}}
	if got := FinalAnswer(toolTail); got != "" {
		t.Fatalf("FinalAnswer(tool-tail) = %q, want empty (run did not converge to a direct answer)", got)
	}
}

// TestRun_BackwardCompatNoAnswerField proves the additive field is backward
// compatible: a model still emitting only {"tool":"","args":{}} (no "answer")
// decodes cleanly with an empty answer — exactly as before this field existed.
func TestRun_BackwardCompatNoAnswerField(t *testing.T) {
	client := &fakeClient{response: `{"tool":"","args":{}}`}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: &fakeExecutor{}, MaxSteps: 3}

	steps, err := loop.Run(context.Background(), []string{"search:read"}, "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(steps) != 1 || steps[0].Decision.Tool != "" {
		t.Fatalf("want one terminating no-tool step, got %+v", steps)
	}
	if got := FinalAnswer(steps); got != "" {
		t.Fatalf("FinalAnswer = %q, want empty for a legacy no-answer decision", got)
	}
}
