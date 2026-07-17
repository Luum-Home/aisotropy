package api

import (
	"strings"
	"testing"

	"github.com/fugue-labs/gollem/ext/mcp"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
)

// TestAgentStepsFromLoop covers the mapping of the homegrown agent loop's
// StepResult shape into the endpoint's AgentStep contract, including a
// direct-answer step (empty Tool).
func TestAgentStepsFromLoop(t *testing.T) {
	loop := []agentloop.StepResult{
		{Decision: agentloop.Decision{Tool: "search", Args: map[string]string{"q": "x"}}, Result: "hit"},
		{Decision: agentloop.Decision{Tool: ""}, Result: "answered directly"},
	}
	steps := agentStepsFromLoop(loop)
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].Tool != "search" || steps[0].Args["q"] != "x" || steps[0].Result != "hit" {
		t.Errorf("step 0 = %+v, want the mapped search step", steps[0])
	}
	if steps[1].Tool != "" || steps[1].Result != "answered directly" {
		t.Errorf("step 1 = %+v, want the direct-answer step", steps[1])
	}

	if got := agentStepsFromLoop(nil); len(got) != 0 {
		t.Errorf("agentStepsFromLoop(nil) = %v, want empty", got)
	}
}

// TestFormatMCPResult covers the ToolResult rendering branches: a nil result, a
// multi-part text join, an IsError result (with and without a message), and the
// structured-content fallback when there is no text.
func TestFormatMCPResult(t *testing.T) {
	t.Run("nil result", func(t *testing.T) {
		got, err := formatMCPResult("t", nil)
		if err != nil || got != "" {
			t.Errorf("nil result = (%q, %v), want (\"\", nil)", got, err)
		}
	})

	t.Run("joins text parts", func(t *testing.T) {
		res := &mcp.ToolResult{Content: []mcp.Content{{Text: "line1"}, {Text: ""}, {Text: "line2"}}}
		got, err := formatMCPResult("t", res)
		if err != nil {
			t.Fatalf("formatMCPResult: %v", err)
		}
		if got != "line1\nline2" {
			t.Errorf("joined text = %q, want \"line1\\nline2\" (empty parts skipped)", got)
		}
	})

	t.Run("IsError becomes a Go error", func(t *testing.T) {
		res := &mcp.ToolResult{IsError: true, Content: []mcp.Content{{Text: "permission denied"}}}
		if _, err := formatMCPResult("read_file", res); err == nil {
			t.Fatal("an IsError result must become a Go error (records friction, not silent success)")
		} else if !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("error = %v, want it to carry the tool's message", err)
		}
	})

	t.Run("IsError with no message", func(t *testing.T) {
		res := &mcp.ToolResult{IsError: true}
		_, err := formatMCPResult("t", res)
		if err == nil || !strings.Contains(err.Error(), "no message") {
			t.Errorf("empty IsError = %v, want the no-message fallback error", err)
		}
	})

	t.Run("structured-content fallback", func(t *testing.T) {
		res := &mcp.ToolResult{StructuredContent: map[string]any{"temp": 22}}
		got, err := formatMCPResult("t", res)
		if err != nil {
			t.Fatalf("formatMCPResult: %v", err)
		}
		if !strings.Contains(got, "temp") || !strings.Contains(got, "22") {
			t.Errorf("structured fallback = %q, want the compact JSON of the structured content", got)
		}
	})
}

// TestNewEvalOutputCheck_Construction covers the constructor's guard and
// threshold clamps: a nil judge is rejected, and out-of-range thresholds are
// clamped into [0,1].
func TestNewEvalOutputCheck_Construction(t *testing.T) {
	t.Run("nil judge rejected", func(t *testing.T) {
		if _, err := NewEvalOutputCheck(nil, 0.5, false); err == nil {
			t.Fatal("a nil judge must be rejected at construction")
		}
	})
	t.Run("threshold clamped low", func(t *testing.T) {
		c, err := NewEvalOutputCheck(stubJudge{value: 0.5}, -1, false)
		if err != nil {
			t.Fatalf("NewEvalOutputCheck: %v", err)
		}
		if c.threshold != 0 {
			t.Errorf("threshold = %v, want clamped to 0", c.threshold)
		}
	})
	t.Run("threshold clamped high", func(t *testing.T) {
		c, err := NewEvalOutputCheck(stubJudge{value: 0.5}, 5, false)
		if err != nil {
			t.Fatalf("NewEvalOutputCheck: %v", err)
		}
		if c.threshold != 1 {
			t.Errorf("threshold = %v, want clamped to 1", c.threshold)
		}
	})
}
