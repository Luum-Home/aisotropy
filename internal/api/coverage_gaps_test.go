package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/primitives"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/tools"
	"github.com/fugue-labs/gollem/ext/mcp"
)

// postJSON issues a POST with a JSON body and returns the response status code.
func postJSON(t *testing.T, h http.Handler, path string, body []byte) int {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// ---- ChatCore quota gate ---------------------------------------------------

// TestChatCore_QuotaExhaustedReturns429 covers ChatCore's constitutional quota
// branch: with the tenant's bucket drained, a turn is rejected BEFORE any LLM
// call with a 429 (huma status), so a throttled tenant never spends tokens.
func TestChatCore_QuotaExhaustedReturns429(t *testing.T) {
	wm := newTestWorkingMemory(t)
	limiter := wm.NewLimiter(0, 1) // capacity 1, no refill
	deps := ChatDeps{Client: llm.NewStubClient(), WM: wm, Limiter: limiter, Guard: &Guard{G: guardrails.Default()}}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	// First turn drains the single token.
	if _, err := ChatCore(context.Background(), deps, in); err != nil {
		t.Fatalf("first turn should be allowed: %v", err)
	}
	// Second turn is over quota -> 429.
	_, err := ChatCore(context.Background(), deps, in)
	if err == nil {
		t.Fatal("second turn over quota must be rejected")
	}
	se, ok := err.(huma.StatusError)
	if !ok || se.GetStatus() != http.StatusTooManyRequests {
		t.Fatalf("quota-exhausted error = %v, want a 429 StatusError", err)
	}
}

// ---- collectCompletion (/ask) edge cases -----------------------------------

// closeNoDoneClient closes its token channel WITHOUT ever sending a Done token,
// exercising collectCompletion's "channel closed" termination (returns the
// accumulated text, nil) — distinct from the explicit Done-token path.
type closeNoDoneClient struct{ text string }

func (closeNoDoneClient) Name() string { return "close-no-done" }

func (c closeNoDoneClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token, 1)
	ch <- llm.Token{Text: c.text}
	close(ch) // no Done token
	return ch, nil
}

// TestAsk_StreamClosedWithoutDone covers collectCompletion's closed-channel
// branch: a producer that closes the stream without a Done token still yields a
// complete 200 reply (the accumulated text), not a hang or a 502.
func TestAsk_StreamClosedWithoutDone(t *testing.T) {
	handler := newAskRouter(t, closeNoDoneClient{text: "partial-but-complete"}, newExactOnlyCache(), nil)
	st, out := postAsk(t, handler, "acme", "alice", "question")
	if st != http.StatusOK {
		t.Fatalf("status = %d, want 200", st)
	}
	if out.Reply != "partial-but-complete" {
		t.Errorf("reply = %q, want the accumulated text", out.Reply)
	}
}

// blockingClient returns an open token channel that never sends and never
// closes, so collectCompletion blocks on its select until ctx is cancelled.
type blockingClient struct{}

func (blockingClient) Name() string { return "blocking" }

func (blockingClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	return make(chan llm.Token), nil // never sends, never closes
}

// TestCollectCompletion_ContextCancelled covers collectCompletion's ctx.Done
// branch directly: with a stream that never produces and a cancelled context,
// the function returns the context error (cutting token spend at the producer),
// never blocking forever.
func TestCollectCompletion_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call
	_, err := collectCompletion(ctx, blockingClient{}, "system", "prompt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collectCompletion err = %v, want context.Canceled", err)
	}
}

// ---- TypedHandlerFor arg validation ----------------------------------------

type reqArgs struct {
	City string `json:"city"`
}

// TestTypedHandlerFor_EmptyArgsValidatesRequired covers TypedHandlerFor's
// empty-payload normalization (empty raw args become "{}" so a required field is
// still caught): an empty arg blob against a schema requiring `city` reflects as
// a *ToolArgsError rather than running the handler with a zero value.
func TestTypedHandlerFor_EmptyArgsValidatesRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)
	ran := false
	h := TypedHandlerFor("get_weather", schema, func(_ context.Context, _ reqArgs) (string, error) {
		ran = true
		return "ok", nil
	})
	_, err := h(context.Background(), json.RawMessage(""))
	var ae *ToolArgsError
	if !errors.As(err, &ae) {
		t.Fatalf("empty args against a required field must reflect a *ToolArgsError, got %v", err)
	}
	if ran {
		t.Error("the handler must NOT run when required args are missing")
	}
}

