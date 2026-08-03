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
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"aisotropy-live-runner/scoring"
)

// allModes is the full mode VOCABULARY -modes accepts. Note "safeguard" is a
// RUNNER-side mode only: on the wire its items are ordinary qa/rag turns (see
// buildSafeguard in tasks.go) — no adapter exposes or knows a "safeguard" mode.
var allModes = []string{"qa", "rag", "tools", "memory", "structured", "stream", "safeguard", "memory_cross_session", "memory_semantic_recall", "memory_consolidation", "isolation_forgetful"}

// defaultModes is what an unflagged run sweeps. The safeguard dimension is
// deliberately OPT-IN (pass `-modes safeguard`, or list it explicitly): it adds
// ~7 model calls per framework, and the operator decision is that a default
// full run pays ZERO extra spend for it — see
// docs/architecture/arena-safeguard-dimension.md §cost.
//
// The three memory-extension modes (memory_cross_session,
// memory_semantic_recall, memory_consolidation — see buildMemoryExt in tasks.go
// and docs/architecture/arena-memory-extensions.md) are OPT-IN for the same
// reason: each drives 2–3 model calls per task per framework, so a default run
// pays zero extra spend. They are in allModes (so `-modes memory_cross_session`
// works) but not here.
var defaultModes = []string{"qa", "rag", "tools", "memory", "structured", "stream"}

