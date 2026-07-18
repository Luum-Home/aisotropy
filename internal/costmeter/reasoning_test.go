package costmeter

import "testing"

// TestRecordUsage_CountsReasoning is the cost blocker (ADR-0061): a turn's
// reasoning tokens (from Usage.ReasoningTokens) must land on the tenant's
// ReasoningTokens line and contribute to CostUSD, SEPARATE from output tokens.
func TestRecordUsage_CountsReasoning(t *testing.T) {
	m := New()
	// Two reasoning turns for the same tenant.
	m.RecordUsage("acme", 100, 40, 30, 0.05)
	m.RecordUsage("acme", 10, 5, 7, 0.01)

	got, ok := m.Stats("acme")
	if !ok {
		t.Fatal("no stats for acme")
	}
	if got.ReasoningTokens != 37 {
		t.Errorf("ReasoningTokens = %d, want 37", got.ReasoningTokens)
	}
	if got.OutputTokens != 45 {
		t.Errorf("OutputTokens = %d, want 45 (reasoning must NOT inflate output)", got.OutputTokens)
	}
	if got.InputTokens != 110 {
		t.Errorf("InputTokens = %d, want 110", got.InputTokens)
	}
	if got.Requests != 2 {
		t.Errorf("Requests = %d, want 2", got.Requests)
	}
}

// TestRecord_BackwardCompatNoReasoning: the legacy 4-arg Record path leaves
// ReasoningTokens at zero — every existing caller is unaffected.
func TestRecord_BackwardCompatNoReasoning(t *testing.T) {
	m := New()
	m.Record("t", 5, 5, 0.001)
	got, _ := m.Stats("t")
	if got.ReasoningTokens != 0 {
		t.Fatalf("legacy Record must not set ReasoningTokens, got %d", got.ReasoningTokens)
	}
}

// TestRecordUsage_NegativeReasoningClamped: a buggy upstream report of negative
// reasoning tokens must never decrement the running total (clamp to zero).
func TestRecordUsage_NegativeReasoningClamped(t *testing.T) {
	m := New()
	m.RecordUsage("t", 1, 1, -50, -1.0)
	got, _ := m.Stats("t")
	if got.ReasoningTokens != 0 || got.CostUSD != 0 {
		t.Fatalf("negative values must clamp to zero, got reasoning=%d cost=%f", got.ReasoningTokens, got.CostUSD)
	}
}
