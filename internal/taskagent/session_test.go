package taskagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// promptRecorderClient is a keyless host-side llm.Client that records the LAST
// user prompt it was handed and then emits a fixed decision. It is how the
// resume tests observe "what the agent loop sees on resume" — the augmented
// prompt the resumed run is driven with. Not a llm.ToolCaller, so it drives the
// structured-output agentloop path (the same one constClient uses).
type promptRecorderClient struct {
	mu   sync.Mutex
	last string
	dec  agentloop.Decision
}

func (c *promptRecorderClient) Name() string { return "prompt-recorder-client" }

func (c *promptRecorderClient) StreamChat(_ context.Context, _, user string) (<-chan llm.Token, error) {
	c.mu.Lock()
	c.last = user
	c.mu.Unlock()
	ch := make(chan llm.Token, 2)
	ch <- llm.Token{Text: marshalDecision(c.dec)}
	ch <- llm.Token{Done: true}
	close(ch)
	return ch, nil
}

func (c *promptRecorderClient) lastPrompt() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// --- store-level tests (no RunTask, no git, no docker) ---

// TestSessionStores_RoundTripAndFound exercises Load/Save round-trip and the
// found bool for both built-in stores. The BadgerDB store runs in in-memory mode
// so the exact persistent code path (JSON encode, scoped key, txn Get/Set) is
// covered keylessly and without a temp dir.
func TestSessionStores_RoundTripAndFound(t *testing.T) {
	badger, err := newInMemoryBadgerSessions()
	if err != nil {
		t.Fatalf("open badger sessions: %v", err)
	}
	t.Cleanup(func() { _ = badger.Close() })

	stores := map[string]SessionStore{
		"in-memory": NewInMemorySessions(),
		"badger":    badger,
	}
	ctx := context.Background()
	key := SessionKey{Tenant: "acme", User: "alice", Session: "s1"}

	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			if _, found, err := store.Load(ctx, key); err != nil || found {
				t.Fatalf("empty Load: found=%v err=%v, want found=false err=nil", found, err)
			}
			state := SessionState{
				Prompt: "do the thing",
				Steps: []AgentStep{
					{Tool: "run_shell", Args: map[string]string{"cmd": "echo hi"}, Result: "hi"},
					{Tool: "git_commit", Result: "committed"},
				},
			}
			if err := store.Save(ctx, key, state); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, found, err := store.Load(ctx, key)
			if err != nil || !found {
				t.Fatalf("Load after Save: found=%v err=%v, want found=true", found, err)
			}
			if got.Prompt != state.Prompt || len(got.Steps) != 2 {
				t.Fatalf("round-trip mismatch: %+v", got)
			}
			if got.Steps[0].Tool != "run_shell" || got.Steps[0].Args["cmd"] != "echo hi" || got.Steps[1].Tool != "git_commit" {
				t.Errorf("step trace not preserved verbatim: %+v", got.Steps)
			}
		})
	}
}

// TestSessionStores_Isolation locks the multi-tenant gate at the store level:
// state saved under one (tenant,user,session) is invisible to every other key
// that shares a session id but differs on tenant and/or user — the tenantA/userB
// and tenantB/userA cross combinations included, mirroring the working-memory
// isolation suite.
func TestSessionStores_Isolation(t *testing.T) {
	badger, err := newInMemoryBadgerSessions()
	if err != nil {
		t.Fatalf("open badger sessions: %v", err)
	}
	t.Cleanup(func() { _ = badger.Close() })

	stores := map[string]SessionStore{
		"in-memory": NewInMemorySessions(),
		"badger":    badger,
	}
	ctx := context.Background()
	const session = "shared-session-id"
	keys := map[string]SessionKey{
		"A":       {Tenant: "acme", User: "alice", Session: session},
		"B":       {Tenant: "globex", User: "bob", Session: session},
		"crossTU": {Tenant: "acme", User: "bob", Session: session},
		"crossUT": {Tenant: "globex", User: "alice", Session: session},
	}

	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			for tag, key := range keys {
				if err := store.Save(ctx, key, SessionState{Prompt: "secret-" + tag}); err != nil {
					t.Fatalf("Save %s: %v", tag, err)
				}
			}
			for tag, key := range keys {
				got, found, err := store.Load(ctx, key)
				if err != nil || !found {
					t.Fatalf("Load %s: found=%v err=%v", tag, found, err)
				}
				if got.Prompt != "secret-"+tag {
					t.Errorf("scope %s read %q, want its own secret-%s (cross-scope bleed)", tag, got.Prompt, tag)
				}
			}
		})
	}
}

