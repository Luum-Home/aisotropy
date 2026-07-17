package semantic

import (
	"context"
	"testing"
	"time"
)

func clearQdrantEnv(t *testing.T) {
	t.Helper()
	t.Setenv("QDRANT_HOST", "")
	t.Setenv("QDRANT_PORT", "")
	t.Setenv("QDRANT_API_KEY", "")
	t.Setenv("QDRANT_USE_TLS", "")
	t.Setenv("QDRANT_COLLECTION", "")
}

func TestOpenFromEnv_DefaultInMemory(t *testing.T) {
	clearQdrantEnv(t)
	st, err := OpenFromEnv(context.Background(), 8)
	if err != nil {
		t.Fatalf("OpenFromEnv: %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, ok := st.(*InMemoryStore); !ok {
		t.Fatalf("default should be InMemoryStore, got %T", st)
	}
}

// TestOpenFromEnv_QdrantSelectedButUnreachable proves QDRANT_HOST routes to the
// Qdrant path (not the in-memory default). No Qdrant server runs here, so the
// gRPC collection-probe must fail — which is exactly the branch under test. The
// live gRPC round-trip stays behind the `manual` build tag (qdrant_manual_test.go).
func TestOpenFromEnv_QdrantSelectedButUnreachable(t *testing.T) {
	clearQdrantEnv(t)
	t.Setenv("QDRANT_HOST", "127.0.0.1")
	t.Setenv("QDRANT_PORT", "1") // nothing listening: connection refused
	t.Setenv("QDRANT_COLLECTION", "test_collection")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := OpenFromEnv(ctx, 8)
	if err == nil {
		_ = st.Close()
		t.Fatalf("expected an error reaching Qdrant at a closed port")
	}
}

func TestOpenFromEnv_QdrantInvalidPortFallsBackToDefault(t *testing.T) {
	clearQdrantEnv(t)
	// A non-numeric port must be ignored (default 6334 used) rather than panic —
	// that fallback branch is what we cover. Point the host at a non-routable
	// RFC-5737 address so the dial times out against the *default* port without
	// touching any real Qdrant that may be listening on localhost:6334.
	t.Setenv("QDRANT_HOST", "192.0.2.1")
	t.Setenv("QDRANT_PORT", "not-a-number")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := OpenFromEnv(ctx, 8); err == nil {
		t.Fatalf("expected connection error against default Qdrant port")
	}
}

func TestOpenQdrant_RejectsNonPositiveDim(t *testing.T) {
	if _, err := OpenQdrant(context.Background(), QdrantConfig{Host: "localhost", Dim: 0}); err == nil {
		t.Fatalf("expected error for non-positive dimension")
	}
}
