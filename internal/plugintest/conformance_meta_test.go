package plugintest

// A conformance suite is only worth anything if it REJECTS a backend that
// violates the contract — a suite that passes everything proves nothing. These
// meta-tests run the individual conformance checks against DELIBERATELY BROKEN
// backends and assert the check fails, exercising the suites' own
// violation-detection (the t.Fatalf assertion branches) that a correct backend
// never reaches.
//
// Capture technique: each conformance helper takes a *testing.T and calls
// t.Fatalf (→ runtime.Goexit) on a violation. We run it in a goroutine against a
// throwaway *testing.T and read Failed(), so an expected failure is observed
// without failing THIS test. The leaf helpers use no t.Run, so a bare
// *testing.T is sufficient.

import (
	"context"
	"errors"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// captureFail runs fn against a fresh throwaway T and reports whether fn made it
// fail (via Fatalf/Errorf). Goexit from Fatalf still runs the deferred close.
func captureFail(fn func(*testing.T)) bool {
	sub := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(sub)
	}()
	<-done
	return sub.Failed()
}

// ---- broken vector backends ------------------------------------------------

// errVectorBackend errors on every operation: the crudest violation, which every
// conformance helper that begins with a write must surface.
type errVectorBackend struct{}

func (errVectorBackend) Upsert(context.Context, string, []semantic.BackendVector) error {
	return errors.New("upsert boom")
}
func (errVectorBackend) Search(context.Context, string, []float32, int) ([]semantic.BackendHit, error) {
	return nil, errors.New("search boom")
}
func (errVectorBackend) Delete(context.Context, string, []string) error {
	return errors.New("delete boom")
}
func (errVectorBackend) Scan(context.Context, string) ([]semantic.BackendVector, error) {
	return nil, errors.New("scan boom")
}
func (errVectorBackend) Close() error { return nil }

// blackholeVectorBackend accepts writes but never returns anything: a backend
// that silently drops data. Reads come back empty, so every assertion that
// expects to read back what was written fails.
type blackholeVectorBackend struct{}

func (blackholeVectorBackend) Upsert(context.Context, string, []semantic.BackendVector) error {
	return nil
}
func (blackholeVectorBackend) Search(context.Context, string, []float32, int) ([]semantic.BackendHit, error) {
	return nil, nil
}
func (blackholeVectorBackend) Delete(context.Context, string, []string) error { return nil }
func (blackholeVectorBackend) Scan(context.Context, string) ([]semantic.BackendVector, error) {
	return nil, nil
}
func (blackholeVectorBackend) Close() error { return nil }

// leakyVectorBackend ignores the namespace entirely and stores every item in one
// shared bucket, returning it for ANY namespace query — the exact isolation
// breach the decorator's namespacing is meant to prevent. Only the isolation
// check should catch this.
type leakyVectorBackend struct{ items []semantic.BackendVector }

func (b *leakyVectorBackend) Upsert(_ context.Context, _ string, items []semantic.BackendVector) error {
	b.items = append(b.items, items...)
	return nil
}
func (b *leakyVectorBackend) Search(_ context.Context, _ string, _ []float32, k int) ([]semantic.BackendHit, error) {
	var hits []semantic.BackendHit
	for _, it := range b.items {
		hits = append(hits, semantic.BackendHit{BackendVector: it, Score: 1})
	}
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}
func (b *leakyVectorBackend) Delete(context.Context, string, []string) error { return nil }
func (b *leakyVectorBackend) Scan(_ context.Context, _ string) ([]semantic.BackendVector, error) {
	return b.items, nil
}
func (b *leakyVectorBackend) Close() error { return nil }

