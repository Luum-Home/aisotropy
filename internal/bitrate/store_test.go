package bitrate

import (
	"context"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func TestStoreMeasurementRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.UnixMilli(1_700_000_000_000).UTC()

	// Three measurements for (acme, alice) with a rising lift → positive slope.
	for i, lift := range []float64{0.5, 1.0, 1.5} {
		m := Measurement{
			TenantID: "acme", UserID: "alice",
			MeasuredAt: base.Add(time.Duration(i) * time.Minute),
			BptWith:    3.0 - lift, BptWithout: 3.0, Lift: lift,
			NTokens: 10, ModelID: "distilgpt2@pinned", SampleID: sampleIDFor(i),
		}
		if err := s.Save(ctx, m); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	latest, ok, err := s.Latest(ctx, "acme", "alice")
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if latest.Lift != 1.5 {
		t.Fatalf("latest lift = %v, want 1.5", latest.Lift)
	}

	series, err := s.Series(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(series) != 3 {
		t.Fatalf("series len = %d, want 3", len(series))
	}

	slope, err := s.TrendSlope(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("TrendSlope: %v", err)
	}
	if slope <= 0 {
		t.Fatalf("trend slope = %v, want positive (rising lift)", slope)
	}
}

func TestStoreTenantIsolation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	acme := Measurement{TenantID: "acme", UserID: "alice", Lift: 2.0, BptWith: 1.0, BptWithout: 3.0, NTokens: 5, ModelID: "m@pinned", SampleID: "a1"}
	globex := Measurement{TenantID: "globex", UserID: "bob", Lift: 9.0, BptWith: 1.0, BptWithout: 10.0, NTokens: 5, ModelID: "m@pinned", SampleID: "g1"}
	if err := s.Save(ctx, acme); err != nil {
		t.Fatalf("Save acme: %v", err)
	}
	if err := s.Save(ctx, globex); err != nil {
		t.Fatalf("Save globex: %v", err)
	}

	// A scoped read never sees another tenant's data.
	got, ok, err := s.Latest(ctx, "acme", "alice")
	if err != nil || !ok {
		t.Fatalf("Latest acme: ok=%v err=%v", ok, err)
	}
	if got.Lift != 2.0 {
		t.Fatalf("acme lift = %v, want 2.0 (globex leaked in?)", got.Lift)
	}

	// A cross-tenant identity with no rows returns ok=false, not another
	// tenant's row.
	if _, ok, err := s.Latest(ctx, "acme", "bob"); err != nil || ok {
		t.Fatalf("Latest(acme,bob) should be empty: ok=%v err=%v", ok, err)
	}

	// The ops cross-tenant view aggregates both tenants, each isolated.
	summaries, err := s.AllSummaries(ctx)
	if err != nil {
		t.Fatalf("AllSummaries: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %d, want 2", len(summaries))
	}
	byTenant := map[string]TenantSummary{}
	for _, su := range summaries {
		byTenant[su.TenantID] = su
	}
	if byTenant["acme"].LatestLift != 2.0 || byTenant["globex"].LatestLift != 9.0 {
		t.Fatalf("summaries mixed tenants: %+v", byTenant)
	}
}

func TestStoreReserveAndHeldOut(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.Reserve(ctx, "acme", "alice", "sess1", "r1", "hola que tal"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := s.Reserve(ctx, "globex", "bob", "sess2", "r2", "buenas"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// Idempotent re-reserve of the same sample_id within scope must not duplicate.
	if err := s.Reserve(ctx, "acme", "alice", "sess1", "r1", "hola que tal (edit)"); err != nil {
		t.Fatalf("Reserve idempotent: %v", err)
	}

	held, err := s.HeldOut(ctx, 10)
	if err != nil {
		t.Fatalf("HeldOut: %v", err)
	}
	if len(held) != 2 {
		t.Fatalf("held-out = %d, want 2 (idempotent re-reserve duplicated?)", len(held))
	}
	// Each held-out sample carries its own tenant/user for scoped re-hydration.
	for _, h := range held {
		if h.TenantID == "acme" && h.UserID != "alice" {
			t.Fatalf("acme sample has wrong user: %+v", h)
		}
	}
}

func TestStoreRejectsEmptyScope(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.Save(ctx, Measurement{TenantID: "", UserID: "alice", SampleID: "x"}); err == nil {
		t.Fatal("Save with empty tenant must be rejected by the isolation gate")
	}
	if err := s.Reserve(ctx, "acme", "", "s", "r", "t"); err == nil {
		t.Fatal("Reserve with empty user must be rejected by the isolation gate")
	}
}

func sampleIDFor(i int) string {
	return []string{"s0", "s1", "s2", "s3", "s4"}[i]
}
