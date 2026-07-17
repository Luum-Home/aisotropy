// Command wasm-vs-docker benchmarks the WASM pure-compute isolation tier
// (wazero, via internal/sandbox.WASMProvider over internal/wasmtool) head-to-head
// against the container "hands" tier (internal/sandbox.DockerProvider) for the
// SAME trivial work — a JSON->JSON echo tool — so the "add a WASM provider for
// cheap per-call pure-compute isolation" decision rests on repo-grounded numbers,
// not vendor blogs. It is the Phase-2 companion to the footprint harness in
// bench/footprint/ and reuses its discipline: the work is held constant (a
// trivial echo) and every number is emitted with the machine + runtime versions.
//
// It measures, per candidate:
//
//	(a) cold start / first dispatch — "tool ready" to "first result".
//	    WASM  : Load (compile module) + first Dispatch.
//	    Docker: Create (docker run -d) + first Exec.
//	(b) warm per-op throughput — N sequential trivial dispatches: ops/s + p50/p95/p99.
//	    WASM  : fresh-instance-per-call (exactly what production does).
//	    Docker: docker exec into a warm container.
//	(c) host RSS per live instance — process RSS to hold one compiled WASM tool
//	    live in-process, vs one idle container's `docker stats` memory.
//
// HONESTY GATES (mirrors docs/research/wasm-vs-docker-sandbox-2026-07-17.md §6):
//   - The WASM tier serves ONLY the pure-compute (JSON->JSON) subset. It CANNOT
//     run the coding-agent hands: WASI has no fork/exec (that is why Docker stays).
//     This bench compares the two on the pure-compute work they can both do; it is
//     NOT a claim that WASM replaces the container tier.
//   - Docker warm throughput uses a smaller N by default (each `docker exec` forks
//     the docker CLI + containerd, ~tens of ms), and the reduced N is reported
//     next to the number — not hidden.
//   - WASM 3.0 rows (WasmGC / memory64) are UNBENCHMARKABLE on wazero v1.12.0
//     (Core 1.0/2.0 only). They are emitted as an explicit "N/A — not shipped in
//     wazero" line, never faked.
//   - The Docker path skips cleanly when the `docker` CLI or the image is absent.
//
// Usage:
//
//	go run ./bench/wasm-vs-docker                 # default N=10000 wasm / 200 docker
//	go run ./bench/wasm-vs-docker -n 10000 -docker-n 200 -cold-runs 5
//	go run ./bench/wasm-vs-docker -skip-docker    # WASM numbers only
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

