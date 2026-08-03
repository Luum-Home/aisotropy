---
type: Research
title: "Archestra Security Architecture — Code-Level Review"
description: >
  Code-level review of Archestra's backend and Rust sandbox-execution security architecture.
tags: [research, archestra, security, sandbox, rust]
timestamp: 2026-07-17T00:00:00Z
---

# Archestra Security Architecture — Code-Level Review

Date: 2026-07-17
Scope: `platform/backend/` (TypeScript/Fastify), `platform/archestra-rs/` (Rust, sandbox execution),
`ai-labs/` (Rust, internal eval harness — not a security-relevant crate, see note below), `docs/pages/*`.
Method: read-only clean-room review of the archestra-review checkout, no code copied.

**License note (read this first):** archestra-review is dual-licensed per `LICENSE.md`: AGPL-3.0
by default, `LicenseRef-Archestra-Enterprise` for files/regions marked Enterprise (SPDX header,
`*.ee.*` filename, or an `ee/` directory). This matters for a per-mechanism verdict, not just a
blanket one — see the per-section notes below and the summary table at the end. aisotropy's
constitutional gate blocks AGPL adoption outright (`.claude/rules/constitutional-gates.md`,
`.claude/rules/cos/license-policy.md`); nothing in this document is copied code, and nothing here
is a recommendation to vendor or import archestra-review — it is architecture-pattern extraction
only, per the task's clean-room constraint.

---

## 1. MCP Gateway — OAuth, On-Behalf-Of, DCR, token exchange

**How it works.** A gateway (`docs/pages/platform-mcp-gateway.md`) is a named MCP surface with its
own auth, visibility, and tool assignments. Archestra draws a hard line between two credential
layers that never touch each other directly:

- **Gateway Token** — how the *client* (Cursor, Claude Desktop, a custom agent) authenticates to
  Archestra. Six methods: OAuth 2.1 (Authorization Code + PKCE, DCR per RFC 7591, CIMD), OAuth
  client credentials (machine-to-machine), OAuth authorization code with a registered app acting
  "on behalf of" the signed-in user, static bearer tokens (`arch_<token>`), ID-JAG (RFC 8693 token
  exchange per the MCP Enterprise-Managed Authorization extension), and IdP JWKS (direct JWT
  validation against an external IdP).
- **Upstream MCP Server Token** — how *Archestra* authenticates to the tool's backing system
  (GitHub, Jira, ServiceNow, …), resolved and injected at tool-call time, never seen by the client.

The "tools run as the user, not a shared service account" property is the **credential resolution**
step, implemented in `platform/backend/src/models/tool-invocation-policy.ts` (policy layer) plus
the runtime injector in `platform/backend/src/clients/mcp-client.ts`. Per
`docs/pages/mcp-authentication.md` ("Resolve at Call Time"), a tool assignment can be pinned to a
specific installed connection, or resolved dynamically: **on-behalf-of-the-user by default** —
the calling identity's own connection first, falling back to a team connection, then an org
connection — or, if the server's "Default credential" is set to "Always use one account", a shared
service account. `mcp-client.ts` injects the resolved token as an `Authorization: Bearer` header at
dispatch time (`buildDefaultAuthorizationHeaders`, lines ~4514–4535; `headers.Authorization =
Bearer ${tokenAuth.rawToken}` at the tokenAuth call sites, lines ~1972/~2024) — the header is set
per-request from whichever credential resolution picked, so two callers hitting the same gateway
tool can end up making the upstream call as two different upstream identities.

**On-Behalf-Of (OBO), concretely.** For Microsoft Entra, the OBO exchange is a literal RFC 8693
`urn:ietf:params:oauth:grant-type:jwt-bearer` grant with `requested_token_use: "on_behalf_of"`:

```
platform/backend/src/services/identity-providers/enterprise-managed/exchange-strategies/entra-obo-strategy.ts
  requestBody = { client_id, grant_type: JwtBearer, requested_token_use: "on_behalf_of", assertion }
```
*(paraphrased field list, not verbatim code — AGPL, pattern reference)*

