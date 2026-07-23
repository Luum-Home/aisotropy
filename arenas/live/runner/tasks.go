package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// toolTask is one tools-mode input. Two shapes exist:
//
//   - POSITIVE (expected != ""): real arithmetic with a known numeric answer,
//     scored by scoring.ScoreToolsNumeric — the tool must actually have executed
//     (tool_calls[].result present) AND the numeric answer must appear.
//   - ABSTAIN (answer != "", expected == ""): a NON-arithmetic question asked
//     while the calculator is registered. Correct behaviour is to NOT call the
//     tool and answer directly; scored by scoring.ScoreToolsAbstain.
type toolTask struct {
	id       string
	question string
	expected string   // positive tasks: the correct numeric answer, comma-free
	answer   []string // abstain tasks: the objectively-correct answer key(s)
}

// arithmeticTasks is the built-in tools-mode input set.
//
// DIFFICULTY TIERS. The first three are the ORIGINAL easy tier, kept verbatim as
// the regression baseline (every framework scored 3/3 on them — a saturated
// smoke test is still a useful smoke test). The `tool-h*` tier below exists
// because that saturation measured adapter plumbing, not framework capability:
// with the model held constant, a single arithmetic call is trivial for every
// contestant. Each hard task moves the difficulty into something the FRAMEWORK
// owns rather than the model:
//
//   - tool-h-chain-1 / tool-h-chain-2 need MULTIPLE dependent calls (the output
//     of one feeds the next). A framework whose tools mode is single-shot —
//     one model turn, capture the call, stop — cannot produce the final number
//     and fails, while a real agent loop passes.
//   - tool-h-coerce carries thousands separators, so the framework's argument
//     marshalling must survive "1,234,567" reaching a numeric tool.
//   - tool-h-abstain is the only NEGATIVE item: a framework hardwired to always
//     invoke its tool scores 0 here while one that exercises judgment scores 1.
//     Without it, over-eager tool use is invisible — every other signal rewards
//     calling the tool.
//
// A tool that legitimately ERRORS (the fourth discriminator asked for) is
// deliberately NOT here: the arena contract does not specify an erroring tool,
// so each adapter registers its own calculator and division-by-zero behaviour
// varies per language (exception / Infinity / NaN). Scoring that would measure
// the adapter's host language, not the framework — and could crash the adapter
// into a lost record rather than a scored one. It needs a SPEC'd error tool
// first; see the report accompanying this change.
var arithmeticTasks = []toolTask{
	{id: "tool-mul", question: "What is 47 multiplied by 89? Use the calculator tool.", expected: "4183"},
	{id: "tool-expr", question: "What is (48 + 12) * 3 / 2? Use the calculator tool.", expected: "90"},
	{id: "tool-add", question: "What is 1234 plus 5678? Use the calculator tool.", expected: "6912"},

	// ---- hard tier -----------------------------------------------------
	{id: "tool-h-chain-1", question: "Multiply 3947 by 8123, then subtract 1234567 from that result. Use the calculator tool for the arithmetic and report the final number.", expected: "30826914"},
	{id: "tool-h-chain-2", question: "Add 18273 and 44821, multiply that sum by 37, then divide the result by 2. Use the calculator tool for the arithmetic and report the final number.", expected: "1167239"},
	{id: "tool-h-coerce", question: "What is 1,234,567 plus 7,654,321? Use the calculator tool and report the final number.", expected: "8888888"},
	{id: "tool-h-abstain", question: "What is the capital city of Australia?", answer: []string{"Canberra"}},
}

// structuredSchemas maps a suite task ID to the STRICT JSON Schema its
// structured-mode run must conform to.
//
// WHY THIS IS OPT-IN (ARENA_STRICT_STRUCTURED, see eval.go). Today the
// structured dimension scores "did the adapter emit a JSON object at all"
// (scoring.ScoreStructured), which every adapter passes — the dimension is
// saturated. Making it schema-strict is the right fix, but it is ALSO a
// contract change: no adapter currently reads adapterInput.Schema (each one
// hardcodes its own output shape — e.g. ai-sdk's NoteSchema, aisotropy's
// answer+confidence envelope), so scoring them against a runner-defined schema
// today would fail all eight for a SPEC gap rather than a capability
// difference — a false-failure board, which is worse than a saturated one.
// So the schema is shipped, threaded into adapterInput.Schema, and injected
// into the structured system prompt, but is INERT unless the operator sets
// ARENA_STRICT_STRUCTURED=1. Flip it on once SPEC.md requires adapters to
// honour the `schema` field.
//
// The schema exercises exactly the things "valid JSON object" cannot:
// required fields, a NESTED required object, a closed ENUM, and an inclusive
// numeric RANGE. Note that `category` is scored for MEMBERSHIP in the closed
// set, never for being the semantically right category — picking the wrong
// member is not a schema violation, so this stays a schema-conformance test
// and introduces no ambiguous answer key.
var structuredSchemas = map[string]string{}

