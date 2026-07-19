// Package scoring is the arena's DETERMINISTIC, PRIMARY quality gate.
//
// WHY THIS PACKAGE EXISTS: the live arena's original quality signal was an
// LLM-as-judge — agnes scoring agnes's own outputs. A same-family judge rates
// its own outputs generously and non-discriminatingly (observed: a flat ~5.0
// across frameworks that behave very differently), which makes it useless as
// the PRIMARY ranking signal. Objective, mode-specific correctness checks
// already existed in the runner (arenas/live/runner/eval.go, dimensions.go)
// but were secondary to the judge. This package CONSOLIDATES those checks
// into one pure, unit-tested, reproducible scorer per dimension and PROMOTES
// them to the arena's primary score. The LLM-judge becomes a secondary,
// opt-in, caveated signal — see arenas/live/runner/eval.go and
// docs/architecture/arena-deterministic-judging.md.
//
// PURITY CONTRACT (enforced by scripts/arena-determinism-guard.sh): this
// package imports NONE of net/http, math/rand, time, internal/llm, or any
// agnes/LLM client. Every exported function is a pure function of its
// arguments — same input MUST produce byte-identical output, forever. No
// wall-clock reads, no randomness, no network calls, no goroutines, no
// package-level mutable state. This is what makes the arena's primary score
// reproducible and impossible for a model (agnes or otherwise) to bias.
package scoring

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Score is one deterministic, objective measurement. Value is always in
// [0,1]; Pass is the dimension's documented pass/fail reduction of Value.
// Detail is a short, deterministic explanation string — for the same inputs
// it is byte-identical across runs (no timestamps, no map-iteration-order
// dependence, no randomness).
type Score struct {
	Value  float64
	Pass   bool
	Detail string
}

// ---- scoreQA ----------------------------------------------------------------

// passQAThreshold is the containment-ratio pass bar for a non-exact QA match.
// Deliberately high (0.8, not a looser bar like 0.6): a reply that merely
// echoes the QUESTION's subject words without the actual answer value (e.g.
// "I don't recall your favorite color" against reference "Your favorite
// color is teal.") shares most non-stopword tokens with the reference while
// omitting the one word that matters — a lower threshold scores that as a
// pass. 0.8 was chosen empirically against exactly that adversarial case
// (see TestScoreMemory_ForgottenFact) plus the "answer extends reference"
// case (see TestScoreQA_ContainmentPass), and is itself covered by the
// reproducibility fixture so a future change to this constant is a
// deliberate, tested decision, not a silent drift.
const passQAThreshold = 0.8

