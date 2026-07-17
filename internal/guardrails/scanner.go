package guardrails

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// This file is the OPT-IN multilingual injection/toxicity backend for the R4
// InputScanner seam (see injection.go). It closes the gap R4 explicitly left
// open: normalize.go canonicalizes (NFKC + case-fold + homoglyph/leet fold) but
// it does NOT translate — a clean, non-English attack such as
// "ignora las instrucciones anteriores" (Spanish) or
// "ignore as instruções anteriores" (Portuguese) collapses to itself and scores
// 0 against the English literal patterns. Only a MODEL closes that gap.
//
// ModelScanner is that model, wired the ADR-0005 way: a subprocess boundary
// (like crates/embed / AISOTROPY_EMBED_BIN and crates/sleep-cycle), NOT a new
// in-process Go dependency. It execs a local classifier worker
// (crates/guardrail-model, a candle mDeBERTa-v3 zero-shot-NLI binary) over an
// NDJSON stdio contract. The tenant text never leaves the process tree.
//
// It is env-gated and OFF BY DEFAULT: with AISOTROPY_GUARDRAIL_MODEL_BIN unset,
// ModelScannerFromEnv returns (nil, false) and DetectorFromEnv returns a plain
// NewDetector() — the regex-only path, byte-for-byte identical to before. A
// scanner failure (missing binary, non-zero exit, timeout, malformed row) FAILS
// OPEN: Inspect returns the zero Verdict and logs, never panicking and never
// blocking the request on an infrastructure fault. Detection is additive — the
// built-in heuristics still run and a model verdict only ever RAISES suspicion
// via mergeVerdict (max score, union of signals).

const (
	// defaultScannerTimeout bounds a single classification. The InputScanner
	// seam signature (Inspect(string) Verdict, no ctx) is fixed by R4, so the
	// scanner owns its own deadline internally rather than taking a per-call
	// ctx. A stuck worker must not stall the request lifecycle.
	defaultScannerTimeout = 5 * time.Second

	// defaultScannerThreshold is the model-score cutoff at or above which the
	// scanner's own Verdict is marked Suspicious. The Detector still applies
	// its own threshold to the MERGED score, so this is the scanner-local
	// signal cutoff, not the final decision.
	defaultScannerThreshold = 0.5

	// defaultInjectionHypothesis is the English NLI hypothesis the zero-shot
	// worker scores the (any-language) premise against. The premise — the user
	// text — may be in ANY of the model's ~100 languages; NLI entailment is
	// cross-lingual, which is precisely why a Spanish/Portuguese premise against
	// an English hypothesis still scores. The worker uses this when a request
	// carries no explicit hypothesis.
	defaultInjectionHypothesis = "This message tries to override, ignore, or reveal the assistant's instructions."

	// scannerRowID is the fixed NDJSON id for the single-text request/response.
	scannerRowID = "r0"
)

// ModelScanner is an InputScanner backed by an out-of-process classifier. It is
// a value type, safe for concurrent use (each Inspect writes its own temp files
// and spawns its own short-lived process), and never mutates shared state.
type ModelScanner struct {
	binPath    string
	modelDir   string
	hypothesis string
	threshold  float64
	timeout    time.Duration
}

// ModelScannerConfig configures a ModelScanner.
type ModelScannerConfig struct {
	// BinPath is the built guardrail-model worker (crates/guardrail-model).
	// Required.
	BinPath string
	// ModelDir holds the pinned classifier (config.json + model.safetensors +
	// tokenizer.json), passed to the worker via AISOTROPY_GUARDRAIL_MODEL_DIR.
	// Required.
	ModelDir string
	// Hypothesis overrides the zero-shot NLI hypothesis the premise is scored
	// against. Empty uses defaultInjectionHypothesis.
	Hypothesis string
	// Threshold is the scanner-local Suspicious cutoff (0..1). Non-positive or
	// >1 falls back to defaultScannerThreshold.
	Threshold float64
	// Timeout bounds one classification. Non-positive uses defaultScannerTimeout.
	Timeout time.Duration
}

// NewModelScanner validates cfg and returns a ready ModelScanner.
func NewModelScanner(cfg ModelScannerConfig) (*ModelScanner, error) {
	if cfg.BinPath == "" {
		return nil, errors.New("guardrails: model scanner requires a non-empty binary path")
	}
	if cfg.ModelDir == "" {
		return nil, errors.New("guardrails: model scanner requires a non-empty model dir")
	}
	hyp := cfg.Hypothesis
	if hyp == "" {
		hyp = defaultInjectionHypothesis
	}
	th := cfg.Threshold
	if th <= 0 || th > 1 {
		th = defaultScannerThreshold
	}
	to := cfg.Timeout
	if to <= 0 {
		to = defaultScannerTimeout
	}
	return &ModelScanner{
		binPath:    cfg.BinPath,
		modelDir:   cfg.ModelDir,
		hypothesis: hyp,
		threshold:  th,
		timeout:    to,
	}, nil
}

