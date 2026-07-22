package aisotropy

// seed_history_test.go is the demonstrable artifact for App.SeedHistory: the
// adoption-surface seam that preloads a prior transcript into working memory with
// NO model call, so a "resume this conversation" flow does not pay a generation
// per assistant turn (the arena-memory-forensics hole).
//
// The load-bearing assertions are (1) SeedHistory makes ZERO calls to the LLM
// client, and (2) a seeded assistant turn is afterwards recalled — both off the
// working-memory store directly (rt.wm.Recent, the exact API the hydration path
// reads) and folded into the system prompt an actual subsequent Turn hands the
// model. The client is a counting wrapper over the keyless fastStub so every
// StreamChat is tallied while the underlying schema-valid stub reply keeps the
// non-streaming reflection pipeline happy.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// seedRecordingClient wraps a real llm.Client, tallying every StreamChat call and
// recording each system prompt the model was handed, while delegating to the
// inner client so replies stay schema-valid for the reflection pipeline.
type seedRecordingClient struct {
	inner   llm.Client
	mu      sync.Mutex
	calls   int
	systems []string
}

func (c *seedRecordingClient) StreamChat(ctx context.Context, system, prompt string) (<-chan llm.Token, error) {
	c.mu.Lock()
	c.calls++
	c.systems = append(c.systems, system)
	c.mu.Unlock()
	return c.inner.StreamChat(ctx, system, prompt)
}

func (c *seedRecordingClient) Name() string { return c.inner.Name() }

func (c *seedRecordingClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *seedRecordingClient) lastSystem() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.systems) == 0 {
		return ""
	}
	return c.systems[len(c.systems)-1]
}

// TestSeedHistoryRecalledWithoutModelCall is the core proof: seeding a prior
// user+assistant transcript makes no model call, and the seeded assistant turn
// is afterwards present in the session's working-memory buffer — the exact tier
// (and the exact Recent read) the hydration path pulls from.
func TestSeedHistoryRecalledWithoutModelCall(t *testing.T) {
	client := &seedRecordingClient{inner: fastStub()}
	app := New(WithLLM(client), WithRateLimit(1e9, 1e9))
	defer func() {
		if err := app.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	ctx := context.Background()
	const (
		tenant  = "acme"
		user    = "u1"
		session = "handoff-1"
	)

	n, err := app.SeedHistory(ctx, tenant, user, session,
		Message{Role: RoleUser, Content: "my order 12345 never arrived"},
		Message{Role: RoleAssistant, Content: "I see order 12345 shipped to the wrong depot"},
	)
	if err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	if n != 2 {
		t.Fatalf("SeedHistory wrote %d turns, want 2", n)
	}

	// (1) No model call was made to seed the transcript.
	if got := client.callCount(); got != 0 {
		t.Fatalf("SeedHistory triggered %d model call(s), want 0 — seeding must be pure durable writes", got)
	}

	// (2) The seeded assistant turn is in the working-memory buffer, recalled via
	// the same rt.wm.Recent the hydration path reads. Reading the store is still
	// zero model calls.
	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	rt.wm.Sync()
	msgs, err := rt.wm.Recent(ctx, memory.Key{Tenant: tenant, User: user, Session: session}, 100)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("recalled %d turns, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != RoleUser || !strings.Contains(msgs[0].Content, "12345") {
		t.Errorf("turn 0 = %+v, want the seeded user turn", msgs[0])
	}
	if msgs[1].Role != RoleAssistant || !strings.Contains(msgs[1].Content, "wrong depot") {
		t.Errorf("turn 1 = %+v, want the seeded assistant turn", msgs[1])
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("recalling from the store made %d model call(s), want 0", got)
	}

	// End-to-end: a subsequent Turn on the same session folds the seeded transcript
	// into the system prompt the model actually receives. That Turn DOES call the
	// model (once) — the assertion is that the seeded assistant content reached the
	// prompt, i.e. it was recalled, not that Turn itself avoided the model.
	if _, err := app.Turn(ctx, ChatInput{TenantID: tenant, UserID: user, SessionID: session, Prompt: "any update?"}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := client.callCount(); got != 1 {
		t.Fatalf("one Turn made %d model call(s), want exactly 1 (proving the 0 above was real)", got)
	}
	if sys := client.lastSystem(); !strings.Contains(sys, "wrong depot") {
		t.Errorf("seeded assistant turn not recalled into the hydrated system prompt; system=%q", sys)
	}
}

// TestSeedHistoryIsolationAndValidation proves the seed write is scoped to the
// named (tenant,user,session) and that malformed input is rejected before any
// write lands.
func TestSeedHistoryIsolationAndValidation(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer func() { _ = app.Close() }()

	ctx := context.Background()

	// Seed tenant A only.
	if _, err := app.SeedHistory(ctx, "tenantA", "user", "s", Message{Role: RoleAssistant, Content: "A-secret-payload"}); err != nil {
		t.Fatalf("SeedHistory(tenantA): %v", err)
	}

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	rt.wm.Sync()

	// tenant B's identical (user,session) coordinates see nothing — isolation by key.
	bMsgs, err := rt.wm.Recent(ctx, memory.Key{Tenant: "tenantB", User: "user", Session: "s"}, 100)
	if err != nil {
		t.Fatalf("Recent(tenantB): %v", err)
	}
	if len(bMsgs) != 0 {
		t.Fatalf("tenantB recalled %d turns, want 0 — cross-tenant leak", len(bMsgs))
	}

	// Validation: bad role, empty content, and an invalid (reserved-char) identity
	// all fail, and none write anything.
	if _, err := app.SeedHistory(ctx, "tenantA", "user", "s", Message{Role: "moderator", Content: "x"}); err == nil {
		t.Errorf("SeedHistory with unknown role should error")
	}
	if _, err := app.SeedHistory(ctx, "tenantA", "user", "s", Message{Role: RoleUser, Content: ""}); err == nil {
		t.Errorf("SeedHistory with empty content should error")
	}
	if _, err := app.SeedHistory(ctx, "bad:tenant", "user", "s", Message{Role: RoleUser, Content: "x"}); err == nil {
		t.Errorf("SeedHistory with reserved-character tenant should error")
	}

	// The failed calls left tenantA's buffer at exactly the one seeded turn.
	rt.wm.Sync()
	aMsgs, err := rt.wm.Recent(ctx, memory.Key{Tenant: "tenantA", User: "user", Session: "s"}, 100)
	if err != nil {
		t.Fatalf("Recent(tenantA): %v", err)
	}
	if len(aMsgs) != 1 || !strings.Contains(aMsgs[0].Content, "A-secret-payload") {
		t.Fatalf("tenantA buffer = %+v, want exactly the one seeded turn", aMsgs)
	}
}
