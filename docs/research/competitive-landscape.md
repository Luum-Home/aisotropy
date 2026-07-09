---
type: Research Report
title: "Competitive landscape — agent frameworks vs. aisotropy (mid-2026)"
description: An architect-level scan of LangChain/LangGraph, Mastra, Letta, Eino, ADK Go and Genkit Go, judging whether aisotropy's combination (Go-native, tenant-isolation-by-construction, temperature-tiered memory, pre-spend cost governance, 100% MIT/Apache) solves a genuinely unaddressed problem.
tags: [research, competitive, landscape, langchain, langgraph, mastra, letta, eino, adk, genkit, go, memory, multi-tenancy]
timestamp: 2026-07-09T00:00:00Z
---

# Competitive landscape — agent frameworks vs. aisotropy (mid-2026)

> **One line:** aisotropy's bet is defensible as a *combination*, not as any single
> feature. Every individual piece has at least one incumbent attacking it; no incumbent
> covers even three of aisotropy's five defining dimensions together.
> **As of:** 2026-07-09. Claims carry inline sources + dates; thin/conflicting evidence is
> flagged **UNCERTAIN** and should be re-verified before being load-bearing.

This report is the written form of a two-track investigation (a repo deep-read plus a
mid-2026 landscape scan). It exists so the positioning judgment lives in the repo, not only
in session memory. It is a snapshot: agent frameworks move weekly, so treat the dated facts
as perishable.

## The niche aisotropy competes for

aisotropy is not "another agent framework." Its thesis is a specific *combination* (see
[vision/thesis.md](../vision/thesis.md), [architecture/overview.md](../architecture/overview.md)):

1. **Tenant isolation by construction** across every memory tier — not a namespace
   convention a developer must remember.
2. **Temperature-tiered memory** with offline consolidation — working (BadgerDB) →
   facts/FTS5 (rqlite) → semantic vectors (Qdrant) → sleep-cycle batch (DataFusion).
3. **Pre-spend cost governance** — reject/degrade before the LLM call, not accounting after.
4. **Memory as governed, deletable data** — retention/deletion as a first-class concern.
5. **All of the above in Go, in-process latency class**, on a 100% MIT/Apache stack.

The test for "does aisotropy solve a real problem" is whether any incumbent covers these
five together. As of mid-2026, none covers even three.

## The LangChain suite (Python/JS)

