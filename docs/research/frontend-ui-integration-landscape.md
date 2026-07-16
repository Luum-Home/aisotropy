---
type: Research Report
title: "Frontend / agent-UI integration landscape — what aisotropy must emit to plug into CopilotKit, Vercel AI SDK & friends (mid-2026)"
description: Code-grounded scan of the agent-UI protocol landscape (AG-UI/CopilotKit, the Vercel AI SDK "UI Message Stream", assistant-ui, A2A, plus the wider field) assessing exactly what adapter aisotropy — a Go service that already streams SSE — must expose to power in-app copilots. Conclusion, ranked by coverage/effort: emit the AI SDK UI Message Stream (covers Vercel AI SDK + assistant-ui at once) and the AG-UI protocol (CopilotKit); both are output-encoding adapters over the existing stream, not a rewrite.
tags: [research, integration, frontend, copilotkit, ag-ui, vercel-ai-sdk, assistant-ui, a2a, mcp, adapters, adoption]
timestamp: 2026-07-16T00:00:00Z
---

# Frontend / agent-UI integration landscape

> **Purpose.** aisotropy runs as an HTTP service that already streams chat over SSE
> (`internal/api/chat_stream.go`). To let developers build *in-app copilots* against it, the
> service must speak the protocols those copilot frameworks expect. This report answers, grounded in
> the actual framework repos (cloned mid-2026-07): **which protocol(s) must aisotropy emit, and at
> what cost?**
>
> **Method.** Read-only review of the cloned source of CopilotKit, ag-ui, vercel/ai, and
> assistant-ui, plus web docs for the wider field. Every wire-format claim is traced to a repo file
> or a cited spec URL (see [References](#references)). Adoption figures are self-reported /
> directional, not independently audited — treat them as such.

## TL;DR — the ranked answer

aisotropy is **one adapter away**, not a rewrite away: it already streams SSE; these integrations
are output-encoding + request-decoding adapters, no new infrastructure. The only structural
difference is that aisotropy emits **named** SSE events (`event: token/done/error`) while the target
protocols emit a **single unnamed event whose JSON carries a `type` field**.

| # | Protocol to emit | Unlocks | Effort |
|---|---|---|---|
| **1** | **AI SDK "UI Message Stream"** (`x-vercel-ai-ui-message-stream: v1`, SSE, terminates with `data: [DONE]`) | **Vercel AI SDK `useChat` AND assistant-ui at once** (assistant-ui consumes the identical wire format). Largest install base. | **S — ~1–2 days** (text), +1–2 days for full tool-call/data-part parity |
| **2** | **AG-UI** (event-based SSE, ~16 event types) | **CopilotKit** natively, plus generative UI, shared state, human-in-the-loop, tool lifecycle. A **community Go SDK ships in-repo**, so this is days, not weeks. | **M — days** |
| 3 | **A2A** (Agent Card + JSON-RPC 2.0) | agent↔agent interop — a *different layer*, not copilot UI | S–M, low priority for copilots |
| — | **MCP** | tool/context layer | **already supported** — orthogonal, do not re-solve |

Optional cheap add: an **OpenAI-compatible `/chat/completions` shim** would make aisotropy a drop-in
backend for **Open WebUI** and **LibreChat** (self-hosted ChatGPT-style UIs) for near-zero
protocol work — a "#1.5" if self-hosted chat UIs are a target audience.

## What each thing is, and the exact contract

### AG-UI + CopilotKit

- **CopilotKit** (`github.com/CopilotKit/CopilotKit`) is the React/TS *frontend + gateway* half:
  UI components (`CopilotChat`/`CopilotSidebar`/`CopilotPopup` in `packages/react-ui`), hooks
  (`useCopilotAction`, `useCoAgent`, `useCopilotReadable` in `packages/react-core`), and a Node
  `CopilotRuntime` (`packages/runtime`). It is **built on AG-UI** — every runtime package depends on
  `@ag-ui/client`/`@ag-ui/core`/`@ag-ui/encoder`. Targeting AG-UI ⇒ consumable by CopilotKit *and*
  every other AG-UI client.
- **AG-UI** (`github.com/ag-ui-protocol/ag-ui`) is the open, event-based agent↔frontend protocol.
  - **Transport:** one `POST` to the agent URL, `Content-Type: application/json`,
    `Accept: text/event-stream`; body is a `RunAgentInput` JSON `{threadId, runId, state, messages,
    tools, context, forwardedProps, resume}` (messages are OpenAI-shaped). Response is SSE, one
    `data: {json}\n\n` per event. Protobuf is an optional alternative negotiated by content-type;
    **SSE JSON is the default**.
  - **~16 event types** (`RUN_STARTED/FINISHED/ERROR`, `STEP_*`, `TEXT_MESSAGE_START/CONTENT/END`,
    `TOOL_CALL_START/ARGS/END/RESULT`, `STATE_SNAPSHOT/DELTA` (JSON-Patch RFC 6902),
    `MESSAGES_SNAPSHOT`, `REASONING_*`, `ACTIVITY_*`, `RAW`, `CUSTOM`).
  - **The floor is five events:** `RUN_STARTED → TEXT_MESSAGE_START → TEXT_MESSAGE_CONTENT →
    TEXT_MESSAGE_END → RUN_FINISHED`. Everything else (tools, state, HITL, generative UI) is additive.
  - **A Go SDK ships in-tree** at `sdks/community/go/` — an `sse.SSEWriter`, the full `events.*` type
    set, and a runnable Fiber HTTP agent example. A Go AG-UI adapter is *wiring aisotropy's token
    stream into that SDK*, not implementing the protocol from scratch.
  - Existing framework adapters live under `ag-ui/integrations/`: LangGraph, CrewAI, Mastra,
    Pydantic-AI, LlamaIndex, Agno, and more.

### Vercel AI SDK "UI Message Stream"

- `github.com/vercel/ai`. `useChat`/`useUIMessageStream` are transport-driven; a backend **in any
  language** can serve them by emitting the right bytes (the SDK docs bless non-JS backends
  explicitly).
- **Two protocols exist; target the right one.** The current (v5/v6/v7) protocol is the **UI Message
  Stream**: `Content-Type: text/event-stream`, header `x-vercel-ai-ui-message-stream: v1`, framing
  `data: ${JSON.stringify(part)}\n\n`, terminating with the literal `data: [DONE]`. Each part carries
  a `type` discriminator (`text-delta`, `tool-input-*`/`tool-output-*`, `data-*`, `finish`, `error`,
  `abort`, …). **Do NOT target the legacy v4 "data stream"** single-char-prefix format (`0:`/`9:`/`a:`/`d:`,
  header `x-vercel-ai-data-stream: v1`) — the in-repo Python example still emits it but it is stale.
- **De-facto standard: the strongest here** — default transport of `@ai-sdk/react`, ~20M+ monthly
  downloads (self-reported).

### assistant-ui

- `github.com/assistant-ui/assistant-ui` — React primitives (`Thread`/`Message`/`Composer`) wired to
  a pluggable **runtime**. It defines **no new wire protocol**: its flagship `useChatRuntime` is
  literally Vercel's `useChat` (via `AssistantChatTransport extends DefaultChatTransport`), and
  `useDataStreamRuntime` auto-detects and decodes the **same** Vercel UI Message Stream. **⇒ One
  AI-SDK-stream emitter serves both Vercel AI SDK and assistant-ui.** Marginal extra effort ≈ 0.

### A2A, MCP, and the rest

- **A2A (Agent2Agent)** — agent↔agent interop: an **Agent Card** (public JSON capabilities) + **JSON-RPC
  2.0 over HTTPS**, unit of work a **Task**. Trivially Go-friendly, but a *different layer* — not a
  copilot-UI protocol. Pursue only when cross-agent orchestration is a goal.
- **MCP** — the tool/context layer, **already supported in aisotropy**. Orthogonal to every UI
  protocol here; do not re-solve.
- **Open WebUI / LibreChat** — popular self-hosted ChatGPT-style UIs that connect to any
  **OpenAI-compatible** API. Reachable via an optional OpenAI-compat shim.
- **Do NOT reimplement:** LangServe (maintenance mode) / LangGraph Platform, Chainlit, Botpress,
  Rivet, Thesys/C1 — these are competitors or platforms, not protocols to target.

## Landscape classification

| Tool | What it is | Type | Go-friendly? |
|---|---|---|---|
| AG-UI | Event-based agent↔frontend protocol over SSE | **(i) UI protocol to emit** | Yes — community Go SDK in-repo |
| Vercel AI SDK | TS toolkit; defines the "UI Message Stream" wire format | **(i) UI protocol to emit** | Wire format is language-agnostic (no official Go lib) |
| assistant-ui | React components riding the Vercel protocol | **(i) — via the Vercel protocol** | Same emitter as Vercel |
| A2A | Agent↔agent interop (Agent Card + JSON-RPC) | **(iii) agent protocol to speak** | Yes (plain JSON-RPC) |
| MCP | Tools/resources/context for agents | **(iii) tool protocol** | **Already supported** |
| Open WebUI / LibreChat | Self-hosted chat UIs over OpenAI-compat APIs | (i)/(iv) via OpenAI-compat shim | Reachable with a shim |
| LangServe / LangGraph Platform, Chainlit, Botpress, Rivet, Thesys/C1 | serving backends / platforms / competitors | **(ii)/(iv) do not reimplement** | — |

## How aisotropy connects — what maps, what's missing

Today (`internal/api/chat_stream.go`): Huma-registered **named** SSE events — `event: token` /
`event: done` / `event: error`, payloads `StreamTokenEvent{Text}` / `StreamDoneEvent{TenantID}` /
`StreamErrorEvent{Message}`. A Vercel/AG-UI client will not parse those named events.

- **Maps directly:** the per-token channel → a `text-delta` (AI SDK) or `TEXT_MESSAGE_CONTENT`
  (AG-UI); stream end → `finish` + `[DONE]` / `RUN_FINISHED`; error → `error` chunk / `RUN_ERROR`;
  ctx-cancellation (already stops the upstream LLM) → `abort`. All SSE, all `http.Flusher`.
- **Must add (the adapter):** the `type`-discriminated (unnamed-event) JSON envelope; the exact
  response headers (`x-vercel-ai-ui-message-stream: v1`; AG-UI's `text/event-stream`); request
  decoding (Vercel `{messages: UIMessage[], trigger}` / AG-UI `RunAgentInput` instead of aisotropy's
  own `ChatInput`); tool-call framing (the multi-event choreography — optional for a text-only v1);
  message/block IDs and a finish reason / run lifecycle.

Crucially this is **an adapter over the output**: tenant isolation, key-brokering, and the memory
tiers stay intact underneath — the protocol is a costume on the stream, not a change to the runtime.

## Recommendation — how to "leave aisotropy prepared"

Two adapter endpoints over the existing stream (core untouched):

1. `POST .../chat/aisdk` → **AI SDK UI Message Stream** (covers Vercel AI SDK + assistant-ui). **~1–2
   days.** Highest coverage-per-effort — start here.
2. `POST .../chat/agui` → **AG-UI** events via the in-repo Go SDK (covers CopilotKit + generative UI /
   shared state / HITL). **Days** — a fast-follow.
3. *(optional)* OpenAI-compat `/chat/completions` shim → Open WebUI / LibreChat as drop-in UIs.
4. A2A (Agent Card + JSON-RPC) — later, only if agent↔agent interop becomes a goal.

## Honest caveats

- **The AG-UI Go SDK is "community" tier** (`sdks/community/go`, `go 1.24.4`), not the first-party
  TS/Python/.NET tier. It is real and complete (SSE writer, event types, working server example) but
  not vendor-guaranteed-stable — **pin the version** before hard-depending on it, or vendor the small
  surface we use.
- **AG-UI's endpoint/handshake/auth is not formally specified** in the fetched docs (no written wire
  spec for URL conventions, auth, reconnection). The "standard" is de-facto — defined by the SDK +
  example server. Expect to reverse-engineer edge cases from the reference implementation.
- **Adoption/de-facto-standard rankings are directional**, drawn from 2026 blog/aggregator sources
  and Vercel's own self-reported download numbers — not independently audited. The *engineering*
  facts (wire formats, event sets, the Go SDK's existence) are code-grounded; the *market* framing is
  softer.
- **This is a fast-moving space.** Newer payload/complement layers (Google's A2UI v0.9, MCP Apps)
  surfaced during the scan; too early to call standards, noted for completeness only.

## References

- AG-UI: <https://docs.ag-ui.com/introduction> · CopilotKit backend/AG-UI:
  <https://docs.copilotkit.ai/backend/ag-ui> · repos `github.com/CopilotKit/CopilotKit`,
  `github.com/ag-ui-protocol/ag-ui` (Go SDK: `sdks/community/go`).
- Vercel AI SDK stream protocol: <https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol> · repo
  `github.com/vercel/ai` (`packages/ai/src/ui-message-stream/`, `ui-message-chunks.ts`).
- assistant-ui custom data-stream runtime: <https://www.assistant-ui.com/docs/runtimes/custom/data-stream>
  · repo `github.com/assistant-ui/assistant-ui`.
- A2A: <https://a2a-protocol.org/latest/specification/>.
- aisotropy's current stream: `internal/api/chat_stream.go`. Related: [competitive
  landscape](competitive-landscape.md), [why Go — the wedge](../vision/why-go.md).
