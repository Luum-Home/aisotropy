package llm

import (
	"fmt"
	"os"
	"sort"
	"sync"
)

// Registry holds multiple named provider connections simultaneously. This is
// the foundation for the runtime being provider-agnostic: instead of a single
// global Client picked once by FromEnv, a Registry can keep several
// connections alive at the same time (e.g. "openrouter" and "anthropic" both
// configured), which is what compressibility-based routing needs (see
// docs/research/bet-04-compressibility-routing.md — routing a request to the
// cheapest/most-compressible-capable provider requires more than one
// candidate to route between).
//
// Adding a new provider only requires two things:
//  1. Implement Client (StreamChat + Name) — the OpenAICompatClient already
//     covers most providers that speak the OpenAI /chat/completions protocol
//     (OpenRouter, OpenAI itself, Groq, Together, local gateways, and —via
//     model ids like "google/gemini-..."— Google/Gemini through OpenRouter).
//  2. Call Register(name, client) on a Registry (or add detection logic to
//     RegistryFromEnv if the provider should be auto-discovered from env
//     vars).
// No other package needs to change: Client is the stable contract every
// caller (internal/api, internal/memory) already depends on.
type Registry struct {
	mu          sync.RWMutex
	clients     map[string]Client
	defaultName string
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{clients: make(map[string]Client)}
}

// Register adds (or replaces) a named provider connection. The first
// provider registered becomes the default unless SetDefault is called
// explicitly afterwards.
func (r *Registry) Register(name string, c Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[name] = c
	if r.defaultName == "" {
		r.defaultName = name
	}
}

// SetDefault selects which registered provider Default() returns. Returns an
// error if name was not registered.
func (r *Registry) SetDefault(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clients[name]; !ok {
		return fmt.Errorf("llm: registry has no provider named %q", name)
	}
	r.defaultName = name
	return nil
}

// Get returns the named provider connection, if registered.
func (r *Registry) Get(name string) (Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[name]
	return c, ok
}

// Default returns the default provider connection. Panics if the registry is
// empty — a Registry built via RegistryFromEnv always has at least "stub"
// registered, so this only happens if a caller constructs an empty Registry
// with NewRegistry and never calls Register.
func (r *Registry) Default() Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[r.defaultName]
	if !ok {
		panic("llm: registry has no default provider registered")
	}
	return c
}

// Names returns the registered provider names, sorted alphabetically.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.clients))
	for name := range r.clients {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultName returns the name of the current default provider.
func (r *Registry) DefaultName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultName
}

// RegistryFromEnv builds a Registry with EVERY provider that has credentials
// configured in the environment — not just one. This is what makes the
// runtime multi-connection: an operator can have OPENROUTER_API_KEY and
// ANTHROPIC_API_KEY set at the same time and both connections are available
// through the Registry (e.g. Get("anthropic") and Get("openrouter")), ready
// for a future router (bet-04) to pick between them per request.
//
// Detected providers, by env var:
//   - "openrouter": OPENROUTER_API_KEY set. Model: OPENROUTER_MODEL /
//     AISOTROPY_MODEL / DefaultOpenRouterModel.
//   - "openai": OPENAI_API_KEY set. Base URL: OPENAI_BASE_URL (optional).
//     Model: OPENAI_MODEL / AISOTROPY_MODEL / defaultOpenAICompatModel.
//   - "anthropic": ANTHROPIC_API_KEY set. Model: ANTHROPIC_MODEL /
//     DefaultAnthropicModel.
//   - "stub": ALWAYS registered — a keyless fallback so the SSE path can
//     always be exercised even with zero credentials configured.
//
// The default provider is chosen by precedence
// openrouter > openai > anthropic > stub, matching the previous single-client
// FromEnv behavior.
func RegistryFromEnv() *Registry {
	reg := NewRegistry()

	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		reg.Register("openrouter", NewOpenRouterClient(key, firstNonEmpty(
			os.Getenv("OPENROUTER_MODEL"), os.Getenv("AISOTROPY_MODEL"))))
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		reg.Register("openai", NewOpenAICompatClient(key,
			firstNonEmpty(os.Getenv("OPENAI_MODEL"), os.Getenv("AISOTROPY_MODEL")),
			os.Getenv("OPENAI_BASE_URL"), "openai"))
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		reg.Register("anthropic", NewAnthropicClient(key,
			firstNonEmpty(os.Getenv("ANTHROPIC_MODEL"), DefaultAnthropicModel)))
	}
	// Always available: a keyless fallback so the SSE path is exercisable
	// with zero credentials configured (dev/CI).
	reg.Register("stub", NewStubClient())

	for _, name := range []string{"openrouter", "openai", "anthropic", "stub"} {
		if _, ok := reg.Get(name); ok {
			_ = reg.SetDefault(name)
			break
		}
	}

	return reg
}

// FromEnv builds the single default Client to use, based on the environment.
// Kept for backward compatibility: internal/api and internal/memory consume
// a single Client today. It is now implemented in terms of RegistryFromEnv so
// the precedence/detection logic lives in exactly one place.
//
// Precedence: OPENROUTER_API_KEY > OPENAI_API_KEY > ANTHROPIC_API_KEY > stub.
func FromEnv() Client {
	return RegistryFromEnv().Default()
}