- **Versions:** LangChain 1.0 + LangGraph 1.0 shipped late Oct 2025 after three years of v0.x
  churn ([langchain.com/blog/langchain-langgraph-1dot0](https://www.langchain.com/blog/langchain-langgraph-1dot0)).
  OSS core is MIT.
- **Long-term memory:** LangGraph checkpointers + Store + the **LangMem** SDK. LangMem looks
  stagnant as a package (latest PyPI 0.0.30, Oct 27 2025) despite active repo commits
  ([langmem docs](https://langchain-ai.github.io/langmem/)). Legacy
  `ConversationBufferMemory` classes were removed at 1.0.
- **Multi-tenancy:** namespacing convention only (`user_id` in the Store/LangMem namespace).
  No enforced isolation primitive — "don't forget the namespace," not context-propagated.
- **Token economy:** 1.0's answer is **middleware** (summarization, trimming, dynamic tool
  selection) — manual, not a runtime-enforced budget.
- **Self-hosting / pricing:** OSS runtime is MIT and self-hostable, but the **platform**
  (durable execution, task queues, assistants) is the canonical open-core squeeze:
  "Self-Hosted Lite" free to 1M node executions then paywalled; full self-host + self-hosted
  LangSmith are Enterprise/sales-gated ([ZenML analysis](https://www.zenml.io/blog/langgraph-pricing)).
- **Observability:** LangSmith is not OSS; trace-volume pricing; highest lock-in of the major
  tools per multiple 2026 comparisons ([SigNoz](https://signoz.io/comparisons/langsmith-alternatives/)).
  The market's escape hatch is **OTel gen_ai conventions + Langfuse** — exactly where
  aisotropy's ADR-0010 already points.
- **No Go story** (langchaingo is an unofficial community port, `tmc/langchaingo`, MIT).

## Mastra (TypeScript)

- **License:** core **Apache 2.0** — relicensed *from* Elastic License 2.0 after enterprise
  pushback ([mastra.ai/blog/apache-license](https://mastra.ai/blog/apache-license)). Enterprise
  features remain source-available (paid production). The ELv2→Apache reversal is empirical
  evidence that a restrictive license is untenable for a framework — a data point in favor of
  aisotropy's permissive-only stance.
- **Traction:** $22M Series A led by Spark Capital, announced Apr 9 2026 ($35M total), built by
  the Gatsby founders ([report](https://faq.com.tw/en/developer-tools/2026-04-10-mastra-22m-series-a-typescript-agents-en/)).
- **Memory:** the closest TS analog to aisotropy's working memory — working memory
  (resource- or thread-scoped), semantic recall (vector), and memory processors that trim
  retrieved messages before the context window ([memory docs](https://mastra.ai/docs/memory/overview)).
- **Multi-tenancy:** resource-scoping is per-user partitioning, not a runtime-enforced tenant
  boundary. TypeScript-only — no Go.

## Letta (MemGPT) — the closest conceptual neighbor

- **License:** Apache 2.0, self-hostable (Docker/Postgres).
- **Memory hierarchy** is explicitly OS-inspired, nearly isomorphic to aisotropy's framing:
  **Core Memory** (in-context editable blocks; RAM) → **Recall Memory** (out-of-context
  searchable history; disk) → **Archival Memory** (unbounded vector store; cold)
  ([letta.com](https://www.letta.com/)).
- **Sleep-time compute:** a paired background agent consolidates memory during idle time — the
  direct analog of aisotropy's DataFusion sleep cycle
  ([letta blog](https://www.letta.com/blog/sleep-time-compute/), 2025).
- **Differences vs aisotropy:** Python; agents are heavyweight durable server objects; memory
  is **self-managed by the LLM via tool calls** rather than runtime-hydrated (aisotropy's is
  deterministic and auditable); multi-tenancy is app-level (identities/orgs), not
  context-propagated with payload-filtered vector scans; no Go, no facts-tier FTS journal, no
  in-process µs working memory.
- **Takeaway:** Letta *validates the memory-OS thesis* while leaving aisotropy's exact
  combination (Go, isolation-by-construction, runtime-hydrated deterministic memory) open.

## The Go-native threat: Eino and ADK Go

These two are the only serious Go-native competitors. Both iterate fast with big-corp
resources; neither has a tiered-memory or multi-tenancy story today.

### Eino (ByteDance / CloudWeGo) — threat #1

- **Stack:** `github.com/cloudwego/eino`, Apache 2.0. Part of **CloudWeGo** alongside Kitex
  (RPC) and Hertz (HTTP) — it slots into ByteDance's Go microservices stack. Model providers
  via eino-ext (OpenAI, Claude, Gemini, Ark/Volcengine, DeepSeek, Qwen, Ollama).
- **Velocity:** ~35 tagged releases in the 7 weeks 2026-05-19 → 2026-07-07, across a stable
  `v0.9.x` line and a `v0.10.0-alpha` line — roughly one release every 1–2 days
  (api.github.com/repos/cloudwego/eino/releases, fetched 2026-07-08). No stated v1.0 timeline;
  0.x with occasional flagged breaking changes.
- **Team/backing:** small concentrated core (~4–7 active committers), ByteDance-affiliated
  (some `-bytedance`-suffixed contributors; security reported via ByteDance Security). 12.2k
  stars — more than Kitex/Hertz. **Coze Studio's runtime depends on Eino** (verified in its
  `go.mod`); CloudWeGo docs say Doubao/TikTok teams shaped its design.
- **Memory (more developed than assumed):** its ADK subsystem has mature checkpoint/resume,
  shipped session persistence (Jun 2026), and a just-landed **AutoMemory** middleware
  (v0.10.0-alpha.5, 2026-06-16). But it is **process-local / event-log**, not a
  temperature-tiered hierarchy. The vector/retrieval-backed memory middleware
  (issue #995, opened 2026-04-24) is **proposed, not implemented**.
- **Multi-tenancy: none.** A repo-wide search for "multi-tenant" returns **0 results, ever**
  (api.github.com/search/issues, fetched 2026-07-08).
- **Structured output:** provider JSON mode + a parser (`schema.NewMessageJSONParser`); **no
  documented fail-closed schema-violation reflection loop**.
- **Cost:** token-usage tracking via callbacks (Dec 2025), post-hoc; no pre-spend quota
  primitive.
- **Observability:** eino-ext callbacks target APMPlus (ByteDance/Volcengine), CozeLoop,
  Langfuse, LangSmith. **No native OTel gen_ai semconv** directory found. **UNCERTAIN** whether
  APMPlus emits spec-compliant `gen_ai.*` attributes or a proprietary schema over the OTel
  wire.

### ADK Go (Google) — threat #2

- **Stack:** `google.golang.org/adk` (v2 → `google.golang.org/adk/v2`, requires Go 1.25+),
  Apache 2.0. Not a web framework — an embeddable agent runtime. Models via
  `google.golang.org/genai` (Gemini-optimized) plus OpenAI-compatible providers.
- **ADK Go 2.0 (2026-06-30)** — **verified real**
  ([announcing-adk-go-20](https://developers.googleblog.com/announcing-adk-go-20/),
  release tag v2.0.0): a graph-based orchestration engine (concurrent scheduler, conditional
  routing, fan-out/fan-in, per-node retries/timeouts, input/output schema validation,
  human-in-the-loop pause/resume). "Same graph-first direction as Python ADK 2.0."
- **Cadence/parity:** ~monthly minors through v1.4.0, then v2.0.0. **Go trails Python by ~6
  weeks** (ADK Python 2.0 GA 2026-05-19; Python already at v2.4.0 on 2026-07-07 while Go sits
  at v2.0.0). ~56 contributors, several Google-identifiable.
- **Memory:** Session/State/Memory services; the rich backends (VertexAI Session Service,
  **VertexAI MemoryBank** added in ADK Go v1.3.0, RAG memory) are **Vertex-tethered**. Context
  compaction is "on our roadmap" with no committed date (maintainer reply, discussion #589,
  2026-05-21).
- **Multi-tenancy:** nothing beyond app+user+session scoping in the v0.2→v2.0 history
  (**UNCERTAIN**, absence-of-evidence).
- **Deployment:** standalone CLI (`adkgo`), Cloud Run, Agent Engine, or any container —
  genuinely self-hostable, but the whole feature gradient steers toward GCP ("works standalone,
  works best on Vertex").
- **Protocols/observability:** **A2A first-class** (Google donated A2A to the Linux Foundation
  2025-06-23; vendor-neutral by mid-2026), MCP (`mcptoolset`), and **OTel gen_ai semconv**
  (three releases cite semconv work). On observability, ADK *matches* aisotropy's ADR-0010 —
  aisotropy does not differentiate here against ADK, only against Eino/Genkit.

### Genkit Go (Google) — threat #3 (complement, not a direct competitor)

- **Stack:** `github.com/firebase/genkit/go`, Apache 2.0 (also TS/Python/Dart SDKs). Go SDK is
  1.0 GA (2025-09-10) with a Go-style stability promise; latest `go/v1.9.0` (2026-06-16).
- **Positioning:** Google itself frames Genkit as a general-purpose **application library** and
  steers "complex agent topologies" to ADK
  ([build-agentic-full-stack-apps-with-genkit](https://developers.googleblog.com/build-agentic-full-stack-apps-with-genkit/),
  2026-07-01). Its Agents API is in **preview**.
- **Durability:** not native — delivered via the third-party `genkitx-temporal` plugin.
- **Protocols:** **MCP native** (client + server, `go/plugins/mcp`); A2A only community-sample.
- **Observability:** OTel *transport* but a **proprietary attribute schema** (`genkit:metadata:*`,
  `genkit:init`), **not** the `gen_ai.*` semantic conventions (verified against
  pkg.go.dev/…/core/tracing). aisotropy's ADR-0010 is aligned with the market *and* ahead of
  Genkit here.

### Other players (brief)

| Framework | Language | License | Note |
|---|---|---|---|
| Vercel AI SDK 6 | TS | Apache 2.0 | 20M+ monthly downloads; memory is BYO |
| LlamaIndex | Python/TS | MIT | Composable memory blocks (static/fact-extraction/vector) under a token budget |
| Zep / Graphiti | Python | Apache 2.0 (Graphiti) | Temporal knowledge graph; facts with validity windows; Zep = commercial platform on top |
| Mem0 | Python | Apache 2.0 | `add()/search()` memory API; server mode for per-user isolation |
| CrewAI | Python | MIT | Large adoption; reliability-at-concurrency criticism (UNCERTAIN figures) |
| Microsoft Agent Framework | .NET/Python | MIT | 1.0 GA Apr 2026; AutoGen + Semantic Kernel converged (both now maintenance) |
| OpenAI Agents SDK / AgentKit | Python/TS | MIT (SDK) | Runtime-tethered to OpenAI; Agent Builder/Evals wound down (announced 2026-06-03) — platform-risk exemplar |
| Claude Agent SDK | Python/TS | MIT code | Memory tool + context editing; usage governed by Anthropic ToS |

## Time-to-threat by capability (the core judgment)

| Capability (aisotropy's niche) | Eino | ADK Go | Verdict |
|---|---|---|---|
| Tiered / long-term memory | ~6–12 months (AutoMemory shipped; #995 proposed) | Has it, but Vertex-tethered; self-hosted parity unlikely by business model | **The real clock is Eino's** |
| Enforced multi-tenant isolation | No signal (0 issues ever) | No signal | **Open lane — nobody is building it** |
| Pre-spend cost governance | Post-hoc usage only | Not found | **Open lane** |
| Fail-closed structured output | Parser only, no repair loop | Node retries in 2.0; no fail-closed evidence | Differentiator, weaker vs ADK 2.0 |
| OTel gen_ai semconv | No | **Yes** | Differentiates vs Eino/Genkit; ties ADK |

## What is genuinely unsolved (mid-2026)

Across the field, practitioners report the same gaps
([O'Reilly AI Agents Stack 2026](https://www.oreilly.com/radar/the-ai-agents-stack-2026-edition/),
[Fountain City survey](https://fountaincity.tech/resources/blog/how-to-build-and-operate-ai-agent-memory-in-2026/)):

- **Memory lifecycle** (consolidation, promotion/demotion, forgetting) is the least-solved
  dimension; only Letta productized sleep-time consolidation.
- **Multi-tenant isolation is a retrofit everywhere** — convention, not construction. This is
  aisotropy's strongest genuinely-unaddressed claim.
- **Pre-spend cost governance** as a framework primitive is essentially absent.
- **Memory as governed/deletable data** (retention, GDPR/HIPAA) is an open gap.
- Research is *converging on aisotropy's bets* (surprisal-gated storage à la Google Titans;
  sleep-inspired consolidation) but only Letta has productized any of it.

## Licensing / pricing pressure points

- **LangGraph/LangSmith:** OSS runtime MIT, but durable-execution platform + self-hosted
  LangSmith are Enterprise-gated — the canonical open-core squeeze.
- **Mastra:** core Apache 2.0 (relicensed *from* ELv2); enterprise features source-available.
- **Letta, Mem0, Graphiti, Eino, Genkit, ADK Go, Vercel AI SDK, CrewAI core, MS Agent
  Framework:** all Apache 2.0 or MIT, genuinely self-hostable.
- **OpenAI Agents SDK / Claude Agent SDK:** permissive code, but runtime-tethered to the
  vendor's API/ToS — not self-hosting in any meaningful sense.
- **Durable-execution substrates** (Temporal MIT server but ops-heavy; Inngest/Restate
  cloud-oriented; Restate server license **UNCERTAIN** — verify current terms).

**Bottom line:** aisotropy's 100% MIT/Apache stack is a real adoption advantage, not ideology —
Mastra's forced ELv2→Apache reversal is the market proof.

## Positioning conclusion for aisotropy

1. **The combination is the moat, not any single feature.** Tenant-isolation-by-construction +
   temperature-tiered memory + pre-spend quota + Go in-process + permissive licensing has
   exactly one conceptual neighbor (Letta: Python, LLM-self-managed memory, app-level tenancy)
   and two Go-native competitors (Eino, ADK Go) with **no** memory-hierarchy or tenancy story.
2. **The memory window is ~6–12 months and Eino sets the clock** — not Google. AutoMemory +
   issue #995 point straight at it. This argues for accelerating aisotropy's facts/semantic
   tiers (Phases 3–4), which is exactly the perishable part of the moat.
3. **Correction to an earlier internal claim:** it is wrong to say "neither Eino nor ADK has a
   memory story." Eino already has serious *session* memory (checkpoint/session/AutoMemory).
   What no one has is *hierarchical, multi-tenant* memory with offline consolidation — that is
   what aisotropy must market, not "we have memory."
4. **Observability differentiates vs Eino/Genkit, not vs ADK.** ADK is OTel gen_ai semconv
   compliant; aisotropy ties it there and only wins against Eino (proprietary APM) and Genkit
   (proprietary `genkit:*` schema).
5. **Sovereignty cuts both ways in aisotropy's favor:** Western enterprises are wary of
   ByteDance ownership (Eino), and ADK carries a GCP gradient. A Go-native, self-hosted,
   permissively-licensed memory-OS sits in the gap between them.

## Sources & caveats

Every dated claim above cites a primary or clearly-labeled secondary source inline. Items
marked **UNCERTAIN** (Eino's Feb–Aug 2025 release-tag gap; APMPlus gen_ai semconv compliance;
ADK Go's Go-native VertexAI RAG memory equivalent; exact per-project engineering headcounts;
Restate's current server license; whether Genkit's Agents API preview covers Python/Dart) rest
on thin or conflicting evidence and must be re-verified before being used as load-bearing
facts. This landscape is a 2026-07-09 snapshot; agent frameworks change weekly.