// ScoreQA deterministically scores a free-text reply against a reference.
// Exact normalized match scores 1.0. Otherwise, when explicit ANSWER KEYS are
// provided (the objectively-correct answer string(s) for the question), the
// reply PASSES iff it fully contains at least one acceptable answer key.
//
// WHY answer keys (and not pure reference token-containment): the old fallback
// scored the fraction of the REFERENCE's tokens present in the reply, which
// rewarded a VERBOSE answer that echoes the reference's framing ("The capital
// of France is Paris" → 1.0) and unfairly FAILED a correct-but-CONCISE answer
// ("Paris" → 0.33, missing "capital"/"France"). That is a verbosity bias, not a
// correctness measure — it penalizes ANY framework that answers tersely, and
// terse-correct is good QA behavior. It also can't tell a concise right answer
// ("Paris") from an echo of the QUESTION's subject ("France"), both single
// reference tokens. An explicit answer key fixes both fairly for every
// framework: "Paris" and "The capital is Paris" both PASS (contain the key),
// "France" and "I don't know" both FAIL (do not). See
// docs/architecture/arena-qa-answer-key-scoring.md.
//
// A key with multiple tokens (e.g. "Pepper Miso" for a two-fact recall) requires
// ALL its tokens — an incomplete answer correctly fails. Multiple keys are
// alternatives (any one suffices), so equivalent phrasings ("299792",
// "299,792", "300000") each pass. When NO answer key is given, it falls back to
// the legacy reference token-containment (backward compatible). No LLM, no
// embeddings, no network: pure normalization + set overlap.
func ScoreQA(reply, reference string, answers []string) Score {
	if strings.TrimSpace(reference) == "" && len(answers) == 0 {
		return Score{Value: 0, Pass: false, Detail: "no reference to score against"}
	}
	nr := normalize(reply)
	if reference != "" && nr == normalize(reference) {
		return Score{Value: 1, Pass: true, Detail: "exact match (normalized)"}
	}
	if len(answers) > 0 {
		replyToks := tokenize(nr)
		best := 0.0
		bestKey := ""
		for _, a := range answers {
			at := tokenize(normalize(a))
			if len(at) == 0 {
				continue
			}
			// containmentRatio(want=answer tokens, have=reply tokens): fraction of
			// THIS answer key present in the reply. 1.0 = the full answer is there.
			if r := containmentRatio(at, replyToks); r > best {
				best, bestKey = r, a
			}
		}
		return Score{
			Value:  best,
			Pass:   best >= 1.0, // reply must contain a COMPLETE acceptable answer
			Detail: fmt.Sprintf("answer-key containment %.2f (best key %q of %d)", best, bestKey, len(answers)),
		}
	}
	// Legacy fallback: reference token-containment (tasks without an answer key).
	ratio := containmentRatio(tokenize(normalize(reference)), tokenize(nr))
	return Score{
		Value:  ratio,
		Pass:   ratio >= passQAThreshold,
		Detail: fmt.Sprintf("token-containment %.2f (threshold %.2f)", ratio, passQAThreshold),
	}
}

// ScoreMemory deterministically scores a multi-turn memory recall reply against
// the persisted fact's reference / answer key. Identical mechanics to ScoreQA —
// kept as its own named entry point because memory recall is a distinct arena
// dimension with its own documented pass rule (arenas/live/SPEC.md).
func ScoreMemory(reply, reference string, answers []string) Score {
	return ScoreQA(reply, reference, answers)
}

// ---- scoreRAG -----------------------------------------------------------

var refusalRe = regexp.MustCompile(`(?i)(don't know|do not know|not available|no information|not in the (source|corpus|context|documents)|isn't in|is not in|cannot find|can't find|couldn't find|unable to|does not (state|mention|contain|say|specify|provide)|doesn't (state|mention|contain|say|specify|provide)|no mention|not mentioned|not provided|not specified|not stated|no such|not found|i (do not|don't) have)`)

// LooksLikeRefusal reports whether a reply reads as a "the source does not
// contain this" refusal, rather than an invented answer. Used both for
// rag_trap scoring here and for the runner's item-level signal rendering.
func LooksLikeRefusal(reply string) bool {
	return refusalRe.MatchString(reply)
}

// ScoreRAG deterministically scores one RAG turn. For a grounded question
// (trap=false) it is hit@1: the top retrieved doc ID must be one of the gold
// supporting docs. For a trap question (trap=true) there is no gold doc —
// the correct behavior is to REFUSE rather than invent an answer, so the
// signal is LooksLikeRefusal(reply).
func ScoreRAG(topDocID string, goldDocs []string, reply string, trap bool) Score {
	if trap {
		refused := LooksLikeRefusal(reply)
		if refused {
			return Score{Value: 1, Pass: true, Detail: "trap question correctly refused"}
		}
		return Score{Value: 0, Pass: false, Detail: "trap question NOT refused — likely invented an answer"}
	}
	top := strings.TrimSpace(topDocID)
	if top == "" || len(goldDocs) == 0 {
		return Score{Value: 0, Pass: false, Detail: "no retrieval or no gold docs to check hit@1 against"}
	}
	for _, g := range goldDocs {
		if strings.EqualFold(strings.TrimSpace(g), top) {
			return Score{Value: 1, Pass: true, Detail: "hit@1: top doc " + top + " is a gold doc"}
		}
	}
	return Score{Value: 0, Pass: false, Detail: "miss@1: top doc " + top + " is not a gold doc"}
}

