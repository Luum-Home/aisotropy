package semantic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// LocalEmbedder is the sovereign, network-free Embedder (Bet 5 first brick,
// ADR-0023). Unlike APIEmbedder (ADR-0018), which POSTs tenant text to an
// external /embeddings endpoint, LocalEmbedder execs a local Rust candle
// binary (crates/embed) that loads a pinned MIT BERT-class sentence model,
// mean-pools + L2-normalizes, and returns vectors over the same NDJSON
// subprocess contract as the bitrate scorer (internal/bitrate, ADR-0005). The
// tenant text never leaves the process tree.
//
// It sits on the ADR-0005 boundary exactly like bitrate.Worker: a value type,
// ctx-cancellable via exec.CommandContext, a non-zero exit (how a Rust panic
// surfaces) becomes a returned error. It is env-gated and dormant by default.
// EmbedderFromEnv now selects the sovereign local embedder when
// AISOTROPY_EMBED_BIN + AISOTROPY_EMBED_MODEL_DIR are set; this per-call form is
// chosen with AISOTROPY_EMBED_MODE=subprocess, while the default ("" or
// "daemon") uses the warm DaemonEmbedder — ~18.8× faster (ADR-0023,
// cmd/embed-bench), since this form pays process spawn + model load on every
// call. Prefer the daemon unless per-call panic-isolation is worth the cost.
type LocalEmbedder struct {
	binPath  string
	modelDir string
	dim      int
	// e5Prefixes gates the e5 query:/passage: instruction prefix (ADR-0054).
	// When false the model is treated as non-e5 and EmbedForRole sends no
	// prefix — byte-identical to the pre-ADR-0054 request.
	e5Prefixes bool
}

// LocalEmbedderConfig configures a LocalEmbedder.
type LocalEmbedderConfig struct {
	// BinPath is the built `embed` binary (crates/embed). Required.
	BinPath string
	// ModelDir holds the pinned model (config.json + model.safetensors +
	// tokenizer.json), passed to the binary via AISOTROPY_EMBED_MODEL_DIR.
	// Required.
	ModelDir string
	// Dimension is the model's hidden size (384 for the multilingual-e5-small
	// default — and for bge-small, so the ADR-0049 swap is dimension-preserving)
	// and MUST match the semantic Store's configured dimension, or Upsert/Search
	// fail with ErrDimensionMismatch. Most sentence models expose it as
	// config.json's hidden_size; it is configured here rather than inferred so
	// the Store and Embedder cannot silently drift. Required (positive).
	Dimension int
	// E5Prefixes enables the e5 query:/passage: instruction prefix (ADR-0054)
	// for this embedder's model. Set it only when ModelDir points at an
	// e5-family model (the ADR-0049 default, multilingual-e5-small); a non-e5
	// model MUST leave it false, since a blanket prefix hurts retrieval on a
	// model not trained for it. EmbedderFromEnv sets this via a model-aware
	// autodetect + AISOTROPY_EMBED_E5_PREFIXES override (see embedder_env.go).
	// Optional; default false ⇒ unprefixed (pre-ADR-0054 behavior).
	E5Prefixes bool
}

// NewLocalEmbedder validates cfg and returns a ready LocalEmbedder. BinPath and
// ModelDir must be non-empty and Dimension positive.
func NewLocalEmbedder(cfg LocalEmbedderConfig) (*LocalEmbedder, error) {
	if cfg.BinPath == "" {
		return nil, errors.New("semantic: local embedder requires a non-empty binary path")
	}
	if cfg.ModelDir == "" {
		return nil, errors.New("semantic: local embedder requires a non-empty model dir")
	}
	if cfg.Dimension <= 0 {
		return nil, errors.New("semantic: local embedder requires a positive vector dimension")
	}
	return &LocalEmbedder{
		binPath:    cfg.BinPath,
		modelDir:   cfg.ModelDir,
		dim:        cfg.Dimension,
		e5Prefixes: cfg.E5Prefixes,
	}, nil
}

// Dimension reports the configured vector width.
func (e *LocalEmbedder) Dimension() int { return e.dim }

// prefixFor returns the e5 instruction prefix to send for role, or "" when this
// embedder is not configured for an e5 model (E5Prefixes false) — the
// model-aware guard (ADR-0054) that keeps a blanket prefix off non-e5 models.
func (e *LocalEmbedder) prefixFor(role EmbedRole) string {
	if !e.e5Prefixes {
		return ""
	}
	return role.prefix()
}

// localEmbedRequest is one line of the NDJSON request file the crate reads.
type localEmbedRequest struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Prefix is the OPTIONAL e5 instruction prefix (crate field per ADR-0049,
	// Go wiring per ADR-0054): "query: " for search text, "passage: " for
	// stored documents/facts. `omitempty` keeps an unprefixed request
	// byte-identical to the pre-ADR-0054 form — the crate's `prefix` field is
	// #[serde(default)], so an absent field means no prefix.
	Prefix string `json:"prefix,omitempty"`
}