// strictAnswerSchema is the canonical strict envelope applied to every hard-tier
// QA task in structured mode.
const strictAnswerSchema = `{
  "type": "object",
  "required": ["answer", "confidence", "category", "sources", "meta"],
  "properties": {
    "answer":     {"type": "string"},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1},
    "category":   {"type": "string", "enum": ["geography","chemistry","physics","history","biology","astronomy","other"]},
    "sources":    {"type": "array", "items": {"type": "string"}},
    "meta": {
      "type": "object",
      "required": ["reasoning_steps"],
      "properties": {"reasoning_steps": {"type": "integer", "minimum": 1, "maximum": 10}}
    }
  }
}`

// strictSchemaName is the schema_name echoed to adapters that key their
// structured output off a named schema.
const strictSchemaName = "arena_strict_answer"

// strictStructuredSuffix is appended to the structured-mode system prompt when
// a task declares a schema, so an adapter that does not (yet) read
// adapterInput.Schema still receives the contract through the channel every
// framework does support. Identical text for every framework — no adapter is
// advantaged by it.
const strictStructuredSuffix = "\n\nRespond with a single JSON object that conforms EXACTLY to this JSON Schema. Include every required field, use only the allowed enum values, and respect the numeric ranges. Emit no prose, no markdown fence — only the JSON object.\n\nSchema:\n" + strictAnswerSchema

func init() {
	// Every hard-tier QA task (id prefix "qa-h") carries the strict envelope.
	// Declared here rather than in suite.jsonl because the suite `task` struct
	// (runner/types.go) has no schema field and is owned elsewhere; keeping the
	// mapping in the runner also keeps the JSONL a pure task file.
	for _, id := range []string{
		"qa-h01-bones", "qa-h02-canberra", "qa-h03-iron",
		"qa-h04-berlin", "qa-h05-boiling", "qa-h06-minutes",
	} {
		structuredSchemas[id] = strictAnswerSchema
	}
}

// ragAnswerKeys maps a rag question ID to its answer key(s), for the MULTI-HOP
// grounded questions whose score is not hit@1 but "were ALL gold docs retrieved
// AND is the composed answer complete" (scoring.ScoreRAGMultiHop).
//
// It is a package-level map rather than a field on ragQuestion because
// runner/types.go (which owns ragQuestion) is not this change's to edit. It is
// populated exactly once, by loadRagQuestions, before any workItem is built, so
// the build stays deterministic. A question with no entry keeps the original
// hit@1 scoring unchanged.
var ragAnswerKeys = map[string][]string{}

const toolSystem = "You are an assistant that uses tools for arithmetic. When the user asks for a calculation, call the calculate tool with the expression, then report the numeric result."

func loadSuite(path string) ([]task, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tasks []task
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var t task
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("suite line %d: %w", i+1, err)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func loadRagQuestions(path string) ([]ragQuestion, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs []ragQuestion
	// extra carries the fields ragQuestion (runner/types.go) does not declare.
	// Parsed from the SAME line so the questions file stays a single source of
	// truth; see ragAnswerKeys for why it lands in a package map.
	type extra struct {
		ID     string   `json:"id"`
		Answer []string `json:"answer"`
	}
	ragAnswerKeys = map[string][]string{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var q ragQuestion
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			return nil, fmt.Errorf("rag line %d: %w", i+1, err)
		}
		var x extra
		if err := json.Unmarshal([]byte(line), &x); err != nil {
			return nil, fmt.Errorf("rag line %d (answer key): %w", i+1, err)
		}
		if len(x.Answer) > 0 {
			ragAnswerKeys[x.ID] = x.Answer
		}
		qs = append(qs, q)
	}
	return qs, nil
}

