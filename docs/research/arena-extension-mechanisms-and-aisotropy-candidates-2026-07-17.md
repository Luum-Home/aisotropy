# Arena — Extension Mechanisms (beyond MCP) & aisotropy Adoption Candidates (2026-07-17)

> **What this is.** A companion to `arena-extensibility-plugins-2026-07-17.md`. That
> report established, with code-truth, *that* aisotropy has a plugin mechanism and
> *that* the competitors have ecosystems. This one goes **deeper on the
> mechanisms**: for each arena framework it documents the **full extension surface**
> — not "how do I add a tool" but "how do I extend it *at all*: providers,
> embedders, vector stores, retrievers, middleware, evaluators, deployment, MCP
> serve." Then it maps those patterns onto aisotropy's **existing seams** (file:line)
> and produces a **prioritized, honest** candidate list: the few worth adopting, not
> everything.
>
> - **Competitor claims** cite upstream URLs (Part A).
> - **aisotropy claims** cite `file:line` in this repo (Part B, Part C seam-mapping).
> - **Design/research only.** No aisotropy code was modified to produce this report.

---

## Part A — per-tool extension architecture (sourced)

For each framework: (1) the **integration architecture** (interface- vs
package- vs plugin-registration- vs registry-based); (2) the **extension points**
(what is pluggable); (3) **discovery/distribution**; (4) **MCP serve** (can it
*publish* its own primitives as an MCP server, not just consume).

### LangChain / LangGraph (Python + JS/TS)

