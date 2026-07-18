// Live-arena runner — exhaustive multi-mode sweep + automatic benchmarks.
//
// Discovers framework adapters (arenas/live/*/run.sh, and run-docker.sh for the
// docker execution form), then runs two passes over the framework × mode grid:
//
//  1. CORRECTNESS/QUALITY pass (native): for each framework × mode it drives the
//     appropriate inputs (qa/structured/stream over the suite QA tasks; rag over
//     the 7 grounded + 2 trap rag_questions, scoring retrieval hit@1 on the gold
//     doc and trap-refusal; tools over arithmetic tasks, scoring real tool
//     execution + numeric correctness; memory as a two-invocation persist→recall
//     check). Each answer is scored by an LLM-as-judge (agnes, /chat/completions).
//
//  2. BENCHMARK pass (automatic): for each framework × bench-mode, and for BOTH
//     the native and docker execution forms, it measures steady-state latency
//     (p50/p95, warm), cold-start (spawn-to-first-output with NO model call),
//     TTFT (from stream mode), provider token spend, peak RSS, and docker image
//     footprint + build time.
//
// It then writes arenas/live/RESULTS.md. stdlib only. Model config comes from
// OPENAI_* env; the API key is never logged. Only adapters that actually exist
// are run — a missing/errored adapter is absent from RESULTS, never faked.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// allModes is the full mode set the multi-mode adapters expose.
var allModes = []string{"qa", "rag", "tools", "memory", "structured", "stream"}

