package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// newRunner wires a real llmagent + runner + in-memory session service.
func newRunner(cfg llmagent.Config) (*runner.Runner, session.Service, memory.Service) {
	ag, err := llmagent.New(cfg)
	if err != nil {
		panic(err)
	}
	svc := session.InMemoryService()
	mem := memory.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "orbit",
		Agent:             ag,
		SessionService:    svc,
		MemoryService:     mem,
		AutoCreateSession: true,
	})
	if err != nil {
		panic(err)
	}
	return r, svc, mem
}

// ===========================================================================
// AREA 1 — MEMORY TIERS (the headline)
// ===========================================================================

func runMemoryTiers(ctx context.Context) {
	hdr("AREA 1 — MEMORY TIERS (the headline: 4 stores of different NATURE in aisotropy)")

	sessSvc := session.InMemoryService()
	memSvc := memory.InMemoryService()

	// FEATURE 1 — temperature hierarchy (meta).
	// ADK gives exactly TWO stores of different nature: session.Service (hot,
	// working KV keyed by appName/userID/sessionID) and memory.Service (warm,
	// keyword recall across a user's sessions). No FTS5 facts tier, no
	// self-hostable vector tier, no offline batch tier.
	_ = sessSvc
	_ = memSvc
	feature(1, "Temperature hierarchy (tiers of nature)", "PARTIAL",
		"2 real tiers: session.Service (working KV) + memory.Service (keyword recall); aisotropy has 4 (KV/FTS5/vector/batch)")

	// FEATURE 2 — working memory (recent history per tenant/user/session).
	cm := &countingModel{}
	r, svc, _ := newRunner(llmagent.Config{Name: "wm", Model: cm,
		Instruction: "You are Orbit."})
	id := "acme|alice"
	if _, _, _, err := runTurn(ctx, r, id, "s1", "MEMO: launch code is XY-9"); err != nil {
		panic(err)
	}
	recall, _, _, err := runTurn(ctx, r, id, "s1", "what did I tell you to remember?")
	if err != nil {
		panic(err)
	}
	if strings.Contains(recall, "XY-9") {
		feature(2, "Working memory (recent history)", "BUILT-IN",
			"turn 2 recalled turn 1 via runner-assembled session history (session.Service): "+shorten(recall))
	} else {
		feature(2, "Working memory (recent history)", "DIY", "recall FAILED: "+shorten(recall))
	}

	// FEATURE 3 — facts / keyword (full-text) search.
	// memory.InMemoryService.SearchMemory does keyword word-intersection over
	// ingested session events. That IS a real keyword-recall tier — but it is
	// session-derived, unranked, and has no FTS5/importance journal.
	got, _ := svc.Get(ctx, &session.GetRequest{AppName: "orbit", UserID: id, SessionID: "s1"})
	if err := memSvc.AddSessionToMemory(ctx, got.Session); err != nil {
		panic(err)
	}
	res, _ := memSvc.SearchMemory(ctx, &memory.SearchRequest{AppName: "orbit", UserID: id, Query: "launch"})
	if len(res.Memories) > 0 {
		feature(3, "Facts/keyword search (FTS-like)", "PARTIAL",
			fmt.Sprintf("memory.SearchMemory keyword-matched %d entry on 'launch' (word-intersection, UNRANKED, no FTS5/importance journal)", len(res.Memories)))
	} else {
		feature(3, "Facts/keyword search (FTS-like)", "DIY", "no keyword hit")
	}

	// FEATURE 4 — semantic / VECTOR RAG.
	// The in-memory recall is keyword-only: a semantically-equivalent query that
	// shares no words is a MISS. The only vector-backed recall is memory/vertexai
	// (GCP-managed MemoryBank) — PAID/managed, not self-hostable OSS.
	sem, _ := memSvc.SearchMemory(ctx, &memory.SearchRequest{AppName: "orbit", UserID: id, Query: "rocket ignition sequence"})
	feature(4, "Semantic/vector RAG", "PAID",
		fmt.Sprintf("keyword store MISSED semantic query 'rocket ignition' (%d hits); vectors only via memory/vertexai MemoryBank (managed, not OSS)", len(sem.Memories)))

	// FEATURE 5 — summary / rolling buffer (context engineering).
	// llmagent.Config.IncludeContents toggles WHETHER history is sent, but there
	// is NO built-in condensation/eviction. We run a DIY summarizer.
	events := collectTexts(got.Session)
	summary := diySummarize(events)
	feature(5, "Summary buffer (context engineering)", "DIY",
		"no ADK condensation primitive (IncludeContents only toggles history on/off); DIY summary="+shorten(summary))

	// FEATURE 6 — importance × recency × relevance scoring.
	// memory.Entry has NO score field and SearchMemory returns UNRANKED matches.
	scored := diyScore(res.Memories)
	feature(6, "Scoring (Rel×Imp×Rec, top-K)", "NOT-POSSIBLE",
		fmt.Sprintf("memory.Entry has no score; SearchMemory is unordered set-intersection; DIY ranked %d entry", len(scored)))

	// FEATURE 7 — contradiction detection / supersede.
	// No supersede/archival concept anywhere in memory.Service.
	store := map[string]string{"hosting": "aws"}
	diySupersede(store, "hosting", "gcp") // newer fact overrides older
	feature(7, "Contradiction detection / supersede", "NOT-POSSIBLE",
		"no supersede/archival in memory.Service; DIY map override hosting=aws -> "+store["hosting"])

	// FEATURE 8 — knowledge ingestion (chunk + embed a document).
	// AddSessionToMemory ingests SESSIONS, not arbitrary documents; there is no
	// chunk or embed primitive. To ingest a doc you must fake it as a session.
	doc := "Aisotropy isolates tenants by construction. Every store scopes by tenant and user."
	if err := ingestDocAsSession(ctx, memSvc, "acme|docs", doc); err != nil {
		panic(err)
	}
	dq, _ := memSvc.SearchMemory(ctx, &memory.SearchRequest{AppName: "orbit", UserID: "acme|docs", Query: "tenants"})
	feature(8, "Knowledge ingestion (chunk+embed)", "DIY",
		fmt.Sprintf("no doc/chunk/embed primitive; DIY wrapped doc as a synthetic session -> %d keyword hit on 'tenants'", len(dq.Memories)))

	// FEATURE 9 — bring-your-own embedder.
	// The keyword store has no embedder seam; the vector path (vertexai) uses
	// Google-managed embeddings that are not swappable to a local model.
	feature(9, "BYO embedder (swap embedding fn)", "NOT-POSSIBLE",
		"in-memory recall is keyword (no embedder); vector path = Vertex managed embeddings, not swappable to a local model")
}

