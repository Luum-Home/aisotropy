package taskagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestSessionKeyValidateRejectsReservedChars covers validate()'s user and
// session branches (the tenant branch is already exercised elsewhere): a
// reserved key-separator character in any component must be rejected, since it
// could forge a cross-tenant prefix collision.
func TestSessionKeyValidateRejectsReservedChars(t *testing.T) {
	store := NewInMemorySessions()
	ctx := context.Background()
	cases := []struct {
		name string
		key  SessionKey
	}{
		{"reserved-tenant", SessionKey{Tenant: "ac:me", User: "alice", Session: "s1"}},
		{"reserved-user", SessionKey{Tenant: "acme", User: "al:ice", Session: "s1"}},
		{"reserved-session", SessionKey{Tenant: "acme", User: "alice", Session: "s\x001"}},
		{"empty-user", SessionKey{Tenant: "acme", User: "", Session: "s1"}},
		{"empty-session", SessionKey{Tenant: "acme", User: "alice", Session: ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := store.Save(ctx, c.key, SessionState{}); err == nil {
				t.Fatal("Save with an invalid key = nil, want validation error")
			}
			if _, _, err := store.Load(ctx, c.key); err == nil {
				t.Fatal("Load with an invalid key = nil, want validation error")
			}
		})
	}
}

// TestBadgerSessionsValidateErrors mirrors the above for the BadgerDB store:
// Save/Load reject an invalid key before touching the DB.
func TestBadgerSessionsValidateErrors(t *testing.T) {
	store, err := newInMemoryBadgerSessions()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bad := SessionKey{Tenant: "acme", User: "al:ice", Session: "s1"}
	if err := store.Save(context.Background(), bad, SessionState{}); err == nil {
		t.Fatal("BadgerSessions.Save with invalid key = nil, want error")
	}
	if _, _, err := store.Load(context.Background(), bad); err == nil {
		t.Fatal("BadgerSessions.Load with invalid key = nil, want error")
	}
}

// TestOpenBadgerSessionsPersistsAcrossReopen exercises the on-disk
// OpenBadgerSessions path: a record saved, closed, and reopened from the same
// directory survives — the "persistent across process restarts" contract.
func TestOpenBadgerSessionsPersistsAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	ctx := context.Background()
	key := SessionKey{Tenant: "acme", User: "alice", Session: "s1"}
	state := SessionState{Prompt: "persist me", Steps: []AgentStep{{Tool: "git_commit", Result: "done"}}}

	store, err := OpenBadgerSessions(dir)
	if err != nil {
		t.Fatalf("OpenBadgerSessions: %v", err)
	}
	if err := store.Save(ctx, key, state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen the same directory: the record must still be there.
	reopened, err := OpenBadgerSessions(dir)
	if err != nil {
		t.Fatalf("reopen OpenBadgerSessions: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, found, err := reopened.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("Load after reopen: found=%v err=%v, want found=true", found, err)
	}
	if got.Prompt != "persist me" || len(got.Steps) != 1 || got.Steps[0].Tool != "git_commit" {
		t.Fatalf("reopened record does not match: %+v", got)
	}
}

// TestOpenBadgerSessionsBadDir covers the open-failure branch: a path that
// cannot be used as a Badger directory returns an error.
func TestOpenBadgerSessionsBadDir(t *testing.T) {
	// A regular file (not a directory) cannot host a Badger store.
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBadgerSessions(f); err == nil {
		t.Fatal("OpenBadgerSessions on a regular file = nil, want error")
	}
}

// TestResolveSessionKeyIdentityAndFallback covers both resolveSessionKey
// branches: identity read from ctx, and the default-scope fallback when ctx
// carries no identity.
func TestResolveSessionKeyIdentityAndFallback(t *testing.T) {
	// Identity present on ctx.
	ctx := llm.WithUser(llm.WithTenant(context.Background(), "acme"), "alice")
	key, err := resolveSessionKey(ctx, "s1")
	if err != nil {
		t.Fatalf("resolveSessionKey: %v", err)
	}
	if key.Tenant != "acme" || key.User != "alice" || key.Session != "s1" {
		t.Fatalf("resolved key = %+v, want acme/alice/s1", key)
	}

	// No identity: falls back to the local sentinel scope.
	key, err = resolveSessionKey(context.Background(), "s2")
	if err != nil {
		t.Fatalf("resolveSessionKey (no identity): %v", err)
	}
	if key.Tenant != defaultSessionScope || key.User != defaultSessionScope {
		t.Fatalf("fallback key = %+v, want %s scope", key, defaultSessionScope)
	}

	// A reserved-character session id fails validation.
	if _, err := resolveSessionKey(context.Background(), "s:bad"); err == nil {
		t.Fatal("resolveSessionKey with a reserved-character session id = nil, want error")
	}
}

// TestResumePromptRendersPriorTrace covers resumePrompt's branches: a prior
// prompt, prior steps (including the empty-tool "final answer" label), and the
// current task appended.
func TestResumePromptRendersPriorTrace(t *testing.T) {
	prior := SessionState{
		Prompt: "earlier task",
		Steps: []AgentStep{
			{Tool: "run_shell", Result: "line1\nline2"},
			{Tool: "", Result: "the final answer"},
		},
	}
	out := resumePrompt(prior, "current task")
	for _, want := range []string{"RESUMED", "earlier task", "Prior steps", "run_shell", "(final answer, no tool)", "current task"} {
		if !strings.Contains(out, want) {
			t.Errorf("resumePrompt output missing %q:\n%s", want, out)
		}
	}
	// previewResult trims at the first newline.
	if strings.Contains(out, "line2") {
		t.Errorf("previewResult did not trim at the newline:\n%s", out)
	}
}

func TestPreviewResultTruncatesLongSingleLine(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := previewResult(long)
	if len([]rune(got)) > 201 { // 200 chars + ellipsis rune
		t.Fatalf("previewResult length = %d runes, want <= 201", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("previewResult did not append an ellipsis: %q", got)
	}
}