// ---- scoreTools ---------------------------------------------------------

// ToolCall is the scoring package's own minimal, dependency-free view of one
// tool invocation — deliberately NOT the runner's toolCall type, so this
// package stays importable without pulling in the runner's package.
type ToolCall struct {
	Name      string
	Arguments map[string]any
	Executed  bool // true when the call carried a real (non-empty) result
}

// ScoreTools deterministically scores a set of tool calls against the
// expected tool name and expected arguments (arenas/live/tasks/suite.jsonl
// `expected_tool` / `expected_args`). PASS requires: (1) a call with the
// exact expected tool name exists, (2) that call actually executed (carried
// a non-empty result — proves the framework didn't just emit a call it never
// ran), and (3) every expected argument is present with a matching value.
// Argument values are compared after normalization (case-fold strings,
// strip thousands separators, numeric-string equivalence) so "47" and "47.0"
// or "4,183" and "4183" are treated as equal — providers serialize tool
// arguments inconsistently, and this is a genuine formatting difference, not
// a correctness difference.
func ScoreTools(calls []ToolCall, expectedTool string, expectedArgs map[string]any) Score {
	if expectedTool == "" {
		return Score{Value: 0, Pass: false, Detail: "no expected_tool to score against"}
	}
	var match *ToolCall
	for i := range calls {
		if calls[i].Name == expectedTool {
			match = &calls[i]
			break
		}
	}
	if match == nil {
		return Score{Value: 0, Pass: false, Detail: "expected tool " + expectedTool + " was not called"}
	}
	if !match.Executed {
		return Score{Value: 0, Pass: false, Detail: "tool " + expectedTool + " was called but did not execute (empty result)"}
	}
	total := len(expectedArgs)
	if total == 0 {
		return Score{Value: 1, Pass: true, Detail: "tool " + expectedTool + " called and executed (no args to check)"}
	}
	matched := 0
	var mismatches []string
	for _, k := range sortedKeys(expectedArgs) {
		want := expectedArgs[k]
		got, ok := match.Arguments[k]
		if ok && argValuesEqual(want, got) {
			matched++
		} else {
			mismatches = append(mismatches, k)
		}
	}
	ratio := float64(matched) / float64(total)
	pass := matched == total
	detail := fmt.Sprintf("tool %s executed, args %d/%d matched", expectedTool, matched, total)
	if len(mismatches) > 0 {
		detail += " (mismatched: " + strings.Join(mismatches, ",") + ")"
	}
	return Score{Value: ratio, Pass: pass, Detail: detail}
}

// ToolExecuted reports whether any call in calls actually executed (carried
// a non-empty result) — the shared "did the framework really run the tool,
// not just emit a call it never invoked" signal used by both ScoreTools and
// ScoreToolsNumeric.
func ToolExecuted(calls []ToolCall) bool {
	for _, c := range calls {
		if c.Executed {
			return true
		}
	}
	return false
}

// ScoreToolsNumeric deterministically scores the arena's built-in arithmetic
// tool-calling tasks (arenas/live/runner/tasks.go arithmeticTasks): PASS
// requires a tool call that actually executed (ToolExecuted) AND the
// expected numeric answer appearing in the final reply, after stripping
// thousands separators from both sides (so "4,183" matches "4183"). This is
// the numeric-answer counterpart to ScoreTools (exact tool-name + argument
// match): the arithmetic task set does not pin an exact expected argument
// string (a correct call may format "47 * 89" many equivalent ways across
// frameworks), but it does pin an expected numeric RESULT, which is what
// this function verifies.
func ScoreToolsNumeric(calls []ToolCall, reply, expectedNumber string) Score {
	if expectedNumber == "" {
		return Score{Value: 0, Pass: false, Detail: "no expected numeric answer to score against"}
	}
	executed := ToolExecuted(calls)
	correct := numberPresent(reply, expectedNumber)
	switch {
	case executed && correct:
		return Score{Value: 1, Pass: true, Detail: "tool executed and numeric answer " + expectedNumber + " present in reply"}
	case executed:
		return Score{Value: 0.5, Pass: false, Detail: "tool executed but numeric answer " + expectedNumber + " not found in reply"}
	case correct:
		return Score{Value: 0.5, Pass: false, Detail: "numeric answer present but no tool actually executed"}
	default:
		return Score{Value: 0, Pass: false, Detail: "tool not executed and numeric answer not found in reply"}
	}
}

