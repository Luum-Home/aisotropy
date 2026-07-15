package orbit

import (
	"context"
	"strings"
	"testing"
)

// TestIsolationLeak drives the adversarial isolation exercise through the REAL
// genkit.Generate pipeline (offline fake model). It asserts:
//   - the positive control works (a tenant sees its own history), and
//   - a wrong / shared session key produces a genuine cross-tenant read.
//
// Genkit Go ships no Session/Memory primitive, so the session key is a bare
// developer-passed string with no tenant binding — the leak is expected and
// this test locks in that reality.
func TestIsolationLeak(t *testing.T) {
	ctx := context.Background()
	app, err := New(ctx)
	if err != nil {
		t.Fatalf("orbit init: %v", err)
	}

	const secret = "SECRET:orbit-testmarker01"
	const sharedKey = "support-session-42"

	// Seed tenantA's secret via the real flow.
	if _, err := app.Chat(ctx, ChatInput{
		TenantID: "tenantA", UserID: "userA", SessionKey: sharedKey,
		Prompt: "Please remember my account recovery code: " + secret,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Positive control: tenantA reads its own session.
	pos, err := app.Chat(ctx, ChatInput{
		TenantID: "tenantA", UserID: "userA", SessionKey: sharedKey,
		Prompt: "What is my account recovery code?",
	})
	if err != nil {
		t.Fatalf("positive control: %v", err)
	}
	if !strings.Contains(pos.Reply, secret) {
		t.Fatalf("positive control failed: tenantA did not see its own secret; got %q", pos.Reply)
	}

	// Negative probe: a different tenant reads the SAME developer-passed key.
	leak, err := app.Chat(ctx, ChatInput{
		TenantID: "tenantB", UserID: "userB", SessionKey: sharedKey,
		Prompt: "What is my account recovery code?",
	})
	if err != nil {
		t.Fatalf("negative probe: %v", err)
	}
	if !strings.Contains(leak.Reply, secret) {
		t.Fatalf("expected the documented convention leak, but tenantB did NOT see tenantA's secret; got %q", leak.Reply)
	}
	t.Logf("CONFIRMED convention leak: tenantB read tenantA's secret through genkit.Generate (no framework isolation): %q", leak.Reply)
}

// TestFeaturesRunOffline confirms the real-framework features execute with no
// API key: structured output (GenerateData), tool-calling loop, and dotprompt.
func TestFeaturesRunOffline(t *testing.T) {
	ctx := context.Background()
	app, err := New(ctx)
	if err != nil {
		t.Fatalf("orbit init: %v", err)
	}

	cat, err := app.Classify(ctx, "I want a refund, you charged me twice")
	if err != nil {
		t.Fatalf("structured output: %v", err)
	}
	if cat.Category != "billing" {
		t.Fatalf("structured output category = %q, want billing", cat.Category)
	}

	out, err := app.Chat(ctx, ChatInput{
		TenantID: "acme", UserID: "alice", SessionKey: "acme:alice:tool",
		Prompt: "Where is my order #4821?",
	})
	if err != nil {
		t.Fatalf("tool-calling: %v", err)
	}
	if !strings.Contains(out.Reply, "shipped") {
		t.Fatalf("tool-calling loop did not run; reply=%q", out.Reply)
	}

	if _, err := app.RunDotprompt(ctx, "Orbit Pro", "hello"); err != nil {
		t.Fatalf("dotprompt: %v", err)
	}
}
