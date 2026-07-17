# The full "Lang*" suite (LangChain ecosystem) — map + how the arena & aisotropy relate

**Date:** 2026-07-17
**Why:** the live arena tested a single `langgraph` adapter. That is ONE piece of a much larger
ecosystem. This maps the whole suite so the arena result ("langgraph, one agent framework") is read
in context, and so the aisotropy comparison is honest about what it does and does not match.

## The stack, in one line

**Build** (LangChain / LCEL) → **Orchestrate** (LangGraph) → **Observe / Evaluate / Deploy**
(LangSmith + LangGraph Platform). That is the LangChain product thesis: not one library, a full
agent-engineering stack.

## Open-source (MIT) — the frameworks

| Component | What it is |
|---|---|
| **langchain-core** | Base abstractions — the `Runnable` interface, prompts, messages, the LLM/embeddings interfaces. The foundation every other package builds on. |
| **langchain** | The framework layer — chains, agents, retrievers. "The agent engineering platform." |
| **langchain-community** | Connectors — vector stores, retrievers, tools, document loaders (moved out of `langchain`). |
| **Integration packages** | `langchain-openai`, `langchain-anthropic`, `langchain-qdrant`, … — provider-specific implementations (install only what you need). |
| **LCEL** (LangChain Expression Language) | Declarative pipe-operator composition: `prompt \| llm \| output_parser`. The modern way to build chains. |
| **LangGraph** | Stateful, cyclical, multi-agent orchestration — nodes/edges/shared-state, cycles, conditional branching, parallelism, **checkpointing** (pause/resume). The 2025–2026 de-facto agent runtime. **This is what the arena's `langgraph` adapter exercises.** MIT. |
| **Deep Agents** | Newer OSS abstraction for long-running "deep" agents, built on LangGraph. |

## Commercial / platform (free dev tier → paid → Enterprise)

| Product | What it is |
|---|---|
| **LangSmith** | The observability + **tracing + evaluation + prompt management + monitoring** PLATFORM you point LangChain/LangGraph at. Built on LangChain's callback system. In 2026 it expanded into a full "agent operations" stack — **LangSmith Fleet** (deployment; formerly Agent Builder), a unified cross-workflow cost view, AWS Marketplace, and **end-to-end native OpenTelemetry** (emit + ingest). This is the richest native observability of any framework in the arena. |
| **LangGraph Platform** | Commercial deployment/hosting — turns a LangGraph app into production APIs/assistants. Priced by "nodes executed" + deployment minutes + seats. Free "Developer" lite tier (~100k nodes/mo); **full self-host/BYOC only in Enterprise**. |
| **LangGraph Studio** | Visual IDE / step-through debugger for LangGraph agents (part of the platform). |
| **LangServe** | Older "deploy a chain as a REST API" library — being superseded by LangGraph Platform. |

## Adjacent (NOT LangChain, Inc. — often confused as part of the suite)

- **LangFlow** — a visual low-code agent builder, a **separate project** (DataStax/IBM lineage), not a LangChain-Inc. product. Frequently lumped in.

## How the ARENA relates

The arena's `langgraph` adapter is the **OSS LangGraph runtime** (the agent-orchestration tier) — the
correct representative for a runtime-vs-runtime comparison. But it is **one tier of the stack**:
- The arena's **observability** dimension measured in-process OTel emission and (correctly) found the
  vanilla langgraph package emits none without a callback→OTel bridge — yet LangChain's *native*
  observability is **LangSmith**, a whole platform, which the arena did not exercise (it is a hosted
  product, not an in-process library). See `docs/research/openai-compat-boundary...` sibling note and
  the engram `discovery/arena-observability-native-platforms`.
- The arena does **not** test LCEL/`langchain` (the build tier) or LangGraph Platform (the deploy
  tier) — those are out of scope for a "run one agent turn" comparison, but they are why LangChain is a
  *stack*, not a library.

## What the arena's `langgraph` adapter actually uses (and what it deliberately does not)

