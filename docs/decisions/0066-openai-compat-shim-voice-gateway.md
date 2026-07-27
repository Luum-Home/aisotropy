# ADR-0066: OpenAI-compat shim (`POST /v1/chat/completions`) — the voice-gateway enabler

**Status:** Accepted (2026-07-23 — operator approved implementation of both workstreams; implementation landed and passed the SDD verify phase the same day: `internal/api/openai_compat.go` + the principal-derived mode in `internal/api/authn.go` + `examples/voice-pipecat/`. Verify reran all gates fresh — build/vet/gofmt clean, the shim + adversarial probe suites green — and confirmed the must-agree path byte-identical for every existing route)
**Delivers:** an OpenAI Chat-Completions-compatible route over the EXISTING streaming pipeline, so any
OpenAI-client ecosystem — voice orchestrators (Pipecat) and self-hosted chat UIs (Open WebUI/LibreChat)
— can point a `base_url` at aisotropy with zero client modification. Zero audio code in the core.
**Related:** ADR-0024 (authenticated-identity seam — this ADR adds its principal-derived variant),
ADR-0042 (NoAuth explicit opt-in), the voice landscape research
(`docs/research/voice-pipeline-landscape-2026-07-23.md`, incl. the license table) and the compat
boundary (`docs/research/openai-compat-boundary-2026-07-17.md`).

## Context

aisotropy has zero voice support (L0, grep-verified in the landscape research). The industry "voice
sandwich" (STT → LLM → TTS) needs only an OpenAI-compatible LLM backend: Pipecat's `OpenAILLMService`
takes a `base_url` + `api_key` and does the rest. aisotropy is already voice-shaped — token SSE
(incremental TTS feed), ctx cancellation (= barge-in, a constitutional gate), an importable Go runtime
— but it does not speak the OpenAI wire protocol as a SERVER (it speaks it as a CLIENT:
`internal/llm/openai_compat.go`).

Two precedents already re-encode the streaming pipeline as foreign wire protocols:
`internal/api/aisdk.go` (Vercel AI SDK UI Message Stream) and `internal/api/agui.go` (AG-UI/CopilotKit).
Both are "protocol costumes" over `StreamCore` (`internal/api/stream_core.go:130`) — same pipeline, same
`StreamDeps` (`stream_core.go:41`), only the output encoding differs, so isolation/quota/guardrails/cost
hold by construction. This ADR is the third costume, with one genuinely new design problem: **the OpenAI
path has no tenant/user path params**, so the ADR-0024 must-agree bind cannot apply as-is.

### The Pipecat contract, verified against source (not docs)

Fetched from `pipecat-ai/pipecat` `src/pipecat/services/openai/base_llm.py` (+ `llm.py`, a thin
subclass) on 2026-07-23:

- Client is the official `AsyncOpenAI(api_key=…, base_url=…)` — the `api_key` rides as
  `Authorization: Bearer <key>`, so **a tenant JWT can be passed AS the api_key** with no client change.
- Requests always send `"stream": True` and `"stream_options": {"include_usage": True}`, plus
  sampling params and `messages`/`tools`/`tool_choice`; `settings.extra` is merged into the request —
  an extension-field channel (e.g. a `session_id`) a PoC can use without forking Pipecat.
- It reads `chunk.choices[0].delta.content`, `delta.tool_calls`, and `chunk.usage` — usage is
  **optional** (`if chunk.usage:`; no error when absent).
- `[DONE]`/termination is handled by the OpenAI SDK, which pydantic-validates each chunk: the shim's
  chunks MUST carry `id`, `object: "chat.completion.chunk"`, `created`, `model`, `choices`.

## Decision

### 1. Route contract: OUTSIDE the tenant/user prefix, JWT mandatory (Option A)

Mount `POST /v1/chat/completions` — NOT `/v1/tenant/{t}/user/{u}/chat/completions`. Identity comes
exclusively from verified JWT claims; the route refuses to serve without a real authenticator.

