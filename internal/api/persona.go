package api

import (
	"context"
	"fmt"
)

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
