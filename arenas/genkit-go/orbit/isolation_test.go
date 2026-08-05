package orbit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	aiexp "github.com/firebase/genkit/go/ai/exp"
	"github.com/firebase/genkit/go/ai/exp/localstore"
)

// TestIsolationLeak drives the adversarial isolation exercise through the REAL
// genkit.Generate pipeline (offline fake model). It asserts:
//   - the positive control works (a tenant sees its own history), and
//   - a wrong / shared session key produces a genuine cross-tenant read.
//
// Genkit Go v1.10.0 DOES ship an experimental session-memory primitive
// (ai/exp SessionStore + localstore), but it is keyed by a developer-chosen
// session id with NO tenant/user/product binding — persistence, not isolation.
// The arena's own store (orbit/store.go) is a DIY stand-in with identical
// semantics; TestRealSessionStoreIsTenantUnaware below reproduces the same leak
// against Genkit's REAL localstore.NewInMemorySessionStore to prove the point
// holds against the framework's actual primitive, not just our stand-in.
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
	// SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md
	t.Logf("CONFIRMED convention leak: tenantB read tenantA's secret through genkit.Generate (no framework isolation): %q", leak.Reply)
}

// TestRealSessionStoreIsTenantUnaware drives Genkit's OWN experimental session
// primitive — localstore.NewInMemorySessionStore (github.com/firebase/genkit/go
// v1.10.0, ai/exp) — directly, with NO DIY store in the path. It proves the
// framework's real memory primitive:
//
//	(a) PERSISTS and replays conversation Messages across turns (positive
//	    control: the same session id reads its own history back), and
//	(b) is keyed SOLELY by a developer-chosen session id with NO tenant/user/
//	    product binding — SessionSnapshot/SessionState carry only a SessionID
//	    field, so two identities that reuse one session id share memory. The
//	    leak our DIY store reproduces is a property of the real primitive too.
//
// This is the honest core: Genkit-Go ships an (experimental) memory primitive,
// but it provides persistence, not isolation. Isolation stays DIY.
func TestRealSessionStoreIsTenantUnaware(t *testing.T) {
	ctx := context.Background()

	// The framework's shipped in-memory session store. State[any] because the
	// custom-state slot is unused here; we only exercise Messages.
	store := localstore.NewInMemorySessionStore[any]()

	const secret = "SECRET:orbit-realstore-marker7"
	// The VERY COMMON developer choice: key the conversation by session id
	// alone. Nothing in the framework reminds you a tenant belongs here —
	// SessionSnapshot has no tenant field to put it in.
	const sessionID = "support-session-42"

	seed := func(msgs []*ai.Message) error {
		now := time.Now()
		_, err := store.SaveSnapshot(ctx, "",
			func(_ *aiexp.SessionSnapshot[any]) (*aiexp.SessionSnapshot[any], error) {
				return &aiexp.SessionSnapshot[any]{
					SessionID: sessionID,
					CreatedAt: now,
					UpdatedAt: now,
					State: &aiexp.SessionState[any]{
						SessionID: sessionID,
						Messages:  msgs,
					},
				}, nil
			})
		return err
	}

	// tenantA seeds its recovery code into the session.
	if err := seed([]*ai.Message{
		ai.NewUserTextMessage("Please remember my account recovery code: " + secret),
		ai.NewTextMessage(ai.RoleModel, "Noted, I will remember it."),
	}); err != nil {
		t.Fatalf("seed via real SessionStore: %v", err)
	}

	// (a) POSITIVE CONTROL — the primitive persists and replays: reading the
	// same session id returns tenantA's own messages.
	snap, err := store.GetLatestSnapshot(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetLatestSnapshot (positive control): %v", err)
	}
	if snap == nil || snap.State == nil {
		t.Fatalf("real SessionStore returned no snapshot for the seeded session")
	}
	if got := messagesText(snap.State.Messages); !strings.Contains(got, secret) {
		t.Fatalf("positive control failed: real SessionStore did not replay tenantA's secret; got %q", got)
	}

	// (b) TENANT-UNAWARE LEAK — "tenantB" is a different identity, but the store
	// exposes NO tenant/user/product key: the only lookup key is the session id.
	// A tenantB caller that reuses the same session id (a shared, guessable, or
	// copy-pasted id) reads tenantA's history verbatim. The framework has no
	// field with which to tell the two identities apart.
	leakSnap, err := store.GetLatestSnapshot(ctx, sessionID) // tenantB, same session id
	if err != nil {
		t.Fatalf("GetLatestSnapshot (leak probe): %v", err)
	}
	if got := messagesText(leakSnap.State.Messages); !strings.Contains(got, secret) {
		t.Fatalf("expected the real SessionStore to be tenant-unaware, but tenantB did NOT read tenantA's secret; got %q", got)
	}

	// Contrast: the store DOES isolate by session id — a DIFFERENT session id is
	// empty. That is exactly the point: isolation is by developer-chosen session
	// id, never by tenant. Fold tenant into the id yourself, and remember to,
	// every time — or leak.
	otherSnap, err := store.GetLatestSnapshot(ctx, "some-other-session-id")
	if err != nil {
		t.Fatalf("GetLatestSnapshot (other session): %v", err)
	}
	if otherSnap != nil {
		t.Fatalf("expected nil for an unseeded session id, got %+v", otherSnap)
	}

	t.Logf("CONFIRMED against the REAL exp SessionStore: session-id-keyed, tenant-unaware. "+
		"A tenantB read of session %q returned tenantA's secret; SessionSnapshot/SessionState "+
		"carry only a SessionID, no tenant/user/product field. Persistence, not isolation.", sessionID)
}

// messagesText flattens a message slice to its text parts for substring checks.
func messagesText(msgs []*ai.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m == nil {
			continue
		}
		for _, p := range m.Content {
			if p != nil && p.Text != "" {
				b.WriteString(p.Text)
				b.WriteString(" ")
			}
		}
	}
	return b.String()
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
