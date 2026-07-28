package main

// dimensions.go — the FOUR new adapter dimensions layered on top of the
// qa/rag/tools/memory/structured/stream correctness pass and the benchmark
// pass. These do NOT share the qa-style stdin/stdout contract, so they are run
// and scored on their own terms:
//
//   memory_scoped / isolation_leak — multi-tenant scoped-memory isolation. The
//     per-adapter memory_scoped contract is NOT uniform (eino needs a separate
//     same-scope and cross-tenant recall; pydantic-ai takes a cross_tenant_id in
//     one call), so the authoritative, deterministic signal is each adapter's
//     own `test-isolation.sh`, which exercises ITS contract correctly. The runner
//     runs that script and parses its PASS/FAIL (exit code + `N passed, M failed`
//     + per-concept PASS lines). An adapter WITHOUT the script (the aisotropy
//     reference adapter) is recorded honestly as native/by-construction, never
//     faked (aisotropy enforces tenant isolation in the core runtime via
//     context.Context propagation + payload-filtered HNSW scan — a constitutional
//     gate — so it ships no hand-rolled arena isolation harness).
//
//   crash_recovery — survive a hard `kill -9` mid-turn and resume. Adapters
//     implement this as a two-process orchestration in `test-crash.sh` (producer
//     killed mid-flight, fresh resumer recalls), so a single stdin call is not
//     self-contained. The runner runs `test-crash.sh` and parses its PASS/FAIL +
//     the resumer's `resumed`/`precrash_fact_recalled`/`state_lost` JSON.
//
//   observability — capability comparison, NOT a pass/fail gate. The mode has a
//     UNIFORM wire contract across adapters (otel_supported, span_count,
//     span_names, span_timings_ms, logs_structured), so the runner drives the
//     `observability` mode directly and records the measured reading. A static,
//     documented "native observability platform" axis (LangSmith / Logfire /
//     Genkit tracing / …) is reported alongside the measured in-process OTel
//     emission, because the in-memory-exporter reading is a deliberately narrow
//     lens that does not capture a framework's full native platform.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// allDimensions is the full set of new dimensions the -dimensions flag selects.
var allDimensions = []string{"memory_scoped", "isolation_leak", "crash_recovery", "observability"}

// signal cell glyphs shared by the RESULTS tables.
const (
	sigPass    = "✓"
	sigFail    = "✗"
	sigUnknown = "—"
)

// ---- scoped-memory & isolation ---------------------------------------------

// isoRecord is one framework's scoped-memory + isolation result, sourced from
// its test-isolation.sh (authoritative) when present.
type isoRecord struct {
	Framework string
	HasScript bool
	ScriptOK  bool // script exited 0 (all assertions passed)
	Passed    int
	Failed    int

	SameScopeHit    string // sigPass|sigFail|sigUnknown
	CrossTenantMiss string
	LeakClean       string // leak_detected==false → PASS (isolated)

	RanErr string // runner-side failure (script crash / timeout)
	Note   string
}

// scopedPass is the memory_scoped gate: same_scope_hit==true AND
// cross_tenant_miss==true.
func (r isoRecord) scopedPass() string {
	if r.SameScopeHit == sigPass && r.CrossTenantMiss == sigPass {
		return sigPass
	}
	if r.SameScopeHit == sigUnknown && r.CrossTenantMiss == sigUnknown {
		return sigUnknown
	}
	return sigFail
}

// ---- crash recovery --------------------------------------------------------

type crashRecord struct {
	Framework string
	HasScript bool
	ScriptOK  bool
	Passed    int
	Failed    int

	Resumed          string // sigPass|sigFail|sigUnknown
	PrecrashRecalled string
	StateLost        string // state_lost==false → PASS

	RanErr string
	Note   string
}

// crashPass is the crash_recovery gate: resumed==true AND
// precrash_fact_recalled==true AND state_lost==false.
func (r crashRecord) crashPass() string {
	if r.Resumed == sigPass && r.PrecrashRecalled == sigPass && r.StateLost == sigPass {
		return sigPass
	}
	if r.Resumed == sigUnknown && r.PrecrashRecalled == sigUnknown && r.StateLost == sigUnknown {
		return sigUnknown
	}
	return sigFail
}

// ---- observability ---------------------------------------------------------

