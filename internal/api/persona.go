package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/lang"
)

// defaultPersonaLanguage is the language a multilingual persona falls back to
// when the request carries no language, or a language for which the product has
// no registered persona. It is the language of the built-in English default
// persona, so an unconfigured or unmatched language always degrades to the same
// literal the default binary has always served.
const defaultPersonaLanguage = "en"

// The request language travels on context.Context via the canonical
// internal/lang seam (lang.WithLanguage, written once by the chat handlers;
// lang.FromContext, read here). The persona layer only READS it, and falls back
// to defaultPersonaLanguage whenever it is absent/Undetermined.

// normalizePersonaLanguage lowercases and trims a language tag so registration
// and lookup agree on a canonical form. It does NOT invent a value: an empty or
// whitespace-only tag normalizes to "", which callers treat as "no language".
func normalizePersonaLanguage(lang string) string {
	return strings.ToLower(strings.TrimSpace(lang))
}

// PersonaProvider supplies the base system prompt (the agent's persona) for a
// given product_id. It is the Phase 8 adoption seam that replaces the previously
// private, hard-coded systemPromptFor: the runtime injects a provider so a
// consumer can define per-product personas without editing this package.
//
// Anti-magic (ADR-0002): BasePrompt MUST return a literal string the consumer
// wrote — there is no templating engine behind this interface. The hydration
// layer (hydrateSystemPrompt) then appends memory/tool context to whatever
// literal the provider returns; the provider itself never templates.
type PersonaProvider interface {
	// BasePrompt returns the base system prompt for productID. An empty
	// productID is valid and yields the generic agent persona.
	BasePrompt(ctx context.Context, productID string) (string, error)
}

// defaultPersonaProvider reproduces the original hard-coded systemPromptFor
// behavior exactly. It is the fallback whenever no provider is configured (a nil
// PersonaProvider) and the base of the map-backed provider for unknown products,
// so the runtime never has a "no persona" state.
type defaultPersonaProvider struct{}

// BasePrompt returns the minimal built-in persona for productID.
func (defaultPersonaProvider) BasePrompt(_ context.Context, productID string) (string, error) {
	return systemPromptFor(productID), nil
}

// mapPersonaProvider serves a per-product base prompt from a fixed map of
// literal strings, falling back to the default provider for any product_id not
// in the map. Every value is a literal the consumer wrote (anti-magic gate).
type mapPersonaProvider struct {
	prompts  map[string]string
	fallback PersonaProvider
}

// NewMapPersonaProvider builds a PersonaProvider over prompts (product_id ->
// literal base prompt), falling back to the built-in default persona for any
// product_id absent from the map. The map is copied defensively so later
// mutation of the caller's map cannot change registered personas.
func NewMapPersonaProvider(prompts map[string]string) PersonaProvider {
	m := make(map[string]string, len(prompts))
	for k, v := range prompts {
		m[k] = v
	}
	return mapPersonaProvider{prompts: m, fallback: defaultPersonaProvider{}}
}

// BasePrompt returns the registered literal for productID, or the default
// persona when productID has no registered persona.
func (m mapPersonaProvider) BasePrompt(ctx context.Context, productID string) (string, error) {
	if p, ok := m.prompts[productID]; ok {
		return p, nil
	}
	return m.fallback.BasePrompt(ctx, productID)
}

// PersonaKey identifies a per-(product, language) persona in a multilingual
// provider. Language is a BCP-47 / ISO-639 code (e.g. "es", "en", "ja"); an
// empty Language registers the product's default-language persona, and is
// normalized to defaultPersonaLanguage. ProductID follows the same convention as
// the rest of the seam: an empty ProductID is the generic (product-less) agent.
type PersonaKey struct {
	ProductID string
	Language  string
}

// multilingualPersonaProvider serves a per-(product, language) base prompt from a
// fixed map of literal strings. Selection reads the request language from ctx and
// degrades deterministically: exact (product, language) -> the language's primary
// subtag (so "es-AR" finds an "es" persona) -> the product's default-language
// persona -> the built-in default provider. Every value is a literal the consumer
// wrote (anti-magic gate, ADR-0002) — a per-language persona is just a different
// literal selected by the language key, never a template.
type multilingualPersonaProvider struct {
	prompts  map[PersonaKey]string
	fallback PersonaProvider
}

// NewMultilingualPersonaProvider builds a PersonaProvider over prompts keyed by
// (product_id, language) literal base prompts. The request language is taken from
// ctx (LanguageFromContext); when a specific language is not configured for a
// product the provider falls back to that product's default-language persona, and
// finally to the built-in default persona — so there is never a "no persona"
// state. The map is copied defensively, and every language key is normalized
// (trimmed + lowercased, empty -> defaultPersonaLanguage) so registration and
// lookup agree on a canonical form.
func NewMultilingualPersonaProvider(prompts map[PersonaKey]string) PersonaProvider {
	m := make(map[PersonaKey]string, len(prompts))
	for k, v := range prompts {
		lang := normalizePersonaLanguage(k.Language)
		if lang == "" {
			lang = defaultPersonaLanguage
		}
		m[PersonaKey{ProductID: k.ProductID, Language: lang}] = v
	}
	return multilingualPersonaProvider{prompts: m, fallback: defaultPersonaProvider{}}
}

// BasePrompt selects the persona for productID in the request language carried on
// ctx, degrading through the product's default-language persona and finally the
// built-in default persona. See multilingualPersonaProvider for the ordering.
func (m multilingualPersonaProvider) BasePrompt(ctx context.Context, productID string) (string, error) {
	code := defaultPersonaLanguage
	if detected, ok := lang.FromContext(ctx); ok {
		code = string(detected)
	}
	code = normalizePersonaLanguage(code)

	// 1. Exact (product, language).
	if p, ok := m.prompts[PersonaKey{ProductID: productID, Language: code}]; ok {
		return p, nil
	}
	// 2. Primary subtag of the language (e.g. "es-AR" -> "es").
	if primary, _, found := strings.Cut(code, "-"); found && primary != "" {
		if p, ok := m.prompts[PersonaKey{ProductID: productID, Language: primary}]; ok {
			return p, nil
		}
	}
	// 3. The product's default-language persona.
	if code != defaultPersonaLanguage {
		if p, ok := m.prompts[PersonaKey{ProductID: productID, Language: defaultPersonaLanguage}]; ok {
			return p, nil
		}
	}
	// 4. The built-in default persona (keeps the default binary byte-for-byte).
	return m.fallback.BasePrompt(ctx, productID)
}

// systemPromptFor builds the minimal system prompt for the given product_id. It
// is the literal the defaultPersonaProvider serves; kept identical to the
// pre-Phase-8 behavior so the default binary's persona is byte-for-byte
// unchanged.
func systemPromptFor(productID string) string {
	if productID == "" {
		return "You are the Aisotropy agent. Respond briefly and directly."
	}
	return fmt.Sprintf("You are the Aisotropy agent for product %q. Respond briefly and directly.", productID)
}
