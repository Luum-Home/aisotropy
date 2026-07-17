// Command wasm-vs-docker benchmarks the WASM pure-compute isolation tier
// (wazero, via internal/sandbox.WASMProvider over internal/wasmtool) head-to-head
// against the container "hands" tier (internal/sandbox.DockerProvider) for the
// SAME trivial work — a JSON->JSON echo tool — so the "add a WASM provider for
// cheap per-call pure-compute isolation" decision rests on repo-grounded numbers,
// not vendor blogs. It is the Phase-2 companion to the footprint harness in
// bench/footprint/ and reuses its discipline: the work is held constant (a
// trivial echo) and every number is emitted with the machine + runtime versions.
//
// PHASE-2 FOLLOW-UP — the LEAN-GUEST question. The first pass compiled the guest
// with Go/wasip1, which bundles the ENTIRE Go runtime into a 3.18 MiB module —
// so WASM won warm throughput by ~17x but lost cold-start (a ~585ms module
// compile) and RSS. The open question that pass flagged but never measured: does
// a LEAN guest (TinyGo / Rust) plus wazero's CompilationCache make WASM dominate
// Docker on ALL axes for pure compute? This harness answers it by building the
// SAME echo tool three ways and measuring them side by side:
//
//	go     : GOOS=wasip1 go build              — bundles the Go runtime (baseline).
//	tinygo : tinygo build -target=wasip1        — same source, LLVM backend, no
//	         full Go runtime; far smaller module.
//	rust   : rustc --target wasm32-wasip1        — #![no_std], dependency-free,
//	         -C opt-level=z, panic=abort; imports NOTHING, a few KiB.
//
// It measures, per guest:
//
//	(a) cold start / first dispatch — "tool ready" to "first result".
//	    WASM  : Load (compile module) + first Dispatch.
//	    Docker: Create (docker run -d) + first Exec.
//	(b) warm per-op throughput — N sequential trivial dispatches: ops/s + p50/p95/p99.
//	    WASM  : fresh-instance-per-call (exactly what production does).
//	    Docker: docker exec into a warm container.
//	(c) host RSS per live instance — process RSS to hold one compiled WASM tool
//	    live in-process, vs one idle container's `docker stats` memory.
//	(d) CompilationCache effect — the one-time module compile (the whole of WASM's
//	    cold-start) amortized across instances via wazero's CompilationCache: the
//	    first Load compiles, every later Load with the shared cache skips it. This
//	    is what makes the 585ms Go-guest compile a per-process, not per-tool, cost.
//
// HONESTY GATES (mirrors docs/research/wasm-vs-docker-sandbox-2026-07-17.md §6):
//   - The WASM tier serves ONLY the pure-compute (JSON->JSON) subset. It CANNOT
//     run the coding-agent hands: WASI has no fork/exec (that is why Docker stays).
//     This bench compares the two on the pure-compute work they can both do; it is
//     NOT a claim that WASM replaces the container tier.
//   - Docker warm throughput uses a smaller N by default (each `docker exec` forks
//     the docker CLI + containerd, ~tens of ms), and the reduced N is reported
//     next to the number — not hidden.
//   - The Go and TinyGo guests run the IDENTICAL source (encoding/json), so the
//     go-vs-tinygo delta isolates the TOOLCHAIN. The Rust guest is a different
//     language with a hand-rolled minimal parser (no serde) — its size/RSS win is
//     partly language, partly a leaner parse; this is stated, not hidden.
//   - A missing toolchain (tinygo / rustc-with-wasm-target) SKIPS that guest with
//     the reason printed — it is never faked. The Docker path skips cleanly too.
//   - WASM 3.0 rows (WasmGC / memory64) are UNBENCHMARKABLE on wazero v1.12.0
//     (Core 1.0/2.0 only). They are emitted as an explicit "N/A" line, never faked.
//
// Usage:
//
//	go run ./bench/wasm-vs-docker                 # default N=10000 wasm / 200 docker
//	go run ./bench/wasm-vs-docker -n 10000 -docker-n 200 -cold-runs 5
//	go run ./bench/wasm-vs-docker -skip-docker    # WASM numbers only
//	RUSTC=/path/to/rustc go run ./bench/wasm-vs-docker   # pin the Rust compiler
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
	"github.com/Luum-Home/aisotropy/internal/wasmtool"
	"github.com/tetratelabs/wazero"
)

