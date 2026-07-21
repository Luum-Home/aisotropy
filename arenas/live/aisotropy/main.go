// LIVE arena adapter for aisotropy — the REFERENCE adapter, EXHAUSTIVE
// multi-mode edition.
//
// It honors the uniform live-arena multi-mode contract (see the sibling adapters
// under arenas/live/* and arenas/live/_shared/README.md): read ONE JSON object
// from stdin (a "mode" field selects the behavior), run aisotropy's REAL runtime
// against the real agnes-2.0-flash model AND aisotropy's real backend tiers (the
// e5 embed server, real Qdrant, real rqlite, on-disk BadgerDB) — not the
// in-process stub defaults — and write EXACTLY one JSON object to stdout (all logs
// to stderr).
//
// "The framework" here is aisotropy's public façade
// (github.com/Luum-Home/aisotropy/aisotropy). Every mode drives a DISTINCT real
// capability of that façade, at parity with a production consumer:
//
//	qa         — App.Turn single-shot (in-process chat surface, full pipeline).
//	rag        — ingest the Nimbus-Rail corpus into aisotropy's OWN semantic tier
//	             (IngestKnowledge -> e5 -> real Qdrant, tenant-scoped), then answer
//	             by hydrating through aisotropy's real tiered memory (App.Turn).
//	tools      — aisotropy's real tool-EXECUTION route: boot App.Run and POST
//	             /v1/.../chat/agent so a registered `calculate` tool is actually
//	             executed and its result folded into the answer (App.Turn cannot
//	             surface tool_calls; the agent route is the real seam, ADR-0012).
//	memory     — aisotropy's real tiered memory ACROSS TWO process invocations:
//	             persist a fact to on-disk BadgerDB + rqlite, recall it in a fresh
//	             process on the same tenant/user/session.
//	structured — aisotropy's fail-closed reflection pipeline (App.Turn -> ChatCore
//	             -> GollemReflect) producing a validated typed JSON object.
//	stream     — aisotropy's real SSE streaming (POST /v1/.../chat/stream), with
//	             time-to-first-token measured.
//	wasm_tool  — aisotropy's DIFFERENTIATOR: a tool body executed inside a wazero
//	             WASM sandbox (the capability no other framework here has), its
//	             result folded into a real agnes turn.
//	memory_scoped / isolation_leak — aisotropy's BY-CONSTRUCTION multi-tenant
//	             isolation: a fact written under one (tenant,user,session) is
//	             recalled there but never surfaces to a different tenant, enforced
//	             by the working-memory key prefix + ctx-scoped facts/semantic
//	             filters — NOT an adapter-side key/filter (see mode_scoped.go).
//	crash_recovery — aisotropy's on-disk BadgerDB durability across a hard
//	             kill -9: a fresh process re-opens AISOTROPY_DATA_DIR and recalls
//	             a pre-crash fact (see mode_crash.go).
//	observability — aisotropy's NATIVE OpenTelemetry span emission
//	             (internal/llm/otel.go, no bridge, no auto-instrumentation),
//	             captured in-process via a loopback-only OTLP/HTTP receiver this
//	             adapter stands up itself (no external collector), across one
//	             representative App.Turn call (see mode_observability.go).
//	middleware — aisotropy's NATIVE, bounded model-middleware seam
//	             (aisotropy.WithModelMiddleware, ADR-0062): a registered
//	             ModelMiddleware transforms the system prompt INBOUND and wraps
//	             the token stream OUTBOUND, running strictly INSIDE the
//	             constitutional pre-spend quota gate and circuit breaker (see
//	             mode_middleware.go).
//
// ARENA_STUB=1 swaps aisotropy's real agnes provider client for its OWN
// keyless internal/llm.StubClient (no OPENAI_API_KEY needed) and clears the
// real-backend-tier env vars run.sh exports, so EVERY mode above runs fully
// offline against aisotropy's bare-binary in-process defaults (see
// newAgnesClient in agnes_client.go and stubMode/clearBackendTierEnv in
// app.go).
//
// The API key is read from OPENAI_API_KEY and never printed, logged, or embedded.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

const frameworkName = "aisotropy"

// Fixed isolation identity for the arena. tenant_id/user_id travel via
// aisotropy's context propagation from here down every memory tier.
const (
	arenaTenant = "arena"
	arenaUser   = "live"
)

const (
	modeQA         = "qa"
	modeRAG        = "rag"
	modeTools      = "tools"
	modeMemory     = "memory"
	modeStructured = "structured"
	modeStream     = "stream"
	modeWASMTool   = "wasm_tool"

	// Multi-tenant isolation + durability modes (parity with the framework
	// adapters), driven through aisotropy's NATIVE, by-construction seams —
	// working-memory key namespacing (tenant:{t}:user:{u}:...) and the
	// ctx-scoped hydration stack — never an adapter-side compound key or filter.
	// See mode_scoped.go and mode_crash.go.
	modeMemoryScoped  = "memory_scoped"
	modeIsolationLeak = "isolation_leak"
	modeCrashRecovery = "crash_recovery"

	// modeObservability measures aisotropy's NATIVE OTel span emission for one
	// representative turn, in-process — see mode_observability.go.
	modeObservability = "observability"

	// modeMiddleware drives one turn through aisotropy's NATIVE, bounded
	// model-middleware seam (aisotropy.WithModelMiddleware, ADR-0062) and
	// reports whether the inbound (system-prompt transform) and outbound
	// (token-stream wrap) hooks genuinely fired — see mode_middleware.go.
	modeMiddleware = "middleware"
)

