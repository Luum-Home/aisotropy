package plugintest

// This file closes specific statement-coverage gaps in doc.go and vector.go
// left by conformance_meta_test.go's err/blackhole/leaky broken backends —
// those all fail on their FIRST operation, so they never reach the SECOND
// or THIRD call in a conformance helper's sequence (e.g. the Search AFTER a
// successful Upsert+re-Upsert, or the Delete/Scan calls inside
// DeleteBySource/PruneTurns). The backends here inject a fault on a chosen
// Nth call to a specific method, or corrupt one specific behavior (silent
// no-op Delete, un-deduped Upsert, a lying Namespaces(), a lossy ExportAll)
// while leaving everything else correct — reaching the t.Fatalf branches a
// first-call-only failure can never trigger.
//
// Every case is verified via the same captureFail technique
// conformance_meta_test.go already uses: run the conformance helper directly
// (not through RunVectorBackendSuite/RunDocBackendSuite) against the broken
// backend and assert it fails.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// ---------------------------------------------------------------------------
// Vector backends
// ---------------------------------------------------------------------------

// vectorFault configures which call (1-indexed) to a given method should
// fail. Zero means "never fail that method".
type vectorFault struct {
	upsertFailAt, searchFailAt, deleteFailAt, scanFailAt int
}

// faultVector wraps a correct RefVectorBackend and injects an error on the
// configured Nth call to a method, letting a conformance helper's LATER
// operations (a second Search, the Scan+Delete inside DeleteBySource/
// PruneTurns) fail instead of only ever the first write.
type faultVector struct {
	*RefVectorBackend
	vectorFault

	mu                                               sync.Mutex
	upsertCalls, searchCalls, deleteCalls, scanCalls int
}

func faultVectorFactory(f vectorFault) VectorBackendFactory {
	return func() semantic.VectorBackend {
		return &faultVector{RefVectorBackend: NewRefVectorBackend(), vectorFault: f}
	}
}

func (b *faultVector) nextCallFails(counter *int, failAt int) bool {
	b.mu.Lock()
	*counter++
	n := *counter
	b.mu.Unlock()
	return failAt > 0 && n == failAt
}

func (b *faultVector) Upsert(ctx context.Context, ns string, items []semantic.BackendVector) error {
	if b.nextCallFails(&b.upsertCalls, b.upsertFailAt) {
		return errors.New("injected upsert fault")
	}
	return b.RefVectorBackend.Upsert(ctx, ns, items)
}

func (b *faultVector) Search(ctx context.Context, ns string, query []float32, k int) ([]semantic.BackendHit, error) {
	if b.nextCallFails(&b.searchCalls, b.searchFailAt) {
		return nil, errors.New("injected search fault")
	}
	return b.RefVectorBackend.Search(ctx, ns, query, k)
}

func (b *faultVector) Delete(ctx context.Context, ns string, ids []string) error {
	if b.nextCallFails(&b.deleteCalls, b.deleteFailAt) {
		return errors.New("injected delete fault")
	}
	return b.RefVectorBackend.Delete(ctx, ns, ids)
}

func (b *faultVector) Scan(ctx context.Context, ns string) ([]semantic.BackendVector, error) {
	if b.nextCallFails(&b.scanCalls, b.scanFailAt) {
		return nil, errors.New("injected scan fault")
	}
	return b.RefVectorBackend.Scan(ctx, ns)
}

// duplicatingVectorBackend never deduplicates Upsert by ID (an append-only
// store), breaking the "idempotent re-Upsert" contract so a conformance
// helper's post-re-Upsert count check can fail.
type duplicatingVectorBackend struct {
	mu   sync.Mutex
	data map[string][]semantic.BackendVector
}

func newDuplicatingVectorBackend() *duplicatingVectorBackend {
	return &duplicatingVectorBackend{data: make(map[string][]semantic.BackendVector)}
}

func (b *duplicatingVectorBackend) Upsert(_ context.Context, ns string, items []semantic.BackendVector) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data[ns] = append(b.data[ns], items...) // bug: no de-dup by ID
	return nil
}

