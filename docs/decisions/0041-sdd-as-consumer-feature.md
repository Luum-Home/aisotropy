---
type: ADR
title: "ADR-0041 — Spec-Driven Development as a consumer feature: deferred — the surfaces that would carry it already exist, and the rest is dev-layer or app composition"
description: Whether aisotropy should offer Spec-Driven Development (SDD) as a product capability to the consumers who build multi-tenant bots on the runtime. Enumerates the three plausible interpretations (declarative bot-spec importer, dev-time spec→code workflow, SDD-as-a-domain-capability), measures each against the anti-magic gate (ADR-0002), the standards-import posture (ADR-0022), the declarative wiring config (ADR-0016 lever #5), and the consolidation direction — and DEFERS: interpretation (a) already exists as the wiring Config/OptionsFromConfig + persona/MCP/knowledge import surfaces (and any richer version violates ADR-0002), (b) belongs at the AGENTS.md/coding-agent dev layer per ADR-0022 (and is partly served by `aisotropy new`), (c) is an application a consumer composes from existing primitives (typed tools + personas + RunTask). No new runtime surface. Concrete adopt trigger defined.
tags: [decisions, adr, sdd, spec-driven, consumer, adoption, anti-magic, consolidation, defer, scope]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0041 — SDD as a consumer feature: deferred

## Status

**⏸️ Deferred — reasoned no-build (2026-07-17).** Decides the *posture* toward offering
Spec-Driven Development (SDD) as a capability aisotropy hands to its **consumers** (third parties
who build a multi-tenant bot by importing `aisotropy/`). No new runtime surface is added. Governed
by the anti-magic gate ([ADR-0002](0002-anti-framework-magic-struct-as-contract.md)); measured
against the standards-import posture ([ADR-0022](0022-agent-config-standards-import-surfaces.md)),
the declarative wiring config ([ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md)
lever #5), the public façade ([ADR-0013](0013-public-extension-surface-facade-vs-promote.md) /
[ADR-0031](0031-embeddable-in-process-turn.md)), the sandboxed task agent
([ADR-0036](0036-sandboxed-task-agent-key-brokering.md)), and the
[consolidation proposal](../architecture/consolidation-proposal.md). This ADR **decides the
boundary**, not code: it records why the surface is not built and the exact trigger to revisit.

## Context

Today "SDD" appears in this repository in exactly one role: it is the methodology used to **build
aisotropy** — the `sdd-*` skills under `.claude/`, and the durable consumer-project lane whose
artifacts land in `docs/roadmap/sdd/`. That is dev-time tooling for *this* codebase, not a
capability the product exposes.

The operator asked a different question: **could aisotropy offer SDD as a product feature to its
consumers?** Answering honestly requires refusing to treat "SDD support" as one thing. "Support
SDD" collapses at least three unrelated features, each living at a different layer — and the layer
is what decides the verdict, exactly as [ADR-0022](0022-agent-config-standards-import-surfaces.md)
found for the agent-config standards.

## The three interpretations (be concrete about who does what)

| # | Interpretation | What the *consumer* actually gets | Layer |
|---|---|---|---|
| **(a)** | **Declarative bot-spec → typed primitives.** A spec file that declares a bot's personas, tools, memory policy, limits — which aisotropy loads into typed primitives, the way ADR-0022 imports a `SKILL.md`. | "I describe my bot in one file and the runtime stands it up." | **Runtime wiring** |
| **(b)** | **A spec→proposal→design→tasks→code workflow for building ON aisotropy** — the repo's own SDD lane, but shipped *to* the consumer as a scaffolding/dev methodology. | "A guided pipeline that turns my bot idea into implemented Go against the façade." | **Dev-time (coding agent)** |
| **(c)** | **SDD as a domain capability the consumer's *bot* offers its *own* end-users** — e.g. a bot that takes an end-user's feature request and produces a spec, then design, then tasks, then (optionally) code. | "My product does SDD *for my users*." | **Application built on the runtime** |

The whole decision turns on noticing these are three different products wearing one word.

## Fit + ROI, per interpretation

### (a) Declarative bot-spec → this already exists, and the richer version is forbidden by ADR-0002.

The "spec that declaratively defines a bot's personas / tools / memory policy which aisotropy loads
into typed primitives" is, field for field, the **wiring `Config`** that already ships
([`aisotropy/config.go`](../../aisotropy/config.go), ADR-0016 lever #5):

- `personas:` → literal per-`product_id` base prompts (`WithPersona`) — the same *literal string,
  no templating* an imported `SKILL.md`'s instructions map to under ADR-0022.
- `mcp_servers:` → scope-gated tools whose behavior lives in an external process the spec only
  *names* (`WithMCPServer`) — the ADR-0022 "code → MCP" rule.
- `rate_limit:` / `cost_pricing:` / `addr:` → the memory/governance deployment policy.
- A consumer's own knowledge (a `SKILL.md`'s `references/`) → the knowledge-ingestion surface
  ([ADR-0018](0018-knowledge-ingestion-surface.md) / [ADR-0019](0019-tenant-wide-document-sharing.md)).

Two shippable example specs already exist —
[`examples/aisotropy.example.yaml`](../../examples/aisotropy.example.yaml) and
[`examples/helpdesk/helpdesk.yaml`](../../examples/helpdesk/helpdesk.yaml) — loaded via
`OptionsFromConfigFile`. So a "bot-spec" is not a gap; it is a **named, tested surface**.

The only part of interpretation (a) that is *not* already covered is the tempting one: a spec that
**also declares tool behavior** (a tool's logic, few-shot guidance, the LLM handler) so the whole
bot is "just the file." That is precisely what `config.go`'s own doc comment refuses and what
[ADR-0002](0002-anti-framework-magic-struct-as-contract.md) forbids: *a behavior-less tool list in
config is exactly the "magic" the project rejects* — "if you don't know what string was sent, it's
a design bug." A YAML file cannot carry a Go `func`; a spec that pretends to is a hidden runtime
behavior path. **So interpretation (a) is either redundant (the wiring config) or a gate violation
(behavior-in-spec). There is no third, valuable version of it left to build.**

### (b) Spec→code dev workflow → belongs at the AGENTS.md / coding-agent layer, per ADR-0022.

A pipeline that turns a bot *idea* into implemented Go is a **dev-time coding activity**, not a
runtime capability. [ADR-0022](0022-agent-config-standards-import-surfaces.md) already placed this
class of thing: dev-time methodology is the AGENTS.md layer (a "README for coding agents" working
inside a repo), explicitly *not* a product/runtime concern. Aisotropy shipping an SDD pipeline to
consumers would put the runtime in the business of driving a coding agent — the opposite of the
sealed, in-process runtime the project is. The consumer already has:

- [`aisotropy new`](../../cmd/aisotropy/new.go) — one command scaffolds a complete, runnable bot
  (persona + typed tool + dev chat + `App.Run`); the "blank main.go → running app" step is solved.
- The [`build your own agent`](../guides/build-your-own-agent.md) walkthrough + the two example
  modules the compiler forces onto public surface only.
- Their *own* coding agent (Claude Code / Cursor / Codex), steered by an in-repo `AGENTS.md`, to
  run whatever spec→code discipline they want — including this repo's own `sdd-*` skills, which are
  portable.

Re-implementing a spec→design→tasks pipeline *inside aisotropy* duplicates general coding-agent
tooling that is not aisotropy's moat and adds a large, opinionated surface no adopter has asked
for. **Dev-layer, already served; not a runtime feature.**

### (c) SDD as a domain capability → the consumer composes it; it is not runtime infrastructure.

A bot that performs SDD *for its end-users* is an **application built on the runtime**, no different
from a helpdesk bot or a research bot. Everything it needs is already a primitive:

- Personas (`WithPersona`) for the spec/design/tasks "voices".
- Typed tools (`WithTypedTool[T]`, ADR-0016 lever #1) + MCP tools for the steps.
- For the "…and then implement it" step, the **key-brokered sandboxed task agent**
  ([`App.RunTask`](../../aisotropy/runtask.go) / `RunTasks`,
  [ADR-0036](0036-sandboxed-task-agent-key-brokering.md) /
  [ADR-0037](0037-concurrent-sandboxed-task-agents.md)) already runs an untrusted agent that writes
  files, runs shells, and commits — in an isolated worktree, never touching the API key. An
  SDD-style "produce a plan, then execute it in a sandbox" flow is *composition over `RunTask`*, not
  a new surface.

Hardcoding an SDD flow into the runtime would bake one opinionated product into infrastructure that
is deliberately unopinionated about what bot you build. **Consumer-composable; not aisotropy's to
ship.**

## Options considered

### Option A — Build a first-party SDD feature (a spec DSL + a spec→code pipeline) — rejected
Maximum "we support SDD" surface. Rejected on every axis: interpretation (a) duplicates the wiring
config or violates ADR-0002; (b) duplicates general coding-agent tooling at the wrong layer; (c)
bakes one application into the runtime. It is textbook "build ahead of demand" — the
[consolidation proposal](../architecture/consolidation-proposal.md) documents this exact
failure mode (e.g. M5: a whole open-core plugin/WASM programme built "ahead of a single external
plugin author"). No adopter has requested SDD; the ask is speculative. Adding ~a package + a DSL to
"look complete" is precisely what the consolidation direction says to stop doing.

### Option B — Extend the existing wiring `Config` to carry "spec-like" structure (tool behavior, workflow steps) — rejected
The seductive middle. Rejected because the moment the spec carries *behavior* (a tool's logic, a
workflow's control flow), it is the ADR-0002 magic path: a markdown/YAML file silently steering the
payload. The wiring config's own boundary comment already draws this line; re-crossing it here would
regress the founding invariant.

### Option C — Defer: no new surface; document the mapping and the trigger — chosen
The three interpretations map cleanly onto surfaces that **already exist and are on-thesis**
(wiring `Config` + ADR-0022 imports for (a); AGENTS.md + `aisotropy new` for (b); typed tools +
personas + `RunTask` for (c)). "SDD support" is therefore not a missing capability but a
**documentation + framing** question, answered by pointing consumers at the primitives they already
have. This keeps the moat and the consolidation posture intact and costs zero new surface.

## Decision

**Defer building any SDD-as-a-feature surface (Option C).** Do not add an `internal/` package, a
spec DSL, or a spec→code pipeline. Record the interpretation-to-surface mapping (above) as the
standing answer: a consumer who wants "SDD" is directed to the declarative wiring config
(interpretation a), their own coding agent + `aisotropy new` (b), or composition over typed
tools/personas/`RunTask` (c). This is consistent with ADR-0022's "embrace each thing at its layer"
and with the consolidation direction of *not building periphery ahead of demand*.

## Consequences

**Positive**
- **Zero speculative surface.** No new package, DSL, or pipeline to test, version, and explain —
  directly aligned with the consolidation proposal's "breadth outran depth" diagnosis.
- **The anti-magic moat is preserved.** The one version of (a) with genuine novelty
  (behavior-in-spec) is exactly the ADR-0002 violation; refusing it is the moat working as designed.
- **The consumer is not blocked.** Every real thing behind "SDD support" is already expressible:
  declarative wiring, scaffolding, and `RunTask` composition. The answer is "you already can," not
  "we don't do that."

**Negative / debt**
- **A framing gap remains.** Until a consumer-facing note connects the dots, a prospective adopter
  may read "no SDD feature" as "aisotropy can't help me build methodically," when the truth is "the
  primitives are there, unbranded." A short guide (`docs/guides/`) mapping the three interpretations
  to existing surfaces is the cheap follow-up if an adopter raises it — deliberately not written
  ahead of that signal, per this same ADR's logic.
- **`aisotropy new` is thin.** It scaffolds one shape; it is not a "spec-first" scaffolder. If the
  trigger below fires, the smallest honest step is enriching the scaffolder's templates (a manifest
  row + a template, per its design), *not* a new runtime surface.

## The adopt trigger (when to revisit)

Revisit this deferral when **all** of the following hold — i.e. real, grounded demand, not a hunch:

1. **A concrete adopter need** exists that the current surfaces genuinely cannot express — a bot
   whose declarative shape the wiring `Config` + ADR-0022 imports + `RunTask` composition cannot
   capture without hand-written Go that the adopter demonstrably shouldn't have to write.
2. That need is for a **runtime/wiring** capability (interpretation a), **not** a coding-agent
   workflow (b) or an application feature (c) — because b and c are settled at their layers above.
3. The proposed surface **stays inside the ADR-0002 boundary**: it declares *values and references*
   (personas, tool *names*, MCP/knowledge references, policy), never *behavior*. If the only way to
   make it valuable is to carry behavior in the spec, the trigger has **not** been met — that is the
   forbidden path, permanently.

If (1)–(3) all hold, the minimal first step is extending the existing wiring `Config` /
`aisotropy new` templates under the same gate — reopen as a follow-on to ADR-0016/ADR-0022, not as a
greenfield SDD subsystem.

## Open questions

1. **Producer direction (out of scope here).** ADR-0022 already flagged exporting aisotropy's own
   tools *as* Agent Skills / MCP for other clients. Whether this repo's `sdd-*` skills should be
   *published* for consumers to run against their own bots is an ADR-0022 producer-side question,
   distinct from "SDD as a runtime feature" — noted, not decided.
2. **Where the consumer-facing framing note lives** if the debt above is paid — a `docs/guides/`
   page vs. a section in `build-your-own-agent.md` — deferred until an adopter asks.

## References

- [ADR-0002 — Anti-framework-magic](0002-anti-framework-magic-struct-as-contract.md) — the gate the
  behavior-in-spec version of interpretation (a) violates.
- [ADR-0022 — Agent-config standards as import surfaces](0022-agent-config-standards-import-surfaces.md)
  — the "embrace each thing at its layer" precedent this ADR applies to SDD; places interpretation
  (b) at the AGENTS.md dev layer.
- [ADR-0016 — Dual surface (lever #5 declarative wiring)](0016-dual-surface-sealed-runtime-composition-sdk.md)
  — the wiring `Config` that already *is* interpretation (a).
- [ADR-0036 — Sandboxed key-brokered task agent](0036-sandboxed-task-agent-key-brokering.md) /
  [ADR-0037](0037-concurrent-sandboxed-task-agents.md) — the `RunTask` primitive interpretation (c)
  composes over.
- [ADR-0020 — Build vs. adopt (revisit triggers)](0020-build-vs-adopt-lang-suite-mastra.md) — the
  precedent for a decision that fixes posture + names a trigger rather than building.
- [Consolidation / pruning proposal](../architecture/consolidation-proposal.md) — the "don't build
  ahead of demand" direction this deferral honors.
- [`aisotropy/config.go`](../../aisotropy/config.go) ·
  [`examples/helpdesk/helpdesk.yaml`](../../examples/helpdesk/helpdesk.yaml) ·
  [`examples/aisotropy.example.yaml`](../../examples/aisotropy.example.yaml) ·
  [`cmd/aisotropy/new.go`](../../cmd/aisotropy/new.go) — the surfaces that already carry the
  non-forbidden parts of "SDD support".