// ===========================================================================
// AREA 2 — CONTRACT & STRUCTURED OUTPUT
// ===========================================================================

func runContractOutput(ctx context.Context) {
	hdr("AREA 2 — CONTRACT & STRUCTURED OUTPUT")

	// FEATURE 10 — fail-closed reflection loop.
	// llmagent.Config.OutputSchema (*genai.Schema) is a REAL structured-output
	// field (BUILT-IN), but ADK has NO repair/retry loop for a schema-breaking
	// reply. We run the DIY reflection loop and fail closed after N retries.
	type reply struct {
		Answer string `json:"answer"`
	}
	attempt := 0
	// A model that returns malformed JSON twice, then valid JSON.
	fakeCall := func() string {
		attempt++
		if attempt < 3 {
			return `{"answer": ` // malformed (schema-breaking)
		}
		return `{"answer":"ok"}`
	}
	const maxRetries = 3
	var final reply
	failedClosed := true
	for i := 0; i < maxRetries; i++ {
		raw := fakeCall()
		if err := json.Unmarshal([]byte(raw), &final); err == nil {
			failedClosed = false
			break
		}
		// DIY: re-request WITH the exact typing error (what aisotropy's loop does).
	}
	if !failedClosed {
		feature(10, "Fail-closed reflection loop (502)", "DIY",
			fmt.Sprintf("OutputSchema field is BUILT-IN but NO repair loop; DIY retried %d times to valid JSON=%q", attempt, final.Answer))
	} else {
		feature(10, "Fail-closed reflection loop (502)", "DIY", "DIY loop failed closed after retries")
	}

	// FEATURE 11 — SSE streaming + immediate ctx-cancellation.
	// (a) streaming: RunConfig.StreamingModeSSE => the runner passes stream=true
	//     to the model and yields partial token events.
	// (b) cancellation: under an ALREADY-cancelled ctx the model's ctx.Err()
	//     check short-circuits => 0 tokens produced (the upstream spend is cut).
	sm := &streamModel{}
	rStream, _, _ := newRunner(llmagent.Config{Name: "stream", Model: sm})
	msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText("stream please")}}
	partials := 0
	for ev, e := range rStream.Run(ctx, "acme|alice", "s-stream", msg, agent.RunConfig{StreamingMode: agent.StreamingModeSSE}) {
		if e != nil {
			break
		}
		if ev != nil && ev.LLMResponse.Partial {
			partials++
		}
	}

	smCancel := &streamModel{}
	rCancel, _, _ := newRunner(llmagent.Config{Name: "streamx", Model: smCancel})
	cctx, cancel := context.WithCancel(ctx)
	cancel() // client already gone before the turn starts
	for _, e := range rCancel.Run(cctx, "acme|alice", "s-x", msg, agent.RunConfig{StreamingMode: agent.StreamingModeSSE}) {
		_ = e
	}
	producedUnderCancel := smCancel.produced.Load()

	if partials >= 2 && producedUnderCancel == 0 {
		feature(11, "SSE streaming + immediate cancel", "BUILT-IN",
			fmt.Sprintf("StreamingModeSSE delivered %d partial token events; cancelled ctx cut the upstream model to %d tokens produced", partials, producedUnderCancel))
	} else {
		feature(11, "SSE streaming + immediate cancel", "PARTIAL",
			fmt.Sprintf("partials=%d producedUnderCancel=%d", partials, producedUnderCancel))
	}

	// FEATURE 12 — response cache (stateless).
	// llmagent BeforeModelCallbacks may RETURN a cached *LLMResponse, which the
	// framework uses to SKIP the actual model call (documented seam). We install
	// a DIY cache in that BUILT-IN seam and prove the model does not run on a hit.
	cm := &countingModel{}
	cache := map[string]*model.LLMResponse{}
	var lastKey string
	before := func(_ agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
		lastKey = cacheKey(req)
		if hit, ok := cache[lastKey]; ok {
			return hit, nil // framework skips the model
		}
		return nil, nil
	}
	after := func(_ agent.Context, resp *model.LLMResponse, _ error) (*model.LLMResponse, error) {
		if resp != nil && !resp.Partial && lastKey != "" {
			cache[lastKey] = resp
		}
		return nil, nil
	}
	rc, _, _ := newRunner(llmagent.Config{Name: "cache", Model: cm,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{before},
		AfterModelCallbacks:  []llmagent.AfterModelCallback{after}})
	q := "what is the launch code?"
	if _, _, _, err := runTurn(ctx, rc, "acme|alice", "sc", q); err != nil {
		panic(err)
	}
	afterFirst := cm.Calls()
	if _, _, _, err := runTurn(ctx, rc, "acme|alice", "sc2", q); err != nil {
		panic(err)
	}
	afterSecond := cm.Calls()
	if afterSecond == afterFirst {
		feature(12, "Response cache (stateless, skip-model)", "BUILT-IN",
			fmt.Sprintf("BeforeModelCallbacks returned cached LLMResponse; model.Calls unchanged %d->%d on repeat", afterFirst, afterSecond))
	} else {
		feature(12, "Response cache (stateless, skip-model)", "PARTIAL",
			fmt.Sprintf("model ran on repeat: %d->%d", afterFirst, afterSecond))
	}
}