`exchange.ts` (`enterprise-managed/exchange.ts`) is the strategy dispatcher: it picks between
`entra_obo`, `okta_managed`, and generic `rfc8693` based on either an explicit config field or a
hostname sniff (`isEntraHostname` / `isOktaHostname`) against the IdP issuer URL. **This whole
enterprise-managed-credential file tree carries `SPDX-License-Identifier:
LicenseRef-Archestra-Enterprise`** — OBO/Okta/RFC-8693 downstream token exchange is an Enterprise
feature, not part of the AGPL core. The gateway's own OAuth 2.1 / DCR / bearer-token / JWKS paths
(the client-facing half) are not under that SPDX marker and appear to be AGPL by the license
router's default rule.

**DCR / CIMD / discovery.** Per the doc, clients self-register at `POST /api/auth/oauth2/register`
(RFC 7591) or use CIMD (client_id as a URL Archestra fetches metadata from); `ARCHESTRA_AUTH_DCR_ENABLED=false`
locks the gateway to pre-registered clients only. Well-known endpoints
(`/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`) are exposed for
standard MCP-client auto-discovery.

**Skepticism note.** I did not read the OAuth server implementation itself (better-auth wiring,
token issuance) in depth — `platform/backend/src/auth/better-auth.ts` exists and is the likely
seam, but I traced the *credential resolution and injection* side (how a tool call ends up
authenticated upstream), which is the security-relevant half for "tools run as the user." The
claim that this is Google's "Enterprise-Managed Authorization" MCP extension implemented faithfully
is doc-sourced, not verified against the MCP spec text.

---

## 2. Dynamic Tool Loading — "load tools when needed"

**How it works.** With **Load tools when needed** enabled on a gateway or agent, the client's
initial `tools/list` shrinks to exactly two meta-tools: `search_tools` and `run_tool`
(`platform/backend/src/archestra-mcp-server/search-tools.ts`,
`platform/backend/src/archestra-mcp-server/run-tool.ts`). The full tool catalog is not sent to the
model up front.

- `search_tools` ranks the agent's reachable tools (assigned +, in "Auto" mode, every
  discoverable tool the caller can access) with a hand-rolled **BM25F** ranker over five weighted
  fields (name/title/description/argNames/argDescriptions — field weights 10/6/3/2/1, `search-tools.ts`
  lines ~987–1153) plus exact-name/title boosts, or a `safe-regex2`-guarded regex mode. Results
  return a *compact one-line parameter signature* (`name!:type`, nested objects collapsed to 2
  levels, enums capped at 20 values with an overflow marker) rather than the full JSON Schema —
  this is the actual mechanism behind the "600 tokens not 60,000" pitch: each candidate tool costs
  roughly one line of text, not a full schema block.