func (b *duplicatingVectorBackend) Search(_ context.Context, ns string, query []float32, k int) ([]semantic.BackendHit, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var hits []semantic.BackendHit
	for _, it := range b.data[ns] {
		hits = append(hits, semantic.BackendHit{BackendVector: it, Score: cosine(query, it.Vector)})
	}
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

func (b *duplicatingVectorBackend) Delete(_ context.Context, ns string, ids []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	kept := b.data[ns][:0]
	for _, it := range b.data[ns] {
		if !drop[it.ID] {
			kept = append(kept, it)
		}
	}
	b.data[ns] = kept
	return nil
}

func (b *duplicatingVectorBackend) Scan(_ context.Context, ns string) ([]semantic.BackendVector, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]semantic.BackendVector(nil), b.data[ns]...), nil
}

func (b *duplicatingVectorBackend) Close() error { return nil }

// corruptContentVectorBackend blanks the "aisotropy.content" payload key on
// every Upsert, so a round-trip Search returns the right count under the
// wrong content — the "round-trip mismatch" branch.
type corruptContentVectorBackend struct{ *RefVectorBackend }

func (b *corruptContentVectorBackend) Upsert(ctx context.Context, ns string, items []semantic.BackendVector) error {
	corrupted := make([]semantic.BackendVector, len(items))
	for i, it := range items {
		cp := it
		cp.Payload = make(map[string]string, len(it.Payload))
		for k, v := range it.Payload {
			cp.Payload[k] = v
		}
		cp.Payload["aisotropy.content"] = "CORRUPTED"
		corrupted[i] = cp
	}
	return b.RefVectorBackend.Upsert(ctx, ns, corrupted)
}

// capTo2VectorBackend silently truncates Search results to at most 2 items
// regardless of k, dropping data the caller never asked to have dropped.
type capTo2VectorBackend struct{ *RefVectorBackend }

func (b *capTo2VectorBackend) Search(ctx context.Context, ns string, query []float32, k int) ([]semantic.BackendHit, error) {
	hits, err := b.RefVectorBackend.Search(ctx, ns, query, k)
	if err != nil {
		return nil, err
	}
	if len(hits) > 2 {
		hits = hits[:2]
	}
	return hits, nil
}

// noDeleteVectorBackend accepts Delete calls but never removes anything —
// the decorator-level DeleteBySource/PruneTurns/Delete all compute the
// RIGHT count via Scan, but the actual eviction silently no-ops.
type noDeleteVectorBackend struct{ *RefVectorBackend }

func (b *noDeleteVectorBackend) Delete(context.Context, string, []string) error { return nil }

// wrongNamespaceCountVector reports a wrong (too-small) namespace count via
// RawVectorInspector while every other operation behaves correctly.
type wrongNamespaceCountVector struct{ *RefVectorBackend }

func (b *wrongNamespaceCountVector) Namespaces() []string {
	all := b.RefVectorBackend.Namespaces()
	if len(all) > 1 {
		return all[:1]
	}
	return all
}

