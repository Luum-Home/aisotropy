package facts

import "testing"

func TestHeuristicExtractor(t *testing.T) {
	sc := Scope{tenant: "acme", user: "alice"}
	var ex HeuristicExtractor

	cases := []struct {
		name        string
		user        string
		wantCount   int
		wantContent string
		wantImp     float64
	}{
		{"remember that", "Remember that the billing DB is postgres.", 1, "the billing DB is postgres", 0.9},
		{"remember plain", "please remember to renew the TLS cert", 1, "to renew the TLS cert", 0.85},
		{"identity", "My name is Ada Lovelace", 1, "My name is Ada Lovelace", 0.8},
		{"preference", "I prefer dark mode in the dashboard", 1, "I prefer dark mode in the dashboard", 0.7},
		{"negative preference", "I never use tabs, only spaces", 1, "I never use tabs, only spaces", 0.7},
		{"no signal", "what is the weather today?", 0, "", 0},
		{"empty", "   ", 0, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ex.Extract(sc, Turn{UserMessage: tc.user, AssistantMessage: "ok"})
			if len(got) != tc.wantCount {
				t.Fatalf("count = %d (%+v), want %d", len(got), got, tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			if got[0].Content != tc.wantContent {
				t.Fatalf("content = %q, want %q", got[0].Content, tc.wantContent)
			}
			if got[0].Importance != tc.wantImp {
				t.Fatalf("importance = %v, want %v", got[0].Importance, tc.wantImp)
			}
			if got[0].TenantID != sc.Tenant() || got[0].UserID != sc.User() {
				t.Fatalf("scope not stamped: %+v", got[0])
			}
			if got[0].ID == "" {
				t.Fatal("empty ID")
			}
		})
	}
}

func TestFactIDIsStableAndScoped(t *testing.T) {
	a := factID(Scope{tenant: "acme", user: "alice"}, "prefers dark mode")
	aAgain := factID(Scope{tenant: "acme", user: "alice"}, "Prefers Dark Mode") // case-insensitive
	if a != aAgain {
		t.Fatalf("ids differ by case: %q vs %q", a, aAgain)
	}
	other := factID(Scope{tenant: "globex", user: "alice"}, "prefers dark mode")
	if a == other {
		t.Fatal("ids collide across tenants — isolation risk in idempotency key")
	}
}

// TestExtractorSavesRoundTrip wires the extractor to the store: an extracted
// fact is searchable, proving the online-extraction path end to end.
func TestExtractorSavesRoundTrip(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{tenant: "acme", user: "alice"}
	facts := HeuristicExtractor{}.Extract(sc, Turn{UserMessage: "Remember that the primary region is eu-west-1"})
	if len(facts) != 1 {
		t.Fatalf("want 1 extracted fact, got %d", len(facts))
	}
	if err := st.Save(t.Context(), sc, facts[0]); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Search(t.Context(), sc, "region", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Content != "the primary region is eu-west-1" {
		t.Fatalf("round-trip failed: %+v", got)
	}
}
