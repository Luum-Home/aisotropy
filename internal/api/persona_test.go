package api

import (
	"context"
	"testing"
)

// TestDefaultPersonaProviderMatchesLegacyStrings pins the default persona to the
// exact literals the pre-Phase-8 systemPromptFor returned, so extracting the
// seam did not change the default binary's persona.
func TestDefaultPersonaProviderMatchesLegacyStrings(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{
		"":             "You are the Aisotropy agent. Respond briefly and directly.",
		"analytics-v1": `You are the Aisotropy agent for product "analytics-v1". Respond briefly and directly.`,
	}
	var p defaultPersonaProvider
	for productID, want := range cases {
		got, err := p.BasePrompt(ctx, productID)
		if err != nil {
			t.Fatalf("BasePrompt(%q) error: %v", productID, err)
		}
		if got != want {
			t.Errorf("BasePrompt(%q) = %q, want %q", productID, got, want)
		}
	}
}

// TestMapPersonaProviderRegisteredAndFallback: a registered product_id returns
// its literal; an unregistered one falls back to the default persona (identical
// to what defaultPersonaProvider would return) — the anti-magic contract, since
// every returned prompt is a literal string.
func TestMapPersonaProviderRegisteredAndFallback(t *testing.T) {
	ctx := context.Background()
	provider := NewMapPersonaProvider(map[string]string{
		"weatherbot": "You are WeatherBot.",
	})

	got, err := provider.BasePrompt(ctx, "weatherbot")
	if err != nil {
		t.Fatalf("registered BasePrompt error: %v", err)
	}
	if got != "You are WeatherBot." {
		t.Errorf("registered BasePrompt = %q, want the registered literal", got)
	}

	// Unknown product_id: must equal the default provider's output exactly.
	want, _ := defaultPersonaProvider{}.BasePrompt(ctx, "unknown-product")
	got, err = provider.BasePrompt(ctx, "unknown-product")
	if err != nil {
		t.Fatalf("fallback BasePrompt error: %v", err)
	}
	if got != want {
		t.Errorf("fallback BasePrompt = %q, want default %q", got, want)
	}
}

// TestMapPersonaProviderCopiesInput: mutating the caller's map after
// construction must not change registered personas (defensive copy).
func TestMapPersonaProviderCopiesInput(t *testing.T) {
	src := map[string]string{"p1": "original"}
	provider := NewMapPersonaProvider(src)
	src["p1"] = "mutated"

	got, err := provider.BasePrompt(context.Background(), "p1")
	if err != nil {
		t.Fatalf("BasePrompt error: %v", err)
	}
	if got != "original" {
		t.Errorf("BasePrompt = %q, want %q (input map must be copied)", got, "original")
	}
}