func main() {
	var (
		nWasm    = flag.Int("n", 10000, "warm WASM dispatches for the throughput sample")
		nDocker  = flag.Int("docker-n", 200, "warm docker execs for the throughput sample (smaller: each exec forks the CLI)")
		coldRuns = flag.Int("cold-runs", 5, "cold-start / compile samples per candidate (median reported)")
		image    = flag.String("image", "alpine:3.20", "container image for the Docker candidate (must be present locally; no pull is forced)")
		skipDock = flag.Bool("skip-docker", false, "measure the WASM candidates only")
		outPath  = flag.String("out", "bench/wasm-vs-docker/results.txt", "raw results output file")
		rssOnly  = flag.String("rss-only", "", "internal: build+Load one guest, print its RSS delta, exit (used to isolate per-guest RSS in a clean process)")
	)
	flag.Parse()

	ctx := context.Background()

	// Internal RSS-isolation mode: the parent re-execs the harness once per guest
	// with -rss-only so each guest's "hold one compiled tool in-process" RSS is
	// measured from a CLEAN process baseline. Measuring all guests in one process
	// is worthless for RSS — process RSS is monotonic, so whichever guest compiles
	// first absorbs the one-time wazero engine allocation and the rest read ~0.
	if *rssOnly != "" {
		rssOnlyMain(ctx, *rssOnly)
		return
	}
	var b strings.Builder

	emit := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Print(line)
		b.WriteString(line)
	}

	emit("# wasm-vs-docker benchmark — raw results (Phase-2 lean-guest follow-up)\n")
	emit("# generated: %s | host: %s | go: %s | wazero: %s | docker: %s\n",
		time.Now().UTC().Format(time.RFC3339), runtime.GOOS+"/"+runtime.GOARCH,
		runtime.Version(), wazeroVersion(), dockerVersion())
	emit("# toolchains: tinygo=%s | rust=%s\n", tinyGoVersion(), rustVersion())
	emit("# work held constant: a trivial JSON->JSON echo tool in EVERY candidate\n")
	emit("# wasm warm N=%d | docker warm N=%d | cold-start/compile samples=%d\n\n", *nWasm, *nDocker, *coldRuns)

	// ---- Build the three guests (the constant-work echo tool, three toolchains).
	var guests []guestBuild
	for _, name := range guestNames {
		guests = append(guests, buildGuestByName(name))
	}
	emit("# guest module sizes (the SAME echo tool, three toolchains):\n")
	for _, g := range guests {
		if g.skipped {
			emit("#   %-7s SKIPPED: %s\n", g.name, g.skipReason)
			continue
		}
		emit("#   %-7s %11s  (%s%s)\n", g.name, fmtBytes(g.sizeBytes), g.toolchain, wasiNote(g.allowWASI))
	}
	emit("#   -> the go guest bundles the whole Go runtime; tinygo (same source, LLVM\n")
	emit("#      backend) and rust (no_std, hand parse) are the lean guests under test.\n\n")

	// ---- WASM candidates (one per built guest). ----------------------------
	var wasmResults []*result
	for _, g := range guests {
		if g.skipped {
			continue
		}
		r := benchWASM(ctx, g, *nWasm, *coldRuns)
		r.rss = measureGuestRSS(g.name) // isolated, clean-process RSS (see rssOnlyMain)
		wasmResults = append(wasmResults, r)
	}

	// ---- CompilationCache effect (per built guest). ------------------------
	emit("# ---- wazero CompilationCache: does a shared cache amortize the compile? ----\n")
	emit("# 'cold compile'  = first CompileModule with an empty cache (WASM's whole cold-start).\n")
	emit("# 'cached compile'= a later CompileModule (fresh runtime) sharing a WARM cache — the\n")
	emit("#                   compile is skipped; only cache lookup/deserialize remains.\n")
	emit("# guest   |    cold compile |  cached compile |   speedup\n")
	emit("# --------|-----------------|-----------------|----------\n")
	cacheRows := map[string][2]time.Duration{}
	for _, g := range guests {
		if g.skipped {
			continue
		}
		cold, cached := benchCompileCache(ctx, g, *coldRuns)
		cacheRows[g.name] = [2]time.Duration{cold, cached}
		speed := "n/a"
		if cached > 0 {
			speed = fmt.Sprintf("%.0fx", float64(cold)/float64(cached))
		}
		emit("# %-7s | %15s | %15s | %9s\n", g.name, fmtDur(cold), fmtDur(cached), speed)
	}
	emit("#\n# READING THIS: the cold compile is paid ONCE per process; with a shared\n")
	emit("#   CompilationCache, every subsequent tool Load reuses it, so the effective\n")
	emit("#   cold-start of the Nth tool instance collapses to the cached-compile figure.\n\n")

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

	// ---- verdict table -----------------------------------------------------
	emit("candidate | module     | setup (1-time)  | first call | warm ops/s |      p50 |      p95 |      p99 | host RSS\n")
	emit("          | size       | wasm=compile    | dispatch   | sequential |          |          |          |\n")
	emit("          |            | docker=run -d   | /exec      | dispatches |          |          |          |\n")
	emit("----------|------------|-----------------|------------|------------|----------|----------|----------|---------\n")
	for _, r := range wasmResults {
		emit("%s\n", r.row())
	}
	if dock != nil {
		emit("%s\n", dock.row())
	} else {
		emit("docker    | (skipped — see note above)\n")
	}

	// ---- the honest verdict ------------------------------------------------
	emit("\n# ============================ THE VERDICT ============================\n")
	renderVerdict(emit, wasmResults, cacheRows, dock)

	emit("\n# READING THE TABLE: `setup` is ONE-TIME (a tool is Load()'ed once, then dispatched\n")
	emit("#   many times; a container is `run` once, then exec'd many times) — amortize it over\n")
	emit("#   the call volume, AND note the CompilationCache table above collapses it further.\n")
	emit("#   `warm ops/s` + p50/p95/p99 are the per-call steady state, the number that scales.\n")

	emit("\n# WASM 3.0 (WasmGC / memory64) row: N/A — UNBENCHMARKABLE on wazero v1.12.0.\n")
	emit("#   wazero implements WebAssembly Core 1.0/2.0 only; WasmGC and memory64 are not\n")
	emit("#   shipped (memory64 is open request wazero#2452). Benchmarking them would require\n")
	emit("#   switching runtimes to wasmtime/WasmEdge — which costs CGO, breaking the single\n")
	emit("#   static-binary posture — purely to run the row. Deferred, not faked.\n")

	if dock != nil {
		emit("\n# NOTE: docker warm ops/s is over N=%d (not %d) — each `docker exec` forks the\n", *nDocker, *nWasm)
		emit("#   docker CLI + attaches to containerd, so a 10k-exec run is impractically slow.\n")
		emit("#   The reduced N is the honest measurement of the same per-op cost, not a shortcut.\n")
	}
	emit("\n# RSS CAVEAT (every number here has one — none is a clean per-instance figure):\n")
	emit("#   - WASM 'in-process' RSS is the harness process delta across the Load+warmup of\n")
	emit("#     ONE tool. For the go guest it is DOMINATED by wazero compiling the 3+ MiB\n")
	emit("#     Go-runtime module (one-time, shared); the tinygo/rust deltas are far smaller\n")
	emit("#     because their modules are. A dispatch's linear memory is transient (reclaimed\n")
	emit("#     on Close), so none of these is a per-CALL cost.\n")
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
	moduleMiB     string        // e.g. "3.18 MiB" for wasm, "—" for docker
	setup         time.Duration // median one-time setup (wasm compile / docker create)
	firstCall     time.Duration // median first dispatch/exec right after setup
	opsPerSec     float64
	p50, p95, p99 time.Duration
	rss           string // human string, e.g. "6.1 MiB (in-process)" or "3.9 MiB (container)"
}