func main() {
	var (
		nWasm    = flag.Int("n", 10000, "warm WASM dispatches for the throughput sample")
		nDocker  = flag.Int("docker-n", 200, "warm docker execs for the throughput sample (smaller: each exec forks the CLI)")
		coldRuns = flag.Int("cold-runs", 5, "cold-start samples per candidate (median reported)")
		image    = flag.String("image", "alpine:3.20", "container image for the Docker candidate (must be present locally; no pull is forced)")
		skipDock = flag.Bool("skip-docker", false, "measure the WASM candidate only")
		outPath  = flag.String("out", "bench/wasm-vs-docker/results.txt", "raw results output file")
	)
	flag.Parse()

	ctx := context.Background()
	var b strings.Builder

	emit := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Print(line)
		b.WriteString(line)
	}

	emit("# wasm-vs-docker benchmark — raw results\n")
	emit("# generated: %s | host: %s | go: %s | wazero: %s | docker: %s\n",
		time.Now().UTC().Format(time.RFC3339), runtime.GOOS+"/"+runtime.GOARCH,
		runtime.Version(), wazeroVersion(), dockerVersion())
	emit("# work held constant: a trivial JSON->JSON echo tool in BOTH candidates\n")
	emit("# wasm warm N=%d | docker warm N=%d | cold-start samples=%d\n\n", *nWasm, *nDocker, *coldRuns)

	// ---- Build the constant-work WASM guest (GOOS=wasip1) once. -------------
	guest, err := buildGuest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: build wasm guest: %v\n", err)
		os.Exit(1)
	}
	emit("# wasm guest module size: %.2f MiB (Go/wasip1 reactor — bundles the Go runtime;\n", float64(len(guest))/(1<<20))
	emit("#   a hand-written WAT/Rust guest would be far smaller. The compiled module is\n")
	emit("#   shared across every dispatch; only per-call linear memory is transient.)\n\n")

	// ---- WASM candidate ----------------------------------------------------
	wasm := benchWASM(ctx, guest, *nWasm, *coldRuns)

	// ---- Docker candidate --------------------------------------------------
	var dock *result
	if *skipDock {
		emit("# docker candidate SKIPPED (-skip-docker)\n\n")
	} else if !dockerAvailable() {
		emit("# docker candidate SKIPPED: `docker` CLI not found on PATH\n\n")
	} else if !imageAvailable(*image) {
		emit("# docker candidate SKIPPED: image %q not present locally (no pull forced)\n\n", *image)
	} else {
		dock = benchDocker(ctx, *image, *nDocker, *coldRuns)
	}

	// ---- render ------------------------------------------------------------
	emit("candidate | setup (1-time)  | first call | warm ops/s |      p50 |      p95 |      p99 | host RSS\n")
	emit("          | wasm=compile    | dispatch   | sequential |          |          |          |\n")
	emit("          | docker=run -d   | /exec      | dispatches |          |          |          |\n")
	emit("----------|-----------------|------------|------------|----------|----------|----------|---------\n")
	emit("%s\n", wasm.row())
	if dock != nil {
		emit("%s\n", dock.row())
	} else {
		emit("docker    | (skipped — see note above)\n")
	}
	emit("\n# READING THIS: `setup` is ONE-TIME (a tool is Load()'ed once, then dispatched many\n")
	emit("#   times; a container is `run` once, then exec'd many times) — amortize it over the\n")
	emit("#   call volume. `warm ops/s` + p50/p95/p99 are the per-call steady state, the number\n")
	emit("#   that actually scales. WASM's setup is a heavy Go-runtime module compile; its\n")
	emit("#   per-call dispatch is where it beats docker exec.\n")

	emit("\n# WASM 3.0 (WasmGC / memory64) row: N/A — UNBENCHMARKABLE on wazero v1.12.0.\n")
	emit("#   wazero implements WebAssembly Core 1.0/2.0 only; WasmGC and memory64 are not\n")
	emit("#   shipped (memory64 is open request wazero#2452). Benchmarking them would require\n")
	emit("#   switching runtimes to wasmtime/WasmEdge — which costs CGO, breaking the single\n")
	emit("#   static-binary posture — purely to run the row. Deferred, not faked. Also: Go does\n")
	emit("#   not emit WasmGC (it bundles its own GC), so the 'leaner module' premise never\n")
	emit("#   applies to Go guests regardless of runtime.\n")

	if dock != nil {
		emit("\n# NOTE: docker warm ops/s is over N=%d (not %d) — each `docker exec` forks the\n", *nDocker, *nWasm)
		emit("#   docker CLI + attaches to containerd, so a 10k-exec run is impractically slow.\n")
		emit("#   The reduced N is the honest measurement of the same per-op cost, not a shortcut.\n")
	}
	emit("\n# RSS CAVEAT (both numbers have one — neither is a clean per-instance figure):\n")
	emit("#   - WASM 'in-process' RSS is the harness process delta, DOMINATED by wazero\n")
	emit("#     compiling the 3+ MiB Go-runtime-bearing module (a one-time, shared cost) —\n")
	emit("#     NOT per-call: a dispatch's linear memory is transient and reclaimed on Close.\n")
	emit("#     A hand-written WAT/Rust guest would shrink both the module and this figure.\n")
	emit("#   - Docker 'container' RSS is one container's `docker stats` memory; it EXCLUDES\n")
	emit("#     the dockerd/containerd host-side memory (hundreds of MiB, shared). So the\n")
	emit("#     sub-MiB container figure is not the full host cost of a container either.\n")

	if err := os.WriteFile(*outPath, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: write %s: %v\n", *outPath, err)
	} else {
		fmt.Fprintf(os.Stderr, "\n[bench] raw table written to %s\n", *outPath)
	}
}

// result holds one candidate's measured numbers. setup and firstCall are split
// out because they are amortized very differently: setup (wasm: compile the
// module in Load; docker: `docker run -d`) happens ONCE per tool/container and is
// spread over every subsequent call, whereas firstCall and the warm percentiles
// are per-dispatch costs. Conflating them into one "cold start" number hides that
// a 600ms Go-module compile is paid once, not per call.
type result struct {
	name          string
	setup         time.Duration // median one-time setup (wasm compile / docker create)
	firstCall     time.Duration // median first dispatch/exec right after setup
	opsPerSec     float64
	p50, p95, p99 time.Duration
	rss           string // human string, e.g. "6.1 MiB (in-process)" or "3.9 MiB (container)"
}

