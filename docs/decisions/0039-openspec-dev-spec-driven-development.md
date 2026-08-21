---
type: ADR
title: "ADR-0039 — OpenSpec (openspec.dev): a dev-time SDD methodology, deliberately deferred as a product/import surface"
description: Positions openspec.dev against aisotropy's standards posture (ADR-0022). OpenSpec is a dev-time spec-driven-development workflow for AI coding assistants (markdown openspec/specs + openspec/changes, MIT, ~61k stars) — it slots into ADR-0022's AGENTS.md (dev-time) layer, NOT the packaging/knowledge/runtime import surfaces that map to aisotropy's TYPED primitives. Decision: deliberately deferred on both axes (no product importer, no in-repo adoption of a second SDD tree) with concrete revisit triggers — formalizing the informal "conscious skip" already recorded in capabilities-and-gaps.md.
tags: [decisions, adr, interop, standards, openspec, sdd, spec-driven-development, dev-time, anti-magic, scope, defer]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0039 — OpenSpec (openspec.dev): dev-time SDD methodology, deferred as a product/import surface

## Status

✅ **Accepted (2026-07-17) — deferred.** Formalizes the informal "conscious skip" already recorded in
[`capabilities-and-gaps.md`](../architecture/capabilities-and-gaps.md) (row `openspec.dev — ❌ NOT
SUPPORTED`, §"Conscious skips (scope-creep unless real demand)") into a decision record with the
research grounding and an explicit revisit trigger. **No code is written.** Governed by the same
layer model as [ADR-0022](0022-agent-config-standards-import-surfaces.md) (agent-config standards as
import/packaging surfaces) and the anti-magic gate ([ADR-0002](0002-anti-framework-magic-struct-as-contract.md)),
and consistent with the [consolidation proposal](../architecture/consolidation-proposal.md)'s
"breadth outran depth — don't build ahead of demand" direction.

## Context