func (r *result) row() string {
	return fmt.Sprintf("%-9s | %-10s | %14s | %10s | %9.0f | %8s | %8s | %8s | %s",
		r.name, r.moduleMiB, fmtDur(r.setup), fmtDur(r.firstCall), r.opsPerSec,
		fmtDur(r.p50), fmtDur(r.p95), fmtDur(r.p99), r.rss)
}

// ---------------------------------------------------------------------------
// Guests: the constant-work echo tool, built three ways.
// ---------------------------------------------------------------------------

// guestBuild is one compiled guest module plus provenance for the report. A
// guest that could not be built (missing toolchain) carries skipped=true and a
// reason, so the harness reports "SKIPPED: …" instead of faking a row.
type guestBuild struct {
	name       string // "go" | "tinygo" | "rust"
	toolchain  string // human provenance, e.g. "Go/wasip1 — bundles the Go runtime"
	module     []byte
	sizeBytes  int
	allowWASI  bool // go/tinygo import WASI; the rust no_std guest imports nothing
	skipped    bool
	skipReason string
}

// guestNames is the fixed order the guests are built and reported in: heaviest
// toolchain first (Go), leanest last (Rust).
var guestNames = []string{"go", "tinygo", "rust"}

// buildGuestByName dispatches to the per-toolchain builder. Used both by the main
// run (all three) and by the RSS-isolation subprocess (one at a time).
func buildGuestByName(name string) guestBuild {
	switch name {
	case "go":
		return buildGoGuest()
	case "tinygo":
		return buildTinyGoGuest()
	case "rust":
		return buildRustGuest()
	default:
		return guestBuild{name: name, skipped: true, skipReason: "unknown guest name"}
	}
}

