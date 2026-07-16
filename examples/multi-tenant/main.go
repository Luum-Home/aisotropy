// Command multi-tenant is a runnable demonstration of Aisotropy's CORE
// differentiator: tenant isolation by construction. One tenant writes a secret
// into its memory; a different tenant CANNOT see it — not because a filter
// happened to catch it, but because every memory tier is scoped from the
// (TenantID, UserID) identity that enters ctx, and there is no code path that
// widens that scope.
//
// It is a SEPARATE Go module (examples/multi-tenant/go.mod) with a replace
// directive pointing at the repo root, and it imports ONLY the public façade
// package github.com/Luum-Home/aisotropy/aisotropy — never any private package.
// Because it is a distinct module, Go's own internal-package rule PHYSICALLY
// forbids it from reaching into aisotropy/internal/..., so this is the isolation
// property proven at the exact surface a third party consumes.
//
// This is the public-API sibling of the internal black-box isolation test
// (aisotropy/isolation_e2e_test.go), which drives the same guarantee over real
// HTTP. Here we drive it in-process via App.Turn.
//
// # How the demo makes the leak (or its absence) visible
//
// The public API hands you a reply, not the internal system prompt — and the
// system prompt is exactly where each tenant's remembered context is hydrated.
// So we install a tiny recorder LLM client via the public WithLLM seam that
// captures the hydrated system prompt of every turn. A marker that a tenant put
// in its OWN memory must appear when THAT tenant asks again (positive control,
// so the negative is not vacuous) and must be ABSENT from the OTHER tenant's
// system prompt (the isolation proof).
//
// The recorder is not a real model; it returns a canned schema-valid reply. The
// isolation being demonstrated is a property of the runtime's memory scoping,
// not of the model — a real provider changes nothing about it.
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

// secretMarker is the distinctive token tenant A writes into its own memory.
// It is purely alphanumeric so it survives verbatim through persistence and
// hydration; two tenants never share it, so finding it in the wrong tenant's
// system prompt would be unambiguous proof of a cross-tenant leak.
const secretMarker = "vaultcode-orangutan7431"

// recorderClient is a consumer-supplied LLMClient (the public seam WithLLM
// accepts) that records the hydrated system prompt of every turn, in call
// order, and answers with a fixed schema-valid JSON reply so the fail-closed
// reflection loop accepts it on the first attempt — the same role the built-in
// keyless stub plays, but one whose captured input we can inspect.
type recorderClient struct {
	mu      sync.Mutex
	systems []string
}

func (c *recorderClient) Name() string { return "recorder" }

func (c *recorderClient) StreamChat(ctx context.Context, system, prompt string) (<-chan aisotropy.Token, error) {
	c.mu.Lock()
	c.systems = append(c.systems, system)
	c.mu.Unlock()

	out := make(chan aisotropy.Token, 2)
	out <- aisotropy.Token{Text: `{"reply":"acknowledged"}`}
	out <- aisotropy.Token{Done: true, Usage: &aisotropy.Usage{InputTokens: 1, OutputTokens: 1}}
	close(out)
	return out, nil
}

func (c *recorderClient) capturedSystems() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.systems...)
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("multi-tenant: %v", err)
	}
}

func run() error {
	ctx := context.Background()

	rec := &recorderClient{}
	app := aisotropy.New(
		aisotropy.WithLLM(rec),
		aisotropy.WithPersona("vault-bot", aisotropy.Persona{
			BasePrompt: "You are VaultBot. Remember what each user tells you.",
		}),
	)
	defer func() { _ = app.Close() }()

	// Two DISTINCT identities. Isolation is keyed on (TenantID, UserID): acme's
	// alice and globex's bob share nothing.
	const (
		tenantA, userA = "acme", "alice"
		tenantB, userB = "globex", "bob"
	)

	// --- Turn A1: tenant A writes a secret into its own memory. ---
	if _, err := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  tenantA,
		UserID:    userA,
		SessionID: "sess-a",
		ProductID: "vault-bot",
		Prompt:    "Store this for me: " + secretMarker,
	}); err != nil {
		return fmt.Errorf("turn A1 (write secret): %w", err)
	}

	// --- Turn A2 (positive control): tenant A asks again, same session. Its own
	// secret MUST hydrate back — otherwise the negative test below would be
	// vacuous (a scope that retrieves nothing "isolates" trivially). ---
	if _, err := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  tenantA,
		UserID:    userA,
		SessionID: "sess-a",
		ProductID: "vault-bot",
		Prompt:    "What did I ask you to store?",
	}); err != nil {
		return fmt.Errorf("turn A2 (tenant A recall): %w", err)
	}

	// --- Turn B1 (isolation proof): a DIFFERENT tenant asks for the stored code,
	// using the retrieval words but NOT the marker itself. If B's system prompt
	// ever contained tenant A's marker, that would be a leak. ---
	if _, err := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  tenantB,
		UserID:    userB,
		SessionID: "sess-b",
		ProductID: "vault-bot",
		Prompt:    "What is the code that was stored for me?",
	}); err != nil {
		return fmt.Errorf("turn B1 (tenant B probe): %w", err)
	}

	systems := rec.capturedSystems()
	if len(systems) < 3 {
		return fmt.Errorf("expected 3 captured system prompts, got %d", len(systems))
	}
	tenantARecallSystem := systems[1] // turn A2
	tenantBProbeSystem := systems[2]  // turn B1

	tenantACanSeeOwnSecret := strings.Contains(tenantARecallSystem, secretMarker)
	tenantBCanSeeOthersSecret := strings.Contains(tenantBProbeSystem, secretMarker)

	fmt.Println("secret written by tenant A (acme/alice):", secretMarker)
	fmt.Println()
	fmt.Println("--- tenant A (acme/alice) recall — its own system prompt ---")
	fmt.Println(indent(tenantARecallSystem))
	fmt.Println()
	fmt.Println("--- tenant B (globex/bob) probe — its own system prompt ---")
	fmt.Println(indent(tenantBProbeSystem))
	fmt.Println()

	fmt.Println("================ ISOLATION RESULT ================")
	fmt.Printf("tenant A sees its OWN secret        : %v (positive control — retrieval works)\n", tenantACanSeeOwnSecret)
	fmt.Printf("tenant B sees tenant A's secret     : %v (must be false — isolation by construction)\n", tenantBCanSeeOthersSecret)

	if !tenantACanSeeOwnSecret {
		return fmt.Errorf("positive control failed: tenant A could not recall its own secret, so the isolation check would be vacuous")
	}
	if tenantBCanSeeOthersSecret {
		return fmt.Errorf("ISOLATION LEAK: tenant B's system prompt contained tenant A's secret %q", secretMarker)
	}

	fmt.Println()
	fmt.Println("PASS: the same secret that tenant A recalls is invisible to tenant B.")
	fmt.Println("Isolation is not a post-filter here — every memory tier is scoped from the")
	fmt.Println("(tenant, user) identity carried in ctx, so there is no path for one tenant's")
	fmt.Println("hydration to reach another's data.")
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
