package primitives

import (
	"context"
	"sync"
	"testing"
)

func TestStoreCRUD(t *testing.T) {
	s := NewStore()

	if _, ok := s.Get("t1", "u1", "search"); ok {
		t.Fatalf("Get on empty store returned ok=true, want false")
	}

	p := Primitive{
		Name:        "search",
		TenantID:    "t1",
		UserID:      "u1",
		Description: "Search the web",
		Examples:    []string{"query: weather in Paris"},
	}
	s.Put(p)

	got, ok := s.Get("t1", "u1", "search")
	if !ok {
		t.Fatalf("Get after Put returned ok=false, want true")
	}
	if got.Description != "Search the web" || got.UpdatedAt.IsZero() {
		t.Fatalf("Get returned unexpected primitive: %+v", got)
	}

	// Upsert overwrites.
	p.Description = "Search the web, precisely"
	s.Put(p)
	got, _ = s.Get("t1", "u1", "search")
	if got.Description != "Search the web, precisely" {
		t.Fatalf("Put did not upsert: got description %q", got.Description)
	}

	// A second primitive for the same user.
	s.Put(Primitive{Name: "calendar", TenantID: "t1", UserID: "u1", Description: "Manage calendar"})
	list := s.ListForUser("t1", "u1")
	if len(list) != 2 {
		t.Fatalf("ListForUser returned %d primitives, want 2", len(list))
	}

	// A different user with the same primitive name must not collide.
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u2", Description: "u2's search clone"})
	if len(s.ListForUser("t1", "u1")) != 2 {
		t.Fatalf("ListForUser(t1,u1) affected by u2's Put")
	}
	if len(s.ListForUser("t1", "u2")) != 1 {
		t.Fatalf("ListForUser(t1,u2) = %d, want 1", len(s.ListForUser("t1", "u2")))
	}
}

func TestRecordFrictionAccrual(t *testing.T) {
	s := NewStore()
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1"})

	p, err := s.RecordFriction("t1", "u1", "search", 1.5)
	if err != nil {
		t.Fatalf("RecordFriction returned error: %v", err)
	}
	if p.FrictionScore != 1.5 {
		t.Fatalf("FrictionScore = %v, want 1.5", p.FrictionScore)
	}

	p, err = s.RecordFriction("t1", "u1", "search", 2.5)
	if err != nil {
		t.Fatalf("RecordFriction returned error: %v", err)
	}
	if p.FrictionScore != 4.0 {
		t.Fatalf("FrictionScore after second accrual = %v, want 4.0", p.FrictionScore)
	}

	// Confirm the store itself was updated, not just the returned copy.
	stored, ok := s.Get("t1", "u1", "search")
	if !ok || stored.FrictionScore != 4.0 {
		t.Fatalf("stored FrictionScore = %+v, want 4.0", stored)
	}

	if _, err := s.RecordFriction("t1", "u1", "search", -1); err == nil {
		t.Fatalf("RecordFriction with negative delta returned nil error, want error")
	}

	// RecordFriction on a name never Put still creates and tracks a primitive.
	p, err = s.RecordFriction("t1", "u1", "never-put", 1)
	if err != nil {
		t.Fatalf("RecordFriction on unregistered primitive returned error: %v", err)
	}
	if p.FrictionScore != 1 || p.Name != "never-put" {
		t.Fatalf("RecordFriction on unregistered primitive = %+v", p)
	}

	s.ResetFriction("t1", "u1", "search")
	stored, _ = s.Get("t1", "u1", "search")
	if stored.FrictionScore != 0 {
		t.Fatalf("FrictionScore after ResetFriction = %v, want 0", stored.FrictionScore)
	}

	// ResetFriction on an absent key is a documented no-op, not a panic.
	s.ResetFriction("t1", "u1", "absent")
}