The `langgraph` entrant is **one competing framework**, but it pulls in **five** LangChain-ecosystem
packages (all MIT — they pass aisotropy's license gate), each doing one job:

| Package | Version | Role in the adapter |
|---|---|---|
| `langgraph` | 1.2.9 | the runtime — `StateGraph`, `ToolNode`, `create_react_agent` (the agent loop the arena scores) |
| `langchain-core` | 1.4.9 | base abstractions — `Runnable`, messages, and the **callback system** bridged to OTel in the observability mode |
| `langchain-openai` | 1.3.5 | `ChatOpenAI` pointed at agnes (Chat Completions) |
| `langchain-qdrant` | 1.1.0 | `QdrantVectorStore` retriever for the `rag` mode |
| `langgraph-checkpoint-sqlite` | 3.1.0 | the `SqliteSaver` checkpointer backing `memory` + `crash_recovery` |

**Why the rest of the Lang\* suite is out of scope (by design, not omission):**

- **LangSmith** — a hosted SaaS **observability/eval platform**, not an in-process library. The arena
  contract is a stdin→stdout adapter that makes one real model call locally; LangSmith is a *backend
  you ship traces to* (account/API key, data leaves to LangChain cloud or a self-hosted server). It is
  a different product category, cannot be an apples-to-apples "run one agent turn" adapter, and wiring
  it would export arena data to an external service. The arena instead measures **in-process OTel
  emission** (which LangGraph needs a callback bridge for) and documents that LangSmith is LangChain's
  real native observability story — see `discovery/arena-observability-native-platforms`.
- **LangGraph Platform / LangGraph Studio** — the commercial **deploy/host + visual-IDE** tier. The
  arena tests the *runtime* (OSS LangGraph), not a managed deployment control plane; full self-host is
  Enterprise-gated anyway.
- **LangServe** — deprecated (superseded by LangGraph Platform); a "deploy a chain as REST" wrapper — a
  deployment concern, not a runtime one.
- **Deep Agents** — an OSS abstraction *on top of* LangGraph. Testing LangGraph (the base) is the fair
  representative; Deep Agents is a specific agent pattern, not a distinct framework.
- **LangFlow** — **not a LangChain, Inc. product** (separate DataStax/IBM project); a visual low-code
  builder, a different category entirely.

Net: the arena exercises the LangChain **OSS framework/runtime tier** (5 packages, one entrant) fairly,
and deliberately excludes the **hosted platform tier** (LangSmith / LangGraph Platform) because those
are SaaS observability/deploy products, not in-process agent runtimes — a different comparison that
would need its own methodology (and would send data off-box).

## How AISOTROPY relates (honest)

aisotropy competes at the **LangGraph runtime tier** — the agent loop, tiered memory, guardrails,
scope-gated tools — and there it has genuine differentiators the OSS LangGraph lacks: **multi-tenant
isolation by construction** (ctx + key-prefix + Qdrant in-scan filter), **Dual-LLM quarantine** and a
**broker-invariant sandbox** built in (arena-proven). What aisotropy does **not** have, as *products*:

| LangChain stack tier | aisotropy equivalent |
|---|---|
| Build (LCEL / langchain) | Partial — the façade builder + typed tools, but not a general chain-composition DSL |
| Orchestrate (LangGraph) | **Yes (runtime)** — single-agent loop + fan-out; multi-agent GRAPH is designed (ADR-0060), not yet built |
| Observe/Eval (LangSmith) | **Primitives, not a platform** — OTel traces (`internal/llm/otel.go`) + audit hash-chain + the eval harness (`internal/eval`); no hosted tracing/eval/prompt-management UI |
| Deploy (LangGraph Platform) | Partial — a single self-hostable Go binary + docker-compose + observability stack; no managed multi-tenant control plane |

**Bottom line:** aisotropy is a strong *runtime* with security/isolation advantages, but the LangChain
value is the **whole stack** (build → orchestrate → observe → deploy) plus a mature managed platform
(LangSmith + LangGraph Platform). A fair pitch positions aisotropy as the **sovereign, multi-tenant,
security-first runtime tier**, not a LangSmith/LangGraph-Platform replacement — those are a hosted
observability/deployment product category aisotropy has building blocks for but no product.

## Sources

- [LangGraph — agent orchestration](https://www.langchain.com/langgraph) · [LangGraph docs](https://docs.langchain.com/oss/python/langgraph/overview)
- [LangChain vs LangGraph vs LangSmith vs LangFlow (DataCamp)](https://www.datacamp.com/tutorial/langchain-vs-langgraph-vs-langsmith-vs-langflow)
- [langchain-core / community architecture](https://www.langchain.com/blog/the-new-langchain-architecture-langchain-core-v0-1-langchain-community-and-a-path-to-langchain-v0-1) · [langchain-core reference](https://reference.langchain.com/python/langchain-core)
- [LangGraph Platform pricing (ZenML)](https://www.zenml.io/blog/langgraph-pricing)
- [LangSmith end-to-end OpenTelemetry](https://www.langchain.com/blog/end-to-end-opentelemetry-langsmith)
- [The complete LangChain ecosystem (GeeksforGeeks)](https://www.geeksforgeeks.org/artificial-intelligence/the-complete-langchain-ecosystem/)
