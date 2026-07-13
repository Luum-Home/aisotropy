package semantic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// DaemonEmbedder is the warm-process form of the sovereign local embedder
// (Bet 5, ADR-0023). Where LocalEmbedder spawns a fresh candle subprocess per
// Embed call — paying process spawn + model load every time — DaemonEmbedder
// starts the same `embed` binary ONCE in `--serve` mode (model loaded once) and
// exchanges NDJSON requests/responses over its stdin/stdout pipe. The
// cmd/embed-bench benchmark (ADR-0023, 2026-07-13, e5-small on arm64-mac CPU)
// measured this at p50 22.5 ms/embedding vs 423.6 ms for the per-call
// subprocess — an 18.8× win, because ~95% of the per-call cost is spawn +
// model-load, which the warm process pays only once.
//
// Concurrency: one daemon owns one stdin/stdout pipe and the Rust serve loop is
// single-threaded, so Embed calls are serialized under a mutex. Throughput is
// therefore bounded by a single daemon (~1/inference-latency); a daemon *pool*
// is the documented follow-up for high-concurrency deployments.
//
// Failure model (the load-bearing invariant): any I/O error, a daemon crash, or
// a ctx cancellation mid-call TEARS DOWN the process and clears it, so the next
// Embed respawns a fresh daemon. This is deliberate — a shared pipe cannot be
// left half-drained: if a cancelled call abandoned an in-flight response, the
// NEXT call would read that stale row and every subsequent result would be
// shifted by one. Killing on any fault guarantees the pipe is always in a known
// state (empty + fresh) at the start of every successful exchange. The respawn
// cost (~one cold start) is paid only on the rare fault, not per call.
//
// Shutdown is clean by construction: closing stdin makes the Rust serve loop
// read EOF and exit, so Close() (and parent-process exit, which closes the pipe)
// terminate the daemon without a kill. It is env-gated exactly like
// LocalEmbedder and selected by EmbedderFromEnv when AISOTROPY_EMBED_BIN +
// AISOTROPY_EMBED_MODEL_DIR are set and AISOTROPY_EMBED_MODE is unset or
// "daemon".
type DaemonEmbedder struct {
	binPath  string
	modelDir string
	dim      int

	mu   sync.Mutex  // serializes Embed and guards proc
	proc *daemonProc // nil until first Embed and after any teardown
}

// daemonProc is one running `embed --serve` process and its pipes.
type daemonProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	stderr *ringBuffer // last bytes of the daemon's stderr, for diagnostics
}

// DaemonEmbedderConfig configures a DaemonEmbedder. Fields mirror
// LocalEmbedderConfig so the two are drop-in interchangeable.
type DaemonEmbedderConfig struct {
	// BinPath is the built `embed` binary (crates/embed). Required.
	BinPath string
	// ModelDir holds the pinned model (config.json + model.safetensors +
	// tokenizer.json), passed via AISOTROPY_EMBED_MODEL_DIR. Required.
	ModelDir string
	// Dimension is the model's hidden size and MUST match the semantic Store's
	// configured dimension. Required (positive).
	Dimension int
}

// NewDaemonEmbedder validates cfg and returns a ready DaemonEmbedder. It does
// NOT spawn the daemon — the process starts lazily on the first Embed, so
// constructing one is cheap and the ~one-time model-load cost is paid on first
// use, not at wiring time.
func NewDaemonEmbedder(cfg DaemonEmbedderConfig) (*DaemonEmbedder, error) {
	if cfg.BinPath == "" {
		return nil, errors.New("semantic: daemon embedder requires a non-empty binary path")
	}
	if cfg.ModelDir == "" {
		return nil, errors.New("semantic: daemon embedder requires a non-empty model dir")
	}
	if cfg.Dimension <= 0 {
		return nil, errors.New("semantic: daemon embedder requires a positive vector dimension")
	}
	return &DaemonEmbedder{
		binPath:  cfg.BinPath,
		modelDir: cfg.ModelDir,
		dim:      cfg.Dimension,
	}, nil
}

// Dimension reports the configured vector width.
func (e *DaemonEmbedder) Dimension() int { return e.dim }