// ---- -tasks subset filter ---------------------------------------------------
//
// See the -tasks flag help text in main.go for the operator-facing contract.
// Background: a scratch `-live` directory of filtered/symlinked files was used
// to run a bounded calibration and broke adapters that resolve auxiliary paths
// RELATIVE TO -live (mastra's .env, aisotropy's _shared/corpus) — a subset
// scratch directory silently strips those paths out from under the adapter.
// -tasks is the fix: it filters the IN-MEMORY task/rag-question slices loaded
// from the REAL -live directory, so path resolution never changes.

// taskFilter is the parsed form of -tasks: a set of exact IDs plus a set of
// prefixes (an entry ending in "*"). A nil *taskFilter matches everything —
// this is what an unset/empty -tasks parses to, so filterTasks/
// filterRagQuestions are no-ops (return the input slice unchanged) and the
// run is byte-identical to today's behavior.
type taskFilter struct {
	exact    map[string]bool
	prefixes []string
}

// parseTaskFilter parses the -tasks flag value. Syntax: comma-separated IDs
// (same separator/trimming as every other CSV flag here, see splitCSV). An
// entry ending in "*" is a PREFIX match — e.g. "qa-h*" matches every ID
// starting with "qa-h" (all six hard-tier QA tasks) without listing each one.
// Anything else is an EXACT match. An empty/whitespace-only string returns
// nil, which taskFilter.match treats as "match everything".
func parseTaskFilter(s string) *taskFilter {
	ids := splitCSV(s)
	if len(ids) == 0 {
		return nil
	}
	f := &taskFilter{exact: map[string]bool{}}
	for _, id := range ids {
		if strings.HasSuffix(id, "*") {
			f.prefixes = append(f.prefixes, strings.TrimSuffix(id, "*"))
		} else {
			f.exact[id] = true
		}
	}
	return f
}

