---
type: Feature matrix
title: "Eino — features vs aisotropy (REAL integration)"
description: Coverage map of the real CloudWeGo Eino v0.9.12 framework against the 11-point Orbit checklist, with coverage classes.
tags: [arenas, eino, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Eino — Features (verified against v0.9.12)

Coverage classes: **BUILT-IN** (framework ships it, works out of the box) · **CONVENTION** (framework
gives a primitive but scoping/correctness is the developer's responsibility) · **DIY** (you must build
it yourself) · **PAID** (only via a paid/proprietary product) · **NOT-POSSIBLE** (framework cannot
provide it by construction).

Everything below was checked against the real source in the module cache
(`github.com/cloudwego/eino@v0.9.12`) and exercised where marked "exercised in smoke/tests".

## 1. Multi-tenant isolation — **NOT-POSSIBLE** (by framework)

- **Real API:** `compose.CheckPointStore { Get(ctx, id string) ([]byte,bool,error); Set(ctx, id string, []byte) error }` + `compose.WithCheckPointID(string)`.
- **Finding:** the persistence key is a single developer-supplied string. No tenant/user dimension
  exists anywhere in the interface, and there is **no default implementation**. Per-node state
  (`WithGenLocalState`) carries only the fields the developer declares.
- **Proof (exercised):** `orbit/memory.go` builds a real interrupt/checkpoint graph; `TestIsolationSharedIDLeaks`
  seeds tenant A under `"shared-session"` and shows tenant B resume the same ID and receive tenant
  A's secret — driven by Eino's real runtime.
- **AISOTROPY:** isolation by construction (ctx propagation + per-statement/key scoping). The leaky
  path is unrepresentable.

## 2. Structured output + retry — **DIY / partial**

- **Real API:** `schema.NewMessageJSONParser` / provider JSON mode; `react.AgentConfig` has no
  fail-closed schema-repair loop.
- **Finding:** you can parse structured output, but there is no built-in "schema violation → bounded
  reflection retry" loop. That repair loop is DIY.
- **AISOTROPY:** default/runtime-wired fail-closed reflection loop (Gollem + homegrown fallback).

## 3. Tool calling — **BUILT-IN**

- **Real API:** `components/tool.InvokableTool`, `components/tool/utils.InferTool[Req,Resp]`,
  `compose.ToolsNodeConfig`, `flow/agent/react.NewAgent`.
- **Proof (exercised):** `orbit/orbit.go` registers a real `lookup_order` tool via `InferTool` and
  runs it through `react.NewAgent`. `TestOrbitAgentTool` asserts the tool result is folded into the
  final answer. Type-safe: the request struct is reflected into a `schema.ToolInfo`.
- **AISOTROPY:** equivalent to `WithTypedTool[T]`.

## 4. Streaming — **BUILT-IN**

- **Real API:** `model.BaseChatModel.Stream(...) (*schema.StreamReader[*schema.Message], error)`;
  the compiled `compose.Runnable` also exposes `Stream()`.
- **Proof (exercised):** `orbit.Stream()` iterates a real `*schema.StreamReader`; `TestOrbitStream`
  asserts multiple chunks. The HTTP server exposes `/chat/stream`.
- **AISOTROPY:** equivalent to Huma SSE + `ctx` cancellation.

## 5. Working memory — **CONVENTION**

- **Real API:** `compose.CheckPointStore` + `WithCheckPointID` (state persisted at interrupts);
  AutoMemory middleware exists in eino-ext (event-log style).
- **Finding:** persistence works, but it is keyed by a bare developer string (see #1). Orbit also
  keeps an app-level `workingMemory map[string][]string` because the developer must own the keying.
- **AISOTROPY:** BadgerDB (µs) working-memory tier, tenant-scoped by construction.

## 6. Semantic memory (vectors) — **DIY**

- **Finding:** not in core. Retrieval/vector-backed memory middleware is proposed in issue #995
  (opened 2026-04-24), not implemented. You wire your own vector store.
- **AISOTROPY:** in-process cosine (active) + Qdrant gRPC (code-only), tenant-filtered during scan.

## 7. Facts memory (journal) — **DIY**

- **Finding:** no built-in facts/journal store. DIY on top of any DB.
- **AISOTROPY:** SQLite + FTS5 facts journal out of the box.

## 8. Cost governance — **DIY / partial**

- **Finding:** token usage is observable via callbacks *after* the call (post-hoc). There is no
  pre-spend quota gate primitive.
- **AISOTROPY:** pre-spend quota gate + per-tenant cost meter (reject/degrade before spending).

## 9. Observability — **DIY / PAID**

- **Finding:** integrations target APMPlus (ByteDance/Volcengine), CozeLoop, Langfuse, LangSmith.
  No native OTel `gen_ai.*` semconv emitter in core. Some targets are proprietary/paid SaaS.
- **AISOTROPY:** OTel gen_ai semconv compliant.

## 10. Sovereignty / licensing — Apache-2.0 (with caveat)

- **Finding:** Apache-2.0, self-hostable code. But the framework is ByteDance-operated and its
  primary observability target (APMPlus) is a ByteDance/Volcengine product — a data-residency /
  sovereignty caveat for Western regulated buyers.
- **AISOTROPY:** 100% MIT/Apache, Western-operated, fully self-hostable.

## 11. Language — Go

- Same language and runtime class as aisotropy. This makes Eino the most directly comparable
  framework; the architectural difference (orchestration library vs sovereign multi-tenant runtime)
  is the whole story.

## Summary

Eino is a genuinely capable **Go-native orchestration library** (LangGraph-for-Go) with real,
type-safe tool calling, streaming, and graph/checkpoint state — all of which this arena exercises
against the real v0.9.12 code. What it does **not** have is any tenancy story: its persistence key is
a bare string with no tenant dimension and no default implementation, so multi-tenant isolation is
**NOT-POSSIBLE by the framework** and must be bolted on by the developer (who can silently get it
wrong, as the smoke test proves).

**vs. aisotropy:** Eino wins on Go-native velocity and CloudWeGo ecosystem fit; aisotropy wins on
isolation-by-construction, temperature-tiered memory, pre-spend cost governance, and sovereignty.
