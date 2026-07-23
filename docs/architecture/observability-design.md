---
type: Process
title: "Observability Design — the arena's 1-span finding reframed, a live P0 privacy fix, and the proposed span tree"
description: The live arena's "aisotropy emits 1 span vs 4-15 for competitors" result is path-specific, not structural — the /chat/agent native tool path already emits gollem ext/otel's full agent/tool span tree today, the reflection path simply never attaches the hooks. Documents a P0 privacy finding (gollem records the user prompt and, until an aisotropy-side redactor, tool arguments unconditionally on exported spans) with its fix status as of this writing, the current instrumentation inventory, the OTel GenAI semconv landscape, and a proposed span tree marked DESIGNED, NOT IMPLEMENTED.
tags: [architecture, process, observability, otel, tracing, privacy, gollem, arena, semconv]
timestamp: 2026-07-22T00:00:00Z
---

# Observability Design — the arena's 1-span finding reframed, a live P0 privacy fix, and the proposed span tree

> **Why this document exists.** The live arena's observability dimension board shows aisotropy
> emitting roughly one span per turn against 4–15 for the seven competitor frameworks — a result that,
> read naively, says "aisotropy's telemetry is structurally thin." It is not. It is **path-specific**:
> the code path the arena's observability mode drives happens to be the one aisotropy call path that
> never attaches gollem's own tracing hooks, while the other production call path already emits
> gollem's full agent/tool span tree today, unmodified. Getting this wrong in either direction — either
> shipping the naive "thin telemetry" reading, or silently fixing the gap and claiming credit without
> showing the work — is exactly the kind of doc/product conflation `docs/architecture/arena-adapter-artifacts.md`
> exists to prevent for capability claims generally. This document also carries a genuinely live
> privacy finding (§2) that a reader evaluating this repo needs to see clearly, not buried in a
> changelog line.

## Headline 1 — the arena finding is path-specific, not structural

The `/chat/agent` native tool path **already emits gollem `ext/otel`'s full agent/tool span tree
today.** `internal/llm/toolagent.go:175` passes `core.WithHooks[string](otelTracingHooks(tracer))`
into the `core.Agent[string]` options, and `otelTracingHooks` (`toolagent.go:240-244`) builds
`otel.TracingHooks(...)` from gollem's own `ext/otel` package — the same hooks that produce the
`agent.run → agent.turn[N] → model.request` / `tool.execute.{name}` tree documented in §4 below. That
path is fully wired.

The arena's observability mode, however, drives `App.Turn → ChatCore → chatReflect → GollemReflect`
(`internal/llm/gollem.go:618-651`), and that function's `core.AgentOption[T]` slice
(`gollem.go:624-627`) **never appends a `core.WithHooks` call** — so the only span that appears on
that path is the inner `chat` span `WithOTel` itself produces (`otel.go`, see §3). The package doc at
the top of `otel.go` (lines 1-13) states the intended split plainly: the streaming path
(`internal/api/chat_stream.go`) is `WithOTel`'s own provider-agnostic instrumentation because it never
builds a `core.Agent[T]`; the structured-output/reflection path "does build a `core.Agent[T]` and is
**expected** to use Gollem's own `ext/otel` when that path is exercised in production" — expected,
never actually wired. **One missing hook attachment on one function, not a missing subsystem.**

## Headline 2 — a P0 privacy finding, and its live fix status

**Gollem's own `TracingHooks` records the user prompt unconditionally.** Its `onRunStart` writes the
`gollem.agent.prompt` attribute onto every `agent.run` span — truncated to 4096 characters, but with
**no `TracingOption` that disables it** at all (pinned module
`github.com/fugue-labs/gollem@v0.5.1/ext/otel/tracing.go:124-130`). Separately, gollem's `ext/otel`
package defaults `captureToolArgs` to **true** (`.../ext/otel/options.go:22-27`), which — before any
aisotropy-side mitigation — would put tool call arguments (routinely carrying user-supplied content)
on exported spans as well. Both are **live on `/chat/agent` whenever an OTLP trace endpoint is
configured** (`OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`), and both stand in
direct conflict with `internal/llm/otel.go`'s own verified no-content posture: `WithOTel`'s spans never
record prompt, completion, or reasoning text (reasoning explicitly excluded, `otel.go` around
lines 197-201 in the streaming decorator), and gollem's `captureToolResults` / `captureModelMsgs`
options are correctly left off by default.