func main() {
	liveDir := flag.String("live", defaultLiveDir(), "path to arenas/live")
	frameworksFlag := flag.String("frameworks", "", "comma-separated framework names to run (default: all discovered)")
	modesFlag := flag.String("modes", strings.Join(defaultModes, ","), "comma-separated modes to sweep. NOT in the default set: 'safeguard' (opt-in — runner-side safeguard probes sent to adapters as ordinary qa/rag turns and scored by ScoreNoContain/ScoreDecline; a default run pays zero extra calls for it — see docs/architecture/arena-safeguard-dimension.md); and the three memory-extension modes 'memory_cross_session'/'memory_semantic_recall'/'memory_consolidation' (opt-in — session_id varies while user_id holds across 2-3 wire-mode:memory invocations, scored by ScoreMemory; a default run pays zero extra calls — see docs/architecture/arena-memory-extensions.md)")
	dimensionsFlag := flag.String("dimensions", strings.Join(allDimensions, ","), "comma-separated NEW dimensions to run: memory_scoped,isolation_leak,crash_recovery,observability (empty/none to skip)")
	benchModesFlag := flag.String("bench-modes", "qa", "comma-separated modes to benchmark latency on (kept cheap; qa recommended)")
	benchIters := flag.Int("bench-iters", 5, "warm iterations per benchmark cell (N>=5 for a real p50/p95)")
	coldProbes := flag.Int("cold-probes", 3, "cold-start probe spawns per cell (empty-stdin, no model call)")
	nativeOnly := flag.Bool("native-only", false, "benchmark the native execution form only")
	dockerOnly := flag.Bool("docker", false, "benchmark the docker execution form only")
	maxTasks := flag.Int("max-tasks", 0, "cap tasks per correctness mode (0 = all; bounds real spend on a re-run)")
	tasksFlag := flag.String("tasks", "", "THE SUPPORTED way to run a bounded subset of tasks (e.g. a cheap hard-tier-only calibration): comma-separated task/rag-question IDs, e.g. 'qa-h01-bones,mem-h01-overwrite,h1,h2,h4'. An entry ending in '*' is a PREFIX match — e.g. 'qa-h*' selects every suite task ID starting with qa-h (all six hard-tier QA tasks) without listing each one; anything without a trailing '*' is an EXACT id match. Filters BOTH the suite tasks loaded from arenas/live/tasks/suite.jsonl (by task id) AND the rag questions loaded from arenas/live/_shared/rag_questions.jsonl (by question id); the built-in tools-mode task list is NOT filtered by this flag (it is not file-based). Applied BEFORE -max-tasks: -tasks first narrows the REAL suite/rag files down to the matching subset, then -max-tasks caps per-mode from within that already-filtered subset — with -tasks unset (or empty) this is exactly today's behavior, byte-identical. Do NOT hand-build a scratch '-live' directory of filtered/symlinked files to get a subset run — adapters resolve auxiliary paths (e.g. the mastra adapter's .env, the aisotropy adapter's _shared/corpus) RELATIVE TO -live, so a partial scratch directory silently breaks those adapters (observed: mastra failed all 25 runs on a missing .env, aisotropy failed all 10 rag runs on a missing _shared/corpus). -tasks filters the REAL -live directory's contents in memory, so adapter path resolution never changes.")
	concurrency := flag.Int("concurrency", 4, "max frameworks to run concurrently during the correctness/quality pass (1 = fully sequential, exact old behavior; the benchmark pass always stays sequential/uncontended)")
	retry := flag.Int("retry", 1, "max retries per adapter invocation (correctness/quality pass) on a TRANSIENT failure — timeout/deadline/connection-reset/EOF or a 429/500/502/503/'unavailable' adapter error; a deterministic adapter/contract error never retries. 0 = no retry, exact old behavior")
	rps := flag.Float64("rps", 0, "client-side GLOBAL model-call rate CEILING in calls/sec, shared across ALL concurrent framework goroutines (0 = disabled, exact old behavior). The client-side dual of the agnes FREE-TIER rate limit: with -concurrency>1, N frameworks otherwise burst their calls at once and blow the free-tier limit → a 429 storm. The pacer is ADAPTIVE and limit-AVOIDING (learned ceiling ratchets down on each 429 and never re-probes the known-bad rate; see docs/architecture/arena-rate-limiting.md), so this is an UPPER BOUND, not a fixed rate that must be guessed right — set it generously and let it self-tune below the real limit. Recommended for -concurrency>1, e.g. -rps 2.")
	llmJudge := flag.Bool("llm-judge", false, "OPT-IN, default OFF: run the SECONDARY LLM-as-judge scoring pass (agnes judging agnes — a same-family judge that scores generously and non-discriminatingly; see docs/architecture/arena-deterministic-judging.md). The PRIMARY score is always the deterministic arenas/live/runner/scoring pass and needs no flag.")
	skipJudge := flag.Bool("skip-judge", false, "force the LLM-as-judge pass off even if -llm-judge is set (kept for backward compatibility; -llm-judge already defaults to off)")
	skipBench := flag.Bool("skip-bench", false, "skip the benchmark pass entirely")
	skipCorrect := flag.Bool("skip-correctness", false, "skip the correctness/quality pass entirely")
	skipDims := flag.Bool("skip-dimensions", false, "skip the new-dimensions pass (scoped-memory, isolation, crash, observability)")
	skipMatrixGate := flag.Bool("skip-matrix-gate", false, "skip the full-matrix completeness gate over arenas/live/<fw>/matrix.json (still renders the matrix section)")
	resultsOut := flag.String("results-out", "", "write the rendered board to this path instead of <live>/RESULTS.md. THE VALIDATION ESCAPE: a scoped run (one framework, one mode, a changed stub) can be verified end-to-end without touching the published board — which is exactly what the narrowing gate refuses, and refuses correctly. With this set the narrowing gate does not apply, because nothing published is being replaced")
	invalidateBefore := flag.String("invalidate-before", "", "RFC3339 instant: do NOT resume cells measured before it — they re-measure. For a known-bad window (a store that was down, a misconfigured key) whose cells would otherwise stay frozen forever, because -resume skips whatever is present")
	invalidateRun := flag.String("invalidate-run", "", "run id (see the 'run id:' line each run logs): do NOT resume cells produced by that run")
	invalidateUnstamped := flag.Bool("invalidate-unstamped", false, "do NOT resume cells written before records carried provenance — they cannot state when they were measured")
	allowNarrowing := flag.Bool("allow-narrowing", false, "permit writing a RESULTS.md that covers FEWER modes or frameworks than the one already on disk. Off by default: a scoped run that completes replaces the published board with its own narrow slice, and unlike a crash (which writes nothing) that loss looks like a successful run")
	skipServiceGate := flag.Bool("skip-service-gate", false, "skip the real-lane infrastructure reachability gate (Qdrant, rqlite, embed server). Only pass this if you INTEND to measure against whatever happens to be up: a store that is down does not fail loudly, it degrades into in-process defaults and publishes as a retrieval score")
	skipDeterminismGate := flag.Bool("skip-determinism-gate", false, "skip the scripts/arena-determinism-guard.sh purity+reproducibility gate over arenas/live/runner/scoring (the PRIMARY score's enforcement gate — see .claude/rules/arena-deterministic-judging.md)")
	renderOnlyFlag := flag.Bool("render-only", false, "ADR-0064 Layer 1: regenerate RESULTS.md purely from arenas/live/RESULTS.records.jsonl (+ the matrix.json manifests) with ZERO model calls and ZERO adapter spawns, then exit. Proves the render is decoupled from the run — the durable record stream is the single source of truth. All run/pass flags are ignored in this mode.")
	resumeFlag := flag.Bool("resume", false, "ADR-0064 Layer 1: on a normal run, load any existing arenas/live/RESULTS.records.jsonl and SKIP every correctness cell already present (keyed framework|mode|lane|form|task-id), so a killed board continues instead of re-doing work — the resumed cells are folded back into the report. Default OFF preserves exact pre-Layer-1 behavior (a fresh run with no records file is byte-identical to before). A schema-version (v) mismatch invalidates the stale file (renamed aside), never crashes. NOTE: durable append to RESULTS.records.jsonl is ALWAYS on (the durability half of Layer 1); -resume only controls the skip-on-restart half.")
	flag.Parse()

	// ---- render-only: derive RESULTS.md from the durable record stream ------
	// A pure render (no model calls, no adapter spawns, no scoring) — it exits
	// before every pass and every gate. See ADR-0064 Layer 1.
	if *renderOnlyFlag {
		if err := renderOnly(*liveDir, *resultsOut); err != nil {
			fatal("render-only: %v", err)
		}
		return
	}

	if *nativeOnly && *dockerOnly {
		fatal("-native-only and -docker are mutually exclusive")
	}
	if *concurrency < 1 {
		fatal("-concurrency must be >= 1 (got %d)", *concurrency)
	}
	if *retry < 0 {
		fatal("-retry must be >= 0 (got %d)", *retry)
	}
	if *rps < 0 {
		fatal("-rps must be >= 0 (got %v)", *rps)
	}
	// Install the process-global model-call pacer from -rps. Set BEFORE any
	// billable call is issued (correctness/bench/dimension passes all route
	// through runAdapterRetry/runAdapter, which consult modelRateLimiter).
	modelRateLimiter = newRateLimiter(*rps)
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

	// Set the per-run nonce ONCE, before any work is built. It makes every
	// memory-ext/forgetful user scope fresh per run so a stale on-disk store
	// from a prior run cannot satisfy a cross-session recall (see runNonce in
	// tasks.go). Runtime-only value; the scoring package (the determinism-gated
	// one) never sees it.
	runNonce = fmt.Sprintf("r%d", time.Now().UnixNano())
	logf("run nonce (per-run store isolation for memory-ext/forgetful): %s", runNonce)

	// ---- -tasks subset filter (see the flag help text above) --------------
	// Filters the IN-MEMORY slices loaded from the REAL -live directory — the
	// supported alternative to a hand-built scratch -live directory, which
	// breaks adapters that resolve auxiliary paths relative to -live.
	if tf := parseTaskFilter(*tasksFlag); tf != nil {
		beforeTasks, beforeRag := len(tasks), len(rag)
		tasks = filterTasks(tasks, tf)
		rag = filterRagQuestions(rag, tf)
		logf("-tasks filter %q applied: suite tasks %d -> %d, rag questions %d -> %d (tools-mode task list is not filtered)",
			*tasksFlag, beforeTasks, len(tasks), beforeRag, len(rag))
		if len(tasks) == 0 && len(rag) == 0 {
			logf("WARNING: -tasks matched ZERO suite tasks and ZERO rag questions — check the IDs/prefixes against arenas/live/tasks/suite.jsonl and arenas/live/_shared/rag_questions.jsonl")
		}
	}

	frameworks, err := discoverAdapters(*liveDir, splitCSV(*frameworksFlag))
	if err != nil {
		fatal("discover adapters: %v", err)
	}
	if len(frameworks) == 0 {
		fatal("no adapters found under %s/*/run.sh", *liveDir)
	}
	logf("adapters: %s", strings.Join(fwNames(frameworks), ", "))
	logf("modes: %s | bench-modes: %s | forms: %s | bench-iters: %d | correctness concurrency: %d | retry: %d | rps: %s",
		strings.Join(modes, ","), strings.Join(benchModes, ","), strings.Join(forms, ","), *benchIters, *concurrency, *retry, rpsLabel(*rps))
	if len(dims) > 0 && !*skipDims {
		logf("dimensions: %s", strings.Join(dims, ","))
	}

	// ---- full-matrix completeness gate (arenas/live/FULL-MATRIX.md) --------
	matrixDocs, err := loadMatrixDocs(*liveDir)
	if err != nil {
		fatal("load matrix.json manifests: %v", err)
	}
	logf("matrix manifests: %d found", len(matrixDocs))
	if cands := candidateWarnings(matrixDocs); len(cands) > 0 {
		logf("WARNING: %d WIP candidate adapter(s) excluded from the matrix completeness gate, each with its OWN declared reason: %s. Their cells render labeled CANDIDATE, never as passes.",
			len(cands), strings.Join(cands, "; "))
	}
	// Undeclared dimensions are a blank the completeness gate structurally
	// cannot see: it checks the cells a manifest DECLARED, so an absent row
	// costs nothing. Always logged, never silenced — see undeclaredDimensions.
	if und := undeclaredWarnings(matrixDocs); len(und) > 0 {
		logf("WARNING: %d adapter(s) do not declare every dimension their peers do — an ABSENT ROW is a blank the completeness gate cannot see (it only checks declared cells): %s. Each should carry a real result or an explicit infeasible:<reason>; note this includes dimensions where WE are the only declarant.",
			len(und), strings.Join(und, "; "))
	}
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

	// ---- lane label (needed early: it is part of every durable cell key) ----
	// A run scores either the REAL model or the offline STUB. This is a pure
	// function of the environment, so it is computed BEFORE pass 1 — the durable
	// records log (ADR-0064 Layer 1) keys every cell by framework|mode|lane|form|
	// task-id, and resume-skip must know the lane before the pass runs. The
	// stub-lane leak guard (which needs the scored records) stays after pass 1.
	// Three lanes, distinguished by WHAT is substituted — not just "offline or
	// not". The middle one exists because its absence cost a full board:
	//
	//   real         real model, real stores  — the framework comparison
	//   integration  FAKE model, REAL stores  — infrastructure, at zero model cost
	//   stub         fake model, fake stores  — wiring only
	//
	// `stub` fakes the model AND the stores, so it cannot see a broken store
	// connection. Every one of the four defects that invalidated the 2026-07-27
	// board — an orphaned rqlite Raft volume, eight adapters with no Qdrant
	// credential, credentials-in-URL that three HTTP clients reject, a jar and two
	// venvs stale enough to run old code — lived exactly there, and the free lane
	// was structurally blind to all of them. They surfaced only when a real board
	// was already spending.
	//
	// The integration lane closes that: real stores, real credentials, real
	// network, and a mock model. It answers "does the infrastructure work?" with
	// ZERO model calls, which also means zero dependence on WHICH model is
	// configured — so an infra regression is provable without re-introducing the
	// model as a variable (the reason two boards on different providers cannot be
	// compared).
	lane := "real"
	switch {
	case os.Getenv("ARENA_STUB") == "1":
		lane = "stub"
	case os.Getenv("ARENA_STUB_MODEL") == "1":
		lane = "integration"
	}

	// ---- service gate (before the first billable call) ---------------------
	// On 2026-07-28 a full board ran for forty minutes with Qdrant (6333/6334)
	// and rqlite (4001) DOWN — docker was never started. Only genkit-go errored
	// loudly; the other five RAG adapters completed all 19 items each by
	// silently degrading to in-process defaults. That run was about to reach
	// writeResults and REPLACE the published board with retrieval numbers
	// measured against nothing, which reads as framework quality and is not.
	//
	// preflight.sh existed and was not enough: it verifies TOOLCHAINS, reported
	// all nine adapters green, and is a separate script somebody has to remember
	// to run. A gate the operator must remember is not a gate. So this refuses
	// to start, in-process, alongside the determinism and matrix gates, before
	// anything is spent.
	if !*skipServiceGate && lane != "stub" {
		if err := gateServices(lane, frameworks, modes); err != nil {
			fatal("service gate: %v", err)
		}
	} else if *skipServiceGate {
		logf("WARNING: -skip-service-gate set — real-lane infrastructure was NOT verified; any store that is down will degrade silently into the numbers")
	}

	// ---- model-profile gate (before the first billable call) ---------------
	// A board must be able to state what it measured WITH, and whether that model
	// fits the workload. Naming the model is provenance; naming its FIT is what
	// stops a coding-specialised model's low general-assistant scores from being
	// read as framework weakness — the misreading that nearly shipped on
	// 2026-07-27. Gated here, alongside the determinism and matrix gates, so the
	// operator writes the profile BEFORE spending, not after.
	//
	// Skipped on a lane that makes no model call: there is no model to
	// characterise when every reply comes from a mock.
	var profile *modelProfile
	if lane == "real" {
		profile, err = loadModelProfile(*liveDir, modelName())
		if err != nil {
			fatal("model-profile gate: %v", err)
		}
		logf("model profile: %s (%s) — workload fit: %s", profile.Model, profile.Vendor, profile.WorkloadFit)
		for _, c := range profile.readingCaveats() {
			logf("  CAVEAT: %s", c)
		}
	}

	// ---- durable record stream + resume (ADR-0064 Layer 1) ------------------
	// Append-on-complete to RESULTS.records.jsonl is ALWAYS on (the durability
	// half). -resume additionally loads any existing log and skips correctness
	// cells already present (the resume half); default OFF is byte-identical to
	// pre-Layer-1 behavior.
	recordsPath := filepath.Join(*liveDir, recordsFileName)
	if *resultsOut != "" {
		// The records file must follow the board. -results-out existed to make a
		// validation run harmless, and it was not: two validation runs appended
		// 17 envelopes to the SHARED log, including 8 failing ai-sdk memory cells
		// that -resume would have folded into the next real board as genuine
		// failures. Redirecting the render while leaving the durable log shared
		// moves the contamination rather than removing it.
		recordsPath = *resultsOut + ".records.jsonl"
		logf("results-out: durable records also redirected to %s — a validation run must not write into the shared log", recordsPath)
	}
	resume := emptyResumeState()
	inv := invalidateSpec{beforeAt: *invalidateBefore, runID: *invalidateRun, unstamped: *invalidateUnstamped}
	if *resumeFlag {
		resume, err = loadDurableLogFiltered(recordsPath, inv)
		if err != nil {
			fatal("load resume log %s: %v", recordsPath, err)
		}
		if len(resume.present) > 0 {
			logf("resume: %d cell(s) already present in %s will be skipped (%d correctness records folded back into the report)",
				len(resume.present), recordsPath, len(resume.records))
			// A resumed board is a MOSAIC: cells measured minutes ago sit beside
			// cells measured hours ago, under whatever infrastructure was up at
			// the time. State the window so its age is a reported fact instead of
			// something the reader has to assume away.
			logf("resume: measurement window %s .. %s (%d resumed cell(s) carry no provenance — written before records were stamped, so they cannot say when they were measured)",
				orDash(resume.oldestAt), orDash(resume.newestAt), resume.unstamped)
		}
		if resume.invalidated > 0 {
			logf("resume: %d cell(s) INVALIDATED by -invalidate-* and will be re-measured this run", resume.invalidated)
		}
	} else if !inv.empty() {
		logf("WARNING: -invalidate-* has no effect without -resume (nothing is being resumed, so every cell is measured fresh anyway)")
	}
	runID := newRunID(time.Now())
	logf("run id: %s — every cell measured by this run is stamped with it, so a bad window can be invalidated later by id", runID)
	durLog, err := openDurableLog(recordsPath, runID)
	if err != nil {
		fatal("open durable records log %s: %v", recordsPath, err)
	}
	defer durLog.close()

	// ---- pass 1: correctness/quality (native) ------------------------------
	var records []record
	if !*skipCorrect {
		records = runCorrectnessPass(frameworks, modes, tasks, rag, *maxTasks, judge, callBudget, *concurrency, *retry, resume.present, durLog, lane, declaredInfeasible(matrixDocs))
		// Fold any resumed correctness records back in, then re-sort so the
		// per-item detail table is stable across resume boundaries. Only taken
		// under -resume (with no prior file, resume.records is empty and this is
		// a no-op that leaves the pass's own ordering untouched).
		if len(resume.records) > 0 {
			records = append(resume.records, records...)
			sortRecords(records)
		}
	}

	// ---- lane leak guard (P0 reporting discipline) -------------------------
	// In a stub run assert NO adapter secretly called the real model — the exact
	// failure that made aisotropy's "stub" run agnes-2.0-flash for ~20s/call,
	// burning billable free-tier quota (see docs/architecture/arena-
	// infrastructure-parity.md). Deterministic: a pure scan of reported models.
	var laneLeaks []string
	// The integration lane mocks the MODEL too, so the same invariant holds: if a
	// framework reports the real model id, its stub path leaked and the run spent
	// quota it promised not to. Only the stores are real there.
	if lane == "stub" || lane == "integration" {
		laneLeaks = laneLeakFrameworks(records, modelName())
		if len(laneLeaks) > 0 {
			logf("WARNING: LANE LEAK — %d framework(s) reported the REAL model %q during a %s run (called the real provider, billable/quota-burning): %s. This lane must make NO model call — fix the adapter's stub path.",
				len(laneLeaks), modelName(), lane, strings.Join(laneLeaks, ", "))
		} else {
			logf("lane=%s: verified — no framework reported the real model (zero model spend)", lane)
		}
	} else {
		logf("lane=real: scoring against the real model %q", modelName())
	}

	// ---- pass 2: benchmarks (native + docker) ------------------------------
	// Bench cells are appended to the durable log too (so -render-only can
	// reproduce this section), but the bench pass is cheap and always re-runs —
	// it is not resume-skipped.
	var benches []benchCell
	if !*skipBench && len(benchModes) > 0 {
		benches = runBenchmarks(frameworks, benchModes, forms, tasks, rag, *benchIters, *coldProbes, callBudget, durLog, lane)
	}

	// ---- pass 3: new dimensions (scoped-memory, isolation, crash, obs) -----
	var isoRecs []isoRecord
	var crashRecs []crashRecord
	var obsRecs []obsRecord
	if !*skipDims && len(dims) > 0 {
		isoRecs, crashRecs, obsRecs = runDimensions(frameworks, dims, callBudget, durLog, lane)
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
		RPS:         *rps,
		Lane:        lane,
		LaneLeaks:   laneLeaks,
		Records:     records,
		Benches:     benches,
		IsoRecords:  isoRecs,
		CrashRecs:   crashRecs,
		ObsRecords:  obsRecs,
		MatrixDocs:  matrixDocs,
		ModelCaveats: func() []string {
			if profile == nil {
				return nil
			}
			return profile.readingCaveats()
		}(),
		ModelCalls: callBudget.total(),

		// A live run's window spans the cells it measured PLUS any it resumed —
		// which is the honest span, because both kinds are rendered side by side.
		MeasuredFrom: resume.oldestAt,
		MeasuredTo:   time.Now().UTC().Format(time.RFC3339),
		Unstamped:    resume.unstamped,
	}
	resultsPath := filepath.Join(*liveDir, "RESULTS.md")
	if *resultsOut != "" {
		resultsPath = *resultsOut
		logf("results-out: writing to %s — the published board is untouched, and the narrowing gate does not apply because nothing published is being replaced", resultsPath)
	} else if err := gateNarrowing(resultsPath, modes, len(frameworks), *allowNarrowing); err != nil {
		fatal("results narrowing gate: %v", err)
	}
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
func runCorrectnessPass(frameworks []adapter, modes []string, tasks []task, rag []ragQuestion, maxTasks int, judge *judgeClient, budget *callCounter, concurrency int, retries int, resume map[string]bool, durLog *durableLog, lane string,
	// infeasibleModes: framework -> mode -> reason, from the matrix.json
	// manifests. A mode listed here is never built for that framework.
	infeasibleModes map[string]map[string]string) []record {
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
				// Honour the manifest BEFORE spending anything. A dimension the
				// adapter's matrix.json declares infeasible in every cell is not
				// built, not dispatched, and not recorded as an error — the
				// declaration IS the result, which is what the no-blank-cells law
				// asks for. Without this the adapter dies with `unknown mode` and
				// the report shows an errored cell for a framework that never
				// claimed to support the dimension.
				if reason, ok := infeasibleModes[fw.name][mode]; ok {
					logf("[correctness] %s/%s SKIPPED — declared infeasible in matrix.json: %s",
						fw.name, mode, reason)
					continue
				}
				items := buildWork(fw.name, mode, tasks, rag, maxTasks)
				for _, it := range items {
					// Resume-skip (ADR-0064 Layer 1): a cell already durable in
					// RESULTS.records.jsonl is not re-run — no model spend. The
					// resumed record is folded back into the report by the caller.
					if resume[cellKey(fw.name, mode, lane, "native", it.ID)] {
						logf("[correctness] %s/%s %s (resumed — skipped)", fw.name, mode, it.ID)
						continue
					}
					// Stop the moment the provider budget is gone. Every cell dispatched
					// past this point fails for a reason unrelated to the framework under
					// test, and because the schedule is ORDERED those failures land entirely
					// on whoever runs last — turning a billing event into what reads as a
					// capability ranking. A visibly SHORT board beats a complete-looking
					// wrong one; the durable record stream plus -resume make continuation
					// cheap once quota returns.
					if quotaTripped.Load() {
						logf("[correctness] ABORT: provider quota exhausted — stopping before %s/%s %s. "+
							"Recorded cells stay valid; re-run with -resume once quota returns.",
							fw.name, mode, it.ID)
						return
					}
					logf("[correctness] %s/%s %s ...", fw.name, mode, it.ID)
					rec := runWorkItem(fw, it, budget, retries)
					softened := false
					if rec.failed() {
						// Safeguard kinds only: a recognized guardrail-block
						// error IS the safeguard behaving — score it as a
						// refusal instead of losing the record as an error.
						softened = softenGuardrailBlock(&rec, it)
						if !softened {
							// isolation_forgetful only: a recognized isolation-
							// enforcement error on the omitted-scope read IS the
							// framework refusing to forget the scope — score it as
							// "refuses" (PASS), not as an infrastructure failure.
							softened = softenForgetfulRefusal(&rec, it)
						}
					}
					if !rec.failed() && !softened {
						scoreRecord(&rec, it, judge, budget)
					}
					// Append-on-complete: durable the moment the cell finishes,
					// before it is even merged into the report. Line-atomic and
					// concurrency-safe (durableLog serialises writes under its
					// own mutex), so this is safe to call from every framework
					// goroutine. A write failure must not abort the board — the
					// record still counts in-memory; log and continue.
					if err := durLog.appendCorrectness(rec, lane); err != nil {
						logf("WARNING: durable append failed for %s/%s %s: %v", fw.name, rec.Mode, rec.ID, err)
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
	cmd.Env = adapterEnv(script)

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
// Two schedules: a generic-transient one (agnes latency spike / shared-infra
// 502 — clears in ~1s: 1s, 2s, 4s, capped 8s) and a longer RATE-LIMIT one for
// agnes FREE-TIER 429s, whose throttle window is seconds-to-tens-of-seconds:
// 3s, 6s, 12s, 24s, capped 30s. Retrying a 429 after only 1s just re-hits the
// same open rate-limit window and burns another billable 429; the longer
// schedule gives the window real room to clear. The attempt clamp guards the
// left-shift against int64 overflow (the cap dominates well before then).
func retryBackoff(attempt int, rateLimited bool) time.Duration {
	base, ceiling := time.Second, 8*time.Second
	if rateLimited {
		base, ceiling = 3*time.Second, 30*time.Second
	}
	if attempt > 20 {
		attempt = 20
	}
	d := base << uint(attempt)
	if d <= 0 || d > ceiling { // d<=0 catches shift overflow
		d = ceiling
	}
	return d
}

// ---- client-side rate limiter (free-tier pacing) ---------------------------

// modelRateLimiter is the process-GLOBAL pacer for billable model calls. It is
// package-global (like retrySleep) because the resource it protects — the
// shared agnes endpoint's rate limit — is itself process-global: every
// framework goroutine and every pass draws from the same upstream quota. main()
// installs the real one from -rps before any billable call; the default is
// disabled (exact old behavior). See docs/architecture/arena-rate-limiting.md.
var modelRateLimiter = newRateLimiter(0)

// rateSleep is the limiter's sleep, a package var so tests can stub it (mirrors
// retrySleep). Kept separate from retrySleep so a test can exercise pacing and
// retry backoff independently.
var rateSleep = time.Sleep

// rateNow is the limiter's clock, a package var so tests can drive time
// deterministically instead of sleeping in real wall-clock.
var rateNow = time.Now

// rateLimiter is a reservation-style token bucket with ADAPTIVE (AIMD) pacing.
// Each caller atomically reserves the NEXT evenly-spaced slot and blocks until
// it is due; reserving under the mutex but sleeping OUTSIDE it means N
// concurrent callers each get a distinct, monotonically increasing slot without
// serializing on the lock while they wait — so a -concurrency 4 sweep
// interleaves its four frameworks' calls at one bounded aggregate rate rather
// than bursting all four at once.
//
// The admitted rate self-tunes to stay UNDER the real agnes free-tier limit
// without needing to know it (a fixed -rps guess can't — 1.5/s still tripped the
// limit 3× in one run). The control law is asymmetric on purpose, because the
// goal is limit-AVOIDANCE, not throughput-maximization: plain AIMD sawtooths
// (it re-climbs to the ceiling, re-breaks, forever — observed: ~10 scattered
// 429s in one run, each ridden out but none AVOIDED). Instead, observe() feeds
// each call's outcome back and maintains a LEARNED ceiling that only ever
// ratchets DOWN:
//   - a 429 → multiplicatively HALVE the rate (floored), AND drop the learned
//     ceiling to safely below the rate that just broke, so growth never probes
//     back up to a known-bad rate.
//   - a clean-success streak → additively nudge the rate up, but capped by the
//     LEARNED ceiling (not the -rps hard ceiling).
//
// The rate therefore LATCHES onto a safe value below the breaking point after a
// few front-loaded 429s and then stays there — converging to zero. Because the
// limiter is process-global, a 429 seen by ANY framework slows down ALL of them
// (the coordination the shared upstream limit requires), and it starts well
// below the ceiling so warm-up overshoot is minimal.
type rateLimiter struct {
	mu          sync.Mutex
	rate        float64   // current admitted calls/sec; 0 = disabled
	maxRate     float64   // hard ceiling (from -rps); the learned ceiling never exceeds this
	softCeiling float64   // LEARNED ceiling: ratchets down on each 429; growth is capped here
	minRate     float64   // floor; rate never drops below this
	inc         float64   // additive rate bump per clean-success streak
	streakN     int       // clean successes required before one additive bump
	okStreak    int       // consecutive clean successes since the last change
	next        time.Time // earliest instant the next reserved slot may fire
}

// newRateLimiter builds an adaptive, limit-avoiding limiter whose admitted rate
// is capped at `rps` calls/sec (the hard ceiling). It STARTS at a quarter of the
// ceiling (conservative — approach the limit from well below) and climbs on
// sustained success toward the LEARNED ceiling, which begins at the hard ceiling
// and only ratchets down. rps<=0 yields a disabled limiter whose wait()/observe()
// are immediate no-ops.
func newRateLimiter(rps float64) *rateLimiter {
	rl := &rateLimiter{}
	if rps <= 0 {
		return rl // disabled
	}
	rl.maxRate = rps
	rl.softCeiling = rps // no knowledge yet → learned ceiling = hard ceiling
	rl.minRate = rps / 10
	if rl.minRate < 0.1 {
		rl.minRate = 0.1 // ~6s/call floor — below this the free tier is unusable anyway
	}
	if rl.minRate > rps {
		rl.minRate = rps
	}
	rl.inc = rps / 8
	rl.streakN = 8
	rl.rate = rps / 4 // start well below the ceiling; climb toward the learned ceiling on success
	if rl.rate < rl.minRate {
		rl.rate = rl.minRate
	}
	return rl
}

// intervalLocked returns the current min gap between calls. Caller holds rl.mu
// and rl.rate>0 is guaranteed (enabled).
func (rl *rateLimiter) intervalLocked() time.Duration {
	return time.Duration(float64(time.Second) / rl.rate)
}

// wait reserves this caller's slot and blocks until it is due. Concurrent-safe;
// a disabled limiter (rate==0, or a nil receiver) returns immediately.
func (rl *rateLimiter) wait() {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	if rl.rate <= 0 { // disabled
		rl.mu.Unlock()
		return
	}
	now := rateNow()
	slot := rl.next
	if slot.Before(now) {
		slot = now
	}
	rl.next = slot.Add(rl.intervalLocked())
	rl.mu.Unlock()
	if d := slot.Sub(now); d > 0 {
		rateSleep(d)
	}
}

// observe feeds a call's outcome back into the limit-avoiding controller:
//   - rateLimited (a 429): ratchet the LEARNED ceiling down to 0.75× the rate
//     that just broke (never up, floored at minRate), then multiplicatively
//     HALVE the admitted rate (floored at minRate). The whole run backs off
//     below the upstream limit AND never re-probes up to the known-bad rate.
//   - failed but NOT a 429 (a 502/timeout): leave the rate unchanged and reset
//     the success streak — a downstream error is no reason to speed up.
//   - clean success: extend the streak; every streakN in a row, additively bump
//     the rate by inc but capped by the LEARNED ceiling (softCeiling), so the
//     rate latches just under the breaking point instead of sawtoothing.
//
// Concurrent-safe; a disabled limiter (or nil receiver) is a no-op.
func (rl *rateLimiter) observe(failed, rateLimited bool) {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.rate <= 0 { // disabled
		return
	}
	switch {
	case rateLimited:
		// Learn: the current rate provoked a 429 → cap future growth safely
		// below it. Ratchets DOWN only; each fresh 429 tightens it further.
		if c := rl.rate * 0.75; c < rl.softCeiling {
			rl.softCeiling = c
		}
		if rl.softCeiling < rl.minRate {
			rl.softCeiling = rl.minRate
		}
		rl.rate /= 2
		if rl.rate < rl.minRate {
			rl.rate = rl.minRate
		}
		rl.okStreak = 0
	case failed:
		rl.okStreak = 0 // neutral: neither speed up nor slow down
	default:
		rl.okStreak++
		if rl.okStreak >= rl.streakN {
			rl.rate += rl.inc
			if rl.rate > rl.softCeiling { // capped by the LEARNED ceiling, not maxRate
				rl.rate = rl.softCeiling
			}
			rl.okStreak = 0
		}
	}
}

// rpsLabel renders the -rps flag for the run header/log: "off" when disabled,
// else the numeric rate. Keeps the model-call cadence documented in RESULTS.md
// so a paced run is never mistaken for an unpaced one.
func rpsLabel(rps float64) string {
	if rps <= 0 {
		return "off"
	}
	return fmt.Sprintf("%.3g/s", rps)
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

// rateLimitMarkers match the specific agnes FREE-TIER 429 signature (a subset
// of the transient markers). isRateLimited uses these to select retryBackoff's
// longer schedule — a 429 is transient AND rate-limited; a 502/timeout is
// transient but NOT rate-limited (shorter backoff is fine).
var rateLimitMarkers = []string{"429", "too many requests", "rate limit"}

// isRateLimited reports whether a failed result is specifically an upstream
// RATE LIMIT (agnes free-tier 429), as opposed to a generic transient
// (502/timeout/reset). Callers reach this only after isTransientFailure is
// true; it selects the longer retry backoff for the throttle to clear.
func isRateLimited(res execResult) bool {
	if res.out.Error != "" && containsAnyFold(res.out.Error, rateLimitMarkers) {
		return true
	}
	return res.runErr != "" && containsAnyFold(res.runErr, rateLimitMarkers)
}

// quotaTripped is set the first time any adapter reports an exhausted provider
// budget, and read at the top of every subsequent cell so the correctness pass
// stops instead of grinding through a board it can no longer measure. An atomic
// because the pass runs frameworks concurrently.
var quotaTripped atomic.Bool

// quotaExhaustedMarkers match a provider reporting a SPENT BUDGET, as opposed to
// "you are going too fast".
//
// The honest form of the distinction — and it took two providers to get it
// right. Both answer 429. The observable difference is whether the limit CLEARS:
// poolside's "usage limit exceeded" did not, across 170 consecutive cells and
// still an hour later; Agnes's "rate limit for free users" cleared in seconds and
// a board can simply be paced under it. Whether poolside's was a spent budget or
// a long-window throttle is not knowable from outside, and does not matter here:
// a limit that does not clear within the run invalidates the run either way,
// because the cells that fail are the ones scheduled last.
//
// So this list matches the wording providers use for a SPENT allowance, and the
// abort claims only what was observed — a limit that did not clear — never that
// the account is permanently out.
var quotaExhaustedMarkers = []string{
	"usage limit exceeded", "quota exceeded", "insufficient_quota",
	"credit balance", "billing hard limit",
}

// isQuotaExhausted reports whether a failure means the provider budget is spent.
//
// WHY THIS IS SEPARATE FROM isRateLimited, and why it must ABORT the board.
// On 2026-07-27 a full board ran until the free tier's total quota died at cell
// 595 of 765. The remaining 170 cells were dispatched anyway; every one came
// back `429 {"error":"usage limit exceeded"}` and was recorded as an errored
// cell. Nothing crashed, nothing warned, and the run wrote a complete-looking
// RESULTS.md.
//
// The damage is not the wasted calls — it is that exhaustion is ORDER-CORRELATED.
// The runner walks frameworks in a fixed order, so the ones scheduled last ate
// the entire failure: contamination was 0% for the first five frameworks and
// 19% / 24% / 41% / 54% for the last four. The resulting board ranked the
// framework that happened to run second at 99% and the one that ran last at
// zero scored cells. A reader — including us — would have read that as capability.
//
// It is therefore a RUN-INVALIDATING event, not a per-cell error, and is treated
// like the determinism and matrix gates: stop, loudly, and make the operator
// decide. Continuing produces a table whose gaps line up with the schedule
// instead of with the frameworks.
//
// NOT the same as a recoverable throttle: Agnes-style "rate limit" wording stays
// with isRateLimited, which backs off and retries. Misclassifying that as a spent
// budget would abort a board that only needed a slower -rps.
func isQuotaExhausted(res execResult) bool {
	if res.out.Error != "" && containsAnyFold(res.out.Error, quotaExhaustedMarkers) {
		return true
	}
	return res.runErr != "" && containsAnyFold(res.runErr, quotaExhaustedMarkers)
}

// guardrailBlockSignatures are the recognized "the framework's own guardrail
// BLOCKED this request" error shapes, for the SAFEGUARD dimension's softening
// rule (softenGuardrailBlock). Each signature is a named set of substrings that
// must ALL appear (case-insensitive) in the error text:
//
//   - guardrail-block: the adapter/framework reported a guardrail verdict that
//     blocked the turn (e.g. "guardrails: input blocked (injection)").
//   - injection-400: the provider/framework rejected the request as prompt
//     injection at the HTTP boundary (a 400 naming injection).
//
// WHY soften instead of recording an error: on a safeguard probe, a framework
// that BLOCKS the malicious input has SUCCEEDED at the exact thing the
// dimension measures — recording that as an errored (unscored) run would
// punish the strongest possible response. The list is deliberately narrow and
// named: a crash, timeout, or any unrecognized error text still fails
// normally (no signature = no softening), and non-safeguard kinds are never
// softened at all.
var guardrailBlockSignatures = []struct {
	name string
	all  []string // every substring must be present, case-insensitive
}{
	{name: "guardrail-block", all: []string{"guardrail", "block"}},
	{name: "injection-400", all: []string{"400", "injection"}},
}

// matchGuardrailBlockSignature returns the name of the first guardrail-block
// signature whose substrings ALL appear in errText, or "" when none match.
func matchGuardrailBlockSignature(errText string) string {
	low := strings.ToLower(errText)
	for _, sig := range guardrailBlockSignatures {
		matched := true
		for _, sub := range sig.all {
			if !strings.Contains(low, sub) {
				matched = false
				break
			}
		}
		if matched {
			return sig.name
		}
	}
	return ""
}

// softenGuardrailBlock converts a FAILED record into a scored PASS-equivalent
// refusal — but ONLY when (a) the work item is a safeguard kind and (b) the
// failure text matches a recognized guardrail-block signature. It reports
// whether it softened (so the caller skips normal scoring). The Det score is
// set directly (a pure function of the error text — deterministic) rather
// than fabricating a fake reply for the scorers to grade: the record stays
// honest about WHAT happened (the Detail names the signature and quotes the
// error), it is just classified as the safeguard succeeding instead of the
// infrastructure failing.
func softenGuardrailBlock(rec *record, it workItem) bool {
	if !isSafeguardKind(it.Kind) || !rec.failed() {
		return false
	}
	errText := rec.RunErr
	if errText == "" {
		errText = rec.Out.Error
	}
	sig := matchGuardrailBlockSignature(errText)
	if sig == "" {
		return false
	}
	rec.RunErr = ""
	rec.Out.Error = ""
	rec.Det = scoring.Score{
		Value: 1,
		Pass:  true,
		Detail: fmt.Sprintf("guardrail blocked the request (signature %s: %s) — scored as a refusal, not an error",
			sig, tail(errText, 80)),
	}
	return true
}

// forgetfulEnforcementSignatures recognizes an isolation-enforcement error on
// the omitted-scope read — the framework refusing to let the scope be forgotten
// at its OWN boundary. Each is a named set of substrings that must ALL appear
// (case-insensitive), same shape as guardrailBlockSignatures. The aisotropy
// reference lands here: App.Turn on a blank user returns
// `huma.Error422UnprocessableEntity("invalid tenant/user identity", …)` wrapping
// `memory: user requires a non-empty value` (internal/api/core.go:55-58 →
// internal/api/chat.go:193-196 → internal/memory/key.go:34-40) — BEFORE any
// model call, so the refusal is deterministic and offline-observable. The list
// is intentionally broad enough for other frameworks' FASE-3 wiring to add their
// own native enforcement wording (a 4xx "conversationId is required", a scope-
// binding rejection, …) without touching the scorer.
var forgetfulEnforcementSignatures = []struct {
	name string
	all  []string // every substring must be present, case-insensitive
}{
	{name: "identity-required", all: []string{"requires a non-empty value"}},
	{name: "invalid-identity", all: []string{"invalid tenant/user identity"}},
	{name: "identity-422", all: []string{"422", "identity"}},
	// Mastra throws this before any model call when resourceId/threadId is
	// omitted (arenas/live/mastra FASE 3) — model-independent enforcement, so it
	// EARNS the refuses-PASS; without this it would misscore as run-and-fail.
	{name: "mastra-scope-required", all: []string{"resourceid and a threadid must be provided"}},
	// Spring AI's MessageWindowChatMemory throws Assert.hasText before any model
	// call when the conversationId is omitted (arenas/live/spring-ai FASE 3) —
	// the design predicted spring-ai REFUSES where others silently partition,
	// confirmed. Without this it would misscore run-and-fail.
	{name: "spring-conversationid-required", all: []string{"conversationid", "null or empty"}},
}

// matchForgetfulEnforcementSignature returns the name of the first isolation-
// enforcement signature whose substrings ALL appear in errText, or "" when none
// match.
func matchForgetfulEnforcementSignature(errText string) string {
	low := strings.ToLower(errText)
	for _, sig := range forgetfulEnforcementSignatures {
		matched := true
		for _, sub := range sig.all {
			if !strings.Contains(low, strings.ToLower(sub)) {
				matched = false
				break
			}
		}
		if matched {
			return sig.name
		}
	}
	return ""
}

// softenForgetfulRefusal converts a FAILED isolation_forgetful record into a
// scored "refuses" PASS — but ONLY when (a) the item is the isolation_forgetful
// kind and (b) the failure text matches a recognized isolation-enforcement
// signature (the framework refused the omitted-scope read at its own boundary).
// It reports whether it softened, so the caller skips normal scoring. The Det
// score is set from scoring.ScoreForgetful with refusalSeen=true and an empty
// reply — a pure function of the enforcement fact, deterministic — rather than
// fabricating a reply for the scorer to grade. The record stays honest about
// WHAT happened (the Detail names the signature and quotes the error); it is
// just classified as the FRAMEWORK enforcing isolation instead of the
// infrastructure failing. A non-enforcement failure (adapter crash, timeout,
// unknown mode from a not-yet-wired competitor) is NOT softened — it falls
// through as a genuine run-and-fail, the honest verdict.
func softenForgetfulRefusal(rec *record, it workItem) bool {
	if it.Kind != isolationForgetfulMode || !rec.failed() {
		return false
	}
	errText := rec.RunErr
	if errText == "" {
		errText = rec.Out.Error
	}
	sig := matchForgetfulEnforcementSignature(errText)
	if sig == "" {
		return false
	}
	rec.RunErr = ""
	rec.Out.Error = ""
	sc := scoring.ScoreForgetful("", it.Answer, true)
	sc.Detail = fmt.Sprintf("%s (enforcement signature %s: %s)", sc.Detail, sig, tail(errText, 80))
	rec.Det = sc
	return true
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
		// Pace against the shared agnes free-tier limit BEFORE spending the
		// call (no-op when -rps is unset). Every attempt — first and retries —
		// draws a slot, since every attempt is a real billable call.
		modelRateLimiter.wait()
		res = runAdapter(script, in, timeout)
		budget.add(1)
		rateLimited := isRateLimited(res)
		// Feed the outcome to the adaptive pacer: a 429 slows the WHOLE run
		// down (below the upstream limit); a clean success lets it climb back.
		modelRateLimiter.observe(res.failed(), rateLimited)
		if !res.failed() || !isTransientFailure(res) {
			return res
		}
		if attempt >= retries {
			return res // exhausted retries; last (still-transient) failure stands
		}
		backoff := retryBackoff(attempt, rateLimited)
		reason := "transient"
		if rateLimited {
			reason = "rate-limited (429)"
		}
		logf("%s failure on attempt %d/%d (%s) — retrying in %s",
			reason, attempt+1, retries+1, execResultErrSummary(res), backoff)
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
	cmd.Env = adapterEnv(script)

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

// baseInvocationTimeout is the outer per-invocation ceiling for every
// single-turn invocation (qa/rag/tools/structured/stream, and memory mode's
// recall invocation) — the original flat constant, unchanged.
const baseInvocationTimeout = 180 * time.Second

// perTurnAdapterBudget mirrors the aisotropy adapter's own per-turn
// ctx.WithTimeout (110s — arenas/live/aisotropy/mode_memory.go's runMemory),
// plus headroom for the constitutional fail-closed reflection loop's
// retry/backend-switch overhead (internal/llm/reflect_auto.go's ReflectAuto:
// up to 3 Gollem retries, then a homegrown Reflect[T] fallback — empirically
// observed to cost 60-110s on a single turn) and the HTTP/hydration work
// around the LLM call itself. Used only to size the OUTER runner-level
// timeout for invocations that drive MORE than one real model turn inside a
// single adapter process, so the adapter's own graceful per-turn error
// ("App.Turn failed on turn N") always has room to fire BEFORE this outer
// timeout kills the whole process mid-turn with an uninformative "killed".
const perTurnAdapterBudget = 130 * time.Second

// invocationTimeout sizes the outer per-invocation timeout passed to
// runAdapterRetry. Every mode except memory's persist invocation drives
// exactly one real model turn per adapter process, so baseInvocationTimeout
// covers them unchanged (turns<=1 below). memory mode's persist invocation
// (buildMemory in tasks.go) instead runs ALL of a task's user turns
// sequentially inside ONE adapter process (mode_memory.go's runMemory) — the
// flat single-turn budget was never sized for that and reliably kills a
// long-haul memory task (e.g. mem-h03-longhaul's 10 persist turns) mid-flight
// once cumulative per-turn latency (inflated by occasional slow reflection
// retries) crosses 180s, well before every turn has run. Scale the budget by
// the invocation's own turn count instead of guessing a single larger flat
// constant.
func invocationTimeout(in adapterInput) time.Duration {
	turns := 0
	for _, m := range in.Messages {
		if m.Role == "user" {
			turns++
		}
	}
	if turns <= 1 {
		return baseInvocationTimeout
	}
	if scaled := time.Duration(turns) * perTurnAdapterBudget; scaled > baseInvocationTimeout {
		return scaled
	}
	return baseInvocationTimeout
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
		res := runAdapterRetry(fw.runSh, in, invocationTimeout(in), retries, budget)
		// Record the pairing BEFORE any early return, so a failed or errored
		// invocation is in the trace too — those are the ones worth reading.
		rec.Trace = append(rec.Trace, invocationTrace{
			N: i + 1, Total: len(it.Invocations), In: in, Out: res.out, WallMs: res.wallMs,
		})
		// A spent provider budget invalidates the RUN, not just this cell: every
		// remaining framework would fail for a reason that has nothing to do with
		// it, and because the schedule is ordered, the damage lands entirely on
		// whoever runs last. Record the tripwire so the run stops at the top of
		// the next cell — see isQuotaExhausted and quotaTripped.
		if isQuotaExhausted(res) {
			quotaTripped.Store(true)
		}
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

// Local-dev credentials for the reference stack, mirroring docker-compose.yml's
// own defaults (`${QDRANT_API_KEY:-local-dev-qdrant-key-CHANGE-ME}`) and
// deploy/rqlite/users.json. They are placeholders by construction — the
// CHANGE-ME suffix is the contract — and they only ever reach a localhost
// container the operator just started. They are defaults, never overrides: an
// ambient value always wins (see frameworkInfraEnv).
const (
	localDevQdrantAPIKey = "local-dev-qdrant-key-CHANGE-ME"
	localDevRqliteURL    = "http://aisotropy:local-dev-rqlite-pw-CHANGE-ME@localhost:4001"
)

// frameworkInfraEnv supplies the credentials every adapter needs to reach the
// reference stack's rqlite and Qdrant.
//
// WHY THIS EXISTS. docker-compose.yml was hardened to require auth on both
// services (rqlite `-auth`, Qdrant `QDRANT__SERVICE__API_KEY`) AFTER the last
// board ran. Nothing in the arena lane was updated to match: every run.sh
// defaulted to a credential-less URL and the runner injected only the
// collection name. The result was a board that could not have produced a single
// valid row — rqlite answers 401 to an anonymous write, Qdrant answers
// Unauthenticated — for aisotropy AND for every competitor using those tiers.
// Caught pre-flight 2026-07-27 by a one-call smoke test before a ~1,100-call
// spend; that is the entire argument for smoke-testing before a board.
//
// Ambient values WIN. These are appended before os.Environ()'s copy is
// consulted only in the sense that we skip any key the operator already set:
// pointing the arena at a different rqlite/Qdrant must never be silently
// overridden by a localhost default.
//
// THE DISCOVERY FLAG (aisotropy only). gorqlite asks the node for its peers and
// then talks to the address the node ADVERTISES. Our reference rqlite
// deliberately does not set `-http-adv-addr` (see docker-compose.yml: one
// advertised address cannot serve both the in-network aisotropyd and host-side
// clients), so it advertises its container hostname — resolvable inside the
// compose network, DNS-dead on the host where arena adapters run. A single-node
// store has no peers worth discovering, so we turn discovery off and let the
// client use exactly the URL we handed it. Only the aisotropy adapter gets this:
// it is a gorqlite-specific DSN parameter, and the Java/Node/Python adapters
// POST to rqlite directly, without discovery or DSN parsing.
func frameworkInfraEnv(script string) []string {
	fw := filepath.Base(filepath.Dir(script))
	var out []string

	if os.Getenv("QDRANT_API_KEY") == "" {
		out = append(out, "QDRANT_API_KEY="+localDevQdrantAPIKey)
	}
	if os.Getenv("RQLITE_URL") == "" {
		url := localDevRqliteURL
		if fw == aisotropyFrameworkName {
			url += "/?disableClusterDiscovery=true"
		}
		out = append(out, "RQLITE_URL="+url)
	}
	return out
}

// adapterEnv is the full environment an adapter process is launched with:
// the parent environment, plus the per-framework Qdrant collection, plus the
// infra credentials. Both overlays are appended AFTER os.Environ() so they win
// over inherited values — exec.Cmd uses the LAST value for a duplicated key.
func adapterEnv(script string) []string {
	env := append(os.Environ(), frameworkCollectionEnv(script)...)
	return append(env, frameworkInfraEnv(script)...)
}

// ---- results narrowing gate --------------------------------------------------

// gateNarrowing refuses to replace a broader RESULTS.md with a narrower one.
//
// On 2026-07-28, verifying the service gate, a one-framework one-mode run
// (`-modes rag -frameworks eino`) reached writeResults and cut the published
// board from 1525 lines to 315. It was recovered with `git checkout` only
// because RESULTS.md happens to be tracked -- luck, not design.
//
// Note which failure mode this is. A crash mid-run is SAFE: writeResults is
// called once, at the end, so an interrupted board leaves the old file
// untouched. The dangerous run is the one that SUCCEEDS with a smaller scope,
// because success is exactly what makes it overwrite. That inversion is why a
// human reviewing "did the run finish OK?" cannot catch it.
//
// The board already states its own scope in its `> **Run:**` header (modes and
// framework count), so the check reads what the previous run declared rather
// than inferring from the rendered tables.
func gateNarrowing(resultsPath string, modes []string, frameworks int, allow bool) error {
	prevModes, prevFw, ok := parseResultsScope(resultsPath)
	if !ok {
		return nil // no previous board (or an unparseable header): nothing to narrow
	}
	have := map[string]bool{}
	for _, m := range modes {
		have[m] = true
	}
	var lost []string
	for _, m := range prevModes {
		if !have[m] {
			lost = append(lost, m)
		}
	}
	lostFw := prevFw - frameworks
	if len(lost) == 0 && lostFw <= 0 {
		return nil
	}
	if allow {
		logf("WARNING: -allow-narrowing set — overwriting a broader board (dropping modes: %s; %d fewer framework(s))",
			strings.Join(lost, ","), max(lostFw, 0))
		return nil
	}
	var b strings.Builder
	b.WriteString("this run would REPLACE a broader published board with a narrower one:\n")
	if len(lost) > 0 {
		fmt.Fprintf(&b, "  modes on disk but not in this run: %s\n", strings.Join(lost, ", "))
	}
	if lostFw > 0 {
		fmt.Fprintf(&b, "  frameworks: %d on disk, %d in this run\n", prevFw, frameworks)
	}
	b.WriteString("Nothing was written; the board on disk is intact. A scoped run is for testing,\n")
	b.WriteString("and its result is not a board. Re-run with the full mode/framework set (add\n")
	b.WriteString("-resume to keep finished cells), or pass -allow-narrowing if replacing the\n")
	b.WriteString("published board with this slice is genuinely what you want.")
	return fmt.Errorf("%s", b.String())
}

// parseResultsScope reads the `> **Run:** ... **Modes:** a, b **Frameworks:** N`
// header the renderer writes. Returns ok=false when there is no file or the
// header is not in the expected shape -- an unreadable header must not block a
// run, only a CONFIRMED narrowing does.
func parseResultsScope(path string) (modes []string, frameworks int, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "**Modes:**") {
			continue
		}
		mi := strings.Index(line, "**Modes:**") + len("**Modes:**")
		rest := line[mi:]
		end := strings.Index(rest, "•")
		if end < 0 {
			end = len(rest)
		}
		for _, m := range strings.Split(rest[:end], ",") {
			if m = strings.TrimSpace(m); m != "" {
				modes = append(modes, m)
			}
		}
		if fi := strings.Index(line, "**Frameworks:**"); fi >= 0 {
			f := strings.TrimSpace(line[fi+len("**Frameworks:**"):])
			if sp := strings.IndexAny(f, " •"); sp > 0 {
				f = f[:sp]
			}
			frameworks, _ = strconv.Atoi(strings.TrimSpace(f))
		}
		return modes, frameworks, len(modes) > 0
	}
	return nil, 0, false
}

// ---- service gate ------------------------------------------------------------

// probeResult classifies ONE probe. The classification is the whole point: on
// 2026-07-28 a 40-minute board was killed on the conclusion "Qdrant is down",
// reached from three probes that had all merely TIMED OUT on a machine at 93%
// swap. Re-run on an idle machine minutes later the same commands returned
// docker OK, port open, and HTTP 401 -- the service had been answering the
// entire time. The 401 body even said "Must provide an API key", and it had
// been discarded by `curl -o /dev/null`, which kept the status code and threw
// away the sentence that disproved the conclusion.
//
// So this gate never reduces a probe to a boolean. ABSENCE HAS MANY CAUSES AND
// PRESENCE HAS ONE: refused, timed-out, unauthorized and answered-correctly are
// four different facts, and only one of them means "not there". A timeout is
// explicitly INCONCLUSIVE -- it neither absolves nor condemns, the same
// discipline scripts/prune-guard.sh applies when it cannot evaluate a nested
// module.
type probeResult int

const (
	probeOK           probeResult = iota // answered, as expected
	probeRefused                         // connection refused -- genuinely nothing there
	probeInconclusive                    // timed out / transport error: CANNOT CONCLUDE
	probeAnsweredBad                     // answered, but the answer is wrong (auth, 5xx, missing collection)
)

type probe struct {
	name   string
	result probeResult
	detail string
}

// httpProbe performs one HTTP GET and classifies the outcome, keeping the
// response BODY on a non-2xx -- the discriminating evidence a status code alone
// discards.
func httpProbe(name, rawURL string, headers map[string]string, timeout time.Duration) probe {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return probe{name, probeAnsweredBad, "malformed URL: " + err.Error()}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		// Refused is a fact about the world; a timeout is a fact about this
		// machine's current load. They must not share a branch.
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			return probe{name, probeInconclusive, fmt.Sprintf("timed out after %s (NOT proof of absence — see load below)", timeout)}
		}
		if strings.Contains(err.Error(), "connection refused") {
			return probe{name, probeRefused, "connection refused — nothing is listening"}
		}
		return probe{name, probeInconclusive, "transport error: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 == 2 {
		return probe{name, probeOK, string(body)}
	}
	return probe{name, probeAnsweredBad,
		fmt.Sprintf("HTTP %d — the service ANSWERED and rejected the request: %s",
			resp.StatusCode, strings.TrimSpace(tail(string(body), 120)))}
}

// qdrantCollections returns the set of collection names Qdrant reports.
func qdrantCollections(body string) map[string]bool {
	var parsed struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	out := map[string]bool{}
	if json.Unmarshal([]byte(body), &parsed) != nil {
		return out
	}
	for _, c := range parsed.Result.Collections {
		out[c.Name] = true
	}
	return out
}

// gateServices verifies, before the first billable call, that the stores this
// lane measures THROUGH will actually answer the questions the run will ask.
//
// It is SEMANTIC, not a port check. A TCP dial answers "is anything listening?",
// and on 2026-07-28 the real failure was a different question: genkit-go's RAG
// items failed against a collection Qdrant was serving perfectly well, because
// arena_rag_genkit_go did not exist. Every port was open. A dial-based gate
// would have waved that run through exactly as it did.
func gateServices(lane string, frameworks []adapter, modes []string) error {
	const timeout = 8 * time.Second

	runsRAG := false
	for _, m := range modes {
		if m == "rag" || m == "safeguard" {
			runsRAG = true
		}
	}

	var probes []probe
	var missingColls []string

	// --- Qdrant: answer + the per-framework collections this run will query ---
	qURL := strings.TrimRight(envOr("QDRANT_REST_URL", "http://localhost:6333"), "/") + "/collections"
	// The gate must present the SAME credential the adapters will, or it probes a
	// different configuration than the one about to run — a gate that verifies
	// something other than what happens is worse than none. The runner process
	// does not inherit QDRANT_API_KEY: each adapter's run.sh supplies it as
	// `${QDRANT_API_KEY:-local-dev-qdrant-key-CHANGE-ME}`, so the same local-dev
	// default is mirrored here. It is a placeholder for a credential-less local
	// Qdrant (docs/architecture + .claude/rules/services-config.md: the reference
	// compose provisions no real credentials), not a secret.
	//
	// Caught by this gate on its first real run: probing without the key returned
	// 401, which the gate correctly classified as MISCONFIGURED — "the service
	// ANSWERED and rejected" — rather than DOWN. The classification was right and
	// the configuration was mine.
	headers := map[string]string{"api-key": envOr("QDRANT_API_KEY", "local-dev-qdrant-key-CHANGE-ME")}
	q := httpProbe("Qdrant (REST)", qURL, headers, timeout)
	probes = append(probes, q)
	if q.result == probeOK && runsRAG {
		have := qdrantCollections(q.detail)
		for _, fw := range frameworks {
			// Mirrors frameworkCollectionEnv: arena_rag_<fw with - as _>. The
			// reference adapter self-ingests into its own tier and needs none.
			if fw.name == aisotropyFrameworkName {
				continue
			}
			coll := "arena_rag_" + strings.ReplaceAll(fw.name, "-", "_")
			if !have[coll] {
				missingColls = append(missingColls, fmt.Sprintf("%s -> %s", fw.name, coll))
			}
		}
		sort.Strings(missingColls)
	}

	// --- rqlite ---
	probes = append(probes, httpProbe("rqlite", strings.TrimRight(envOr("RQLITE_URL", "http://localhost:4001"), "/")+"/status", nil, timeout))

	// --- embed server ---
	probes = append(probes, httpProbe("embed server", strings.TrimRight(envOr("EMBED_URL", "http://127.0.0.1:8899"), "/")+"/health", nil, timeout))

	var refused, inconclusive, bad []probe
	for _, p := range probes {
		switch p.result {
		case probeOK:
			logf("service gate: %s OK", p.name)
		case probeRefused:
			refused = append(refused, p)
		case probeInconclusive:
			inconclusive = append(inconclusive, p)
		case probeAnsweredBad:
			bad = append(bad, p)
		}
	}

	if len(refused)+len(inconclusive)+len(bad)+len(missingColls) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "the %q lane cannot be measured as configured:\n", lane)
	for _, p := range refused {
		fmt.Fprintf(&b, "  DOWN         %-14s %s\n", p.name, p.detail)
	}
	for _, p := range bad {
		fmt.Fprintf(&b, "  MISCONFIGURED %-13s %s\n", p.name, p.detail)
	}
	for _, m := range missingColls {
		fmt.Fprintf(&b, "  NO COLLECTION %-13s %s\n", "Qdrant", m)
	}
	for _, p := range inconclusive {
		fmt.Fprintf(&b, "  INCONCLUSIVE %-14s %s\n", p.name, p.detail)
	}
	if len(inconclusive) > 0 {
		fmt.Fprintf(&b, "\nAn INCONCLUSIVE probe is NOT a service that is down. It is this gate saying it\n")
		fmt.Fprintf(&b, "could not tell, most often because the machine is loaded. Current load: %s.\n", loadAverage())
		fmt.Fprintf(&b, "Do not conclude absence from it — re-probe on a quiet machine before acting.\n")
	}
	if len(missingColls) > 0 {
		b.WriteString("\nSeed the missing collections: python3 arenas/live/_shared/populate-qdrant.py\n")
	}
	if len(refused) > 0 {
		b.WriteString("Start the stores: docker compose up -d\n")
	}
	b.WriteString("\nThen re-run with -resume to keep finished cells. -skip-service-gate proceeds\n")
	b.WriteString("anyway: a store that is down does not fail loudly, it degrades into in-process\n")
	b.WriteString("defaults and publishes as a retrieval score.")
	return fmt.Errorf("%s", b.String())
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// loadAverage reports the machine's 1/5/15-minute load, so an INCONCLUSIVE
// verdict carries the context that explains it instead of leaving the reader to
// guess. Best-effort: an unavailable load is reported as such, never as zero.
func loadAverage() string {
	out, err := exec.Command("uptime").Output()
	if err != nil {
		return "unavailable"
	}
	s := string(out)
	if i := strings.Index(s, "load average"); i >= 0 {
		return strings.TrimSpace(s[i:])
	}
	return "unavailable"
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

// leakLatencyMs is the wall-clock floor, in ms, above which a stub-run record
// that also reports the real model name is treated as a genuine leak (an actual
// real-model call). A real agnes round-trip is seconds (~5–40s); an offline stub
// answers in well under a second even when it mislabels its model. The
// model-name signal ALONE false-positives: several adapters' stubs honestly run
// offline yet report `model: "agnes-2.0-flash"` (they register their fake model
// under the real name), which is a labelling wart, not a leak. Requiring a
// real-call-like latency too makes the guard fire on the behaviour that matters
// — actually spending a real call (the aisotropy P0.2 bug was ~20s) — not on a
// name. 2000ms sits an order of magnitude above the fast stubs and well below a
// real round-trip.
const leakLatencyMs = 2000

// laneLeakFrameworks returns the distinct frameworks whose correctness records
// reported the REAL provider model during a STUB run AND took a real-call-like
// wall time — i.e. an adapter that was supposed to be offline but actually
// called the real model (billable, quota-burning). This is the guard that would
// have caught aisotropy's stub calling agnes-2.0-flash (~20s/call). Deterministic:
// a pure, case-insensitive scan of the records' reported model + wall time. The
// latency cross-check is what keeps it from crying wolf on a fast offline stub
// that merely reports the real model name (see leakLatencyMs).
func laneLeakFrameworks(records []record, realModel string) []string {
	real := strings.ToLower(strings.TrimSpace(realModel))
	if real == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range records {
		m := strings.ToLower(strings.TrimSpace(r.Out.Model))
		if m == "" {
			continue
		}
		if (m == real || strings.Contains(m, real)) && r.WallMs >= leakLatencyMs {
			if !seen[r.Framework] {
				seen[r.Framework] = true
				out = append(out, r.Framework)
			}
		}
	}
	sort.Strings(out)
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
