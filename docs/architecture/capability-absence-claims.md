---
type: Concept
title: "Capability-Absence Claims — ecosystem sweep required (a standing PREMISE)"
description: >
  A claim that a competitor lacks a capability requires an ecosystem-level sweep, not core-source reading alone.
tags: [architecture, premise, competitive-claims, evidence, methodology]
timestamp: 2026-08-03T00:00:00Z
---

# Capability-Absence Claims — ecosystem sweep required (a standing PREMISE)

> **Origin (the mistake this prevents):** a public doc claimed the seven competitor frameworks
> "have no concept of a tenant," based on reading their CORES during a memory forensic. The core
> reading was accurate — but the claim generalized past its evidence: nobody had swept their
> ECOSYSTEMS (extensions, plugins, platform offerings, official docs). At least two counter-signals
> existed in our own adapters' code the whole time: mastra's Memory scopes by `resource`/`thread`
> (our adapter passes those parameters), and langgraph's `BaseStore` has hierarchical namespaces.
> Same failure class as the memory-suite claim ("the suite doesn't measure what our memory uniquely
> does") made before checking whether competitors had memory extensions (mem0/Zep/langmem/etc.).
> Asserting a competitor LACKS something, in public, on core-only evidence, is the outward-facing
> twin of the prune-evidence miss — and worse, because it mislabels someone else's product.

## The rule (always active for comparative/competitive claims)

**A claim that a framework/tool/competitor LACKS a capability ("X has no Y", "only we do Z",
"they can't do W") requires an ECOSYSTEM-LEVEL sweep, not core-source reading alone.** Core
reading proves what the core enforces; absence claims require checking, with a URL or file:line
per finding:

1. **Core** — is it in the framework itself?
2. **Official extensions/packages** — first-party packages outside core (langmem, memory blocks,
   @ai-sdk/* families, eino-ext, ...).
3. **Platform offerings** — the vendor's hosted tier often adds what the OSS core lacks
   (LangGraph Platform auth, LlamaCloud, Firebase for genkit).
4. **Official third-party integrations** — documented integrations with services (mem0, Zep,
   Letta), noting license and self-hostable vs cloud-only (a cloud API is not apples-to-apples
   with in-process).
5. **The vendor's own docs** — a "multi-tenancy"/"memory"/"observability" guide prescribing a
   pattern counts as an answer (label it: prescribed-pattern, not primitive).

Label maturity per finding: NATIVE / OFFICIAL-EXTENSION / OFFICIAL-THIRD-PARTY-INTEGRATION /
COMMUNITY-ONLY / NONE — with last-release date (an abandoned extension does not count as "has").

## The enforcement ladder (for "we guarantee it, they don't" claims)

"Has multi-tenancy" (or auth, or isolation, or quota) is too coarse. Classify on L0–L3:

- **L0 — no concept**: nothing in the framework addresses it.
- **L1 — parameter/convention**: a first-class identifier partitions data IF passed correctly on
  every call; omitting it silently reads another scope. (The padlock.)
- **L2 — enforced at a boundary**: identity validated/bound once; downstream cannot escape it
  without deliberate effort.
- **L3 — enforced by construction**: no code path reaches the data without the scope;
  cross-scope access is unrepresentable in the API. (The safe.)

Conflating L1 with L0 ("no concept") overstates the gap; conflating L1 with L3 ("has it")
understates it. Name the level.

## Symmetric rigor (non-negotiable)

Every standard applied to a competitor's claim applies to OUR OWN with the same severity, in the
same document. If we claim L3, stress-test the edges (e.g. "given the identity, downstream is
scoped" is NOT "the identity cannot be wrong" — NoAuth mode, embedder-supplied tenant strings).
A comparison doc that audits others leniently toward itself is marketing, not engineering.

## Relationship to sibling rules

- `prune-evidence-gate.md` — deletion claims need executable evidence (inward-facing code-truth).
- `dependency-reuse-first.md` — "our dependency has/lacks X" needs source citation (gollem-first).
- **This rule** — "their product lacks X" needs the 5-point ecosystem sweep + maturity labels +
  the L0–L3 ladder + symmetric rigor. It is the outward-facing member of the family.

## Contextual trigger

Always active when writing or reviewing: competitive comparisons, arena RESULTS/docs claims about
other frameworks, "unique to us" statements, marketing-adjacent architecture docs, and any
sentence of the form "framework X cannot / has no / lacks". Before such a sentence ships, the
sweep exists or the sentence is rewritten to state exactly what WAS checked ("the core does not
enforce...", which is narrower and true).