// numberPresent reports whether the expected number appears in the reply,
// after stripping thousands separators from both sides.
func numberPresent(reply, expected string) bool {
	norm := func(s string) string { return strings.ReplaceAll(s, ",", "") }
	return strings.Contains(norm(reply), norm(expected))
}

// ---- scoreStructured ------------------------------------------------------

// ScoreStructured deterministically scores a structured-mode reply: valid
// iff the value is non-nil and marshals to a well-formed JSON object or
// array (not null, not a bare scalar). This is the arena's structured
// contract as currently wired (arenas/live/runner/types.go
// adapterOutput.Structured) — no per-task JSON Schema is threaded through
// today, so "valid" reduces to "the adapter actually produced a structured
// value", which is exactly what schema-following buys you at the top level.
func ScoreStructured(structured any) Score {
	if structured == nil {
		return Score{Value: 0, Pass: false, Detail: "structured field is nil"}
	}
	b, err := json.Marshal(structured)
	if err != nil {
		return Score{Value: 0, Pass: false, Detail: "structured value does not marshal to JSON: " + err.Error()}
	}
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		return Score{Value: 0, Pass: false, Detail: "structured value marshals to null/empty"}
	}
	if !(strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		return Score{Value: 0, Pass: false, Detail: "structured value is a bare scalar, not an object/array"}
	}
	return Score{Value: 1, Pass: true, Detail: "valid JSON object/array"}
}

// ScoreStructuredWithSchema is ScoreStructured plus a minimal, dependency-free
// structural validation against a JSON Schema fragment (supports "type",
// "required", and one level of "properties"/"items" type-checking — enough
// to catch a missing required field or a wrong top-level type without
// pulling in a full external JSON Schema validator). Reserved for when a
// per-task schema is threaded into the scoring call (arenas/live/tasks
// currently carries none); ScoreStructured is what the runner calls today.
func ScoreStructuredWithSchema(structured any, schema []byte) Score {
	base := ScoreStructured(structured)
	if !base.Pass || len(strings.TrimSpace(string(schema))) == 0 {
		return base
	}
	var sch map[string]any
	if err := json.Unmarshal(schema, &sch); err != nil {
		return Score{Value: base.Value, Pass: false, Detail: "schema itself is not valid JSON: " + err.Error()}
	}
	var val any
	b, _ := json.Marshal(structured)
	if err := json.Unmarshal(b, &val); err != nil {
		return Score{Value: 0, Pass: false, Detail: "structured value round-trip failed: " + err.Error()}
	}
	if problems := validateAgainstSchema(val, sch); len(problems) > 0 {
		return Score{Value: 0, Pass: false, Detail: "schema violations: " + strings.Join(problems, "; ")}
	}
	return Score{Value: 1, Pass: true, Detail: "valid JSON object/array, schema-conformant"}
}