func TestVectorSuiteRejectsBrokenBackends(t *testing.T) {
	errFactory := func() semantic.VectorBackend { return errVectorBackend{} }
	blackholeFactory := func() semantic.VectorBackend { return blackholeVectorBackend{} }
	leakyFactory := func() semantic.VectorBackend { return &leakyVectorBackend{} }

	cases := []struct {
		name    string
		check   func(*testing.T, VectorBackendFactory)
		factory VectorBackendFactory
	}{
		// An all-erroring backend fails every helper that starts with a write.
		{"err/RoundTrip", vectorRoundTrip, errFactory},
		{"err/KindFilter", vectorKindFilter, errFactory},
		{"err/Delete", vectorDelete, errFactory},
		{"err/DeleteBySource", vectorDeleteBySource, errFactory},
		{"err/PruneTurns", vectorPruneTurns, errFactory},
		{"err/Isolation", vectorIsolation, errFactory},
		// A data-dropping backend passes writes but fails every read-back.
		{"blackhole/RoundTrip", vectorRoundTrip, blackholeFactory},
		{"blackhole/KindFilter", vectorKindFilter, blackholeFactory},
		{"blackhole/DeleteBySource", vectorDeleteBySource, blackholeFactory},
		{"blackhole/PruneTurns", vectorPruneTurns, blackholeFactory},
		{"blackhole/Isolation", vectorIsolation, blackholeFactory},
		// A namespace-ignoring backend is caught precisely by the isolation proof.
		{"leaky/Isolation", vectorIsolation, leakyFactory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !captureFail(func(sub *testing.T) { tc.check(sub, tc.factory) }) {
				t.Fatalf("conformance check %q PASSED a broken backend — the suite failed to catch a violation", tc.name)
			}
		})
	}
}

// A correct backend that only errors on writes still satisfies the scope- and
// dimension-mismatch checks, because the decorator rejects those BEFORE reaching
// the backend. This proves those two checks are decorator-level guards, not
// backend behavior — so an erroring backend must NOT make them fail.
func TestVectorScopeAndDimChecksAreDecoratorLevel(t *testing.T) {
	errFactory := func() semantic.VectorBackend { return errVectorBackend{} }
	if captureFail(func(sub *testing.T) { vectorScopeMismatch(sub, errFactory) }) {
		t.Fatal("ScopeMismatch check should pass even with an erroring backend (decorator rejects pre-backend)")
	}
	if captureFail(func(sub *testing.T) { vectorDimensionMismatch(sub, errFactory) }) {
		t.Fatal("DimensionMismatch check should pass even with an erroring backend (decorator rejects pre-backend)")
	}
}

// ---- broken doc backends ---------------------------------------------------

type errDocBackend struct{}

func (errDocBackend) Put(context.Context, string, string, facts.BackendDoc) error {
	return errors.New("put boom")
}
func (errDocBackend) Get(context.Context, string, string) (facts.BackendDoc, bool, error) {
	return facts.BackendDoc{}, false, errors.New("get boom")
}
func (errDocBackend) Search(context.Context, string, string, int) ([]facts.BackendDocHit, error) {
	return nil, errors.New("search boom")
}
func (errDocBackend) Delete(context.Context, string, []string) error {
	return errors.New("delete boom")
}
func (errDocBackend) Scan(context.Context, string) ([]facts.BackendDoc, error) {
	return nil, errors.New("scan boom")
}
func (errDocBackend) ExportAll(context.Context) ([]facts.BackendDoc, error) {
	return nil, errors.New("export boom")
}
func (errDocBackend) Close() error { return nil }

// blackholeDocBackend accepts Put but never returns a document.
type blackholeDocBackend struct{}

func (blackholeDocBackend) Put(context.Context, string, string, facts.BackendDoc) error { return nil }
func (blackholeDocBackend) Get(context.Context, string, string) (facts.BackendDoc, bool, error) {
	return facts.BackendDoc{}, false, nil
}
func (blackholeDocBackend) Search(context.Context, string, string, int) ([]facts.BackendDocHit, error) {
	return nil, nil
}
func (blackholeDocBackend) Delete(context.Context, string, []string) error { return nil }
func (blackholeDocBackend) Scan(context.Context, string) ([]facts.BackendDoc, error) {
	return nil, nil
}
func (blackholeDocBackend) ExportAll(context.Context) ([]facts.BackendDoc, error) { return nil, nil }
func (blackholeDocBackend) Close() error                                          { return nil }

