package api

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/lang"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// undeterminedPrompt is a short, code-mixed input the detector cannot classify;
// lang.Detect returns Undetermined for it (verified in internal/lang). It stands
// in for the "very short / code-mixed follow-up" the persistence feature exists
// to rescue.
const undeterminedPrompt = "123 456"

// runTurn drives one non-streaming turn through ChatCore for the given subject
// and prompt, sharing a single WorkingMemory across calls so persistence is
// observable across requests. It returns the language the persona consumer
// actually saw on ctx (the effective, post-persistence language).
func runTurn(t *testing.T, wm *memory.WorkingMemory, tenant, user, prompt string) lang.Language {
	t.Helper()
	spy := &langSpyPersona{}
	deps := ChatDeps{
		Client:  llm.NewStubClient(),
		WM:      wm,
		Persona: spy,
		Guard:   &Guard{G: guardrails.Default()},
	}
	in := &ChatInput{TenantID: tenant, UserID: user, Body: ChatRequest{Prompt: prompt, SessionID: "s1"}}
	if _, err := ChatCore(context.Background(), deps, in); err != nil {
		t.Fatalf("ChatCore(%s/%s): %v", tenant, user, err)
	}
	if !spy.called {
		t.Fatalf("persona never consulted for %s/%s; cannot observe ctx language", tenant, user)
	}
	return spy.captured
}

// TestChatCore_LanguagePersistsAcrossRequests is scenario (a): a user's detected
// language is written to per-subject working memory on one request and is visible
// as the stored value on the next — and a later confident detection in a new
// language updates it. Persistence, then update, both across separate requests.
func TestChatCore_LanguagePersistsAcrossRequests(t *testing.T) {
	wm := newTestWorkingMemory(t)
	ctx := context.Background()

	// Request 1: a Spanish prompt. Detection is confident -> it is remembered.
	if got := runTurn(t, wm, "acme", "alice", "Por favor resume el informe y destaca los riesgos."); got != lang.Spanish {
		t.Fatalf("request 1 effective language = %q, want %q", got, lang.Spanish)
	}
	if code, found, err := wm.GetSubjectLanguage(ctx, "acme", "alice"); err != nil || !found || code != "es" {
		t.Fatalf("after request 1, stored language = (%q,%v,%v), want (\"es\",true,nil)", code, found, err)
	}

	// Request 2: a confident French prompt on the same subject updates memory.
	if got := runTurn(t, wm, "acme", "alice", "Peux-tu résumer le rapport et souligner les risques principaux, s'il te plaît."); got != lang.French {
		t.Fatalf("request 2 effective language = %q, want %q", got, lang.French)
	}
	if code, found, err := wm.GetSubjectLanguage(ctx, "acme", "alice"); err != nil || !found || code != "fr" {
		t.Fatalf("after request 2, stored language = (%q,%v,%v), want (\"fr\",true,nil)", code, found, err)
	}
}

// TestChatCore_UndeterminedInheritsStoredLanguage is scenario (b): once a
// subject's language is known, a follow-up whose own detection is Undetermined
// inherits the stored language rather than resetting to the default — and the
// low-signal turn does not overwrite what was learned.
func TestChatCore_UndeterminedInheritsStoredLanguage(t *testing.T) {
	wm := newTestWorkingMemory(t)
	ctx := context.Background()

	// Teach the subject Spanish with a confident first turn.
	if got := runTurn(t, wm, "acme", "alice", "Hola, necesito ayuda con el informe trimestral por favor."); got != lang.Spanish {
		t.Fatalf("seed turn effective language = %q, want %q", got, lang.Spanish)
	}

	// A short/code-mixed follow-up detects as Undetermined but inherits Spanish.
	if got := runTurn(t, wm, "acme", "alice", undeterminedPrompt); got != lang.Spanish {
		t.Fatalf("undetermined follow-up effective language = %q, want inherited %q", got, lang.Spanish)
	}

	// The low-signal turn must not have overwritten the remembered language.
	if code, found, err := wm.GetSubjectLanguage(ctx, "acme", "alice"); err != nil || !found || code != "es" {
		t.Fatalf("stored language after low-signal turn = (%q,%v,%v), want unchanged (\"es\",true,nil)", code, found, err)
	}
}

// TestChatCore_StoredLanguageIsolatedPerSubject is scenario (c): user A's
// remembered language never leaks into user B (same tenant) or into the same
// user id under a different tenant. A different subject with an Undetermined
// prompt gets no language, proving there is no cross-subject fallback.
func TestChatCore_StoredLanguageIsolatedPerSubject(t *testing.T) {
	wm := newTestWorkingMemory(t)

	// tenant "acme", user "alice" is taught Spanish.
	if got := runTurn(t, wm, "acme", "alice", "Hola, necesito ayuda con el informe trimestral por favor."); got != lang.Spanish {
		t.Fatalf("acme/alice seed language = %q, want %q", got, lang.Spanish)
	}

	// A different USER in the same tenant, with an Undetermined prompt, inherits
	// NOTHING — the effective language is not-set (zero value), not Spanish.
	if got := runTurn(t, wm, "acme", "bob", undeterminedPrompt); got == lang.Spanish {
		t.Fatalf("acme/bob inherited acme/alice's Spanish across users — isolation leak")
	} else if got != lang.Undetermined && got != "" {
		t.Fatalf("acme/bob effective language = %q, want not-set", got)
	}

	// The SAME user id under a different TENANT likewise inherits nothing.
	if got := runTurn(t, wm, "globex", "alice", undeterminedPrompt); got == lang.Spanish {
		t.Fatalf("globex/alice inherited acme/alice's Spanish across tenants — isolation leak")
	} else if got != lang.Undetermined && got != "" {
		t.Fatalf("globex/alice effective language = %q, want not-set", got)
	}
}
