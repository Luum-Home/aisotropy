package main

import (
	"encoding/json"

	"aisotropy-live-runner/scoring"
)

// ---- multi-mode adapter contract (stdin) -----------------------------------
//
// Every adapter (arenas/live/<fw>/run.sh and run-docker.sh) reads exactly one
// JSON object of this shape from stdin. The "mode" field selects the behavior.
// See arenas/live/SPEC.md, arenas/live/_shared/README.md and any adapter README.

type inMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type inTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// adapterInput is one invocation's request. Fields the selected mode does not
// use are omitted (omitempty) so a single-mode adapter that only reads
// system/messages still parses it unchanged.
type adapterInput struct {
	Mode       string          `json:"mode"`
	System     string          `json:"system,omitempty"`
	Messages   []inMessage     `json:"messages"`
	Tools      []inTool        `json:"tools,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	TopK       int             `json:"top_k,omitempty"`
	Schema     json.RawMessage `json:"schema,omitempty"`
	SchemaName string          `json:"schema_name,omitempty"`
}

// ---- multi-mode adapter contract (stdout) ----------------------------------

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Result    string         `json:"result,omitempty"`
}

type retrievedDoc struct {
	DocID string  `json:"doc_id"`
	Title string  `json:"title"`
	Score float64 `json:"score"`
	Text  string  `json:"text"`
}

type adapterOutput struct {
	Framework    string         `json:"framework"`
	Mode         string         `json:"mode"`
	Model        string         `json:"model"`
	Reply        string         `json:"reply"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	LatencyMs    int64          `json:"latency_ms"`
	ToolCalls    []toolCall     `json:"tool_calls"`
	Retrieved    []retrievedDoc `json:"retrieved"`
	Structured   any            `json:"structured"`
	TTFTMs       int64          `json:"ttft_ms"`
	Error        string         `json:"error"`
}

// ---- suite task (arenas/live/tasks/suite.jsonl) ----------------------------

type task struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	System       string          `json:"system"`
	Messages     []inMessage     `json:"messages"`
	Tools        json.RawMessage `json:"tools"`
	Reference    string          `json:"reference"`
	ExpectedTool string          `json:"expected_tool"`
	ExpectedArgs map[string]any  `json:"expected_args"`
}

// ---- rag question (arenas/live/_shared/rag_questions.jsonl) -----------------

type ragQuestion struct {
	ID             string   `json:"id"`
	Question       string   `json:"question"`
	Reference      string   `json:"reference"`
	SupportingDocs []string `json:"supporting_docs"`
	Trap           bool     `json:"trap"`
}

// ---- unit of scored work ---------------------------------------------------
//
// One workItem produces one scored record. Most modes run a single adapter
// invocation; memory mode runs two invocations on a shared session_id (a
// persist call, then a fresh recall call) — the whole point of the memory
// dimension is that the fact survives a separate process invocation.

type workItem struct {
	Framework string
	Mode      string
	ID        string
	Kind      string // scoring discriminator: qa|structured|stream|memory|rag_grounded|rag_trap|tools

	Invocations []adapterInput // 1, or 2 for memory (persist then recall)

	// judge inputs
	Question  string
	Reference string

	// rag
	SupportingDocs []string

	// tools
	ExpectedNumber string
}

// ---- per-run record --------------------------------------------------------

type record struct {
	Framework string
	Mode      string
	ID        string
	Kind      string

	Out    adapterOutput
	WallMs int64 // runner-measured wall clock of the final (scored) invocation
	SumIn  int   // provider input tokens summed across invocations (memory)
	SumOut int   // provider output tokens summed across invocations
	HasTok bool  // true when every invocation reported usage >= 0

	RunErr string // runner-side failure (adapter crash / timeout / bad JSON)

	// Det is the PRIMARY, always-computed deterministic score
	// (arenas/live/runner/scoring) — pure, reproducible, no LLM/network/
	// randomness. See docs/architecture/arena-deterministic-judging.md.
	Det scoring.Score

	// judged quality (0 = not judged) — SECONDARY, opt-in (-llm-judge), and
	// caveated: an LLM-as-judge on the same model family as the contestants.
	// See writeHeader's self-judge caveat in results.go.
	Correctness  int
	Helpfulness  int
	Faithfulness int
	JudgeNote    string

	// mode-specific correctness signals
	RetrievalHit  bool // rag_grounded: retrieved[0].doc_id ∈ supporting_docs
	TrapRefused   bool // rag_trap: reply declined instead of inventing
	ToolExecuted  bool // tools: a tool_call carried a real Result
	AnswerCorrect bool // tools: the expected number appears in the reply
	StructuredOK  bool // structured: structured field parsed to a non-null object
	TTFTMs        int64
}

func (r record) failed() bool { return r.RunErr != "" || r.Out.Error != "" }