**Fix status: LANDED during the writing of this document.** At the time this research began the fix
existed only as an uncommitted working-tree change (`internal/llm/otel.go` and
`internal/llm/toolagent.go` modified, `internal/llm/otel_redact_test.go` untracked, no commit in
`git log --oneline -5`). It has since been committed as **`7b229be`
("fix(llm): P0 — stop exporting user prompts and tool args on telemetry spans")**, which now sits at
the top of `git log --oneline -5` on this branch — verified by re-running that exact command, not
assumed. `git show --stat 7b229be` touches the same three files: `internal/llm/otel.go` (+128/-2),
the new `internal/llm/otel_redact_test.go` (+277), and `internal/llm/toolagent.go` (+27). The design,
as landed, is:

- **Option (a) — a locally-owned redacting `SpanExporter` wrapper — is the option that landed.**
  `otel.go` defines `otelContentDenylist` (`otel.go:123-127`, currently `gollem.agent.prompt`,
  `gollem.tool.args`, `gollem.tool.result`) and `isRedactedGollemAttr` (`otel.go:138-149`, an
  exact-match check plus a defensive `"gollem.*.messages"`-shaped suffix check for any future gollem
  attribute this list does not yet name). `redactingExporter` (`otel.go:172-189`) wraps the OTLP
  `sdktrace.SpanExporter` and strips every matched attribute from every span before it leaves the
  process — via a `redactedSpan` wrapper (`otel.go:192-`) around `sdktrace.ReadOnlySpan` that filters
  `Attributes()`. `NewTracerProvider` installs it by default (`otel.go:561-569`):
  `if !otelCaptureContentEnabled() { spanExporter = &redactingExporter{next: exporter} }`.
  `otelTracingHooks` (`toolagent.go:240-244`) independently sets
  `otel.WithCaptureToolArgs(otelCaptureContentEnabled())` — off unless the same env var is set — as a
  second, defense-in-depth layer against `gollem.tool.args` specifically. Both layers share one opt-in
  escape hatch, `AISOTROPY_OTEL_CAPTURE_CONTENT` (`toolagent.go:34`), documented as development-only:
  setting it disables the redactor and restores gollem's tool-argument capture. The commit message
  itself records two honest limits on the fix's own verification: the redaction was unit-tested at the
  exporter/span-wrapper level, **not decoded off the actual OTLP protobuf wire**; and the defensive
  `"gollem.*.messages"`-shaped denylist rule is speculative — no such attribute exists in the pinned
  gollem `v0.5.1`, so it protects against a hypothetical future one, not a currently observed leak.
- **Option (b) — an upstream gollem `WithCapturePrompt(false)` option** remains the cleaner, cross-repo
  fix (gollem stops recording the unconditional attribute at the source instead of aisotropy stripping
  it after the fact) but was **not** the option taken, and has not been proposed upstream — it would
  require a cross-repo write per the operator's global git rules (`~/.claude/CLAUDE.md`'s "Cross-repo
  writes — checkpoint previo obligatorio"). Option (a)'s local redactor remains valuable as
  defense-in-depth even if option (b) is pursued later: a future gollem upgrade that adds a new
  content-bearing attribute is caught by the denylist/suffix rule before it needs a second aisotropy
  release to react.

**Read this section as a live example of why "as of this writing" matters**: the fix moved from
uncommitted to committed inside the working session that produced this document, which is exactly why
this document names the specific commit and the exact command (`git log --oneline -5 --
internal/llm/otel.go internal/llm/toolagent.go`) to re-verify rather than a stable-sounding but
staleness-prone status word — a future reader should re-run that command against current `HEAD`, not
trust this paragraph's tense.

## 1. Current instrumentation inventory (verified `file:line`)

- **`WithOTel` decorator** (streaming path): one `chat` span per `StreamChat`
  (`otel.go`, span constant `spanNameChat`, `otel.go:90`; start call at the decorator's `StreamChat`
  method, `otel.go:351-368`); a `chat.tools` span per `CallTools`/`CallToolsReasoning`
  (`otel.go:299-345`, span constant `spanNameChatTools`, `otel.go:92-93`). Attributes:
  `gen_ai.system`, `gen_ai.request.model`, `gen_ai.operation.name`; `gen_ai.usage.*` populated only
  from a real provider `Usage`, never fabricated; `aisotropy.stream.delta_count` (deliberately a
  non-`gen_ai.*` extension attribute); `aisotropy.ttft_ms`/`duration_ms`; `tenant_id`/`user_id` read
  from ctx (`stream_core.go:141-142` sets `llm.WithTenant`/`llm.WithUser` specifically so
  `internal/llm`'s `WithOTel` can attribute spans without `internal/llm` importing `internal/api` —
  see the comment at `stream_core.go:135-140`).