// localEmbedRow is one line of the NDJSON result file the crate writes.
type localEmbedRow struct {
	ID        string    `json:"id"`
	Embedding []float32 `json:"embedding"`
	Dim       int       `json:"dim"`
	ModelID   string    `json:"model_id"`
}

// Embed writes texts to a temp NDJSON request file, execs the embed binary
// (--input/--output, AISOTROPY_EMBED_MODEL_DIR in the env), and reads back one
// vector per input in input order. It honors ctx cancellation via
// exec.CommandContext (a cancelled request kills the subprocess). A non-zero
// exit carries the binary's stderr. It never silently returns fewer/misaligned
// vectors than requested.
func (e *LocalEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.EmbedForRole(ctx, texts, RoleUnspecified)
}

// EmbedForRole is Embed with an e5 query:/passage: instruction prefix (ADR-0054)
// applied when this embedder is configured for an e5-family model
// (Config.E5Prefixes). When E5Prefixes is false the role is ignored and the
// NDJSON request is byte-identical to the pre-ADR-0054 form (empty prefix,
// omitted by `omitempty`), so a non-e5 model pointed at this binary is
// unaffected. It satisfies RoleEmbedder.
func (e *LocalEmbedder) EmbedForRole(ctx context.Context, texts []string, role EmbedRole) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	inPath, outPath, cleanup, err := e.writeRequest(texts, e.prefixFor(role))
	if err != nil {
		return nil, err
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, e.binPath, "--input", inPath, "--output", outPath)
	cmd.Env = append(os.Environ(), "AISOTROPY_EMBED_MODEL_DIR="+e.modelDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("semantic: local embedder exit: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}

	return e.readResults(outPath, len(texts))
}

// writeRequest creates the temp input/output NDJSON files and writes one
// request line per text, keyed by its input index so results can be realigned.
func (e *LocalEmbedder) writeRequest(texts []string, prefix string) (inPath, outPath string, cleanup func(), err error) {
	inFile, err := os.CreateTemp("", "aisotropy-embed-in-*.ndjson")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("semantic: create embed input: %w", err)
	}
	outFile, err := os.CreateTemp("", "aisotropy-embed-out-*.ndjson")
	if err != nil {
		_ = os.Remove(inFile.Name())
		return "", "", func() {}, fmt.Errorf("semantic: create embed output: %w", err)
	}
	_ = outFile.Close()
	cleanup = func() {
		_ = os.Remove(inFile.Name())
		_ = os.Remove(outFile.Name())
	}

	w := bufio.NewWriter(inFile)
	enc := json.NewEncoder(w)
	for i, t := range texts {
		if err := enc.Encode(localEmbedRequest{ID: indexID(i), Text: t, Prefix: prefix}); err != nil {
			_ = inFile.Close()
			cleanup()
			return "", "", func() {}, fmt.Errorf("semantic: encode embed request: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = inFile.Close()
		cleanup()
		return "", "", func() {}, fmt.Errorf("semantic: flush embed request: %w", err)
	}
	if err := inFile.Close(); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("semantic: close embed request: %w", err)
	}
	return inFile.Name(), outFile.Name(), cleanup, nil
}

// readResults parses the NDJSON output and realigns rows into input order by
// their index-derived id, validating that every position is filled exactly once
// and that each vector matches the configured dimension.
func (e *LocalEmbedder) readResults(outPath string, want int) ([][]float32, error) {
	f, err := os.Open(outPath)
	if err != nil {
		return nil, fmt.Errorf("semantic: open embed output: %w", err)
	}
	defer func() { _ = f.Close() }()

	result := make([][]float32, want)
	filled := make([]bool, want)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row localEmbedRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("semantic: decode embed row: %w", err)
		}
		idx, ok := idFromIndex(row.ID)
		if !ok || idx < 0 || idx >= want {
			return nil, fmt.Errorf("semantic: embed row id %q out of range for %d inputs", row.ID, want)
		}
		if filled[idx] {
			return nil, fmt.Errorf("semantic: embed row duplicate id %q", row.ID)
		}
		if len(row.Embedding) != e.dim {
			return nil, fmt.Errorf("semantic: embed row %q dim %d != configured %d: %w", row.ID, len(row.Embedding), e.dim, ErrDimensionMismatch)
		}
		result[idx] = row.Embedding
		filled[idx] = true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("semantic: scan embed output: %w", err)
	}
	for i, ok := range filled {
		if !ok {
			return nil, fmt.Errorf("semantic: embed output missing vector for input %d", i)
		}
	}
	return result, nil
}

// indexID / idFromIndex map an input position to/from the stable row id used in
// the NDJSON contract ("r<index>"), so realignment survives any row reordering
// by the subprocess.
func indexID(i int) string { return fmt.Sprintf("r%d", i) }

func idFromIndex(id string) (int, bool) {
	if !strings.HasPrefix(id, "r") {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(id, "r%d", &n); err != nil {
		return 0, false
	}
	return n, true
}
