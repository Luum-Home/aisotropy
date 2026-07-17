package api

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/lang"
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

// TestMultilingualPersonaProviderSelectsByLanguage covers the three required
// cases: (a) a persona registered for "es" is returned when ctx language is "es";
// (b) an unregistered language falls back to the product's default (English)
// persona; (c) with no language set on ctx, the default-language persona is
// served — i.e. behavior is unchanged from a single-language registration.
func TestMultilingualPersonaProviderSelectsByLanguage(t *testing.T) {
	const (
		enPrompt = "You are WeatherBot."
		esPrompt = "Eres WeatherBot."
	)
	provider := NewMultilingualPersonaProvider(map[PersonaKey]string{
		{ProductID: "weatherbot", Language: "en"}: enPrompt,
		{ProductID: "weatherbot", Language: "es"}: esPrompt,
	})

	// (a) ctx language "es" -> the Spanish literal.
	got, err := provider.BasePrompt(lang.WithLanguage(context.Background(), "es"), "weatherbot")
	if err != nil {
		t.Fatalf("es BasePrompt error: %v", err)
	}
	if got != esPrompt {
		t.Errorf("es BasePrompt = %q, want %q", got, esPrompt)
	}

	// (b) unregistered language ("ja") -> the product's default-language persona.
	got, err = provider.BasePrompt(lang.WithLanguage(context.Background(), "ja"), "weatherbot")
	if err != nil {
		t.Fatalf("ja BasePrompt error: %v", err)
	}
	if got != enPrompt {
		t.Errorf("unregistered-language BasePrompt = %q, want default-language %q", got, enPrompt)
	}

	// (c) no language on ctx -> the default-language persona (unchanged behavior).
	got, err = provider.BasePrompt(context.Background(), "weatherbot")
	if err != nil {
		t.Fatalf("no-language BasePrompt error: %v", err)
	}
	if got != enPrompt {
		t.Errorf("no-language BasePrompt = %q, want default-language %q", got, enPrompt)
	}
}

// TestMultilingualPersonaProviderFallsBackToBuiltinDefault: a product with no
// registered persona at all degrades to the built-in default persona, byte-for-
// byte identical to what defaultPersonaProvider serves — the default binary's
// persona is never changed by introducing the multilingual seam.
func TestMultilingualPersonaProviderFallsBackToBuiltinDefault(t *testing.T) {
	provider := NewMultilingualPersonaProvider(map[PersonaKey]string{
		{ProductID: "weatherbot", Language: "en"}: "You are WeatherBot.",
	})

	ctx := lang.WithLanguage(context.Background(), "es")
	want, _ := defaultPersonaProvider{}.BasePrompt(ctx, "unknown-product")
	got, err := provider.BasePrompt(ctx, "unknown-product")
	if err != nil {
		t.Fatalf("fallback BasePrompt error: %v", err)
	}
	if got != want {
		t.Errorf("fallback BasePrompt = %q, want built-in default %q", got, want)
	}
}

// TestMultilingualPersonaProviderPrimarySubtagAndEmptyKey verifies two ergonomic
// behaviors: a regional tag ("es-AR") finds the base-language ("es") persona, and
// an empty Language key registers the default-language persona.
func TestMultilingualPersonaProviderPrimarySubtagAndEmptyKey(t *testing.T) {
	const (
		defPrompt = "You are the default persona."
		esPrompt  = "Eres la persona en espanol."
	)
	provider := NewMultilingualPersonaProvider(map[PersonaKey]string{
		{ProductID: "bot"}:                 defPrompt, // empty Language -> default language
		{ProductID: "bot", Language: "es"}: esPrompt,
	})

	// Regional tag resolves to the primary-subtag persona.
	got, err := provider.BasePrompt(lang.WithLanguage(context.Background(), "es-AR"), "bot")
	if err != nil {
		t.Fatalf("es-AR BasePrompt error: %v", err)
	}
	if got != esPrompt {
		t.Errorf("es-AR BasePrompt = %q, want %q (primary subtag)", got, esPrompt)
	}

	// Empty-language registration is served as the default-language persona.
	got, err = provider.BasePrompt(context.Background(), "bot")
	if err != nil {
		t.Fatalf("default BasePrompt error: %v", err)
	}
	if got != defPrompt {
		t.Errorf("default BasePrompt = %q, want %q (empty Language key)", got, defPrompt)
	}
}