| | Option A — outside prefix, JWT-only | Option B — under tenant/user prefix |
|---|---|---|
| `base_url` shape | `https://host/v1` — exactly OpenAI's; SDK appends `/chat/completions` | `https://host/v1/tenant/acme/user/alice` — works mechanically, but bakes ONE end-user into a deployment-static config |
| Multi-user clients | one deployment config serves all users (identity = per-request credential) | Open WebUI/LibreChat have a single global `base_url` — per-user URLs are unusable |
| Identity source | verified claims only (the credential IS the identity — OpenAI's own model) | duplicated: URL AND JWT, must-agree redundancy |
| NoAuth mode | structurally impossible (no path to copy identity from) → fail closed | NoAuth would trust bare path identity on a route whose clients always send a credential |

**Chosen: Option A** (matches the operator's leaning). Constitutional analysis: the gate requires
`tenant_id` via a validated boundary → ctx, never body. Option A derives it from VERIFIED claims at the
same middleware boundary ADR-0024 owns — the same L2 (enforced-at-a-boundary) rung with fewer moving
parts, and *stronger* in one respect: there is no unverified path assertion to agree with at all.

### 2. The principal-derived identity seam (the new piece)

Today `BindAuthMiddleware` (`internal/api/authn.go:52`) is ONE API-wide Huma middleware doing a
must-agree bind: `principal.TenantID != req.PathTenant() || principal.UserID != req.PathUser()` → 403
(`authn.go:66`), where `PathTenant()/PathUser()` read the operation's path params (`authn.go:42-43`).
On a path-param-less route those return `""`, so every real principal mismatches → the middleware
already fails closed on this route — correct, but unusable. The variant:

- **Opt-in per operation, never by accident:** the shim's `huma.Operation` carries
  `Metadata["identity"] = "principal"`. `BindAuthMiddleware` branches on that metadata:
  absent → must-agree bind, byte-identical for every existing route; present → principal-derived mode.
- **Principal-derived mode:** authenticate as today, then require
  `p.TenantID != "" && p.UserID != "" && p.ScopesVerified` — else 401. `NoAuth().Authenticate` returns
  the PATH identity with `ScopesVerified: false` (`internal/auth/auth.go:99-101`); with no path params
  that principal is empty → 401 **by construction**. So "JWT mandatory even when the rest of the server
  runs NoAuth" is structural, not a config check.
- **No authenticator configured:** `DenyAllMiddleware` (`authn.go:80`, installed at
  `aisotropy/app.go:1154`) already 401s everything — unchanged. There is NO fallback path to NoAuth.
- **ctx injection reused verbatim:** `withVerifiedPrincipal` (`authn.go:94-101`) already sets the
  tenant/user ctx carriers + `internal/llm` carriers + the principal. The handler builds
  `ChatInput{TenantID: p.TenantID, UserID: p.UserID, …}` from `PrincipalFromContext` (`authn.go:117`)
  instead of path params; `StreamCore`'s `withIdentity` (`stream_core.go:131`) still charset-validates,
  so hostile claim strings are rejected, not trusted.
- **Verified scopes → tool gating:** `effectiveScopes` (`authn.go:133`) already prefers
  `p.Scopes` when `ScopesVerified`; the OpenAI body has no scopes field (nil bodyScopes), and since the
  route REQUIRES a verified principal, the body-fallback branch is unreachable here.

Rejected alternative — a chi route outside Huma with its own auth (the MCP-serve precedent,
`app.go` `a.mcpServe`): loses `huma.StreamResponse`, duplicates auth error mapping, and diverges from
the aisdk/agui registration precedent (both register via `huma.Register` inside the contract).
Rejected alternative — per-operation Huma middleware only: API-wide middleware runs FIRST, so
`BindAuthMiddleware` would 403 before a per-route variant could act; the branch must live in the
API-wide middleware.

### 3. The shim: third wire costume over `StreamCore`

New file `internal/api/openai_compat.go` (`RegisterOpenAICompat`), mirroring `aisdk.go`/`agui.go`:

- **Registration:** `huma.Register` with `SkipValidateBody` + `RawBody []byte` (the agui pattern,
  `agui.go:176,217`) — OpenAI `messages[].content` is polymorphic (string | typed parts) and is decoded
  leniently in-handler; unknown params (temperature, top_p, …) are accepted and ignored in v1. The
  route is additive in the OpenAPI doc (no existing struct/tag changes — backward-compat gate holds).
- **Request → ChatInput:** latest user message's text = prompt (reuse/factor the string-or-parts
  normalizer `aguiMessageText`, `agui.go:389`); incoming `system` messages are IGNORED in v1 (the
  persona + memory hydration own the system prompt — documented); session key = optional `session_id`
  extension field (stock clients unaffected; Pipecat can send it via `settings.extra`) else a stable
  `"openai"` default (the `aguiDefaultThread` pattern, `agui.go:78`).
- **`stream:true` encode:** SSE frames `data: {chunk}\n\n` with
  `{id, object:"chat.completion.chunk", created, model, choices:[{index:0, delta:{content:…},
  finish_reason:null}]}`; terminal chunk with `finish_reason:"stop"`; when the client sent
  `stream_options.include_usage` and `StreamFinal.Usage` (`stream_core.go:65-78`) is non-nil, a
  penultimate usage-only chunk (empty `choices`) — the exact shape our OWN client reads
  (`internal/llm/openai_compat.go` `applyStreamChunk`); then `data: [DONE]`. Nil usage → omitted
  (Pipecat tolerates, verified).
- **`stream:false` encode:** drive the SAME `StreamCore` with a buffering sink, emit one
  `chat.completion` JSON object. (Not `ChatCore`: one pipeline, one costume rule.)
- **Errors:** pre-stream failures → OpenAI error envelope `{"error":{"message","type","code"}}` with
  a real HTTP status (the decode-before-stream discipline, `aisdk.go:334-345`); mid-stream failures →
  a `data: {"error":{…}}` frame then close WITHOUT `[DONE]` (the OpenRouter precedent our own client
  ecosystem handles).
- **`model` field semantics:** echoed back (request's `model`, else `"aisotropy"`), **advisory only**
  — it never selects the model. Model selection stays with the gateway + quota-pressure degrade
  (`stream_core.go:234-249`); a body-controlled model pick would bypass cost/routing policy.
- **Gates held by construction:** quota-before-spend (`stream_core.go:155`), input/output guardrails,
  tenant-scoped hydration, cost accounting, and ctx-cancellation (`stream_core.go:269` — this IS
  barge-in) are all inside `StreamCore`; the shim cannot drift from them.

### 4. Out-of-core Pipecat PoC (`examples/voice-pipecat/`)

Python, following the `examples/clients/python` precedent (own `requirements.txt`, no `go.mod`, never
in the Go module): Pipecat (BSD-2) + faster-whisper STT (MIT) + Kokoro-FastAPI TTS (Apache-2.0) +
`OpenAILLMService(api_key=<tenant JWT>, base_url=<shim>)`. Ships TWO executable metrics (evidencia
ejecutable — no latency claims without a command): `measure_ttfab.py` (time-to-first-audio-byte) and
`measure_bargein.py` (barge-in-to-silence, exercising ctx cancellation end-to-end).

## Reuse of dependency primitives

Checked before designing (dependency-reuse-first):

- **Huma:** `huma.StreamResponse` (used at `aisdk.go:360`, `agui.go:269`) — reused. `SkipValidateBody`
  + `RawBody` (`agui.go:176,217`) — reused. Huma's `sse` package (`sse.Register`,
  `chat_stream.go:223`) — NOT reused: it emits named typed events; OpenAI chunks are unnamed
  data-only frames, so the shim hand-rolls its writer exactly as `aisdk.go` `part()` and
  `agui.go` `writeAGUIFrame` already do (precedent, not invention).
- **In-repo protocol knowledge:** `internal/llm/openai_compat.go` is the CLIENT of this same protocol
  (`newStreamRequest`, `applyStreamChunk`) — its struct shapes are the server encoder's contract
  reference, and a round-trip test (in-repo client consuming the in-repo shim) falls out for free.
- **authn seam:** `withVerifiedPrincipal` / `PrincipalFromContext` / `effectiveScopes`
  (`authn.go:94/117/133`) — reused verbatim.
- **gollem:** not implicated — this is output encoding, not agent-loop/middleware/orchestration work
  (`core/` surface check per the rule: nothing there encodes OpenAI server-side wire formats).

## Consequences

- One shim unlocks two ecosystems (voice orchestrators + self-hosted chat UIs) with zero audio code
  and zero new dependencies in the core.
- `BindAuthMiddleware` gains a second, opt-in mode — a small, auditable diff on the single
  fail-closed gate; every existing route's behavior is byte-identical.
- The OpenAPI doc advertises an OpenAI-shaped route (additive); documented as a compat surface.
- Adversarial tests become merge blockers: unauthenticated → 401; NoAuth-configured server → this
  route STILL 401; body-supplied tenant/model ignored; JWT tenant is the one that reaches memory keys.

## Non-goals (explicit, each with its boundary rationale)

- **No Responses API** (`/v1/responses`) — Chat Completions ONLY, per
  `docs/research/openai-compat-boundary-2026-07-17.md`: the compat boundary is the contract line.
- **No tool calls** (`delta.tool_calls`), no logprobs, no `n>1`, no vision/audio content parts —
  text-only v1 floor; Pipecat reads `tool_calls` but does not require it.
- **No `GET /v1/models`** in v1 — Open WebUI convenience, named follow-on, not part of the floor.
- **No telephony/Asterisk** — GPLv2 requires its own license-exception ADR (or a CPaaS); deferred.
- **No F5-TTS official weights** — CC-BY-NC, BARRED for SaaS (Apache alt exists: OpenF5-TTS-Base).
  Full license table: `docs/research/voice-pipeline-landscape-2026-07-23.md`.

### Real-provider verification — dismissed, then REVERSED the same day

**First recorded as dismissed (2026-07-27), on a false premise.** The dismissal's leading argument
was that "the gap is a **credential**, not a capability". That was simply wrong, and the repo
contained the disproof the whole time: the live arena makes real calls through
`OPENAI_API_KEY` (`arenas/live/runner/eval.go:241`) and its published board reports **1094 real
calls** against `agnes-2.0-flash`. The credential exists, works, and is used routinely. Worse, the
provider it points at is the *most* informative one available for this test —
`docs/research/openai-compat-boundary-2026-07-17.md` exists precisely because agnes's
OpenAI-compat gateway surprised us before.

The other two arguments still hold and are kept: the wire contract is pinned from **both ends**
(the golden test round-trips through this repo's own Chat Completions *client*,
`internal/llm/openai_compat.go` — the same code that talks to real providers in production), and a
single un-repeated run verifies one provider, one model, one day, while the deterministic tests
verify every run. But those support "the risk is low", not "the check is impossible" — which is
what the dismissal actually implied.

**Reversed by operator decision, and the check was run.**

### Real-provider conformance — RUN 2026-07-27, both paths PASS

Provider: `agnes-2.0-flash` via `https://apihub.agnes-ai.com/v1` (the credential already in
`.env`, the same one the arena uses). The probe asks only what the stub cannot answer: do `usage`
and `finish_reason` arrive from a REAL provider in the shapes the shim expects?

| Path | Result | Real values observed |
|---|---|---|
| **Non-streaming** | **PASS** | `object: "chat.completion"`, `finish_reason: "stop"`, `usage {prompt_tokens: 289, completion_tokens: 1, total_tokens: 290}` |
| **Streaming** (`stream_options.include_usage: true`) | **PASS** | 4 frames, terminator `[DONE]`, `finish_reason: "stop"`, usage chunk `{423, 2, 425}` |

`Server-Timing: pipeline;dur=591.4` was emitted on the non-streaming response — the header
adopted from the voicebox spike, confirmed against a real upstream rather than the stub's
microsecond path.

**No divergence found.** Every shape the stub emits is the shape agnes returns; the usage numbers
are real token counts, not the stub's canned values. This is the one thing the deterministic
suite structurally cannot prove, and it is now proven — once, against one provider, on one day.
That caveat is the honest scope of the result and the reason the deterministic tests remain the
regression guard.

**Method note (kept because it nearly produced a false negative):** the first probe run reported
the non-streaming path as failing. The failure was a quoting bug in the probe's own Python, not
in the shim — escaped quotes inside an f-string in a `-c` heredoc. A probe that fails must be
proven guilty before the system under test is: rewriting the check to a file made it pass on the
first attempt with no change to aisotropy.

## Implementation plan (S, additive — pending operator approval)

1. `internal/api/authn.go` — principal-derived mode behind operation metadata (small diff).
2. `internal/api/openai_compat.go` (+ `_test.go`) — the shim + golden wire tests + the adversarial
   identity tests above.
3. `aisotropy/app.go` — one registration line.
4. `examples/voice-pipecat/` — the PoC + the two metric scripts.
Rollback: delete the shim file + registration line; delete the example dir. No migrations.
