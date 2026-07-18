package aisotropy

import (
	"context"
	"testing"
)

// TestApp_Close_NoPanicAfterFailedSemanticOpen is the regression for the
// typed-nil trap fixed in openSemanticStore + ensureSemantic: when the semantic
// store fails to open (here: Qdrant configured but the RPC fails under a
// cancelled context), ensureSemantic must memoize a GENUINELY-nil semantic.Store
// — not a (*QdrantStore)(nil) boxed into the interface, which is != nil as an
// interface, slips past App.Close's `if semanticStore != nil` guard, and panics
// on the nil-receiver Close(). After the fix, ensureSemantic returns a true nil
// store and App.Close is safe on a failed-open App.
func TestApp_Close_NoPanicAfterFailedSemanticOpen(t *testing.T) {
	t.Setenv("QDRANT_HOST", "127.0.0.1") // take the Qdrant path, not in-memory
	t.Setenv("QDRANT_PORT", "6334")

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))

	// A cancelled context makes OpenQdrant's first RPC fail immediately and
	// deterministically (no real dial wait).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store, _, err := app.ensureSemantic(ctx)
	if err == nil {
		t.Fatal("ensureSemantic must fail with a cancelled context + Qdrant configured")
	}
	// The core assertion: a TRUE nil interface, not a typed-nil box.
	if store != nil {
		t.Fatalf("ensureSemantic must return a genuinely-nil store on failure, got a non-nil interface %T (typed-nil trap regressed)", store)
	}

	// The regression proper: Close must not panic on a failed-open App.
	if cerr := app.Close(); cerr != nil {
		t.Fatalf("App.Close after a failed semantic open must not error/panic, got: %v", cerr)
	}
}
