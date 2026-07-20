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

// buildWork produces the scored work items for one framework × mode, capping to
// maxTasks per mode when maxTasks > 0.
func buildWork(framework, mode string, tasks []task, rag []ragQuestion, maxTasks int) []workItem {
	switch mode {
	case "qa", "structured", "stream":
		return buildQAStyle(framework, mode, filterKind(tasks, "single_shot_qa"), maxTasks)
	case "memory":
		return buildMemory(framework, filterKind(tasks, "multi_turn_memory"), maxTasks)
	case "tools":
		return buildTools(framework, maxTasks)
	case "rag":
		return buildRAG(framework, rag, maxTasks)
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

// buildRAG runs each rag question through rag mode. When capping, it keeps the
// grounded questions first but guarantees at least one trap is included so the
// trap-refusal metric is always exercised.
func buildRAG(framework string, rag []ragQuestion, maxTasks int) []workItem {
	var grounded, traps []ragQuestion
	for _, q := range rag {
		if q.Trap {
			traps = append(traps, q)
		} else {
			grounded = append(grounded, q)
		}
	}
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