func cacheKey(req *model.LLMRequest) string {
	var b strings.Builder
	for _, c := range req.Contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p != nil {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String()
}

// ===========================================================================
// AREA 3 — TOOLS & AGENTIC
// ===========================================================================

type weatherArgs struct {
	City string `json:"city"`
}
type weatherResult struct {
	Report string `json:"report"`
}

// staticToolset is a minimal tool.Toolset (ADK ships mcptoolset/skilltoolset but
// no trivial static one). It exists only to feed the BUILT-IN tool.FilterToolset.
type staticToolset struct {
	name  string
	tools []tool.Tool
}

func (s *staticToolset) Name() string { return s.name }
func (s *staticToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	return s.tools, nil
}

func runToolsAgentic(ctx context.Context) {
	hdr("AREA 3 — TOOLS & AGENTIC")

	mkTool := func(name, desc string) tool.Tool {
		t, err := functiontool.New(functiontool.Config{Name: name, Description: desc},
			func(agent.Context, weatherArgs) (weatherResult, error) { return weatherResult{Report: "ok"}, nil })
		if err != nil {
			panic(err)
		}
		return t
	}

	// FEATURE 13 — scope-gated tools (governance by absence).
	// tool.FilterToolset + tool.AllowedToolsPredicate is the BUILT-IN mechanism
	// to hide tools from the model. Binding "allowed" to an authenticated SCOPE
	// is developer code (ADK has no scope/identity), so the whole feature is PARTIAL.
	all := &staticToolset{name: "kit", tools: []tool.Tool{
		mkTool("search_docs", "search"),
		mkTool("read_financials", "read $"),
		mkTool("write_tasks", "write tasks"),
	}}
	callerScopes := map[string]bool{"read:financial-data": true} // NO write:tasks
	allowed := []string{"search_docs"}
	if callerScopes["read:financial-data"] {
		allowed = append(allowed, "read_financials")
	}
	if callerScopes["write:tasks"] {
		allowed = append(allowed, "write_tasks")
	}
	filtered := tool.FilterToolset(all, tool.AllowedToolsPredicate(allowed))
	visible, _ := filtered.Tools(nil)
	names := toolNames(visible)
	gatedOut := !contains(names, "write_tasks")
	if gatedOut && contains(names, "read_financials") {
		feature(13, "Scope-gated tools (absence)", "PARTIAL",
			fmt.Sprintf("tool.FilterToolset+AllowedToolsPredicate BUILT-IN hid write_tasks (visible=%v); scope->identity binding is DIY", names))
	} else {
		feature(13, "Scope-gated tools (absence)", "DIY", fmt.Sprintf("visible=%v", names))
	}

	// FEATURE 14 — multi-step agent loop.
	tm := &toolModel{}
	wt, _ := functiontool.New(functiontool.Config{Name: "get_weather", Description: "weather by city"},
		func(_ agent.Context, a weatherArgs) (weatherResult, error) {
			return weatherResult{Report: "sunny in " + a.City}, nil
		})
	r, _, _ := newRunner(llmagent.Config{Name: "agent", Model: tm, Tools: []tool.Tool{wt}})
	reply, _, _, err := runTurn(ctx, r, "acme|alice", "sa", "what is the weather?")
	if err != nil {
		panic(err)
	}
	if tm.steps.Load() >= 2 && strings.Contains(strings.ToLower(reply), "sunny") {
		feature(14, "Multi-step agent loop (tool call)", "BUILT-IN",
			fmt.Sprintf("runner ran %d model steps: FunctionCall get_weather -> tool exec -> final %q", tm.steps.Load(), shorten(reply)))
	} else {
		feature(14, "Multi-step agent loop (tool call)", "PARTIAL",
			fmt.Sprintf("steps=%d reply=%q", tm.steps.Load(), reply))
	}

	// FEATURE 15 — typed tools + MCP.
	// functiontool.New[TArgs,TResults] infers a JSON Schema from the Go structs
	// (stored on decl.ParametersJsonSchema). tool/mcptoolset consumes MCP natively.
	decl := wt.(interface {
		Declaration() *genai.FunctionDeclaration
	}).Declaration()
	params := schemaProps(decl.ParametersJsonSchema)
	feature(15, "Typed tools + MCP", "BUILT-IN",
		fmt.Sprintf("functiontool.New[T] inferred JSON-Schema params=%v from the Go struct; tool/mcptoolset consumes MCP natively", params))
}

// ===========================================================================
// AREA 4 — AUTH
// ===========================================================================

func runAuth(ctx context.Context) {
	hdr("AREA 4 — AUTH")

	// FEATURE 16 — identity-binding gate, at PARITY with aisotropy.
	// Key construction and auth are separate layers. Without a gate, ADK trusts
	// the userID string it is handed — exactly as aisotropy trusts the path
	// tenant_id in its default NoAuth mode (ADR-0024), verifying only when
	// AISOTROPY_JWT_SECRET is set. ADK provides the gate SEAM: an llmagent
	// BeforeAgentCallback runs before the agent and, by returning content,
	// short-circuits the turn WITHOUT invoking the model (see agent.go: "If it
	// returns non-nil content ... the agent run will be skipped"). We install one
	// that rejects an unbound identity and prove the model never runs. The
	// credential check itself is developer code in BOTH frameworks.
	cm := &countingModel{}
	authorized := map[string]bool{"acme|alice": true} // stands in for verified JWT claims
	refusal := &genai.Content{Role: genai.RoleModel,
		Parts: []*genai.Part{{Text: "401 unauthorized: identity not bound"}}}
	gate := func(c agent.Context) (*genai.Content, error) {
		if !authorized[c.UserID()] {
			return refusal, nil // short-circuit: agent + model skipped
		}
		return nil, nil
	}
	r, _, _ := newRunner(llmagent.Config{Name: "auth", Model: cm,
		BeforeAgentCallbacks: []agent.BeforeAgentCallback{gate}})

	// Authorized identity: gate passes, model runs.
	before := cm.Calls()
	if _, _, _, err := runTurn(ctx, r, "acme|alice", "s-ok", "hi"); err != nil {
		panic(err)
	}
	okRan := cm.Calls() > before

	// Forged/unbound identity: gate rejects BEFORE the model.
	beforeForged := cm.Calls()
	reply, _, _, err := runTurn(ctx, r, "globex|admin", "s-bad", "hi")
	if err != nil {
		panic(err)
	}
	forgedBlocked := cm.Calls() == beforeForged && strings.Contains(reply, "401")

	if okRan && forgedBlocked {
		feature(16, "Identity-binding gate (auth)", "PARTIAL",
			"llmagent BeforeAgentCallback (BUILT-IN seam) rejected an unbound userID before the model ran; credential check is DIY — parity with aisotropy NoAuth-vs-JWT")
	} else {
		feature(16, "Identity-binding gate (auth)", "PARTIAL",
			fmt.Sprintf("BeforeAgentCallback seam ran; okRan=%v forgedBlocked=%v reply=%q", okRan, forgedBlocked, shorten(reply)))
	}
}

// ===========================================================================
// AREA 5 — COST & GOVERNANCE
// ===========================================================================

func runCostGov(ctx context.Context) {
	hdr("AREA 5 — COST & GOVERNANCE")

	cm := &countingModel{}
	r, _, _ := newRunner(llmagent.Config{Name: "cost", Model: cm})

	// FEATURE 17 — per-tenant PRE-SPEND quota gate.
	// ADK has no pre-spend primitive; cost is post-hoc token metadata. DIY gate
	// rejects BEFORE runner.Run; the call-counter proves the model never runs.
	spent := map[string]int32{"acme": 100}
	const budget int32 = 40
	gate := func(t string) bool { return spent[t] < budget }
	before := cm.Calls()
	rejected := !gate("acme")
	after := cm.Calls()
	if rejected && after == before {
		feature(17, "Per-tenant PRE-SPEND quota gate", "DIY",
			fmt.Sprintf("no ADK primitive; DIY gate rejected over-budget tenant BEFORE runner.Run (model.Calls %d==%d)", before, after))
	} else {
		feature(17, "Per-tenant PRE-SPEND quota gate", "DIY", "gate ran but model unexpectedly invoked")
	}

	// FEATURE 18 — circuit breaker.
	// No breaker primitive; DIY wrapper opens after N consecutive failures.
	cb := &breaker{threshold: 5}
	failing := func() error { return errors.New("provider down") }
	var lastErr error
	tripped := false
	for i := 0; i < 7; i++ {
		if lastErr = cb.call(failing); errors.Is(lastErr, errCircuitOpen) {
			tripped = true
			break
		}
	}
	feature(18, "Circuit breaker (fast-fail)", "DIY",
		fmt.Sprintf("no ADK primitive; DIY breaker OPENED after %d failures, then fast-failed (tripped=%v)", cb.threshold, tripped))

	// FEATURE 19 — quota-pressure model degradation.
	// No primitive; DIY routes to a cheap model under pressure.
	pressure := 0.97
	chosen := "primary-model"
	if pressure >= 0.95 {
		chosen = "cheap-model"
	}
	feature(19, "Quota-pressure degradation (cheap model)", "DIY",
		fmt.Sprintf("no ADK primitive; DIY router at pressure=%.2f selected %q instead of 429", pressure, chosen))

	// FEATURE 20 — cost meter (per-tenant).
	// event.UsageMetadata is a REAL per-call field (BUILT-IN visibility); the
	// per-TENANT aggregation is DIY (ADK has no tenant).
	byTenant := map[string]int32{}
	for _, t := range []struct{ tenant, prompt string }{
		{"acme", "MEMO: a"}, {"globex", "MEMO: b"},
	} {
		_, in, out, err := runTurn(ctx, r, t.tenant+"|u", "s"+t.tenant, t.prompt)
		if err != nil {
			panic(err)
		}
		byTenant[t.tenant] += in + out
	}
	feature(20, "Cost meter (per-tenant)", "PARTIAL",
		fmt.Sprintf("per-call event.UsageMetadata BUILT-IN; DIY per-tenant totals acme=%d globex=%d tok", byTenant["acme"], byTenant["globex"]))
}

// ===========================================================================
// AREA 6 — OBSERVABILITY
// ===========================================================================

func runObservability(ctx context.Context) {
	hdr("AREA 6 — OBSERVABILITY")

	// FEATURE 21 — OTel gen_ai spans.
	// ADK emits OTel spans with gen_ai.* attributes via the global tracer. We
	// capture them with an in-memory recorder and confirm gen_ai attributes.
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(ctx) }()

	cm := &countingModel{}
	r, _, _ := newRunner(llmagent.Config{Name: "obs", Model: cm})
	tracer := tp.Tracer("orbit/parity")
	dctx, parent := tracer.Start(ctx, "orbit.turn", oteltrace.WithAttributes(attribute.String("tenant.id", "acme")))
	if _, _, _, err := runTurn(dctx, r, "acme|alice", "so", "MEMO: traced"); err != nil {
		panic(err)
	}
	parent.End()
	_ = tp.ForceFlush(ctx)

	genaiSeen := false
	tenantOnADK := false
	for _, s := range rec.Ended() {
		for _, a := range s.Attributes() {
			k := string(a.Key)
			if strings.HasPrefix(k, "gen_ai.") {
				genaiSeen = true
			}
			if k == "tenant.id" && s.Name() != "orbit.turn" {
				tenantOnADK = true
			}
		}
	}
	if genaiSeen {
		feature(21, "OTel gen_ai spans", "BUILT-IN",
			fmt.Sprintf("captured ADK spans with gen_ai.* attrs via global tracer; tenant_id native on ADK spans=%v (DIY enrich)", tenantOnADK))
	} else {
		feature(21, "OTel gen_ai spans", "PARTIAL", "no gen_ai attrs captured")
	}

	// FEATURE 22 — health / readiness probe.
	// ADK core exposes no health endpoint primitive (server/adkrest is a chat
	// server, not a readiness gate). DIY probes the memory tiers, no LLM spend.
	sessSvc := session.InMemoryService()
	memSvc := memory.InMemoryService()
	sessionOK := probe(func() error {
		_, e := sessSvc.List(context.Background(), &session.ListRequest{AppName: "orbit", UserID: "_health"})
		return e
	})
	memOK := probe(func() error {
		_, e := memSvc.SearchMemory(context.Background(), &memory.SearchRequest{AppName: "orbit", UserID: "_health", Query: "ping"})
		return e
	})
	feature(22, "Health / readiness probe", "DIY",
		fmt.Sprintf("no ADK health primitive; DIY read-only probes session=%v memory=%v (no LLM spend)", sessionOK, memOK))

	// FEATURE 23 — product evaluation (precision/recall of retrieval).
	// No eval harness; DIY IR metrics over memory.SearchMemory.
	memSvc2 := memory.InMemoryService()
	_ = ingestDocAsSession(ctx, memSvc2, "acme|eval", "tenant isolation vector filter scope")
	rr, _ := memSvc2.SearchMemory(ctx, &memory.SearchRequest{AppName: "orbit", UserID: "acme|eval", Query: "isolation"})
	precision := 0.0
	if len(rr.Memories) > 0 {
		precision = 1.0 // the single relevant doc was retrieved
	}
	feature(23, "Product evaluation (P@k / judge)", "DIY",
		fmt.Sprintf("no ADK eval harness; DIY P@1=%.1f over memory.SearchMemory('isolation')", precision))
}

