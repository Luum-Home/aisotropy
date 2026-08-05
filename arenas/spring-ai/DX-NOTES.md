---
type: Developer notes
title: "Spring AI — DX notes"
description: Honest enterprise-JVM developer-experience assessment of Spring AI 2.0.0 for a multi-tenant production support chatbot.
tags: [arenas, spring-ai, dx, developer-experience, jvm]
timestamp: 2026-07-16T00:00:00Z
---

# Spring AI — DX Notes

Assessed by actually building Orbit against Spring AI 2.0.0 (JDK 26, Maven 3.9.16) — compiling and
running the memory / advisor / structured-output pipeline offline with a keyless fake `ChatModel`.

## The good

### Spring-idiomatic from the first line
If your shop is already on Spring Boot, Spring AI is the lowest-friction entrant here. `ChatClient`
is a fluent builder; memory is an advisor you register once; tools are `@Tool`-annotated methods;
the model is a bean you swap by changing a dependency. Everything a Spring team already knows —
dependency injection, autoconfig, `application.yml`, Actuator, Micrometer, Spring Security — applies
directly. That is a real, underrated advantage: the surrounding governance you have to *build* on
this framework is exactly the surrounding governance a Spring team already builds well.

### Clean, well-typed core primitives
The APIs I exercised are tidy and honest about their contracts: `ChatMemory.get(conversationId)`,
`MessageWindowChatMemory.builder().maxMessages(n)`, `BeanOutputConverter<T>` with `getFormat()` /
`getJsonSchema()` / `convert()`. Structured output into a Java `record` worked first try. The window
memory's turn-aware eviction (retains `SystemMessage`, evicts whole user/assistant turns) is a
thoughtful default.

### Native observability + evaluation, in-framework
Micrometer `Observation` instrumentation ships in the box (`ChatClientObservationConvention`), and
there is a genuine `Evaluator` surface (`RelevancyEvaluator`, `FactCheckingEvaluator`) for
LLM-as-judge relevancy/faithfulness. Most arenas make you reach for a SaaS product or DIY these;
Spring AI keeps them local and OSS.

### Fully OSS, pure-permissive tree, no SaaS gate
The 34-dependency tree is **100% permissive** (Apache-2.0 / MIT / BSD / CC0) with **zero** copyleft,
zero MPL leaf, zero source-available packages — cleaner than the Python arenas' `certifi`-MPL leaf.
There is no paid/hosted tier anywhere in the framework: nothing is NOT-POSSIBLE or PAID. For a
regulated, self-hosted deployment, the license and sovereignty posture is excellent.

### The one place it can *build* atomicity the others can't
A JDBC-backed `ChatMemoryRepository` + a JDBC vector store on the same `DataSource` can share a
Spring `@Transactional` boundary — so an atomic "write the turn and its consolidation intent
together" is buildable in-process, which is structurally NOT-POSSIBLE in five of the seven arenas.
Spring AI won't hand you an idempotent outbox worker, but the JVM transaction model at least lets you
build one honestly.

## The painful

### `conversationId` has no framework-enforced identity binding — the isolation story is yours to construct
<!-- SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (Spring AI = L1: required,
     throwing, single-axis `conversationId`; identity binding is the developer's layer). -->
This is the crux, stated fairly. The memory contract is keyed by a `String conversationId`
(`ChatMemory.get(String)`) that the developer owns; nothing in the framework binds it to an
authenticated principal. That cuts both ways, and I ran both:

- **Naive convention (the bug).** If you let a caller supply/reuse the id, or fold four axes into a
  flat *unescaped* `"|"` join, you get the failures I reproduced: a wrong caller reads the wrong
  memory, a dropped/mis-passed product segment substitutes, and a delimiter forgery lets
  `user="alice|productX"` serialize to another identity's key. But this is a **developer
  key-composition bug**, not a Spring AI defect — any keyed store (aisotropy's own BadgerDB/rqlite/
  Qdrant included) behaves the same under an unauthorized key, and the collision is an artifact of a
  key *format* I chose. Note too that `findConversationIds()` is an **admin/maintenance surface**,
  not something a request handler should ever call with a foreign id.
- **Correct construction (the fix, one screen of code).** Derive the `conversationId` from the
  verified principal, read through a facade that recomputes it server-side, and use a
  structured/escaped key. In Part B of both demos the cross-tenant and cross-product reads become
  structurally impossible and a smuggled foreign id is refused. Spring Security producing the
  principal makes this the *most* idiomatic DIY of any arena.

So the honest, narrower point is not "it leaks" but: **isolation here is a convention you construct,
not a default the framework guarantees.** aisotropy ships the equivalent of Part B by default as an
unforgeable `Scope`; Spring AI gives you excellent rails to build it and lets you skip them.

### One memory nature, not a hierarchy
`ChatMemory` is a message window; `VectorStore` is a separate module. There is no full-text/facts
tier and no offline consolidation. For a memory-OS-shaped product (facts journal + vectors +
sleep-cycle), you are assembling and scoping several unrelated pieces yourself.

### No governance rails
No pre-spend quota gate, no per-tenant cost meter, no circuit breaker, no pressure degradation. Usage
tokens are visible per call (`ChatResponse.getMetadata().getUsage()`), but turning that into a
per-tenant ledger and a *reject-before-spend* gate is all your code. For a multi-tenant product where
a runaway tenant is a P1, that is the load-bearing gap.

### No fail-closed structured-output loop on 1.x
`BeanOutputConverter` throws on malformed JSON but does not re-request with the typing error. The
self-correcting retry only arrives in Spring AI 2.0. On 1.x you own the repair loop.

### JVM operational weight
A fat-jar + JRE is heavier to ship and slower to cold-start than a Go static binary. Fine for an
enterprise that already runs the JVM; a cost if you wanted aisotropy's megabyte-binary footprint.

### API line still moving
1.0 GA landed after a long milestone series, and 2.0 GA is already out with structured-output
changes. The core memory/advisor API used here is stable across both, but teams should expect the
edges (structured output, agents) to keep evolving.

## The verdict

Spring AI is the **enterprise-JVM play**: the most idiomatic on-ramp for a Spring shop, a clean typed
core, native observability and evaluation, a pristine permissive license, and — uniquely among the
arenas — a transaction model that lets you *build* atomic consolidation. But it treats multi-tenancy
exactly as the rest of the field does: `conversationId` is a string bound to nothing, memory is one
window nature, and there is no pre-spend governance. The saving grace is that everything you must
build sits in an ecosystem (Spring Security, Actuator, Micrometer, `@Transactional`) that a Spring
team builds well.

**Best for:** Spring/JVM enterprises that will own the runtime, want OSS + self-hosting with zero
SaaS lock-in, and are comfortable wiring isolation and cost governance themselves on familiar rails.

**Worst for:** Teams that want isolation and pre-spend cost control **guaranteed by the framework**
rather than assembled — a cross-tenant leak here is one mis-composed `conversationId` away.

**vs. aisotropy:** Spring AI wins on ecosystem idiom, native observability/eval, and OSS license
posture; it can even build atomic consolidation the Python arenas cannot. aisotropy wins on the three
structural axes — isolation-by-construction, tiered memory of different nature, and per-tenant
pre-spend governance — that Spring AI leaves to developer discipline. The trade: the JVM's richest,
most self-hostable agent SDK with do-it-yourself tenancy, vs. a sealed multi-tenant runtime where the
leaky read does not exist.