func validateAgainstSchema(val any, schema map[string]any) []string {
	var problems []string
	if wantType, ok := schema["type"].(string); ok {
		if !jsonTypeMatches(val, wantType) {
			problems = append(problems, fmt.Sprintf("type: want %q, got %s", wantType, jsonTypeName(val)))
		}
	}
	obj, isObj := val.(map[string]any)
	if req, ok := schema["required"].([]any); ok && isObj {
		for _, r := range req {
			key, _ := r.(string)
			if key == "" {
				continue
			}
			if _, present := obj[key]; !present {
				problems = append(problems, "missing required field "+key)
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok && isObj {
		for _, key := range sortedKeysAny(props) {
			propSchema, _ := props[key].(map[string]any)
			if propSchema == nil {
				continue
			}
			fieldVal, present := obj[key]
			if !present {
				continue // absence is a "required" concern, handled above
			}
			if wantType, ok := propSchema["type"].(string); ok && !jsonTypeMatches(fieldVal, wantType) {
				problems = append(problems, fmt.Sprintf("field %s: want type %q, got %s", key, wantType, jsonTypeName(fieldVal)))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func jsonTypeMatches(v any, want string) bool {
	switch want {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	default:
		return true // unknown declared type — do not fail on it
	}
}

// ---- scoreStream ----------------------------------------------------------

// ScoreStream deterministically scores a stream-mode turn: PASS iff a
// positive time-to-first-token was actually measured and reported by the
// adapter. This intentionally does NOT impose an arbitrary latency SLA
// (e.g. "TTFT must be under Nms") — that threshold would bias the score
// toward whatever number this package's author picked, which is exactly the
// kind of non-objective judgment this package exists to avoid. "Was TTFT
// measured at all" is the objective, framework-agnostic fact; the actual ms
// value is reported in RESULTS.md's benchmark tables for comparison.
func ScoreStream(ttftMs int64) Score {
	if ttftMs <= 0 {
		return Score{Value: 0, Pass: false, Detail: "no positive TTFT reported"}
	}
	return Score{Value: 1, Pass: true, Detail: fmt.Sprintf("TTFT measured: %dms", ttftMs)}
}

// ---- shared normalization helpers -----------------------------------------

var punctRe = regexp.MustCompile(`[^\w\s]`)
var spaceRe = regexp.MustCompile(`\s+`)

// normalize lowercases, strips punctuation, and collapses whitespace — a
// pure, deterministic, locale-independent (byte-based \w) transform.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = punctRe.ReplaceAllString(s, " ")
	s = spaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// stopWords are excluded from the containment ratio so it measures content
// overlap, not shared function words every English sentence has.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "was": true,
	"were": true, "of": true, "in": true, "on": true, "at": true, "to": true,
	"and": true, "or": true, "it": true, "this": true, "that": true, "be": true,
	"has": true, "have": true, "with": true, "for": true, "as": true,
}

func tokenize(normalized string) []string {
	if normalized == "" {
		return nil
	}
	var out []string
	for _, tok := range strings.Split(normalized, " ") {
		if tok == "" || stopWords[tok] {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// containmentRatio returns what fraction of `want`'s DISTINCT tokens appear
// in `have`. Order-independent, duplicate-independent, deterministic.
func containmentRatio(want, have []string) float64 {
	if len(want) == 0 {
		return 0
	}
	haveSet := map[string]bool{}
	for _, t := range have {
		haveSet[t] = true
	}
	wantSet := map[string]bool{}
	for _, t := range want {
		wantSet[t] = true
	}
	hit := 0
	for t := range wantSet {
		if haveSet[t] {
			hit++
		}
	}
	return float64(hit) / float64(len(wantSet))
}

// argValuesEqual compares two tool-argument values with light normalization:
// strings are compared case-folded with thousands separators stripped;
// numbers compare by float64 value regardless of int/float/string-numeric
// representation (JSON round-tripping and different SDKs disagree on this).
func argValuesEqual(want, got any) bool {
	wf, wok := toFloat(want)
	gf, gok := toFloat(got)
	if wok && gok {
		return wf == gf
	}
	ws := normalizeArgString(fmt.Sprintf("%v", want))
	gs := normalizeArgString(fmt.Sprintf("%v", got))
	return ws == gs
}

func normalizeArgString(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, ",", "")
	return s
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(t), ",", "")
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysAny(m map[string]any) []string {
	return sortedKeys(m)
}