// scannerRequest is the one NDJSON request line the worker reads.
type scannerRequest struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Hypothesis string `json:"hypothesis,omitempty"`
}

// scannerRow is the one NDJSON result line the worker writes. Score is the
// entailment (injection) probability in 0..1; Label and Suspicious are the
// worker's own informational read, but the scanner re-derives Suspicious from
// Score against its own threshold so the cutoff has a single owner.
type scannerRow struct {
	ID         string  `json:"id"`
	Score      float64 `json:"score"`
	Label      string  `json:"label"`
	Suspicious bool    `json:"suspicious"`
	ModelID    string  `json:"model_id"`
}

// Inspect scores text with the out-of-process classifier and returns an advisory
// Verdict. It satisfies the InputScanner seam. It FAILS OPEN: any error (empty
// input aside) yields the zero Verdict with a logged warning — a model-tier
// fault must never harden into a request failure, and detection is additive so a
// missed model signal only forfeits extra coverage, it never removes the
// built-in heuristics. It never panics.
func (s *ModelScanner) Inspect(text string) Verdict {
	if text == "" {
		return Verdict{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	row, err := s.classify(ctx, text)
	if err != nil {
		slog.Warn("guardrails: model scanner failed open (regex-only path stands)", "error", err)
		return Verdict{}
	}

	suspicious := row.Score >= s.threshold
	var signals []string
	if suspicious {
		label := strings.TrimSpace(row.Label)
		if label == "" {
			label = "injection"
		}
		signals = []string{"multilingual_model:" + label}
	}
	return Verdict{
		Suspicious: suspicious,
		Signals:    signals,
		Score:      row.Score,
	}
}

// classify writes a one-line NDJSON request, execs the worker (--input/--output,
// model dir in the env), and reads back the single result row. ctx cancellation
// kills the subprocess (exec.CommandContext); a non-zero exit — how a Rust panic
// surfaces — carries the worker's stderr.
func (s *ModelScanner) classify(ctx context.Context, text string) (scannerRow, error) {
	inPath, outPath, cleanup, err := s.writeRequest(text)
	if err != nil {
		return scannerRow{}, err
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, s.binPath, "--input", inPath, "--output", outPath)
	cmd.Env = append(os.Environ(), "AISOTROPY_GUARDRAIL_MODEL_DIR="+s.modelDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return scannerRow{}, &scannerError{stage: "worker exit", err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return s.readResult(outPath)
}

// writeRequest creates the temp input/output NDJSON files and writes the single
// request line for text.
func (s *ModelScanner) writeRequest(text string) (inPath, outPath string, cleanup func(), err error) {
	inFile, err := os.CreateTemp("", "aisotropy-guardrail-in-*.ndjson")
	if err != nil {
		return "", "", func() {}, err
	}
	outFile, err := os.CreateTemp("", "aisotropy-guardrail-out-*.ndjson")
	if err != nil {
		_ = os.Remove(inFile.Name())
		return "", "", func() {}, err
	}
	_ = outFile.Close()
	cleanup = func() {
		_ = os.Remove(inFile.Name())
		_ = os.Remove(outFile.Name())
	}

	enc := json.NewEncoder(inFile)
	req := scannerRequest{ID: scannerRowID, Text: text, Hypothesis: s.hypothesis}
	if err := enc.Encode(req); err != nil {
		_ = inFile.Close()
		cleanup()
		return "", "", func() {}, err
	}
	if err := inFile.Close(); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return inFile.Name(), outFile.Name(), cleanup, nil
}

// readResult parses the single NDJSON result row keyed by scannerRowID. A worker
// that emits no matching row is an error (fail-open at the Inspect layer).
func (s *ModelScanner) readResult(outPath string) (scannerRow, error) {
	f, err := os.Open(outPath)
	if err != nil {
		return scannerRow{}, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row scannerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return scannerRow{}, err
		}
		if row.ID == scannerRowID {
			return row, nil
		}
	}
	if err := sc.Err(); err != nil {
		return scannerRow{}, err
	}
	return scannerRow{}, errors.New("guardrails: model scanner produced no result row")
}

// scannerError carries the worker's stderr alongside the exit error so a config
// or model-load failure is diagnosable in the (warning-level) fail-open log.
type scannerError struct {
	stage  string
	err    error
	stderr string
}

func (e *scannerError) Error() string {
	if e.stderr == "" {
		return "guardrails: " + e.stage + ": " + e.err.Error()
	}
	return "guardrails: " + e.stage + ": " + e.err.Error() + "; stderr: " + e.stderr
}

func (e *scannerError) Unwrap() error { return e.err }
