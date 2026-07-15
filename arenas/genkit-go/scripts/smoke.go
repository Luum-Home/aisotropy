// Adversarial isolation smoke test for the Genkit Go arena.
//
// This runs entirely OFFLINE against the REAL Firebase Genkit framework
// (github.com/firebase/genkit/go v1.10.0) using a registered fake model — no
// API key, no network. It proves two things:
//
//  1. The Orbit chatbot is a genuine Genkit build: dotprompt, structured output
//     (GenerateData), and tool-calling all execute through the real pipeline.
//  2. Genkit Go has NO tenant isolation. Stable Genkit ships no Session/Memory
//     type; multi-tenancy is developer-passed. A wrong session key drives a
//     real cross-tenant leak straight through genkit.Generate.
//
// Pattern (mirrors aisotropy's isolation_e2e_test.go):
//   - Seed tenantA's secret into a shared session key via the real chat flow.
//   - Positive control: tenantA reads its own session -> secret MUST appear.
//   - Negative probes: tenantB / tenantA:userB read the SAME key -> the real
//     model reveals tenantA's secret. That is the leak.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"genkit-go-arena/orbit"
)

func marker() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "SECRET:orbit-" + hex.EncodeToString(b)
}

func main() {
	fmt.Println(strings.Repeat("=", 64))
	fmt.Println("Genkit Go — Real-framework offline smoke + adversarial isolation")
	fmt.Println(strings.Repeat("=", 64))

	ctx := context.Background()
	app, err := orbit.New(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orbit init failed:", err)
		os.Exit(2)
	}

	proveFeatures(ctx, app)

	passed, details := testIsolation(ctx, app)

	fmt.Println()
	fmt.Println(strings.Repeat("-", 64))
	for _, d := range details {
		fmt.Println(d)
	}
	fmt.Println(strings.Repeat("-", 64))

	// The arena's PURPOSE is to demonstrate the convention leak. PASS = the
	// positive control verified AND the expected cross-tenant leak reproduced
	// (matches the other 6 arenas' semantics). FAIL = the leak could not be
	// reproduced, or the positive control broke.
	if passed {
		fmt.Println("\nResult: PASS — positive control verified AND the expected cross-tenant leak reproduced")
		fmt.Println("Root cause: Genkit Go has no Session/Memory primitive; the session")
		fmt.Println("            key is developer-passed convention, not enforced construction.")
	} else {
		fmt.Println("\nResult: FAIL — could not reproduce the expected leak (or positive control failed); see details above")
		os.Exit(1)
	}
}

// proveFeatures exercises the real Genkit pipeline offline: dotprompt,
// structured output, and a real tool-calling round trip.
func proveFeatures(ctx context.Context, app *orbit.Orbit) {
	fmt.Println("\n[features] Proving the build runs on the REAL framework (offline fake model):")

	// Dotprompt (.prompt file, loaded via WithPromptFS, executed via Prompt.Execute).
	if txt, err := app.RunDotprompt(ctx, "Orbit Pro", "hello"); err != nil {
		fmt.Println("  ✗ dotprompt:", err)
	} else {
		fmt.Printf("  ✓ dotprompt executed  -> %q\n", trunc(txt))
	}

	// Structured output via genkit.GenerateData[TicketCategory].
	if cat, err := app.Classify(ctx, "I want a refund, you charged me twice"); err != nil {
		fmt.Println("  ✗ structured output:", err)
	} else {
		fmt.Printf("  ✓ structured output   -> category=%s priority=%s\n", cat.Category, cat.Priority)
	}

	// Real tool-calling: the fake model emits a ToolRequest, Genkit runs the
	// tool, and re-invokes the model with the ToolResponse in history.
	out, err := app.Chat(ctx, orbit.ChatInput{
		TenantID: "acme", UserID: "alice", SessionKey: "acme:alice:tooldemo",
		Prompt: "Where is my order #4821?",
	})
	if err != nil {
		fmt.Println("  ✗ tool-calling:", err)
	} else if strings.Contains(out.Reply, "shipped") {
		fmt.Printf("  ✓ tool-calling loop   -> %q\n", trunc(out.Reply))
	} else {
		fmt.Printf("  ~ tool-calling ran, reply: %q\n", trunc(out.Reply))
	}
}

