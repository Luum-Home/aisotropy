package aisotropy

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/plugintest"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// backend_reexport_test.go proves the P2 "bring your own backend" finisher: the
// sealed-driver payload types are now re-exported on the public façade
// (aisotropy.BackendVector / BackendHit / BackendDoc / BackendDocHit), so an
// EXTERNAL module can declare the FULL VectorBackend / DocBackend method
// signatures against the façade alone — no internal import required.
//
// The two backends below are authored using ONLY the façade aliases in their
// method signatures (aisotropy.VectorBackend, aisotropy.BackendVector,
// aisotropy.BackendHit, and the DocBackend equivalents), which is the compile
// proof. They delegate storage to the in-repo reference backends so the same
// conformance + isolation suites that gate the built-in seams exercise real
// round-trip, CRUD, and cross-tenant-isolation behavior against the alias-typed
// implementations.

// aliasVectorBackend implements the façade VectorBackend alias using only the
// re-exported BackendVector / BackendHit payload aliases in its signatures.
type aliasVectorBackend struct{ inner *plugintest.RefVectorBackend }

// Compile-time proof: the alias-typed implementation satisfies the public seam.
var _ VectorBackend = (*aliasVectorBackend)(nil)

func newAliasVectorBackend() *aliasVectorBackend {
	return &aliasVectorBackend{inner: plugintest.NewRefVectorBackend()}
}

func (b *aliasVectorBackend) Upsert(ctx context.Context, ns string, items []BackendVector) error {
	return b.inner.Upsert(ctx, ns, items)
}

func (b *aliasVectorBackend) Search(ctx context.Context, ns string, query []float32, k int) ([]BackendHit, error) {
	return b.inner.Search(ctx, ns, query, k)
}

func (b *aliasVectorBackend) Delete(ctx context.Context, ns string, ids []string) error {
	return b.inner.Delete(ctx, ns, ids)
}

func (b *aliasVectorBackend) Scan(ctx context.Context, ns string) ([]BackendVector, error) {
	return b.inner.Scan(ctx, ns)
}

// Namespaces implements plugintest.RawVectorInspector, delegating to the
// reference backend so the suite's raw-leak isolation assertion runs.
func (b *aliasVectorBackend) Namespaces() []string { return b.inner.Namespaces() }

func (b *aliasVectorBackend) Close() error { return b.inner.Close() }

// aliasDocBackend implements the façade DocBackend alias using only the
// re-exported BackendDoc / BackendDocHit payload aliases in its signatures.
type aliasDocBackend struct{ inner *plugintest.RefDocBackend }

// Compile-time proof: the alias-typed implementation satisfies the public seam.
var _ DocBackend = (*aliasDocBackend)(nil)

func newAliasDocBackend() *aliasDocBackend {
	return &aliasDocBackend{inner: plugintest.NewRefDocBackend()}
}

func (b *aliasDocBackend) Put(ctx context.Context, ns, key string, doc BackendDoc) error {
	return b.inner.Put(ctx, ns, key, doc)
}

func (b *aliasDocBackend) Get(ctx context.Context, ns, key string) (BackendDoc, bool, error) {
	return b.inner.Get(ctx, ns, key)
}

func (b *aliasDocBackend) Search(ctx context.Context, ns, query string, k int) ([]BackendDocHit, error) {
	return b.inner.Search(ctx, ns, query, k)
}

func (b *aliasDocBackend) Delete(ctx context.Context, ns string, keys []string) error {
	return b.inner.Delete(ctx, ns, keys)
}

func (b *aliasDocBackend) Scan(ctx context.Context, ns string) ([]BackendDoc, error) {
	return b.inner.Scan(ctx, ns)
}

func (b *aliasDocBackend) ExportAll(ctx context.Context) ([]BackendDoc, error) {
	return b.inner.ExportAll(ctx)
}

func (b *aliasDocBackend) Namespaces() []string { return b.inner.Namespaces() }

func (b *aliasDocBackend) Close() error { return b.inner.Close() }

// TestReexportedVectorBackend_ExternalConformance runs the full VectorBackend
// conformance + isolation suite against a backend authored purely from the
// re-exported façade aliases, proving an external module can implement
// WithVectorBackend with no internal import.
func TestReexportedVectorBackend_ExternalConformance(t *testing.T) {
	plugintest.RunVectorBackendSuite(t, func() semantic.VectorBackend {
		return newAliasVectorBackend()
	})
}

// TestReexportedDocBackend_ExternalConformance runs the full DocBackend
// conformance + isolation suite against a backend authored purely from the
// re-exported façade aliases.
func TestReexportedDocBackend_ExternalConformance(t *testing.T) {
	plugintest.RunDocBackendSuite(t, func() facts.DocBackend {
		return newAliasDocBackend()
	})
}