// match reports whether id passes the filter. A nil filter (the -tasks-unset
// case) matches everything.
func (f *taskFilter) match(id string) bool {
	if f == nil {
		return true
	}
	if f.exact[id] {
		return true
	}
	for _, p := range f.prefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// filterTasks keeps only suite tasks (arenas/live/tasks/suite.jsonl) whose ID
// passes filter. A nil filter returns tasks unchanged (same slice, no copy) —
// the exact -tasks-unset behavior.
func filterTasks(tasks []task, filter *taskFilter) []task {
	if filter == nil {
		return tasks
	}
	out := make([]task, 0, len(tasks))
	for _, t := range tasks {
		if filter.match(t.ID) {
			out = append(out, t)
		}
	}
	return out
}

// filterRagQuestions keeps only rag questions (arenas/live/_shared/
// rag_questions.jsonl) whose ID passes filter. A nil filter returns rag
// unchanged (same slice, no copy) — the exact -tasks-unset behavior.
func filterRagQuestions(rag []ragQuestion, filter *taskFilter) []ragQuestion {
	if filter == nil {
		return rag
	}
	out := make([]ragQuestion, 0, len(rag))
	for _, q := range rag {
		if filter.match(q.ID) {
			out = append(out, q)
		}
	}
	return out
}

// buildWork produces the scored work items for one framework × mode, capping to
// maxTasks per mode when maxTasks > 0.
func buildWork(framework, mode string, tasks []task, rag []ragQuestion, maxTasks int) []workItem {
	switch mode {
	case "qa", "structured", "stream":
		return buildQAStyle(framework, mode, filterKind(tasks, "single_shot_qa"), maxTasks)
	case "memory":
		return buildMemory(framework, filterKind(tasks, "multi_turn_memory"), maxTasks)
	case memoryCrossSessionMode, memorySemanticRecallMode, memoryConsolidationMode:
		return buildMemoryExt(framework, mode, tasks, maxTasks)
	case isolationForgetfulMode:
		return buildForgetful(framework, tasks, maxTasks)
	case "tools":
		return buildTools(framework, maxTasks)
	case "rag":
		return buildRAG(framework, rag, maxTasks)
	case "safeguard":
		return buildSafeguard(framework, tasks, rag, maxTasks)
	default:
		return nil
	}
}

// buildQAStyle covers qa, structured and stream: the same single-shot QA tasks,
// only the mode differs (and therefore the adapter behavior + scored signal).
func buildQAStyle(framework, mode string, qa []task, maxTasks int) []workItem {
	qa = capTasks(qa, maxTasks)
	out := make([]workItem, 0, len(qa))
	for _, t := range qa {
		inv := adapterInput{Mode: mode, System: t.System, Messages: t.Messages}
		// Strict structured envelope: only in structured mode, only for tasks
		// that declare a schema, and only when the operator opted in — see
		// structuredSchemas for why this is gated.
		if mode == "structured" && strictStructuredEnabled() {
			if sch, ok := structuredSchemas[t.ID]; ok {
				inv.Schema = json.RawMessage(sch)
				inv.SchemaName = strictSchemaName
				inv.System = t.System + strictStructuredSuffix
			}
		}
		out = append(out, workItem{
			Framework:   framework,
			Mode:        mode,
			ID:          t.ID,
			Kind:        mode, // qa|structured|stream
			Invocations: []adapterInput{inv},
			Question:    lastUser(t.Messages),
			Reference:   t.Reference,
			Answer:      t.Answer,
		})
	}
	return out
}

// buildMemory turns each multi_turn_memory task into a two-invocation persist→
// recall check on a shared session_id: invocation 1 carries the history up to
// (but excluding) the final user question; invocation 2 — a completely separate
// process spawn — asks only the recall question. The fact must survive.
func buildMemory(framework string, mem []task, maxTasks int) []workItem {
	mem = capTasks(mem, maxTasks)
	out := make([]workItem, 0, len(mem))
	for _, t := range mem {
		idx := lastUserIndex(t.Messages)
		if idx < 0 {
			continue
		}
		persist := t.Messages[:idx]
		recall := []inMessage{t.Messages[idx]}
		session := fmt.Sprintf("arena-mem-%s-%s", framework, t.ID)
		invs := []adapterInput{}
		if len(persist) > 0 {
			invs = append(invs, adapterInput{Mode: "memory", System: t.System, Messages: persist, SessionID: session})
		}
		invs = append(invs, adapterInput{Mode: "memory", Messages: recall, SessionID: session})
		out = append(out, workItem{
			Framework:   framework,
			Mode:        "memory",
			ID:          t.ID,
			Kind:        "memory",
			Invocations: invs,
			Question:    t.Messages[idx].Content,
			Reference:   t.Reference,
			Answer:      t.Answer,
		})
	}
	return out
}

// The three memory-EXTENSION dimensions (docs/architecture/arena-memory-
// extensions.md). Each is a RUNNER-side mode whose suite `kind` equals the mode
// name, exactly like the `safeguard` family. On the wire every invocation is an
// ordinary mode:"memory" turn — so a memory-capable adapter needs no new mode
// dispatch — plus the additive `user_id` field (adapterInput.UserID) and a
// session_id that VARIES across the task's invocations while user_id is HELD.
// That varies-session/holds-user mechanic is the whole design: a session-keyed
// transcript store returns empty for the fresh recall session, so only genuine
// user-scoped memory can recall — transcript replay is defeated by construction.
const (
	memoryCrossSessionMode   = "memory_cross_session"
	memorySemanticRecallMode = "memory_semantic_recall"
	memoryConsolidationMode  = "memory_consolidation"
)

// consolidateDirective is the synthetic middle invocation of a
// memory_consolidation work item: a directive turn the runner inserts between
// persist and recall so a framework whose consolidation is TRIGGERED (langmem's
// manager, llamaindex's flush) runs it to completion before the process exits —
// process exit is the deterministic barrier the design mandates (no sleeps, no
// polling). For a framework that consolidates continuously in the hot path
// (aisotropy's outbox/sleep-cycle, mastra's working-memory profile) this turn is
// approximately a no-op trigger — itself a reportable finding, not a defect.
const consolidateDirective = "Please consolidate and durably remember the key facts from our conversation so far, so you can recall them in a later session."

// isMemoryExtKind reports whether a suite task/workItem kind belongs to the
// memory-extension family — shared by the builder routing and the eval/results
// dispatch, mirroring isSafeguardKind.
func isMemoryExtKind(kind string) bool {
	return kind == memoryCrossSessionMode || kind == memorySemanticRecallMode || kind == memoryConsolidationMode
}

// buildMemoryExt builds the OPT-IN memory-extension work items for one of the
// three dimensions (mode == the suite kind). The load-bearing shape:
//
//   - PERSIST invocation(s): every message up to (but excluding) the final user
//     turn, sent under session A and the task's constant user_id.
//   - CONSOLIDATE invocation (memory_consolidation ONLY): the consolidateDirective
//     under session A, same user — the deterministic consolidation barrier.
//   - RECALL invocation: the final user turn, sent under a DIFFERENT session B,
//     the SAME user_id. A session-keyed store finds nothing here; only genuine
//     user-scoped memory recalls the persisted fact.
//
// Every invocation goes over the wire as mode:"memory" (zero new adapter mode)
// carrying user_id — the only wire change versus plain `memory`. The user_id is
// UNIQUE PER TASK (isolation is by user, since session now varies within a task),
// so the aisotropy adapter's per-run facts reset can scope task boundaries by
// user without needing session in its marker (see arenas/live/aisotropy/
// rqlite_reset.go). Scored by scoring.ScoreMemory (answer-key containment), the
// design's chosen primary — no new scorer.
func buildMemoryExt(framework, mode string, tasks []task, maxTasks int) []workItem {
	mem := capTasks(filterKind(tasks, mode), maxTasks)
	out := make([]workItem, 0, len(mem))
	for _, t := range mem {
		idx := lastUserIndex(t.Messages)
		if idx < 0 {
			continue
		}
		persist := t.Messages[:idx]
		recall := []inMessage{t.Messages[idx]}
		// Unique-per-task user (the constant scope across this task's invocations)
		// and two DISTINCT sessions: session A holds persist (+consolidate),
		// session B is the fresh recall session that defeats transcript replay.
		user := "u-" + mode + "-" + t.ID
		sessionA := fmt.Sprintf("arena-%s-%s-a", mode, t.ID)
		sessionB := fmt.Sprintf("arena-%s-%s-b", mode, t.ID)

		var invs []adapterInput
		if len(persist) > 0 {
			invs = append(invs, adapterInput{Mode: "memory", System: t.System, Messages: persist, SessionID: sessionA, UserID: user})
		}
		if mode == memoryConsolidationMode {
			invs = append(invs, adapterInput{Mode: "memory", Messages: []inMessage{{Role: "user", Content: consolidateDirective}}, SessionID: sessionA, UserID: user})
		}
		invs = append(invs, adapterInput{Mode: "memory", Messages: recall, SessionID: sessionB, UserID: user})

		out = append(out, workItem{
			Framework:   framework,
			Mode:        mode,
			ID:          t.ID,
			Kind:        mode, // suite kind == mode name; scored by ScoreMemory in eval.go
			Invocations: invs,
			Question:    t.Messages[idx].Content,
			Reference:   t.Reference,
			Answer:      t.Answer,
		})
	}
	return out
}

// isolationForgetfulMode is the OPT-IN "forgetful integrator" isolation
// dimension (docs/architecture/arena-tenancy-levels.md §"forgetful integrator").
// It MEASURES the L1-vs-L3 tenancy difference the memory_scoped/isolation_leak
// dimensions today only ASSERT (every framework shows a ✓ that means different
// things): write a fact under a scope through the framework's OWN scoping
// primitive, then read it back with the scope OMITTED — the exact real-world
// integrator mistake — and score three deterministic outcomes (scoring.
// ScoreForgetful): refuses (enforcement, PASS) / leak / silent_partition.
//
// The mode string is ALSO the suite `kind` AND the RECALL invocation's wire mode
// (mirroring the safeguard/memory-ext "mode == kind" shape). Opt-in: it is in
// allModes (so `-modes isolation_forgetful` works) but NOT defaultModes, so an
// unflagged run pays zero extra calls.
const isolationForgetfulMode = "isolation_forgetful"

// buildForgetful builds the OPT-IN isolation_forgetful work items. Load-bearing
// shape (a persist→forgetful-recall pair, one process per invocation):
//
//   - PERSIST invocation: every message up to (but excluding) the final user
//     turn, sent as an ordinary wire-mode "memory" turn under an explicit,
//     UNIQUE-per-task user (scope A) and session A. This writes the fact through
//     the framework's own memory API exactly as a real integrator would.
//   - RECALL invocation: the final user turn, sent as wire-mode
//     isolation_forgetful with the USER SCOPE DELIBERATELY OMITTED (UserID "").
//     The distinct wire mode is what tells an adapter to forward the omitted
//     scope RAW to the framework's own API (never backfilling a default the way
//     the plain-memory handler does) — so the framework's OWN boundary decides
//     the outcome, not adapter glue. This is the fairness constraint the design
//     mandates (docs/architecture/arena-idiomaticity-and-dx.md, arena-tenancy-
//     levels.md §5): the omission happens at the framework's API boundary.
//
// The scoped answer key (t.Answer) is the fact written under scope A — the data
// a leak would return and a correct enforcement must NOT. Scored by
// scoring.ScoreForgetful (three-way verdict) via eval.go — enforcement errors
// are recognized by the runner's softenForgetfulRefusal (main.go).
func buildForgetful(framework string, tasks []task, maxTasks int) []workItem {
	items := capTasks(filterKind(tasks, isolationForgetfulMode), maxTasks)
	out := make([]workItem, 0, len(items))
	for _, t := range items {
		idx := lastUserIndex(t.Messages)
		if idx < 0 {
			continue
		}
		persist := t.Messages[:idx]
		recall := []inMessage{t.Messages[idx]}
		// scope A: a unique-per-task (tenant-held, user) scope the fact lives
		// under. sessionB is a well-formed recall session so the ONLY thing the
		// recall "forgot" is the isolation axis (user) — the sharpest test.
		userA := "u-forget-" + t.ID
		sessionA := "arena-forget-" + t.ID + "-a"
		sessionB := "arena-forget-" + t.ID + "-b"

		var invs []adapterInput
		if len(persist) > 0 {
			invs = append(invs, adapterInput{Mode: "memory", System: t.System, Messages: persist, SessionID: sessionA, UserID: userA})
		}
		// The omitted-scope read: UserID intentionally BLANK (the forgetful
		// mistake). Wire mode isolation_forgetful routes the adapter to a handler
		// that forwards the blank identity to the framework's own API.
		invs = append(invs, adapterInput{Mode: isolationForgetfulMode, Messages: recall, SessionID: sessionB, UserID: ""})

		out = append(out, workItem{
			Framework:   framework,
			Mode:        isolationForgetfulMode,
			ID:          t.ID,
			Kind:        isolationForgetfulMode, // suite kind == mode name; scored by ScoreForgetful in eval.go
			Invocations: invs,
			Question:    t.Messages[idx].Content,
			Reference:   t.Reference,
			Answer:      t.Answer, // the scoped fact: a leak returns it, enforcement must not
		})
	}
	return out
}

func buildTools(framework string, maxTasks int) []workItem {
	set := arithmeticTasks
	if maxTasks > 0 && maxTasks < len(set) {
		set = set[:maxTasks]
	}
	out := make([]workItem, 0, len(set))
	for _, a := range set {
		out = append(out, workItem{
			Framework: framework,
			Mode:      "tools",
			ID:        a.id,
			Kind:      "tools",
			Invocations: []adapterInput{{
				Mode:     "tools",
				System:   toolSystem,
				Messages: []inMessage{{Role: "user", Content: a.question}},
			}},
			Question:       a.question,
			ExpectedNumber: a.expected,
			Answer:         a.answer, // non-nil only for the abstain task
		})
	}
	return out
}

// isSafeguardKind reports whether a task/rag-question/workItem kind belongs to
// the safeguard dimension (docs/architecture/arena-safeguard-dimension.md).
// Shared by the builders (routing), the scorer dispatch (eval.go) and the
// guardrail-block softening rule (main.go).
func isSafeguardKind(kind string) bool {
	return kind == "safeguard_nocontain" || kind == "safeguard_decline"
}

// buildSafeguard builds the OPT-IN safeguard dimension's work items
// (docs/architecture/arena-safeguard-dimension.md). The invariant is ZERO
// adapter-contract change: every safeguard item goes over the wire as an
// ordinary single-shot mode:"qa" (the six suite tasks — persona-following,
// identity concealment, system-prompt exfiltration, jailbreak) or mode:"rag"
// (sg-corpus-injection, reusing the exact rag machinery/TopK/collection envs)
// turn — an adapter cannot tell a safeguard turn from a normal one, so no
// adapter code changes and no adapter can special-case the probe. All
// safeguard-ness lives runner-side: the workItem's Kind/Deny/Markers select
// scoring.ScoreNoContain / scoring.ScoreDecline in eval.go.
//
// maxTasks caps the COMBINED item list (suite tasks first, then safeguard rag
// questions) — the safeguard family is one small, deliberately-ordered set,
// not two independent pools like the grounded/trap split in buildRAG.
func buildSafeguard(framework string, tasks []task, rag []ragQuestion, maxTasks int) []workItem {
	var out []workItem
	for _, t := range tasks {
		if !isSafeguardKind(t.Kind) {
			continue
		}
		out = append(out, workItem{
			Framework: framework,
			Mode:      "safeguard",
			ID:        t.ID,
			Kind:      t.Kind,
			Invocations: []adapterInput{{
				Mode:     "qa", // wire mode: an ordinary qa turn — see the builder godoc
				System:   t.System,
				Messages: t.Messages,
			}},
			Question:  lastUser(t.Messages),
			Reference: t.Reference,
			Deny:      t.Deny,
			Markers:   t.Markers,
		})
	}
	for _, q := range rag {
		if !isSafeguardKind(q.Kind) {
			continue
		}
		out = append(out, workItem{
			Framework: framework,
			Mode:      "safeguard",
			ID:        q.ID,
			Kind:      q.Kind,
			Invocations: []adapterInput{{
				Mode:     "rag", // wire mode: an ordinary rag turn against the same corpus/collection
				Messages: []inMessage{{Role: "user", Content: q.Question}},
				TopK:     4,
			}},
			Question:       q.Question,
			Reference:      q.Reference,
			SupportingDocs: q.SupportingDocs,
			Deny:           q.Deny,
		})
	}
	if maxTasks > 0 && maxTasks < len(out) {
		out = out[:maxTasks]
	}
	return out
}

// buildRAG runs each rag question through rag mode. When capping, it keeps the
// grounded questions first but guarantees at least one trap is included so the
// trap-refusal metric is always exercised. Safeguard rag questions (kind
// safeguard_*) are EXCLUDED here — they belong to the opt-in safeguard mode
// (buildSafeguard), so a default rag sweep is byte-identical to pre-safeguard
// behavior and pays zero extra calls.
func buildRAG(framework string, rag []ragQuestion, maxTasks int) []workItem {
	var normal, grounded, traps []ragQuestion
	for _, q := range rag {
		if isSafeguardKind(q.Kind) {
			continue
		}
		normal = append(normal, q)
		if q.Trap {
			traps = append(traps, q)
		} else {
			grounded = append(grounded, q)
		}
	}
	rag = normal
	selected := rag
	if maxTasks > 0 && maxTasks < len(rag) {
		selected = nil
		nGround := maxTasks
		nTrap := 0
		if len(traps) > 0 && maxTasks >= 2 {
			nTrap = 1
			nGround = maxTasks - 1
		}
		if nGround > len(grounded) {
			nGround = len(grounded)
		}
		selected = append(selected, grounded[:nGround]...)
		if nTrap > 0 {
			selected = append(selected, traps[0])
		}
	}
	out := make([]workItem, 0, len(selected))
	for _, q := range selected {
		kind := "rag_grounded"
		if q.Trap {
			kind = "rag_trap"
		}
		out = append(out, workItem{
			Framework: framework,
			Mode:      "rag",
			ID:        q.ID,
			Kind:      kind,
			Invocations: []adapterInput{{
				Mode:     "rag",
				Messages: []inMessage{{Role: "user", Content: q.Question}},
				TopK:     4,
			}},
			Question:       q.Question,
			Reference:      q.Reference,
			SupportingDocs: q.SupportingDocs,
			// Multi-hop questions carry answer key(s); their presence is what
			// switches eval.go from hit@1 to ScoreRAGMultiHop.
			Answer: ragAnswerKeys[q.ID],
		})
	}
	return out
}

// ---- helpers ---------------------------------------------------------------

func filterKind(tasks []task, kind string) []task {
	var out []task
	for _, t := range tasks {
		if t.Kind == kind {
			out = append(out, t)
		}
	}
	return out
}

func capTasks(t []task, maxTasks int) []task {
	if maxTasks > 0 && maxTasks < len(t) {
		return t[:maxTasks]
	}
	return t
}

func lastUser(msgs []inMessage) string {
	if i := lastUserIndex(msgs); i >= 0 {
		return msgs[i].Content
	}
	return ""
}

func lastUserIndex(msgs []inMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}
