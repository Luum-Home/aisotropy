package costmeter

import (
	"fmt"
	"sync"
	"testing"
)

// TestRecordAndStats verifies single-tenant accumulation across multiple
// Record calls.
func TestRecordAndStats(t *testing.T) {
	m := New()

	m.Record("tenant-a", 100, 50, 0.01)
	m.Record("tenant-a", 200, 75, 0.02)

	stats, ok := m.Stats("tenant-a")
	if !ok {
		t.Fatalf("Stats(tenant-a) ok = false, want true")
	}
	if stats.TenantID != "tenant-a" {
		t.Errorf("TenantID = %q, want %q", stats.TenantID, "tenant-a")
	}
	if stats.Requests != 2 {
		t.Errorf("Requests = %d, want 2", stats.Requests)
	}
	if stats.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", stats.InputTokens)
	}
	if stats.OutputTokens != 125 {
		t.Errorf("OutputTokens = %d, want 125", stats.OutputTokens)
	}
	if got, want := stats.CostUSD, 0.03; !floatEquals(got, want) {
		t.Errorf("CostUSD = %v, want %v", got, want)
	}
}

// TestStatsUnknownTenant verifies the (zero-value, false) contract for a
// tenant that never recorded any usage.
func TestStatsUnknownTenant(t *testing.T) {
	m := New()

	stats, ok := m.Stats("nobody")
	if ok {
		t.Fatalf("Stats(nobody) ok = true, want false")
	}
	if stats != (TenantStats{}) {
		t.Errorf("Stats(nobody) = %+v, want zero value", stats)
	}
}

// TestSnapshotOrdering verifies Snapshot returns entries sorted by TenantID
// ascending, regardless of insertion order.
func TestSnapshotOrdering(t *testing.T) {
	m := New()

	m.Record("tenant-c", 1, 1, 0)
	m.Record("tenant-a", 1, 1, 0)
	m.Record("tenant-b", 1, 1, 0)

	snap := m.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("len(Snapshot()) = %d, want 3", len(snap))
	}

	wantOrder := []string{"tenant-a", "tenant-b", "tenant-c"}
	for i, want := range wantOrder {
		if snap[i].TenantID != want {
			t.Errorf("snap[%d].TenantID = %q, want %q", i, snap[i].TenantID, want)
		}
	}
}

// TestSnapshotIsIndependentCopy verifies mutating a Snapshot result, or
// calling Record after taking a Snapshot, does not affect the other.
func TestSnapshotIsIndependentCopy(t *testing.T) {
	m := New()
	m.Record("tenant-a", 10, 5, 1.0)

	snap := m.Snapshot()
	snap[0].InputTokens = 999999 // mutate the copy

	stats, ok := m.Stats("tenant-a")
	if !ok {
		t.Fatalf("Stats(tenant-a) ok = false, want true")
	}
	if stats.InputTokens != 10 {
		t.Errorf("InputTokens after mutating snapshot copy = %d, want 10 (Meter must be unaffected)", stats.InputTokens)
	}

	// Record more usage after the snapshot was taken; the old snapshot must
	// not observe it.
	m.Record("tenant-a", 100, 100, 1.0)
	if snap[0].InputTokens != 999999 {
		t.Errorf("old snapshot mutated by later Record; got %d", snap[0].InputTokens)
	}
}

// TestEstimateCostUSD verifies the pure cost-from-price-table helper.
func TestEstimateCostUSD(t *testing.T) {
	tests := []struct {
		name                        string
		inputTokens, outputTokens   int
		inPricePer1M, outPricePer1M float64
		want                        float64
	}{
		{"zero tokens", 0, 0, 3.00, 15.00, 0},
		{"1M in only", 1_000_000, 0, 3.00, 15.00, 3.00},
		{"1M out only", 0, 1_000_000, 3.00, 15.00, 15.00},
		{"mixed", 500_000, 200_000, 3.00, 15.00, 1.5 + 3.0},
		{"zero price", 1_000_000, 1_000_000, 0, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateCostUSD(tt.inputTokens, tt.outputTokens, tt.inPricePer1M, tt.outPricePer1M)
			if !floatEquals(got, tt.want) {
				t.Errorf("EstimateCostUSD(%d, %d, %v, %v) = %v, want %v",
					tt.inputTokens, tt.outputTokens, tt.inPricePer1M, tt.outPricePer1M, got, tt.want)
			}
		})
	}
}

