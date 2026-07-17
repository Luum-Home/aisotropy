# "OpenAI-compatible" is a spectrum — where agnes (and gateways like it) break

**Date:** 2026-07-17
**Origin:** the live arena (`arenas/live/`). Building the Vercel AI SDK adapter against the
`agnes-2.0-flash` OpenAI-compatible endpoint (`https://apihub.agnes-ai.com/v1`) produced a
reproducible **HTTP 500 on the tool-loop follow-up**. Root-causing it exposed a general truth about
what "OpenAI-compatible" actually guarantees.

## TL;DR

- agnes is **OpenAI-compatible on the classic Chat Completions API** (`/v1/chat/completions`) — the
  de-facto standard every gateway implements. It is **not** compatible with OpenAI's newer
  **Responses API** (`/v1/responses`).
- The arena bug was **not agnes misbehaving** — it was **`@ai-sdk/openai` defaulting to the Responses
  API** since AI SDK v5. The fix (`openai.chat(model)`) is exactly what the AI SDK docs prescribe for
  OpenAI-compatible endpoints.
- A **second, separate** gap: agnes's **function-calling-based structured output** is unreliable;
  the robust path is text-completion + a parser.
- aisotropy's own `internal/llm/openai_compat.go` never hit either problem because it hand-rolls
  **Chat Completions only** and does not rely on strict function-calling structured output.

## What actually happened

agnes is a **gateway** (`apihub.agnes-ai.com`) in front of vLLM — confirmed by the response
`system_fingerprint: vllm-0.21.0-tp2-...`. Its compatibility surface is Chat Completions.

`@ai-sdk/openai`'s `openai(modelId)` shorthand **defaults to the Responses API** since AI SDK 5.
The Responses API is OpenAI's newer, stateful API with **different wire shapes for tool interactions**
(`function_call` / `function_call_output` items, rather than the classic `role:"tool"` messages). The
first turn can appear to work, but the **tool-loop follow-up** — where the tool result is sent back —
serialises those items in the Responses shape, and agnes's gateway returns **500** because it does not
implement `/v1/responses`.

This is a **known gateway pattern**, not an agnes-specific defect: the LiteLLM proxy returns **500 on
`/v1/responses` even when the vLLM backend returns 200** ([LiteLLM #14061](https://github.com/BerriAI/litellm/issues/14061)),
and OpenAI-compatible proxies broadly break under the Responses API through a proxy
([OpenCode/Responses proxy bug #353](https://github.com/yvgude/lean-ctx/issues/353)).

## The fix (officially documented)

Force classic Chat Completions:

```ts
// WRONG for an OpenAI-compatible (non-OpenAI) endpoint: defaults to /v1/responses
const model = openai(process.env.OPENAI_MODEL)
// RIGHT: /v1/chat/completions
const model = openai.chat(process.env.OPENAI_MODEL)
```

The AI SDK docs say it verbatim: *"Since AI SDK 5, the OpenAI responses API is called by default
(unless you specify e.g. 'openai.chat')"* and *"Use this factory [`openai.chat`] when a custom
`baseURL` points at an OpenAI-compatible endpoint that implements Chat Completions but not the
Responses API."* ([AI SDK OpenAI provider docs](https://ai-sdk.dev/providers/ai-sdk-providers/openai))

## The second gap: function-calling structured output

Independently, two arena adapters found agnes's **structured-output-via-function-calling** unreliable
against `agnes-2.0-flash`:

- **LlamaIndex** — `llm.structured_predict()` (function-calling path) failed twice, even with
  `tool_choice="required"`; resolved with `LLMTextCompletionProgram` + `PydanticOutputParser`.
- **Pydantic-AI** — `output_type=Model` reliably raised `UnexpectedModelBehavior`; resolved with
  `PromptedOutput` (text + parse).

Function-calling structured output and `response_format: json_schema` are **not** part of the stable
compatibility contract; support and strictness vary by backend.

## The general lesson

**"OpenAI-compatible" is a spectrum, not a binary:**

| Tier | Feature | Portability across OpenAI-compatible endpoints |
|---|---|---|
| **Stable contract** | Chat Completions: `messages`, `tools` (classic `role:"tool"`), streaming, `usage` | High — assume it works |
| **Varies by backend** | Responses API (`/v1/responses`) | **Low** — rare in gateways/proxies |
| **Varies by backend** | Strict function-calling structured output, `response_format: json_schema`, `tool_choice:"required"` | Medium/Low — test it |
| **Varies by backend** | logprobs, seed determinism, some sampling params | Low |

**Practical rules for targeting agnes and gateways like it:**
1. Force **Chat Completions** (`openai.chat`, `api_base` on the classic route), never the Responses API.
2. For structured output, prefer **text + parser** over function-calling / `json_schema` strict mode.
3. Reference implementation that stays in the safe lane: aisotropy's `internal/llm/openai_compat.go`
   (raw `net/http` + JSON, Chat Completions only — the same anti-magic discipline that dodged both
   gaps here).

## Sources

- [AI SDK — OpenAI provider (default = Responses API; use `.chat` for compatible endpoints)](https://ai-sdk.dev/providers/ai-sdk-providers/openai)
- [LiteLLM #14061 — proxy 500 on `/v1/responses` while vLLM returns 200](https://github.com/BerriAI/litellm/issues/14061)
- [OpenCode + OpenAI Responses API proxy bug #353](https://github.com/yvgude/lean-ctx/issues/353)
- [vLLM — OpenAI-compatible server (supports both Chat Completions and the newer Responses API)](https://docs.vllm.ai/en/latest/serving/online_serving/openai_compatible_server/)
- [LiteLLM — `/responses` bridging to Chat Completions](https://docs.litellm.ai/docs/response_api)
- [Vercel AI Gateway — Responses API support (a recent addition, not a baseline)](https://vercel.com/changelog/ai-gateway-supports-openais-responses-api)

Surfaced by: `arenas/live/ai-sdk/` (the 500), `arenas/live/llamaindex/` and `arenas/live/pydantic-ai/`
(the structured-output gap). Memory: `discovery/agnes-openai-compat-boundary`.
