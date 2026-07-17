package api

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/lang"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// langSpyPersona is a PersonaProvider that captures the language carried on the
// ctx it is called with. Persona selection is the first real consumer of the
// language-detection seam (research R1/R5), so reading lang.FromContext here
// proves the detected language actually reaches a downstream consumer — not just
// that it was written to some ctx the rest of the pipeline never sees.
type langSpyPersona struct {
	captured lang.Language
	called   bool
}

func (p *langSpyPersona) BasePrompt(ctx context.Context, _ string) (string, error) {
	p.captured, _ = lang.FromContext(ctx)
	p.called = true
	return "You are a test agent. Respond briefly.", nil
}

// TestChatCore_CarriesDetectedLanguageOnContext is the seam's integration
// assertion: a Spanish chat prompt is detected once at the ChatCore boundary,
// placed on ctx via the canonical lang.WithLanguage, and read back by the
// persona consumer via lang.FromContext — the exact write-once/read-everywhere
// contract the guardrails (R4) and personas (R5) depend on.
func TestChatCore_CarriesDetectedLanguageOnContext(t *testing.T) {
	wm := newTestWorkingMemory(t)
	spy := &langSpyPersona{}
	deps := ChatDeps{
		Client:  llm.NewStubClient(),
		WM:      wm,
		Persona: spy,
		Guard:   &Guard{G: guardrails.Default()},
	}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{
		Prompt:    "Por favor resume el informe trimestral y destaca los riesgos principales.",
		SessionID: "s1",
	}}

	if _, err := ChatCore(context.Background(), deps, in); err != nil {
		t.Fatalf("ChatCore: %v", err)
	}
	if !spy.called {
		t.Fatal("persona provider was never consulted; cannot prove ctx carried the language")
	}
	if spy.captured != lang.Spanish {
		t.Fatalf("ctx carried language %q, want %q (Spanish prompt)", spy.captured, lang.Spanish)
	}
}
