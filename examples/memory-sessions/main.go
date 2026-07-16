// Command memory-sessions is a runnable lesson on Aisotropy's SESSIONS &
// WORKING MEMORY: what a session is, and what actually persists between two
// turns of the same conversation.
//
// It is a SEPARATE Go module (examples/memory-sessions/go.mod) with a replace
// directive pointing at the repo root, and it imports ONLY the public façade
// package github.com/Luum-Home/aisotropy/aisotropy — never any private package.
// Because it is a distinct module, Go's own internal-package rule PHYSICALLY
// forbids it from reaching into aisotropy/internal/..., so the fact that it
// compiles is the "public surface only" guarantee.
//
// # What this shows
//
// A session is identified by the (TenantID, UserID, SessionID) triple you pass
// to App.Turn. Reuse that triple across calls and the runtime keeps a working
// memory buffer for it: each turn's user message and the assistant's reply are
// appended, and on the NEXT turn they are hydrated back into the system prompt
// the model is shown ("Most recent messages in this session"). That is how the
// agent "remembers" what you said a moment ago without you resending it.
//
// # How we make persistence VISIBLE (honest note on the keyless stub)
//
// The public API returns you a reply, not the internal system prompt, so to
// TEACH persistence we install a tiny recorder LLM client via WithLLM. It is a
// legitimate consumer-supplied client (WithLLM is public, LLMClient is public):
// it captures the exact system prompt the runtime hydrates for each turn and
// returns a canned, schema-valid reply — the same role the built-in keyless
// stub plays, but one whose captured input we can print. This is not a real
// model: it does not reason, it just lets us hold up the two turns' system
// prompts side by side. Point Aisotropy at a real provider (WithLLM with a real
// client, or set ANTHROPIC_API_KEY/OPENAI_API_KEY/OPENROUTER_API_KEY and drop
// WithLLM) and the SAME memory hydration feeds a model that actually uses the
// remembered context.
//
// Run it keyless (no API key, no Docker):
//
//	go run .
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// recorderClient is a consumer-supplied LLMClient (the public seam WithLLM
// accepts) that records the system prompt of every turn and answers with a
// fixed, schema-valid JSON reply. Capturing the system prompt is the whole
// point: it is where the runtime hydrates working memory, so comparing the
// system prompt of turn 1 vs turn 2 shows exactly what persisted.
//
// It is NOT a model. The reply is a constant; only the captured input varies.
type recorderClient struct {
	mu      sync.Mutex
	systems []string // one hydrated system prompt per Turn, in call order
}

func (c *recorderClient) Name() string { return "recorder" }

// StreamChat records the system prompt, then streams back one canned reply that
// satisfies the chat route's structured-output schema ({"reply": string}), so
// the fail-closed reflection loop accepts it on the first attempt — exactly as
// the built-in stub does keyless.
func (c *recorderClient) StreamChat(ctx context.Context, system, prompt string) (<-chan aisotropy.Token, error) {
	c.mu.Lock()
	c.systems = append(c.systems, system)
	c.mu.Unlock()

	out := make(chan aisotropy.Token, 2)
	out <- aisotropy.Token{Text: `{"reply":"Got it — noted for this session."}`}
	out <- aisotropy.Token{Done: true, Usage: &aisotropy.Usage{InputTokens: 1, OutputTokens: 1}}
	close(out)
	return out, nil
}

// capturedSystems returns a copy of the recorded system prompts.
func (c *recorderClient) capturedSystems() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.systems...)
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("memory-sessions: %v", err)
	}
}