- **Integration architecture — package-based, interface-rooted.** Abstract base
  classes live in `langchain-core` (`BaseChatModel`, `Embeddings`, `VectorStore`,
  `BaseRetriever`, `BaseTool`, `BaseOutputParser`); every concrete integration is a
  first-class package (`langchain-community` + hundreds of partner packages such as
  `langchain-openai`, `langchain-qdrant`). A custom vector store subclasses
  `VectorStore` and is conformance-tested against `langchain-tests`'
  `VectorStoreIntegrationTests`.
  ([vector stores](https://python.langchain.com/docs/how_to/vectorstore_retriever/),
  [VectorStoreRetriever ref](https://reference.langchain.com/javascript/langchain-core/vectorstores/VectorStoreRetriever),
  [DeepWiki: vector stores](https://deepwiki.com/langchain-ai/langchain/3.4-vector-stores))
- **Extension points:** chat models, embeddings, vector stores, retrievers, tools,
  output parsers, callbacks, document loaders, and — new in LangChain v1 — **agent
  middleware**: `create_agent` is a minimal harness and capabilities (guardrails,
  retries, routing, custom tool policies) are added as **composable middleware**
  built on LangGraph's durable execution/persistence/human-in-the-loop.
- **Discovery/distribution:** PyPI/npm packages + **LangChain Hub** (a prompt
  registry inside LangSmith).
- **MCP:** **both.** Consume via `langchain-mcp-adapters` (convert any MCP server's
  tools into LangChain/LangGraph tools). **Serve:** the LangGraph **Agent Server**
  exposes every deployed agent as an MCP tool at `/mcp` over Streamable HTTP,
  enabled automatically — so LangGraph *publishes* into the ecosystem, not only
  consumes.
  ([MCP endpoint in Agent Server](https://docs.langchain.com/langsmith/server-mcp),
  [langchain-mcp-adapters](https://github.com/langchain-ai/langchain-mcp-adapters))
  *(This updates the prior report's "consume only" for LangChain.)*

### LlamaIndex (Python + TS)

- **Integration architecture — namespace-package registry.** Core abstractions in
  `llama-index-core`; **300+** integration packages in the `llama-index-integrations`
  monorepo (since v0.10) under a strict `llama-index-{category}-{provider}` naming
  convention, spanning 19 abstraction categories.
  ([v0.10 migration](https://www.llamaindex.ai/blog/llamaindex-v0-10-838e735948f8),
  [llama-index-integrations](https://github.com/run-llama/llama_index/tree/main/llama-index-integrations))
- **Extension points:** LLMs, embeddings (subclass `BaseEmbedding`, override 5
  embed methods), vector stores (subclass `BasePydanticVectorStore` — e.g.
  `PGVectorStore`, `OpensearchVectorStore`), retrievers, **data loaders/readers**
  (LlamaHub, 200+), node parsers, tools (`BaseToolSpec`/`FunctionTool`), callbacks,
  output parsers.
  ([custom embeddings](https://developers.llamaindex.ai/python/framework/integrations/embeddings/custom_embeddings/),
  [vector stores](https://docs.llamaindex.ai/en/stable/module_guides/storing/vector_stores/))
- **Discovery/distribution:** LlamaHub + PyPI namespace packages.
- **MCP:** **both** — consume (`llama-index-tools-mcp` `McpToolSpec`), and expose
  agent tools as MCP tools.
  ([LlamaHub](https://llamahub.ai/))

### Pydantic-AI (Python)

- **Integration architecture — interface-based (no first-party hub).** Three
  abstractions: `Model` (ABC with `request`/`request_stream`), `Provider`
  (auth/client/base-URL), and `ModelProfile` (per-model-family request-shaping,
  independent of Model/Provider). A custom vendor = implement `Model`, pass a
  `Provider` and optional `ModelProfile`. Toolsets extend `AbstractToolset` (ships
  `FileSystemToolset`, `SkillsToolset`, `TodoToolset`).
  ([models overview](https://pydantic.dev/docs/ai/models/overview/),
  [Model base](https://pydantic.dev/docs/ai/api/models/base/),
  [providers](https://pydantic.dev/docs/ai/api/pydantic-ai/providers/),
  [profiles](https://pydantic.dev/docs/ai/api/pydantic-ai/profiles/))
- **Extension points:** models, providers, profiles, toolsets, tools
  (`@agent.tool`). Embeddings/vector stores are **not** a first-party abstraction —
  it leans on the FastMCP ecosystem instead of a provider hub.
- **Discovery/distribution:** PyPI; **no dedicated hub** (delegates to FastMCP).
- **MCP:** **both** — client (`MCPToolset`, built on FastMCP: stdio + Streamable
  HTTP + SSE + in-process) and **serve** (Pydantic AI serves its tools over MCP via
  FastMCP).

### Vercel AI SDK (JS/TS)

- **Integration architecture — provider-specification + registry + middleware.**
  The `@ai-sdk/provider` package defines the `LanguageModelV2`/`V3` **specification**
  — the foundational TypeScript contract every provider (`@ai-sdk/openai`,
  `@ai-sdk/anthropic`, …) implements. Apps compose with `customProvider`,
  `createProviderRegistry` (a registry of providers/models), and **middleware**.
  ([provider management](https://ai-sdk.dev/docs/ai-sdk-core/provider-management),
  [createProviderRegistry](https://ai-sdk.dev/docs/reference/ai-sdk-core/provider-registry),
  [@ai-sdk/provider spec](https://tessl.io/registry/tessl/npm-ai-sdk--provider))
- **Extension points:** language-model providers, embedding-model providers,
  **language-model middleware** via `wrapLanguageModel(model, middleware)` — a
  pluggable, composable interception layer (guardrails, caching, logging, request
  modification) applied in order; tools (`tool()`); the AI Gateway for routing.
  ([middleware](https://ai-sdk.dev/docs/ai-sdk-core/middleware),
  [wrapLanguageModel](https://ai-sdk.dev/docs/reference/ai-sdk-core/wrap-language-model))
- **Discovery/distribution:** npm; provider list; AI Gateway.
- **MCP:** **consume** — `@ai-sdk/mcp` (`createMCPClient`, stabilized in AI SDK 6).
  No first-party *serve* primitive (apps expose via a separate MCP SDK).

### Eino (Go — CloudWeGo / ByteDance)

- **Integration architecture — interface-based components + callback handlers.**
  Five layers; the Component layer defines interfaces `ChatModel`, `ChatTemplate`,
  `Retriever`, `Indexer`, `Embedding`, `Document.Loader`, `Document.Transformer`,
  `Tool`, `ToolsNode`. Official/community implementations live in **`eino-ext`**
  (OpenAI, Claude, Gemini, Ollama, Milvus, Elasticsearch, Redis, MCP tools). A
  custom tool implements `Info()` + `InvokableRun()`.
  ([components](https://pkg.go.dev/github.com/cloudwego/eino/components),
  [eino-ext SKILL](https://github.com/cloudwego/eino-ext/blob/main/skills/eino-component/SKILL.md))
- **Extension points:** every component interface + **callback handlers**
  (cross-cutting injection: OnStart/OnEnd/OnError/OnStartWithStreamInput/
  OnEndWithStreamOutput; a `Checker` interface controls callback timing) — Eino's
  answer to middleware.
  ([callback manual](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/callback_manual/))
- **Discovery/distribution:** Go modules (`eino-ext`).
- **MCP:** **consume** — `eino-ext/components/tool/mcp` wraps an MCP server's tools
  as `InvokableTool`s (and `MCPPrompt` as `ChatTemplate`). No first-party serve.

### Genkit (JS/TS + Go — Firebase / Google)

- **Integration architecture — a formal `plugin()` registration system.** A plugin
  is an initializer that runs at Genkit start and **registers actions**:
  `defineModel`, `defineEmbedder`, `defineRetriever`/`defineIndexer`,
  `defineEvaluator`, `defineTool`. One plugin can bundle a whole vendor
  (model + embedder + retriever + evaluator + telemetry) under a single name. A
  **Dynamic Action Resolver** allows just-in-time registration of a requested action
  that wasn't defined upfront.
  ([plugin authoring](https://genkit.dev/docs/js/plugin-authoring/overview/),
  [writing evaluators](https://genkit.dev/docs/plugin-authoring-evaluator/))
- **Extension points:** models, embedders, retrievers/indexers, evaluators, tools,
  telemetry — all via one plugin surface, in **both JS and Go**.
- **Discovery/distribution:** npm `genkitx-*`/`@genkit-ai/*`, Go modules,
  `awesome-genkit`.
- **MCP:** **both.** Consume (`createMcpClient`/`createMcpHost`; Go `MCPManager`/
  `MCPHost`). **Serve** (`createMcpServer` in JS; Go `go/plugins/mcp` —
  `NewServer` + tools then `ServeStdio()`, consumable by Claude Desktop / VS Code).
  ([Go mcp plugin](https://pkg.go.dev/github.com/firebase/genkit/go/plugins/mcp),
  [js/plugins/mcp](https://github.com/firebase/genkit/tree/main/js/plugins/mcp))

### Mastra (TS)

- **Integration architecture — provider interfaces + integrations layer +
  registry.** `MastraStorage` (PostgreSQL/LibSQL/MongoDB) and `MastraVector` are
  provider interfaces; Memory is layered (storage + vector + processors);
  tools/agents/workflows are registerable primitives via `createTool` (AI-SDK
  `tool()`-compatible).
  ([configuration](https://mastra.ai/reference/configuration),
  [memory & storage](https://deepwiki.com/mastra-ai/mastra/7-memory-and-storage-architecture))
- **Extension points:** storage providers, vector providers, memory processors,
  tools, agents, workflows.
- **Discovery/distribution:** npm `@mastra/*` + the **"MCP Registry Registry"** (a
  hub *of* MCP registries).
- **MCP:** **both** — `MCPClient` (consume many servers) and `MCPServer` that
  exposes tools, **agents** (auto-converted to `ask_<agentKey>`) and **workflows**
  (`run_<workflowKey>`) over stdio/HTTP.
  ([MCPServer](https://mastra.ai/reference/tools/mcp-server),
  [MCP overview](https://mastra.ai/docs/mcp/overview))

### Per-tool mechanism summary

| Framework | Extension architecture | Pluggable points (beyond tools) | MCP serve |
|---|---|---|---|
| **LangChain/LangGraph** | package-based, `langchain-core` interfaces + **agent middleware** (v1) | models, embeddings, vector stores, retrievers, parsers, callbacks, middleware; Hub for prompts | **serve** (LangGraph Agent Server `/mcp`, Streamable HTTP) |
| **LlamaIndex** | namespace packages `llama-index-{cat}-{prov}` (300+) | LLMs, embeddings, vector stores, retrievers, **loaders (LlamaHub)**, node parsers, callbacks | **serve** (expose agent tools) |
| **Pydantic-AI** | interface-based: `Model`/`Provider`/`ModelProfile` + `AbstractToolset` | models, providers, profiles, toolsets | **serve** (via FastMCP) |
| **Vercel AI SDK** | provider **specification** (`LanguageModelV2/V3`) + registry + **`wrapLanguageModel` middleware** | model providers, embedding providers, middleware, registry | consume only |
| **Eino** | interface components + **callback handlers** | ChatModel/Embedding/Retriever/Indexer/Loader/Transformer/Tool + callbacks | consume only |
| **Genkit** | **`plugin()` registration** of actions | `defineModel`/`defineEmbedder`/`defineRetriever`/`defineEvaluator`/`defineTool` | **serve** (JS `createMcpServer`; Go `mcp.NewServer`) |
| **Mastra** | provider interfaces (`MastraStorage`/`MastraVector`) + integrations + registry | storage, vector, memory processors, tools, agents, workflows | **serve** (tools + agents + workflows) + MCP Registry Registry |

**Cross-cutting pattern read.** Three recurring shapes dominate the arena:
1. **A provider specification/interface** every backend implements (Vercel
   `LanguageModelV2`, Pydantic `Model`, LangChain `VectorStore`, Mastra
   `MastraVector`, Eino components).
2. **A middleware/callback seam** to wrap the pipeline (Vercel `wrapLanguageModel`,
   Eino callbacks, LangChain agent middleware).
3. **MCP as a two-way bus** — the strong frameworks all **serve**, not just consume,
   so their primitives become usable by any MCP client. Only Vercel and Eino are
   consume-only, and both are the "SDK, not platform" members of the set.

---

## Part B — aisotropy's current extension surface (code-truth)

### What IS pluggable today

1. **Typed in-process tools** — `WithTypedTool[T]` (`aisotropy/tools.go:38-59`). A
   Go struct drives the model-facing JSON Schema, argument decoding, and
   fail-closed reflection; scope-gated by `requiredScope`. (ADR-0016 lever #1.)
2. **MCP consume** — `WithMCPServer(scope, command, args...)`
   (`aisotropy/mcp.go:31`). At boot the runtime spawns a stdio MCP subprocess,
   lists its tools, and adapts each into a scope-gated aisotropy tool
   (`internal/api/mcp_tools.go` `BuildMCPTools`; wired in `aisotropy/app.go`). YAML
   config equivalent: `MCPServerConfig` → `WithMCPServer` (`aisotropy/config.go:60-130`).
3. **Declarative plugin manifests** — `internal/plugin` (`manifest.go`,
   `loader.go`, `registry.go`) + `WithPlugin`/`WithPluginDir`
   (`aisotropy/extend.go:498-509`) + `AISOTROPY_PLUGIN_DIR`. A closed `kind` enum —
   `mcp` | `go-import` | `wasm` (`manifest.go:222-229`) — with a **required
   permissive-license gate** (`manifest.go:149-174, 216-221`). All three kinds
   ultimately contribute **tools** only (`loader.go:86-98`): MCP (out-of-process),
   go-import (compile-time descriptor, handlers via a real Go import,
   `loader.go:179-196`), WASM (in-process, memory-isolated sandbox,
   `loader.go:147-177`). Discovery is a **local, non-recursive directory scan**
   (`registry.go:40-72`).
4. **Six "policy" seams (open-core interface aliases + `WithX` options)** —
   `aisotropy/extend.go`: `GuardrailCheck` (`WithGuardrailCheck`), `Reranker`
   (`WithReranker`), `Summarizer` (`WithSummarizer`), `EvalJudge` (`WithEvalJudge`),
   `ModelPolicy` (`WithModelPolicy`), `TenantRoutingPolicy`
   (`WithTenantRoutingPolicy`) — `extend.go:71-278, 384-490`. All six are
   fully implementable from an external module (payload types re-exported) and all
   six are wired end-to-end (`extend.go:31-58`).
5. **Two sealed-driver backend seams** — `VectorBackend` (`WithVectorBackend`,
   `extend.go:279-306, 433`) and `DocBackend` (`WithFactsBackend`,
   `extend.go:307-332, 448`). A consumer supplies a **narrow, tenant-UNAWARE**
   driver (opaque namespace + opaque ids); the runtime wraps it in a sealed scoping
   decorator that derives the `(tenant,user)` namespace from the validated Scope on
   every call — "bring your own backend, NOT your isolation" (`extend.go:19-29`).
6. **Env-selected backends** — `llm.RegistryFromEnv` (openrouter/openai/anthropic/
   stub, `internal/llm/registry.go:134-162`), `semantic.OpenFromEnv` (Qdrant vs
   in-memory, `internal/semantic/factory.go:18-37`), `facts.OpenFromEnv`
   (rqlite/SQLite/in-memory, `internal/facts/factory.go:18-29`). Also
   `WithModelPolicy` chains atop the `llm.Registry` (`registry.go:38-59`).
7. **The scope gate over all of the above** — `internal/tools/tools.go:43-59`
   (`Registry.Permitted`): a tool whose `RequiredScope` isn't granted is never
   injected into the model payload. Every extension path lands in this one registry,
   so it is governed identically to a built-in.

### What is NOT pluggable today

- **MCP serve is absent.** A repo-wide grep for an MCP *server* in non-test source
  finds only `WithMCPServer` (the **consume** path) and doc comments — aisotropy is
  MCP **client-only**. It can ingest the ecosystem's tools but cannot publish its
  own tools *as* an MCP endpoint. **Notably, the serve primitive is already in the
  dependency tree**: `github.com/fugue-labs/gollem v0.5.1` (`go.mod`) ships
  `ext/mcp` with `Server` — `NewServer(...)`, `AddTool(tool, handler)`,
  `SetResources`, `SetPrompts`, `HTTPServerTransport` (Streamable HTTP), and SSE
  (`gollem@v0.5.1/ext/mcp/server.go`, `server_http.go`, `sse.go`). It is present but
  **unused**.
- **The manifest surface stops at tools.** A plugin can contribute only tools
  (`loader.go:86-98`). There is **no declarative kind** for an embedder, retriever,
  vector store, or model **provider** — those are Go-option (`WithVectorBackend`/
  `WithFactsBackend`/`WithModelPolicy`) or env-selected (factories) only. Genkit's
  `plugin()`-registers-many-action-types has no aisotropy equivalent.
- **No generic middleware/interceptor seam.** aisotropy has *targeted, named* policy
  seams (§4 above), not a single `wrapLanguageModel`/callback-handler chain a third
  party registers to wrap the whole pipeline.
- **No registry/marketplace/index.** Discovery is a local directory scan
  (`registry.go:40`); there is no remote index, no versioned distribution, no
  LlamaHub/Mastra-registry analogue.
- **Sealed-driver payload types not yet re-exported.** `BackendVector`/`BackendHit`
  and `BackendDoc`/`BackendDocHit` are not aliased on the façade
  (`extend.go:301-304, 330-331`), so an **external** module cannot yet fully
  implement `WithVectorBackend`/`WithFactsBackend` (in-repo it can).

---

## Part C — prioritized adoption candidates

Each candidate: what it is · who does it well · mapping onto aisotropy's existing
seams (file:line) · effort/blast-radius · constitutional fit (tenant isolation,
scope-gate, license, sandbox/pre-LLM gates) · priority.

### P1 — MCP serve: publish aisotropy's scope-gated tools as an MCP server

- **What.** Add the missing half of MCP interop: expose the runtime's existing
  `toolset` + `typedHandlers` as an MCP server (stdio + Streamable HTTP), so any MCP
  client (Claude Desktop, Cursor, another aisotropy, LangGraph) can call
  aisotropy's tools. Optionally expose the agent itself as one tool (Mastra's
  `ask_<agentKey>` pattern).
- **Who does it well.** Mastra (`MCPServer` — tools + agents + workflows), Genkit
  (Go `mcp.NewServer` + `ServeStdio`), Pydantic-AI (FastMCP), LangGraph (`/mcp`
  Streamable HTTP).
- **Maps onto existing seams.** This is the *mirror* of the consume path and reuses
  the same shapes: iterate `tools.Registry.Permitted(scopes)`
  (`internal/tools/tools.go:43-59`) and dispatch through the existing
  `api.TypedToolHandler` map. **The serve primitive is already vendored** — gollem
  `ext/mcp.Server.AddTool(tool, handler)` + `HTTPServerTransport`
  (`gollem@v0.5.1/ext/mcp/server.go:120,138`, `server_http.go:32`). Identity/scope
  come from the same ctx-derived Scope the chat path already uses at the Huma
  boundary. A new `/mcp` route mounts it, analogous to the existing
  out-of-OpenAPI-contract routes.
- **Effort / blast radius.** **Medium.** The primitive exists; the work is (a) a
  per-request server built from the caller's scope-permitted tool subset, (b) an
  HTTP mount + auth binding to the JWT scope-gate, (c) a `WithMCPServe`/config
  toggle. **Additive** — one new route, zero change to existing chat/stream paths.
- **Constitutional fit.** **Strong.** The scope gate maps directly — only
  `Permitted(scopes)` tools are ever served, so "governance by absence"
  (`tools.go:1-8`) holds on the serve side too. Tenant isolation: bind served tools
  to the caller's validated Scope from ctx (identical to chat). License: gollem is
  MIT/Apache and already a dependency. One gate to honor: a served tool that calls
  the model must still pass the pre-LLM quota/circuit-breaker gate ("reject/degrade
  before spending tokens").
- **Priority: P1 — and the top recommendation.** It closes the single genuine
  asymmetry (consume-but-can't-publish) that separates aisotropy from every
  serve-capable competitor, at low incremental risk *because the primitive is
  already in the tree and the scope gate already exists*. It also unlocks
  composability: aisotropy instances (and its own typed tools) become first-class
  citizens of the MCP ecosystem, both directions.

### P2 — Re-export the sealed-driver payload types (finish "bring your own backend")

- **What.** Alias `BackendVector`/`BackendHit` and `BackendDoc`/`BackendDocHit` on
  the public façade so an **external** module can fully implement `WithVectorBackend`
  / `WithFactsBackend`. Today five/six policy seams are externally implementable but
  the two sealed backends are not (`extend.go:301-304, 330-331`).
- **Who does it well.** Everyone — LlamaIndex `BasePydanticVectorStore`, Mastra
  `MastraVector`, LangChain `VectorStore` are all fully externally implementable.
- **Maps onto existing seams.** `aisotropy/extend.go` `VectorBackend`/`DocBackend`
  aliases (`extend.go:279-332`); the conformance + isolation-proof test harnesses
  **already exist** (`internal/plugintest` `RunVectorBackendSuite` /
  `RunDocBackendSuite`, referenced at `extend.go:302-304, 331`).
- **Effort / blast radius.** **Small.** Type aliases + doc; the seal, wiring, and
  test suites are already built. Purely additive.
- **Constitutional fit.** **Perfect.** The sealed scoping decorator is unchanged;
  this only lets the *tenant-unaware driver* be authored from outside the repo. No
  new isolation surface.
- **Priority: P2.** High ROI: it completes a "bring your own vector/facts store"
  story that is already ~90% built and is table-stakes versus the arena. Good
  precondition for any future backend-plugin work.

### P2 — A declarative "provider" plugin kind, scoped to the isolation-safe slice

- **What.** Let a manifest declaratively contribute a **model provider or embedder**
  — the way Genkit's `plugin()` bundles model+embedder+retriever, or Vercel's
  `@ai-sdk/*` provider packages register into `createProviderRegistry`. Do it
  **only** for out-of-process/sandboxed sources (an MCP-served embedder/model, or a
  WASM module), never in-process Go behavior.
- **Who does it well.** Genkit (`plugin()` registering multiple action types),
  Vercel (`createProviderRegistry` + provider spec), LlamaIndex
  (`llama-index-{cat}-{prov}`).
- **Maps onto existing seams.** The manifest `kind` enum (`manifest.go:222-229`)
  currently yields only tools; extend it (or add a `provides:` field) to also target
  `llm.Registry.Register` (`registry.go:52`) and the embedder seam. The
  sealed-driver seams (`WithVectorBackend`/`WithFactsBackend`) and `WithModelPolicy`
  are the wiring targets.
- **Effort / blast radius.** **Large.** Go has no safe runtime plugin loading — the
  manifest's own `go-import` limitation (`loader.go:62-70`) — so a *declarative*
  provider can realistically only be an **MCP-served** or **WASM** provider, not an
  arbitrary Go one. That constraint is real and narrows the achievable scope.
  Depends on the P2 payload re-export above for the backend case.
- **Constitutional fit.** **OK if narrow, risky if broad.** The sealed-driver
  invariant (tenant-unaware drivers, `extend.go:19-29`) must hold; the report's own
  warning against exposing raw `facts.Store`/`semantic.Store` still applies. An
  MCP-served embedder is isolation-safe (out-of-process, runtime owns scope).
- **Priority: P2 (do the narrow slice only).** Ship the MCP-served-embedder/model
  path; **defer** any general in-process backend plugin. This gives Genkit-style
  "one plugin, many capabilities" ergonomics without breaching the isolation model.

### P3 — Plugin registry index / distribution (defer until there is something to index)

- **What.** A remote index / versioned distribution so plugins can be listed,
  pinned, and fetched — an analogue of LlamaHub, Mastra's "MCP Registry Registry",
  or the Vercel provider list, layered over the current local scan.
- **Who does it well.** LlamaHub, Mastra MCP Registry Registry, npm/PyPI namespaces.
- **Maps onto existing seams.** `registry.go` today is `Load(dir)` local-scan only
  (`registry.go:40-72`); an index adds a fetch+verify+cache layer feeding the same
  `Registry`.
- **Effort / blast radius.** **Medium-large**, and it introduces a **network +
  supply-chain** surface.
- **Constitutional fit.** **Tension.** Must obey the per-manifest license gate
  (already enforced, `manifest.go:216-221`) plus digest pinning (the repo's
  supply-chain-defense rule). The WASM sandbox (`loader.go:147-177`) already makes
  *executing* untrusted plugins safe; distribution is a separate, additive risk.
- **Priority: P3 — defer.** Honest framing: there are **~0 third-party plugins
  today**. An index of nothing is premature; the ecosystem gap is network-effects,
  not mechanism. Build this once P1/P2 have produced publishable artifacts (an
  MCP-served aisotropy is itself the first thing worth indexing).

### P3 — Generic request/model middleware seam (prefer more named seams instead)

- **What.** A `wrapLanguageModel`-style or Eino-callback-style handler chain a third
  party registers to wrap the pipeline (logging, caching, guardrail ordering).
- **Who does it well.** Vercel (`wrapLanguageModel`), Eino (callback handlers),
  LangChain v1 (agent middleware).
- **Maps onto existing seams.** aisotropy already has *targeted* seams (guardrail
  checks, reranker, model policy, summarizer — `extend.go:71-278`); a generic
  middleware would generalize them into one interceptor. But the model call already
  runs through `internal/gateway` + circuit breaker + cost meter.
- **Effort / blast radius.** **Medium**, and **constitutionally risky**: a seam that
  wraps the model call can move work *around* the pre-LLM quota/circuit-breaker gate,
  violating "reject/degrade before spending tokens."
- **Priority: P3.** aisotropy's named seams already cover the safe subset; a generic
  wrap-the-model middleware fights the constitutional pre-LLM gates. Prefer adding
  *more named seams* (e.g. an input-guardrail seam) over one broad interceptor.

### Priority summary

| Priority | Candidate | Effort | Constitutional fit | One-line rationale |
|---|---|---|---|---|
| **P1** | **MCP serve** (publish tools) | Medium | Strong | Closes the consume-but-can't-publish gap; serve primitive already vendored (gollem `ext/mcp`), scope gate already exists. **Top pick.** |
| **P2** | Re-export sealed-driver payload types | Small | Perfect | Finishes "bring your own vector/facts store" for external modules; harness already built. |
| **P2** | Declarative MCP-served provider/embedder kind | Large (narrow) | OK if narrow | Genkit-style "one plugin, many capabilities" — but only the isolation-safe out-of-process slice. |
| **P3** | Plugin registry index / distribution | Medium-large | Tension | Defer: ~0 plugins exist to index; network-effects, not mechanism. |
| **P3** | Generic model middleware seam | Medium | Risky | Fights the pre-LLM quota gate; prefer more *named* seams. |

**Bottom line.** aisotropy's extension *mechanism* is already competitive on the
architecture axis (declarative manifests, a WASM sandbox posture most rivals lack, a
uniform scope gate, six wired policy seams, two sealed backends). The one clear,
high-value, low-risk gap versus the serve-capable arena (LangGraph, Genkit,
Pydantic-AI, Mastra) is **MCP serve** — and because the gollem serve primitive is
already a dependency and the scope gate is already the governing mechanism, it is
both the most impactful and among the cheapest patterns to adopt. Everything else is
either a small finisher (P2 re-export), a scoped extension (P2 provider kind), or
correctly deferred (P3 index, P3 generic middleware).

---

## Sources

- LangChain/LangGraph — https://python.langchain.com/docs/how_to/vectorstore_retriever/ ; https://reference.langchain.com/javascript/langchain-core/vectorstores/VectorStoreRetriever ; https://deepwiki.com/langchain-ai/langchain/3.4-vector-stores ; https://docs.langchain.com/langsmith/server-mcp ; https://github.com/langchain-ai/langchain-mcp-adapters
- LlamaIndex — https://www.llamaindex.ai/blog/llamaindex-v0-10-838e735948f8 ; https://github.com/run-llama/llama_index/tree/main/llama-index-integrations ; https://developers.llamaindex.ai/python/framework/integrations/embeddings/custom_embeddings/ ; https://docs.llamaindex.ai/en/stable/module_guides/storing/vector_stores/ ; https://llamahub.ai/
- Pydantic-AI — https://pydantic.dev/docs/ai/models/overview/ ; https://pydantic.dev/docs/ai/api/models/base/ ; https://pydantic.dev/docs/ai/api/pydantic-ai/providers/ ; https://pydantic.dev/docs/ai/api/pydantic-ai/profiles/
- Vercel AI SDK — https://ai-sdk.dev/docs/ai-sdk-core/provider-management ; https://ai-sdk.dev/docs/reference/ai-sdk-core/provider-registry ; https://ai-sdk.dev/docs/ai-sdk-core/middleware ; https://ai-sdk.dev/docs/reference/ai-sdk-core/wrap-language-model ; https://tessl.io/registry/tessl/npm-ai-sdk--provider
- Eino — https://pkg.go.dev/github.com/cloudwego/eino/components ; https://github.com/cloudwego/eino-ext/blob/main/skills/eino-component/SKILL.md ; https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/callback_manual/
- Genkit — https://genkit.dev/docs/js/plugin-authoring/overview/ ; https://genkit.dev/docs/plugin-authoring-evaluator/ ; https://pkg.go.dev/github.com/firebase/genkit/go/plugins/mcp ; https://github.com/firebase/genkit/tree/main/js/plugins/mcp
- Mastra — https://mastra.ai/reference/tools/mcp-server ; https://mastra.ai/docs/mcp/overview ; https://deepwiki.com/mastra-ai/mastra/7-memory-and-storage-architecture ; https://mastra.ai/reference/configuration
- aisotropy code — this repo: `aisotropy/{tools,mcp,extend,config}.go`, `aisotropy/app.go`, `internal/plugin/{manifest,loader,registry}.go`, `internal/tools/tools.go`, `internal/llm/registry.go`, `internal/semantic/factory.go`, `internal/facts/factory.go`, `go.mod` (gollem v0.5.1 `ext/mcp`)
