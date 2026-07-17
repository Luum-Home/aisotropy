# Archestra (AGPL) vs aisotropy — Security-Surface Gap Analysis

> **Date:** 2026-07-17 · **Type:** clean-room, patterns-only research report
> **Scope:** what aisotropy already has, what it could *learn* (not adopt-code-from), what is genuinely novel in Archestra.
> **Discipline:** every aisotropy claim below cites `file:line` (code-truth). Archestra claims cite its docs (behavioral description, no code read into aisotropy).

Archestra is an enterprise MCP-agent-security platform: an LLM/MCP gateway with OAuth
On-Behalf-Of, dynamic tool loading, deterministic taint-tracking guardrails
(Dual-LLM + Lethal-Trifecta), sandboxed code execution, and OTel + Prometheus
observability. aisotropy is an in-process multi-tenant "LLM-OS" runtime. The two
overlap on the *tool-safety* surface; they diverge sharply on the *gateway / identity
brokering* surface.

The headline: **aisotropy already has a stronger per-value Dual-LLM primitive and a
stronger key-broker sandbox invariant than Archestra's docs describe. Archestra has a
conversation-level deterministic taint state machine, argument/result value policies,
and per-user upstream credential brokering that aisotropy does not.**

---

## 1. Per-dimension comparison

| Dimension | aisotropy | Evidence (aisotropy) | Archestra (doc) | Verdict |
|---|---|---|---|---|
| **Dual-LLM guardrail** | **HAVE (arguably beats)** | `internal/guardrails/quarantine.go:52-127` (structural tool-less `QuarantinedModel` + semantic `Schema`, fail-closed `Extract`) | Dual-LLM Agent: main agent never sees raw tool output; quarantine agent answers constrained multiple-choice | **Match / edge to aisotropy** — aisotropy's guarantee is *by construction* (compile-time no-tool seam + total schema validation), not a subagent convention |
| **Scope-gated tools** | **HAVE** | `internal/tools/tools.go:19-54` (`Tool.RequiredScope`, `Registry.Permitted`); double gate in `internal/api/chat_agent.go:161,234` (offer-only-permitted + structural re-check) | Static per-gateway/team tool assignment + access control | **Match** — aisotropy gates on verified scopes; Archestra gates on team/gateway membership |
| **Sandboxed execution** | **HAVE (beats on one axis)** | `internal/sandbox/sandbox.go:1-42` broker invariant (provider key **never** enters the box); secure-by-default hardening (`--network=none`, cap-drop-ALL, read-only rootfs, pids/mem/cpu caps) `sandbox.go:217-276`; Kata/gVisor runtime knob `sandbox.go:196-215`; WASM in-process sandbox `internal/plugin/loader.go:147-177` | Sandboxed code execution, K8s-native FS | **Match** — aisotropy's *key-broker inversion* (model stays host-side, only tool effects are boxed) is a sharper, explicitly-documented invariant |
| **Injection detection** | **PARTIAL** | `internal/guardrails/injection.go` + model scanner `scanner.go`/`daemon_scanner.go`; **fails open** by design (`guardrails.go:58-71`) | Probabilistic LLM guardrails available but explicitly *not* the final control plane | **Match (both weak here by design)** — detection is never complete; both lean on stronger deterministic layers |
| **Output leak prevention** | **HAVE** | `internal/guardrails/output.go:164-186` (secret-echo + system-prompt-segment-echo + toxicity, fail-closed leak verdict) | Not the emphasized surface (taint gating handles exfil) | **aisotropy-only** on *content* leak; see taint gap for *action* leak |
| **MCP integration** | **PARTIAL (client-side only)** | `internal/api/mcp_tools.go:36-53` (`BuildMCPTools` adapts an MCP server's tools, each scope-gated); `internal/plugin/loader.go:86-131` (MCP/WASM/go-import plugin kinds) | Full **MCP Gateway**: one curated MCP endpoint fronting many servers, per-gateway visibility/auth | **aisotropy consumes MCP; Archestra serves MCP** — different role |
| **OAuth On-Behalf-Of / credential brokering** | **MISSING** | JWT identity only: `aisotropy/authenv.go:80-124` (HS256/RS256/JWKS), must-agree bind + verified scopes `internal/api/authn.go:122-128`. No per-user *upstream* credential resolution (grep: no `OnBehalf`/`token exchange`) | Per-user upstream creds: OAuth 2.1, ID-JAG (RFC 8693), JWKS, "Resolve at call time" — tools run *as the user* | **Archestra-only (genuine gap)** |
| **Taint-tracking / context-aware enforcement** | **MISSING** | Quarantine is *per-value*, not *per-conversation* (`quarantine.go:118-127`). No conversation trust-state that dynamically narrows the tool set (grep: no `taint`/`sensitive context`/`trifecta` outside quarantine docstrings & unrelated `internal/tenancy` SQL-taint) | Deterministic Lethal-Trifecta state machine: reading untrusted/external data marks context *sensitive*, which **disables exfil-capable tools** for the rest of the loop (incl. subagents) | **Archestra-only (genuine gap — likely #1 to learn)** |
| **Tool call/result value policies** | **MISSING** | Gate is static scope (`tools.go:43-54`), not a predicate over runtime args/results | Deterministic policies over **args** (`to[*]` ends `@company.com` → allow, else approve/block) and **results** (classify sensitive by `from` domain), plus a `require-approval` tier | **Archestra-only (genuine gap)** |
| **Dynamic tool loading** | **MISSING** | All permitted tools built up front (`chat_agent.go:234-240`); no lazy discovery (grep: no `search_tools`/`run_tool`) | "Load tools when needed": `tools/list` shows only `search_tools`+`run_tool`; policies still enforced on the tool actually run | **Archestra-only (moderate gap)** |
| **Observability** | **PARTIAL** | OTel *traces*: `internal/llm/otel.go` (one span/StreamChat, GenAI semconv); in-process cost meter `internal/costmeter/costmeter.go` + `/costs` route | OTel traces **+ Prometheus metrics** (`/metrics`) + metric→trace **exemplars** + content-capture span events | **Archestra deeper** — aisotropy has traces + per-tenant cost, lacks Prometheus metrics/exemplars |

---

## 2. What aisotropy already matches or beats

1. **Dual-LLM quarantine is stronger as built.** `quarantine.go` makes the tool-less
   property a *type-signature* guarantee (`QuarantinedModel` has no channel to pass
   tools in or return a tool call, `quarantine.go:52-75`) and admits only
   schema-conforming values (`EnumSchema`/`BoolSchema`, exact-match, `quarantine.go:196-252`),
   failing closed on anything else (`quarantine.go:157-178`). Archestra's Dual-LLM is a
   *subagent convention* (constrained multiple-choice). Same idea; aisotropy's is
   enforced by construction, not by prompt/agent wiring.

2. **The key-broker sandbox invariant is a sharper isolation stance.** `sandbox.go:1-42`
   states — and the task runner enforces — that the provider API key is *never* placed
   in `Spec.Env` and the LLM call never happens inside the box. Only the *effect* of a
   tool crosses the boundary. A prompt-injected command can vandalize a throwaway
   worktree but cannot exfiltrate the key "because the key was never in the room." Plus
   secure-by-default hardening and a drop-in Kata/gVisor runtime knob (`sandbox.go:196-268`).

3. **Double scope-gate with authority resolution.** Tools are offered only if permitted
   *and* an unregistered/hallucinated tool name is structurally unable to execute
   (`chat_agent.go:230-259`); scopes come from the *verified* principal so a client
   cannot self-grant via body scopes (`authn.go:122-128`). This is a real,
   test-backed isolation seam, not a doc claim.

4. **Content-level leak prevention** (secret echo + system-prompt-segment echo,
   `output.go:164-186`) is a surface Archestra's docs de-emphasize.

---

## 3. What is genuinely novel in Archestra (aisotropy lacks)

1. **Deterministic conversation-level taint tracking (Lethal-Trifecta defense).**
   The trifecta = private-data access + untrusted-content exposure + external-comms
   ability held *simultaneously*. Archestra breaks it deterministically: a tool *result
   policy* can mark returned data **sensitive**, which flips the conversation into a
   sensitive trust state; *tool call policies* then **block exfil-capable tools** while
   in that state — and delegation to a subagent **inherits** the taint. Crucially the
   final allow/block is from *stored policy*, not a fresh model judgment
   (auditable, predictable). This is the strategic idea aisotropy is missing: it has a
   strong *per-value* quarantine but no *per-conversation* trust state that reshapes the
   scope-gated tool set as untrusted data enters.

2. **Value-level tool policies (args + results), with a `require-approval` tier.**
   Decisions depend on the *actual attempted action*, not just the tool name: inspect
   `to[*]` recipients before `send_email` runs; classify `emails[*].from` domains after
   `read_email` returns. aisotropy's gate is a static scope check only.

3. **Per-user upstream credential brokering (OAuth OBO / token exchange).** Tools run
   *as the user*: gateway auth (OAuth 2.1 / ID-JAG / JWKS / bearer) is separate from
   upstream MCP-server auth, resolved at call time (RFC 8693 token exchange, "Resolve at
   call time"). aisotropy authenticates identity (JWT) and *brokers the provider key*
   host-side, but does not resolve per-user *downstream/upstream* credentials.

4. **Dynamic ("load-when-needed") tool loading** via `search_tools`/`run_tool`, keeping
   `tools/list` small while policies still bind to the tool actually executed.

5. **Deeper observability**: Prometheus `/metrics` + metric→trace exemplars +
   content-capture span events, versus aisotropy's traces-only + in-process cost meter.

---

## 4. Prioritized clean-room patterns to adopt (with aisotropy-seam mapping)

Patterns, not code. Each maps onto an existing aisotropy seam without importing or
transcribing any Archestra source.

### P1 — Conversation-level taint state that narrows the scope-gated tool set  *(highest value)*
**Idea:** track a per-request trust flag that flips to *tainted* when untrusted data
enters the context (a RAG chunk, a tool result classified sensitive, third-party text);
while tainted, remove exfil-capable tools from the permitted set for the remainder of
the loop. Deterministic, stored-policy-driven, auditable.

**Why it fits:** aisotropy already computes the permitted tool set *per request* and
per step (`chat_agent.go:161,234` → `Registry.Permitted(scopes)`). Taint becomes a
second, dynamic filter composed *after* the static scope filter: `Permitted(scopes)`
minus `ExfilCapable` when the request is tainted. It also dovetails with the existing
injection `Verdict` (`guardrails.go:51-71`) and the quarantine (a "sensitive result"
is exactly what should be routed through `quarantine.Extract` rather than admitted as
free text). The `RequiredScope` field could gain a sibling `SafeInTaintedContext bool`
(or an "egress" capability tag) on `tools.Tool` (`tools.go:19-30`) so the taint filter
is declarative. Subagent/step inheritance is natural because taint lives on the
request `context.Context`, the same channel tenant identity already rides
(`authn.go:34`).

**Clean-room note:** the *mechanism* (a boolean/enum trust state gating a
declaratively-tagged tool set) is a standard capability-security idea; implement from
the concept, not from Archestra's policy schema.

### P2 — Value-level tool policies (predicate over args and results)
**Idea:** before a tool runs, evaluate a predicate over its *arguments*; after it runs,
evaluate a predicate over its *result* to set/clear taint (P1). A middle
`require-approval` outcome pauses autonomous execution.

**Why it fits:** aisotropy already validates tool args against a JSON Schema at the
typed-tool boundary and reflects violations back to the model
(`mcp_tools.go:62-82`, `chat_agent.go:249-259`). A policy predicate is an *additional*
check at the same choke point — return a distinct `ToolPolicyError` (sibling of
`ToolArgsError`) that the loop renders as "blocked/needs approval" instead of executing.
The result-side predicate hooks the same place tool output is rendered
(`mcp_tools.go:89-116`) and feeds P1's taint flag. Reuses the existing
`OutputCheck` extension shape (`output.go:125,209-221`) as the predicate interface.

### P3 — Per-user upstream credential brokering (OAuth OBO), preserving the broker invariant
**Idea:** resolve the *downstream* credential for an MCP/tool call from the *verified
caller identity* at call time, so tools act as the user, not a shared service account.

**Why it fits (and the guardrail):** aisotropy already has verified principals and
scopes on the context (`authn.go:94-128`) and an MCP client lifecycle it owns
(`loader.go:114-131`). A `CredentialResolver(ctx) -> upstreamToken` seam, injected into
`mcpHandler` (`mcp_tools.go:62-82`), would map identity→upstream token *host-side*.
**Constitutional constraint:** this must honor the broker invariant
(`sandbox.go:1-42`) — an upstream user token resolved for an MCP call is *not* the
provider API key and must never enter the sandbox `Spec.Env`; it is handed to the
host-side MCP client only. Start with a pluggable resolver interface (static map / env)
before any OAuth token-exchange implementation; full RFC 8693 is a large, separable
effort.

### P4 — Dynamic ("load-when-needed") tool exposure
**Idea:** for large tool sets, expose two meta-tools (`search_tools`, `run_tool`) and
resolve the real tool lazily; keep P1/P2 policies binding on the tool actually run.

**Why it fits:** the permitted-set construction (`chat_agent.go:234-240`) is already the
single place tool specs are assembled; a "lazy mode" wraps it with two synthetic
specs and routes `run_tool(name,args)` back through the same double scope-gate +
policy checks. Lower priority — matters only once catalogs are large.

### P5 — Prometheus metrics + exemplars alongside existing OTel traces
**Idea:** add a `/metrics` endpoint (request/LLM/tool counters + latency histograms)
and trace exemplars so metrics link to traces.

**Why it fits:** the OTel decorator (`otel.go`) and per-tenant cost meter
(`costmeter.go`) already hold the measurements; a metrics exporter is an additive
sibling of the existing tracer, wired at the same decorator seam. Lowest security
value; operational maturity only.

**Suggested order:** P1 → P2 (they compose: P2's result predicate feeds P1's taint) →
P3 → P4 → P5.

---

## 5. License note

The reviewed Archestra checkout (sibling `archestra-review/` repo) is **dual-licensed
AGPL-3.0 / Enterprise** (`LICENSE.md`, `LICENSE_AGPL`, `LICENSE_ENTERPRISE`). Per aisotropy's
constitutional license gate (`.claude/rules/constitutional-gates.md`,
`.claude/rules/cos/license-policy.md`), **AGPL is a hard BLOCKER** for this
MIT/Apache-2.0 SaaS runtime. This analysis is therefore **patterns-only, clean-room**:
no Archestra source was read into aisotropy code, nothing is vendored, no code was
copied. Every pattern above is described at the level of a security *concept* and mapped
onto aisotropy's own pre-existing seams, to be implemented from first principles. Do not
copy Archestra code or config schemas; re-derive from the idea.
