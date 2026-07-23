package memory

// OWASP LLM10:2025 — Unbounded Consumption (per-tenant quota under concurrency).
//
// The constitutional quota gate (checked BEFORE any LLM call) is already proven
// for a single tenant by TestLimiterConcurrentCannotExceedBurst. This suite
// EXTENDS that to the multi-tenant burst case the security posture depends on:
// two tenants hammered CONCURRENTLY each stay capped at their OWN bucket, with no
// cross-tenant bleed — one tenant's flood can neither borrow from nor starve
// another's budget. Deterministic: frozen clock (rate 0, no refill), in-memory
// BadgerDB, no model, no network. See docs/security/owasp-llm-coverage.md.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestOWASP_LLM10_ConcurrentMultiTenantBurstIsolation: N goroutines per tenant
// race against each tenant's own bucket; each tenant is granted EXACTLY its burst
// and no more, independently.
func TestOWASP_LLM10_ConcurrentMultiTenantBurstIsolation(t *testing.T) {
	const burst = 5
	l := newTestLimiter(t, 0, burst) // rate 0 → no refill during the test
	frozen := time.Now()
	l.now = func() time.Time { return frozen }

	tenants := []string{"tenant-a", "tenant-b", "tenant-c"}
	const goroutinesPerTenant = 40

	granted := make(map[string]*int64, len(tenants))
	for _, tn := range tenants {
		var n int64
		granted[tn] = &n
	}

	var wg sync.WaitGroup
	for _, tn := range tenants {
		for range goroutinesPerTenant {
			wg.Add(1)
			go func(tenant string) {
				defer wg.Done()
				ok, err := l.Allow(context.Background(), tenant)
				if err != nil {
					t.Errorf("Allow(%s): %v", tenant, err)
					return
				}
				if ok {
					atomic.AddInt64(granted[tenant], 1)
				}
			}(tn)
		}
	}
	wg.Wait()

	for _, tn := range tenants {
		if got := atomic.LoadInt64(granted[tn]); got != burst {
			t.Errorf("tenant %s granted %d tokens under concurrency, want exactly %d — a tenant must never exceed its own bucket nor be starved by another's flood", tn, got, burst)
		}
	}
}

// TestOWASP_LLM10_EmptyTenantRejected: an empty tenant is rejected (fail-closed),
// so a missing identity can never open an unbounded, unattributable spend path.
func TestOWASP_LLM10_EmptyTenantRejected(t *testing.T) {
	l := newTestLimiter(t, 0, 5)
	if ok, err := l.Allow(context.Background(), ""); ok || err == nil {
		t.Fatalf("empty tenant must be rejected fail-closed, got ok=%v err=%v", ok, err)
	}
}
