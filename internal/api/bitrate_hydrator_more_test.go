package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// errPersona is a PersonaProvider that always fails, so the ContextWithout error
// branch (persona base-prompt failure) can be exercised.
type errPersona struct{}

func (errPersona) BasePrompt(context.Context, string) (string, error) {
	return "", errors.New("persona boom")
}

// TestBitrateHydrator_ContextWith covers the "with memory" builder: a valid
// (tenant,user) yields the hydrated prompt via the identical hydrateSystemPrompt
// the streaming handler uses (nil wm, so no live buffer), and an invalid scope
// (empty tenant) returns a wrapped error rather than a silent empty context.
func TestBitrateHydrator_ContextWith(t *testing.T) {
	h := BitrateHydrator{} // all-nil collaborators: persona defaults, no stores

	t.Run("valid scope hydrates", func(t *testing.T) {
		got, err := h.ContextWith(context.Background(), "acme", "alice", "s1", "p1", "what did we decide")
		if err != nil {
			t.Fatalf("ContextWith: %v", err)
		}
		if strings.TrimSpace(got) == "" {
			t.Error("expected a non-empty persona base prompt, got empty")
		}
	})

	t.Run("invalid scope errors", func(t *testing.T) {
		if _, err := h.ContextWith(context.Background(), "", "alice", "s1", "p1", "q"); err == nil {
			t.Fatal("ContextWith with an empty tenant must return a scope error")
		}
	})
}

// TestBitrateHydrator_ContextWithout covers the "no memory" baseline: the nil
// persona falls back to the default provider (non-empty base), and a failing
// persona surfaces a wrapped error.
func TestBitrateHydrator_ContextWithout(t *testing.T) {
	t.Run("nil persona uses default", func(t *testing.T) {
		h := BitrateHydrator{}
		base, err := h.ContextWithout(context.Background(), "p1")
		if err != nil {
			t.Fatalf("ContextWithout: %v", err)
		}
		if strings.TrimSpace(base) == "" {
			t.Error("default persona base prompt must be non-empty")
		}
	})

	t.Run("failing persona errors", func(t *testing.T) {
		h := BitrateHydrator{Persona: errPersona{}}
		if _, err := h.ContextWithout(context.Background(), "p1"); err == nil {
			t.Fatal("ContextWithout must surface a persona failure")
		}
	})
}