func TestVectorSuiteCoverageGaps(t *testing.T) {
	cases := []struct {
		name    string
		check   func(*testing.T, VectorBackendFactory)
		factory VectorBackendFactory
	}{
		{"RoundTrip/FirstSearchFails", vectorRoundTrip, faultVectorFactory(vectorFault{searchFailAt: 1})},
		{"RoundTrip/SearchAfterReUpsertFails", vectorRoundTrip, faultVectorFactory(vectorFault{searchFailAt: 2})},
		{"RoundTrip/ReUpsertDuplicatesInsteadOfReplacing", vectorRoundTrip, func() semantic.VectorBackend { return newDuplicatingVectorBackend() }},
		{"RoundTrip/CorruptedContent", vectorRoundTrip, func() semantic.VectorBackend {
			return &corruptContentVectorBackend{RefVectorBackend: NewRefVectorBackend()}
		}},
		{"KindFilter/FilteredSearchFails", vectorKindFilter, faultVectorFactory(vectorFault{searchFailAt: 1})},
		{"KindFilter/UnfilteredSearchFails", vectorKindFilter, faultVectorFactory(vectorFault{searchFailAt: 2})},
		{"KindFilter/UnfilteredCountWrong", vectorKindFilter, func() semantic.VectorBackend {
			return &capTo2VectorBackend{RefVectorBackend: NewRefVectorBackend()}
		}},
		{"Delete/DeleteFails", vectorDelete, faultVectorFactory(vectorFault{deleteFailAt: 1})},
		{"Delete/SearchAfterDeleteFails", vectorDelete, faultVectorFactory(vectorFault{searchFailAt: 1})},
		{"Delete/NoOpDeleteLeavesPoints", vectorDelete, func() semantic.VectorBackend {
			return &noDeleteVectorBackend{RefVectorBackend: NewRefVectorBackend()}
		}},
		{"DeleteBySource/ScanFails", vectorDeleteBySource, faultVectorFactory(vectorFault{scanFailAt: 1})},
		{"DeleteBySource/SearchAfterFails", vectorDeleteBySource, faultVectorFactory(vectorFault{searchFailAt: 1})},
		{"DeleteBySource/NoOpDeleteLeavesWrongPoints", vectorDeleteBySource, func() semantic.VectorBackend {
			return &noDeleteVectorBackend{RefVectorBackend: NewRefVectorBackend()}
		}},
		{"PruneTurns/ScanFails", vectorPruneTurns, faultVectorFactory(vectorFault{scanFailAt: 1})},
		{"PruneTurns/SearchAfterFails", vectorPruneTurns, faultVectorFactory(vectorFault{searchFailAt: 1})},
		{"PruneTurns/NoOpDeleteLeavesOldTurn", vectorPruneTurns, func() semantic.VectorBackend {
			return &noDeleteVectorBackend{RefVectorBackend: NewRefVectorBackend()}
		}},
		{"Isolation/ASearchFails", vectorIsolation, faultVectorFactory(vectorFault{searchFailAt: 1})},
		{"Isolation/BSearchFails", vectorIsolation, faultVectorFactory(vectorFault{searchFailAt: 2})},
		{"Isolation/BUpsertFails", vectorIsolation, faultVectorFactory(vectorFault{upsertFailAt: 2})},
		{"Isolation/RawSearchFails", vectorIsolation, faultVectorFactory(vectorFault{searchFailAt: 5})},
		{"Isolation/WrongNamespaceCount", vectorIsolation, func() semantic.VectorBackend {
			return &wrongNamespaceCountVector{RefVectorBackend: NewRefVectorBackend()}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !captureFail(func(sub *testing.T) { tc.check(sub, tc.factory) }) {
				t.Fatalf("conformance check %q PASSED a deliberately broken backend", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Doc backends
// ---------------------------------------------------------------------------

// docFault configures which call (1-indexed) to a given method should fail.
type docFault struct {
	saveFailAt, searchFailAt, deleteFailAt, exportFailAt int
}

// faultDoc wraps a correct RefDocBackend and injects an error on the
// configured Nth call to a method.
type faultDoc struct {
	*RefDocBackend
	docFault

	mu                                              sync.Mutex
	putCalls, searchCalls, deleteCalls, exportCalls int
}

func faultDocFactory(f docFault) DocBackendFactory {
	return func() facts.DocBackend {
		return &faultDoc{RefDocBackend: NewRefDocBackend(), docFault: f}
	}
}

func (b *faultDoc) nextCallFails(counter *int, failAt int) bool {
	b.mu.Lock()
	*counter++
	n := *counter
	b.mu.Unlock()
	return failAt > 0 && n == failAt
}

func (b *faultDoc) Put(ctx context.Context, ns, key string, doc facts.BackendDoc) error {
	if b.nextCallFails(&b.putCalls, b.saveFailAt) {
		return errors.New("injected put fault")
	}
	return b.RefDocBackend.Put(ctx, ns, key, doc)
}

func (b *faultDoc) Search(ctx context.Context, ns, query string, k int) ([]facts.BackendDocHit, error) {
	if b.nextCallFails(&b.searchCalls, b.searchFailAt) {
		return nil, errors.New("injected search fault")
	}
	return b.RefDocBackend.Search(ctx, ns, query, k)
}

func (b *faultDoc) Delete(ctx context.Context, ns string, keys []string) error {
	if b.nextCallFails(&b.deleteCalls, b.deleteFailAt) {
		return errors.New("injected delete fault")
	}
	return b.RefDocBackend.Delete(ctx, ns, keys)
}

func (b *faultDoc) ExportAll(ctx context.Context) ([]facts.BackendDoc, error) {
	if b.nextCallFails(&b.exportCalls, b.exportFailAt) {
		return nil, errors.New("injected export fault")
	}
	return b.RefDocBackend.ExportAll(ctx)
}

// noDeleteDocBackend accepts Delete calls but never removes anything.
type noDeleteDocBackend struct{ *RefDocBackend }

func (b *noDeleteDocBackend) Delete(context.Context, string, []string) error { return nil }

// alwaysFoundGetDocBackend fabricates a present-but-empty doc for a missing
// key instead of correctly reporting absence, breaking Supersede's
// unknown-fact fail-closed guard (which relies on Get's ok=false).
type alwaysFoundGetDocBackend struct{ *RefDocBackend }

func (b *alwaysFoundGetDocBackend) Get(ctx context.Context, ns, key string) (facts.BackendDoc, bool, error) {
	d, ok, err := b.RefDocBackend.Get(ctx, ns, key)
	if err != nil || ok {
		return d, ok, err
	}
	return facts.BackendDoc{Key: key, Fields: map[string]string{}}, true, nil
}

// fieldDroppingDocBackend silently drops all supplementary Fields on Put,
// losing the supersede marker (aisotropy.superseded_by) Supersede's archive
// step relies on, so a superseded fact leaks back into Search results.
type fieldDroppingDocBackend struct{ *RefDocBackend }

func (b *fieldDroppingDocBackend) Put(ctx context.Context, ns, key string, doc facts.BackendDoc) error {
	stripped := doc
	// A non-nil empty map: the caller (facts.scopedDocStore.Supersede) writes
	// directly into the map returned by a later Get, so it must never be nil.
	stripped.Fields = map[string]string{}
	return b.RefDocBackend.Put(ctx, ns, key, stripped)
}

// exportAllFaultDoc corrupts ExportAll's results in one of three ways,
// configured per instance: dropping a specific key, corrupting a specific
// key's tenant/user identity fields, or stripping the supersede marker from
// every doc (undoing ExportAll's "current only" filtering upstream).
type exportAllFaultDoc struct {
	*RefDocBackend
	skipKey            string
	corruptIdentityKey string
	stripSupersededBy  bool
}

func (b *exportAllFaultDoc) ExportAll(ctx context.Context) ([]facts.BackendDoc, error) {
	docs, err := b.RefDocBackend.ExportAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]facts.BackendDoc, 0, len(docs))
	for _, d := range docs {
		if b.skipKey != "" && d.Key == b.skipKey {
			continue
		}
		if b.corruptIdentityKey != "" && d.Key == b.corruptIdentityKey {
			cp := d
			cp.Fields = make(map[string]string, len(d.Fields))
			for k, v := range d.Fields {
				cp.Fields[k] = v
			}
			cp.Fields["aisotropy.tenant"] = "WRONG-TENANT"
			cp.Fields["aisotropy.user"] = "WRONG-USER"
			d = cp
		}
		if b.stripSupersededBy {
			cp := d
			cp.Fields = make(map[string]string, len(d.Fields))
			for k, v := range d.Fields {
				if k == "aisotropy.superseded_by" {
					continue
				}
				cp.Fields[k] = v
			}
			d = cp
		}
		out = append(out, d)
	}
	return out, nil
}

// wrongNamespaceCountDocBackend reports a wrong (too-small) namespace count
// via RawDocInspector while every other operation behaves correctly.
type wrongNamespaceCountDocBackend struct{ *RefDocBackend }

func (b *wrongNamespaceCountDocBackend) Namespaces() []string {
	all := b.RefDocBackend.Namespaces()
	if len(all) > 1 {
		return all[:1]
	}
	return all
}

func TestDocSuiteCoverageGaps(t *testing.T) {
	cases := []struct {
		name    string
		check   func(*testing.T, DocBackendFactory)
		factory DocBackendFactory
	}{
		{"RoundTrip/SearchFails", docRoundTrip, faultDocFactory(docFault{searchFailAt: 1})},
		{"Supersede/UnknownFactSucceedsWhenItShouldFailClosed", docSupersede, func() facts.DocBackend {
			return &alwaysFoundGetDocBackend{RefDocBackend: NewRefDocBackend()}
		}},
		{"Supersede/SupersededFactStillSearchable", docSupersede, func() facts.DocBackend {
			return &fieldDroppingDocBackend{RefDocBackend: NewRefDocBackend()}
		}},
		{"Delete/DeleteFails", docDelete, faultDocFactory(docFault{deleteFailAt: 1})},
		{"Delete/NoOpDeleteLeavesFacts", docDelete, func() facts.DocBackend {
			return &noDeleteDocBackend{RefDocBackend: NewRefDocBackend()}
		}},
		{"ExportAll/SecondSaveFails", docExportAll, faultDocFactory(docFault{saveFailAt: 2})},
		{"ExportAll/ThirdSaveFails", docExportAll, faultDocFactory(docFault{saveFailAt: 3})},
		{"ExportAll/ExportFails", docExportAll, faultDocFactory(docFault{exportFailAt: 1})},
		{"ExportAll/MissingA1", docExportAll, func() facts.DocBackend {
			return &exportAllFaultDoc{RefDocBackend: NewRefDocBackend(), skipKey: "a1"}
		}},
		{"ExportAll/MissingB1", docExportAll, func() facts.DocBackend {
			return &exportAllFaultDoc{RefDocBackend: NewRefDocBackend(), skipKey: "b1"}
		}},
		{"ExportAll/WrongIdentity", docExportAll, func() facts.DocBackend {
			return &exportAllFaultDoc{RefDocBackend: NewRefDocBackend(), corruptIdentityKey: "a1"}
		}},
		{"ExportAll/SupersededFactLeaks", docExportAll, func() facts.DocBackend {
			return &exportAllFaultDoc{RefDocBackend: NewRefDocBackend(), stripSupersededBy: true}
		}},
		{"ExportAll/MissingSupersedingFact", docExportAll, func() facts.DocBackend {
			return &exportAllFaultDoc{RefDocBackend: NewRefDocBackend(), skipKey: "a3"}
		}},
		{"Isolation/ASearchFails", docIsolation, faultDocFactory(docFault{searchFailAt: 1})},
		{"Isolation/BSearchFails", docIsolation, faultDocFactory(docFault{searchFailAt: 2})},
		{"Isolation/BSaveFails", docIsolation, faultDocFactory(docFault{saveFailAt: 2})},
		{"Isolation/WrongNamespaceCount", docIsolation, func() facts.DocBackend {
			return &wrongNamespaceCountDocBackend{RefDocBackend: NewRefDocBackend()}
		}},
		{"Isolation/RawSearchFails", docIsolation, faultDocFactory(docFault{searchFailAt: 5})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !captureFail(func(sub *testing.T) { tc.check(sub, tc.factory) }) {
				t.Fatalf("conformance check %q PASSED a deliberately broken backend", tc.name)
			}
		})
	}
}

// TestRefDocBackendSearchTieBreaksByKey covers the RefDocBackend.Search sort
// comparator's tie-break branch (equal relevance scores fall back to
// ascending Key), never exercised by the existing reference-backend tests
// which never seed two docs with identical token overlap.
func TestRefDocBackendSearchTieBreaksByKey(t *testing.T) {
	ctx := context.Background()
	b := NewRefDocBackend()
	if err := b.Put(ctx, "ns", "zzz", facts.BackendDoc{Key: "zzz", Content: "shared term"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := b.Put(ctx, "ns", "aaa", facts.BackendDoc{Key: "aaa", Content: "shared term"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	hits, err := b.Search(ctx, "ns", "shared term", 4)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 tied hits, got %d", len(hits))
	}
	if hits[0].Key != "aaa" || hits[1].Key != "zzz" {
		t.Errorf("tied scores should tie-break by ascending Key, got order [%s, %s]", hits[0].Key, hits[1].Key)
	}
}
