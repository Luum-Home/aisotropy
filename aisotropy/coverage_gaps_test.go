package aisotropy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/plugintest"
	"github.com/Luum-Home/aisotropy/internal/routing"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// embedErrEmbedder satisfies the Embedder seam but fails every Embed, so a code
// path that must embed (IngestKnowledge chunking) surfaces the error rather than
// silently succeeding. Dimension is fixed so the semantic store still constructs.
type embedErrEmbedder struct{ dim int }

func (e embedErrEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embed backend down")
}
func (e embedErrEmbedder) Dimension() int { return e.dim }

// TestTurn_InvalidIdentityReturnsError covers Turn's ChatCore-error branch: the
// runtime builds fine (keyless stub), but a turn with an empty tenant fails
// identity validation inside ChatCore and Turn surfaces that error rather than a
// zero ChatOutput with nil error.
func TestTurn_InvalidIdentityReturnsError(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{
		TenantID: "", UserID: "alice", SessionID: "s1", Prompt: "hi",
	}); err == nil {
		t.Fatal("Turn with an empty tenant must return the ChatCore identity error")
	}
}

// TestIngestKnowledge_EmbedErrorSurfaces covers IngestKnowledge's Ingest-error
// branch: with an embedder that fails, chunking a non-empty document forces an
// embed call that errors, and the error is returned (never a silent 0, nil).
func TestIngestKnowledge_EmbedErrorSurfaces(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithEmbedder(embedErrEmbedder{dim: 64}))
	defer closeApp(t, app)()

	if _, err := app.IngestKnowledge(context.Background(), "acme", "alice",
		Document{ID: "d1", Content: "a non-empty document body that must be chunked and embedded"}); err == nil {
		t.Fatal("IngestKnowledge with a failing embedder must return the embed error")
	}
}

// TestIngestTenantKnowledge_EmbedErrorSurfaces is the tenant-shared analogue of
// TestIngestKnowledge_EmbedErrorSurfaces: the tenant-bucket ingest arm surfaces
// the same embed failure.
func TestIngestTenantKnowledge_EmbedErrorSurfaces(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithEmbedder(embedErrEmbedder{dim: 64}))
	defer closeApp(t, app)()

	if _, err := app.IngestTenantKnowledge(context.Background(), "acme",
		Document{ID: "d1", Content: "a non-empty tenant-shared document body to chunk and embed"}); err == nil {
		t.Fatal("IngestTenantKnowledge with a failing embedder must return the embed error")
	}
}

