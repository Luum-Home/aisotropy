package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/sandbox"
	"github.com/Luum-Home/aisotropy/internal/wasmtool"
	"github.com/tetratelabs/wazero"
)

// ---------------------------------------------------------------------------
// WASM candidate
// ---------------------------------------------------------------------------

// benchWASM measures one guest's cold-start (setup=Load, firstCall=first
// Dispatch) and warm throughput against provider. It returns an error instead
// of exiting the process on a Load/Dispatch failure, so callers (and tests)
// can handle it — main still turns a non-nil error into exit code 1.
func benchWASM(ctx context.Context, provider sandbox.ComputeProvider, g guestBuild, n, coldRuns int) (*result, error) {
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
		inst, err := provider.Load(ctx, wasmSpec(g))
		if err != nil {
			return nil, fmt.Errorf("wasm[%s] Load: %w", g.name, err)
		}
		setups = append(setups, time.Since(t0))
		t1 := time.Now()
		if _, err := inst.Dispatch(ctx, args); err != nil {
			return nil, fmt.Errorf("wasm[%s] first Dispatch: %w", g.name, err)
		}
		firsts = append(firsts, time.Since(t1))
		_ = inst.Close(ctx)
	}

	// Warm throughput: one compiled instance, N fresh-instance-per-call dispatches.
	inst, err := provider.Load(ctx, wasmSpec(g))
	if err != nil {
		return nil, fmt.Errorf("wasm[%s] Load (warm): %w", g.name, err)
	}
	defer inst.Close(ctx)
	// Warmup + a one-time output sanity print, so the run also VERIFIES the guest
	// produces a well-formed result, not just that Dispatch returns nil error.
	firstOut, err := inst.Dispatch(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("wasm[%s] warmup Dispatch: %w", g.name, err)
	}
	fmt.Fprintf(os.Stderr, "[bench] wasm[%s] first dispatch output: %s\n", g.name, firstOut)

	durs := make([]time.Duration, 0, n)
	wallStart := time.Now()
	for i := 0; i < n; i++ {
		s := time.Now()
		if _, err := inst.Dispatch(ctx, args); err != nil {
			return nil, fmt.Errorf("wasm[%s] Dispatch %d: %w", g.name, i, err)
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
		rss: "(measured separately)", // filled by measureGuestRSS in runBench
	}, nil
}

func wasmSpec(g guestBuild) sandbox.ComputeSpec {
	return sandbox.ComputeSpec{
		Name:        "echo-" + g.name,
		Description: "trivial compute echo tool (constant work)",
		Module:      g.module,
		AllowWASI:   g.allowWASI,
	}
}

// executablePath resolves the running binary's own path; it is a var (not a
// direct os.Executable call) so tests can force the "cannot determine own
// path" branch of measureGuestRSS without needing a broken process image.
var executablePath = os.Executable