// Embed sends texts to the warm daemon and returns one vector per input in
// input order. It serializes calls (one shared pipe) and honors ctx: a
// cancellation kills the daemon (unblocking any in-flight read) and the call
// returns ctx.Err(); the daemon is torn down so the next call respawns a clean
// one. It never returns fewer/misaligned vectors than requested.
func (e *DaemonEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("semantic: daemon embed cancelled before start: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	p, err := e.ensureProcLocked()
	if err != nil {
		return nil, err
	}

	// Watchdog: if ctx is cancelled mid-exchange, kill the daemon so a blocked
	// read unblocks with an error. done stops the watchdog on the happy path.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = p.cmd.Process.Kill()
		case <-done:
		}
	}()

	result, err := e.exchangeLocked(p, texts)
	if err != nil {
		// Any fault poisons the shared pipe — tear down so the next call
		// respawns from a known-clean state (see the type doc's failure model).
		e.killLocked()
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("semantic: daemon embed cancelled: %w", cerr)
		}
		return nil, err
	}
	return result, nil
}

// exchangeLocked writes one request line per text (keyed by input index) then
// reads exactly len(texts) response rows, realigning them into input order by
// id and validating every vector's dimension. Caller holds e.mu.
func (e *DaemonEmbedder) exchangeLocked(p *daemonProc, texts []string) ([][]float32, error) {
	for i, t := range texts {
		line, err := json.Marshal(localEmbedRequest{ID: indexID(i), Text: t})
		if err != nil {
			return nil, fmt.Errorf("semantic: encode daemon request: %w", err)
		}
		if _, err := p.stdin.Write(append(line, '\n')); err != nil {
			return nil, fmt.Errorf("semantic: write daemon request: %w; stderr: %s", err, p.stderr)
		}
	}

	result := make([][]float32, len(texts))
	filled := make([]bool, len(texts))
	for range texts {
		if !p.stdout.Scan() {
			if err := p.stdout.Err(); err != nil {
				return nil, fmt.Errorf("semantic: read daemon response: %w; stderr: %s", err, p.stderr)
			}
			return nil, fmt.Errorf("semantic: daemon closed stdout early; stderr: %s", p.stderr)
		}
		var row localEmbedRow
		if err := json.Unmarshal(p.stdout.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("semantic: decode daemon row: %w", err)
		}
		idx, ok := idFromIndex(row.ID)
		if !ok || idx < 0 || idx >= len(texts) {
			return nil, fmt.Errorf("semantic: daemon row id %q out of range for %d inputs", row.ID, len(texts))
		}
		if filled[idx] {
			return nil, fmt.Errorf("semantic: daemon row duplicate id %q", row.ID)
		}
		if len(row.Embedding) != e.dim {
			return nil, fmt.Errorf("semantic: daemon row %q dim %d != configured %d: %w", row.ID, len(row.Embedding), e.dim, ErrDimensionMismatch)
		}
		result[idx] = row.Embedding
		filled[idx] = true
	}
	return result, nil
}

// ensureProcLocked returns the running daemon, starting one if none is live.
// Caller holds e.mu.
func (e *DaemonEmbedder) ensureProcLocked() (*daemonProc, error) {
	if e.proc != nil {
		return e.proc, nil
	}
	// #nosec G204 -- binPath is an operator-provided env value (AISOTROPY_EMBED_BIN),
	// the same trust level as the bitrate scorer path; not attacker-controlled.
	cmd := exec.Command(e.binPath, "--serve")
	cmd.Env = append(os.Environ(), "AISOTROPY_EMBED_MODEL_DIR="+e.modelDir)
	stderr := &ringBuffer{max: 8 * 1024}
	cmd.Stderr = stderr // exec drains this in its own goroutine.

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("semantic: daemon stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("semantic: daemon stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("semantic: start daemon %q: %w", e.binPath, err)
	}

	sc := bufio.NewScanner(stdout)
	// A 384-float row is a few KB; be generous so a large-dim model never
	// overflows the token buffer (mirrors LocalEmbedder.readResults).
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	e.proc = &daemonProc{cmd: cmd, stdin: stdin, stdout: sc, stderr: stderr}
	return e.proc, nil
}

// killLocked tears down the running daemon (if any) and clears it so the next
// Embed respawns. Caller holds e.mu.
func (e *DaemonEmbedder) killLocked() {
	if e.proc == nil {
		return
	}
	_ = e.proc.stdin.Close() // EOF -> serve loop exits; a kill covers the hang case.
	if e.proc.cmd.Process != nil {
		_ = e.proc.cmd.Process.Kill()
	}
	_ = e.proc.cmd.Wait()
	e.proc = nil
}

// Close shuts the daemon down cleanly (closing stdin makes the serve loop read
// EOF and exit). Safe to call more than once and on a never-started embedder.
func (e *DaemonEmbedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.killLocked()
	return nil
}

// ringBuffer is a tiny bounded io.Writer that keeps only the last `max` bytes —
// enough of the daemon's stderr for a diagnostic without unbounded growth. exec
// writes to it from its own goroutine, so it is mutex-guarded.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