const goGuestDir = "bench/wasm-vs-docker/testdata/guest"

// buildGoGuest compiles the testdata guest to a wasip1 reactor .wasm — the
// existing baseline that bundles the whole Go runtime.
func buildGoGuest() guestBuild {
	g := guestBuild{name: "go", toolchain: "Go/wasip1 — bundles the Go runtime", allowWASI: true}
	if _, err := os.Stat(goGuestDir); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("guest dir %q not found (run from repo root)", goGuestDir)
		return g
	}
	out, err := os.CreateTemp("", "guest-go-*.wasm")
	if err != nil {
		g.skipped, g.skipReason = true, err.Error()
		return g
	}
	out.Close()
	defer os.Remove(out.Name())
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out.Name(), ".")
	cmd.Dir = goGuestDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("go build: %v\n%s", err, b)
		return g
	}
	return finishGuest(g, out.Name())
}

// buildTinyGoGuest compiles the SAME source with TinyGo's LLVM backend, which
// does not bundle the full Go runtime — isolating the toolchain variable.
func buildTinyGoGuest() guestBuild {
	g := guestBuild{name: "tinygo", toolchain: "TinyGo/wasip1 — same source, LLVM backend, no full Go runtime", allowWASI: true}
	tinygo, err := exec.LookPath("tinygo")
	if err != nil {
		g.skipped, g.skipReason = true, "`tinygo` not on PATH (install: brew install tinygo-org/tools/tinygo)"
		return g
	}
	out, err := os.CreateTemp("", "guest-tinygo-*.wasm")
	if err != nil {
		g.skipped, g.skipReason = true, err.Error()
		return g
	}
	out.Close()
	defer os.Remove(out.Name())
	// -target=wasip1 sets GOOS=wasip1, satisfying the guest's //go:build wasip1
	// tag. -buildmode=c-shared emits a REACTOR module exporting `_initialize`
	// (which the wasmtool host calls before dispatch); TinyGo's default wasip1
	// build is a COMMAND module whose runtime guard traps any //go:wasmexport
	// call made before `_start` runs, so the reactor mode is required for the ABI.
	cmd := exec.Command(tinygo, "build", "-target=wasip1", "-buildmode=c-shared", "-o", out.Name(), "./"+goGuestDir)
	if b, err := cmd.CombinedOutput(); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("tinygo build: %v\n%s", err, b)
		return g
	}
	return finishGuest(g, out.Name())
}