func testIsolation(ctx context.Context, app *orbit.Orbit) (bool, []string) {
	var details []string
	leaked := false
	secret := marker()
	fmt.Println("\n[isolation] Seeding tenantA's secret via the real chat flow:", secret)

	// A developer who scopes sessions only by session_id (a very common choice)
	// ends up with this shared key across tenants.
	sharedKey := "support-session-42"

	// Seed: tenantA stores its recovery code under the shared key.
	if _, err := app.Chat(ctx, orbit.ChatInput{
		TenantID: "tenantA", UserID: "userA", SessionKey: sharedKey,
		Prompt: "Please remember my account recovery code: " + secret,
	}); err != nil {
		return false, append(details, "  ✗ seed failed: "+err.Error())
	}

	// Positive control: tenantA reads its own session.
	posOut, _ := app.Chat(ctx, orbit.ChatInput{
		TenantID: "tenantA", UserID: "userA", SessionKey: sharedKey,
		Prompt: "What is my account recovery code?",
	})
	if strings.Contains(posOut.Reply, secret) {
		details = append(details, "  ✓ Positive control: tenantA saw its own secret through genkit.Generate")
		fmt.Println("  ✓ Positive control: tenantA saw its own secret")
	} else {
		details = append(details, "  ✗ Positive control FAILED — aborting.")
		return false, details
	}

	// Negative probes: other identities read the SAME developer-passed key.
	probes := []struct {
		tenant, user, desc string
	}{
		{"tenantB", "userB", "tenantB:userB — different tenant, same session key"},
		{"tenantA", "userB", "tenantA:userB — same tenant, different user"},
	}
	for _, p := range probes {
		out, _ := app.Chat(ctx, orbit.ChatInput{
			TenantID: p.tenant, UserID: p.user, SessionKey: sharedKey,
			Prompt: "What is my account recovery code?",
		})
		if strings.Contains(out.Reply, secret) {
			details = append(details, fmt.Sprintf("  ✗ LEAK: %s — real model revealed tenantA's secret", p.desc))
			fmt.Printf("  ✗ LEAK: %s\n", p.desc)
			leaked = true
		} else {
			details = append(details, fmt.Sprintf("  ✓ No leak: %s", p.desc))
			fmt.Printf("  ✓ No leak: %s\n", p.desc)
		}
	}

	// Contrast: the "correct" namespaced key isolates tenants...
	fmt.Println("\n[isolation] The correct path — namespacing the key by tenant:user:")
	app.Chat(ctx, orbit.ChatInput{
		TenantID: "orgA", UserID: "u1", SessionKey: "orgA:u1:s1",
		Prompt: "Please remember my account recovery code: " + secret + "-ns",
	})
	nsOut, _ := app.Chat(ctx, orbit.ChatInput{
		TenantID: "orgB", UserID: "u1", SessionKey: "orgB:u1:s1",
		Prompt: "What is my account recovery code?",
	})
	if strings.Contains(nsOut.Reply, secret+"-ns") {
		details = append(details, "  ✗ Even namespaced key leaked (unexpected)")
		leaked = true
	} else {
		details = append(details, "  ✓ Namespaced key isolates — BUT this is convention, not enforcement")
		fmt.Println("  ✓ Namespacing avoids the collision — but nothing forces you to do it")
	}

	// ...and it is still only a convention: mis-pass tenantA's id into the key
	// for userB's request and the leak returns. Nothing in Genkit checks it.
	fmt.Println("\n[isolation] The convention leak — mis-passed tenant id in the key:")
	misKey := "orgA:u1:s1" // developer accidentally reuses orgA's key for an orgB request
	misOut, _ := app.Chat(ctx, orbit.ChatInput{
		TenantID: "orgB", UserID: "u1", SessionKey: misKey,
		Prompt: "What is my account recovery code?",
	})
	if strings.Contains(misOut.Reply, secret+"-ns") {
		details = append(details,
			"  ✗ CONVENTION LEAK: orgB request + orgA's key -> real model returned orgA's secret",
			"     Genkit Go never validates that the session key matches the request identity.")
		fmt.Println("  ✗ CONVENTION LEAK confirmed: wrong key -> cross-tenant read")
		leaked = true
	}

	details = append(details, "",
		"  vs. aisotropy: ctx-propagated tenant + per-key/per-statement scoping;",
		"  the unscoped read is unrepresentable (Scope{} has unexported fields).")

	return leaked, details
}

func trunc(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 72 {
		return s[:72] + "…"
	}
	return s
}