func (r *result) row() string {
	return fmt.Sprintf("%-9s | %14s | %10s | %9.0f | %8s | %8s | %8s | %s",
		r.name, fmtDur(r.setup), fmtDur(r.firstCall), r.opsPerSec,
		fmtDur(r.p50), fmtDur(r.p95), fmtDur(r.p99), r.rss)
}

// ---------------------------------------------------------------------------
// WASM candidate
// ---------------------------------------------------------------------------

func benchWASM(ctx context.Context, guest []byte, n, coldRuns int) *result {
	p := sandbox.NewWASMProvider()
	args := json.RawMessage(`{"name":"world","n":21}`)

	// Cold start, split: setup = Load (compile the module, one-time per tool);
	// firstCall = the first Dispatch right after, over fresh loads.
	rssBefore := selfRSSMiB()
	var setups, firsts []time.Duration
	for i := 0; i < coldRuns; i++ {
		t0 := time.Now()
		inst, err := p.Load(ctx, wasmSpec(guest))
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: wasm Load: %v\n", err)
			os.Exit(1)
		}
		setups = append(setups, time.Since(t0))
		t1 := time.Now()
		if _, err := inst.Dispatch(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: wasm first Dispatch: %v\n", err)
			os.Exit(1)
		}
		firsts = append(firsts, time.Since(t1))
		_ = inst.Close(ctx)
	}

	// Warm throughput: one compiled instance, N fresh-instance-per-call dispatches.
	inst, err := p.Load(ctx, wasmSpec(guest))
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: wasm Load (warm): %v\n", err)
		os.Exit(1)
	}
	defer inst.Close(ctx)
	if _, err := inst.Dispatch(ctx, args); err != nil { // warmup
		fmt.Fprintf(os.Stderr, "FATAL: wasm warmup Dispatch: %v\n", err)
		os.Exit(1)
	}
	rssAfter := selfRSSMiB()

	durs := make([]time.Duration, 0, n)
	wallStart := time.Now()
	for i := 0; i < n; i++ {
		s := time.Now()
		if _, err := inst.Dispatch(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: wasm Dispatch %d: %v\n", i, err)
			os.Exit(1)
		}
		durs = append(durs, time.Since(s))
	}
	wall := time.Since(wallStart)

	p50, p95, p99 := percentiles(durs)
	rssDelta := rssAfter - rssBefore
	if rssDelta < 0 {
		rssDelta = 0
	}
	return &result{
		name:      "wasm",
		setup:     median(setups),
		firstCall: median(firsts),
		opsPerSec: float64(n) / wall.Seconds(),
		p50:       p50, p95: p95, p99: p99,
		rss: fmt.Sprintf("%.1f MiB (in-process, shared module)", rssDelta),
	}
}

func wasmSpec(guest []byte) sandbox.ComputeSpec {
	return sandbox.ComputeSpec{
		Name:        "echo",
		Description: "trivial compute echo tool (constant work)",
		Module:      guest,
		AllowWASI:   true,
	}
}

// ---------------------------------------------------------------------------
// Docker candidate
// ---------------------------------------------------------------------------

