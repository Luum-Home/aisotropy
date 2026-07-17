package semantic

import (
	"testing"
)

func clearEmbedEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AISOTROPY_EMBED_BIN", "AISOTROPY_EMBED_MODEL_DIR", "AISOTROPY_EMBED_MODE",
		"AISOTROPY_EMBED_URL", "AISOTROPY_EMBED_MODEL", "AISOTROPY_EMBED_API_KEY",
		"AISOTROPY_EMBED_DIM",
	} {
		t.Setenv(k, "")
	}
}

func TestEmbedderFromEnv_DefaultHash(t *testing.T) {
	clearEmbedEnv(t)
	emb := EmbedderFromEnv()
	if _, ok := emb.(HashEmbedder); !ok {
		t.Fatalf("default should be HashEmbedder, got %T", emb)
	}
	if emb.Dimension() != defaultEmbedDim {
		t.Fatalf("default HashEmbedder dim = %d, want %d", emb.Dimension(), defaultEmbedDim)
	}
}

func TestEmbedderFromEnv_APIEmbedder(t *testing.T) {
	clearEmbedEnv(t)
	t.Setenv("AISOTROPY_EMBED_URL", "http://localhost:11434/v1")
	t.Setenv("AISOTROPY_EMBED_DIM", "384")
	emb := EmbedderFromEnv()
	if _, ok := emb.(*APIEmbedder); !ok {
		t.Fatalf("URL set should select APIEmbedder, got %T", emb)
	}
	if emb.Dimension() != 384 {
		t.Fatalf("APIEmbedder dim = %d, want 384", emb.Dimension())
	}
}

func TestEmbedderFromEnv_LocalBinWithoutModelDirFallsThrough(t *testing.T) {
	clearEmbedEnv(t)
	// AISOTROPY_EMBED_BIN set but no MODEL_DIR: localEmbedderFromEnv returns nil,
	// and with no URL the selection falls through to HashEmbedder.
	t.Setenv("AISOTROPY_EMBED_BIN", "/bin/true")
	emb := EmbedderFromEnv()
	if _, ok := emb.(HashEmbedder); !ok {
		t.Fatalf("misconfigured local embedder should fall through to HashEmbedder, got %T", emb)
	}
}

func TestEmbedderFromEnv_LocalSubprocessSelected(t *testing.T) {
	clearEmbedEnv(t)
	// A fully-configured subprocess local embedder is constructed (no Embed call,
	// so no real binary is executed here). This covers the subprocess selection
	// branch of localEmbedderFromEnv.
	t.Setenv("AISOTROPY_EMBED_BIN", "/bin/true")
	t.Setenv("AISOTROPY_EMBED_MODEL_DIR", t.TempDir())
	t.Setenv("AISOTROPY_EMBED_MODE", "subprocess")
	t.Setenv("AISOTROPY_EMBED_DIM", "384")
	emb := EmbedderFromEnv()
	if _, ok := emb.(*LocalEmbedder); !ok {
		t.Fatalf("subprocess mode should select LocalEmbedder, got %T", emb)
	}
	if emb.Dimension() != 384 {
		t.Fatalf("LocalEmbedder dim = %d, want 384", emb.Dimension())
	}
}

func TestEmbedderFromEnv_LocalDaemonSelected(t *testing.T) {
	clearEmbedEnv(t)
	// Default mode ("" -> daemon). NewDaemonEmbedder validates config only; it
	// does not spawn the process until first Embed.
	t.Setenv("AISOTROPY_EMBED_BIN", "/bin/true")
	t.Setenv("AISOTROPY_EMBED_MODEL_DIR", t.TempDir())
	t.Setenv("AISOTROPY_EMBED_DIM", "512")
	emb := EmbedderFromEnv()
	if _, ok := emb.(*DaemonEmbedder); !ok {
		t.Fatalf("default local mode should select DaemonEmbedder, got %T", emb)
	}
}

func TestEmbedDimFromEnv(t *testing.T) {
	clearEmbedEnv(t)
	// Unset -> default.
	if got := embedDimFromEnv(); got != defaultEmbedDim {
		t.Fatalf("unset dim = %d, want %d", got, defaultEmbedDim)
	}
	// Invalid -> default.
	t.Setenv("AISOTROPY_EMBED_DIM", "-5")
	if got := embedDimFromEnv(); got != defaultEmbedDim {
		t.Fatalf("negative dim = %d, want default %d", got, defaultEmbedDim)
	}
	t.Setenv("AISOTROPY_EMBED_DIM", "not-a-number")
	if got := embedDimFromEnv(); got != defaultEmbedDim {
		t.Fatalf("non-numeric dim = %d, want default %d", got, defaultEmbedDim)
	}
	// Valid -> parsed.
	t.Setenv("AISOTROPY_EMBED_DIM", "768")
	if got := embedDimFromEnv(); got != 768 {
		t.Fatalf("valid dim = %d, want 768", got)
	}
}

func TestNewLocalEmbedder_Validation(t *testing.T) {
	if _, err := NewLocalEmbedder(LocalEmbedderConfig{ModelDir: "m", Dimension: 8}); err == nil {
		t.Fatalf("empty BinPath should error")
	}
	if _, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: "b", Dimension: 8}); err == nil {
		t.Fatalf("empty ModelDir should error")
	}
	if _, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: "b", ModelDir: "m", Dimension: 0}); err == nil {
		t.Fatalf("non-positive Dimension should error")
	}
	e, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: "b", ModelDir: "m", Dimension: 384})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if e.Dimension() != 384 {
		t.Fatalf("Dimension() = %d, want 384", e.Dimension())
	}
}
