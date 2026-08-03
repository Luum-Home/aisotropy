---
type: Research
title: "WASM/WASI Feasibility for the Agent-Framework Arena — Can They Make Real Outbound LLM Calls?"
description: >
  Internet research on whether each candidate agent framework can run under WASM/WASI and make real outbound LLM calls.
tags: [research, wasm, wasi, arena, frameworks, feasibility]
timestamp: 2026-07-17T00:00:00Z
---

# WASM/WASI Feasibility for the Agent-Framework Arena — Can They Make Real Outbound LLM Calls?

**Date:** 2026-07-17
**Question:** Can each candidate agent framework realistically run in a **WebAssembly (WASM/WASI)** build that makes **real outbound network calls** — an OpenAI-compatible chat-completion HTTP request to a remote endpoint, and ideally a Qdrant vector-DB call over HTTP/gRPC — so the live "arena" can add a WASM execution form alongside native and Docker?
**Method:** Internet research, code-truth skeptical. A blog demo that mocks the network or runs offline does **not** count. "Compiles to wasm" is distinguished from "runs the full framework AND makes a real outbound LLM call."

---

## TL;DR verdict table

| # | Framework | Lang | Verdict | One-line reason |
|---|-----------|------|---------|-----------------|
| 1 | **Eino** (CloudWeGo) | Go | **INFEASIBLE** | Standard Go→wasip1 has no outbound sockets; the only outbound path (dispatchrun/net) needs a WasmEdge host and is unverified for Eino; TinyGo→wasi-http can't compile a reflection/`encoding-json`-heavy framework. Zero demos. |
| 2 | **Genkit Go** (Google) | Go | **INFEASIBLE** | Same Go WASI wall as Eino, heavier dependency tree, no WASM target, no demo. |
| 3 | **LangGraph / LangChain** | Python | **EXPERIMENTAL-PARTIAL** | Runs in Pyodide; `langchain_openai` installs via micropip; real browser `fetch()` LLM calls demonstrated — but fragile (httpx→`pyodide-http` patch, Node needs an XHR polyfill, browser CORS). |
| 4 | **Pydantic-AI** | Python | **EXPERIMENTAL-PARTIAL** | Official Pyodide sandbox (pydantic.run) demos PydanticAI; `pydantic-core` now ships official Emscripten wheels (PEP 783). Outbound still needs an httpx/pyodide-http shim. |
| 5 | **LlamaIndex** | Python | **INFEASIBLE (thin evidence)** | No LlamaIndex-in-WASM demo found; very large, heavy-dependency package; same httpx-in-Pyodide problem, but nobody has shown it running. |
| 6 | **Vercel AI SDK** (`ai` + `@ai-sdk/openai`) | TS/JS | **EXPERIMENTAL-PARTIAL (closest to feasible)** | JS→WASM component via ComponentizeJS/jco on Spin, with `fetch` backed by wasi-http. A directly analogous TS SDK (`@openai/agents`) is **proven** making real `api.openai.com` calls from inside a wasm component. |

**Bottom line up front:** The working assumption is **CONFIRMED for Go** (wazero / `GOOS=wasip1` genuinely cannot open outbound sockets) and **PARTIALLY REFUTED for Python/JS** — they *can* run in WASM and make real outbound calls, but only through specific runtimes (Pyodide `fetch`, or a wasi-http component host like Spin/wasmtime), not uniformly and not without HTTP-layer patching. Details and recommendation at the end.

> **OPERATOR DECISION (2026-07-17):** Adopted the "stay native + Docker only" recommendation.
> The live arena competes across **native + Docker** execution forms for all frameworks; there is
> **no uniform WASM form** (Go frameworks hard-blocked; LlamaIndex undemonstrated; Python/JS only
> experimental). WASM remains in scope **only as aisotropy's tool-sandbox capability** (wazero,
> demonstrated live) — a genuine differentiator, not a fabricated latency column. The experimental
> **Vercel AI SDK → WASM** path (ComponentizeJS/Spin + wasi-http) is **noted here but deliberately
> NOT attempted** — the evidence is an architectural analog (`@openai/agents`), not the `ai` package
> itself, so it does not meet the arena's evidence bar.

---

## The blocking technical facts (shared foundation)

These govern every verdict below.