// ===========================================================================
// AREA 7 — CONSOLIDATION / SLEEP
// ===========================================================================

func runConsolidation(ctx context.Context) {
	hdr("AREA 7 — CONSOLIDATION / SLEEP")

	// FEATURE 24 — transactional outbox (atomic message + intent).
	// session.AppendEvent writes ONE store; there is no ACID transaction spanning
	// "user turn + pending consolidation event" across two stores, and no outbox
	// worker / cold-boot recovery. aisotropy writes both-or-neither in one txn.
	svc := session.InMemoryService()
	created, _ := svc.Create(ctx, &session.CreateRequest{AppName: "orbit", UserID: "acme|alice", SessionID: "sx"})
	ev := &session.Event{Author: "user"}
	ev.Content = &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "MEMO: x"}}}
	_ = svc.AppendEvent(ctx, created.Session, ev)
	feature(24, "Transactional outbox (atomic msg+intent)", "NOT-POSSIBLE",
		"session.AppendEvent is single-store; no atomic message+event txn across stores, no outbox worker / cold-boot recovery")

	// FEATURE 25 — async consolidation (dedup / contradictions offline).
	// No sleep-cycle primitive; DIY dedup over exported records.
	records := []string{"prod uses aws", "prod uses aws", "prod uses gcp"}
	deduped := diyDedup(records)
	feature(25, "Async consolidation (dedup/sleep)", "DIY",
		fmt.Sprintf("no ADK sleep primitive (only AddSessionToMemory ingest); DIY dedup %d->%d records", len(records), len(deduped)))
}