The operator asked whether and how aisotropy should support [openspec.dev](https://openspec.dev/).
Answering requires first pinning down *what OpenSpec actually is*, because "spec" is overloaded (API
spec / agent-runtime spec / change spec).

### What OpenSpec is (researched, cited)

OpenSpec ([Fission-AI/OpenSpec](https://github.com/Fission-AI/OpenSpec), MIT license) is a
**lightweight spec-driven-development (SDD) framework for AI coding assistants**. Its own tagline:
*"Spec-driven development (SDD) for AI coding assistants."* It aligns a human and a coding agent on
**what to build before any code is written**, so requirements don't live only in ephemeral chat
history. It is a **dev-time planning/coordination layer inside a repo**, not a runtime dependency,
not an API-specification standard (it is not OpenAPI), and not a bot-definition format.

Concretely, `openspec init` creates a version-controlled `openspec/` tree of **markdown**:

```
openspec/
├── specs/                     # source of truth: how the system behaves today
│   └── <domain>/spec.md       #   ## Requirements → ### Requirement: <name> (SHALL/MUST/SHOULD/MAY)
│                              #                     → #### Scenario: GIVEN / WHEN / THEN
└── changes/                   # proposed modifications (work in progress)
    └── <change-name>/
        ├── proposal.md        #   intent, in/out-of-scope, approach
        ├── design.md          #   technical approach, architecture decisions
        ├── tasks.md           #   implementation checklist (1.1, 1.2, checkboxes)
        └── specs/<domain>/spec.md   # DELTA spec: ## ADDED / ## MODIFIED / ## REMOVED Requirements
```

Completed changes archive to `openspec/changes/archive/<YYYY-MM-DD>-<name>/` and their deltas merge
into the main specs, which become the updated source of truth. RFC-2119 keywords carry requirement
strength. Adoption is strong for a young tool: **~61k GitHub stars, v1.6.0 (July 2026), actively
maintained**, consumed by **30+ AI coding assistants** (Claude Code, Cursor, Copilot, Codex, …) via
`AGENTS.md` compatibility and native slash commands (`/opsx:explore`, `/opsx:propose`, …).

### Why this is the load-bearing observation

OpenSpec's format is *strikingly similar to how aisotropy itself is built* — this ADR's
Requirement/Scenario/delta structure is nearly the same shape as OpenSpec's, and this repo already
runs a spec-driven workflow (the `docs/decisions/` ADR log + the agent-tooling harness `sdd-*` skills
under `.claude/`, whose `sdd-init` even advertises an `openspec init` trigger). That resemblance is a
trap: it invites conflating "aisotropy's build process resembles OpenSpec" with "aisotropy-the-product
should support OpenSpec." They are different questions at different layers.

## The layer question (against ADR-0022)

[ADR-0022](0022-agent-config-standards-import-surfaces.md) fixed a four-layer model for how aisotropy
relates to open agent-config standards. Placing OpenSpec in it is the whole decision:

| Layer (ADR-0022) | Standard | Maps to an aisotropy primitive? | OpenSpec belongs here? |
|---|---|---|---|
| **Dev-time** | AGENTS.md | No — dev-tool context, zero product/runtime impact | **✅ Yes — this is OpenSpec's layer** |
| **Packaging** | Agent Skills / `SKILL.md` | Yes — `scripts/`→tools, instructions→literal persona | No — OpenSpec packages no runtime capability |
| **Knowledge** | OKF | Yes — `IngestKnowledge`→`KindDocument` (ADR-0018) | No (see below) |
| **Runtime tools** | MCP | Yes — scope-gated tool execution (ADR-0016 lever #2) | No — OpenSpec is not a tool wire-format |

OpenSpec is unambiguously a **dev-time methodology** — the same layer as AGENTS.md, which ADR-0022
adopts *in-repo, with zero product/runtime impact*. An OpenSpec `proposal.md`/`tasks.md`/delta-spec is
a **software-engineering planning artifact about building software with an AI assistant**. It does
**not** describe a bot capability, a persona, a tool, or tenant-facing knowledge that the aisotropy
runtime would serve. There is therefore **no typed primitive for an importer to target** — unlike a
`SKILL.md` (→ typed tools + literal persona) or an OKF document (→ ingested knowledge).

The one technically-possible mapping — *any* markdown, including a delta-spec, could be fed to the
existing generic knowledge surface ([ADR-0018](0018-knowledge-ingestion-surface.md) `IngestKnowledge`
→ `KindDocument`) — is **already served with zero OpenSpec-specific code**. Special-casing it would
add a bespoke surface that buys nothing the generic path doesn't already give, which is the definition
of speculative periphery.

## Goals / Non-goals

**Goals**
- Give a precise, cited answer to "what is openspec.dev and does aisotropy support it."
- Decide *where* it would sit (dev-time layer) and whether that warrants any code today.
- Leave an explicit, falsifiable trigger so a future maintainer re-opens the question on evidence, not
  vibe.

**Non-goals**
- **Not** building an `openspec → primitive` importer (there is no primitive to import to).
- **Not** turning SDD into an aisotropy *product feature* — SDD is how aisotropy is *built*, not a
  capability it offers consumers ([`capabilities-and-gaps.md`](../architecture/capabilities-and-gaps.md)
  row "Spec-Driven Development (SDD) — DEV-PROCESS ONLY").

## Options considered

### Option A — Build a minimal `internal/openspec` importer (openspec change/spec → typed config/primitive) — rejected
The literal task-shaped option: parse `openspec/changes/<c>/specs/<d>/spec.md` and map it to something
aisotropy-typed. Rejected because the target does not exist: an OpenSpec change is a *dev-planning
document*, not a runtime capability/persona/knowledge object. The importer would either (a) reduce to
"ingest markdown as `KindDocument`," which ADR-0018 already does generically, or (b) invent a
synthetic mapping (e.g. Requirement→persona fragment) with **no demonstrated audience** — precisely
the "depth-first engineering on a surface with zero demonstrated demand" the
[consolidation proposal](../architecture/consolidation-proposal.md) (§3, M5) flags as the project's
core over-extension risk. Building it to "close a gap" would *widen* the gap between claim and value.

### Option B — Adopt OpenSpec in-repo as the dev-time SDD workflow (like AGENTS.md) — rejected for now
OpenSpec's real home is the dev-time layer, and ADR-0022 *did* adopt AGENTS.md there. But the repo
already has two overlapping spec-driven mechanisms: the `docs/decisions/` ADR log (this file is one)
and the agent-tooling `sdd-*` harness skills. Introducing a third, parallel `openspec/` tree is
redundant coordination overhead with no contributor asking for it. This is an operator/dev-process
choice, reversible at any time, and can be revisited cheaply if the team composition changes (below).

### Option C — Deliberately defer, document exactly what it is + the revisit trigger — chosen
Records the honest state: OpenSpec is understood, correctly placed at the dev-time layer, and needs
**no product code**. A well-reasoned defer is a valid outcome — the same posture ADR-0022 took toward
the Skill *importer* ("boundary decided, importer is future work") and ADR-0020 took toward adopting
LangChain/Mastra ("right call only under a specific ICP, here are the triggers to revisit").

## Decision

**Defer OpenSpec on both axes, and say so explicitly.**

1. **As a product / import surface → not supported, and not planned.** There is no aisotropy primitive
   an OpenSpec artifact maps to that the generic knowledge surface (ADR-0018) doesn't already cover.
   No `internal/openspec` package is created.
2. **As an in-repo dev-time methodology → not adopted now.** The `docs/decisions/` ADR log + `sdd-*`
   harness skills already provide spec-driven development for building aisotropy. Adopting a parallel
   `openspec/` tree waits on real pull (below).

This upgrades the informal skip in
[`capabilities-and-gaps.md`](../architecture/capabilities-and-gaps.md) to a cited decision record.

## When this decision STOPS being true (revisit triggers)

Re-open (amend this ADR or write a new one) if **any** fire:

1. **A real adopter standardizes on OpenSpec.** An external consumer or contributor works the repo
   through OpenSpec slash-commands and wants shared `openspec/specs/` — then adopt it at the dev-time
   layer (Option B), exactly as AGENTS.md was adopted. Trigger: ≥1 concrete requester, not
   hypothetical.
2. **OpenSpec adds a runtime/agent-definition artifact** (not just dev-time planning) that carries
   capability or tenant-facing knowledge — then re-run the ADR-0022 layer test on *that* artifact; it
   might map to a typed tool / persona / `KindDocument` and justify an importer.
3. **The eval harness ([ADR-0026](0026-product-evaluation-harness.md)) needs a portable
   acceptance-criteria format.** OpenSpec's `Requirement`/`Scenario` (GIVEN/WHEN/THEN) is a candidate
   source-of-truth for gold acceptance tests. If ADR-0026 grows a gold dataset and wants a standard
   spec format, reconsider parsing OpenSpec specs *into eval fixtures* (still not a runtime importer).
4. **The harness `sdd-*` workflow is replaced by OpenSpec.** If the agent tooling migrates to
   OpenSpec as its SDD substrate, the in-repo redundancy argument (Option B) dissolves and adoption
   becomes free.

## Consequences

**Positive**
- The stance is now cited and falsifiable: readers get exactly what OpenSpec is, why it's the
  dev-time layer, and the precise signal that would change the answer — no re-research next time.
- Stays on-thesis with the consolidation direction: no new speculative surface added to close a gap
  that isn't demanded.
- License is clean either way (MIT), so adoption at the dev-time layer remains a cheap, reversible
  move the day a trigger fires.

**Negative / honesty debt**
- "We understand OpenSpec and chose to defer" is weaker ecosystem-interop than "we support it." That
  is the deliberate cost — the same anti-scope-creep tax ADR-0020/consolidation-proposal accept.
- If OpenSpec's ~61k-star momentum makes it a *de facto* dev-tool expectation, the dev-time adoption
  (Option B) may need to happen sooner than trigger #1 strictly requires; this ADR errs toward
  waiting for a concrete requester.

## Addendum — 2026-08-21: revisit trigger #4 fired; the redundancy argument no longer holds

**Nothing in the decision above is rewritten.** This records a change of state in the repo that
invalidates one of the reasons the decision rested on.

Option B ("adopt OpenSpec in-repo as the dev-time SDD workflow") was rejected *for now* on the
grounds that **the repo already had two overlapping spec-driven mechanisms**, so an `openspec/`
tree would be a redundant third. That is no longer the state of the repo. The harness-side `sdd-*`
skills cited here — in §"Why this is the load-bearing observation", in Option B, and in Decision
point 2 — **are no longer present**. The remaining in-repo spec-driven mechanism is the
`docs/decisions/` ADR log alone (this file is one of its entries).

Revisit trigger #4 read: *"If the harness tooling migrates to OpenSpec as its SDD substrate, the
in-repo redundancy argument (Option B) dissolves and adoption becomes free."* What happened is
adjacent and stronger — the tooling was not migrated, it was removed — so the trigger's
**condition** (the second mechanism stops existing) is met by a route the trigger did not name.

What this does and does not settle:

- **Dissolved:** the "third, parallel tree" objection. Adopting a dev-time SDD workflow today would
  make a *second* mechanism next to the ADR log, not a third.
- **Unchanged:** the product/import-surface half of the decision (Decision point 1). An OpenSpec
  artifact still maps to no aisotropy primitive that ADR-0018's generic knowledge path does not
  already cover. No `internal/openspec` package is warranted by this addendum.
- **Unchanged:** revisit trigger #1 has not fired — no concrete external requester has appeared.
- **Open, and deliberately not decided here:** with one mechanism left, whether the repo wants a
  dev-time SDD substrate at all, and if so which one. That is an operator call and needs its own
  record; this addendum only reports that the ground the old answer stood on moved.

## References

- [openspec.dev](https://openspec.dev/) · [Fission-AI/OpenSpec (GitHub, MIT)](https://github.com/Fission-AI/OpenSpec) · [core concepts / spec format](https://openspec.dev/docs/core-concepts) — the researched primary sources.
- [ADR-0022 — Agent-config standards as import & packaging surfaces](0022-agent-config-standards-import-surfaces.md) — the layer model this ADR applies; OpenSpec lands on the AGENTS.md (dev-time) layer.
- [ADR-0002 — Anti-framework-magic](0002-anti-framework-magic-struct-as-contract.md) — the gate any "import a spec and inject it" idea is measured against.
- [ADR-0018 — Knowledge-ingestion surface](0018-knowledge-ingestion-surface.md) — the generic markdown→`KindDocument` path that already covers the only viable OpenSpec mapping.
- [ADR-0020 — Build vs. adopt](0020-build-vs-adopt-lang-suite-mastra.md) — the precedent for a defer/positioning decision with explicit revisit triggers.
- [ADR-0026 — Product-evaluation harness](0026-product-evaluation-harness.md) — where OpenSpec's Requirement/Scenario format could later feed gold acceptance tests (trigger #3).
- [consolidation-proposal.md](../architecture/consolidation-proposal.md) — "breadth outran depth; don't build ahead of demand," the direction this defer honors.
- [capabilities-and-gaps.md](../architecture/capabilities-and-gaps.md) — the informal "conscious skip" this ADR formalizes.
- [AGENTS.md](../../AGENTS.md) — the in-repo dev-time standard OpenSpec would join if trigger #1 fires.
