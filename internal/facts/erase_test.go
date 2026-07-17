package facts

import (
	"context"
	"testing"
)

// TestSQLiteEraseScope proves EraseScope removes every fact — current AND
// superseded — for a (tenant,user), leaves other subjects untouched, keeps the
// FTS index consistent, and is idempotent. This is the facts half of GDPR
// Art.17 subject erasure.
func TestSQLiteEraseScope(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	alice := Scope{tenant: "acme", user: "alice"}
	bob := Scope{tenant: "acme", user: "bob"}       // same tenant, other user
	other := Scope{tenant: "globex", user: "alice"} // other tenant, same user

	// Alice: one current fact plus a superseded one (Supersede archives the old
	// row and writes a replacement, so alice ends with two rows total).
	if err := st.Save(ctx, alice, Fact{ID: "a1", TenantID: "acme", UserID: "alice", Content: "alice likes espresso", Importance: 0.5}); err != nil {
		t.Fatalf("Save a1: %v", err)
	}
	if err := st.Supersede(ctx, alice, "a1", Fact{ID: "a2", TenantID: "acme", UserID: "alice", Content: "alice likes cortado", Importance: 0.5}); err != nil {
		t.Fatalf("Supersede a1->a2: %v", err)
	}
	// Bob and the other-tenant subject each keep one fact.
	if err := st.Save(ctx, bob, Fact{ID: "b1", TenantID: "acme", UserID: "bob", Content: "bob likes espresso", Importance: 0.5}); err != nil {
		t.Fatalf("Save b1: %v", err)
	}
	if err := st.Save(ctx, other, Fact{ID: "o1", TenantID: "globex", UserID: "alice", Content: "other likes espresso", Importance: 0.5}); err != nil {
		t.Fatalf("Save o1: %v", err)
	}

	// Erase alice: both her rows (current a2 + superseded a1) are removed.
	n, err := st.EraseScope(ctx, alice)
	if err != nil {
		t.Fatalf("EraseScope: %v", err)
	}
	if n != 2 {
		t.Fatalf("EraseScope removed %d rows, want 2 (current + superseded)", n)
	}

	// Alice searches empty (and the FTS index survived the bulk delete).
	if got, err := st.Search(ctx, alice, "espresso cortado", 10); err != nil {
		t.Fatalf("Search alice after erase: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("alice Search returned %d, want 0 after erase: %+v", len(got), got)
	}
	// Bob and the other tenant are intact.
	if got, _ := st.Search(ctx, bob, "espresso", 10); len(got) != 1 {
		t.Fatalf("bob Search returned %d, want 1 (erase leaked into another user)", len(got))
	}
	if got, _ := st.Search(ctx, other, "espresso", 10); len(got) != 1 {
		t.Fatalf("globex/alice Search returned %d, want 1 (erase leaked across tenants)", len(got))
	}

	// Idempotent: erasing again removes nothing.
	if n, err := st.EraseScope(ctx, alice); err != nil || n != 0 {
		t.Fatalf("second EraseScope = (%d,%v), want (0,nil) — not idempotent", n, err)
	}
}