- **`NewTracerProvider`** (`otel.go:539-573`): a no-op provider when neither
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` nor `OTEL_EXPORTER_OTLP_ENDPOINT` is set; batched export via
  `otlptracehttp` otherwise; fails open on a misconfigured collector (falls back to no-op rather than
  blocking chat traffic — `otel.go:551-558`'s comment states the reliability posture explicitly); NOT
  registered as the process-global `TracerProvider`; the `aisotropy` public facade has no
  `WithTracerProvider` option today — the arena's own observability adapter documents this exact pain
  point (`arenas/live/aisotropy/mode_observability.go:9-21`).
- **Workers:** `sleepcycle.run` span plus a `agent.optimization.consolidation` span event
  (`internal/sleepcycle/worker.go:85,103`); `bitrate.run` span (`internal/bitrate/coordinator.go:68`).
  Wired from `aisotropy/app.go:654-660` (sleep-cycle coordinator + scheduler) and `app.go:848`
  (bitrate coordinator).
- **Not instrumented at all:** hydration — working-memory summary/recent
  (`internal/api/chat_stream.go:317,321`), facts FTS search (`chat_stream.go:334`), semantic search
  (`chat_stream.go:344` → `searchSemanticContext`, `chat_stream.go:452`); pipeline gates — quota
  (`internal/api/stream_core.go:156`), input guardrail (`stream_core.go:172`), model-gateway selection
  (`stream_core.go:234`), output guardrail (`stream_core.go:310`); persistence + outbox —
  `AppendWithOutbox` (`stream_core.go:226`, `internal/memory/outbox.go:87`), the drain worker
  (`outbox.go:174,213`) and the consolidation sink it feeds — meaning **async consolidation is
  trace-invisible today**. There is **no HTTP-level tracing** (zero `otelhttp`/`otelchi` hits in the
  codebase) — every span emitted today is a root span, with no parent tying it to the inbound HTTP
  request.
- **One product-level detail worth flagging while reading the inventory:** the non-streaming path
  (`App.Turn`/`ChatCore`) passes a nil semantic tier at `internal/api/chat.go:274`, so semantic search
  never runs on that path at all — a product question independent of instrumentation, but relevant to
  §5's span-count prediction below, which assumes the observability mode's actual (tool-less, nil
  semantic) request shape.

## 2. What gollem `ext/otel` provides for free (dependency-reuse-first)

Per `.claude/rules/dependency-reuse-first.md`, the inventory-before-build discipline: gollem
`v0.5.1`'s `ext/otel` package (`TracingHooks`, `tracing.go:56-85`) already emits, once attached, a
full tree per agent run — `agent.run → agent.turn[N] → model.request` and
`tool.execute.{name}` (child-agent nesting), plus `guardrail.{name}`, `output.validation`,
`output.repair`, `run_condition`, and `context.compaction.{strategy}` events (the tree is documented
at `tracing.go:40-49`). Attributes come from `attributes.go:17-24`: `gollem.*` namespaced attributes
plus `gen_ai.request.model` / `gen_ai.system` / `gen_ai.usage.*` / `gen_ai.finish_reason`. This is the
entire agent-loop tree available on **both** aisotropy call paths the moment `core.WithHooks` is
attached — nothing in §5's proposed tree re-implements what this package already does; the proposal's
additive spans are specifically the parts gollem structurally cannot know about (tiered memory,
tenant identity, async outbox/consolidation — see §5).

## 3. The OTel GenAI semantic-conventions landscape

The dedicated GenAI semantic-conventions repository has existed since `v1.42.0` (June 2026); overall
status is **Development** (https://github.com/open-telemetry/semantic-conventions-genai ;
https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/). Span-name vocabulary:
inference spans are named `{operation} {model}` (e.g. `chat gpt-4`); agent spans use
`invoke_agent`/`create_agent`/`invoke_workflow`/`plan`; tool spans use `execute_tool {tool_name}`
(requiring `gen_ai.operation.name` + `gen_ai.tool.name`); retrieval spans use
`{operation} {data_source}`. **Memory operations are first-class in the spec** —
`search_memory`/`create_memory`/`update_memory`/`upsert_memory`/`delete_memory`/`create_memory_store`
— and map directly onto aisotropy's own tier vocabulary (working memory, facts, semantic). A newer
draft replaces `gen_ai.system` with `gen_ai.provider.name`; §1's inventory names `gen_ai.system`
today, so this is a documented, anticipated source of churn, not a current bug. Content attributes
(`system_instructions`, `input.messages`, `output.messages`) are spec'd **SHOULD NOT capture by
default, opt-in only** — aisotropy's existing no-content posture (Headline 2 above) already matches the
semconv default rather than deviating from it.

### How the arena's leaders instrument (cross-checked against `arenas/live/RESULTS.md`)

| Framework | Spans/turn | Approach |
|---|---|---|
| pydantic-ai | 4 | `invoke_agent → chat {model} → execute_tool` — pure semconv via `InstrumentedModel`/Logfire (https://ai.pydantic.dev/api/models/instrumented/) |
| ai-sdk | 6 | `invoke_agent`/`step N`/`chat` — semconv-ish, via `@ai-sdk/otel` |
| mastra | 10–11 | `AGENT_RUN`/`MODEL_GENERATION`/`TOOL_CALL` internal names, mapped to semconv on export (https://mastra.ai/en/docs/observability/ai-tracing) |
| langgraph | 9 | per-node span names via a callback bridge (https://blog.langchain.com/end-to-end-opentelemetry-langsmith/) |
| llamaindex | 15 | method-level spans via its own dispatcher, using OpenInference `span.kind` rather than pure semconv (https://docs.llamaindex.ai/en/stable/module_guides/observability/instrumentation/ ; https://arize-ai.github.io/openinference/spec/semantic_conventions.html) |
| genkit-go | 5 | flow/action taxonomy (https://pkg.go.dev/github.com/firebase/genkit/go/core/tracing) |

Operator/backend side: Langfuse maps `gen_ai.*` spans directly to its own "generation" concept
(https://langfuse.com/integrations/native/opentelemetry); Phoenix translates both OpenInference and
`gen_ai.*` conventions (https://arize.com/docs/phoenix/tracing/concepts-tracing/translating-conventions);
Datadog has native `gen_ai.*` support (https://www.datadoghq.com/blog/llm-otel-semantic-convention/).

**Conclusion for aisotropy's own consumption target: aim at the official `gen_ai.*` semantic
conventions' operation names; OpenInference is unnecessary** — the only framework leaning on it
(llamaindex) is doing so for span-kind vocabulary, not because a `gen_ai.*`-speaking backend cannot
already ingest aisotropy's spans today (it can, per §1's attribute inventory).

## 4. Proposed span tree — **DESIGNED, NOT IMPLEMENTED**

Nothing in this section exists yet. No hook, span, or attribute described below has landed in
`internal/api`, `internal/llm`, or `internal/memory`.

```
invoke_agent aisotropy                          (root)
├── search_memory working_memory                 (hydration: wm.Summary + wm.Recent)
├── search_memory facts                          (hydration: FTS5 search)
├── retrieval {collection}                       (hydration: semantic search)
│   └── embeddings {model}                       (the embedding call the retrieval depends on)
├── create_memory working_memory                 (user turn persist + outbox enqueue)
├── agent.run                                    (gollem ext/otel, once hooks attach)
│   └── agent.turn[N]
│       ├── model.request
│       │   └── chat {model}                     (the EXISTING WithOTel span, becomes a child — zero WithOTel code change needed)
│       ├── output.validation / output.repair    (gollem, conditional)
│       └── tool.execute.{name}                  (gollem, per tool call)
├── create_memory working_memory                 (assistant turn persist)
└── [span link] consolidation.drain               (async outbox drain — linked, not a child, since it outlives the request)
```

Events, not spans (sub-millisecond, no independent duration worth a span): `quota.allowed` /
`quota.degraded`; guardrail verdicts (names and scores only — **never** content, matching the
existing no-content posture).

### Hook points and effort

| Hook point | Where | Effort |
|---|---|---|
| Root `invoke_agent` span | `StreamCore` (`stream_core.go:130`) + `ChatCore` | S — unlocks parenting for everything else; the existing `chat` span becomes a child with **zero** `WithOTel` code changes |
| `search_memory` (working memory) | `chat_stream.go:317-325` | S |
| `search_memory` (facts) | `chat_stream.go:333-339` | S |
| `retrieval` / `embeddings` | `searchSemanticContext`, `chat_stream.go:452` | S |
| `create_memory` (user + assistant persist) | `stream_core.go:210-229` / `internal/memory/outbox.go:87` | S |
| gollem `TracingHooks` on the reflection path | add `core.WithHooks` at `gollem.go:624-627`, thread a tracer through, or register a global provider at `aisotropy/app.go:654` (`startRuntime`) | S/M |
| Outbox `traceparent` + linked `consolidation.drain` | `OutboxEvent` (`outbox.go:35-`) gains a trace-context field; linked span in the drain worker (`outbox.go:174,213`) | M |
| Rename `chat` → `chat {model}` | `otel.go:90` (constant) + `otel.go:351-368` (span start) | S |
| Facade `WithTracerProvider` option / read the global provider | closes the exact gap the arena adapter documents (`mode_observability.go:9-21`) | S |
| `otelhttp` SERVER span | optional — turns every root span into a real child of an HTTP-level span | S, optional |

### Ranked implementation order

1. **P0 — the redacting exporter** (Headline 2 above) — **DONE**, landed as commit `7b229be`.
2. Root `invoke_agent` (`StreamCore` + `ChatCore`).
3. Hydration `search_memory`/`retrieval`/`embeddings`.
4. `GollemReflect` `TracingHooks` attachment (closes Headline 1's gap).
5. `create_memory` spans + the `chat {model}` rename.
6. Outbox `traceparent` + linked `consolidation.drain`.
7. Facade `WithTracerProvider` / global-provider read.
8. Optional: `otelhttp` server spans; an upstream gollem naming PR (`tool.execute.{name}` →
   `execute_tool {name}`, and adding `gen_ai.operation.name` to tool spans — gollem's current naming
   degrades gracefully in Langfuse via attribute-based mapping, so this is a polish item, not a
   blocker); a linked span chain into the sleep-cycle worker.

### Constitutional constraint on every new span

Every span this design adds records **counts, scores, and ids only** — a retrieved fact's text, a
working-memory summary's text, or the prompt itself is never an attribute, matching the semconv
default (§3) and the existing no-content posture (Headline 2/§1). `tenant_id`/`user_id` attributes are fine
and already present both on spans (`otel.go:86-87,245-249`) and in structured logs
(`stream_core.go:177-184`).

### Honest prediction, not a target

For the observability mode's actual request shape (tool-less `App.Turn`/`ChatCore`, nil semantic tier
per §1's product-detail note): `invoke_agent` + 2× `search_memory` + 2× `create_memory` + `agent.run`
+ `agent.turn` + `model.request` + `chat {model}` + `output.validation` ≈ **9–10 spans**. A
`retrieval` span would only appear once the nil-semantic gap on the non-streaming path
(`chat.go:274`) is closed, which is a separate product decision, not an observability one. Every span
in this count corresponds to a real semconv operation or an actual pipeline stage that already
executes today — **none of it is decorative**, and this number is a prediction to be checked against
an actual implementation, not a target implementation should be shaped to hit.

## Could not establish

- Whether Langfuse or Phoenix maps the proposed `search_memory`/`retrieval` spans to their own
  dedicated "observation type" concepts, or would render them as plain nested spans — worst case is
  the latter, which is still correct, just less pretty in that vendor's UI.
- The final resolution of `gen_ai.system` vs. `gen_ai.provider.name` — the spec is in active
  Development status as of July 2026, so this is expected to keep moving.
- Exhaustive per-span attribute sets for mastra and genkit-go's own instrumentation — only span
  counts and taxonomy were verified, not every attribute each competitor emits.
- Whether the exporter-wrapper redaction approach (implemented) or the upstream gollem option
  (proposed, not implemented) is the right long-term home for this fix — both work; the tradeoff is
  "fixed now, local" vs. "fixed at the source, cross-repo, needs an upstream PR."
- Why eino's own arena observability cell reported 0 spans — out of scope for this document, which is
  about aisotropy's own design, not a competitor post-mortem.
- The stability timeline for the OTel GenAI semantic conventions generally — all relevant sections are
  Development status as of this writing, so anything cited from them may change before it stabilizes.

## Cross-links

- `docs/architecture/observability.md` — the existing streaming-path OTel design (`WithOTel`, the
  North Star bits/token metric) this document extends rather than replaces.
- `docs/decisions/0010-gollem-adoption-scope-and-observability.md` — the ADR establishing the
  two-call-path, two-instrumentation-strategy split this document's Headline 1 traces back to.
- `docs/architecture/dependency-reuse-first.md` and `.claude/rules/dependency-reuse-first.md` — the
  gollem-first discipline behind §2's "what gollem provides for free" and the additive-only design in
  §4.
- `docs/architecture/arena-adapter-artifacts.md` — the Q0 discipline (core vs. adapter vs. doc) this
  document's Headline 1 applies to a benchmark-board finding before drawing any product conclusion
  from it.
- `docs/architecture/arena-deterministic-judging.md` — a related but distinct discipline: that
  document guards the arena's SCORER against non-determinism; this document is about what the arena's
  observability MODE actually measures.
- `docs/architecture/multi-tenancy.md` — the `tenant_id`/`user_id` ctx propagation this document's
  span attributes rely on without re-deriving identity.