// TestRecordConcurrent exercises Record from many goroutines against both a
// shared set of tenants and per-goroutine-unique tenants, then asserts the
// Snapshot totals are exact. Run with -race to catch data races.
func TestRecordConcurrent(t *testing.T) {
	m := New()

	const (
		numGoroutines  = 100
		numRecordsEach = 200
	)
	sharedTenants := []string{"tenant-shared-1", "tenant-shared-2", "tenant-shared-3"}

	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	for g := 0; g < numGoroutines; g++ {
		go func(g int) {
			defer wg.Done()
			uniqueTenant := fmt.Sprintf("tenant-unique-%d", g)
			for r := 0; r < numRecordsEach; r++ {
				// Half the records land on a shared tenant (contention), half on
				// a tenant unique to this goroutine (map growth under
				// concurrency).
				if r%2 == 0 {
					shared := sharedTenants[r%len(sharedTenants)]
					m.Record(shared, 10, 5, 0.001)
				} else {
					m.Record(uniqueTenant, 7, 3, 0.0005)
				}
			}
		}(g)
	}
	wg.Wait()

	snap := m.Snapshot()

	// Verify sorted order is preserved even at this scale.
	for i := 1; i < len(snap); i++ {
		if snap[i-1].TenantID > snap[i].TenantID {
			t.Fatalf("Snapshot not sorted: %q > %q at index %d", snap[i-1].TenantID, snap[i].TenantID, i)
		}
	}

	// Expect numGoroutines unique tenants + 3 shared tenants.
	wantTenantCount := numGoroutines + len(sharedTenants)
	if len(snap) != wantTenantCount {
		t.Fatalf("len(Snapshot()) = %d, want %d", len(snap), wantTenantCount)
	}

	byID := make(map[string]TenantStats, len(snap))
	for _, s := range snap {
		byID[s.TenantID] = s
	}

	// Each unique-tenant goroutine records numRecordsEach/2 "odd r" hits of
	// (7, 3, 0.0005).
	wantUniqueRequests := int64(numRecordsEach / 2)
	for g := 0; g < numGoroutines; g++ {
		id := fmt.Sprintf("tenant-unique-%d", g)
		s, ok := byID[id]
		if !ok {
			t.Fatalf("missing tenant %q in snapshot", id)
		}
		if s.Requests != wantUniqueRequests {
			t.Errorf("%s: Requests = %d, want %d", id, s.Requests, wantUniqueRequests)
		}
		if s.InputTokens != wantUniqueRequests*7 {
			t.Errorf("%s: InputTokens = %d, want %d", id, s.InputTokens, wantUniqueRequests*7)
		}
		if s.OutputTokens != wantUniqueRequests*3 {
			t.Errorf("%s: OutputTokens = %d, want %d", id, s.OutputTokens, wantUniqueRequests*3)
		}
		if want := float64(wantUniqueRequests) * 0.0005; !floatEquals(s.CostUSD, want) {
			t.Errorf("%s: CostUSD = %v, want %v", id, s.CostUSD, want)
		}

		// Cross-check against Stats for the same tenant.
		single, ok := m.Stats(id)
		if !ok {
			t.Fatalf("Stats(%q) ok = false, want true", id)
		}
		if single != s {
			t.Errorf("Stats(%q) = %+v, want %+v (Snapshot value)", id, single, s)
		}
	}

	// Every "even r" record (r=0,2,4,...,numRecordsEach-2 -> numRecordsEach/2
	// hits per goroutine) lands on one of the 3 shared tenants in round-robin
	// order by r%len(sharedTenants). Total shared hits across all goroutines:
	totalSharedHits := int64(numGoroutines * (numRecordsEach / 2))
	var gotSharedRequests, gotSharedInput, gotSharedOutput int64
	var gotSharedCost float64
	for _, id := range sharedTenants {
		s, ok := byID[id]
		if !ok {
			t.Fatalf("missing shared tenant %q in snapshot", id)
		}
		gotSharedRequests += s.Requests
		gotSharedInput += s.InputTokens
		gotSharedOutput += s.OutputTokens
		gotSharedCost += s.CostUSD
	}
	if gotSharedRequests != totalSharedHits {
		t.Errorf("total shared Requests = %d, want %d", gotSharedRequests, totalSharedHits)
	}
	if gotSharedInput != totalSharedHits*10 {
		t.Errorf("total shared InputTokens = %d, want %d", gotSharedInput, totalSharedHits*10)
	}
	if gotSharedOutput != totalSharedHits*5 {
		t.Errorf("total shared OutputTokens = %d, want %d", gotSharedOutput, totalSharedHits*5)
	}
	if want := float64(totalSharedHits) * 0.001; !floatEquals(gotSharedCost, want) {
		t.Errorf("total shared CostUSD = %v, want %v", gotSharedCost, want)
	}
}

// floatEquals compares floats with a small epsilon to tolerate accumulated
// floating-point error from repeated addition.
func floatEquals(a, b float64) bool {
	const epsilon = 1e-9
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= epsilon
}
