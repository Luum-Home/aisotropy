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
	"sync"
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
	concurrency := flag.Int("concurrency", 4, "max frameworks to run concurrently during the correctness/quality pass (1 = fully sequential, exact old behavior; the benchmark pass always stays sequential/uncontended)")
	retry := flag.Int("retry", 1, "max retries per adapter invocation (correctness/quality pass) on a TRANSIENT failure — timeout/deadline/connection-reset/EOF or a 429/500/502/503/'unavailable' adapter error; a deterministic adapter/contract error never retries. 0 = no retry, exact old behavior")
	llmJudge := flag.Bool("llm-judge", false, "OPT-IN, default OFF: run the SECONDARY LLM-as-judge scoring pass (agnes judging agnes — a same-family judge that scores generously and non-discriminatingly; see docs/architecture/arena-deterministic-judging.md). The PRIMARY score is always the deterministic arenas/live/runner/scoring pass and needs no flag.")
	skipJudge := flag.Bool("skip-judge", false, "force the LLM-as-judge pass off even if -llm-judge is set (kept for backward compatibility; -llm-judge already defaults to off)")
	skipBench := flag.Bool("skip-bench", false, "skip the benchmark pass entirely")
	skipCorrect := flag.Bool("skip-correctness", false, "skip the correctness/quality pass entirely")
	skipDims := flag.Bool("skip-dimensions", false, "skip the new-dimensions pass (scoped-memory, isolation, crash, observability)")
	skipMatrixGate := flag.Bool("skip-matrix-gate", false, "skip the full-matrix completeness gate over arenas/live/<fw>/matrix.json (still renders the matrix section)")
	skipDeterminismGate := flag.Bool("skip-determinism-gate", false, "skip the scripts/arena-determinism-guard.sh purity+reproducibility gate over arenas/live/runner/scoring (the PRIMARY score's enforcement gate — see .claude/rules/arena-deterministic-judging.md)")
	flag.Parse()

	if *nativeOnly && *dockerOnly {
		fatal("-native-only and -docker are mutually exclusive")
	}
	if *concurrency < 1 {
		fatal("-concurrency must be >= 1 (got %d)", *concurrency)
	}
	if *retry < 0 {
		fatal("-retry must be >= 0 (got %d)", *retry)
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

	// ---- determinism gate: is the PRIMARY scorer pure + reproducible? ------
	// Enforces .claude/rules/arena-deterministic-judging.md: the arena's
	// primary score is deterministic-by-construction, and that claim is
	// backed by an executable gate, not a comment. Runs BEFORE any billable
	// model call — a broken/impure scorer should fail fast, not after
	// spending real agnes calls.
	if !*skipDeterminismGate {
		if err := runDeterminismGate(repoRoot); err != nil {
			fatal("determinism gate: %v", err)
		}
	} else {
		logf("WARNING: -skip-determinism-gate set — the primary scorer's purity/reproducibility was NOT verified this run")
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
	logf("modes: %s | bench-modes: %s | forms: %s | bench-iters: %d | correctness concurrency: %d | retry: %d",
		strings.Join(modes, ","), strings.Join(benchModes, ","), strings.Join(forms, ","), *benchIters, *concurrency, *retry)
	if len(dims) > 0 && !*skipDims {
		logf("dimensions: %s", strings.Join(dims, ","))
	}

	// ---- full-matrix completeness gate (arenas/live/FULL-MATRIX.md) --------
	matrixDocs, err := loadMatrixDocs(*liveDir)
	if err != nil {
		fatal("load matrix.json manifests: %v", err)
	}
	logf("matrix manifests: %d found", len(matrixDocs))
	if !*skipMatrixGate {
		if err := gateMatrices(matrixDocs); err != nil {
			fatal("matrix completeness gate: %v", err)
		}
	} else if len(matrixDocs) > 0 {
		logf("WARNING: -skip-matrix-gate set — pending/invalid cells will render in RESULTS.md but did not fail the run")
	}

	// The LLM-judge is SECONDARY and OPT-IN: it runs only when -llm-judge is
	// explicitly passed (default false) AND -skip-judge was not also passed.
	// The PRIMARY score (arenas/live/runner/scoring, always computed in
	// scoreRecord) needs no flag at all.
	var judge *judgeClient
	if *llmJudge && !*skipJudge {
		judge, err = newJudgeClient()
		if err != nil {
			logf("WARNING: judge disabled: %v", err)
		}
	} else {
		logf("LLM-judge OFF (secondary, opt-in — pass -llm-judge to enable). Primary score: deterministic (arenas/live/runner/scoring).")
	}

	callBudget := &callCounter{}

	// ---- pass 1: correctness/quality (native) ------------------------------
	var records []record
	if !*skipCorrect {
		records = runCorrectnessPass(frameworks, modes, tasks, rag, *maxTasks, judge, callBudget, *concurrency, *retry)
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
		Model:       modelName(),
		Frameworks:  fwNames(frameworks),
		Modes:       modes,
		Dimensions:  dims,
		BenchModes:  benchModes,
		Forms:       forms,
		BenchIters:  *benchIters,
		ColdProbes:  *coldProbes,
		JudgeOn:     judge != nil,
		Concurrency: *concurrency,
		Records:     records,
		Benches:     benches,
		IsoRecords:  isoRecs,
		CrashRecs:   crashRecs,
		ObsRecords:  obsRecs,
		MatrixDocs:  matrixDocs,
		ModelCalls:  callBudget.total(),
	}
	resultsPath := filepath.Join(*liveDir, "RESULTS.md")
	if err := writeResults(resultsPath, rep); err != nil {
		fatal("write results: %v", err)
	}
	logf("wrote %s (%d correctness records, %d bench cells, %d iso + %d crash + %d obs dimension records, %d model calls)",
		resultsPath, len(records), len(benches), len(isoRecs), len(crashRecs), len(obsRecs), callBudget.total())
}

// runCorrectnessPass runs the correctness/quality sweep with up to
// `concurrency` frameworks in flight at once. Each framework's full
// modes×items sequence (including its LLM-judge calls) runs inside its own
// goroutine against a LOCAL records slice — no shared mutable state inside a
// worker. Each goroutine writes its local slice into its OWN, exclusive slot
// of a pre-sized results slice (index i, never resized, never touched by any
// other goroutine), which is race-free without a mutex: concurrent writes to
// disjoint slice elements are not a data race, and wg.Wait() establishes the
// happens-before edge before the slots are read back. The merged records are
// then stably sorted by (framework, mode) — using the caller-supplied
// frameworks/modes order, not alphabetical — so RESULTS.md rendering (in
// particular the per-item detail table, which walks the slice directly) stays
// deterministic regardless of goroutine completion order. concurrency=1
// reproduces the exact old sequential loop and ordering.
func runCorrectnessPass(frameworks []adapter, modes []string, tasks []task, rag []ragQuestion, maxTasks int, judge *judgeClient, budget *callCounter, concurrency int, retries int) []record {
	if concurrency < 1 {
		concurrency = 1
	}
	if retries < 0 {
		retries = 0
	}

	perFW := make([][]record, len(frameworks))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, fw := range frameworks {
		wg.Add(1)
		sem <- struct{}{} // acquire a slot; blocks once `concurrency` workers are in flight
		go func(i int, fw adapter) {
			defer wg.Done()
			defer func() { <-sem }() // release the slot

			var local []record
			for _, mode := range modes {
				items := buildWork(fw.name, mode, tasks, rag, maxTasks)
				for _, it := range items {
					logf("[correctness] %s/%s %s ...", fw.name, mode, it.ID)
					rec := runWorkItem(fw, it, budget, retries)
					if !rec.failed() {
						scoreRecord(&rec, it, judge, budget)
					}
					local = append(local, rec)
				}
			}
			perFW[i] = local // this goroutine owns index i exclusively — no race
		}(i, fw)
	}
	wg.Wait()

	var records []record
	for _, local := range perFW {
		records = append(records, local...)
	}

	// Defensive determinism guarantee (belt-and-suspenders on top of the
	// index-ordered merge above): sort by the caller's framework/mode order so
	// any future change to the collection strategy can't silently reorder
	// RESULTS.md rows based on goroutine completion timing.
	fwIdx := make(map[string]int, len(frameworks))
	for i, fw := range frameworks {
		fwIdx[fw.name] = i
	}
	modeIdx := make(map[string]int, len(modes))
	for i, m := range modes {
		modeIdx[m] = i
	}
	sort.SliceStable(records, func(i, j int) bool {
		if fwIdx[records[i].Framework] != fwIdx[records[j].Framework] {
			return fwIdx[records[i].Framework] < fwIdx[records[j].Framework]
		}
		return modeIdx[records[i].Mode] < modeIdx[records[j].Mode]
	})

	return records
}

// callCounter tracks how many billable model round-trips the sweep issued, so
// RESULTS can document the real agnes spend. Guarded by a mutex: the
// correctness pass now calls add() from multiple framework goroutines
// concurrently (bounded by -concurrency).
type callCounter struct {
	mu sync.Mutex
	n  int
}

func (c *callCounter) add(n int) {
	c.mu.Lock()
	c.n += n
	c.mu.Unlock()
}

// total returns the current count. Safe to call concurrently; also safe to
// call after all writers have joined (main calls it only after every pass's
// goroutines have completed via wg.Wait()).
func (c *callCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

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
	cmd.Env = append(os.Environ(), frameworkCollectionEnv(script)...)

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

// retrySleep is the backoff sleep used between retry attempts. It is a
// package var (not a literal time.Sleep call) purely so tests can stub it to
// a no-op and exercise the retry loop instantly instead of burning several
// real seconds per test case.
var retrySleep = time.Sleep

// retryBackoff returns the backoff before retry attempt `attempt` (0-based:
// the sleep before the FIRST retry, i.e. after the initial attempt failed).
// Exponential-ish and short by design — the transient failures this targets
// (agnes latency spikes, shared-infra 429/502 under load) clear in seconds,
// not minutes: 1s, 2s, 4s, capped at 8s.
func retryBackoff(attempt int) time.Duration {
	d := time.Second << uint(attempt)
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

// transientRunErrMarkers match process-level failures on execResult.runErr —
// the adapter process itself timed out, was killed, or its connection to
// agnes dropped mid-call. Matched case-insensitively as substrings against
// the full runErr string (which embeds the exec error plus a tail of
// stderr), since a shared-infra timeout surfaces either as the outer
// exec.CommandContext deadline or as text the adapter itself logged to
// stderr before a non-zero exit.
var transientRunErrMarkers = []string{
	"context deadline exceeded",
	"timeout",
	"connection reset",
	"eof",
}

// transientOutErrMarkers match adapter-REPORTED failures on
// adapterOutput.Error (valid JSON, exit 0 or 1, but the adapter's own call to
// agnes failed) — HTTP status codes and text agnes/the adapter's HTTP client
// uses for retryable conditions. Matched case-insensitively as substrings.
var transientOutErrMarkers = []string{
	"429",
	"500",
	"502",
	"503",
	"deadline",
	"timeout",
	"unavailable",
}

// isTransientFailure classifies a failed execResult as TRANSIENT (worth a
// retry — real agnes latency variance or shared-infra 429/5xx) vs.
// DETERMINISTIC (a malformed contract, a 400, an adapter logic error like
// "unknown mode" — retrying wastes a billable call and will never succeed).
// A successful result (no runErr, no out.Error) is never transient — callers
// only reach this after confirming failure, but it is safe to call directly.
func isTransientFailure(res execResult) bool {
	if res.runErr != "" {
		return containsAnyFold(res.runErr, transientRunErrMarkers)
	}
	if res.out.Error != "" {
		return containsAnyFold(res.out.Error, transientOutErrMarkers)
	}
	return false
}

// containsAnyFold reports whether s contains any of markers, case-insensitive.
func containsAnyFold(s string, markers []string) bool {
	low := strings.ToLower(s)
	for _, m := range markers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// runAdapterRetry invokes runAdapter once, then retries up to `retries`
// additional times ONLY when the failure classifies as TRANSIENT
// (isTransientFailure) — a deterministic failure (malformed contract, bad
// request, adapter logic error) returns immediately on attempt 1, since
// retrying it wastes a billable call for a guaranteed-repeat failure.
//
// Every attempt — the first and every retry — is a real billable agnes call
// and is counted via budget.add(1); retries=0 makes this call exactly one
// runAdapter invocation plus one budget.add(1), i.e. the exact pre-retry
// behavior. On exhaustion the LAST attempt's result (still failed) is
// returned unchanged — a persistent failure is never hidden.
func runAdapterRetry(script string, in adapterInput, timeout time.Duration, retries int, budget *callCounter) execResult {
	var res execResult
	for attempt := 0; ; attempt++ {
		res = runAdapter(script, in, timeout)
		budget.add(1)
		if !res.failed() || !isTransientFailure(res) {
			return res
		}
		if attempt >= retries {
			return res // exhausted retries; last (still-transient) failure stands
		}
		backoff := retryBackoff(attempt)
		logf("transient failure on attempt %d/%d (%s) — retrying in %s",
			attempt+1, retries+1, execResultErrSummary(res), backoff)
		retrySleep(backoff)
	}
}

// failed reports whether an execResult represents a failed invocation, by
// the same rule record.failed() uses: a runner-side error, or an
// adapter-reported logical error.
func (r execResult) failed() bool { return r.runErr != "" || r.out.Error != "" }

// execResultErrSummary returns a short human-readable reason for a log line,
// preferring the runner-side error and falling back to the adapter-reported
// one.
func execResultErrSummary(res execResult) string {
	if res.runErr != "" {
		return res.runErr
	}
	return res.out.Error
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
	cmd.Env = append(os.Environ(), frameworkCollectionEnv(script)...)

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
// scoring against the FINAL invocation's output. Each invocation is retried
// up to `retries` times on a TRANSIENT failure (see runAdapterRetry);
// retries=0 reproduces the exact pre-retry behavior.
func runWorkItem(fw adapter, it workItem, budget *callCounter, retries int) record {
	rec := record{Framework: fw.name, Mode: it.Mode, ID: it.ID, Kind: it.Kind}
	sumIn, sumOut := 0, 0
	hasTok := true
	for i, in := range it.Invocations {
		res := runAdapterRetry(fw.runSh, in, 180*time.Second, retries, budget)
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

// ---- Qdrant collision fix ----------------------------------------------------

// aisotropyFrameworkName is excluded from the collection override below: it
// does not read the shared, statically pre-populated `arena_rag` corpus at
// all — it self-ingests the Nimbus-Rail corpus into its OWN tiered memory at
// runtime (POST /v1/.../knowledge, see arenas/live/aisotropy/README.md
// "rag" mode + rag_backends.go) into its already-unique "aisotropy_arena"
// collection. Overriding it here would just rename an already-unique,
// self-managed collection for no benefit.
const aisotropyFrameworkName = "aisotropy"

// frameworkCollectionEnv returns environment overrides that give this
// framework's Qdrant collection a UNIQUE per-framework name
// (arena_rag_<fw>), preventing cross-framework vector bleed when multiple
// adapters point at one real, shared Qdrant instance (a fairness bug: 6
// adapters — eino, genkit-go, langgraph, llamaindex, ai-sdk, mastra —
// defaulted to the SAME "arena_rag" collection; two of them (genkit-go,
// pydantic-ai) did not even honor the env var until this change, see
// docs/architecture/arena-deterministic-judging.md "collision fix").
// Appended AFTER os.Environ() so it wins over any inherited
// QDRANT_COLLECTION/ARENA_COLLECTION — exec.Cmd documents that when Env
// contains duplicate keys, only the LAST value for each key is used.
// Derived from the script's parent directory name (every adapter script
// lives at arenas/live/<fw>/run[-docker].sh), so no caller needs to thread
// the framework name through. Both env var names are set because adapters
// are inconsistent about which one they read: eino/langgraph/ai-sdk/mastra/
// genkit-go/pydantic-ai read QDRANT_COLLECTION; llamaindex reads
// ARENA_COLLECTION (arenas/live/llamaindex/adapter.py).
//
// OPERATIONAL NOTE: each arena_rag_<fw> collection must be SEEDED with the
// Nimbus-Rail corpus before a real (non-stub) RAG sweep — see
// arenas/live/_shared/populate-qdrant.py, which now seeds one collection per
// discovered adapter (not just the single legacy "arena_rag").
func frameworkCollectionEnv(script string) []string {
	fw := filepath.Base(filepath.Dir(script))
	if fw == "" || fw == "." || fw == string(filepath.Separator) || fw == aisotropyFrameworkName {
		return nil
	}
	coll := "arena_rag_" + strings.ReplaceAll(fw, "-", "_")
	return []string{
		"QDRANT_COLLECTION=" + coll,
		"ARENA_COLLECTION=" + coll,
	}
}

// ---- determinism gate --------------------------------------------------------

// runDeterminismGate runs scripts/arena-determinism-guard.sh — the
// executable proof that arenas/live/runner/scoring (the arena's PRIMARY
// score) is pure (no LLM/network/randomness/wall-clock dependency) and
// reproducible (byte-identical scores across repeated runs). Mirrors
// gateMatrices' shape: a hard failure here stops the run before any
// billable model call is made. A missing guard script is a hard error, not
// a silent skip — the gate existing is the whole point.
func runDeterminismGate(repoRoot string) error {
	script := filepath.Join(repoRoot, "scripts", "arena-determinism-guard.sh")
	if !fileExists(script) {
		return fmt.Errorf("guard script not found: %s (see .claude/rules/arena-deterministic-judging.md)", script)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stderr // gate output is progress/diagnostic, not the report
	cmd.Stderr = os.Stderr
	logf("[gate] running %s ...", script)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scripts/arena-determinism-guard.sh failed (%v) — the primary scorer is not verified pure/reproducible; fix it before running the arena (see output above)", err)
	}
	logf("[gate] determinism gate PASSED")
	return nil
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