// ---- Request contract (stdin) ----

type inMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type inTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type request struct {
	Mode      string      `json:"mode"`
	System    string      `json:"system"`
	Messages  []inMessage `json:"messages"`
	Tools     []inTool    `json:"tools"`
	SessionID string      `json:"session_id"` // memory mode: correlates turns across process invocations
	TopK      int         `json:"top_k"`      // rag mode: retrieval depth (default 4)

	// Multi-tenant scope carried through aisotropy's REAL identity path
	// (ChatInput.TenantID/UserID -> ctx -> every memory tier's key/filter).
	// Used by memory_scoped, isolation_leak, and crash_recovery — see
	// mode_scoped.go / mode_crash.go. The scope is enforced BY CONSTRUCTION
	// inside aisotropy (key-prefix validation + Qdrant in-scan payload filter),
	// NOT by this adapter.
	TenantID  string `json:"tenant_id"`
	UserID    string `json:"user_id"`
	ProductID string `json:"product_id"`
	Fact      string `json:"fact"`             // write/seed payload: persisted as a real user turn under the scope above
	Query     string `json:"query"`            // recall/probe payload: asked as a real turn against the scope above
	ExpectSub string `json:"expect_substring"` // recall/probe/resume: substring the reply must contain to count as a scoped hit / leak / pre-crash recall

	// crash_recovery mode — exactly one of CrashTurns (producer/process A,
	// SIGKILLed mid-turn) or Messages (resumer/process B, fresh process) is set.
	CrashTurns []string `json:"crash_turns"`
}

// ---- Response contract (stdout) ----

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Result    string         `json:"result,omitempty"` // populated only when the tool was actually executed
}

type retrievedDoc struct {
	DocID string  `json:"doc_id"`
	Title string  `json:"title"`
	Score float64 `json:"score"`
	Text  string  `json:"text"`
}

// scopedRecall is the memory_scoped-mode "scoped_recall" output. same_scope_hit
// and cross_tenant_miss are two named views of ONE observation: whether asking
// aisotropy under the given identity surfaced the scoped fact. A recall issued
// for the scope the fact was written under reports same_scope_hit=true; a recall
// issued as a different tenant (whose key prefix never matches) reports
// cross_tenant_miss=true. The observable is aisotropy's own hydrated reply — the
// adapter applies no filtering of its own.
type scopedRecall struct {
	SameScopeHit    bool   `json:"same_scope_hit"`
	CrossTenantMiss bool   `json:"cross_tenant_miss"`
	RecalledReply   string `json:"recalled_reply"`
}

type result struct {
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

	// memory_scoped mode only (null otherwise) — see mode_scoped.go.
	ScopedRecall *scopedRecall `json:"scoped_recall"`
	// isolation_leak mode only (null otherwise); false = no leak (good).
	LeakDetected *bool `json:"leak_detected"`
	// crash_recovery mode only (null otherwise), populated by the resumer
	// (process B) — see mode_crash.go.
	Resumed              *bool `json:"resumed"`
	PrecrashFactRecalled *bool `json:"precrash_fact_recalled"`
	StateLost            *bool `json:"state_lost"`
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "PROGRESS: "+format+"\n", a...)
}

func modelName() string {
	if m := os.Getenv("OPENAI_MODEL"); m != "" {
		return m
	}
	if stubMode() {
		// Honest label: ARENA_STUB=1 never calls agnes-2.0-flash at all — the
		// model actually answering is aisotropy's native internal/llm.StubClient.
		return "aisotropy-stub"
	}
	return "agnes-2.0-flash"
}

// emit writes exactly one JSON object to stdout, normalizing nil slices to [] so
// the wire shape is stable.
func emit(r result) {
	r.Framework = frameworkName
	if r.Model == "" {
		r.Model = modelName()
	}
	if r.ToolCalls == nil {
		r.ToolCalls = []toolCall{}
	}
	if r.Retrieved == nil {
		r.Retrieved = []retrievedDoc{}
	}
	b, err := json.Marshal(r)
	if err != nil {
		fmt.Printf(`{"framework":%q,"mode":%q,"model":%q,"reply":"","input_tokens":-1,"output_tokens":-1,"latency_ms":0,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"marshal result: %s"}`+"\n",
			frameworkName, r.Mode, modelName(), err.Error())
		return
	}
	fmt.Println(string(b))
}