// obsOutput is the observability mode's OWN wire contract (NOT adapterOutput).
type obsOutput struct {
	Framework      string             `json:"framework"`
	Mode           string             `json:"mode"`
	OtelSupported  bool               `json:"otel_supported"`
	SpanCount      int                `json:"span_count"`
	SpanNames      []string           `json:"span_names"`
	SpanTimingsMS  map[string]float64 `json:"span_timings_ms"`
	LogsStructured bool               `json:"logs_structured"`
	LogSample      string             `json:"log_sample"`
	Notes          string             `json:"notes"`
	Error          string             `json:"error"`
}

// obsRecord is one framework's measured observability reading plus its static,
// documented native-platform classification.
type obsRecord struct {
	Framework string
	Measured  bool // a valid observability reading was obtained

	OtelSupported  bool
	SpanCount      int
	SpanNames      []string
	LogsStructured bool

	RanErr string
	Note   string
}

// obsPlatform is the static, DOCUMENTED classification of a framework's native
// observability platform and its in-process OTel emission mode. This is the
// second axis of the observability table (the first is the arena-measured
// reading above). Sources are cited in the RESULTS notes; this is documented,
// not measured — the arena's in-memory-exporter reading is a narrow lens.
type obsPlatform struct {
	Platform string // native observability platform / suite
	Emission string // in-process OTel emission mode as it applies in the arena
}

var nativeObsPlatform = map[string]obsPlatform{
	"aisotropy": {
		Platform: "structured slog (in-proc) + native OTel spans (internal/llm/otel.go)",
		// Was "n/a — adapter exposes no observability mode" long after the mode
		// existed: arenas/live/aisotropy/mode_observability.go and the
		// modeObservability constant in that adapter's main.go. The board
		// rendered the stale sentence next to a measured span count from the
		// very mode it claimed was absent.
		//
		// It is the same rot the citation guard catches — a once-true sentence
		// nobody edited while the code moved under it — and it landed on OUR
		// row, understating us. Symmetric rigor is not only about auditing
		// competitors leniently; a stale claim about the house is still a false
		// claim, and this one had the arena reporting a capability we ship as
		// one we lack.
		Emission: "native in-proc OTel (no bridge, no auto-instrumentation)",
	},
	"eino": {
		Platform: "Eino callbacks (eino-ext apmplus / OTel ACL)",
		Emission: "callback→OTel bridge (no native in-proc SDK export)",
	},
	"genkit-go": {
		Platform: "Genkit built-in tracing",
		Emission: "native",
	},
	"langgraph": {
		Platform: "LangSmith (richest here) + LangSmith SDK OTel (emit+ingest)",
		Emission: "callback→OTel bridge (vanilla langgraph/langchain-core)",
	},
	"pydantic-ai": {
		Platform: "Pydantic Logfire + InstrumentedModel",
		Emission: "native (InstrumentedModel)",
	},
	"llamaindex": {
		Platform: "instrumentation dispatcher + OpenInference OTel",
		Emission: "native",
	},
	"ai-sdk": {
		Platform: "AI SDK telemetry + @ai-sdk/otel registration",
		Emission: "via-SDK-registration (flag alone = 0 spans — a real gotcha)",
	},
}

// ---- execution -------------------------------------------------------------

// runDimensions fills the three new dimension record sets for every framework.
// isolation (memory_scoped + isolation_leak) is served by test-isolation.sh;
// crash by test-crash.sh; observability by a direct observability-mode call.
func runDimensions(frameworks []adapter, dims []string, budget *callCounter, durLog *durableLog, lane string) ([]isoRecord, []crashRecord, []obsRecord) {
	want := map[string]bool{}
	for _, d := range dims {
		want[d] = true
	}
	isolationWanted := want["memory_scoped"] || want["isolation_leak"]
	crashWanted := want["crash_recovery"]
	obsWanted := want["observability"]

	var isoRecs []isoRecord
	var crashRecs []crashRecord
	var obsRecs []obsRecord

	// Dimension records are durabled too (ADR-0064 Layer 1 append-on-complete)
	// so -render-only reproduces these tables. A durable-write failure never
	// aborts the pass.
	for _, fw := range frameworks {
		if isolationWanted {
			logf("[dimensions] %s scoped-memory + isolation ...", fw.name)
			ir := runIsolation(fw, budget)
			isoRecs = append(isoRecs, ir)
			if err := durLog.appendIso(ir, lane); err != nil {
				logf("WARNING: durable append failed for iso %s: %v", fw.name, err)
			}
		}
		if crashWanted {
			logf("[dimensions] %s crash_recovery ...", fw.name)
			cr := runCrash(fw, budget)
			crashRecs = append(crashRecs, cr)
			if err := durLog.appendCrash(cr, lane); err != nil {
				logf("WARNING: durable append failed for crash %s: %v", fw.name, err)
			}
		}
		if obsWanted {
			logf("[dimensions] %s observability ...", fw.name)
			or := runObservability(fw, budget)
			obsRecs = append(obsRecs, or)
			if err := durLog.appendObs(or, lane); err != nil {
				logf("WARNING: durable append failed for obs %s: %v", fw.name, err)
			}
		}
	}
	return isoRecs, crashRecs, obsRecs
}