- `run_tool` dispatches to any resolved tool name — built-in, third-party MCP, or an
  `agent-<id>` delegation — and **re-runs every gate a direct call would hit**: RBAC
  (`filterToolNamesByPermission`), the per-conversation enabled-tool set, tool invocation policy
  evaluation (`evaluateSingleMcpToolInvocationPolicy`, same function the direct-call gateway path
  uses), and argument-schema validation. This is explicit in the doc ("`run_tool` does not bypass
  input conditions, team conditions, untrusted-context rules, or approval-required rules") and
  confirmed in code: `run-tool.ts` calls the identical `evaluateSingleMcpToolInvocationPolicy` used
  by the direct dispatch path, so the "load on demand" ergonomics never become a policy-bypass
  surface — a security-relevant design choice (a lazy-loading indirection is a classic place a
  security team forgets to re-gate).

**Skepticism note on the marketing number.** I grep'd the repo for "600 token" / "60,000 token" —
not found verbatim anywhere in `docs/pages/` or `README.md`. The figure is presumably from
Archestra's blog/marketing, not this checkout. What *is* verifiable in code is the mechanism that
would produce a large token reduction (compact one-line signatures + on-demand two-tool surface
instead of N full JSON-Schema tool defs) — I'm citing the mechanism, not vouching for the specific
number.

---

## 3. Taint-Tracking / Data-Leak Prevention — the standout mechanism

This is the most interesting piece of the codebase from a security-architecture standpoint: a
**deterministic, stateless-recompute taint tracker** enforced at the tool-dispatch layer, not a
model-judgment call.

### 3.1 How trust state is computed (not stored)

`platform/backend/src/guardrails/trusted-data.ts` — `evaluateIfContextIsTrusted(messages, …)` —
walks **every tool call in the full conversation message history** on **every agent turn** (not
just the newest one) and re-derives whether the context is "trusted" from scratch:

1. Collect every `toolCalls[]` entry across all messages, skipping platform-authored results (a
   `tool_state` error envelope, or a seeded app-render pointer) that carry no external data.
2. Bulk-evaluate each tool result against `TrustedDataPolicyModel` (`models/trusted-data-policy.ts`).
3. **Default-deny**: a result is untrusted unless a policy explicitly marks it `mark_as_trusted`.
   The code comment states this outright: *"KEY SECURITY PRINCIPLE: Data is UNTRUSTED by default
   … If no policy matches, the data is considered untrusted … allowlist approach for maximum
   security."* An unknown tool (no DB row) is also untrusted by construction.
4. A policy per tool result can resolve to `Safe` (trusted), `Sensitive` (untrusted), `Blocked`
   (content replaced with a refusal notice), or `Dual LLM` (routed through quarantine — see §4).
   Policies support field-path conditions with wildcards (`emails[*].from`) and operators
   (`endsWith`, `contains`, `regex`, context conditions like team membership) — e.g. "every
   `emails[*].from` ends with `@mycompany.com`" → Safe, else → Sensitive.

**Why recompute instead of storing a flag:** this is architecturally significant. There is no
`conversation.tainted = true` mutable field anywhere in this path — the entire message history is
re-scanned each time `evaluateIfContextIsTrusted` runs. That makes the taint state a pure function
of conversation history, immune to a bug (or an attacker) flipping a stored boolean back to
"trusted" mid-session; the only way to be trusted again is for the *history itself* to contain no
untrusted tool result. The call site confirms this runs per-turn: `agents/context-trust.ts` wraps
it as `evaluateToolExecutionContextTrust`, called from
`platform/backend/src/clients/chat-tool-builder.ts` (~line 192) immediately before every Archestra
built-in tool dispatch, using `options.messages` — the AI SDK's live message array for that turn.

### 3.2 How taint gates the next tool call

Once `contextIsTrusted` is computed, it flows into
`platform/backend/src/models/tool-invocation-policy.ts` — `ToolInvocationPolicyModel.evaluateBatch`
— which evaluates **tool call policies** (as opposed to tool *result* policies above) per tool:

- `allow_always` (stored as `allow_when_context_is_untrusted`) — runs regardless of taint.
- `block_when_context_is_untrusted` — allowed when trusted, blocked once the context is tainted.
- `block_always` — never runs, taint irrelevant.
- `require_approval` — requires human-in-the-loop; in an autonomous run this blocks outright
  (`TOOL_INVOCATION_APPROVAL_REQUIRED_AUTONOMOUS_REASON`).
- **No policy configured at all** → the restrictive branch: `TOOL_INVOCATION_NO_POLICY_UNTRUSTED_REASON`
  blocks the call once context is untrusted. An un-configured tool is not fail-open.

Specific (conditioned) policies are evaluated before default (unconditioned) policies, and a
`block_always` match short-circuits immediately — this is the deterministic ordering that makes the
system auditable: given a stored policy set and a taint state, the allow/block decision has exactly
one code path, not an LLM re-deciding at runtime. `platform/backend/src/guardrails/tool-invocation.ts`
(`evaluateSingleMcpToolInvocationPolicy`, `evaluatePolicies`) is the call-site glue used identically
by the MCP Gateway direct-call path and the `run_tool` dynamic-dispatch path (§2), so the same
enforcement point covers both surfaces — no separate "fast path" that skips the check.

### 3.3 Observability tie-in

Blocked calls get their own OTel spans even though they never executed:
`platform/backend/src/observability/tracing/mcp.ts` — `recordBlockedToolSpans` — emits a
zero-duration span per blocked tool call with `mcp.blocked=true` and `mcp.blocked_reason` set,
`SpanStatusCode.ERROR`, alongside the same GenAI semantic-convention attributes (agent id/name,
tool name, session id) as a normal executed-tool span. This means a security operator can query "how
many tool calls did the lethal-trifecta guardrail block, for which agent, in which conversation"
directly from trace data — the taint enforcement is not just deterministic, it is auditable
end-to-end (see §6).

**License note for §3:** `trusted-data.ts`, `tool-invocation.ts`, `tool-invocation-policy.ts`,
`trusted-data-policy.ts`, `search-tools.ts`, `run-tool.ts` carry no SPDX Enterprise header — under
the license router's default rule this is **AGPL-3.0**, i.e. the taint-tracking core itself is in
the open-source tier, not gated behind the Enterprise license. (Some *policy authoring* UX around
it — e.g. auto-configuration — may differ; I didn't audit the frontend/UI licensing.)

---

## 4. Dual-LLM + Lethal-Trifecta Guardrails

**The lethal trifecta** (`docs/pages/platform-ai-tool-guardrails.md`, citing Simon Willison): an
agent with (1) access to private data, (2) exposure to untrusted content, (3) the ability to
communicate externally, is exploitable by prompt injection hidden in the untrusted content — the
model cannot reliably distinguish injected instructions from the real task because it's one token
stream. Archestra's stated position is explicit that **prompt engineering alone cannot fix this**;
the fix has to be deterministic and external to the model's own judgment. That framing matches what
the code does (§3): the allow/block decision is a policy lookup, not an LLM self-check.

**Dual LLM implementation** — `platform/backend/src/agents/subagents/dual-llm.ts`
(`DualLlmSubagent`). This is a code-verified instance of Willison's "Dual LLM pattern" /
CaMeL-style quarantine, not just a doc claim:

- Two distinct built-in agents are loaded via `AgentModel.getBuiltInAgent`:
  `BUILT_IN_AGENT_IDS.DUAL_LLM_MAIN` and `BUILT_IN_AGENT_IDS.DUAL_LLM_QUARANTINE`.
- **Main agent** (`processWithMainAgent`) never receives the raw tool output. It only sees the
  user's original request and a running transcript of `QUESTION:`/`OPTIONS:` /
  `Answer: <index>` turns it generated itself. It runs for up to `maxRounds` (configurable per
  built-in agent, default 5) asking one multiple-choice question at a time, or emits the literal
  string `DONE` to stop early.
- **Quarantine agent** (`answerQuestion` → `executeObjectAgent` with a Zod schema
  `{ answer: z.number().int() }`) is the *only* thing that ever sees the untrusted `toolResult`. Its
  output is constrained by `generateObject` to a bare integer index into the main agent's own
  option list — structurally incapable of returning free text, so there is no channel for an
  injected instruction in the tool output to reach the main agent as prose. Even an out-of-range or
  non-numeric answer is defensively clamped (`options.length - 1`, i.e. the safest/last option).
- After the round budget, the main agent writes a final summary **from the Q&A transcript alone**
  (`buildSummaryPrompt`) — it never touches `toolResult` directly.
- Wired in from `guardrails/trusted-data.ts`: when a tool result policy resolves to
  `shouldSanitizeWithDualLlm`, `evaluateIfContextIsTrusted` instantiates
  `DualLlmSubagent.create(...)` and replaces the tool result in the conversation with
  `analysis.result` (the sanitized summary) — the raw content is discarded from the context the
  main conversational agent sees, and the result is marked trusted (`toolResultIsTrusted = true`)
  since it passed through quarantine.

**Verdict:** this matches the docs' description exactly (`docs/pages/platform-built-in-subagents.md`
§"Dual LLM Agent") and is a genuinely distinct security control from the taint-tracking allow/block
gate in §3 — taint-tracking decides *whether a later tool can run at all*; Dual LLM decides *whether
untrusted content can be summarized into context without carrying an injected instruction along with
it*. They compose: a tool result policy can be `Dual LLM` (sanitize, then treat as trusted) instead
of the binary `Safe`/`Sensitive`/`Blocked`.

**License:** `agents/subagents/dual-llm.ts` has no Enterprise SPDX header — AGPL by the router's
default rule.

A third component, the **Policy Configuration Subagent** (`agents/subagents/policy-configuration.ts`,
not fully read line-by-line but confirmed to exist and match the doc), auto-proposes default tool
call/result policies from tool metadata (name, description, MCP server, param schema, MCP tool
annotations) sent to an LLM, with reasoning stored for audit — this is the piece that keeps the
system usable at scale (you don't hand-configure guardrails for every one of hundreds of tools),
but it is explicitly a *proposal* mechanism: policies with custom conditions are preserved and not
silently overwritten, so a human-reviewed policy can't be clobbered by the auto-configurator.

---

## 5. Sandboxed Execution, Resource Controls, Prompt-Injection Prevention

**Two separate sandboxes exist** — worth being precise about, since the docs discuss them together:

1. **Code sandbox** (`docs/pages/platform-code-sandbox.md`) — a private Linux container for
   `run_command` (shell + Python) during chat, backed by Dagger.
2. **Skills sandbox** (`platform/backend/src/skills-sandbox/`) — file storage/execution scope for
   agent skills; not deep-dived here beyond confirming its existence and its `execution-sandbox-registry.ts`.

**Runtime shape (Dagger + Rust).** `platform/backend/src/sandbox-runtime/sandbox-runtime-service.ts`
is the TypeScript service-singleton wrapper; it lazily loads a native Rust addon
(`@archestra/sandbox-rs`, built from `platform/archestra-rs/sandbox-rs` + `sandbox-core`) and calls
`runSandbox({ replayEntries, limits, command, cwd, timeoutSeconds, environment })`. The container
model is explicitly **not long-lived**: "the source of truth is an append-only command log … each
command starts a fresh container from a warm base image, replays the recorded history, then runs
the new step" (doc + confirmed by the `replayEntries`/`ReplayStep` machinery in
`platform/archestra-rs/sandbox-core/src/backends/dagger.rs`). This buys crash-safety (an engine
restart just replays the log) at the cost of non-determinism for commands that touch the network,
clock, or a random source on a cold replay.

**Resource controls — verified in the Rust supervisor, not just the doc.**
`platform/archestra-rs/sandbox-core/src/supervisor.rs` embeds a small stdlib-only Python script
(`ARCHESTRA_RUN_PY`, injected into the warm base image at `/usr/local/bin/archestra_run`) that
every sandboxed command runs *through*:

- `resource.setrlimit(resource.RLIMIT_CPU, (cpu, cpu))` and `RLIMIT_AS` (address-space/memory) —
  hard OS-level rlimits, not a soft timeout the process could ignore.
- `os.setsid()` before exec, so a wall-clock timeout can `os.killpg(proc.pid, SIGKILL)` the entire
  process group — a command that forks children doesn't survive the timeout by orphaning them.
- Output capped at `--out-cap` bytes per stream (stdout/stderr independently), with a truncation
  marker appended when hit.
- Defaults confirmed in `supervisor.rs` tests/constructors: `cpu_seconds: 30`, `memory_bytes: 1024 *
  1024 * 1024` (1 GiB) — matching the doc's "30 seconds of CPU, 1 GiB of memory, 120 seconds of
  wall-clock" (the wall-clock timeout is a separate parameter passed alongside, not shown at the
  same line).

