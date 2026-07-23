package api

import (
	"context"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// ADR-0068 (A2) — configurable persona hardening.
//
// These tests pin two properties that must not regress:
//  1. DEFAULT OFF is byte-identical: with AISOTROPY_PERSONA_HARDENING unset the
//     served persona equals the base persona exactly (no drift for existing
//     deployments).
//  2. When ON, the hardening text lands INSIDE the trusted system region —
//     directly below the base persona and ABOVE the untrusted-context fence
//     (hydrateSystemPrompt appends the fence only AFTER PersonaProvider.BasePrompt
//     returns), and NEVER inside/after that fence.

// oneFactStore returns exactly one fact from Search, so hydrateSystemPrompt emits
// the untrusted-context fence — the with-fence case the trusted-region assertion
// needs. Every other method is an inert stub (this store is read-only here).
type oneFactStore struct{ content string }

func (oneFactStore) Save(context.Context, facts.Scope, facts.Fact) error { return nil }
func (s oneFactStore) Search(context.Context, facts.Scope, string, int) ([]facts.ScoredFact, error) {
	return []facts.ScoredFact{{Fact: facts.Fact{Content: s.content}, Score: 1}}, nil
}
func (oneFactStore) Supersede(context.Context, facts.Scope, string, facts.Fact) error { return nil }
func (oneFactStore) ExportAll(context.Context) ([]facts.Fact, error)                  { return nil, nil }
func (oneFactStore) Delete(context.Context, facts.Scope, []string) error              { return nil }
func (oneFactStore) Close() error                                                     { return nil }

// TestPersonaHardening_DefaultOffByteIdentical proves the default-OFF guarantee
// across both public provider constructors and the empty-product path: with the
// env gate unset, BasePrompt returns the base persona byte-for-byte.
func TestPersonaHardening_DefaultOffByteIdentical(t *testing.T) {
	t.Setenv(personaHardeningEnvVar, "") // explicitly OFF

	ctx := context.Background()
	mp := NewMapPersonaProvider(map[string]string{"prod": "You are the prod agent."})

	got, err := mp.BasePrompt(ctx, "prod")
	if err != nil {
		t.Fatalf("BasePrompt: %v", err)
	}
	if want := "You are the prod agent."; got != want {
		t.Fatalf("hardening OFF must be byte-identical; got %q want %q", got, want)
	}
	if strings.Contains(got, "Safeguards") {
		t.Fatalf("hardening block must be absent when OFF; got %q", got)
	}

	// Unknown product falls back to the built-in default persona, still bare.
	fallback, _ := mp.BasePrompt(ctx, "unknown")
	if strings.Contains(fallback, "Safeguards") {
		t.Fatalf("fallback persona must be bare when OFF; got %q", fallback)
	}
}

// TestPersonaHardening_OnAppendsBelowPersona proves that when ON the hardening
// block is appended AFTER the base persona (trusted region), the base persona
// text is preserved verbatim, and no untrusted-fence marker leaks into the
// provider output.
func TestPersonaHardening_OnAppendsBelowPersona(t *testing.T) {
	t.Setenv(personaHardeningEnvVar, "true")

	ctx := context.Background()
	const base = "You are the prod agent."
	mp := NewMapPersonaProvider(map[string]string{"prod": base})

	got, err := mp.BasePrompt(ctx, "prod")
	if err != nil {
		t.Fatalf("BasePrompt: %v", err)
	}
	if !strings.HasPrefix(got, base) {
		t.Fatalf("base persona must remain the prefix; got %q", got)
	}
	if !strings.Contains(got, personaHardeningBlock) {
		t.Fatalf("hardening block must be present when ON; got %q", got)
	}
	// Ordering: persona strictly before hardening (trusted region, below persona).
	if strings.Index(got, base) >= strings.Index(got, personaHardeningBlock) {
		t.Fatalf("hardening must come AFTER the base persona; got %q", got)
	}
	// The provider itself never emits fence markers — the fence is
	// hydrateSystemPrompt's job, strictly after this returns.
	if strings.Contains(got, untrustedContextOpen) || strings.Contains(got, untrustedContextClose) {
		t.Fatalf("persona provider output must not contain fence markers; got %q", got)
	}
}

// TestPersonaHardening_EmptyBaseYieldsHardeningAlone covers the empty-base branch
// (a product-less agent whose literal is ""): the hardening block is served alone.
func TestPersonaHardening_EmptyBaseYieldsHardeningAlone(t *testing.T) {
	t.Setenv(personaHardeningEnvVar, "1")
	p := hardenedPersonaProvider{inner: literalPersona("")}
	got, err := p.BasePrompt(context.Background(), "")
	if err != nil {
		t.Fatalf("BasePrompt: %v", err)
	}
	if got != personaHardeningBlock {
		t.Fatalf("empty base must yield the hardening block alone; got %q", got)
	}
}

type literalPersona string

func (l literalPersona) BasePrompt(context.Context, string) (string, error) { return string(l), nil }

// TestPersonaHardening_AssembledTrustedRegion is the table-driven, assembled-prompt
// guard: it runs the REAL hydrateSystemPrompt across {hardening off/on} ×
// {no-hydration → no fence / one-fact → fence present} and asserts the hardening
// text always sits in the TRUSTED region — above the untrusted-context fence when
// one exists, and absent entirely when OFF. It never issues a model call.
func TestPersonaHardening_AssembledTrustedRegion(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	const base = "You are the Nimbus Rail assistant."

	tests := []struct {
		name       string
		hardening  bool
		withFence  bool // inject a fact so the fence appears
		wantHarden bool
		wantFence  bool
	}{
		{name: "off_no_fence", hardening: false, withFence: false, wantHarden: false, wantFence: false},
		{name: "off_with_fence", hardening: false, withFence: true, wantHarden: false, wantFence: true},
		{name: "on_no_fence", hardening: true, withFence: false, wantHarden: true, wantFence: false},
		{name: "on_with_fence", hardening: true, withFence: true, wantHarden: true, wantFence: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.hardening {
				t.Setenv(personaHardeningEnvVar, "true")
			} else {
				t.Setenv(personaHardeningEnvVar, "")
			}
			persona := NewMapPersonaProvider(map[string]string{"p1": base})

			var store facts.Store
			if tt.withFence {
				store = oneFactStore{content: "the user prefers window seats"}
			}

			sys, err := hydrateSystemPrompt(context.Background(), nil, store, nil, nil, nil, persona,
				key, scope, "p1", "which seat am I in?")
			if err != nil {
				t.Fatalf("hydrateSystemPrompt: %v", err)
			}

			if !strings.HasPrefix(sys, base) {
				t.Fatalf("assembled system must start with the base persona; got %q", sys)
			}

			gotFence := strings.Contains(sys, untrustedContextOpen)
			if gotFence != tt.wantFence {
				t.Fatalf("fence present = %v, want %v; system=%q", gotFence, tt.wantFence, sys)
			}

			gotHarden := strings.Contains(sys, personaHardeningBlock)
			if gotHarden != tt.wantHarden {
				t.Fatalf("hardening present = %v, want %v; system=%q", gotHarden, tt.wantHarden, sys)
			}

			// The invariant: whenever BOTH hardening and the fence are present, the
			// hardening text must precede the fence open marker — trusted region,
			// never below/inside the untrusted block.
			if gotHarden && gotFence {
				if strings.Index(sys, personaHardeningBlock) >= strings.Index(sys, untrustedContextOpen) {
					t.Fatalf("hardening must sit ABOVE the untrusted fence; system=%q", sys)
				}
			}
		})
	}
}
