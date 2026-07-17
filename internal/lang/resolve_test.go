package lang

import "testing"

// TestResolve exhaustively pins the detect-vs-remember precedence (ADR-0053):
// a confident detection wins and is persisted only when changed; an Undetermined
// detection inherits the stored language and is never persisted; and with no
// signal on either side the result is Undetermined with no write.
func TestResolve(t *testing.T) {
	tests := []struct {
		name          string
		detected      Language
		stored        Language
		wantEffective Language
		wantPersist   bool
	}{
		{
			name:          "confident detection, nothing stored -> learn it",
			detected:      Spanish,
			stored:        Undetermined,
			wantEffective: Spanish,
			wantPersist:   true,
		},
		{
			name:          "confident detection differs from stored -> update",
			detected:      French,
			stored:        Spanish,
			wantEffective: French,
			wantPersist:   true,
		},
		{
			name:          "confident detection equals stored -> no rewrite",
			detected:      Spanish,
			stored:        Spanish,
			wantEffective: Spanish,
			wantPersist:   false,
		},
		{
			name:          "undetermined detection, stored present -> inherit, no write",
			detected:      Undetermined,
			stored:        Spanish,
			wantEffective: Spanish,
			wantPersist:   false,
		},
		{
			name:          "undetermined detection, nothing stored -> undetermined, no write",
			detected:      Undetermined,
			stored:        Undetermined,
			wantEffective: Undetermined,
			wantPersist:   false,
		},
		{
			name:          "empty (zero-value) detection, stored present -> inherit",
			detected:      "",
			stored:        German,
			wantEffective: German,
			wantPersist:   false,
		},
		{
			name:          "confident detection, empty (never-stored) zero value -> learn it",
			detected:      Japanese,
			stored:        "",
			wantEffective: Japanese,
			wantPersist:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotEffective, gotPersist := Resolve(tc.detected, tc.stored)
			if gotEffective != tc.wantEffective {
				t.Errorf("effective = %q, want %q", gotEffective, tc.wantEffective)
			}
			if gotPersist != tc.wantPersist {
				t.Errorf("persist = %v, want %v", gotPersist, tc.wantPersist)
			}
		})
	}
}

// TestResolve_LowSignalNeverOverwrites is the property the persistence contract
// hinges on: a low-signal follow-up must not erase a previously learned language.
func TestResolve_LowSignalNeverOverwrites(t *testing.T) {
	if _, persist := Resolve(Undetermined, Spanish); persist {
		t.Fatal("an Undetermined follow-up must never persist over a stored language")
	}
}