// TestOpenFactsStore_OnDiskDataDir covers openFactsStore's on-disk factory
// branch: with AISOTROPY_DATA_DIR set (and no RQLITE_URL, no dedicated tenants),
// the SQLite store is opened under the data dir rather than in memory.
func TestOpenFactsStore_OnDiskDataDir(t *testing.T) {
	t.Setenv("RQLITE_URL", "")
	t.Setenv("AISOTROPY_DATA_DIR", t.TempDir())

	router := routing.NewRouter(routing.NewStaticPolicy())
	s, err := openFactsStore(router, nil)
	if err != nil {
		t.Fatalf("openFactsStore (on-disk): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestOpenSemanticStore_CollectionOverride covers openSemanticStore's
// QDRANT_COLLECTION base-name override branch: with the collection env set but no
// QDRANT_HOST, the in-process store is built under the overridden base name.
func TestOpenSemanticStore_CollectionOverride(t *testing.T) {
	t.Setenv("QDRANT_HOST", "")
	t.Setenv("QDRANT_COLLECTION", "custom_knowledge_base")

	router := routing.NewRouter(routing.NewStaticPolicy())
	s, err := openSemanticStore(context.Background(), router, nil, 64)
	if err != nil {
		t.Fatalf("openSemanticStore (collection override): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestStartRuntime_BadMCPPluginSkipped covers startRuntime's plugin LoadTools
// failure branch: a KindMCP manifest whose command cannot be spawned fails to
// load its tools and is logged + skipped, never downing the runtime.
func TestStartRuntime_BadMCPPluginSkipped(t *testing.T) {
	dir := t.TempDir()
	manifest := "" +
		"name: bad-mcp-plugin\n" +
		"version: 0.0.1\n" +
		"license: MIT\n" +
		"kind: mcp\n" +
		"run:\n" +
		"  command: aisotropy-nonexistent-mcp-plugin-binary-xyz\n" +
		"tools:\n" +
		"  - name: never_loads\n" +
		"    description: a tool whose MCP server never starts\n"
	path := filepath.Join(dir, "bad-mcp-plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithPlugin(path))
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "hi",
	}); err != nil {
		t.Fatalf("Turn must survive a plugin whose tools fail to load: %v", err)
	}
}

// TestStartRuntime_PluginPersonaDeclarationLogged covers startRuntime's
// persona-declaration branch: a go-import manifest that lists persona identifiers
// (metadata only) is surfaced without fabricating a prompt, and its tool still
// merges. The runtime must boot and serve a turn.
func TestStartRuntime_PluginPersonaDeclarationLogged(t *testing.T) {
	dir := t.TempDir()
	manifest := "" +
		"name: persona-plugin\n" +
		"version: 0.0.1\n" +
		"license: MIT\n" +
		"kind: go-import\n" +
		"personas:\n" +
		"  - analytics-v1\n" +
		"tools:\n" +
		"  - name: persona_plugin_tool\n" +
		"    description: a tool from a plugin that also declares persona identifiers\n"
	path := filepath.Join(dir, "persona-plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithPlugin(path))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	found := false
	for _, tool := range rt.toolRegistry.Permitted(nil) {
		if tool.Name == "persona_plugin_tool" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("a persona-declaring plugin's tool must still merge into the registry")
	}
}

// RunTask/RunTasks fallback-branch coverage moved to runtask_coverage_gaps_test.go
// (build tag keybroker) since those façade methods are compiled only under
// -tags keybroker.

// TestStartRuntime_NoLLMClientFallsBackToEnv covers startRuntime's client
// selection when the App has no WithLLM: it falls back to llm.FromEnv() (the
// keyless stub with no ANTHROPIC_API_KEY), boots the runtime, and serves a turn.
func TestStartRuntime_NoLLMClientFallsBackToEnv(t *testing.T) {
	app := New(WithRateLimit(1e9, 1e9)) // no WithLLM
	defer closeApp(t, app)()

	out, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "hi",
	})
	if err != nil {
		t.Fatalf("Turn on the env-fallback client: %v", err)
	}
	if !out.Stub {
		t.Error("expected the keyless stub client from llm.FromEnv()")
	}
}

// TestStartRuntime_EnvGatedWorkersAndJudge covers several env-gated startRuntime
// branches at once: AISOTROPY_BITRATE_BIN boots the bpt store + measurement
// scheduler, AISOTROPY_SLEEPCYCLE_BIN schedules the DataFusion consolidation
// worker, and AISOTROPY_GUARDRAILS_LLM_JUDGE attaches the built-in LLM safety
// judge to the output inspector. All schedulers wait on hour-scale tickers, so
// none execute during the test — the boot wiring is what is exercised.
func TestStartRuntime_EnvGatedWorkersAndJudge(t *testing.T) {
	t.Setenv("AISOTROPY_BITRATE_BIN", "aisotropy-nonexistent-bitrate-bin")
	t.Setenv("AISOTROPY_SLEEPCYCLE_BIN", "aisotropy-nonexistent-sleepcycle-bin")
	t.Setenv("AISOTROPY_GUARDRAILS_LLM_JUDGE", "true")

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "hi",
	}); err != nil {
		t.Fatalf("Turn with the env-gated workers + judge booted: %v", err)
	}
}

// TestStartRuntime_FactsBackendWithDedicatedTenants covers the branch where a
// consumer WithFactsBackend is configured ALONGSIDE a dedicated-tenant allowlist:
// tier routing is deliberately not layered over the sealed backend (it owns its
// persistence), which the runtime logs — and the runtime still boots and serves.
func TestStartRuntime_FactsBackendWithDedicatedTenants(t *testing.T) {
	t.Setenv("AISOTROPY_DEDICATED_TENANTS", "acme")
	backend := plugintest.NewRefDocBackend()
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithFactsBackend(backend))
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "hi",
	}); err != nil {
		t.Fatalf("Turn with a facts backend + dedicated tenants: %v", err)
	}
}

