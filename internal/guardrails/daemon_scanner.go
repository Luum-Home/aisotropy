package guardrails

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DaemonScanner is the WARM form of the multilingual model scanner (T1). Where
// ModelScanner (ADR-0052) spawns a fresh guardrail-model subprocess per Inspect
// — paying process spawn + mDeBERTa model load (~hundreds of MB) every call —
// DaemonScanner starts the SAME binary ONCE in `--serve` mode (model loaded
// once) and exchanges one NDJSON request/response per Inspect over its
// stdin/stdout pipe. This is the exact shape crates/embed's DaemonEmbedder uses
// for the semantic tier: the cold per-call cost of an inline guardrail on the
// chat path is dominated by spawn + weight load, which a warm process pays only
// once, so this is what makes the ADR-0052 PoC production-usable.
//
// It satisfies the InputScanner seam (injection.go) and is a drop-in for
// ModelScanner: Inspect has the same advisory, additive, FAIL-OPEN contract. Any
// fault — a daemon crash, an I/O error, a timeout, a malformed row — yields the
// zero Verdict with a warning and NEVER blocks the request; the built-in regex
// heuristics still run, so a missed model signal only forfeits extra coverage.
//
// Concurrency + failure model (mirrors DaemonEmbedder, the load-bearing
// invariant): one daemon owns one stdin/stdout pipe and the Rust serve loop is
// single-threaded, so Inspect calls are serialized under a mutex; throughput is
// bounded by a single daemon (~1/inference-latency), and a daemon POOL is the
// documented high-concurrency follow-up. On ANY fault the process is torn down
// and cleared, so the next Inspect respawns a clean daemon — a shared pipe must
// never be left half-drained, or a stale in-flight row would shift every later
// result by one. Because the InputScanner seam is ctx-free (Inspect(string)
// Verdict, fixed by R4), the scanner owns its own per-call deadline internally: a
// stuck worker is killed by a watchdog after Timeout, unblocking the read.
type DaemonScanner struct {
	binPath    string
	modelDir   string
	hypothesis string
	threshold  float64
	timeout    time.Duration

	mu   sync.Mutex         // serializes Inspect and guards proc
	proc *scannerDaemonProc // nil until first Inspect and after any teardown
}

// scannerDaemonProc is one running `guardrail-model --serve` process and its pipes.
type scannerDaemonProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	stderr *boundedBuffer // last bytes of the daemon's stderr, for diagnostics
}

// DaemonScannerConfig configures a DaemonScanner. Fields mirror ModelScannerConfig
// so the two are drop-in interchangeable.
type DaemonScannerConfig struct {
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
	// Timeout bounds one classification (from request write to response read).
	// Non-positive uses defaultScannerTimeout.
	Timeout time.Duration
}