**Container identity.** `backends/dagger.rs` chains `.with_user(SKILL_SANDBOX_USER)` after a
root-only setup/chown step (`with_user("root")` → `with_exec(chown -R …)` → back to
`SKILL_SANDBOX_USER`) — i.e. setup runs as root to fix ownership on mounted skill files, then the
actual command executes as a non-root user. This matches the doc's "each container runs as a
non-root user, with no host mounts and no backend environment variables inside."

**Network / prompt-injection angle.** The doc is candid that this sandbox is not attacker-hardened:
*"The sandbox limits what one user's code can reach — it is not a defense against a determined
attacker… the case it handles is the careless script an agent just generated."* Network egress is
on by default (installers need it) and follows the environment's configured network policy applied
to the Dagger engine pod; the doc explicitly warns that an unrestricted policy lets the sandbox
reach link-local and cloud-metadata endpoints, and tells operators to restrict it in production.
This is an honest limitation disclosure, not a gap I'm inferring — it's stated in-doc, which is a
point in favor of the docs' credibility elsewhere in this review.

**Prompt-injection prevention specifically** is *not* primarily a sandbox property in this
architecture — it's handled by the taint-tracking + Dual LLM layers (§3, §4) at the tool-result
level, before untrusted content ever reaches a position where it could instruct the sandbox to do
something. The sandbox's own contribution to injection defense is narrower: resource/identity
isolation so a successful injection that gets code execution can't trivially pivot to the host or
to another tenant's container.