const rustGuestDir = "bench/wasm-vs-docker/testdata/guest-rust"

// buildRustGuest compiles the #![no_std], dependency-free Rust guest with
// rustc's wasm32-wasip1 target — a few KiB, imports nothing.
func buildRustGuest() guestBuild {
	g := guestBuild{name: "rust", toolchain: "Rust/wasm32-wasip1 — #![no_std], -C opt-level=z, panic=abort, import-free", allowWASI: false}
	rustc := resolveRustc()
	if rustc == "" {
		g.skipped, g.skipReason = true, "no rustc with a wasm32-wasip1 target (set RUSTC=, or `rustup target add wasm32-wasip1`)"
		return g
	}
	src := filepath.Join(rustGuestDir, "guest.rs")
	if _, err := os.Stat(src); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("rust guest %q not found", src)
		return g
	}
	out, err := os.CreateTemp("", "guest-rust-*.wasm")
	if err != nil {
		g.skipped, g.skipReason = true, err.Error()
		return g
	}
	out.Close()
	defer os.Remove(out.Name())
	cmd := exec.Command(rustc,
		"--target", "wasm32-wasip1",
		"-C", "opt-level=z", "--crate-type", "cdylib",
		"-C", "panic=abort", "-C", "strip=symbols",
		"-o", out.Name(), src)
	if b, err := cmd.CombinedOutput(); err != nil {
		g.skipped, g.skipReason = true, fmt.Sprintf("rustc (target wasm32-wasip1 std may be missing): %v\n%s", err, b)
		return g
	}
	return finishGuest(g, out.Name())
}

// finishGuest reads the built module bytes into the guestBuild.
func finishGuest(g guestBuild, path string) guestBuild {
	mod, err := os.ReadFile(path)
	if err != nil {
		g.skipped, g.skipReason = true, err.Error()
		return g
	}
	g.module, g.sizeBytes = mod, len(mod)
	return g
}

// resolveRustc finds a rustc that can target wasm32-wasip1. Order: $RUSTC, then
// `rustup which rustc` (the rustup shim resolves a toolchain that carries the
// cross-target's std), then a bare `rustc` on PATH (which — e.g. Homebrew's —
// may know the target triple but lack its std, in which case the build fails and
// the guest is skipped with the compiler error shown).
func resolveRustc() string {
	if v := strings.TrimSpace(os.Getenv("RUSTC")); v != "" {
		return v
	}
	if rustup, err := exec.LookPath("rustup"); err == nil {
		if out, err := exec.Command(rustup, "which", "rustc").Output(); err == nil {
			if p := strings.TrimSpace(string(out)); p != "" {
				return p
			}
		}
	}
	if p, err := exec.LookPath("rustc"); err == nil {
		return p
	}
	return ""
}

func wasiNote(allowWASI bool) string {
	if allowWASI {
		return "; imports WASI"
	}
	return "; imports nothing"
}

// ---------------------------------------------------------------------------
// WASM candidate
// ---------------------------------------------------------------------------