- **WASI preview 1 (`wasip1`) has no sockets.** The official Go WASI blog states: *"A notable missing feature in the `wasip1` API is a full implementation of network sockets. `wasip1` only defines functions that operate on already opened sockets, making it impossible to support some of the most popular features of the Go standard library, such as HTTP servers."* — https://go.dev/blog/wasi
- **wazero has no wasi-http and no stable outbound sockets.** wazero's socket support is experimental (`experimental/sys` `AllowOutboundTCP`) and there is a long-standing open request to "Support dialing sockets, udp, tcp, unix" — https://github.com/tetratelabs/wazero/issues/2187 . A separate community `wasi-http` shim for wazero exists but is a third-party bolt-on — https://github.com/brendandburns/wazero-http
- **Outbound HTTP from wasm is only possible via one of three mechanisms** (per the "HTTP-request-from-inside-WASM" survey — https://github.com/vasilev/HTTP-request-from-inside-WASM ):
  1. **Host imports / wasi-http** (server-side WASI components on wasmtime/Spin/WasmEdge);
  2. **JS `fetch` interop** (browser/Node, e.g. Pyodide, Emscripten);
  3. **Custom runtime bindings** (WasmEdge socket extension, Extism, Spin SDK).
  Browser wasm cannot open raw sockets — it must delegate to host JS.
- **wasi-http (WASI 0.2 / preview 2)** is the clean, standard outbound path, implemented by **wasmtime, Spin/Fermyon, WasmEdge** — but **not** by wazero, and **not** by the standard `go` compiler's `wasip1` port.

---

## 1. Eino (CloudWeGo, Go) — **INFEASIBLE**

**Official WASM target?** None. Eino is *"an LLM application development framework in Golang"* with official ChatModel implementations for OpenAI, Ollama, etc. (https://github.com/cloudwego/eino , https://www.cloudwego.io/docs/eino/ ). No WASM/WASI build target or guide exists in its docs.

**Evidence and the two possible Go paths — both blocked:**

- **Path A — standard `go` compiler → `GOOS=wasip1`:** The full Go runtime (goroutines, `reflect`, `encoding/json`, `crypto/tls`) compiles, so Eino's Go code *would* largely compile. But `wasip1` has **no outbound sockets** (go.dev/blog/wasi, above), so its OpenAI HTTP client cannot connect on a standard host. The *only* escape is the `dispatchrun/net` (ex-`stealthrocket/net`) library, which uses `go:wasmimport` to provide `net.Dial`/`net.Listen` and auto-installs a dialer for `net/http` via `import _ "github.com/stealthrocket/net/http"` — **but only on hosts that implement the WasmEdge/`wasi-go` socket extension** (README: *"Applications built with this library are compatible with WasmEdge and wasi-go such as Timecraft"*). It does **not** work on wazero or plain wasmtime. — https://github.com/dispatchrun/net . No one has demonstrated Eino (or its OpenAI client) running this way; the claim would be pure speculation.
- **Path B — TinyGo → `wasip2` + wasi-http:** TinyGo *does* compile a subset of Go to wasip2 components, and the `wasihttp` package provides an `http.RoundTripper` for real outbound requests on wasmtime/Spin (https://wasmcloud.com/docs/wash/developer-guide/language-support/go/ , https://spinframework.dev/v2/http-outbound ). **But TinyGo cannot compile a framework like Eino:** *"encoding/json usage compiles, but panics at runtime"* due to limited `reflect` support, and *"packages… that rely heavily on reflection have partial or no support… the single biggest source of frustration"* (https://tinygo.org/docs/guides/compatibility/ , https://github.com/tinygo-org/tinygo/issues/2660 ). Eino leans on reflection, generics, and `encoding/json` throughout its graph/component system — it will not run under TinyGo.

**Blocker:** No wasm host offers Eino both (a) a compiler that accepts the full framework AND (b) outbound HTTP. Standard-Go gives the framework but no sockets (except a WasmEdge-only hack, undemonstrated); TinyGo gives sockets but not the framework.

**Verdict: INFEASIBLE today.**

---

## 2. Genkit Go (Google) — **INFEASIBLE**

**Official WASM target?** None found. Searches for "genkit go webassembly / wasm" returned no Genkit-specific WASM support, guide, or issue — only generic Go-WASM material.

**Evidence:** Genkit Go is subject to the **identical Go WASI wall** as Eino (Section 1), and is arguably worse: it pulls in a large Google-ecosystem dependency tree (telemetry, plugin providers) that increases the chance of `wasip1`/TinyGo incompatibilities. No demonstration of Genkit Go compiling to wasm, let alone making a real outbound LLM call, exists in public sources.

**Blocker:** Same as Eino — no compiler+outbound combination. Thin-to-zero evidence.

**Verdict: INFEASIBLE today.**

---

## 3. LangGraph / LangChain (Python) — **EXPERIMENTAL-PARTIAL**

**Official WASM target?** No official LangChain "WASM build," but Python-in-WASM is a real, mature runtime: **Pyodide** (CPython 3.12 compiled to WebAssembly, browser + Node) — https://pyodide.org/ . Note: LangChain's own "langchain-sandbox" uses Pyodide, but that is for *sandboxing untrusted tool code*, **not** for running LangChain itself in wasm — don't conflate them (https://github.com/langchain-ai/langchain-sandbox ).

**Real evidence of the full framework + outbound LLM call:**

- A concrete build report runs **LangChain Python inside Pyodide (WASM) in the browser and makes real, direct `fetch()` calls to LLM APIs** with no proxy: *"OpenAI / Anthropic → direct browser `fetch()` calls to the API"*, installing the provider package at runtime — *"the agent needs `langchain_openai`… installed at runtime via `micropip` inside the browser"* — https://dev.to/ozzoo/how-i-built-a-browser-native-ai-agent-platform-with-pyodide-no-backend-required-1j5d . This is the strongest positive datapoint for a Python framework making a real outbound call from wasm.

**The catches (why PARTIAL, not FEASIBLE):**

- **httpx uses sockets and fails by default.** The `openai-python` client switched from `requests` to `httpx`; in Pyodide this throws *"Host is unreachable"* because *"Pyodide cannot create direct TCP sockets."* The fix is to route through JS `fetch` via **`pyodide-http`** (patches requests/httpx onto `XMLHttpRequest`/`fetch`) — https://github.com/pyodide/pyodide/issues/4292 , https://github.com/koenvo/pyodide-http .
- **Node (not browser) needs extra work.** `pyodide-http`'s XHR patch *"will not work in Node unless you include a polyfill for XMLHttpRequest"*; async streaming needs cross-origin isolation or the `pyodide-httpx`/`pyfetch` route — https://pyodide.org/en/stable/usage/api/python-api/http.html . An arena runner is typically Node/server, so this is real friction.
- **Browser CORS.** Direct browser→`api.openai.com` calls depend on CORS/relaxed endpoints; a proxy is often needed.

**Path that works:** Pyodide (browser or Node) + `micropip install langchain langchain-openai` + `pyodide-http`/`pyodide-httpx` patch to bridge httpx to JS `fetch`. Demonstrated for LangChain in-browser; requires per-runtime HTTP shimming. Qdrant HTTP would ride the same `fetch` bridge; gRPC would not work (no raw sockets).

**Verdict: EXPERIMENTAL-PARTIAL** — works, but only through the Pyodide `fetch` bridge with HTTP-layer patching, and startup is heavy.

---

## 4. Pydantic-AI (Python) — **EXPERIMENTAL-PARTIAL**

**Official WASM target?** No dedicated build, but Pydantic invests directly in Pyodide: **pydantic.run** is an official *"Python browser sandbox… built to demonstrate Pydantic, PydanticAI, and Pydantic Logfire"* — https://github.com/pydantic/pydantic.run .

**Real evidence:**

- **`pydantic-core` (the Rust core of Pydantic v2) now ships official Emscripten/Pyodide wheels.** PEP 783 (Emscripten packaging) was accepted and *"Pydantic v2.14.0a1 was released with a PyEmscripten wheel"* — https://pydantic.dev/articles/emscripten-wheels-pydantic , https://github.com/pydantic/pydantic-core/issues/1506 . This removes the historical "no pydantic-core wheel in Pyodide" blocker, so PydanticAI's data model layer runs in wasm.
- PydanticAI's model layer talks to OpenAI via an `AsyncOpenAI` client (httpx) — https://ai.pydantic.dev/models/openai/ . Same as LangChain, a real outbound call requires the httpx→`fetch` shim (`pyodide-http`/`pyodide-httpx`).

**The catch:** No public source shows PydanticAI making a *real remote LLM call* end-to-end from wasm (the sandbox demos the framework; the network step still needs the pyodide-http patch and CORS/proxy handling). Verdict is by architecture-analogy to the LangChain result, not a standalone proof.

**Path that works:** Pyodide + PydanticAI + official pydantic-core wheel + `pyodide-http`/`pyodide-httpx` for the httpx client. Best-supported Python option because Pydantic itself maintains the Pyodide path.

**Verdict: EXPERIMENTAL-PARTIAL.**

---

## 5. LlamaIndex (Python) — **INFEASIBLE (thin evidence)**

**Official WASM target?** None. **No LlamaIndex-in-Pyodide/WASM demo or guide was found** in any search.

**Evidence:** Only generic "Python runs in Pyodide" material surfaced — nothing LlamaIndex-specific. LlamaIndex is a large framework with an extensive dependency surface (many optional integrations, native/compiled deps such as tokenizers and vector-store clients) that is far more likely to hit missing-Emscripten-wheel walls in Pyodide than a slim client. It shares the same httpx-outbound problem as Sections 3–4, but unlike those it has **zero** demonstrated wasm run.

**Per the skeptical rule (thin evidence → INFEASIBLE, not optimistic guessing):**

**Verdict: INFEASIBLE today** (no demonstrated path; would be speculation).

---

## 6. Vercel AI SDK (`ai` + `@ai-sdk/openai`, TypeScript/JS) — **EXPERIMENTAL-PARTIAL (closest to feasible)**

**Official WASM target?** No Vercel-published "AI SDK on wasm" guide, but the JS→WASM component toolchain is mature and outbound-capable: **ComponentizeJS/jco** (`bytecodealliance/ComponentizeJS`) compiles JS/TS to a WASI component, and **Spin's JS SDK is built on ComponentizeJS**, exposing the global **`fetch` backed by wasi-http** for outbound requests — https://github.com/bytecodealliance/ComponentizeJS , https://spinframework.dev/v3/javascript-components , https://spinframework.dev/v2/http-outbound (guarded by `allowed_outbound_hosts` in `spin.toml`).

**Real evidence (the strongest datapoint in this whole report):**

- Fermyon/Akamai published a working build of the **OpenAI Agents SDK for TypeScript** (`@openai/agents`) **compiled to a wasm component and making real `api.openai.com` calls from inside wasm.** The steps are explicit: `spin new -t http-ts` → `npm install @openai/agents` → `npx webpack` → `j2w -i build/bundle.js -o dist/support-agent.wasm`, with `allowed_outbound_hosts = ["https://api.openai.com"]` and live agent runs against OpenAI. — https://www.akamai.com/blog/developers/agents-with-openai-agents-sdk (formerly fermyon.com/blog/agents-with-openai-agents-sdk). This is a **real** outbound LLM call from a JS AI-agent framework inside a wasm component — not a mock.

**Why Vercel AI SDK inherits this:** The `ai` SDK is built on web-standard primitives — `fetch`, `ReadableStream`, `TextEncoder` — the same surface `@openai/agents` uses. The verified `@openai/agents` result is a direct architectural analog, so the AI SDK is very likely buildable the same way (webpack/esbuild bundle → ComponentizeJS/jco → Spin/wasmtime wasi-http). **Not yet verified for the exact `ai` package** (no `streamText`-in-Spin demo found), and bundling risk exists if any transitive dep reaches for a Node-only built-in.

**Path that works:** Bundle `ai` + `@ai-sdk/openai` → ComponentizeJS/jco → run on Spin or wasmtime with wasi-http; outbound `fetch` to the LLM endpoint (and to Qdrant's REST API) works; Qdrant **gRPC** would not (wasi-http is HTTP, not arbitrary sockets).

**Verdict: EXPERIMENTAL-PARTIAL, and the single most feasible candidate.**

---

## BOTTOM LINE — what to actually do for the arena

**Which could realistically be added as a WASM execution form that makes a real remote LLM call:**

- **Best candidate — Vercel AI SDK (JS/TS)** via **ComponentizeJS/jco + Spin/wasmtime (wasi-http)**. A near-identical TS agent SDK is *proven* calling OpenAI from inside a wasm component. This is the one worth attempting first.
- **Possible but fragile — Pydantic-AI and LangChain/LangGraph (Python)** via **Pyodide** + an httpx→`fetch` shim (`pyodide-http`/`pyodide-httpx`). Real outbound calls are demonstrated for LangChain in-browser; in a Node/server arena runner they require an XHR polyfill and CORS/proxy handling, plus heavy per-run startup. Pydantic-AI is the better-supported of the two (official pydantic-core Emscripten wheels + pydantic.run).

**Which cannot (stay native + Docker):**

- **Eino and Genkit Go** — effectively **infeasible today**. Standard Go `wasip1` cannot open outbound sockets (except a WasmEdge-only `dispatchrun/net` hack that nobody has shown working for these frameworks and that wazero/wasmtime don't support), and TinyGo's wasi-http path cannot compile reflection/`encoding-json`-heavy frameworks. No demos exist.
- **LlamaIndex** — **infeasible on thin evidence**; no demonstrated wasm run at all.

**Recommendation:**

> **Do not add a uniform WASM form across all six frameworks** — the arena must make a real remote LLM call *for every* entrant, and no single WASM runtime satisfies all six. Two of six (the Go frameworks) are hard-blocked, and a third (LlamaIndex) is undemonstrated.
>
> If a WASM form is wanted at all, **attempt it only for the Vercel AI SDK** (ComponentizeJS/Spin, wasi-http) as an *experimental, per-framework* execution form — that's the only path with a proven real-outbound-call analog. Optionally add a **second experimental Python form (Pydantic-AI or LangChain via Pyodide-in-Node)** if you accept the httpx-shim/XHR-polyfill fragility and slower cold start.
>
> For a **fair, apples-to-apples cross-framework arena**, the pragmatic call is **stay native + Docker only**, and treat WASM as an optional experimental badge on the 1–2 frameworks (JS AI SDK, maybe Pyodide Python) where a real outbound call is genuinely achievable. **Do not attempt WASM for Eino or Genkit Go.**

**Assumption reconciliation:** The starting hypothesis — "networked agent frameworks cannot run in WASM because wasip1/wazero have no outbound sockets, and Python/JS don't compile meaningfully" — is **confirmed for the Go/wazero half** and **overturned for the Python/JS half**: Python (Pyodide `fetch`) and JS (ComponentizeJS + wasi-http) *do* run their frameworks and *can* make real outbound LLM calls — just not on wazero/wasip1, and not without runtime-specific HTTP plumbing.

---

## Sources (all fetched/searched during this research)

- Go WASI networking limits (official): https://go.dev/blog/wasi
- Go 1.24 Wasm expansion (Google Cloud): https://cloud.google.com/blog/products/application-development/go-1-24-expands-support-for-wasm
- wazero socket request (open): https://github.com/tetratelabs/wazero/issues/2187
- wazero wasi-http community shim: https://github.com/brendandburns/wazero-http
- Outbound-HTTP-from-WASM survey: https://github.com/vasilev/HTTP-request-from-inside-WASM
- dispatchrun/net (ex-stealthrocket/net), WasmEdge-only sockets: https://github.com/dispatchrun/net
- TinyGo reflect/encoding-json limits: https://tinygo.org/docs/guides/compatibility/ , https://github.com/tinygo-org/tinygo/issues/2660
- TinyGo wasi-http RoundTripper (wasmCloud guide): https://wasmcloud.com/docs/wash/developer-guide/language-support/go/
- Eino repo/docs: https://github.com/cloudwego/eino , https://www.cloudwego.io/docs/eino/
- LangChain-in-Pyodide real fetch() LLM calls: https://dev.to/ozzoo/how-i-built-a-browser-native-ai-agent-platform-with-pyodide-no-backend-required-1j5d
- LangChain Sandbox (Pyodide, for tool-code sandboxing — not framework hosting): https://github.com/langchain-ai/langchain-sandbox
- openai-python fails in Pyodide (httpx sockets): https://github.com/pyodide/pyodide/issues/4292
- pyodide-http (httpx/requests → fetch/XHR patch; Node needs XHR polyfill): https://github.com/koenvo/pyodide-http , https://pyodide.org/en/stable/usage/api/python-api/http.html
- pydantic.run official Pyodide sandbox (demos PydanticAI): https://github.com/pydantic/pydantic.run
- pydantic-core Emscripten wheels / PEP 783: https://pydantic.dev/articles/emscripten-wheels-pydantic , https://github.com/pydantic/pydantic-core/issues/1506
- PydanticAI OpenAI model (AsyncOpenAI/httpx): https://ai.pydantic.dev/models/openai/
- Spin JS components + ComponentizeJS + wasi-http fetch: https://spinframework.dev/v3/javascript-components , https://spinframework.dev/v2/http-outbound
- ComponentizeJS: https://github.com/bytecodealliance/ComponentizeJS
- **Proven** TS OpenAI Agents SDK → wasm component → real api.openai.com calls: https://www.akamai.com/blog/developers/agents-with-openai-agents-sdk
- Vercel AI SDK (`ai`, `@ai-sdk/openai`, fetch/streamText): https://github.com/vercel/ai , https://ai-sdk.dev/docs/introduction