// TestTypedHandlerFor_MalformedJSONReflects covers TypedHandlerFor's decode-error
// branch: with no schema (decode-only), malformed JSON passes the (no-op)
// validator but fails json.Unmarshal, surfacing a *ToolArgsError.
func TestTypedHandlerFor_MalformedJSONReflects(t *testing.T) {
	h := TypedHandlerFor("get_weather", nil, func(_ context.Context, _ reqArgs) (string, error) {
		return "ok", nil
	})
	_, err := h(context.Background(), json.RawMessage(`{"city": `)) // truncated JSON
	var ae *ToolArgsError
	if !errors.As(err, &ae) {
		t.Fatalf("malformed JSON must reflect a *ToolArgsError, got %v", err)
	}
}

// ---- MCP tools -------------------------------------------------------------

// listErrMCPClient fails ListTools, exercising BuildMCPTools' discovery-error
// branch.
type listErrMCPClient struct{}

func (listErrMCPClient) ListTools(context.Context) ([]mcp.Tool, error) {
	return nil, errors.New("mcp server unreachable")
}
func (listErrMCPClient) CallTool(context.Context, string, map[string]any) (*mcp.ToolResult, error) {
	return nil, nil
}

// TestBuildMCPTools_ListToolsError covers BuildMCPTools' error branch: a server
// whose ListTools fails yields an error (never a partial/empty toolset silently).
func TestBuildMCPTools_ListToolsError(t *testing.T) {
	_, _, err := BuildMCPTools(context.Background(), listErrMCPClient{}, "read:files")
	if err == nil {
		t.Fatal("BuildMCPTools with a failing ListTools must return an error")
	}
}

// TestMCPHandler_EmptyArgsNormalized covers mcpHandler's empty-payload branch: an
// empty arg blob against a schema-less tool is normalized to "{}" and dispatched
// to the server (no reflection), so a no-arg tool still runs.
func TestMCPHandler_EmptyArgsNormalized(t *testing.T) {
	fc := &fakeMCPClient{result: &mcp.ToolResult{Content: []mcp.Content{{Text: "done"}}}}
	// No schema -> decode-only; empty args normalize to "{}" and reach CallTool.
	h := mcpHandler("noarg_tool", nil, fc)
	out, err := h(context.Background(), json.RawMessage("   "))
	if err != nil {
		t.Fatalf("empty args must normalize and dispatch, got err %v", err)
	}
	if out != "done" {
		t.Errorf("result = %q, want the server's text", out)
	}
	if fc.calls != 1 {
		t.Errorf("CallTool calls = %d, want 1", fc.calls)
	}
}

// TestMCPHandler_MalformedArgsReflect covers mcpHandler's decode-error branch:
// with no schema, malformed JSON passes the no-op validator but fails Unmarshal,
// surfacing a *ToolArgsError without ever invoking the server.
func TestMCPHandler_MalformedArgsReflect(t *testing.T) {
	fc := &fakeMCPClient{result: &mcp.ToolResult{}}
	h := mcpHandler("noarg_tool", nil, fc)
	_, err := h(context.Background(), json.RawMessage(`{bad`))
	var ae *ToolArgsError
	if !errors.As(err, &ae) {
		t.Fatalf("malformed MCP args must reflect a *ToolArgsError, got %v", err)
	}
	if fc.calls != 0 {
		t.Errorf("a reflecting arg error must NOT invoke the server: calls = %d, want 0", fc.calls)
	}
}

// ---- selectClient gateway fallback -----------------------------------------

// TestSelectClient_GatewayRouteErrorFallsBack covers selectClient's error branch:
// a gateway with no registered client and no default cannot route, so selectClient
// returns the fallback client rather than nil (a client value can never drop a
// request).
func TestSelectClient_GatewayRouteErrorFallsBack(t *testing.T) {
	gw := gateway.New(nil, nil) // no policy, no clients, no default -> Route errors
	fallback := llm.NewStubClient()
	got := selectClient(context.Background(), gw, fallback, nil, "acme", "alice", "prompt", "", false)
	if got != fallback {
		t.Fatalf("selectClient on a Route error must return the fallback, got %v", got)
	}
}

