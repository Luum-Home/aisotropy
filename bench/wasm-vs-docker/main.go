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
//   - Docker warm throughput uses a smaller N by default (each `docker exec`
//     forks the docker CLI + containerd, ~tens of ms), and the reduced N is
//     reported next to the number — not hidden.
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
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// config is the parsed form of the harness's CLI flags. Split out from flag
// parsing itself (parseFlags) so run/runBench take a plain value — that is
// what makes them testable with arbitrary combinations without touching the
// process-global flag.CommandLine.
type config struct {
	nWasm    int
	nDocker  int
	coldRuns int
	image    string
	skipDock bool
	outPath  string
	rssOnly  string
}

func defaultConfig() config {
	return config{
		nWasm:    10000,
		nDocker:  200,
		coldRuns: 5,
		image:    "alpine:3.20",
		outPath:  "bench/wasm-vs-docker/results.txt",
	}
}

// parseFlags parses args into a config using a fresh FlagSet (never the
// package-global flag.CommandLine), so run is safe to call repeatedly — e.g.
// once per test case — without cross-call flag state leaking.
func parseFlags(args []string) (config, error) {
	cfg := defaultConfig()
	fs := flag.NewFlagSet("wasm-vs-docker", flag.ContinueOnError)
	fs.IntVar(&cfg.nWasm, "n", cfg.nWasm, "warm WASM dispatches for the throughput sample")
	fs.IntVar(&cfg.nDocker, "docker-n", cfg.nDocker, "warm docker execs for the throughput sample (smaller: each exec forks the CLI)")
	fs.IntVar(&cfg.coldRuns, "cold-runs", cfg.coldRuns, "cold-start / compile samples per candidate (median reported)")
	fs.StringVar(&cfg.image, "image", cfg.image, "container image for the Docker candidate (must be present locally; no pull is forced)")
	fs.BoolVar(&cfg.skipDock, "skip-docker", cfg.skipDock, "measure the WASM candidates only")
	fs.StringVar(&cfg.outPath, "out", cfg.outPath, "raw results output file")
	fs.StringVar(&cfg.rssOnly, "rss-only", cfg.rssOnly, "internal: build+Load one guest, print its RSS delta, exit (used to isolate per-guest RSS in a clean process)")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func main() {
	err := run(os.Args[1:], os.Stdout, execRunner{}, sandbox.NewWASMProvider(), sandbox.NewDockerProvider())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run is the whole harness entry point: parse flags, then either serve the
// -rss-only child-process branch or run the full benchmark. It is
// parameterized over stdout, the process-execution seam, and the two backend
// providers so it is testable end to end with fakes — no real
// docker/tinygo/rustc/wasm toolchain required.
func run(args []string, stdout io.Writer, r cmdRunner, wasmProvider sandbox.ComputeProvider, dockerProv dockerProvider) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	ctx := context.Background()

	// Internal RSS-isolation mode: the parent re-execs the harness once per guest
	// with -rss-only so each guest's "hold one compiled tool in-process" RSS is
	// measured from a CLEAN process baseline. Measuring all guests in one process
	// is worthless for RSS — process RSS is monotonic, so whichever guest compiles
	// first absorbs the one-time wazero engine allocation and the rest read ~0.
	if cfg.rssOnly != "" {
		rssOnlyMain(ctx, r, wasmProvider, cfg.rssOnly, stdout)
		return nil
	}

	return runBench(ctx, cfg, stdout, r, wasmProvider, dockerProv)
}

// runBench drives one full measurement pass: build the three guests, bench
// each WASM candidate, measure the CompilationCache effect, bench Docker (if
// available), render the verdict table, and write the raw results file.
func runBench(ctx context.Context, cfg config, stdout io.Writer, r cmdRunner, wasmProvider sandbox.ComputeProvider, dockerProv dockerProvider) error {
	var b strings.Builder

	emit := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		fmt.Fprint(stdout, line)
		b.WriteString(line)
	}

	emit("# wasm-vs-docker benchmark — raw results (Phase-2 lean-guest follow-up)\n")
	emit("# generated: %s | host: %s | go: %s | wazero: %s | docker: %s\n",
		time.Now().UTC().Format(time.RFC3339), runtime.GOOS+"/"+runtime.GOARCH,
		runtime.Version(), wazeroVersion(), dockerVersion(r))
	emit("# toolchains: tinygo=%s | rust=%s\n", tinyGoVersion(r), rustVersion(r))
	emit("# work held constant: a trivial JSON->JSON echo tool in EVERY candidate\n")
	emit("# wasm warm N=%d | docker warm N=%d | cold-start/compile samples=%d\n\n", cfg.nWasm, cfg.nDocker, cfg.coldRuns)

	// ---- Build the three guests (the constant-work echo tool, three toolchains).
	var guests []guestBuild
	for _, name := range guestNames {
		guests = append(guests, buildGuestByName(r, name))
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
		res, err := benchWASM(ctx, wasmProvider, g, cfg.nWasm, cfg.coldRuns)
		if err != nil {
			return err
		}
		res.rss = measureGuestRSS(r, g.name) // isolated, clean-process RSS (see rssOnlyMain)
		wasmResults = append(wasmResults, res)
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
		cold, cached, err := benchCompileCache(ctx, g, cfg.coldRuns)
		if err != nil {
			return err
		}
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
	if cfg.skipDock {
		emit("# docker candidate SKIPPED (-skip-docker)\n\n")
	} else if !dockerAvailable(r) {
		emit("# docker candidate SKIPPED: `docker` CLI not found on PATH\n\n")
	} else if !imageAvailable(r, cfg.image) {
		emit("# docker candidate SKIPPED: image %q not present locally (no pull forced)\n\n", cfg.image)
	} else {
		dock = benchDocker(ctx, r, dockerProv, cfg.image, cfg.nDocker, cfg.coldRuns)
	}

	// ---- verdict table -----------------------------------------------------
	emit("candidate | module     | setup (1-time)  | first call | warm ops/s |      p50 |      p95 |      p99 | host RSS\n")
	emit("          | size       | wasm=compile    | dispatch   | sequential |          |          |          |\n")
	emit("          |            | docker=run -d   | /exec      | dispatches |          |          |          |\n")
	emit("----------|------------|-----------------|------------|------------|----------|----------|----------|---------\n")
	for _, res := range wasmResults {
		emit("%s\n", res.row())
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
		emit("\n# NOTE: docker warm ops/s is over N=%d (not %d) — each `docker exec` forks the\n", cfg.nDocker, cfg.nWasm)
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

	if err := os.WriteFile(cfg.outPath, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: write %s: %v\n", cfg.outPath, err)
	} else {
		fmt.Fprintf(os.Stderr, "\n[bench] raw table written to %s\n", cfg.outPath)
	}
	return nil
}
