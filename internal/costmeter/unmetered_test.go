package costmeter_test

import (
	"sync"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
)

// RecordUnmetered exists because "the provider billed this turn and we do not
// know the token counts" and "no turn happened" were previously the same thing
// on the meter: both left no trace. Conflating them makes the accounting gap
// invisible, which is precisely the property a reconciliation against the
// provider's invoice needs to see.

func TestRecordUnmetered_CountsTheTurnWithoutInventingTokens(t *testing.T) {
	m := costmeter.New()
	m.RecordUnmetered("acme")

	s, ok := m.Stats("acme")
	if !ok {
		t.Fatal("an unmetered turn must still leave a record for the tenant")
	}
	if s.Requests != 1 {
		t.Errorf("Requests = %d, want 1", s.Requests)
	}
	if s.UnmeteredTurns != 1 {
		t.Errorf("UnmeteredTurns = %d, want 1", s.UnmeteredTurns)
	}
	if s.InputTokens != 0 || s.OutputTokens != 0 || s.ReasoningTokens != 0 || s.CostUSD != 0 {
		t.Errorf("tokens/cost = %d/%d/%d/%v, want all zero: an unmetered turn must never fabricate counts",
			s.InputTokens, s.OutputTokens, s.ReasoningTokens, s.CostUSD)
	}
}

// TestRecordUnmetered_IsDistinguishableFromAZeroCostTurn is the whole point: a
// metered turn that happened to cost nothing and a turn whose cost is unknown
// must not look identical in /costs.
func TestRecordUnmetered_IsDistinguishableFromAZeroCostTurn(t *testing.T) {
	m := costmeter.New()
	m.RecordUsage("acme", 0, 0, 0, 0) // metered, genuinely zero
	m.RecordUnmetered("acme")         // billed, counts unknown

	s, _ := m.Stats("acme")
	if s.Requests != 2 {
		t.Errorf("Requests = %d, want 2", s.Requests)
	}
	if s.UnmeteredTurns != 1 {
		t.Errorf("UnmeteredTurns = %d, want 1: exactly one of the two turns carried no usage", s.UnmeteredTurns)
	}
}

// TestRecordUnmetered_MixesWithMeteredTurns checks the counters stay independent:
// recording usage must never touch UnmeteredTurns.
func TestRecordUnmetered_MixesWithMeteredTurns(t *testing.T) {
	m := costmeter.New()
	m.RecordUsage("acme", 100, 200, 50, 0.5)
	m.RecordUnmetered("acme")
	m.RecordUsage("acme", 100, 200, 50, 0.5)

	s, _ := m.Stats("acme")
	if s.Requests != 3 || s.UnmeteredTurns != 1 {
		t.Errorf("Requests/UnmeteredTurns = %d/%d, want 3/1", s.Requests, s.UnmeteredTurns)
	}
	if s.InputTokens != 200 || s.OutputTokens != 400 {
		t.Errorf("tokens = %d/%d, want 200/400: the unmetered turn must add nothing", s.InputTokens, s.OutputTokens)
	}
}

// TestRecordUnmetered_Concurrent guards the shared-map write path.
func TestRecordUnmetered_Concurrent(t *testing.T) {
	m := costmeter.New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.RecordUnmetered("acme")
		}()
	}
	wg.Wait()

	s, _ := m.Stats("acme")
	if s.UnmeteredTurns != 50 {
		t.Errorf("UnmeteredTurns = %d, want 50", s.UnmeteredTurns)
	}
}

// TestSnapshot_CarriesUnmeteredTurns proves the new counter reaches the /costs
// read path (Snapshot is what the endpoint serializes).
func TestSnapshot_CarriesUnmeteredTurns(t *testing.T) {
	m := costmeter.New()
	m.RecordUnmetered("acme")

	snap := m.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot has %d tenants, want 1", len(snap))
	}
	if snap[0].UnmeteredTurns != 1 {
		t.Errorf("snapshot UnmeteredTurns = %d, want 1", snap[0].UnmeteredTurns)
	}
}