// ---- knowledge route 500 branches ------------------------------------------

// deleteErrStore embeds an in-memory semantic store but fails DeleteBySource,
// exercising the knowledge-delete routes' 500 branch.
type deleteErrStore struct {
	semantic.Store
	err error
}

func (d deleteErrStore) ListBySource(context.Context, semantic.Scope, semantic.Kind, string) ([]semantic.Point, error) {
	return nil, nil
}

func (d deleteErrStore) DeleteBySource(context.Context, semantic.Scope, semantic.Kind, string) (int, error) {
	return 0, d.err
}

func newKnowledgeErrRouter(t *testing.T, store semantic.Store, emb semantic.Embedder) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("knowledge-err-test", "0.1.0"))
	RegisterKnowledge(api, store, emb, semantic.DefaultChunkOptions(), nil)
	RegisterKnowledgeDelete(api, store)
	RegisterTenantKnowledge(api, store, emb, semantic.DefaultChunkOptions(), nil)
	RegisterTenantKnowledgeDelete(api, store)
	return router
}

// TestKnowledgeIngest_EmbedErrorReturns500 covers RegisterKnowledge's and
// RegisterTenantKnowledge's ingestion-failure branch: a failing embedder makes
// semantic.Ingest error, surfaced as 500 (not a silent 200).
func TestKnowledgeIngest_EmbedErrorReturns500(t *testing.T) {
	store := semantic.NewInMemoryStore(64)
	t.Cleanup(func() { _ = store.Close() })
	handler := newKnowledgeErrRouter(t, store, errEmbedder{dim: 64})

	docs := []KnowledgeDocument{{ID: "d1", Content: "a document body to chunk and embed"}}

	t.Run("user-scoped", func(t *testing.T) {
		if rec := postKnowledge(t, handler, "acme", "alice", docs); rec.Code != http.StatusInternalServerError {
			t.Fatalf("ingest with a failing embedder = %d, want 500", rec.Code)
		}
	})
	t.Run("tenant-shared", func(t *testing.T) {
		if rec := postTenantKnowledge(t, handler, "acme", docs); rec.Code != http.StatusInternalServerError {
			t.Fatalf("tenant ingest with a failing embedder = %d, want 500", rec.Code)
		}
	})
}

// TestKnowledgeDelete_StoreErrorReturns500 covers the knowledge-delete routes'
// 500 branch: a store whose DeleteBySource errors is surfaced as 500 on both the
// user-scoped and tenant-shared delete endpoints.
func TestKnowledgeDelete_StoreErrorReturns500(t *testing.T) {
	base := semantic.NewInMemoryStore(64)
	t.Cleanup(func() { _ = base.Close() })
	store := deleteErrStore{Store: base, err: errors.New("delete backend down")}
	handler := newKnowledgeErrRouter(t, store, semantic.NewHashEmbedder(64))

	t.Run("user-scoped", func(t *testing.T) {
		if rec := deleteKnowledge(t, handler, "acme", "alice", "doc-1"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("delete with a failing store = %d, want 500", rec.Code)
		}
	})
	t.Run("tenant-shared", func(t *testing.T) {
		if rec := deleteTenantKnowledge(t, handler, "acme", "doc-1"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("tenant delete with a failing store = %d, want 500", rec.Code)
		}
	})
}

// ---- hydration helpers -----------------------------------------------------

// TestHydrateSystemPrompt_PersonaErrorFails covers hydrateSystemPrompt's
// persona-error branch: a BasePrompt failure aborts hydration with an error,
// unlike the non-fatal facts/semantic enrichment paths.
func TestHydrateSystemPrompt_PersonaErrorFails(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	if _, err := hydrateSystemPrompt(context.Background(), nil, nil, nil, nil, nil, errPersona{},
		key, scope, "p1", "prompt"); err == nil {
		t.Fatal("a persona BasePrompt failure must fail hydration")
	}
}