var summaryRe = regexp.MustCompile(`(\d+)\s+passed,\s+(\d+)\s+failed`)

// runIsolation runs the framework's test-isolation.sh and parses its PASS/FAIL.
func runIsolation(fw adapter, budget *callCounter) isoRecord {
	rec := isoRecord{Framework: fw.name}
	script := fw.dir + "/test-isolation.sh"
	if !fileExists(script) {
		rec.HasScript = false
		rec.SameScopeHit, rec.CrossTenantMiss, rec.LeakClean = sigUnknown, sigUnknown, sigUnknown
		// This note is rendered for ANY adapter without the script, so it must not
		// assert a capability on the framework's behalf. It previously read
		// "isolation is enforced by construction in the core runtime" — text
		// written for aisotropy that then attached itself to spring-ai, the only
		// competitor without the script, making an UNEVIDENCED POSITIVE claim
		// about someone else's product (and one contradicted by our own
		// docs/architecture/arena-tenancy-levels.md, which grades Spring AI L1).
		rec.Note = "n/a — this adapter ships no test-isolation.sh, so the compound (tenant,user,product) probe was not run for it. This is a missing measurement, NOT a statement about the framework's isolation; see docs/architecture/arena-tenancy-levels.md for its L0-L3 placement"
		return rec
	}
	rec.HasScript = true
	stdout, code, runErr := runScript(script, 180*time.Second)
	// test-isolation.sh makes 3 real model calls (2 recall turns + 1 leak probe).
	budget.add(3)
	if runErr != "" {
		rec.RanErr = runErr
	}
	rec.ScriptOK = code == 0
	if m := summaryRe.FindStringSubmatch(stdout); m != nil {
		rec.Passed, _ = strconv.Atoi(m[1])
		rec.Failed, _ = strconv.Atoi(m[2])
	}
	rec.SameScopeHit = scanConcept(stdout, sameScopeRe)
	rec.CrossTenantMiss = scanConcept(stdout, crossTenantRe)
	rec.LeakClean = scanConcept(stdout, zeroLeakRe)
	return rec
}

// runCrash runs the framework's test-crash.sh and parses its PASS/FAIL plus the
// resumer's resumed/precrash_fact_recalled/state_lost JSON.
func runCrash(fw adapter, budget *callCounter) crashRecord {
	rec := crashRecord{Framework: fw.name}
	script := fw.dir + "/test-crash.sh"
	if !fileExists(script) {
		rec.HasScript = false
		rec.Resumed, rec.PrecrashRecalled, rec.StateLost = sigUnknown, sigUnknown, sigUnknown
		rec.Note = "no test-crash.sh — no framework here has native crash-recovery; this adapter ships no crash harness"
		return rec
	}
	rec.HasScript = true
	stdout, code, runErr := runScript(script, 300*time.Second)
	// test-crash.sh makes ~2 real model calls (producer turn-1 + resumer recall).
	budget.add(2)
	if runErr != "" {
		rec.RanErr = runErr
	}
	rec.ScriptOK = code == 0
	if m := summaryRe.FindStringSubmatch(stdout); m != nil {
		rec.Passed, _ = strconv.Atoi(m[1])
		rec.Failed, _ = strconv.Atoi(m[2])
	}
	rec.Resumed = scanBoolJSON(stdout, "resumed", true)
	rec.PrecrashRecalled = scanBoolJSON(stdout, "precrash_fact_recalled", true)
	// state_lost==false is the PASS condition.
	rec.StateLost = scanBoolJSON(stdout, "state_lost", false)
	return rec
}