func run() error {
	ctx := context.Background()

	rec := &recorderClient{}
	app := aisotropy.New(
		// Install our recorder as the LLM client. This is the same public seam a
		// consumer uses to plug in a real provider client; here it lets us read
		// back the hydrated system prompt so persistence is visible.
		aisotropy.WithLLM(rec),
		// A per-product persona so the base system prompt is stable and easy to
		// tell apart from the hydrated memory section.
		aisotropy.WithPersona("notes-bot", aisotropy.Persona{
			BasePrompt: "You are NotesBot. Keep track of what the user tells you in this session.",
		}),
	)
	defer func() { _ = app.Close() }()

	// One session = one (tenant, user, session) triple. We keep it constant
	// across BOTH turns — that constancy is what makes them the same conversation.
	const (
		tenant  = "acme"
		user    = "alice"
		session = "session-morning-1"
	)

	// --- Turn 1: the user states a fact. ---
	turn1Prompt := "Remember that my favorite color is teal."
	out1, err := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  tenant,
		UserID:    user,
		SessionID: session,
		ProductID: "notes-bot",
		Prompt:    turn1Prompt,
	})
	if err != nil {
		return fmt.Errorf("turn 1: %w", err)
	}

	// --- Turn 2: SAME session, a follow-up that never repeats the fact. ---
	turn2Prompt := "What is my favorite color?"
	out2, err := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  tenant,
		UserID:    user,
		SessionID: session, // <- same session id: this is what threads the two turns
		ProductID: "notes-bot",
		Prompt:    turn2Prompt,
	})
	if err != nil {
		return fmt.Errorf("turn 2: %w", err)
	}

	systems := rec.capturedSystems()
	if len(systems) < 2 {
		return fmt.Errorf("expected 2 captured system prompts, got %d", len(systems))
	}
	sys1, sys2 := systems[0], systems[1]

	// out.Stub reports whether the runtime fell back to the BUILT-IN keyless
	// stub. Here it is false because we deliberately installed our own recorder
	// via WithLLM — the runtime uses the client you give it. (Drop WithLLM and
	// run with no API key and this would be true: the built-in stub.)
	fmt.Println("using built-in keyless stub:", out1.Stub, "(false: our WithLLM recorder client is in use)")
	fmt.Println()
	fmt.Println("================ TURN 1 ================")
	fmt.Printf("user prompt : %s\n", turn1Prompt)
	fmt.Printf("reply       : %s\n", out1.Reply)
	fmt.Println("--- system prompt the model was shown on turn 1 ---")
	fmt.Println(indent(sys1))
	fmt.Println()
	fmt.Println("================ TURN 2 ================")
	fmt.Printf("user prompt : %s\n", turn2Prompt)
	fmt.Printf("reply       : %s\n", out2.Reply)
	fmt.Println("--- system prompt the model was shown on turn 2 ---")
	fmt.Println(indent(sys2))
	fmt.Println()

	// The proof: turn 1's user message was NOT in turn 1's own system prompt
	// (nothing had been persisted yet), but it IS in turn 2's system prompt —
	// hydrated back from working memory under "Most recent messages in this
	// session". Same session id in, remembered context out.
	firstInOwnTurn := strings.Contains(sys1, turn1Prompt)
	firstRecalledOnTurn2 := strings.Contains(sys2, turn1Prompt)

	fmt.Println("================ WHAT PERSISTED ================")
	fmt.Printf("turn-1 message present in turn-1 system prompt : %v (nothing persisted yet)\n", firstInOwnTurn)
	fmt.Printf("turn-1 message present in turn-2 system prompt : %v (hydrated from working memory)\n", firstRecalledOnTurn2)

	if firstInOwnTurn || !firstRecalledOnTurn2 {
		return fmt.Errorf(
			"persistence check failed: expected turn-1 message absent on turn 1 and present on turn 2 (got %v / %v)",
			firstInOwnTurn, firstRecalledOnTurn2)
	}

	fmt.Println()
	fmt.Println("PASS: the session remembered turn 1 when serving turn 2.")
	fmt.Println("Change SessionID on turn 2 and re-run: the recall disappears — a different")
	fmt.Println("session id is a different conversation, with its own empty working memory.")
	return nil
}

// indent prefixes every line with two spaces so the embedded system prompt is
// easy to tell apart from the surrounding report.
func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = "  " + ln
	}
	return strings.Join(lines, "\n")
}