// --- RunTask-integrated tests (git required, docker NOT — uses fakeProvider) ---

// TestRunTask_SessionID_PersistsStepTrace proves requirement (1): a run with a
// SessionID persists its step trace to the store, keyed by the resolved
// (tenant,user,session). No docker: fakeProvider stands up a non-container
// sandbox; the keyless constClient drives exactly one run_shell step.
func TestRunTask_SessionID_PersistsStepTrace(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)

	store := NewInMemorySessions()
	client := &constClient{dec: agentloop.Decision{Tool: ToolRunShell, Args: map[string]string{"cmd": "echo hi"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunTask(ctx, client, nil, Request{
		RepoDir:      repo,
		Prompt:       "do a shell step",
		MaxSteps:     1,
		Scopes:       HandScopes(),
		Provider:     &fakeProvider{},
		SessionID:    "s1",
		SessionStore: store,
	})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(res.Steps))
	}

	// No identity on ctx → the resolved key uses the default local scope.
	key := SessionKey{Tenant: defaultSessionScope, User: defaultSessionScope, Session: "s1"}
	got, found, err := store.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("session not persisted: found=%v err=%v", found, err)
	}
	if len(got.Steps) != len(res.Steps) {
		t.Fatalf("persisted %d steps, want %d", len(got.Steps), len(res.Steps))
	}
	if got.Steps[0].Tool != ToolRunShell {
		t.Errorf("persisted step tool = %q, want %q", got.Steps[0].Tool, ToolRunShell)
	}
	if got.Prompt != "do a shell step" {
		t.Errorf("persisted prompt = %q", got.Prompt)
	}
}

// TestRunTask_Resume_RehydratesPriorContext proves requirement (2): a Resume run
// rehydrates the prior run's trace and the agent loop is driven with a prompt
// that CONTAINS that prior context (asserted on what the recording client saw),
// and the saved trace accumulates across the two runs.
func TestRunTask_Resume_RehydratesPriorContext(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)

	store := NewInMemorySessions()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Run 1: populate the session with one run_shell step.
	run1 := &constClient{dec: agentloop.Decision{Tool: ToolRunShell, Args: map[string]string{"cmd": "echo first-run"}}}
	if _, err := RunTask(ctx, run1, nil, Request{
		RepoDir: repo, Prompt: "first task", MaxSteps: 1, Scopes: HandScopes(),
		Provider: &fakeProvider{}, SessionID: "s1", SessionStore: store,
	}); err != nil {
		t.Fatalf("run 1: %v", err)
	}

	// Run 2: RESUME. The recorder captures the (augmented) prompt the loop sees.
	run2 := &promptRecorderClient{dec: agentloop.Decision{Tool: ToolRunShell, Args: map[string]string{"cmd": "echo second-run"}}}
	if _, err := RunTask(ctx, run2, nil, Request{
		RepoDir: repo, Prompt: "second task", MaxSteps: 1, Scopes: HandScopes(),
		Provider: &fakeProvider{}, SessionID: "s1", Resume: true, SessionStore: store,
	}); err != nil {
		t.Fatalf("run 2 (resume): %v", err)
	}

	seen := run2.lastPrompt()
	if !strings.Contains(seen, "RESUMED") {
		t.Errorf("resumed prompt missing the resume preamble:\n%s", seen)
	}
	if !strings.Contains(seen, "Prior steps") || !strings.Contains(seen, ToolRunShell) {
		t.Errorf("resumed prompt does not carry the prior step trace:\n%s", seen)
	}
	if !strings.Contains(seen, "first task") {
		t.Errorf("resumed prompt does not carry the prior task prompt:\n%s", seen)
	}
	if !strings.Contains(seen, "second task") {
		t.Errorf("resumed prompt dropped the CURRENT task:\n%s", seen)
	}

	// The persisted trace accumulated: run 1's step + run 2's step.
	key := SessionKey{Tenant: defaultSessionScope, User: defaultSessionScope, Session: "s1"}
	got, found, err := store.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("session missing after resume: found=%v err=%v", found, err)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("accumulated %d steps across two runs, want 2", len(got.Steps))
	}
}