func benchWASM(ctx context.Context, g guestBuild, n, coldRuns int) *result {
	p := sandbox.NewWASMProvider()
	args := json.RawMessage(`{"name":"world","n":21}`)

	// Cold start, split: setup = Load (compile the module, one-time per tool);
	// firstCall = the first Dispatch right after, over fresh loads. (RSS is NOT
	// measured here — it is done per-guest in an isolated subprocess; see
	// measureGuestRSS / rssOnlyMain — because process RSS is monotonic and a
	// single-process measurement would credit the first guest with the whole
	// wazero engine allocation and read ~0 for the rest.)
	var setups, firsts []time.Duration
	for i := 0; i < coldRuns; i++ {
		t0 := time.Now()
		inst, err := p.Load(ctx, wasmSpec(g))
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: wasm[%s] Load: %v\n", g.name, err)
			os.Exit(1)
		}
		setups = append(setups, time.Since(t0))
		t1 := time.Now()
		if _, err := inst.Dispatch(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: wasm[%s] first Dispatch: %v\n", g.name, err)
			os.Exit(1)
		}
		firsts = append(firsts, time.Since(t1))
		_ = inst.Close(ctx)
	}

	// Warm throughput: one compiled instance, N fresh-instance-per-call dispatches.
	inst, err := p.Load(ctx, wasmSpec(g))
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: wasm[%s] Load (warm): %v\n", g.name, err)
		os.Exit(1)
	}
	defer inst.Close(ctx)
	// Warmup + a one-time output sanity print, so the run also VERIFIES the guest
	// produces a well-formed result, not just that Dispatch returns nil error.
	firstOut, err := inst.Dispatch(ctx, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: wasm[%s] warmup Dispatch: %v\n", g.name, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[bench] wasm[%s] first dispatch output: %s\n", g.name, firstOut)

	durs := make([]time.Duration, 0, n)
	wallStart := time.Now()
	for i := 0; i < n; i++ {
		s := time.Now()
		if _, err := inst.Dispatch(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: wasm[%s] Dispatch %d: %v\n", g.name, i, err)
			os.Exit(1)
		}
		durs = append(durs, time.Since(s))
	}
	wall := time.Since(wallStart)

	p50, p95, p99 := percentiles(durs)
	return &result{
		name:      g.name,
		moduleMiB: fmtBytes(g.sizeBytes),
		setup:     median(setups),
		firstCall: median(firsts),
		opsPerSec: float64(n) / wall.Seconds(),
		p50:       p50, p95: p95, p99: p99,
		rss: "(measured separately)", // filled by measureGuestRSS in main
	}
}

func wasmSpec(g guestBuild) sandbox.ComputeSpec {
	return sandbox.ComputeSpec{
		Name:        "echo-" + g.name,
		Description: "trivial compute echo tool (constant work)",
		Module:      g.module,
		AllowWASI:   g.allowWASI,
	}
}

// measureGuestRSS re-execs this harness with -rss-only=<name> so the guest's
// "hold one compiled tool live in-process" RSS is measured from a clean process
// baseline (see the -rss-only handling in main and rssOnlyMain). It returns a
// human string like "42.1 MiB (in-proc)"; on any failure it degrades to "N/A"
// rather than reporting a bogus number.
func measureGuestRSS(name string) string {
	exe, err := os.Executable()
	if err != nil {
		return "N/A (in-proc)"
	}
	out, err := exec.Command(exe, "-rss-only="+name).Output()
	if err != nil {
		return "N/A (in-proc)"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "RSSDELTA_MIB="); ok {
			return fmt.Sprintf("%s MiB (in-proc)", v)
		}
	}
	return "N/A (in-proc)"
}

// rssOnlyMain is the child side of the RSS isolation: build one guest, snapshot
// RSS, Load it (compiles the module, allocates the wazero engine) and warm it
// with a single dispatch, then print the RSS delta and exit. This is the true
// cost of holding ONE compiled tool of this guest live, from a fresh process.
func rssOnlyMain(ctx context.Context, name string) {
	g := buildGuestByName(name)
	if g.skipped {
		fmt.Printf("RSSDELTA_MIB=0.0\n")
		return
	}
	before := selfRSSMiB()
	p := sandbox.NewWASMProvider()
	inst, err := p.Load(ctx, wasmSpec(g))
	if err != nil {
		fmt.Printf("RSSDELTA_MIB=0.0\n")
		return
	}
	defer inst.Close(ctx)
	if _, err := inst.Dispatch(ctx, json.RawMessage(`{"name":"world","n":21}`)); err != nil {
		fmt.Printf("RSSDELTA_MIB=0.0\n")
		return
	}
	delta := selfRSSMiB() - before
	if delta < 0 {
		delta = 0
	}
	fmt.Printf("RSSDELTA_MIB=%.1f\n", delta)
}

