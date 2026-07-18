package facts

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeDocBackend is a tenant-UNAWARE, in-memory DocBackend used to exercise the
// sealed scopedDocStore decorator (backend.go) without a real third-party
// keyword engine. It stores documents under an opaque namespace exactly as a
// community backend would, and offers per-method error injection so the
// decorator's error-propagation branches can be driven with the stdlib only.
type fakeDocBackend struct {
	// ns -> key -> doc
	data map[string]map[string]BackendDoc

	putErr    error
	getErr    error
	searchErr error
	delErr    error
	scanErr   error
	exportErr error
	closeErr  error

	closed bool
}

func newFakeDocBackend() *fakeDocBackend {
	return &fakeDocBackend{data: map[string]map[string]BackendDoc{}}
}

func (b *fakeDocBackend) Put(_ context.Context, ns, key string, doc BackendDoc) error {
	if b.putErr != nil {
		return b.putErr
	}
	m, ok := b.data[ns]
	if !ok {
		m = map[string]BackendDoc{}
		b.data[ns] = m
	}
	// Deep-copy Fields so a later mutation by the decorator can't alias storage.
	cp := BackendDoc{Key: doc.Key, Content: doc.Content, Fields: map[string]string{}}
	for k, v := range doc.Fields {
		cp.Fields[k] = v
	}
	m[key] = cp
	return nil
}

func (b *fakeDocBackend) Get(_ context.Context, ns, key string) (BackendDoc, bool, error) {
	if b.getErr != nil {
		return BackendDoc{}, false, b.getErr
	}
	m, ok := b.data[ns]
	if !ok {
		return BackendDoc{}, false, nil
	}
	d, ok := m[key]
	return d, ok, nil
}

// Search returns docs whose Content contains query as a substring, higher score
// for a longer match ratio, best first — enough signal to exercise the
// decorator's re-ranking without depending on a real FTS engine.
func (b *fakeDocBackend) Search(_ context.Context, ns, query string, k int) ([]BackendDocHit, error) {
	if b.searchErr != nil {
		return nil, b.searchErr
	}
	var hits []BackendDocHit
	for _, d := range b.data[ns] {
		if strings.Contains(strings.ToLower(d.Content), strings.ToLower(query)) {
			hits = append(hits, BackendDocHit{BackendDoc: d, Score: float64(len(query)) / float64(len(d.Content)+1)})
		}
	}
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

func (b *fakeDocBackend) Delete(_ context.Context, ns string, keys []string) error {
	if b.delErr != nil {
		return b.delErr
	}
	m := b.data[ns]
	for _, key := range keys {
		delete(m, key)
	}
	return nil
}

func (b *fakeDocBackend) Scan(_ context.Context, ns string) ([]BackendDoc, error) {
	if b.scanErr != nil {
		return nil, b.scanErr
	}
	var out []BackendDoc
	for _, d := range b.data[ns] {
		out = append(out, d)
	}
	return out, nil
}

func (b *fakeDocBackend) ExportAll(_ context.Context) ([]BackendDoc, error) {
	if b.exportErr != nil {
		return nil, b.exportErr
	}
	var out []BackendDoc
	for _, m := range b.data {
		for _, d := range m {
			out = append(out, d)
		}
	}
	return out, nil
}

func (b *fakeDocBackend) Close() error {
	b.closed = true
	return b.closeErr
}

// fixedClock returns a deterministic now for created_at/recency assertions.
func newDocStore(b DocBackend, now time.Time) *scopedDocStore {
	return &scopedDocStore{backend: b, now: func() time.Time { return now }}
}

func TestDocBackendStore_SaveSearchRoundTrip(t *testing.T) {
	b := newFakeDocBackend()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	s := newDocStore(b, now)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	f := Fact{ID: "f1", TenantID: "acme", UserID: "alice",
		Content: "the user prefers postgres over mysql", Importance: 0.8}
	if err := s.Save(ctx, sc, f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Search(ctx, sc, "postgres", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d, want 1", len(got))
	}
	if got[0].ID != "f1" || got[0].TenantID != "acme" || got[0].UserID != "alice" {
		t.Fatalf("identity reattachment wrong: %+v", got[0].Fact)
	}
	if got[0].Content != f.Content || got[0].Importance != 0.8 {
		t.Fatalf("content/importance not round-tripped: %+v", got[0].Fact)
	}
	if !got[0].CreatedAt.Equal(now) || !got[0].UpdatedAt.Equal(now) {
		t.Fatalf("timestamps not set from clock: created=%v updated=%v", got[0].CreatedAt, got[0].UpdatedAt)
	}
	if got[0].Score <= 0 {
		t.Fatalf("expected positive score, got %v", got[0].Score)
	}
}

func TestDocBackendStore_SaveScopeMismatch(t *testing.T) {
	s := newDocStore(newFakeDocBackend(), time.Now())
	sc := Scope{tenant: "acme", user: "alice"}
	// Wrong tenant.
	err := s.Save(context.Background(), sc, Fact{ID: "x", TenantID: "evil", UserID: "alice", Content: "c"})
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("wrong tenant: got %v, want ErrScopeMismatch", err)
	}
	// Wrong user.
	err = s.Save(context.Background(), sc, Fact{ID: "x", TenantID: "acme", UserID: "mallory", Content: "c"})
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("wrong user: got %v, want ErrScopeMismatch", err)
	}
}