// TestStartRuntime_TypedHandlersCopied covers startRuntime's typed-handler copy
// loop: a WithTypedTool registration populates the App's typed handlers, which
// startRuntime copies into the runtime's dispatch map.
func TestStartRuntime_TypedHandlersCopied(t *testing.T) {
	type cityArgs struct {
		City string `json:"city"`
	}
	app := New(
		WithLLM(fastStub()),
		WithRateLimit(1e9, 1e9),
		WithTypedTool("lookup_city", "look up a city", "", func(_ context.Context, a cityArgs) (string, error) {
			return "found " + a.City, nil
		}),
	)
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	if _, ok := rt.typedHandlers["lookup_city"]; !ok {
		t.Fatal("WithTypedTool handler was not copied into the runtime typed-handler map")
	}
}

// TestEnsureSemantic_VectorBackendWithDedicatedTenants covers ensureSemantic's
// branch where a WithVectorBackend is configured alongside a dedicated-tenant
// allowlist: the sealed decorator provides per-tenant isolation, so physical tier
// routing is not layered over it (logged) — ingestion still routes into the
// backend.
func TestEnsureSemantic_VectorBackendWithDedicatedTenants(t *testing.T) {
	t.Setenv("AISOTROPY_DEDICATED_TENANTS", "acme")
	backend := plugintest.NewRefVectorBackend()
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithVectorBackend(backend))
	defer closeApp(t, app)()

	n, err := app.IngestKnowledge(context.Background(), "acme", "alice",
		Document{ID: "d1", Content: "a document body to chunk and embed into the sealed backend"})
	if err != nil {
		t.Fatalf("IngestKnowledge with a vector backend + dedicated tenants: %v", err)
	}
	if n == 0 {
		t.Fatal("expected chunks written to the sealed vector backend")
	}
}

// scanErrVectorBackend is a VectorBackend whose Scan fails, so the sealed store's
// DeleteBySource (which enumerates a namespace before deleting) surfaces an error.
type scanErrVectorBackend struct {
	*plugintest.RefVectorBackend
}

func (scanErrVectorBackend) Scan(context.Context, string) ([]semantic.BackendVector, error) {
	return nil, errors.New("scan backend down")
}

// TestDeleteKnowledge_BackendScanErrorSurfaces covers DeleteKnowledge's and
// DeleteTenantKnowledge's DeleteBySource-error branch: a vector backend whose
// Scan fails makes the sealed store's DeleteBySource error, which the SDK arms
// surface rather than swallowing into (0, nil).
func TestDeleteKnowledge_BackendScanErrorSurfaces(t *testing.T) {
	backend := scanErrVectorBackend{RefVectorBackend: plugintest.NewRefVectorBackend()}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithVectorBackend(backend))
	defer closeApp(t, app)()

	ctx := context.Background()
	if _, err := app.DeleteKnowledge(ctx, "acme", "alice", "doc-1"); err == nil {
		t.Fatal("DeleteKnowledge must surface the backend Scan error")
	}
	if _, err := app.DeleteTenantKnowledge(ctx, "acme", "doc-1"); err == nil {
		t.Fatal("DeleteTenantKnowledge must surface the backend Scan error")
	}
}

// buildWASMEchoGuest compiles the shared wasmtool echo fixture to a wasip1
// reactor module and returns its on-disk path, so startRuntime's KindWASM
// plugin-loading success path (tool + typed-handler merge) can be exercised end
// to end with only the standard Go toolchain. Skipped under -short.
func buildWASMEchoGuest(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping wasm build under -short (shells out to `go build` for GOOS=wasip1)")
	}
	out := filepath.Join(t.TempDir(), "echo.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = filepath.Join("..", "internal", "wasmtool", "testdata", "echomod")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasm guest: %v\n%s", err, b)
	}
	return out
}