func main() {
	liveDir := flag.String("live", defaultLiveDir(), "path to arenas/live")
	frameworksFlag := flag.String("frameworks", "", "comma-separated framework names to run (default: all discovered)")
	modesFlag := flag.String("modes", strings.Join(allModes, ","), "comma-separated modes to sweep")
	dimensionsFlag := flag.String("dimensions", strings.Join(allDimensions, ","), "comma-separated NEW dimensions to run: memory_scoped,isolation_leak,crash_recovery,observability (empty/none to skip)")
	benchModesFlag := flag.String("bench-modes", "qa", "comma-separated modes to benchmark latency on (kept cheap; qa recommended)")
	benchIters := flag.Int("bench-iters", 5, "warm iterations per benchmark cell (N>=5 for a real p50/p95)")
	coldProbes := flag.Int("cold-probes", 3, "cold-start probe spawns per cell (empty-stdin, no model call)")
	nativeOnly := flag.Bool("native-only", false, "benchmark the native execution form only")
	dockerOnly := flag.Bool("docker", false, "benchmark the docker execution form only")
	maxTasks := flag.Int("max-tasks", 0, "cap tasks per correctness mode (0 = all; bounds real spend on a re-run)")
	skipJudge := flag.Bool("skip-judge", false, "skip the LLM-as-judge scoring pass")
	skipBench := flag.Bool("skip-bench", false, "skip the benchmark pass entirely")
	skipCorrect := flag.Bool("skip-correctness", false, "skip the correctness/quality pass entirely")
	skipDims := flag.Bool("skip-dimensions", false, "skip the new-dimensions pass (scoped-memory, isolation, crash, observability)")
	flag.Parse()

	if *nativeOnly && *dockerOnly {
		fatal("-native-only and -docker are mutually exclusive")
	}
	forms := []string{"native", "docker"}
	switch {
	case *nativeOnly:
		forms = []string{"native"}
	case *dockerOnly:
		forms = []string{"docker"}
	}

	// Load repo-root .env so the JUDGE client (which runs in the runner's own
	// process, unlike the adapters that source .env in their run.sh) sees
	// OPENAI_*. Values already present in the environment win; the key is never
	// logged. Repo root is arenas/live/../.. = two levels up from -live.
	repoRoot := filepath.Dir(filepath.Dir(*liveDir))
	if n := loadDotEnv(filepath.Join(repoRoot, ".env")); n > 0 {
		logf("loaded %d var(s) from %s/.env (values not logged)", n, repoRoot)
	}

	modes := intersect(splitCSV(*modesFlag), allModes)
	if len(modes) == 0 {
		fatal("no valid modes selected (want a subset of %s)", strings.Join(allModes, ","))
	}
	benchModes := intersect(splitCSV(*benchModesFlag), allModes)
	dims := intersect(splitCSV(*dimensionsFlag), allDimensions)

	// ---- inputs -------------------------------------------------------------
	suitePath := filepath.Join(*liveDir, "tasks", "suite.jsonl")
	tasks, err := loadSuite(suitePath)
	if err != nil {
		fatal("load suite: %v", err)
	}
	ragPath := filepath.Join(*liveDir, "_shared", "rag_questions.jsonl")
	rag, err := loadRagQuestions(ragPath)
	if err != nil {
		fatal("load rag questions: %v", err)
	}
	logf("loaded %d suite tasks, %d rag questions", len(tasks), len(rag))

	frameworks, err := discoverAdapters(*liveDir, splitCSV(*frameworksFlag))
	if err != nil {
		fatal("discover adapters: %v", err)
	}
	if len(frameworks) == 0 {
		fatal("no adapters found under %s/*/run.sh", *liveDir)
	}
	logf("adapters: %s", strings.Join(fwNames(frameworks), ", "))
	logf("modes: %s | bench-modes: %s | forms: %s | bench-iters: %d",
		strings.Join(modes, ","), strings.Join(benchModes, ","), strings.Join(forms, ","), *benchIters)
	if len(dims) > 0 && !*skipDims {
		logf("dimensions: %s", strings.Join(dims, ","))
	}

	var judge *judgeClient
	if !*skipJudge {
		judge, err = newJudgeClient()
		if err != nil {
			logf("WARNING: judge disabled: %v", err)
		}
	}

	callBudget := &callCounter{}

	// ---- pass 1: correctness/quality (native) ------------------------------
	var records []record
	if !*skipCorrect {
		for _, fw := range frameworks {
			for _, mode := range modes {
				items := buildWork(fw.name, mode, tasks, rag, *maxTasks)
				for _, it := range items {
					logf("[correctness] %s/%s %s ...", fw.name, mode, it.ID)
					rec := runWorkItem(fw, it, callBudget)
					if !rec.failed() {
						scoreRecord(&rec, it, judge, callBudget)
					}
					records = append(records, rec)
				}
			}
		}
	}

	// ---- pass 2: benchmarks (native + docker) ------------------------------
	var benches []benchCell
	if !*skipBench && len(benchModes) > 0 {
		benches = runBenchmarks(frameworks, benchModes, forms, tasks, rag, *benchIters, *coldProbes, callBudget)
	}

	// ---- pass 3: new dimensions (scoped-memory, isolation, crash, obs) -----
	var isoRecs []isoRecord
	var crashRecs []crashRecord
	var obsRecs []obsRecord
	if !*skipDims && len(dims) > 0 {
		isoRecs, crashRecs, obsRecs = runDimensions(frameworks, dims, callBudget)
	}

	// ---- emit ---------------------------------------------------------------
	rep := reportData{
		Model:      modelName(),
		Frameworks: fwNames(frameworks),
		Modes:      modes,
		Dimensions: dims,
		BenchModes: benchModes,
		Forms:      forms,
		BenchIters: *benchIters,
		ColdProbes: *coldProbes,
		JudgeOn:    judge != nil,
		Records:    records,
		Benches:    benches,
		IsoRecords: isoRecs,
		CrashRecs:  crashRecs,
		ObsRecords: obsRecs,
		ModelCalls: callBudget.n,
	}
	resultsPath := filepath.Join(*liveDir, "RESULTS.md")
	if err := writeResults(resultsPath, rep); err != nil {
		fatal("write results: %v", err)
	}
	logf("wrote %s (%d correctness records, %d bench cells, %d iso + %d crash + %d obs dimension records, %d model calls)",
		resultsPath, len(records), len(benches), len(isoRecs), len(crashRecs), len(obsRecs), callBudget.n)
}

// callCounter tracks how many billable model round-trips the sweep issued, so
// RESULTS can document the real agnes spend.
type callCounter struct{ n int }