func benchDocker(ctx context.Context, image string, n, coldRuns int) *result {
	p := sandbox.NewDockerProvider()
	defer p.CleanupAll(ctx) // belt-and-suspenders: remove any container we created
	argv := []string{"echo", "hi"}

	// Cold start, split: setup = Create (`docker run -d`, one-time per container);
	// firstCall = the first Exec right after, over fresh containers. Each is Closed
	// immediately after.
	var setups, firsts []time.Duration
	for i := 0; i < coldRuns; i++ {
		t0 := time.Now()
		sb, err := p.Create(ctx, dockerSpec(image, fmt.Sprintf("wasmbench-cold-%d", i)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: docker Create failed (%v) — skipping docker candidate\n", err)
			return nil
		}
		setups = append(setups, time.Since(t0))
		t1 := time.Now()
		if _, err := sb.Exec(ctx, argv, sandbox.ExecOptions{}); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: docker first Exec failed (%v) — skipping docker candidate\n", err)
			_ = sb.Close(ctx)
			return nil
		}
		firsts = append(firsts, time.Since(t1))
		_ = sb.Close(ctx)
	}

	// Warm throughput + RSS: one long-lived container, N sequential execs.
	sb, err := p.Create(ctx, dockerSpec(image, "wasmbench-warm"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: docker Create (warm) failed (%v)\n", err)
		return nil
	}
	defer sb.Close(ctx)
	if _, err := sb.Exec(ctx, argv, sandbox.ExecOptions{}); err != nil { // warmup
		fmt.Fprintf(os.Stderr, "WARN: docker warmup Exec failed (%v)\n", err)
		return nil
	}
	rss := containerRSSMiB("aisotropy-sbx-wasmbench-warm")

	durs := make([]time.Duration, 0, n)
	wallStart := time.Now()
	for i := 0; i < n; i++ {
		s := time.Now()
		if _, err := sb.Exec(ctx, argv, sandbox.ExecOptions{}); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: docker Exec %d failed (%v) — stopping early at %d\n", i, err, i)
			durs = durs[:i]
			break
		}
		durs = append(durs, time.Since(s))
	}
	wall := time.Since(wallStart)
	if len(durs) == 0 {
		return nil
	}

	p50, p95, p99 := percentiles(durs)
	return &result{
		name:      "docker",
		setup:     median(setups),
		firstCall: median(firsts),
		opsPerSec: float64(len(durs)) / wall.Seconds(),
		p50:       p50, p95: p95, p99: p99,
		rss: rss,
	}
}

func dockerSpec(image, name string) sandbox.Spec {
	return sandbox.Spec{Image: image, Workdir: sandbox.DefaultWorkdir, Name: name}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildGuest compiles the testdata guest to a wasip1 reactor .wasm and returns
// its bytes — the same dependency-free approach the wasmtool/sandbox tests use.
func buildGuest() ([]byte, error) {
	// Resolve the guest dir relative to the invocation CWD. `go run` / the built
	// binary is expected to be launched from the repo root (as the README says).
	guestDir := filepath.FromSlash("bench/wasm-vs-docker/testdata/guest")
	if _, statErr := os.Stat(guestDir); statErr != nil {
		return nil, fmt.Errorf("guest dir %q not found (run from the repo root): %w", guestDir, statErr)
	}
	tmp, err := os.MkdirTemp("", "wasmbench")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "guest.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = guestDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%w\n%s", err, b)
	}
	return os.ReadFile(out)
}

func percentiles(d []time.Duration) (p50, p95, p99 time.Duration) {
	if len(d) == 0 {
		return 0, 0, 0
	}
	s := make([]time.Duration, len(d))
	copy(s, d)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	pick := func(q float64) time.Duration {
		idx := int(q * float64(len(s)-1))
		return s[idx]
	}
	return pick(0.50), pick(0.95), pick(0.99)
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := make([]time.Duration, len(d))
	copy(s, d)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func fmtDur(d time.Duration) string {
	switch {
	case d >= time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	case d >= time.Microsecond:
		return fmt.Sprintf("%.1fµs", float64(d)/float64(time.Microsecond))
	default:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	}
}

// selfRSSMiB reports this process's current resident set size in MiB via `ps`
// (portable across macOS and Linux; `ps -o rss=` returns KiB). Returns 0 on any
// failure — RSS is a best-effort density signal, not a hard gate.
func selfRSSMiB() float64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kib, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return kib / 1024
}

// containerRSSMiB reads one container's memory usage via `docker stats`.
func containerRSSMiB(name string) string {
	out, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", name).Output()
	if err != nil {
		return "N/A"
	}
	// Format: "12.3MiB / 512MiB" — take the first token.
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "/", 2)[0])
	return first + " (container)"
}

func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

func imageAvailable(image string) bool {
	err := exec.Command("docker", "image", "inspect", image).Run()
	return err == nil
}

func dockerVersion() string {
	out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		return "n/a"
	}
	return strings.TrimSpace(string(out))
}

// wazeroVersion reads the pinned wazero module version from the build info, so
// the reported runtime version is the one actually linked, not a hardcoded guess.
func wazeroVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/tetratelabs/wazero" {
			return dep.Version
		}
	}
	return "unknown"
}
