# Arena — Extensibility / Plugins (2026-07-17)

> **What this measures.** The per-turn arena modes score *behavior* on a single
> request. This dimension scores *architecture*: is the framework **extensible**
> (can a third party add a custom tool/component/integration without forking it),
> does it speak **MCP** (the emerging tool-interop standard — consume and/or
> serve), how **broad** is its pre-built ecosystem, and how large is its
> **community**. Aisotropy has no community today; the honest question is whether
> it at least has the **mechanism**. This report answers that with code-truth for
> aisotropy (a real, executed smoke) and cited sources for the competitors.

- **aisotropy claims** cite `file:line` in this repo (code, not docs).
- **Competitor claims** cite upstream URLs.
- **Smoke evidence** is the captured stdout/stderr of a throwaway harness that
  boots aisotropy's *public* builder and loads real plugin manifests. No
  `internal/` or `aisotropy/` source was modified; the harness lives only in a
  scratch dir.

---

## Part A — aisotropy's extension mechanism (code-truth)

Aisotropy exposes **four** ways to grow the toolset "without editing the core"
(the ADR-0016 thesis). All four converge on the *same* scope-gated shapes the
native path uses — `[]tools.Tool` + `map[string]api.TypedToolHandler` — so an
extension tool is governed identically to a built-in one.

### 1. Typed in-process tools — `WithTypedTool[T]` (lever #1)