// measureGuestRSS re-execs this harness with -rss-only=<name> so the guest's
// "hold one compiled tool live in-process" RSS is measured from a clean process
// baseline (see the -rss-only handling in run and rssOnlyMain). It returns a
// human string like "42.1 MiB (in-proc)"; on any failure it degrades to "N/A"
// rather than reporting a bogus number.
func measureGuestRSS(r cmdRunner, name string) string {
	exe, err := executablePath()
	if err != nil {
		return "N/A (in-proc)"
	}
	out, err := r.Output(cmdSpec{Name: exe, Args: []string{"-rss-only=" + name}})
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
// with a single dispatch, then print the RSS delta to stdout. This is the true
// cost of holding ONE compiled tool of this guest live, from a fresh process.
// It never returns an error: any failure degrades to printing a 0.0 delta,
// exactly like the original os.Exit-free child-mode behavior.
func rssOnlyMain(ctx context.Context, r cmdRunner, provider sandbox.ComputeProvider, name string, stdout io.Writer) {
	g := buildGuestByName(r, name)
	if g.skipped {
		fmt.Fprintf(stdout, "RSSDELTA_MIB=0.0\n")
		return
	}
	before := selfRSSMiB(r)
	inst, err := provider.Load(ctx, wasmSpec(g))
	if err != nil {
		fmt.Fprintf(stdout, "RSSDELTA_MIB=0.0\n")
		return
	}
	defer inst.Close(ctx)
	if _, err := inst.Dispatch(ctx, json.RawMessage(`{"name":"world","n":21}`)); err != nil {
		fmt.Fprintf(stdout, "RSSDELTA_MIB=0.0\n")
		return
	}
	delta := selfRSSMiB(r) - before
	if delta < 0 {
		delta = 0
	}
	fmt.Fprintf(stdout, "RSSDELTA_MIB=%.1f\n", delta)
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
// no imports (that happens at instantiate). A CompileModule failure returns an
// error instead of exiting the process.
func benchCompileCache(ctx context.Context, g guestBuild, runs int) (cold, cached time.Duration, err error) {
	compileOnce := func(cache wazero.CompilationCache) (time.Duration, error) {
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
			return 0, fmt.Errorf("cache[%s] CompileModule: %w", g.name, err)
		}
		return time.Since(t0), nil
	}

	// cold: a brand-new empty cache each time — no reuse possible.
	var colds []time.Duration
	for i := 0; i < runs; i++ {
		c := wazero.NewCompilationCache()
		d, cerr := compileOnce(c)
		_ = c.Close(ctx)
		if cerr != nil {
			return 0, 0, cerr
		}
		colds = append(colds, d)
	}

	// cached: one shared cache, warmed by a discarded first compile, then measured.
	shared := wazero.NewCompilationCache()
	defer shared.Close(ctx)
	if _, cerr := compileOnce(shared); cerr != nil { // warm the cache (this one pays full compile)
		return 0, 0, cerr
	}
	var hits []time.Duration
	for i := 0; i < runs; i++ {
		d, cerr := compileOnce(shared)
		if cerr != nil {
			return 0, 0, cerr
		}
		hits = append(hits, d)
	}
	return median(colds), median(hits), nil
}

// ---------------------------------------------------------------------------
// Docker candidate
// ---------------------------------------------------------------------------

// dockerProvider is the subset of *sandbox.DockerProvider the Docker candidate
// needs: sandbox.Provider (Create/Name) plus CleanupAll for the
// belt-and-suspenders sweep. *sandbox.DockerProvider satisfies this
// structurally; tests substitute a fake.
type dockerProvider interface {
	sandbox.Provider
	CleanupAll(ctx context.Context)
}

func benchDocker(ctx context.Context, r cmdRunner, p dockerProvider, image string, n, coldRuns int) *result {
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
	rss := containerRSSMiB(r, "aisotropy-sbx-wasmbench-warm")

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
// toolchain / backend probes — all shell out via cmdRunner.
// ---------------------------------------------------------------------------

// selfRSSMiB reports this process's current resident set size in MiB via `ps`
// (portable across macOS and Linux; `ps -o rss=` returns KiB). Returns 0 on any
// failure — RSS is a best-effort density signal, not a hard gate.
func selfRSSMiB(r cmdRunner) float64 {
	out, err := r.Output(cmdSpec{Name: "ps", Args: []string{"-o", "rss=", "-p", strconv.Itoa(os.Getpid())}})
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
func containerRSSMiB(r cmdRunner, name string) string {
	out, err := r.Output(cmdSpec{Name: "docker", Args: []string{"stats", "--no-stream", "--format", "{{.MemUsage}}", name}})
	if err != nil {
		return "N/A"
	}
	// Format: "12.3MiB / 512MiB" — take the first token.
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "/", 2)[0])
	return first + " (container)"
}

func dockerAvailable(r cmdRunner) bool {
	_, err := r.LookPath("docker")
	return err == nil
}

func imageAvailable(r cmdRunner, image string) bool {
	err := r.Run(cmdSpec{Name: "docker", Args: []string{"image", "inspect", image}})
	return err == nil
}

func dockerVersion(r cmdRunner) string {
	out, err := r.Output(cmdSpec{Name: "docker", Args: []string{"version", "--format", "{{.Server.Version}}"}})
	if err != nil {
		return "n/a"
	}
	return strings.TrimSpace(string(out))
}

// tinyGoVersion reports the TinyGo version if present, else "not installed".
func tinyGoVersion(r cmdRunner) string {
	tinygo, err := r.LookPath("tinygo")
	if err != nil {
		return "not installed"
	}
	out, err := r.Output(cmdSpec{Name: tinygo, Args: []string{"version"}})
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// rustVersion reports the resolved rustc version if present, else "not installed".
func rustVersion(r cmdRunner) string {
	rustc := resolveRustc(r)
	if rustc == "" {
		return "not installed"
	}
	out, err := r.Output(cmdSpec{Name: rustc, Args: []string{"--version"}})
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// readBuildInfo is a seam over debug.ReadBuildInfo so tests can force both the
// "ok=false" and "dependency not found" branches of wazeroVersion.
var readBuildInfo = debug.ReadBuildInfo

// wazeroVersion reads the pinned wazero module version from the build info, so
// the reported runtime version is the one actually linked, not a hardcoded guess.
func wazeroVersion() string {
	info, ok := readBuildInfo()
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