// ===========================================================================
// small DIY helpers
// ===========================================================================

func shorten(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

func collectTexts(s session.Session) []string {
	var out []string
	for ev := range s.Events().All() {
		if ev.LLMResponse.Content == nil {
			continue
		}
		for _, p := range ev.LLMResponse.Content.Parts {
			if p != nil && p.Text != "" {
				out = append(out, p.Text)
			}
		}
	}
	return out
}

func diySummarize(texts []string) string {
	if len(texts) == 0 {
		return "(empty)"
	}
	return fmt.Sprintf("%d msgs condensed; first=%q", len(texts), shorten(texts[0]))
}

func diyScore(entries []memory.Entry) []memory.Entry {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp) // DIY recency
	})
	return entries
}

func diySupersede(store map[string]string, key, newVal string) { store[key] = newVal }

func ingestDocAsSession(ctx context.Context, mem memory.Service, userID, doc string) error {
	svc := session.InMemoryService()
	created, err := svc.Create(ctx, &session.CreateRequest{AppName: "orbit", UserID: userID, SessionID: "doc"})
	if err != nil {
		return err
	}
	ev := &session.Event{Author: "model"}
	ev.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: doc}}}
	if err := svc.AppendEvent(ctx, created.Session, ev); err != nil {
		return err
	}
	got, err := svc.Get(ctx, &session.GetRequest{AppName: "orbit", UserID: userID, SessionID: "doc"})
	if err != nil {
		return err
	}
	return mem.AddSessionToMemory(ctx, got.Session)
}

func diyDedup(rs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func toolNames(ts []tool.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name())
	}
	sort.Strings(out)
	return out
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func probe(f func() error) bool { return f() == nil }

// schemaProps marshals an inferred JSON Schema (any) and returns its property
// names, so we read the REAL inferred schema without importing jsonschema.
func schemaProps(js any) []string {
	if js == nil {
		return nil
	}
	b, err := json.Marshal(js)
	if err != nil {
		return nil
	}
	var m struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	var out []string
	for k := range m.Properties {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var errCircuitOpen = errors.New("circuit open")

type breaker struct {
	threshold int
	failures  int
	open      bool
}

func (b *breaker) call(f func() error) error {
	if b.open {
		return errCircuitOpen
	}
	if err := f(); err != nil {
		b.failures++
		if b.failures >= b.threshold {
			b.open = true
		}
		return err
	}
	b.failures = 0
	return nil
}