`aisotropy/tools.go:38` — a Go struct `T` drives three things at once: the JSON
Schema shown to the model (from `T`'s tags), decoding of the model's arguments
into `T`, and fail-closed reflection when the arguments don't decode
(`aisotropy/tools.go:38-59`). The tool is scope-gated by `requiredScope` exactly
like any built-in (`tools.go:45-49`). This is the "struct as the single contract"
thesis applied to tool inputs.

### 2. MCP servers — `WithMCPServer` (lever #2, polyglot execution)

`aisotropy/mcp.go:31` registers a stdio MCP server (any language). At boot the
runtime spawns `command args...`, lists its tools, and adapts each into a
scope-gated aisotropy tool carrying the server's JSON Schema
(`aisotropy/app.go:634-651`). Discovery + adaptation happens in
`internal/api/mcp_tools.go:36` (`BuildMCPTools`) — one dispatch handler per
discovered tool (`mcp_tools.go:50`, `mcp_tools.go:62`), args validated against
the server's `InputSchema` with reflection on violation (`mcp_tools.go:63-75`),
result rendered to text (`formatMCPResult`, `mcp_tools.go:89`). The consumer
never imports an MCP library — `command`, `args`, `scope` are the entire surface
(`mcp.go:29-30`). This is **consume-only**: aisotropy is an MCP *client*, not an
MCP *server* (a repo-wide grep for `mcp.NewServer`/`ServeMCP` finds nothing —
aisotropy does not expose its own tools *as* an MCP endpoint).

### 3. Declarative plugin manifests — `internal/plugin` + `WithPlugin` / `WithPluginDir` / `AISOTROPY_PLUGIN_DIR`

The richest surface. A **plugin** is an `aisotropy-plugin.yaml` file plus a way to
launch what it describes (`internal/plugin/manifest.go:1-26`). What the manifest
supports (`manifest.go:86-142`):

| Field | Meaning | Enforcement |
|---|---|---|
| `name`, `version` | stable id + pin/audit version | required (`manifest.go:210-215`) |
| `license` | SPDX-ish id | **required + permissive-allowlist gated** — MIT/BSD-2/3/Apache-2.0/ISC only; copyleft & source-available rejected (`manifest.go:149-174`, `216-221`) |
| `kind` | launch mechanism | closed enum `mcp` \| `go-import` \| `wasm`; unknown value rejected at load, not request time (`manifest.go:222-229`) |
| `trust` | isolation posture (`trusted-inproc` \| `sandboxed`) | validated metadata (`manifest.go:230-235`) |
| `run.command`/`run.args` | stdio MCP subprocess spec | `command` required for `kind: mcp` (`manifest.go:236-238`) |
| `run.module` | path to a `.wasm` module | required for `kind: wasm` (`manifest.go:239-241`) |
| `tools[]` | scope-gated tool descriptors (`name`, `description`, `required_scope`) | `name` required each (`manifest.go:242-246`) |
| `personas[]` | persona identifiers (metadata only) | surfaced, never fabricated into a prompt (`app.go:718-728`) |

**How plugins are loaded** — the three kinds (`internal/plugin/loader.go:86-98`):

- **`kind: mcp`** → connect the declared stdio subprocess through an *injected*
  connector (`loader.go:114-131`) and adapt its tools via the same
  `BuildMCPTools` path as `WithMCPServer`. This is **out-of-process** execution:
  behavior lives in the subprocess; the Go core keeps owning scope + identity.
  The connector is injected so `internal/plugin` imports no MCP client library
  (`loader.go:21-28`); `aisotropy/app.go:698-704` supplies the real one
  (gollem's `mcp.NewStdioClient` + `client.Close`).
- **`kind: go-import`** → Go has no safe runtime plugin loading, so this is a
  documented **compile-time convention**, not a dynamic load: the manifest's
  declared tools become scope-gated *governance descriptors* with **no handlers**
  (`loader.go:179-196`); the behavior arrives via a normal Go import the consumer
  links. Stated plainly rather than faked (`loader.go:64-70`).
- **`kind: wasm`** → load the `.wasm` module into the wazero sandbox
  (`internal/wasmtool`) and expose its ABI entry point as **one** scope-gated
  tool (`loader.go:147-177`). Runs **in-process but memory-isolated**: WASI
  present-but-powerless, no filesystem/network/clock/ambient authority by default
  (`manifest.go:55-62`, `loader.go:159-165`). This is the intended path for
  *untrusted community code* — a genuinely differentiated posture.

Discovery is a directory scan for `*plugin*.yaml`/`*plugin*.yml`
(`registry.go:29`, `registry.go:40-72`), strict (a malformed or copyleft manifest
fails the scan loudly rather than being silently dropped, `registry.go:35-39`).
Wiring at boot merges every loaded plugin's tools + handlers into the same
`toolset`/`typedHandlers` maps the native and MCP paths populate
(`app.go:659-731`), and is **inert-safe**: no `WithPlugin`/`WithPluginDir` and an
unset `AISOTROPY_PLUGIN_DIR` leaves the toolset byte-for-byte unchanged
(`app.go:672-677`). Every load is best-effort: a bad manifest/dir/plugin is
logged and skipped, never downing the runtime (`app.go:668-670`, `705-710`).

### 4. Scope gate (applies to all of the above)

`internal/tools/tools.go:43-59` — `Registry.Permitted(scopes)` omits any tool
whose `RequiredScope` isn't granted. Governance by absence: an ungranted tool is
*never injected into the model payload* (`tools.go:1-8`, `61-75`). Extension tools
get this for free because they land in the same registry.

---

## Part A (cont.) — SMOKE: proving the mechanism actually works

A throwaway harness (`scratchpad/ext-smoke/`, outside the repo) uses **only**
aisotropy's public builder and gollem's public MCP API. It:

1. builds a trivial stdio MCP server (`mcpserver/main.go`) exposing one tool
   `smoke_echo`, served with gollem's `mcp.ServeStdio`;
2. writes two **real** manifests into a plugin dir — a `kind: mcp` one pointing
   `run.command` at that server binary, and a `kind: go-import` one declaring a
   descriptor tool;
3. points `AISOTROPY_PLUGIN_DIR` at the dir, constructs `aisotropy.New()`
   (keyless stub LLM — no provider key), runs one `app.Turn(...)`, and captures
   the runtime's own `slog` output.

**Captured output (verbatim):**

```
================ RUNTIME LOGS ================
time=2026-07-17T21:19:31.527-03:00 level=INFO msg="plugin tools registered" plugin=smoke-goimport kind=go-import tools=1 handlers=0
time=2026-07-17T21:19:31.883-03:00 level=INFO msg="plugin tools registered" plugin=smoke-mcp kind=mcp tools=1 handlers=1
=============================================
Turn completed. Reply length=53

RESULT: mcp_plugin_loaded=true go_import_plugin_loaded=true
SMOKE PASSED
```

**What this proves (not just "the code exists"):**

- `plugin=smoke-mcp kind=mcp tools=1 handlers=1` — aisotropy **spawned the
  out-of-process MCP subprocess**, completed the MCP `initialize` handshake,
  discovered `smoke_echo` via `tools/list`, built a **live dispatch handler**
  (`handlers=1`), and merged it scope-gated into the registry. The whole
  manifest→license-gate→validate→spawn→discover→register chain ran for real.
- `plugin=smoke-goimport kind=go-import tools=1 handlers=0` — the declarative
  descriptor loaded (`handlers=0`, exactly as `loader.go:179-196` documents:
  descriptor-only, handler arrives via a Go import).
- `Turn completed` — the runtime booted and served a request with the plugins
  wired in; loading did not down the core.

**Honest scope of the smoke:** it proves **load + registration + a live handler**
end-to-end from a real subprocess. It does **not** drive the model to *invoke*
`smoke_echo` (the keyless stub LLM doesn't emit tool calls deterministically), so
the tool-*execution* leg is covered by the repo's own tests
(`internal/plugin/loader_test.go` `TestLoadTools_MCP_WiresToolsAndScope`,
`aisotropy/plugin_failure_test.go` `TestStartRuntime_PluginDirFromEnv`) rather
than re-driven here. The WASM sandbox path is likewise covered by
`loader_test.go` `TestLoadTools_WASM_LoadsSandboxedTool` (not re-run in this
smoke because it needs a compiled `.wasm` fixture).

---

## Part B — the competitors' extension ecosystems

### LangChain / LangGraph (Python + JS/TS)
- **Mechanism:** subclass `BaseTool` / use the `@tool` decorator; every
  integration is a first-class package. The `langchain-community` +
  provider-partner packages number in the **hundreds** (LLMs, vector stores,
  loaders, tools).
- **MCP:** consume via `langchain-mcp-adapters`, which converts any MCP server's
  tools into LangChain/LangGraph tools ("hundreds of existing MCP servers without
  writing custom adapters"); LangChain tools can also be exposed the other way.
  [langchain-mcp-adapters](https://github.com/langchain-ai/langchain-mcp-adapters),
  [LangChain MCP docs](https://docs.langchain.com/oss/python/langchain/mcp).
- **Ecosystem:** **L** — the broadest in the space.
- **Community:** **large** (the reference ecosystem everyone integrates against).

### LlamaIndex (Python + TS)
- **Mechanism:** `BaseToolSpec` / `FunctionTool`; **LlamaHub** packs the community
  registry (**200+** data loaders plus agent tools; since v0.10 folded into the
  `llama-index-integrations` monorepo).
  [LlamaHub](https://llamahub.ai/),
  [Data Connectors](https://developers.llamaindex.ai/python/framework/module_guides/loading/connector/).
- **MCP:** consume via `llama-index-tools-mcp` (`McpToolSpec` inherits
  `BaseToolSpec`, converts MCP tools to `FunctionTool`s); LlamaIndex agent tools
  can also be **exposed as** MCP tools.
  [MCP ToolSpec](https://llamahub.ai/l/tools/llama-index-tools-mcp),
  [expose as MCP](https://docs.llamaindex.ai/en/stable/examples/tools/mcp_agent_tools/).
- **Ecosystem:** **L** (RAG/loader-heavy). **Community:** **large**.

### Pydantic-AI (Python)
- **Mechanism:** `@agent.tool` decorators, plus `AbstractToolset` you subclass for
  custom toolsets (ships `FileSystemToolset`, `SkillsToolset`, `TodoToolset`).
  [Toolsets](https://ai.pydantic.dev/toolsets/).
- **MCP:** **both** — client via `MCPToolset` (built on **FastMCP**; stdio +
  Streamable HTTP + SSE + in-process), and **server** (Pydantic AI can serve its
  tools over MCP).
  [MCP overview](https://pydantic.dev/docs/ai/mcp/overview/),
  [MCP server](https://pydantic.dev/docs/ai/mcp/server/).
- **Ecosystem:** **M** — leans on the FastMCP ecosystem rather than a first-party
  integration hub. **Community:** **small–medium** (young, fast-rising).

### Vercel AI SDK (JS/TS)
- **Mechanism:** the `tool()` helper for custom tools; **provider packages**
  (`@ai-sdk/openai`, `@ai-sdk/anthropic`, `@ai-sdk/google`, …) — dozens, spanning
  most model vendors.
- **MCP:** consume via `@ai-sdk/mcp` (`createMCPClient`) over stdio/SSE/HTTP —
  **stabilized in AI SDK 6 (2025-12-22)** with OAuth + HTTP transport (was
  `experimental_createMCPClient`). Automatic MCP↔AI-SDK tool conversion, plus
  resources/prompts/elicitation.
  [createMCPClient](https://ai-sdk.dev/docs/reference/ai-sdk-core/create-mcp-client),
  [AI SDK 6](https://vercel.com/blog/ai-sdk-6).
- **Ecosystem:** **L** (huge npm reach). **Community:** **large**.

### Eino (Go — CloudWeGo / ByteDance)
- **Mechanism:** implement the `InvokableTool` interface; official + community
  component implementations live in **`eino-ext`** (chat models, tools,
  retrievers, prompts for OpenAI/Ollama/etc.).
  [eino-ext](https://github.com/cloudwego/eino-ext).
- **MCP:** consume — `eino-ext/components/tool/mcp` wraps an MCP server's tools as
  `InvokableTool`s (and `MCPPrompt` as a `ChatTemplate`). No first-party MCP
  *serve* documented.
  [Eino Tool - MCP](https://www.cloudwego.io/docs/eino/ecosystem_integration/tool/tool_mcp/).
- **Ecosystem:** **M** (`eino-ext` is real but narrower than the JS/Python hubs).
  **Community:** **small–medium** (ByteDance-backed, strong in its region).

### Genkit (JS/TS + Go — Firebase / Google)
- **Mechanism:** a formal **plugin** system — `genkitx-*` / `@genkit-ai/*`
  plugins (Ollama, Express, Redis, Auth0, Langfuse, …) plus `defineTool`.
  [awesome-genkit](https://github.com/xavidop/awesome-genkit).
- **MCP:** **both** — consume via `createMcpClient` / `createMcpHost`, and
  **serve** Genkit tools/prompts via `createMcpServer` (the `genkitx-mcp` /
  `go/plugins/mcp` plugin).
  [Genkit MCP plugin](https://github.com/firebase/genkit/tree/main/js/plugins/mcp).
- **Ecosystem:** **M** (curated Google-adjacent plugin set). **Community:**
  **medium** (Google-backed).

### Mastra (TS)
- **Mechanism:** `createTool` (and Vercel-AI-SDK-`tool()` compatible) plus an
  integrations layer; tools/agents/workflows are all registerable primitives.
  [Tools & MCP](https://mastra.ai/en/docs/agents/using-tools-and-mcp).
- **MCP:** **both** — `MCPClient` (connect to many servers) and `MCPServer`
  (expose Mastra tools, **agents**, and workflows as MCP tools). Mastra also runs
  an **"MCP Registry Registry"** — a hub of MCP registries.
  [MCPClient](https://mastra.ai/en/reference/tools/mcp-client),
  [MCPServer](https://mastra.ai/reference/tools/mcp-server),
  [MCP Registry Registry](https://mastra.ai/mcp-registry-registry).
- **Ecosystem:** **M–L** (fast-growing). **Community:** **medium–large** (one of
  the fastest-rising TS agent frameworks).

---

## Part C — the comparison table + honest verdict

| Framework | Plugin mechanism (kind) | MCP | Ecosystem breadth | Community |
|---|---|---|---|---|
| **LangChain / LangGraph** | Y — `BaseTool`/`@tool` + hundreds of `langchain-*` packages | **consume** (+ tools convertible to MCP) | **L** — hundreds (LLMs, stores, loaders, tools) | **large** |
| **LlamaIndex** | Y — `BaseToolSpec`/`FunctionTool` + LlamaHub packs | **consume + expose** | **L** — 200+ loaders + tools | **large** |
| **Pydantic-AI** | Y — `@agent.tool` + `AbstractToolset` | **consume + serve** (FastMCP) | **M** — via FastMCP ecosystem | **small–medium** |
| **Vercel AI SDK** | Y — `tool()` + dozens of provider pkgs | **consume** (`@ai-sdk/mcp`, stable v6) | **L** — huge npm reach | **large** |
| **Eino** | Y — `InvokableTool` + `eino-ext` components | **consume** | **M** — `eino-ext` | **small–medium** |
| **Genkit** | Y — `plugin()` + `defineTool` | **consume + serve** | **M** — `genkitx`/`@genkit-ai` | **medium** (Google) |
| **Mastra** | Y — `createTool` + integrations | **consume + serve** (+ MCP registry) | **M–L** — integrations + registry | **medium–large** |
| **aisotropy** | Y — **declarative manifest** (`mcp`/`go-import`/**`wasm`**) + `WithTypedTool` + `WithMCPServer` | **consume only** (no serve) | **S** — 0 pre-built; mechanism only | **none** (0–2 consumers) |

### Verdict — "prepared to support extensions" ≠ "has a mature extension ecosystem"

Aisotropy sits on the honest side of that line: it is the **former**, not the
latter, and the two should not be conflated.

**What aisotropy genuinely HAS (verified, code-cited, smoke-proven):**
- A real, working plugin mechanism: declarative `aisotropy-plugin.yaml` manifests
  with a license gate, three launch kinds, out-of-process MCP tool execution, a
  typed in-process tool registry, and `AISOTROPY_PLUGIN_DIR` zero-code discovery —
  all landing in one scope-gated registry. The smoke above loaded a real MCP
  subprocess plugin and a go-import descriptor at boot and served a turn.
- One thing most competitors **don't** offer out of the box: a **WASM sandbox**
  kind for running *untrusted* community code in-process with no ambient authority
  (`manifest.go:55-62`, `loader.go:147-177`). On isolation posture, aisotropy is
  ahead of the pack, not behind it.
- MCP-standard interop on the **consume** side, identical to how the mainstream
  frameworks ingest third-party tools.

**What aisotropy genuinely LACKS (equally important to state):**
- **Ecosystem breadth: near zero.** There is no `aisotropy-community` package set,
  no LlamaHub/langchain-community analogue, no dozens of pre-built providers. The
  mechanism can load a plugin; there are essentially **no plugins to load**.
- **No marketplace / registry / discovery** comparable to LlamaHub, the Vercel
  provider list, or Mastra's MCP Registry Registry.
- **No community: 0–2 consumers today.** Contribution volume is nil; there is no
  third-party author base producing manifests.
- **MCP serve is absent.** Pydantic-AI, Genkit, and Mastra can *expose* their
  primitives as an MCP server; aisotropy is client-only. It can consume the
  ecosystem but cannot yet publish into it.

**Fair framing.** On the *architecture* axis this dimension actually measures,
aisotropy is competitive — arguably differentiated by the WASM-sandbox trust
posture and the uniform scope gate. On the *ecosystem/community* axis, it is at
zero and it would be dishonest to imply otherwise: the mainstream frameworks win
this outright on breadth and adoption, and that gap is a function of age and
network effects, not of a missing mechanism. Aisotropy is **prepared to support
extensions**; it does **not** have a mature extension ecosystem. Both halves of
that sentence are true, and the report states both.

---

## Sources

- LangChain MCP adapters — https://github.com/langchain-ai/langchain-mcp-adapters ; https://docs.langchain.com/oss/python/langchain/mcp
- LlamaHub — https://llamahub.ai/ ; MCP ToolSpec — https://llamahub.ai/l/tools/llama-index-tools-mcp ; expose-as-MCP — https://docs.llamaindex.ai/en/stable/examples/tools/mcp_agent_tools/ ; connectors — https://developers.llamaindex.ai/python/framework/module_guides/loading/connector/
- Pydantic-AI — https://ai.pydantic.dev/toolsets/ ; https://pydantic.dev/docs/ai/mcp/overview/ ; https://pydantic.dev/docs/ai/mcp/server/
- Vercel AI SDK — https://ai-sdk.dev/docs/reference/ai-sdk-core/create-mcp-client ; https://vercel.com/blog/ai-sdk-6
- Eino — https://github.com/cloudwego/eino-ext ; https://www.cloudwego.io/docs/eino/ecosystem_integration/tool/tool_mcp/
- Genkit — https://github.com/firebase/genkit/tree/main/js/plugins/mcp ; https://github.com/xavidop/awesome-genkit
- Mastra — https://mastra.ai/en/reference/tools/mcp-client ; https://mastra.ai/reference/tools/mcp-server ; https://mastra.ai/mcp-registry-registry
