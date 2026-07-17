package memory

import (
	"context"
	"testing"
)

// TestSubjectLanguage_Roundtrip covers the read/write half of per-subject
// language persistence: a Put is visible to a later Get for the same subject,
// and an overwrite replaces the value (ADR-0053).
func TestSubjectLanguage_Roundtrip(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)

	if code, found, err := wm.GetSubjectLanguage(ctx, "acme", "alice"); err != nil || found || code != "" {
		t.Fatalf("Get before Put = (%q,%v,%v), want (\"\",false,nil)", code, found, err)
	}

	if err := wm.PutSubjectLanguage(ctx, "acme", "alice", "es"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	code, found, err := wm.GetSubjectLanguage(ctx, "acme", "alice")
	if err != nil || !found || code != "es" {
		t.Fatalf("Get after Put = (%q,%v,%v), want (\"es\",true,nil)", code, found, err)
	}

	// Overwrite replaces, does not append.
	if err := wm.PutSubjectLanguage(ctx, "acme", "alice", "fr"); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	if code, found, err := wm.GetSubjectLanguage(ctx, "acme", "alice"); err != nil || !found || code != "fr" {
		t.Fatalf("Get after overwrite = (%q,%v,%v), want (\"fr\",true,nil)", code, found, err)
	}
}

// TestSubjectLanguage_IsolatedPerSubject is the constitutional-gate assertion:
// a language stored for one (tenant,user) subject is structurally unreachable
// from a different user OR a different tenant. There is no cross-subject read.
func TestSubjectLanguage_IsolatedPerSubject(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)

	// tenant "acme", user "alice" speaks Spanish.
	if err := wm.PutSubjectLanguage(ctx, "acme", "alice", "es"); err != nil {
		t.Fatalf("Put acme/alice: %v", err)
	}

	// A different USER in the same tenant does not see it.
	if code, found, err := wm.GetSubjectLanguage(ctx, "acme", "bob"); err != nil || found || code != "" {
		t.Fatalf("acme/bob leaked acme/alice's language: (%q,%v,%v)", code, found, err)
	}

	// A different TENANT with the same user id does not see it.
	if code, found, err := wm.GetSubjectLanguage(ctx, "globex", "alice"); err != nil || found || code != "" {
		t.Fatalf("globex/alice leaked acme/alice's language: (%q,%v,%v)", code, found, err)
	}

	// The two other subjects can hold their OWN, independent values without
	// disturbing the first.
	if err := wm.PutSubjectLanguage(ctx, "acme", "bob", "de"); err != nil {
		t.Fatalf("Put acme/bob: %v", err)
	}
	if err := wm.PutSubjectLanguage(ctx, "globex", "alice", "ja"); err != nil {
		t.Fatalf("Put globex/alice: %v", err)
	}
	for _, tc := range []struct {
		tenant, user, want string
	}{
		{"acme", "alice", "es"},
		{"acme", "bob", "de"},
		{"globex", "alice", "ja"},
	} {
		code, found, err := wm.GetSubjectLanguage(ctx, tc.tenant, tc.user)
		if err != nil || !found || code != tc.want {
			t.Fatalf("%s/%s = (%q,%v,%v), want (%q,true,nil)", tc.tenant, tc.user, code, found, err, tc.want)
		}
	}
}

// TestSubjectLanguage_BlankIdentityRejected proves a blank or reserved-character
// identity can never read or write a wildcard key — isolation by construction.
func TestSubjectLanguage_BlankIdentityRejected(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)

	cases := []struct{ tenant, user string }{
		{"", "alice"},
		{"acme", ""},
		{"a:b", "alice"},   // reserved separator smuggled into the key space
		{"acme", "a\x00b"}, // reserved NUL
	}
	for _, c := range cases {
		if _, _, err := wm.GetSubjectLanguage(ctx, c.tenant, c.user); err == nil {
			t.Errorf("Get(%q,%q) = nil error, want rejection", c.tenant, c.user)
		}
		if err := wm.PutSubjectLanguage(ctx, c.tenant, c.user, "es"); err == nil {
			t.Errorf("Put(%q,%q) = nil error, want rejection", c.tenant, c.user)
		}
	}

	// An empty code is rejected too — the stored value is a real tag, not blank.
	if err := wm.PutSubjectLanguage(ctx, "acme", "alice", ""); err == nil {
		t.Error("Put with empty code = nil error, want rejection")
	}
}

// TestSubjectLanguage_ErasedWithSubject proves the remembered language lives
// under the subject prefix and is removed by EraseSubject (GDPR Art.17), and
// that erasing one subject leaves another subject's language intact.
func TestSubjectLanguage_ErasedWithSubject(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)

	if err := wm.PutSubjectLanguage(ctx, "acme", "alice", "es"); err != nil {
		t.Fatalf("Put acme/alice: %v", err)
	}
	if err := wm.PutSubjectLanguage(ctx, "acme", "bob", "de"); err != nil {
		t.Fatalf("Put acme/bob: %v", err)
	}

	if _, err := wm.EraseSubject(ctx, "acme", "alice"); err != nil {
		t.Fatalf("EraseSubject: %v", err)
	}

	if _, found, _ := wm.GetSubjectLanguage(ctx, "acme", "alice"); found {
		t.Error("acme/alice language survived subject erasure")
	}
	if code, found, _ := wm.GetSubjectLanguage(ctx, "acme", "bob"); !found || code != "de" {
		t.Errorf("acme/bob language collaterally erased: (%q,%v)", code, found)
	}
}