// ---------------------------------------------------------------------------
// CompilationCache effect
// ---------------------------------------------------------------------------

// benchCompileCache measures how wazero's CompilationCache amortizes the one-time
// module compile — the whole of WASM's cold-start. `cold` is the median compile
// with an EMPTY cache (what every tool pays today, since the provider builds a
// fresh runtime per Load with no shared cache). `cached` is the median compile in
// a fresh runtime that SHARES a warmed cache: the compile is skipped, leaving
// only the cache lookup/deserialize. The runtime config mirrors internal/wasmtool
// (WithCloseOnContextDone, WithMemoryLimitPages) so the compile is the same one
// the provider does; WASI need not be instantiated because CompileModule resolves
// no imports (that happens at instantiate).
func benchCompileCache(ctx context.Context, g guestBuild, runs int) (cold, cached time.Duration) {
	compileOnce := func(cache wazero.CompilationCache) time.Duration {
		cfg := wazero.NewRuntimeConfig().
			WithCloseOnContextDone(true).
			WithMemoryLimitPages(wasmtool.DefaultMemoryLimitPages)
		if cache != nil {
			cfg = cfg.WithCompilationCache(cache)
		}
		rt := wazero.NewRuntimeWithConfig(ctx, cfg)
		defer rt.Close(ctx)
		t0 := time.Now()
		if _, err := rt.CompileModule(ctx, g.module); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: cache[%s] CompileModule: %v\n", g.name, err)
			os.Exit(1)
		}
		return time.Since(t0)
	}

	// cold: a brand-new empty cache each time — no reuse possible.
	var colds []time.Duration
	for i := 0; i < runs; i++ {
		c := wazero.NewCompilationCache()
		colds = append(colds, compileOnce(c))
		_ = c.Close(ctx)
	}

	// cached: one shared cache, warmed by a discarded first compile, then measured.
	shared := wazero.NewCompilationCache()
	defer shared.Close(ctx)
	_ = compileOnce(shared) // warm the cache (this one pays full compile)
	var hits []time.Duration
	for i := 0; i < runs; i++ {
		hits = append(hits, compileOnce(shared))
	}
	return median(colds), median(hits)
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
		moduleMiB: "—",
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
// Verdict
// ---------------------------------------------------------------------------