// NewDaemonScanner validates cfg and returns a ready DaemonScanner. It does NOT
// spawn the daemon — the process starts lazily on the first Inspect, so
// constructing one is cheap and the one-time model-load cost is paid on first
// use, not at wiring time.
func NewDaemonScanner(cfg DaemonScannerConfig) (*DaemonScanner, error) {
	if cfg.BinPath == "" {
		return nil, errors.New("guardrails: daemon scanner requires a non-empty binary path")
	}
	if cfg.ModelDir == "" {
		return nil, errors.New("guardrails: daemon scanner requires a non-empty model dir")
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
	return &DaemonScanner{
		binPath:    cfg.BinPath,
		modelDir:   cfg.ModelDir,
		hypothesis: hyp,
		threshold:  th,
		timeout:    to,
	}, nil
}

// Inspect scores text with the warm out-of-process classifier and returns an
// advisory Verdict, satisfying the InputScanner seam. It FAILS OPEN: any error
// (empty input aside) yields the zero Verdict with a logged warning — a model-
// tier fault must never harden into a request failure, and detection is additive
// so a missed model signal only forfeits extra coverage. It never panics.
func (s *DaemonScanner) Inspect(text string) Verdict {
	if text == "" {
		return Verdict{}
	}
	row, err := s.classify(text)
	if err != nil {
		slog.Warn("guardrails: daemon scanner failed open (regex-only path stands)", "error", err)
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

// classify runs one warm request/response exchange against the daemon, honoring
// the scanner's internal timeout. On ANY error it tears the daemon down so the
// next call respawns from a known-clean state (the pipe must never be left
// half-drained).
func (s *DaemonScanner) classify(text string) (scannerRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := s.ensureProcLocked()
	if err != nil {
		return scannerRow{}, err
	}

	// Watchdog: if the deadline fires mid-exchange, kill the daemon so a blocked
	// read unblocks with an error. done stops the watchdog on the happy path.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
		case <-done:
		}
	}()

	row, err := s.exchangeLocked(p, text)
	if err != nil {
		// Any fault poisons the shared pipe — tear down so the next call
		// respawns from a known-clean state (see the type doc's failure model).
		s.killLocked()
		if cerr := ctx.Err(); cerr != nil {
			return scannerRow{}, fmt.Errorf("guardrails: daemon scan timed out: %w", cerr)
		}
		return scannerRow{}, err
	}
	return row, nil
}

// exchangeLocked writes the single request line (fixed id scannerRowID) then
// reads exactly one response row, validating the id. Caller holds s.mu.
func (s *DaemonScanner) exchangeLocked(p *scannerDaemonProc, text string) (scannerRow, error) {
	req := scannerRequest{ID: scannerRowID, Text: text, Hypothesis: s.hypothesis}
	line, err := json.Marshal(req)
	if err != nil {
		return scannerRow{}, fmt.Errorf("guardrails: encode daemon request: %w", err)
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return scannerRow{}, fmt.Errorf("guardrails: write daemon request: %w; stderr: %s", err, p.stderr)
	}

	if !p.stdout.Scan() {
		if err := p.stdout.Err(); err != nil {
			return scannerRow{}, fmt.Errorf("guardrails: read daemon response: %w; stderr: %s", err, p.stderr)
		}
		return scannerRow{}, fmt.Errorf("guardrails: daemon closed stdout early; stderr: %s", p.stderr)
	}
	var row scannerRow
	if err := json.Unmarshal(p.stdout.Bytes(), &row); err != nil {
		return scannerRow{}, fmt.Errorf("guardrails: decode daemon row: %w", err)
	}
	if row.ID != scannerRowID {
		return scannerRow{}, fmt.Errorf("guardrails: daemon row id %q != expected %q", row.ID, scannerRowID)
	}
	return row, nil
}

// ensureProcLocked returns the running daemon, starting one if none is live.
// Caller holds s.mu.
func (s *DaemonScanner) ensureProcLocked() (*scannerDaemonProc, error) {
	if s.proc != nil {
		return s.proc, nil
	}
	// #nosec G204 -- binPath is an operator-provided env value
	// (AISOTROPY_GUARDRAIL_MODEL_BIN), the same trust level as the embedder path;
	// not attacker-controlled.
	cmd := exec.Command(s.binPath, "--serve")
	cmd.Env = append(os.Environ(), "AISOTROPY_GUARDRAIL_MODEL_DIR="+s.modelDir)
	stderr := &boundedBuffer{max: 8 * 1024}
	cmd.Stderr = stderr // exec drains this in its own goroutine.

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("guardrails: daemon stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("guardrails: daemon stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("guardrails: start daemon %q: %w", s.binPath, err)
	}

	sc := bufio.NewScanner(stdout)
	// One result row is a few hundred bytes; be generous so an unusually long
	// model_id or label can never overflow the token buffer.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	s.proc = &scannerDaemonProc{cmd: cmd, stdin: stdin, stdout: sc, stderr: stderr}
	return s.proc, nil
}

// killLocked tears down the running daemon (if any) and clears it so the next
// Inspect respawns. Caller holds s.mu.
func (s *DaemonScanner) killLocked() {
	if s.proc == nil {
		return
	}
	_ = s.proc.stdin.Close() // EOF -> serve loop exits; a kill covers the hang case.
	if s.proc.cmd.Process != nil {
		_ = s.proc.cmd.Process.Kill()
	}
	_ = s.proc.cmd.Wait()
	s.proc = nil
}

// Close shuts the daemon down cleanly (closing stdin makes the serve loop read
// EOF and exit). Safe to call more than once and on a never-started scanner.
func (s *DaemonScanner) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.killLocked()
	return nil
}

// boundedBuffer is a tiny bounded io.Writer that keeps only the last `max` bytes
// — enough of the daemon's stderr for a diagnostic without unbounded growth.
// exec writes to it from its own goroutine, so it is mutex-guarded. (It is the
// guardrails-package twin of semantic.ringBuffer; kept local to avoid a
// cross-internal-package import for a five-line helper.)
type boundedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