// emitFinal writes the final stdout JSON object. mode=observability and
// mode=middleware do NOT use the shared `result` envelope (see
// mode_observability.go's observabilityOutput and mode_middleware.go's
// middlewareOutput doc comments) — each stashes its own distinct output type
// in out.Structured, encoded here instead of out itself, matching the
// sibling eino/genkit-go observability/middleware adapters.
func emitFinal(out *result) {
	switch out.Mode {
	case modeObservability:
		obs, ok := out.Structured.(*observabilityOutput)
		if !ok {
			// dispatch failed before reaching runObservability (e.g. bad stdin
			// JSON) — still emit a valid observability-shaped envelope rather
			// than the generic one.
			obs = &observabilityOutput{Framework: frameworkName, Mode: modeObservability, Error: out.Error}
		}
		b, err := json.Marshal(obs)
		if err != nil {
			fmt.Printf(`{"framework":%q,"mode":%q,"otel_supported":false,"span_count":0,"span_names":[],"span_timings_ms":{},"logs_structured":false,"log_sample":"","notes":"","error":"marshal observability result: %s"}`+"\n",
				frameworkName, modeObservability, err.Error())
			return
		}
		fmt.Println(string(b))
	case modeMiddleware:
		mw, ok := out.Structured.(*middlewareOutput)
		if !ok {
			// dispatch failed before reaching runMiddleware (e.g. bad stdin
			// JSON) — still emit a valid middleware-shaped envelope rather
			// than the generic one.
			mw = &middlewareOutput{Framework: frameworkName, Mode: modeMiddleware, Error: out.Error}
		}
		b, err := json.Marshal(mw)
		if err != nil {
			fmt.Printf(`{"framework":%q,"mode":%q,"text":"","middleware":null,"error":"marshal middleware result: %s"}`+"\n",
				frameworkName, modeMiddleware, err.Error())
			return
		}
		fmt.Println(string(b))
	default:
		emit(*out)
	}
}

func main() {
	out := result{Mode: modeQA, Model: modelName()}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		out.Error = fmt.Sprintf("read stdin: %v", err)
		out.InputTokens, out.OutputTokens = -1, -1
		emitFinal(&out)
		os.Exit(1)
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		out.Error = fmt.Sprintf("parse stdin JSON: %v", err)
		out.InputTokens, out.OutputTokens = -1, -1
		emitFinal(&out)
		os.Exit(1)
	}
	if req.Mode == "" {
		req.Mode = modeQA
	}
	out.Mode = req.Mode

	if err := dispatch(&req, &out); err != nil {
		out.Error = err.Error()
		if out.InputTokens == 0 && out.OutputTokens == 0 {
			out.InputTokens, out.OutputTokens = -1, -1
		}
		fmt.Fprintln(os.Stderr, "aisotropy-live-adapter: "+err.Error())
		emitFinal(&out)
		os.Exit(1)
	}
	emitFinal(&out)
}

func dispatch(req *request, out *result) error {
	logf("aisotropy adapter: mode=%s, %d msg(s), %d tool(s), model=%s", req.Mode, len(req.Messages), len(req.Tools), modelName())
	// F1 (arena-memory-forensics): isolate the on-disk working-memory data dir
	// across DIFFERENT arena runs while preserving it across the SAME run's
	// paired process invocations (memory persist->recall, crash_recovery
	// producer->resumer) — see datadir_isolation.go for the full mechanism and
	// why a naive wipe-on-every-boot would break crash_recovery.
	isolateWorkingMemoryDataDir()
	// Ensure the real networked rqlite facts tier can migrate cleanly on this
	// boot (see rqlite_reset.go for the aisotropy migration defect this sidesteps).
	resetRqliteFacts()
	start := time.Now()
	var err error
	switch req.Mode {
	case modeQA:
		err = runQA(req, out)
	case modeRAG:
		err = runRAG(req, out)
	case modeTools:
		err = runTools(req, out)
	case modeMemory:
		err = runMemory(req, out)
	case modeStructured:
		err = runStructured(req, out)
	case modeStream:
		err = runStream(req, out)
	case modeWASMTool:
		err = runWASMTool(req, out)
	case modeMemoryScoped:
		err = runMemoryScoped(req, out)
	case modeIsolationLeak:
		err = runIsolationLeak(req, out)
	case modeCrashRecovery:
		err = runCrashRecovery(req, out)
	case modeObservability:
		err = runObservability(req, out)
	case modeMiddleware:
		err = runMiddleware(req, out)
	default:
		return fmt.Errorf("unknown mode %q (want one of qa|rag|tools|memory|structured|stream|wasm_tool|memory_scoped|isolation_leak|crash_recovery|observability|middleware)", req.Mode)
	}
	if out.LatencyMs == 0 {
		out.LatencyMs = time.Since(start).Milliseconds()
	}
	return err
}

// userMessages returns the content of user-role messages in order.
func userMessages(msgs []inMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" {
			out = append(out, m.Content)
		}
	}
	return out
}

// lastUserMessage returns the final user turn — the SPEC's "one to answer".
func lastUserMessage(msgs []inMessage) (string, error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content, nil
		}
	}
	return "", fmt.Errorf("no user message found in messages[]")
}
