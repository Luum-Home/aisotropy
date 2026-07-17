package api

import (
	"testing"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestRecordStreamCost covers the shared cost-recording helper: a nil meter or
// nil usage records nothing; a real usage records to the meter using the custom
// price basis when set (and the package defaults otherwise).
func TestRecordStreamCost(t *testing.T) {
	t.Run("nil meter records nothing", func(t *testing.T) {
		// Must not panic with a nil meter.
		recordStreamCost(StreamDeps{}, "acme", &llm.Usage{InputTokens: 10, OutputTokens: 5})
	})

	t.Run("nil usage records nothing", func(t *testing.T) {
		meter := costmeter.New()
		recordStreamCost(StreamDeps{Meter: meter}, "acme", nil)
		if len(meter.Snapshot()) != 0 {
			t.Error("nil usage must record nothing")
		}
	})

	t.Run("custom price basis records a positive cost", func(t *testing.T) {
		meter := costmeter.New()
		deps := StreamDeps{Meter: meter, InPricePer1M: 3.0, OutPricePer1M: 15.0}
		recordStreamCost(deps, "acme", &llm.Usage{InputTokens: 1000, OutputTokens: 1000})

		snap := meter.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("snapshot has %d tenants, want 1", len(snap))
		}
		s := snap[0]
		if s.InputTokens != 1000 || s.OutputTokens != 1000 {
			t.Errorf("recorded tokens = %d/%d, want 1000/1000", s.InputTokens, s.OutputTokens)
		}
		if s.CostUSD <= 0 {
			t.Errorf("recorded cost = %v, want a positive USD estimate", s.CostUSD)
		}
	})

	t.Run("default price basis when unset", func(t *testing.T) {
		meter := costmeter.New()
		recordStreamCost(StreamDeps{Meter: meter}, "acme", &llm.Usage{InputTokens: 100, OutputTokens: 100})
		if len(meter.Snapshot()) != 1 {
			t.Fatal("a real usage with the default price basis must still record")
		}
	})
}