// leakyDocBackend ignores namespaces and returns every stored doc for any query.
type leakyDocBackend struct{ docs []facts.BackendDoc }

func (b *leakyDocBackend) Put(_ context.Context, _, key string, doc facts.BackendDoc) error {
	doc.Key = key
	b.docs = append(b.docs, doc)
	return nil
}
func (b *leakyDocBackend) Get(_ context.Context, _, key string) (facts.BackendDoc, bool, error) {
	for _, d := range b.docs {
		if d.Key == key {
			return d, true, nil
		}
	}
	return facts.BackendDoc{}, false, nil
}
func (b *leakyDocBackend) Search(_ context.Context, _, _ string, k int) ([]facts.BackendDocHit, error) {
	var hits []facts.BackendDocHit
	for _, d := range b.docs {
		hits = append(hits, facts.BackendDocHit{BackendDoc: d, Score: 1})
	}
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}
func (b *leakyDocBackend) Delete(context.Context, string, []string) error { return nil }
func (b *leakyDocBackend) Scan(_ context.Context, _ string) ([]facts.BackendDoc, error) {
	return b.docs, nil
}
func (b *leakyDocBackend) ExportAll(_ context.Context) ([]facts.BackendDoc, error) {
	return b.docs, nil
}
func (b *leakyDocBackend) Close() error { return nil }

func TestDocSuiteRejectsBrokenBackends(t *testing.T) {
	errFactory := func() facts.DocBackend { return errDocBackend{} }
	blackholeFactory := func() facts.DocBackend { return blackholeDocBackend{} }
	leakyFactory := func() facts.DocBackend { return &leakyDocBackend{} }

	cases := []struct {
		name    string
		check   func(*testing.T, DocBackendFactory)
		factory DocBackendFactory
	}{
		{"err/RoundTrip", docRoundTrip, errFactory},
		{"err/Supersede", docSupersede, errFactory},
		{"err/Delete", docDelete, errFactory},
		{"err/ExportAll", docExportAll, errFactory},
		{"err/Isolation", docIsolation, errFactory},
		{"blackhole/RoundTrip", docRoundTrip, blackholeFactory},
		{"blackhole/Supersede", docSupersede, blackholeFactory},
		{"blackhole/ExportAll", docExportAll, blackholeFactory},
		{"blackhole/Isolation", docIsolation, blackholeFactory},
		{"leaky/Isolation", docIsolation, leakyFactory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !captureFail(func(sub *testing.T) { tc.check(sub, tc.factory) }) {
				t.Fatalf("conformance check %q PASSED a broken backend — the suite failed to catch a violation", tc.name)
			}
		})
	}
}

// The scope-mismatch check is a decorator-level guard: an erroring backend must
// not make it fail (the decorator rejects the cross-user Save before the backend
// is reached).
func TestDocScopeCheckIsDecoratorLevel(t *testing.T) {
	errFactory := func() facts.DocBackend { return errDocBackend{} }
	if captureFail(func(sub *testing.T) { docScopeMismatch(sub, errFactory) }) {
		t.Fatal("ScopeMismatch check should pass even with an erroring backend (decorator rejects pre-backend)")
	}
}

// The suites' scope constructors fail closed on an invalid (empty) identity —
// the front-door guard every conformance case relies on before it touches a
// backend.
func TestSuiteScopeHelpersRejectEmptyIdentity(t *testing.T) {
	if !captureFail(func(sub *testing.T) { mustScope(sub, "", "") }) {
		t.Fatal("mustScope should fail on an empty (tenant,user)")
	}
	if !captureFail(func(sub *testing.T) { factsScope(sub, "", "") }) {
		t.Fatal("factsScope should fail on an empty (tenant,user)")
	}
}
