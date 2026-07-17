package semantic

import (
	"context"
	"testing"
)

func TestHashEmbedder_Dimension(t *testing.T) {
	// Zero-value HashEmbedder (Dim==0) falls back to the default width.
	if got := (HashEmbedder{}).Dimension(); got != defaultEmbedDim {
		t.Fatalf("zero-value Dimension = %d, want %d", got, defaultEmbedDim)
	}
	// Explicit dimension is honored.
	if got := NewHashEmbedder(64).Dimension(); got != 64 {
		t.Fatalf("explicit Dimension = %d, want 64", got)
	}
}

func TestHashEmbedder_EmbedDeterministicAndNormalized(t *testing.T) {
	h := NewHashEmbedder(32)
	vs1, err := h.Embed(context.Background(), []string{"the quick brown fox", ""})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vs1) != 2 || len(vs1[0]) != 32 {
		t.Fatalf("unexpected shape: %d vecs", len(vs1))
	}
	// Determinism: same input -> identical vector.
	vs2, _ := h.Embed(context.Background(), []string{"the quick brown fox"})
	for i := range vs1[0] {
		if vs1[0][i] != vs2[0][i] {
			t.Fatalf("HashEmbedder is not deterministic at index %d", i)
		}
	}
	// A non-empty text is L2-normalized (unit length within float tolerance).
	var norm float64
	for _, x := range vs1[0] {
		norm += float64(x) * float64(x)
	}
	if norm < 0.99 || norm > 1.01 {
		t.Fatalf("vector not unit length: norm^2=%v", norm)
	}
	// The empty string embeds to the zero vector (norm 0).
	var zn float64
	for _, x := range vs1[1] {
		zn += float64(x) * float64(x)
	}
	if zn != 0 {
		t.Fatalf("empty string should embed to zero vector, norm^2=%v", zn)
	}
}

func TestDaemonEmbedder_Dimension(t *testing.T) {
	e, err := NewDaemonEmbedder(DaemonEmbedderConfig{BinPath: "/bin/true", ModelDir: t.TempDir(), Dimension: 256})
	if err != nil {
		t.Fatalf("NewDaemonEmbedder: %v", err)
	}
	if e.Dimension() != 256 {
		t.Fatalf("Dimension() = %d, want 256", e.Dimension())
	}
}