// TestRunTask_Resume_TenantScoped proves requirement (3): another tenant's
// resume does not see a first tenant's session. Tenant/user travel via ctx
// (llm.WithTenant/WithUser), the SessionID is deliberately SHARED, and the store
// is the SAME instance — so only the (tenant,user,session) key scoping keeps
// them apart. Tenant B's resume starts COLD (no preamble), and B's stored record
// holds only B's own step, never A's.
func TestRunTask_Resume_TenantScoped(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	store := NewInMemorySessions()

	repoA := t.TempDir()
	gitInit(t, repoA)
	ctxA := llm.WithUser(llm.WithTenant(context.Background(), "acme"), "alice")
	runA := &constClient{dec: agentloop.Decision{Tool: ToolRunShell, Args: map[string]string{"cmd": "echo tenant-A"}}}
	if _, err := RunTask(ctxA, runA, nil, Request{
		RepoDir: repoA, Prompt: "tenant A task", MaxSteps: 1, Scopes: HandScopes(),
		Provider: &fakeProvider{}, SessionID: "shared", SessionStore: store,
	}); err != nil {
		t.Fatalf("tenant A run: %v", err)
	}

	// Tenant B resumes the SAME session id — must not inherit A's context.
	repoB := t.TempDir()
	gitInit(t, repoB)
	ctxB := llm.WithUser(llm.WithTenant(context.Background(), "globex"), "bob")
	runB := &promptRecorderClient{dec: agentloop.Decision{Tool: ToolRunShell, Args: map[string]string{"cmd": "echo tenant-B"}}}
	if _, err := RunTask(ctxB, runB, nil, Request{
		RepoDir: repoB, Prompt: "tenant B task", MaxSteps: 1, Scopes: HandScopes(),
		Provider: &fakeProvider{}, SessionID: "shared", Resume: true, SessionStore: store,
	}); err != nil {
		t.Fatalf("tenant B run: %v", err)
	}

	seen := runB.lastPrompt()
	if strings.Contains(seen, "RESUMED") || strings.Contains(seen, "tenant A task") {
		t.Errorf("tenant B's resume LEAKED tenant A's session:\n%s", seen)
	}

	// A's record is intact and holds A's step; B's record holds only B's step.
	keyA := SessionKey{Tenant: "acme", User: "alice", Session: "shared"}
	stateA, foundA, _ := store.Load(context.Background(), keyA)
	if !foundA || len(stateA.Steps) != 1 || stateA.Prompt != "tenant A task" {
		t.Errorf("tenant A's session was clobbered: found=%v %+v", foundA, stateA)
	}
	keyB := SessionKey{Tenant: "globex", User: "bob", Session: "shared"}
	stateB, foundB, _ := store.Load(context.Background(), keyB)
	if !foundB || len(stateB.Steps) != 1 || stateB.Prompt != "tenant B task" {
		t.Errorf("tenant B's session is wrong: found=%v %+v", foundB, stateB)
	}
}

// TestRunTask_ResumeWithoutStoreIsError proves the hard-error guard: Resume set
// without a SessionID/SessionStore fails fast rather than silently starting cold.
// It needs neither git nor docker — the guard fires before any worktree/sandbox.
func TestRunTask_ResumeWithoutStoreIsError(t *testing.T) {
	_, err := RunTask(context.Background(), &constClient{}, nil, Request{
		RepoDir: t.TempDir(), // non-empty so the guard, not the RepoDir check, fires
		Prompt:  "x",
		Resume:  true,
	})
	if err == nil {
		t.Fatal("Resume without a SessionStore/SessionID must be a hard error")
	}
	if !strings.Contains(err.Error(), "Resume requires") {
		t.Errorf("error = %v, want the Resume-requires guard message", err)
	}
}