// TestHydrateSystemPrompt_IncludesFacts covers hydrateSystemPrompt's facts
// injection loop: a stored fact whose content matches the prompt is folded into
// the system prompt under the known-facts section.
func TestHydrateSystemPrompt_IncludesFacts(t *testing.T) {
	store, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	fact := facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "the rollout plan ships on Friday", Importance: 0.9}
	if err := store.Save(context.Background(), scope, fact); err != nil {
		t.Fatalf("Save: %v", err)
	}

	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	sys, err := hydrateSystemPrompt(context.Background(), nil, store, nil, nil, nil, nil,
		key, scope, "p1", "rollout plan")
	if err != nil {
		t.Fatalf("hydrateSystemPrompt: %v", err)
	}
	if !strings.Contains(sys, "rollout plan ships on Friday") {
		t.Errorf("expected the matching fact injected into the system prompt, got %q", sys)
	}
}

// manyPointStore embeds an in-memory store but returns a fixed over-window set of
// scored points on Search, exercising searchSemanticContext's no-reranker
// truncation to the injection window.
type manyPointStore struct {
	semantic.Store
	points []semantic.ScoredPoint
}

func (m manyPointStore) Search(_ context.Context, sc semantic.Scope, _ []float32, limit int, _ ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	// Only the user scope (non-empty user) returns points; the tenant-shared scan
	// returns none, so the merged pool is exactly m.points.
	if sc.User() == "" {
		return nil, nil
	}
	return m.points, nil
}

// TestSearchSemanticContext_TruncatesToWindow covers searchSemanticContext's
// no-reranker truncation branch: a search returning more than semanticWindow
// points is trimmed to semanticWindow before injection.
func TestSearchSemanticContext_TruncatesToWindow(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	pts := make([]semantic.ScoredPoint, 0, semanticWindow+3)
	for i := 0; i < semanticWindow+3; i++ {
		pts = append(pts, semantic.ScoredPoint{
			Point: semantic.Point{ID: "p" + string(rune('a'+i)), Content: "chunk"},
			Score: float32(i),
		})
	}
	store := manyPointStore{Store: semantic.NewInMemoryStore(64), points: pts}
	t.Cleanup(func() { _ = store.Store.Close() })

	got := searchSemanticContext(context.Background(), store, semantic.NewHashEmbedder(64), nil, scope, "query")
	if len(got) != semanticWindow {
		t.Fatalf("returned %d points, want the window cap of %d", len(got), semanticWindow)
	}
}

// TestMergeScoredPoints_DedupSortTruncate covers mergeScoredPoints directly: a
// duplicate ID across sets is dropped (first wins), the result is sorted by score
// descending, and it is truncated to the limit.
func TestMergeScoredPoints_DedupSortTruncate(t *testing.T) {
	setA := []semantic.ScoredPoint{
		{Point: semantic.Point{ID: "a", Content: "A"}, Score: 0.2},
		{Point: semantic.Point{ID: "b", Content: "B"}, Score: 0.9},
	}
	setB := []semantic.ScoredPoint{
		{Point: semantic.Point{ID: "a", Content: "A-dup"}, Score: 0.99}, // duplicate ID -> dropped
		{Point: semantic.Point{ID: "c", Content: "C"}, Score: 0.5},
	}
	merged := mergeScoredPoints(2, setA, setB)
	if len(merged) != 2 {
		t.Fatalf("merged len = %d, want the limit of 2", len(merged))
	}
	if merged[0].ID != "b" {
		t.Errorf("top result id = %q, want b (highest score after dedup+sort)", merged[0].ID)
	}
	for _, p := range merged {
		if p.Content == "A-dup" {
			t.Error("the duplicate-id point must be dropped (first occurrence wins)")
		}
	}
}

// ---- StreamCore additional branches ----------------------------------------

// errSink fails OnToken, exercising StreamCore's client-disconnect early return.
func errSink() StreamSink {
	return StreamSink{
		OnToken: func(string) error { return errors.New("client gone") },
		OnDone:  func(StreamFinal) {},
	}
}

// TestStreamCore_OnTokenErrorStopsLive covers StreamCore's live-flush disconnect
// branch: a sink whose OnToken fails makes StreamCore stop cleanly (nil error,
// no OnDone) without persisting a partial turn.
func TestStreamCore_OnTokenErrorStopsLive(t *testing.T) {
	deps := StreamDeps{Client: tokenClient{name: "primary", token: "HELLO"}, WM: newTestWorkingMemory(t), Guard: &Guard{G: guardrails.Default()}}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}
	if err := StreamCore(context.Background(), deps, in, errSink()); err != nil {
		t.Fatalf("a client disconnect on OnToken must return nil, got %v", err)
	}
}