func (c *callCounter) add(n int) { c.n += n }

// ---- adapter discovery & execution -----------------------------------------

type adapter struct {
	name      string
	runSh     string // native runner (always present)
	runDocker string // docker runner ("" when the adapter has no run-docker.sh)
	dir       string
}

func (a adapter) script(form string) string {
	if form == "docker" {
		return a.runDocker
	}
	return a.runSh
}

func discoverAdapters(liveDir string, only []string) ([]adapter, error) {
	matches, err := filepath.Glob(filepath.Join(liveDir, "*", "run.sh"))
	if err != nil {
		return nil, err
	}
	onlySet := map[string]bool{}
	for _, o := range only {
		onlySet[o] = true
	}
	var out []adapter
	for _, m := range matches {
		dir := filepath.Dir(m)
		name := filepath.Base(dir)
		if name == "runner" || name == "tasks" || name == "_shared" {
			continue
		}
		if len(onlySet) > 0 && !onlySet[name] {
			continue
		}
		a := adapter{name: name, runSh: m, dir: dir}
		if dockerSh := filepath.Join(dir, "run-docker.sh"); fileExists(dockerSh) {
			a.runDocker = dockerSh
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// execResult is one raw adapter invocation's measured outcome.
type execResult struct {
	out      adapterOutput
	parsed   bool
	wallMs   int64
	maxRSSKB int64
	runErr   string
}

// runAdapter runs one adapter invocation over the given script (native or
// docker), feeding the JSON request on stdin and measuring wall clock + peak
// RSS. It never crashes on adapter failure — a bad exit or unparseable stdout
// becomes a runErr on the returned struct.
func runAdapter(script string, in adapterInput, timeout time.Duration) execResult {
	inBytes, err := json.Marshal(in)
	if err != nil {
		return execResult{runErr: fmt.Sprintf("marshal input: %v", err)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Stdin = bytes.NewReader(inBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()

	start := time.Now()
	runErr := cmd.Run()
	wall := time.Since(start)

	res := execResult{wallMs: wall.Milliseconds(), maxRSSKB: maxRSSKB(cmd.ProcessState)}
	if runErr != nil {
		res.runErr = fmt.Sprintf("adapter exec: %v (stderr: %s)", runErr, tail(stderr.String(), 200))
	}
	out, perr := parseAdapterOutput(stdout.Bytes())
	if perr != nil {
		if res.runErr == "" {
			res.runErr = fmt.Sprintf("parse adapter output: %v (stdout: %s)", perr, tail(stdout.String(), 200))
		}
		return res
	}
	res.out = out
	res.parsed = true
	// An adapter that parsed but signalled its own error keeps res.runErr empty;
	// the caller inspects out.Error. Clear a benign non-zero exit if the JSON
	// itself is a clean success (some adapters exit 0; some exit 1 on error and
	// still emit valid JSON — both are handled by the record's failed() check).
	if res.out.Error == "" {
		res.runErr = ""
	} else if res.runErr == "" {
		// adapter emitted a JSON error object with exit 0
	}
	return res
}

// execStdin runs a bash script with the given stdin, returning its raw stdout,
// exit code and any runner-side failure string. Unlike runAdapter it does not
// parse or interpret stdout — the caller owns that (used by the new dimensions:
// the observability mode's own wire contract, and the isolation/crash scripts).
func execStdin(script string, stdin []byte, timeout time.Duration) (stdout []byte, exitCode int, runErr string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", script)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	} else {
		cmd.Stdin = bytes.NewReader(nil)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	cmd.Env = os.Environ()

	err := cmd.Run()
	exitCode = 0
	if err != nil {
		exitCode = 1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
		runErr = fmt.Sprintf("exec %s: %v (stderr: %s)", filepath.Base(script), err, tail(errb.String(), 200))
	}
	return out.Bytes(), exitCode, runErr
}

// runScript runs a bash script with no stdin (the isolation/crash test scripts
// orchestrate their own inputs) and returns its stdout as a string. A non-zero
// exit is NOT a runner failure for these scripts — it is a FAIL assertion the
// caller parses — so runErr is only set on a genuine spawn/timeout failure.
func runScript(script string, timeout time.Duration) (stdout string, exitCode int, runErr string) {
	out, code, rerr := execStdin(script, nil, timeout)
	// A non-zero exit from a test script means "some assertion failed", which is
	// data, not a runner error. Only surface a runErr when we got no output at
	// all (spawn failure / timeout with an empty script), so real FAIL runs are
	// parsed normally.
	if code != 0 && len(strings.TrimSpace(string(out))) > 0 {
		rerr = ""
	}
	return string(out), code, rerr
}

// runWorkItem runs every invocation of a work item natively (the correctness
// pass is native-only), summing provider token usage across invocations and
// scoring against the FINAL invocation's output.
func runWorkItem(fw adapter, it workItem, budget *callCounter) record {
	rec := record{Framework: fw.name, Mode: it.Mode, ID: it.ID, Kind: it.Kind}
	sumIn, sumOut := 0, 0
	hasTok := true
	for i, in := range it.Invocations {
		res := runAdapter(fw.runSh, in, 180*time.Second)
		budget.add(1)
		if res.runErr != "" {
			rec.RunErr = fmt.Sprintf("invocation %d/%d: %s", i+1, len(it.Invocations), res.runErr)
			return rec
		}
		if res.out.Error != "" {
			rec.Out = res.out
			rec.WallMs = res.wallMs
			return rec // adapter-reported error; record it as-is
		}
		if res.out.InputTokens >= 0 && res.out.OutputTokens >= 0 {
			sumIn += res.out.InputTokens
			sumOut += res.out.OutputTokens
		} else {
			hasTok = false
		}
		rec.Out = res.out // last one wins → the scored answer
		rec.WallMs = res.wallMs
	}
	rec.SumIn, rec.SumOut, rec.HasTok = sumIn, sumOut, hasTok
	rec.TTFTMs = rec.Out.TTFTMs
	return rec
}

// parseAdapterOutput extracts the single JSON result object. Adapters may leak a
// stray line to stdout despite the contract; take the last line that parses as a
// result object (framework field present).
func parseAdapterOutput(b []byte) (adapterOutput, error) {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var out adapterOutput
		if err := json.Unmarshal([]byte(line), &out); err == nil && out.Framework != "" {
			return out, nil
		}
	}
	return adapterOutput{}, fmt.Errorf("no valid result JSON object on stdout")
}

// maxRSSKB returns the child process's peak resident set size in kilobytes.
// getrusage reports Maxrss in bytes on darwin and in kilobytes on linux.
func maxRSSKB(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return 0
	}
	m := int64(ru.Maxrss)
	if runtime.GOOS == "darwin" {
		return m / 1024
	}
	return m
}

// ---- small helpers ----------------------------------------------------------

func defaultLiveDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	if filepath.Base(wd) == "runner" {
		return filepath.Dir(wd)
	}
	return wd
}

func modelName() string {
	if m := os.Getenv("OPENAI_MODEL"); m != "" {
		return m
	}
	return "agnes-2.0-flash"
}

// loadDotEnv parses a KEY=VALUE .env file and sets any variable NOT already
// present in the environment. It returns how many vars it set. Values are never
// logged. Supports optional `export ` prefix and surrounding quotes; ignores
// blank lines and `#` comments. A missing file is a no-op (returns 0).
func loadDotEnv(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	set := 0
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		if err := os.Setenv(k, v); err == nil {
			set++
		}
	}
	return set
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// intersect returns the members of want that are in allowed, preserving want's
// order and dropping duplicates/unknowns.
func intersect(want, allowed []string) []string {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, w := range want {
		if ok[w] && !seen[w] {
			out = append(out, w)
			seen[w] = true
		}
	}
	return out
}

func fwNames(fws []adapter) []string {
	out := make([]string, len(fws))
	for i, f := range fws {
		out[i] = f.name
	}
	return out
}

func tail(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "PROGRESS: "+format+"\n", a...)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", a...)
	os.Exit(1)
}