// TestStartRuntime_WASMPluginContributesToolAndHandler covers startRuntime's
// plugin-success merge path: a KindWASM plugin loaded via WithPlugin contributes
// both a scope-gated tool AND a typed dispatch handler into the runtime, so both
// the tools-append and the typed-handler-copy branches run (the go-import path
// contributes no handler, so this is the branch that exercises the handler copy).
func TestStartRuntime_WASMPluginContributesToolAndHandler(t *testing.T) {
	module := buildWASMEchoGuest(t)
	dir := t.TempDir()
	manifest := "" +
		"name: wasm-echo-plugin\n" +
		"version: 1.0.0\n" +
		"license: Apache-2.0\n" +
		"kind: wasm\n" +
		"run:\n" +
		"  module: " + module + "\n" +
		"tools:\n" +
		"  - name: wasm_echo\n" +
		"    description: echo via a sandboxed wasm module\n" +
		"    required_scope: \"\"\n"
	path := filepath.Join(dir, "wasm-plugin.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithPlugin(path))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	found := false
	for _, tool := range rt.toolRegistry.Permitted(nil) {
		if tool.Name == "wasm_echo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("wasm plugin tool wasm_echo was not merged into the registry")
	}
	if _, ok := rt.typedHandlers["wasm_echo"]; !ok {
		t.Fatal("wasm plugin's typed dispatch handler was not copied into the runtime")
	}
}

// TestRun_AddrFromEnv covers Run's address-resolution branch that reads
// AISOTROPY_ADDR when no WithAddr was supplied: the server binds the env address,
// answers liveness, and shuts down cleanly on ctx cancel.
func TestRun_AddrFromEnv(t *testing.T) {
	addr := freeAddr(t)
	t.Setenv("AISOTROPY_ADDR", addr)
	app := New(WithLLM(fastStub()), WithAuthenticator(NoAuth())) // no WithAddr -> env is used

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	waitForServer(t, "http://"+addr+"/healthz")

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error on shutdown: %v", err)
	}
}

// TestTurn_RuntimeBuildFailureSurfaces covers Turn's ensureRuntime-error branch
// (and startRuntime's working-memory open-error branch): pointing
// AISOTROPY_DATA_DIR at a regular file makes the on-disk BadgerDB open fail, so
// building the runtime errors and Turn surfaces that error rather than panicking.
func TestTurn_RuntimeBuildFailureSurfaces(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Setenv("AISOTROPY_DATA_DIR", f) // a FILE where a data dir is expected

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer func() { _ = app.Close() }()

	if _, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "hi",
	}); err == nil {
		t.Fatal("Turn must surface the runtime build failure from a bad data dir")
	}
}

// TestRun_RuntimeBuildFailureReturnsError covers Run's startRuntime-error branch:
// a data dir that is actually a file makes runtime construction fail, and Run
// returns that error before ever binding the HTTP listener.
func TestRun_RuntimeBuildFailureReturnsError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Setenv("AISOTROPY_DATA_DIR", f)

	app := New(WithAddr(freeAddr(t)), WithLLM(fastStub()), WithAuthenticator(NoAuth()))
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run must return the runtime build error from a bad data dir")
	}
}

// TestOpenFactsStore_RqliteBranchRoutes covers openFactsStore's RQLITE_URL
// branch selection (and the "routing not applied on the rqlite backend" warning
// when dedicated tenants are configured): with RQLITE_URL set to an unreachable
// node, the rqlite path is taken and its connection/migration honestly fails —
// exercising the branch decision without a live cluster.
func TestOpenFactsStore_RqliteBranchRoutes(t *testing.T) {
	t.Setenv("RQLITE_URL", "http://127.0.0.1:1") // unreachable: connection refused, fast
	router := routing.NewRouter(routing.NewStaticPolicy("acme"))
	if _, err := openFactsStore(router, []string{"acme"}); err == nil {
		t.Fatal("openFactsStore against an unreachable rqlite must return a connection error")
	}
}

// TestOpenSemanticStore_QdrantBranchRoutes covers openSemanticStore's
// QDRANT_HOST branch selection and QDRANT_PORT parsing: with the host/port env
// set to an unreachable endpoint, the Qdrant path is taken and honestly fails to
// connect — exercising the branch + port-parse without a live Qdrant. A bounded
// context keeps the failure fast even if the gRPC dial would otherwise linger.
func TestOpenSemanticStore_QdrantBranchRoutes(t *testing.T) {
	t.Setenv("QDRANT_HOST", "127.0.0.1")
	t.Setenv("QDRANT_PORT", "1") // unreachable port -> exercises the port-parse + branch
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	router := routing.NewRouter(routing.NewStaticPolicy())
	if _, err := openSemanticStore(ctx, router, nil, 64); err == nil {
		t.Fatal("openSemanticStore against an unreachable Qdrant must return a connection error")
	}
}

// compile-time: embedErrEmbedder is a valid Embedder and llm is referenced.
var _ Embedder = embedErrEmbedder{}
var _ VectorBackend = scanErrVectorBackend{}
var _ = llm.NewStubClient