func TestFrictionDetectorObserve(t *testing.T) {
	s := NewStore()
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1"})
	d := NewFrictionDetector(s)

	if _, err := d.Observe("t1", "u1", "search", SignalRetry); err != nil {
		t.Fatalf("Observe(SignalRetry) error: %v", err)
	}
	if _, err := d.Observe("t1", "u1", "search", SignalFailure); err != nil {
		t.Fatalf("Observe(SignalFailure) error: %v", err)
	}
	if _, err := d.Observe("t1", "u1", "search", SignalUserCorrection); err != nil {
		t.Fatalf("Observe(SignalUserCorrection) error: %v", err)
	}

	got, _ := s.Get("t1", "u1", "search")
	want := SignalRetry.weight() + SignalFailure.weight() + SignalUserCorrection.weight()
	if got.FrictionScore != want {
		t.Fatalf("FrictionScore after 3 signals = %v, want %v", got.FrictionScore, want)
	}
}

// TestStoreConcurrentFrictionRecording exercises the exact concurrency
// scenario called out in the acceptance criteria: many goroutines record
// friction on the SAME primitive concurrently, and the final score must be
// exactly the sum of every recorded delta (no lost updates).
func TestStoreConcurrentFrictionRecording(t *testing.T) {
	s := NewStore()
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1"})

	const goroutines = 50
	const perGoroutine = 20
	const delta = 1.0

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if _, err := s.RecordFriction("t1", "u1", "search", delta); err != nil {
					t.Errorf("RecordFriction error: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	got, ok := s.Get("t1", "u1", "search")
	if !ok {
		t.Fatalf("primitive missing after concurrent RecordFriction")
	}
	want := float64(goroutines*perGoroutine) * delta
	if got.FrictionScore != want {
		t.Fatalf("FrictionScore = %v, want exactly %v (lost update under concurrency)", got.FrictionScore, want)
	}
}

// TestStoreConcurrentTenantUserIsolation runs concurrent friction recording
// across many distinct (tenant, user) pairs against primitives with the SAME
// name, and verifies no cross-contamination: each key's final score is
// exactly its own accrual, and no other tenant/user's primitive was created
// as a side effect.
func TestStoreConcurrentTenantUserIsolation(t *testing.T) {
	s := NewStore()
	const pairs = 10
	const perPair = 30

	var wg sync.WaitGroup
	for i := 0; i < pairs; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perPair; j++ {
				if _, err := s.RecordFriction(tenantID(i), userID(i), "search", 1.0); err != nil {
					t.Errorf("RecordFriction error: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < pairs; i++ {
		got, ok := s.Get(tenantID(i), userID(i), "search")
		if !ok {
			t.Fatalf("primitive for pair %d missing", i)
		}
		if got.FrictionScore != float64(perPair) {
			t.Fatalf("pair %d FrictionScore = %v, want %v", i, got.FrictionScore, float64(perPair))
		}
	}
}

func tenantID(i int) string { return "tenant-" + string(rune('a'+i)) }
func userID(i int) string   { return "user-" + string(rune('a'+i)) }

func TestHeuristicRewriter(t *testing.T) {
	rw := HeuristicRewriter{}
	ctx := context.Background()

	p := Primitive{
		Name:          "search",
		TenantID:      "t1",
		UserID:        "u1",
		Description:   "Search the web",
		Examples:      []string{"query: weather in Paris"},
		FrictionScore: 10,
		Revision:      0,
	}

	revised, err := rw.Rewrite(ctx, p)
	if err != nil {
		t.Fatalf("Rewrite returned error: %v", err)
	}
	if revised.Revision != 1 {
		t.Fatalf("Revision = %d, want 1", revised.Revision)
	}
	if revised.FrictionScore != 0 {
		t.Fatalf("FrictionScore after rewrite = %v, want 0", revised.FrictionScore)
	}
	if len(revised.Examples) != len(p.Examples)+1 {
		t.Fatalf("Examples len = %d, want %d", len(revised.Examples), len(p.Examples)+1)
	}
	if revised.Examples[0] != p.Examples[0] {
		t.Fatalf("Rewrite must not remove existing examples: got %v", revised.Examples)
	}
	if revised.Description == p.Description {
		t.Fatalf("Description was not sharpened: %q", revised.Description)
	}

	// Original examples slice must not be mutated in place (no aliasing bug).
	if len(p.Examples) != 1 {
		t.Fatalf("original Examples slice was mutated: %v", p.Examples)
	}

	// A second rewrite bumps Revision again and appends another example.
	revisedTwice, err := rw.Rewrite(ctx, revised)
	if err != nil {
		t.Fatalf("second Rewrite returned error: %v", err)
	}
	if revisedTwice.Revision != 2 {
		t.Fatalf("Revision after second rewrite = %d, want 2", revisedTwice.Revision)
	}
	if len(revisedTwice.Examples) != 3 {
		t.Fatalf("Examples len after second rewrite = %d, want 3", len(revisedTwice.Examples))
	}
}

func TestConsolidateRewritesOnlyOverThreshold(t *testing.T) {
	s := NewStore()
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1", Description: "d1", FrictionScore: 10})
	s.Put(Primitive{Name: "calendar", TenantID: "t1", UserID: "u1", Description: "d2", FrictionScore: 2})
	s.Put(Primitive{Name: "email", TenantID: "t1", UserID: "u2", Description: "d3", FrictionScore: 5})

	const threshold = 5.0
	rewritten, err := Consolidate(context.Background(), s, HeuristicRewriter{}, threshold)
	if err != nil {
		t.Fatalf("Consolidate returned error: %v", err)
	}
	if rewritten != 2 {
		t.Fatalf("Consolidate rewrote %d primitives, want 2 (search and email)", rewritten)
	}

	search, _ := s.Get("t1", "u1", "search")
	if search.Revision != 1 || search.FrictionScore != 0 {
		t.Fatalf("search was not rewritten correctly: %+v", search)
	}
	email, _ := s.Get("t1", "u2", "email")
	if email.Revision != 1 || email.FrictionScore != 0 {
		t.Fatalf("email was not rewritten correctly: %+v", email)
	}
	calendar, _ := s.Get("t1", "u1", "calendar")
	if calendar.Revision != 0 || calendar.FrictionScore != 2 {
		t.Fatalf("calendar (below threshold) was modified: %+v", calendar)
	}
}

func TestConsolidateIsIdempotentAcrossRuns(t *testing.T) {
	s := NewStore()
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1", Description: "d1", FrictionScore: 10})

	const threshold = 5.0
	first, err := Consolidate(context.Background(), s, HeuristicRewriter{}, threshold)
	if err != nil {
		t.Fatalf("first Consolidate returned error: %v", err)
	}
	if first != 1 {
		t.Fatalf("first Consolidate rewrote %d, want 1", first)
	}

	// No new friction recorded: a second run must be a no-op.
	second, err := Consolidate(context.Background(), s, HeuristicRewriter{}, threshold)
	if err != nil {
		t.Fatalf("second Consolidate returned error: %v", err)
	}
	if second != 0 {
		t.Fatalf("second Consolidate rewrote %d, want 0 (idempotent)", second)
	}

	got, _ := s.Get("t1", "u1", "search")
	if got.Revision != 1 {
		t.Fatalf("Revision after idempotent second run = %d, want still 1", got.Revision)
	}

	// New friction accrues past the threshold again: a third run rewrites it.
	if _, err := s.RecordFriction("t1", "u1", "search", threshold); err != nil {
		t.Fatalf("RecordFriction error: %v", err)
	}
	third, err := Consolidate(context.Background(), s, HeuristicRewriter{}, threshold)
	if err != nil {
		t.Fatalf("third Consolidate returned error: %v", err)
	}
	if third != 1 {
		t.Fatalf("third Consolidate rewrote %d, want 1 after new friction accrued", third)
	}
	got, _ = s.Get("t1", "u1", "search")
	if got.Revision != 2 {
		t.Fatalf("Revision after third run = %d, want 2", got.Revision)
	}
}

func TestConsolidateTenantUserIsolation(t *testing.T) {
	s := NewStore()
	// Same primitive name, same tenant, different users: only the one over
	// threshold should be rewritten; the other user's friction/description
	// must be untouched.
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1", Description: "u1 guidance", FrictionScore: 100})
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u2", Description: "u2 guidance", FrictionScore: 0})

	rewritten, err := Consolidate(context.Background(), s, HeuristicRewriter{}, 5.0)
	if err != nil {
		t.Fatalf("Consolidate returned error: %v", err)
	}
	if rewritten != 1 {
		t.Fatalf("Consolidate rewrote %d, want 1", rewritten)
	}

	u1, _ := s.Get("t1", "u1", "search")
	if u1.Revision != 1 || u1.FrictionScore != 0 {
		t.Fatalf("u1's primitive not rewritten as expected: %+v", u1)
	}
	u2, _ := s.Get("t1", "u2", "search")
	if u2.Revision != 0 || u2.Description != "u2 guidance" || u2.FrictionScore != 0 {
		t.Fatalf("u2's primitive was affected by u1's friction/rewrite: %+v", u2)
	}
}

func TestConsolidateValidatesArguments(t *testing.T) {
	s := NewStore()
	if _, err := Consolidate(context.Background(), nil, HeuristicRewriter{}, 1.0); err == nil {
		t.Fatalf("Consolidate with nil store returned nil error, want error")
	}
	if _, err := Consolidate(context.Background(), s, nil, 1.0); err == nil {
		t.Fatalf("Consolidate with nil rewriter returned nil error, want error")
	}
}

func TestConsolidateRespectsContextCancellation(t *testing.T) {
	s := NewStore()
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1", FrictionScore: 10})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Consolidate(ctx, s, HeuristicRewriter{}, 5.0)
	if err == nil {
		t.Fatalf("Consolidate with a cancelled context returned nil error, want context.Canceled")
	}
}

// TestStoreExamplesNotAliased proves a Primitive returned from the Store owns
// its Examples slice: mutating it (or the slice a caller passed to Put) does
// not corrupt the stored primitive. Primitive is held by value in the map, so
// without a defensive copy the Examples backing array would be shared.
func TestStoreExamplesNotAliased(t *testing.T) {
	s := NewStore()
	orig := []string{"example-0"}
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1", Examples: orig})

	// Mutating the caller's original slice must not reach into the store.
	orig[0] = "mutated-by-caller"

	got, ok := s.Get("t1", "u1", "search")
	if !ok {
		t.Fatalf("Get: primitive not found")
	}
	if got.Examples[0] != "example-0" {
		t.Fatalf("stored Examples aliased the caller's slice: got %q", got.Examples[0])
	}

	// Mutating a returned copy must not reach into the store either.
	got.Examples[0] = "mutated-by-reader"
	again, _ := s.Get("t1", "u1", "search")
	if again.Examples[0] != "example-0" {
		t.Fatalf("returned Examples aliased the stored slice: got %q", again.Examples[0])
	}
}

// TestZeroValueStoreWritable proves a zero-value Store (no NewStore) is safe to
// write to: the map is lazily initialized under the lock rather than panicking.
func TestZeroValueStoreWritable(t *testing.T) {
	var s Store
	s.Put(Primitive{Name: "search", TenantID: "t1", UserID: "u1"})
	if _, ok := s.Get("t1", "u1", "search"); !ok {
		t.Fatalf("zero-value Store did not retain the Put primitive")
	}
	if _, err := s.RecordFriction("t1", "u1", "other", 1.0); err != nil {
		t.Fatalf("RecordFriction on zero-value Store: %v", err)
	}
}
