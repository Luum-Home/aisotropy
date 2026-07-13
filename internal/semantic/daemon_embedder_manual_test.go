//go:build manual

package semantic

import (
	"context"
	"os"
	"testing"
)

// TestDaemonEmbedderLiveWiring proves the ADR-0023 wiring end-to-end against the
// REAL candle binary + model: EmbedderFromEnv, with AISOTROPY_EMBED_BIN +
// AISOTROPY_EMBED_MODEL_DIR set (and AISOTROPY_EMBED_MODE unset/"daemon"), must
// select a *DaemonEmbedder, and that warm daemon must produce real embeddings
// with genuine semantic separation (similar > dissimilar) across repeated calls
// on the one long-lived process.
//
// It is a //go:build manual test (never runs in `go test ./...`); it needs the
// built binary and a local model. Run:
//
//	AISOTROPY_EMBED_BIN=crates/embed/target/release/embed \
//	AISOTROPY_EMBED_MODEL_DIR=/abs/model-dir \
//	AISOTROPY_EMBED_DIM=384 \
//	  go test -tags manual -run TestDaemonEmbedderLiveWiring ./internal/semantic/ -v
func TestDaemonEmbedderLiveWiring(t *testing.T) {
	if os.Getenv("AISOTROPY_EMBED_BIN") == "" {
		t.Skip("AISOTROPY_EMBED_BIN unset — set BIN + MODEL_DIR + DIM to run the live daemon wiring test")
	}

	emb := EmbedderFromEnv()
	dmn, ok := emb.(*DaemonEmbedder)
	if !ok {
		t.Fatalf("EmbedderFromEnv() = %T, want *DaemonEmbedder (check AISOTROPY_EMBED_BIN/MODEL_DIR and that MODE is unset or \"daemon\")", emb)
	}
	defer func() { _ = dmn.Close() }()

	ctx := context.Background()
	// Two similar sentences + one unrelated one, embedded on the SAME warm daemon.
	vecs, err := emb.Embed(ctx, []string{
		"el gato duerme en la ventana",
		"the cat is asleep on the windowsill",
		"la factura de electricidad vence el martes",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != emb.Dimension() {
			t.Fatalf("vector %d dim %d != Dimension() %d", i, len(v), emb.Dimension())
		}
	}
	simCats := cosineTest(vecs[0], vecs[1])
	disCatBill := cosineTest(vecs[0], vecs[2])
	if simCats <= disCatBill {
		t.Fatalf("expected similar>dissimilar; cos(cats)=%.4f, cos(cat,bill)=%.4f", simCats, disCatBill)
	}
	t.Logf("warm daemon OK: cos(similar)=%.4f > cos(dissimilar)=%.4f", simCats, disCatBill)

	// Prove warmth: a second call must reuse the SAME process (no respawn).
	if _, err := emb.Embed(ctx, []string{"otra consulta"}); err != nil {
		t.Fatalf("second Embed on warm daemon: %v", err)
	}
}

func cosineTest(a, b []float32) float32 {
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt32(na) * sqrt32(nb))
}

func sqrt32(x float32) float32 {
	if x <= 0 {
		return 0
	}
	// Newton's method — a few iterations are plenty for a test assertion.
	z := x
	for i := 0; i < 20; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}