**On `ai-labs/`:** this Rust workspace is **not** a security-enforcement component — its own README
states it's "Rust workspace for Archestra's AI-feature prototyping," currently housing
`archestra-bench`, an internal agentic-eval/benchmark harness (spins up isolated backend instances,
drives scripted chat tasks, grades submissions out-of-band via pytest verifiers that never enter the
sandbox/MCP surface, so the agent can't game its own grading). It's worth noting only because the
task scope named it explicitly; the actual sandbox security code lives in
`platform/archestra-rs/sandbox-core` and `sandbox-rs`, reviewed above.

**License:** `sandbox-runtime-service.ts` and the Rust `sandbox-core`/`sandbox-rs` crates carry no
Enterprise SPDX marker in the files inspected — AGPL by default rule.

---

## 6. Observability — OTel traces + Prometheus

`platform/backend/src/observability/` is a real, wired-in implementation, not a stub:

- **Prometheus**: `/metrics` on the web process (`:9050`) and, when running the separate worker
  deployment, a second `:9000` endpoint for task-queue/Knowledge-Base metrics. Security-relevant
  counter: `llm_blocked_tools_total` — tool calls blocked by invocation policy, labeled by provider/
  model/agent — i.e. the taint-gate's block decisions (§3.2) are directly Prometheus-queryable, not
  just visible in traces.
- **OTel traces**: `platform/backend/src/observability/tracing/mcp.ts` —
  `startActiveMcpSpan` wraps every executed tool call in a span named `execute_tool {toolName}`
  following the OTel GenAI semantic conventions (`gen_ai.tool.name`, `gen_ai.tool.call.id`,
  `gen_ai.agent.id`, etc.), with tool-call-arguments and tool-call-result captured as span *events*
  only when `captureContent` is enabled in config (i.e. content capture is an opt-in, not
  unconditionally logging tool payloads into traces — a reasonable default for sensitive tool
  output).
- **Blocked-call spans**: `recordBlockedToolSpans` (same file) emits a span *per blocked tool call
  name* even though nothing executed — `mcp.blocked=true`, `mcp.blocked_reason=<reason>`,
  `SpanStatusCode.ERROR` — using the same agent/team/session/user attribute-setting helpers as a
  normal span, so blocked and executed calls are queryable side-by-side in the same trace backend
  with the same attribute schema. This is the piece that makes §3's deterministic guardrail
  auditable in practice: an operator can build a dashboard of "which agents hit the lethal-trifecta
  gate, for which tool, how often" without any bespoke instrumentation — it's the same trace/metric
  pipeline as everything else.
- Auth: bearer-token or basic-auth to the OTLP collector endpoint (`ARCHESTRA_OTEL_EXPORTER_OTLP_ENDPOINT`),
  OTLP-compatible backend agnostic (Jaeger/Tempo/Honeycomb/Grafana Cloud named as examples).

**License:** the tracing/metrics files inspected carry no Enterprise SPDX marker — AGPL by default rule.

---

## Summary — mechanism list, how each works, standout ideas

| Mechanism | How it works | File(s) | License tier |
|---|---|---|---|
| MCP Gateway auth | 6 client auth methods (OAuth 2.1+DCR/CIMD, client-credentials, auth-code OBO, bearer, ID-JAG, JWKS); credential resolution picks caller's own connection vs. shared service account at call time | `docs/pages/mcp-authentication.md`, `models/tool-invocation-policy.ts`, `clients/mcp-client.ts` | AGPL (gateway) |
| Enterprise OBO/token-exchange | RFC 8693 `on_behalf_of` grant (Entra), Okta managed-credential exchange, generic RFC 8693 — strategy dispatch by IdP hostname or explicit config | `services/identity-providers/enterprise-managed/*` | **Enterprise** (SPDX-marked) |
| Dynamic tool loading | Two meta-tools (`search_tools`/`run_tool`) replace the full tool list; BM25F keyword ranking + compact one-line param signatures; `run_tool` re-runs every RBAC/policy/schema gate a direct call would | `archestra-mcp-server/search-tools.ts`, `run-tool.ts`, `dynamic-tools.ts` | AGPL |
| **Taint-tracking (standout)** | Stateless recompute of trust from full message history on every turn; default-deny (untrusted unless explicitly allow-listed); deterministic policy lookup (not LLM judgment) gates the *next* tool call; blocked calls get audit spans | `guardrails/trusted-data.ts`, `guardrails/tool-invocation.ts`, `models/trusted-data-policy.ts`, `models/tool-invocation-policy.ts`, `agents/context-trust.ts` | AGPL |
| Dual LLM / lethal trifecta | Main agent never sees raw untrusted output; quarantine agent sees it but can only answer a bounded multiple-choice index (`generateObject` + Zod int schema); final summary built from Q&A transcript only | `agents/subagents/dual-llm.ts` | AGPL |
| Sandboxed execution | Dagger-backed, replay-log container model (no long-lived state); Rust in-container supervisor enforces `RLIMIT_CPU`/`RLIMIT_AS`, process-group SIGKILL timeout, output caps; non-root user after root-only chown setup | `sandbox-runtime/sandbox-runtime-service.ts`, `archestra-rs/sandbox-core/src/supervisor.rs`, `.../backends/dagger.rs` | AGPL |
| Observability | Prometheus (`llm_blocked_tools_total` etc.) + OTel GenAI-semconv spans; blocked tool calls get their own zero-duration spans with `mcp.blocked`/`mcp.blocked_reason` for audit | `observability/tracing/mcp.ts`, `observability/metrics/*` | AGPL |

**The standout idea, in one sentence:** taint isn't a mutable flag an attacker (or a bug) could
reset — it's a pure function recomputed from the entire tool-call history on every single turn,
default-deny, enforced by a policy-table lookup instead of asking the model "is this safe," and the
block decision itself is emitted as a first-class trace span so the control is auditable, not just
effective. Combined with Dual LLM's information-theoretic quarantine (the untrusted-content-reading
agent is structurally unable to emit free text), this is a genuinely deterministic answer to the
lethal trifecta rather than a probabilistic guardrail bolted on top of the same model that's
supposed to be defended against injection.

**Overall confidence / caveats:** high confidence on the mechanisms actually read in full
(taint-tracking, Dual LLM, sandbox resource limits, gateway credential model) — these were verified
against source, not just docs. Lower confidence on: the OAuth/DCR server implementation itself
(`better-auth.ts` wiring not read in depth), the skills-sandbox's isolation properties relative to
the code-sandbox (not deep-dived), and the exact list of what else besides identity-provider
token-exchange is Enterprise-licensed (I checked SPDX headers only on the files I opened, not an
exhaustive repo-wide license audit — a full audit would `grep -rl "LicenseRef-Archestra-Enterprise"`
across the whole tree, which I did not run).