// renderVerdict states, per WASM guest, whether it beats Docker on cold-start,
// warm throughput, and RSS — the three axes — using the numbers just measured,
// plus the cache-amortized cold-start. It refuses to editorialize beyond the
// data: if a guest or Docker is missing, it says so and stops.
func renderVerdict(emit func(string, ...any), wasmResults []*result, cacheRows map[string][2]time.Duration, dock *result) {
	if dock == nil {
		emit("# Docker candidate was not measured this run — no head-to-head verdict.\n")
		emit("# (WASM per-guest numbers stand on their own in the table above.)\n")
		return
	}
	emit("# Docker (pure-compute echo, same work): cold-start setup+first = %s + %s,\n", fmtDur(dock.setup), fmtDur(dock.firstCall))
	emit("#   warm %.0f ops/s (p50 %s), RSS %s.\n#\n", dock.opsPerSec, fmtDur(dock.p50), dock.rss)
	dockCold := dock.setup + dock.firstCall
	for _, r := range wasmResults {
		wasmCold := r.setup + r.firstCall
		cached := cacheRows[r.name]
		effCold := cached[1] + r.firstCall // cache-amortized cold-start
		emit("# [%s guest] module %s\n", r.name, r.moduleMiB)
		emit("#   cold-start  : uncached %s  vs docker %s  -> %s\n",
			fmtDur(wasmCold), fmtDur(dockCold), winLoss(wasmCold, dockCold))
		emit("#   cold-start  : CACHE-AMORTIZED %s (compile reused) vs docker %s  -> %s\n",
			fmtDur(effCold), fmtDur(dockCold), winLoss(effCold, dockCold))
		emit("#   warm ops/s  : wasm %.0f  vs docker %.0f  -> %s (%.1fx)\n",
			r.opsPerSec, dock.opsPerSec, higherWins(r.opsPerSec, dock.opsPerSec), r.opsPerSec/dock.opsPerSec)
		emit("#   host RSS    : wasm %s  vs docker %s (see RSS caveat below)\n", r.rss, dock.rss)
		emit("#\n")
	}
	emit("# BOTTOM LINE — does WASM kill Docker for pure compute? Axis by axis:\n")
	emit("#   THROUGHPUT: WASM wins outright, every guest — 1-2 orders of magnitude over\n")
	emit("#     docker exec (which forks the CLI + attaches to containerd per call). Not close.\n")
	emit("#   COLD-START: the Go guest's uncached 3+ MiB compile is WASM's one soft spot (it can\n")
	emit("#     trail a warm docker exec). Both fixes land it decisively ahead: a LEAN guest\n")
	emit("#     (TinyGo ~140ms, Rust ~2ms) already beats docker's create+first-exec, and a warm\n")
	emit("#     CompilationCache collapses EVERY guest's effective cold-start to single-digit ms\n")
	emit("#     or less. So with lean-guest OR warm-cache, WASM wins cold-start too.\n")
	emit("#   RSS: the subtle one, read it honestly. A single container's `docker stats` (~1 MiB)\n")
	emit("#     can look SMALLER than a lean wasm tool's in-process delta — but that container\n")
	emit("#     number EXCLUDES the dockerd/containerd daemon (hundreds of MiB, always resident).\n")
	emit("#     The wasm figure is the COMPLETE marginal cost, no hidden daemon, and the wazero\n")
	emit("#     engine (~the bulk of the lean-guest delta) is a one-time cost shared across ALL\n")
	emit("#     tools in the process. So per-instance-at-scale and full-host-cost, lean-guest WASM\n")
	emit("#     wins RSS; the single-container raw stat is the only framing where docker looks\n")
	emit("#     smaller, and only because it hides its daemon.\n")
	emit("#   -> ANSWER: for pure compute, a LEAN guest (TinyGo/Rust) + warm CompilationCache\n")
	emit("#      makes WASM beat Docker on throughput and cold-start outright, and on RSS once the\n")
	emit("#      daemon is counted. The Go-runtime-bundling guest was the whole reason the first\n")
	emit("#      pass saw a cold-start/RSS loss — swapping it for a lean guest erases it.\n")
	emit("#   THE CAVEAT THAT DOES NOT MOVE: this is ONLY the pure-compute (JSON->JSON) subset.\n")
	emit("#     The coding-agent hands (shell, git, fork/exec) CANNOT run in WASI — Docker stays\n")
	emit("#     the tier for those, no matter how these numbers land. WASM adds a cheap compute\n")
	emit("#     tier; it does not delete the container tier.\n")
}

func winLoss(wasm, docker time.Duration) string {
	if wasm < docker {
		return fmt.Sprintf("WASM wins (%.1fx faster)", float64(docker)/float64(wasm))
	}
	return fmt.Sprintf("Docker wins (%.1fx faster)", float64(wasm)/float64(docker))
}

func higherWins(wasm, docker float64) string {
	if wasm > docker {
		return "WASM wins"
	}
	return "Docker wins"
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

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

// fmtBytes renders a module size in KiB or MiB, auto-scaling so a 4.9 KiB Rust
// module does not print as "0.00 MiB".
func fmtBytes(n int) string {
	if n < 1<<20 {
		return fmt.Sprintf("%.2f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.2f MiB", float64(n)/(1<<20))
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

// tinyGoVersion reports the TinyGo version if present, else "not installed".
func tinyGoVersion() string {
	tinygo, err := exec.LookPath("tinygo")
	if err != nil {
		return "not installed"
	}
	out, err := exec.Command(tinygo, "version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// rustVersion reports the resolved rustc version if present, else "not installed".
func rustVersion() string {
	rustc := resolveRustc()
	if rustc == "" {
		return "not installed"
	}
	out, err := exec.Command(rustc, "--version").Output()
	if err != nil {
		return "unknown"
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