func TestDocBackendStore_SavePreservesCreatedAtOnUpsert(t *testing.T) {
	b := newFakeDocBackend()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	// First write at t0.
	if err := newDocStore(b, t0).Save(ctx, sc, Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "hello world", Importance: 0.5}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// Upsert at t1 with new content; created_at must survive, updated_at moves.
	t1 := t0.Add(48 * time.Hour)
	if err := newDocStore(b, t1).Save(ctx, sc, Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "hello updated", Importance: 0.9}); err != nil {
		t.Fatalf("upsert Save: %v", err)
	}

	got, err := newDocStore(b, t1).Search(ctx, sc, "hello", 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("Search after upsert: got %d (%v)", len(got), err)
	}
	if !got[0].CreatedAt.Equal(t0) {
		t.Fatalf("created_at not preserved: got %v want %v", got[0].CreatedAt, t0)
	}
	if !got[0].UpdatedAt.Equal(t1) {
		t.Fatalf("updated_at not moved: got %v want %v", got[0].UpdatedAt, t1)
	}
	if got[0].Content != "hello updated" || got[0].Importance != 0.9 {
		t.Fatalf("upsert did not refresh content/importance: %+v", got[0].Fact)
	}
}

func TestDocBackendStore_SaveDefaultsCreatedAtWhenZero(t *testing.T) {
	b := newFakeDocBackend()
	now := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	sc := Scope{tenant: "t", user: "u"}
	ctx := context.Background()
	// Fact has zero CreatedAt: put() should stamp it with now.
	if err := newDocStore(b, now).Save(ctx, sc, Fact{ID: "f", TenantID: "t", UserID: "u", Content: "zero clock fact"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := newDocStore(b, now).Search(ctx, sc, "clock", 5)
	if len(got) != 1 || !got[0].CreatedAt.Equal(now) {
		t.Fatalf("created_at default: %+v", got)
	}
}

func TestDocBackendStore_SearchEmptyAndZeroLimit(t *testing.T) {
	s := newDocStore(newFakeDocBackend(), time.Now())
	sc := Scope{tenant: "t", user: "u"}
	ctx := context.Background()
	if got, err := s.Search(ctx, sc, "   ", 5); err != nil || got != nil {
		t.Fatalf("whitespace query: got %v (%v), want nil", got, err)
	}
	if got, err := s.Search(ctx, sc, "x", 0); err != nil || got != nil {
		t.Fatalf("zero limit: got %v (%v), want nil", got, err)
	}
}

func TestDocBackendStore_SearchExcludesSupersededAndTrims(t *testing.T) {
	b := newFakeDocBackend()
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	s := newDocStore(b, now)

	for i := 0; i < 5; i++ {
		id := "f" + strconv.Itoa(i)
		if err := s.Save(ctx, sc, Fact{ID: id, TenantID: "acme", UserID: "alice", Content: "shared keyword doc " + id, Importance: 0.5}); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}
	// Supersede f0 -> it must drop out of Search results.
	if err := s.Supersede(ctx, sc, "f0", Fact{ID: "f0-new", TenantID: "acme", UserID: "alice", Content: "shared keyword replacement", Importance: 0.5}); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	got, err := s.Search(ctx, sc, "keyword", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit trim: got %d, want 2", len(got))
	}
	for _, sf := range got {
		if sf.ID == "f0" {
			t.Fatalf("superseded fact f0 leaked into results")
		}
	}
}

func TestDocBackendStore_SupersedeScopeMismatchAndNotFound(t *testing.T) {
	b := newFakeDocBackend()
	now := time.Now()
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	s := newDocStore(b, now)

	// Scope mismatch on the replacement.
	err := s.Supersede(ctx, sc, "old", Fact{ID: "n", TenantID: "evil", UserID: "alice", Content: "c"})
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("scope mismatch: got %v", err)
	}
	// Target absent.
	err = s.Supersede(ctx, sc, "missing", Fact{ID: "n", TenantID: "acme", UserID: "alice", Content: "c"})
	if !errors.Is(err, ErrFactNotFound) {
		t.Fatalf("absent target: got %v, want ErrFactNotFound", err)
	}

	// Save then supersede once (ok), then supersede the SAME old id again -> not found
	// because it is already superseded.
	if err := s.Save(ctx, sc, Fact{ID: "a", TenantID: "acme", UserID: "alice", Content: "orig"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Supersede(ctx, sc, "a", Fact{ID: "a2", TenantID: "acme", UserID: "alice", Content: "next"}); err != nil {
		t.Fatalf("first Supersede: %v", err)
	}
	err = s.Supersede(ctx, sc, "a", Fact{ID: "a3", TenantID: "acme", UserID: "alice", Content: "third"})
	if !errors.Is(err, ErrFactNotFound) {
		t.Fatalf("re-supersede already-superseded: got %v, want ErrFactNotFound", err)
	}
}

func TestDocBackendStore_ExportAllCrossTenant(t *testing.T) {
	b := newFakeDocBackend()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	acme := Scope{tenant: "acme", user: "alice"}
	globex := Scope{tenant: "globex", user: "bob"}
	sA := newDocStore(b, now)
	if err := sA.Save(ctx, acme, Fact{ID: "z", TenantID: "acme", UserID: "alice", Content: "acme fact"}); err != nil {
		t.Fatalf("Save acme: %v", err)
	}
	if err := sA.Save(ctx, globex, Fact{ID: "a", TenantID: "globex", UserID: "bob", Content: "globex fact"}); err != nil {
		t.Fatalf("Save globex: %v", err)
	}
	// A superseded fact must be excluded from ExportAll.
	if err := sA.Save(ctx, acme, Fact{ID: "s", TenantID: "acme", UserID: "alice", Content: "will be archived"}); err != nil {
		t.Fatalf("Save s: %v", err)
	}
	if err := sA.Supersede(ctx, acme, "s", Fact{ID: "s2", TenantID: "acme", UserID: "alice", Content: "replacement"}); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	all, err := sA.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	// Expect: acme/z, globex/a, acme/s2 — all current, ordered by id.
	var ids []string
	for _, f := range all {
		ids = append(ids, f.ID)
		if f.SupersededBy != "" {
			t.Fatalf("superseded fact %s leaked into ExportAll", f.ID)
		}
	}
	want := []string{"a", "s2", "z"}
	if len(ids) != len(want) {
		t.Fatalf("ExportAll ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ExportAll order = %v, want %v", ids, want)
		}
	}
	// Cross-tenant identity comes from the doc's own fields.
	for _, f := range all {
		if f.ID == "a" && (f.TenantID != "globex" || f.UserID != "bob") {
			t.Fatalf("exported identity wrong for a: %+v", f)
		}
	}
}

func TestDocBackendStore_DeleteAndClose(t *testing.T) {
	b := newFakeDocBackend()
	now := time.Now()
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	s := newDocStore(b, now)

	// Empty ids is a no-op (must not reach the backend Delete path).
	if err := s.Delete(ctx, sc, nil); err != nil {
		t.Fatalf("Delete(nil): %v", err)
	}
	if err := s.Save(ctx, sc, Fact{ID: "d1", TenantID: "acme", UserID: "alice", Content: "deletable keyword"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Delete(ctx, sc, []string{"d1"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := s.Search(ctx, sc, "keyword", 5)
	if len(got) != 0 {
		t.Fatalf("expected fact deleted, found %d", len(got))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !b.closed {
		t.Fatalf("Close did not reach backend")
	}
}

func TestDocBackendStore_NamespaceIsolation(t *testing.T) {
	// Distinct (tenant,user) pairs must map to distinct namespaces, and the
	// \x00 separator must keep ("a","bc") and ("ab","c") distinct.
	pairs := []Scope{
		{tenant: "a", user: "bc"},
		{tenant: "ab", user: "c"},
		{tenant: "acme", user: "alice"},
		{tenant: "acme", user: "bob"},
	}
	seen := map[string]Scope{}
	for _, sc := range pairs {
		ns := namespaceFor(sc)
		if !strings.HasPrefix(ns, "aisotropy-facts-v1-") {
			t.Fatalf("namespace prefix wrong: %q", ns)
		}
		if prev, ok := seen[ns]; ok {
			t.Fatalf("namespace collision between %+v and %+v", prev, sc)
		}
		seen[ns] = sc
	}
	// Determinism: same scope, same namespace.
	if namespaceFor(pairs[0]) != namespaceFor(Scope{tenant: "a", user: "bc"}) {
		t.Fatalf("namespaceFor is not deterministic")
	}
}

func TestDocBackendStore_BackendErrorsPropagate(t *testing.T) {
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	sentinel := errors.New("backend boom")

	t.Run("save get error", func(t *testing.T) {
		b := newFakeDocBackend()
		b.getErr = sentinel
		if err := newDocStore(b, time.Now()).Save(ctx, sc, Fact{ID: "f", TenantID: "acme", UserID: "alice", Content: "c"}); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("save put error", func(t *testing.T) {
		b := newFakeDocBackend()
		b.putErr = sentinel
		if err := newDocStore(b, time.Now()).Save(ctx, sc, Fact{ID: "f", TenantID: "acme", UserID: "alice", Content: "c"}); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("search error", func(t *testing.T) {
		b := newFakeDocBackend()
		b.searchErr = sentinel
		if _, err := newDocStore(b, time.Now()).Search(ctx, sc, "x", 5); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("supersede get error", func(t *testing.T) {
		b := newFakeDocBackend()
		b.getErr = sentinel
		if err := newDocStore(b, time.Now()).Supersede(ctx, sc, "old", Fact{ID: "n", TenantID: "acme", UserID: "alice", Content: "c"}); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("supersede put error", func(t *testing.T) {
		b := newFakeDocBackend()
		// Seed a current fact so Get finds it; then make Put fail on the archive write.
		if err := newDocStore(b, time.Now()).Save(ctx, sc, Fact{ID: "old", TenantID: "acme", UserID: "alice", Content: "c"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		b.putErr = sentinel
		if err := newDocStore(b, time.Now()).Supersede(ctx, sc, "old", Fact{ID: "n", TenantID: "acme", UserID: "alice", Content: "c"}); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("export error", func(t *testing.T) {
		b := newFakeDocBackend()
		b.exportErr = sentinel
		if _, err := newDocStore(b, time.Now()).ExportAll(ctx); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("delete error", func(t *testing.T) {
		b := newFakeDocBackend()
		b.delErr = sentinel
		if err := newDocStore(b, time.Now()).Delete(ctx, sc, []string{"x"}); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("erase scope scan error", func(t *testing.T) {
		b := newFakeDocBackend()
		b.scanErr = sentinel
		if _, err := newDocStore(b, time.Now()).EraseScope(ctx, sc); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("erase scope delete error", func(t *testing.T) {
		b := newFakeDocBackend()
		store := newDocStore(b, time.Now())
		if err := store.Save(ctx, sc, Fact{ID: "f", TenantID: "acme", UserID: "alice", Content: "c"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		b.delErr = sentinel
		if _, err := store.EraseScope(ctx, sc); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
}

// TestDocBackendStore_EraseScope proves EraseScope removes every document
// (current and superseded) in sc's namespace, is a no-op when the namespace is
// empty, leaves other scopes untouched, and is idempotent.
func TestDocBackendStore_EraseScope(t *testing.T) {
	b := newFakeDocBackend()
	store := newDocStore(b, time.Now())
	ctx := context.Background()
	alice := Scope{tenant: "acme", user: "alice"}
	bob := Scope{tenant: "acme", user: "bob"}

	// Empty namespace: no-op, zero rows.
	if n, err := store.EraseScope(ctx, alice); err != nil || n != 0 {
		t.Fatalf("EraseScope on empty namespace = (%d,%v), want (0,nil)", n, err)
	}

	if err := store.Save(ctx, alice, Fact{ID: "a1", TenantID: "acme", UserID: "alice", Content: "first"}); err != nil {
		t.Fatalf("Save a1: %v", err)
	}
	if err := store.Supersede(ctx, alice, "a1", Fact{ID: "a2", TenantID: "acme", UserID: "alice", Content: "second"}); err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	if err := store.Save(ctx, bob, Fact{ID: "b1", TenantID: "acme", UserID: "bob", Content: "bob content"}); err != nil {
		t.Fatalf("Save b1: %v", err)
	}

	n, err := store.EraseScope(ctx, alice)
	if err != nil {
		t.Fatalf("EraseScope: %v", err)
	}
	if n != 2 {
		t.Fatalf("EraseScope removed %d, want 2 (current a2 + superseded a1)", n)
	}

	all, err := store.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(all) != 1 || all[0].ID != "b1" {
		t.Fatalf("expected only bob's fact to remain, got %+v", all)
	}

	// Idempotent: erasing again removes nothing.
	if n, err := store.EraseScope(ctx, alice); err != nil || n != 0 {
		t.Fatalf("second EraseScope = (%d,%v), want (0,nil)", n, err)
	}
}

func TestCombineDocScore(t *testing.T) {
	// Relevance floored at 1e-6, so a zero-relevance hit still ranks by
	// importance*recency rather than collapsing to zero.
	if got := combineDocScore(0, 1, 0); got <= 0 {
		t.Fatalf("floored relevance should keep score positive, got %v", got)
	}
	// Negative age (clock skew) is clamped to zero age -> recency == 1.
	fresh := combineDocScore(1, 1, -time.Hour)
	if fresh < 0.999 || fresh > 1.001 {
		t.Fatalf("negative age should give recency 1, score=%v", fresh)
	}
	// One half-life old halves the recency factor.
	old := combineDocScore(1, 1, recencyHalfLife)
	if old < 0.49 || old > 0.51 {
		t.Fatalf("one half-life should halve recency, score=%v", old)
	}
	// Higher importance ranks higher, all else equal.
	if combineDocScore(1, 0.9, 0) <= combineDocScore(1, 0.1, 0) {
		t.Fatalf("importance should raise score")
	}
}

func TestParseFloatAndMillisToTime(t *testing.T) {
	if parseFloat("") != 0 {
		t.Fatalf("empty importance should be 0")
	}
	if parseFloat("not-a-number") != 0 {
		t.Fatalf("malformed importance should be 0")
	}
	if got := parseFloat("0.75"); got != 0.75 {
		t.Fatalf("parseFloat(0.75)=%v", got)
	}
	if !millisToTime("").IsZero() {
		t.Fatalf("empty millis should be zero time")
	}
	if !millisToTime("garbage").IsZero() {
		t.Fatalf("malformed millis should be zero time")
	}
	ms := int64(1_700_000_000_000)
	if got := millisToTime(strconv.FormatInt(ms, 10)); !got.Equal(time.UnixMilli(ms)) {
		t.Fatalf("millisToTime round-trip wrong: %v", got)
	}
}

func TestDocFromFactAndBack(t *testing.T) {
	sc := Scope{tenant: "acme", user: "alice"}
	created := time.UnixMilli(1_700_000_000_000)
	updated := created.Add(time.Hour)
	f := Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "hello",
		Importance: 0.6, CreatedAt: created, UpdatedAt: updated, SupersededBy: "f2"}
	doc := docFromFact(sc, f)
	if doc.Key != "f1" || doc.Content != "hello" {
		t.Fatalf("docFromFact key/content: %+v", doc)
	}
	if doc.Fields[fkTenant] != "acme" || doc.Fields[fkUser] != "alice" {
		t.Fatalf("docFromFact identity fields missing: %+v", doc.Fields)
	}

	// Scoped reconstruction takes identity from the scope, not the doc fields.
	other := Scope{tenant: "different", user: "someone"}
	got := factFromDoc(other, doc)
	if got.TenantID != "different" || got.UserID != "someone" {
		t.Fatalf("factFromDoc must use scope identity: %+v", got)
	}
	if got.Content != "hello" || got.Importance != 0.6 || got.SupersededBy != "f2" {
		t.Fatalf("factFromDoc fields wrong: %+v", got)
	}
	if !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) {
		t.Fatalf("factFromDoc timestamps wrong: %+v", got)
	}

	// Exported reconstruction takes identity from the doc's own fields.
	exp := factFromExportedDoc(doc)
	if exp.TenantID != "acme" || exp.UserID != "alice" {
		t.Fatalf("factFromExportedDoc must use doc identity: %+v", exp)
	}
}
