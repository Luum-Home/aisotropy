package guardrails

import (
	"strings"
	"testing"
)

func has(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

// TestIdentityTokens_NewProviderIsDefendedAutomatically is the whole reason this
// is derived rather than a hardcoded list. Point the stack at a provider nobody
// anticipated and its names must already be denied — with no source edit, no
// list to remember, and therefore no silent gap between switching providers and
// someone noticing the test stopped protecting anything.
func TestIdentityTokens_NewProviderIsDefendedAutomatically(t *testing.T) {
	cases := []struct {
		name, model, baseURL string
		want                 []string
	}{
		{
			name: "poolside", model: "poolside/laguna-s-2.1",
			baseURL: "https://inference.poolside.ai/v1",
			want:    []string{"poolside", "laguna"},
		},
		{
			name: "agnes", model: "agnes-2.0-flash",
			baseURL: "https://apihub.agnes-ai.com/v1",
			want:    []string{"agnes"},
		},
		{
			// A provider invented for this test: nothing in the source knows it.
			name: "an unanticipated vendor", model: "northwind/zephyr-x3",
			baseURL: "https://api.northwind-labs.example/v1",
			want:    []string{"northwind", "zephyr"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IdentityTokens(tc.model, tc.baseURL)
			for _, w := range tc.want {
				if !has(got, w) {
					t.Errorf("token %q not denied for %s — a provider switch would leave it undefended.\n  derived: %v", w, tc.name, got)
				}
			}
		})
	}
}

// TestIdentityTokens_BaselineAlwaysPresent — a model often volunteers that it is
// a GPT or Claude derivative even when reached through a third-party gateway, so
// those names are denied regardless of who is configured.
func TestIdentityTokens_BaselineAlwaysPresent(t *testing.T) {
	got := IdentityTokens("", "")
	for _, w := range []string{"openai", "anthropic", "claude", "gemini", "llama", "mistral"} {
		if !has(got, w) {
			t.Errorf("baseline token %q missing with no provider configured", w)
		}
	}
}

// TestIdentityTokens_NoGenericFragments is the counterweight. Denying a fragment
// like "flash", "chat" or "api" would make the assistant refuse ordinary
// sentences — turning a security control into a usability bug. Over-blocking is
// its own failure mode, not extra safety.
func TestIdentityTokens_NoGenericFragments(t *testing.T) {
	got := IdentityTokens("poolside/laguna-s-2.1", "https://inference.poolside.ai/v1")
	for _, bad := range []string{"flash", "inference", "chat", "api", "model", "v1", "https", "ai", "com", "2", "1"} {
		if has(got, bad) {
			t.Errorf("generic fragment %q was denied — this would make the assistant refuse ordinary text.\n  derived: %v", bad, got)
		}
	}
	for _, tok := range got {
		if len(tok) < 4 {
			t.Errorf("token %q is shorter than 4 chars — too collision-prone to deny", tok)
		}
	}
}

// TestIdentityTokens_Deterministic — the same configuration must always produce
// the same denial set, or a safeguard result is not reproducible.
func TestIdentityTokens_Deterministic(t *testing.T) {
	first := strings.Join(IdentityTokens("poolside/laguna-s-2.1", "https://inference.poolside.ai/v1"), ",")
	for i := 0; i < 20; i++ {
		got := strings.Join(IdentityTokens("poolside/laguna-s-2.1", "https://inference.poolside.ai/v1"), ",")
		if got != first {
			t.Fatalf("iteration %d differs:\n  %s\n  %s", i, first, got)
		}
	}
}

// TestLeakedIdentityTokens_ReportsWhatLeaked — a caller needs to know WHICH name
// escaped, not merely that something did; "leaked" without the token is an alert
// nobody can act on.
func TestLeakedIdentityTokens_ReportsWhatLeaked(t *testing.T) {
	model, base := "poolside/laguna-s-2.1", "https://inference.poolside.ai/v1"

	leaked := LeakedIdentityTokens("I am Laguna, served by Poolside.", model, base)
	if !has(leaked, "poolside") || !has(leaked, "laguna") {
		t.Errorf("expected both vendor and model names reported, got %v", leaked)
	}

	if got := LeakedIdentityTokens("I am the assistant for Nimbus Rail. How can I help?", model, base); len(got) != 0 {
		t.Errorf("clean reply reported leaks: %v", got)
	}

	// Case-insensitive: a capitalised disclosure is still a disclosure.
	if got := LeakedIdentityTokens("Built on POOLSIDE infrastructure.", model, base); !has(got, "poolside") {
		t.Errorf("uppercase disclosure not detected: %v", got)
	}
}