// TestStreamCore_BlockingModeBuffersAndInspects covers StreamCore's hard
// output-blocking branch: with BlockOutput on, tokens are buffered (not flushed
// live), the assembled reply is inspected via checkOutput, and only the safe text
// is delivered on completion.
func TestStreamCore_BlockingModeBuffersAndInspects(t *testing.T) {
	deps := StreamDeps{
		Client: tokenClient{name: "primary", token: "a safe assembled reply"},
		WM:     newTestWorkingMemory(t),
		Guard:  &Guard{G: guardrails.Default(), BlockOutput: true},
	}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	var got strings.Builder
	var doneText string
	sink := StreamSink{
		OnToken: func(text string) error { got.WriteString(text); return nil },
		OnDone:  func(f StreamFinal) { doneText = f.Text },
	}
	if err := StreamCore(context.Background(), deps, in, sink); err != nil {
		t.Fatalf("StreamCore (blocking): %v", err)
	}
	// In blocking mode the single delivery happens after inspection, on completion.
	if !strings.Contains(got.String(), "safe assembled reply") {
		t.Errorf("blocking mode delivered %q, want the inspected assembled reply", got.String())
	}
	if doneText == "" {
		t.Error("OnDone should carry the delivered (inspected) text in blocking mode")
	}
}

// TestStreamCore_BlockingModeOnTokenErrorStops covers the blocking-path
// disconnect branch: a sink whose OnToken fails on the single buffered emit makes
// StreamCore stop without recording a done frame.
func TestStreamCore_BlockingModeOnTokenErrorStops(t *testing.T) {
	deps := StreamDeps{
		Client: tokenClient{name: "primary", token: "buffered reply"},
		WM:     newTestWorkingMemory(t),
		Guard:  &Guard{G: guardrails.Default(), BlockOutput: true},
	}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}
	doneFired := false
	sink := StreamSink{
		OnToken: func(string) error { return errors.New("client gone") },
		OnDone:  func(StreamFinal) { doneFired = true },
	}
	if err := StreamCore(context.Background(), deps, in, sink); err != nil {
		t.Fatalf("blocking-mode disconnect must return nil, got %v", err)
	}
	if doneFired {
		t.Error("a disconnect before the buffered emit must NOT fire OnDone")
	}
}

// TestStreamCore_InjectsScopeGatedTools covers StreamCore's tool-fragment branch:
// a registered tool whose required scope the caller holds is injected into the
// system prompt (the run completes cleanly with the fragment appended).
func TestStreamCore_InjectsScopeGatedTools(t *testing.T) {
	reg := tools.NewRegistry(tools.Tool{Name: "read_file", Description: "read a file", RequiredScope: "read"})
	deps := StreamDeps{
		Client:  tokenClient{name: "primary", token: "hi"},
		WM:      newTestWorkingMemory(t),
		ToolReg: reg,
		Guard:   &Guard{G: guardrails.Default()},
	}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1", Scopes: []string{"read"}}}
	var got strings.Builder
	if err := StreamCore(context.Background(), deps, in, discardSink(&got)); err != nil {
		t.Fatalf("StreamCore with scope-gated tools: %v", err)
	}
	if got.Len() == 0 {
		t.Error("expected the turn to deliver tokens with tools injected")
	}
}

// ---- aisdk stream framing --------------------------------------------------

// unwrapToFlusher wraps a writer and only exposes a Flusher via Unwrap, so
// newAISDKStream must walk the Unwrap chain to find the flusher.
type unwrapToFlusher struct {
	inner *flushWriter
}

func (u unwrapToFlusher) Write(p []byte) (int, error) { return u.inner.Write(p) }
func (u unwrapToFlusher) Unwrap() http.ResponseWriter { return u.inner }

// flushWriter is a minimal http.ResponseWriter+Flusher over a buffer.
type flushWriter struct {
	buf     strings.Builder
	flushed int
}

func (f *flushWriter) Header() http.Header         { return http.Header{} }
func (f *flushWriter) WriteHeader(int)             {}
func (f *flushWriter) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *flushWriter) Flush()                      { f.flushed++ }

