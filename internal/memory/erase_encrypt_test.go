package memory

import (
	"bytes"
	"context"
	"testing"
)

// TestEraseSubjectAcrossSessionsAndIsolation proves EraseSubject removes every
// key for a (tenant,user) across ALL of that subject's sessions, leaves other
// subjects untouched, is idempotent, and rejects a blank/malformed identity
// without deleting anything (no wildcard). This is the working-memory half of
// GDPR Art.17 subject erasure.
func TestEraseSubjectAcrossSessionsAndIsolation(t *testing.T) {
	wm, err := OpenInMemory(0, nil)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = wm.Close() })
	ctx := context.Background()

	// Alice has two sessions; bob and the other-tenant subject one each.
	aliceS1 := Key{Tenant: "acme", User: "alice", Session: "s1"}
	aliceS2 := Key{Tenant: "acme", User: "alice", Session: "s2"}
	bob := Key{Tenant: "acme", User: "bob", Session: "s1"}
	other := Key{Tenant: "globex", User: "alice", Session: "s1"}
	for _, k := range []Key{aliceS1, aliceS2, bob, other} {
		if err := wm.Append(ctx, k, Message{Role: "user", Content: "hello " + k.Session}); err != nil {
			t.Fatalf("Append %s/%s/%s: %v", k.Tenant, k.User, k.Session, err)
		}
	}

	n, err := wm.EraseSubject(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("EraseSubject: %v", err)
	}
	// Two sessions, each with a chat message + its seq counter => 4 keys.
	if n < 2 {
		t.Fatalf("EraseSubject removed %d keys, want >= 2 (both alice sessions)", n)
	}

	// Both alice sessions are empty.
	for _, k := range []Key{aliceS1, aliceS2} {
		got, err := wm.Recent(ctx, k, 10)
		if err != nil {
			t.Fatalf("Recent %s: %v", k.Session, err)
		}
		if len(got) != 0 {
			t.Fatalf("alice %s Recent returned %d, want 0 after erase", k.Session, len(got))
		}
	}
	// Bob and the other-tenant subject are intact.
	for _, k := range []Key{bob, other} {
		got, err := wm.Recent(ctx, k, 10)
		if err != nil {
			t.Fatalf("Recent %s/%s: %v", k.Tenant, k.User, err)
		}
		if len(got) != 1 {
			t.Fatalf("%s/%s Recent returned %d, want 1 (erase leaked across subjects)", k.Tenant, k.User, len(got))
		}
	}

	// Idempotent.
	if n, err := wm.EraseSubject(ctx, "acme", "alice"); err != nil || n != 0 {
		t.Fatalf("second EraseSubject = (%d,%v), want (0,nil) — not idempotent", n, err)
	}

	// Blank/malformed identity errors and deletes nothing.
	for _, tc := range []struct{ tenant, user string }{
		{"", "alice"},
		{"acme", ""},
		{"acme", "bad:user"},
	} {
		if _, err := wm.EraseSubject(ctx, tc.tenant, tc.user); err == nil {
			t.Fatalf("EraseSubject(%q,%q) = nil err, want a validation error (never a wildcard delete)", tc.tenant, tc.user)
		}
	}
	// Bob still intact after the rejected calls.
	if got, _ := wm.Recent(ctx, bob, 10); len(got) != 1 {
		t.Fatalf("bob Recent returned %d after rejected erases, want 1", len(got))
	}
}

// TestSubjectPrefixExactBoundary guards the delimiter that keeps user "a" from
// matching user "ab": the prefix for one is never a prefix of the other.
func TestSubjectPrefixExactBoundary(t *testing.T) {
	a, err := SubjectPrefix("acme", "a")
	if err != nil {
		t.Fatalf("SubjectPrefix a: %v", err)
	}
	ab, err := SubjectPrefix("acme", "ab")
	if err != nil {
		t.Fatalf("SubjectPrefix ab: %v", err)
	}
	if bytes.HasPrefix(ab, a) {
		t.Fatalf("SubjectPrefix(%q) is a prefix of SubjectPrefix(%q) — user boundary leak", "a", "ab")
	}
	if _, err := SubjectPrefix("", "u"); err == nil {
		t.Fatal("SubjectPrefix with blank tenant must error")
	}
}

// TestOpenEncryptedRoundTrip proves BadgerDB at-rest encryption is REAL: data
// written under a key is readable after reopen with the SAME key, and is
// inaccessible when reopened with a wrong key or no key at all. It also checks a
// wrong-length key is rejected up front.
func TestOpenEncryptedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	key := bytes.Repeat([]byte{0x2a}, 32) // 32 bytes => AES-256
	k := Key{Tenant: "acme", User: "alice", Session: "s1"}

	// Wrong-length key is rejected before touching disk.
	if _, err := OpenEncrypted(dir, 0, nil, bytes.Repeat([]byte{1}, 20)); err == nil {
		t.Fatal("OpenEncrypted with a 20-byte key must error (not 16/24/32)")
	}

	// Write under the key, then close so everything flushes to disk.
	wm, err := OpenEncrypted(dir, 0, nil, key)
	if err != nil {
		t.Fatalf("OpenEncrypted (write): %v", err)
	}
	if err := wm.Append(ctx, k, Message{Role: "user", Content: "secret payload"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := wm.Close(); err != nil {
		t.Fatalf("Close (write): %v", err)
	}

	// Reopen with the SAME key: the message reads back.
	wm2, err := OpenEncrypted(dir, 0, nil, key)
	if err != nil {
		t.Fatalf("OpenEncrypted (reopen same key): %v", err)
	}
	got, err := wm2.Recent(ctx, k, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Content != "secret payload" {
		t.Fatalf("Recent after encrypted reopen = %+v, want the single 'secret payload' message", got)
	}
	if err := wm2.Close(); err != nil {
		t.Fatalf("Close (reopen): %v", err)
	}

	// Reopen with a WRONG key: BadgerDB refuses to open.
	wrong := bytes.Repeat([]byte{0x99}, 32)
	if bad, err := OpenEncrypted(dir, 0, nil, wrong); err == nil {
		_ = bad.Close()
		t.Fatal("OpenEncrypted with a wrong key must error (encryption key mismatch)")
	}

	// Reopen with NO key (plaintext Open): BadgerDB refuses an encrypted store.
	if bad, err := Open(dir, 0, nil); err == nil {
		_ = bad.Close()
		t.Fatal("Open (no key) on an encrypted store must error")
	}
}