// runObservability drives the observability mode directly and records the
// measured reading. A framework without the mode returns an error JSON (or
// unknown-mode) — recorded honestly, never faked.
func runObservability(fw adapter, budget *callCounter) obsRecord {
	rec := obsRecord{Framework: fw.name}
	// A representative multi-op turn. Adapters that own a fixed observability
	// scenario (eino) ignore this; adapters that instrument the actual turn
	// (pydantic-ai) require a non-empty message to trace.
	in := []byte(`{"mode":"observability","system":"You are an assistant that uses tools for arithmetic.","messages":[{"role":"user","content":"What is (48 + 12) * 3 / 2? Use the calculator tool."}]}`)
	stdout, _, runErr := execStdin(fw.runSh, in, 180*time.Second)
	budget.add(1) // the observability scenario makes real model calls
	if runErr != "" {
		rec.RanErr = runErr
	}
	out, ok := parseObsOutput(stdout)
	if !ok {
		if rec.RanErr == "" {
			rec.RanErr = "no parseable observability JSON on stdout"
		}
		rec.Note = "observability mode not exposed by this adapter"
		return rec
	}
	if out.Error != "" {
		rec.Note = "adapter error: " + tail(out.Error, 80)
		return rec
	}
	rec.Measured = true
	rec.OtelSupported = out.OtelSupported
	rec.SpanCount = out.SpanCount
	rec.SpanNames = out.SpanNames
	rec.LogsStructured = out.LogsStructured
	if out.Notes != "" {
		rec.Note = tail(out.Notes, 120)
	}
	return rec
}

// ---- parsing helpers -------------------------------------------------------

var (
	sameScopeRe   = regexp.MustCompile(`(?i)same[ _-]?scope.*hit|same_scope_hit`)
	crossTenantRe = regexp.MustCompile(`(?i)cross[ _-]?tenant.*miss|cross_tenant_miss`)
	// zero-leak: the real "no leak" assertion. Exclude the ai-sdk positive-control
	// line (which SHOULD leak to prove the test works).
	zeroLeakRe = regexp.MustCompile(`(?i)zero[ _-]?leak|leak[_-]detected=false`)
	passLineRe = regexp.MustCompile(`(?i)^\s*PASS:`)
	failLineRe = regexp.MustCompile(`(?i)^\s*FAIL:`)
)

// scanConcept returns sigPass if a PASS line matches the concept, sigFail if a
// FAIL line matches it, and sigUnknown if neither is found. FAIL wins if both.
func scanConcept(stdout string, conceptRe *regexp.Regexp) string {
	sawPass, sawFail := false, false
	for _, line := range strings.Split(stdout, "\n") {
		if !conceptRe.MatchString(line) {
			continue
		}
		if strings.Contains(strings.ToLower(line), "positive-control") {
			continue // ai-sdk's deliberately-leaking control, not the isolation signal
		}
		if passLineRe.MatchString(line) {
			sawPass = true
		} else if failLineRe.MatchString(line) {
			sawFail = true
		}
	}
	switch {
	case sawFail:
		return sigFail
	case sawPass:
		return sigPass
	default:
		return sigUnknown
	}
}

// scanBoolJSON extracts a boolean field from the crash resumer's JSON on stdout
// and maps it to a PASS/FAIL glyph. wantTrue reports whether true is the passing
// value (resumed/precrash_fact_recalled want true; state_lost wants false).
func scanBoolJSON(stdout, field string, wantTrue bool) string {
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(field) + `"\s*:\s*(true|false)`)
	m := re.FindStringSubmatch(stdout)
	if m == nil {
		return sigUnknown
	}
	val := m[1] == "true"
	if val == wantTrue {
		return sigPass
	}
	return sigFail
}

// parseObsOutput extracts the single observability JSON object from stdout,
// taking the last line that parses with a framework field.
func parseObsOutput(b []byte) (obsOutput, bool) {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var out obsOutput
		if err := json.Unmarshal([]byte(line), &out); err == nil && out.Framework != "" {
			return out, true
		}
	}
	return obsOutput{}, false
}

// spanNamesCell renders a compact span-name list for the observability table.
func spanNamesCell(names []string) string {
	if len(names) == 0 {
		return sigUnknown
	}
	joined := strings.Join(names, ", ")
	return "`" + tail(joined, 60) + "`"
}

func fmtCounts(passed, failed int, ok bool) string {
	if passed == 0 && failed == 0 {
		return sigUnknown
	}
	glyph := sigFail
	if ok {
		glyph = sigPass
	}
	return fmt.Sprintf("%d/%d %s", passed, passed+failed, glyph)
}