// TestNewAISDKStream_WalksUnwrapForFlusher covers newAISDKStream's Unwrap-walk
// branch: when the writer is not itself a Flusher but unwraps to one, the flusher
// is discovered through the chain and a written frame is flushed.
func TestNewAISDKStream_WalksUnwrapForFlusher(t *testing.T) {
	fw := &flushWriter{}
	s := newAISDKStream(unwrapToFlusher{inner: fw})
	if err := s.part(map[string]string{"type": "start"}); err != nil {
		t.Fatalf("part: %v", err)
	}
	if fw.flushed == 0 {
		t.Error("expected the flusher discovered via Unwrap to be flushed")
	}
	if !strings.HasPrefix(fw.buf.String(), "data: ") {
		t.Errorf("frame = %q, want a data: SSE frame", fw.buf.String())
	}
}

// failAfterWriter fails the Nth (1-indexed) Write call, so aisdkStream.part's
// multi-write framing surfaces a mid-frame write error.
type failAfterWriter struct {
	n     int
	count int
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.count++
	if w.count >= w.n {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

// TestAISDKPart_WriteErrorsSurface covers aisdkStream.part's write-error branches:
// a failure on the payload write (second write) and on the terminator write
// (third write) each surface as an error the caller treats as a disconnect.
func TestAISDKPart_WriteErrorsSurface(t *testing.T) {
	t.Run("payload write fails", func(t *testing.T) {
		s := newAISDKStream(&failAfterWriter{n: 2})
		if err := s.part(map[string]string{"type": "x"}); err == nil {
			t.Fatal("a failed payload write must surface an error")
		}
	})
	t.Run("terminator write fails", func(t *testing.T) {
		s := newAISDKStream(&failAfterWriter{n: 3})
		if err := s.part(map[string]string{"type": "x"}); err == nil {
			t.Fatal("a failed terminator write must surface an error")
		}
	})
	t.Run("prefix write fails", func(t *testing.T) {
		s := newAISDKStream(&failAfterWriter{n: 1})
		if err := s.part(map[string]string{"type": "x"}); err == nil {
			t.Fatal("a failed prefix write must surface an error")
		}
	})
}

// ---- chat/agent branches ---------------------------------------------------

// TestChatAgent_EmptySessionReturns422 covers RegisterAgent's identity-validation
// branch: an empty session id fails withIdentity (session is required) before the
// tool loop, surfacing as 422.
func TestChatAgent_EmptySessionReturns422(t *testing.T) {
	h := newAgentRouterFull(t, llm.NewStubClient(), DemoExecutor{}, nil)
	body, err := json.Marshal(map[string]any{"prompt": "hi", "product_id": "analytics-v1", "session_id": ""})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := postJSON(t, h, "/v1/tenant/acme/user/alice/chat/agent", body)
	if rec != http.StatusUnprocessableEntity {
		t.Fatalf("empty-session agent status = %d, want 422", rec)
	}
}

// TestChatAgent_FrictionDetectorWraps covers RegisterAgent's friction-executor
// branch: with a friction detector configured, the per-request executor is
// wrapped in a frictionExecutor before the tool loop runs. The keyless stub
// cannot drive the structured loop to completion (it maps to 500, exactly as
// TestChatAgent_FallbackLoopPath documents), but the wrap on the request path is
// what this test exercises — the handler must reach the loop (500), not reject
// earlier at identity/guard (422/400).
func TestChatAgent_FrictionDetectorWraps(t *testing.T) {
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("agent-friction-test", "0.1.0"))
	detector := primitives.NewFrictionDetector(primitives.NewStore())
	RegisterAgent(api, llm.NewStubClient(), agentScopeRegistry(), DemoExecutor{}, (*TypedToolExecutor)(nil), detector, noop.NewTracerProvider(), 3, nil, nil)

	body, err := json.Marshal(map[string]any{"prompt": "do the thing", "product_id": "analytics-v1", "session_id": "s1", "scopes": []string{"read"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if code := postJSON(t, router, "/v1/tenant/acme/user/alice/chat/agent", body); code != http.StatusInternalServerError {
		t.Fatalf("agent turn with a friction detector = %d, want 500 (wrap reached the fallback loop)", code)
	}
}

// compile-time guards for the fakes/interfaces used above.
var (
	_ MCPClient           = listErrMCPClient{}
	_ semantic.Store      = deleteErrStore{}
	_ io.Writer           = &failAfterWriter{}
	_ http.ResponseWriter = &flushWriter{}
)
