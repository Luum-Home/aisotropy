---
type: ADR
title: "ADR-0022 — Agent-config standards (AGENTS.md / Agent Skills / OKF / MCP): supported as import & packaging surfaces, not opaque runtime behavior"
description: How aisotropy relates to the emerging open agent-configuration standards — AGENTS.md (dev-time), Agent Skills / SKILL.md (capability packaging), OKF (knowledge-as-data), MCP (runtime tools) — each embraced at the layer it belongs to, with the anti-magic gate (ADR-0002) as the load-bearing boundary: a standard is an import/packaging convention that maps to aisotropy's TYPED primitives + LITERAL personas, never hidden runtime prompt-injection.
tags: [decisions, adr, interop, standards, agents-md, agent-skills, skill-md, okf, mcp, anti-magic, extension, primitives]
timestamp: 2026-07-10T00:00:00Z
---

# ADR-0022 — Agent-config standards as import & packaging surfaces

## Status

✅ **Accepted (2026-07-10).** Decides the *boundary and posture* toward the open agent-config
standards; the concrete Skill-import mapping is scoped future work (like ADR-0016 lever #4). Governed
by the anti-magic gate ([ADR-0002](0002-anti-framework-magic-struct-as-contract.md)); extends the
extension surface ([ADR-0013](0013-public-extension-surface-facade-vs-promote.md) /
[ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md)) and the knowledge-ingestion surface
([ADR-0018](0018-knowledge-ingestion-surface.md)). A dev-time [`AGENTS.md`](../../AGENTS.md) was added
to the repo alongside this ADR.

## Context

The operator asked: *how are primitives added — do we take skills/rules/workflows in markdown? do we
respect and support [agents.md](https://agents.md/), [agentskills.io](https://agentskills.io/), and
others?* Answering requires separating **three layers** that are easy to conflate, because a given
standard lives in exactly one:

| Layer | Standard | What it is | Carries behavior? |
|---|---|---|---|
| **Dev-time** | **AGENTS.md** (Agentic AI Foundation / Linux Foundation; 60k+ repos) | A "README for coding agents" working *inside a repo* — build/test/conventions for Cursor/Codex/Claude Code/etc. Explicitly **not** a runtime bot-definition format. | Instructions to a *dev tool*, not to the product |
| **Packaging** | **Agent Skills / `SKILL.md`** (open format, originated at Anthropic; agentskills.io) | A folder = `SKILL.md` (name/description + **instructions**) + optional `scripts/`/`references/`/`assets/`, loaded by progressive disclosure. Adopted mostly by coding agents; the *format* is general. | **Yes** — instructions are prompt-behavior; `scripts/` is executable code |
| **Knowledge** | **OKF** (Open Knowledge Format) | Curated markdown + frontmatter as portable knowledge. Aisotropy already dogfoods it for `docs/`. | No — data, not behavior |
| **Runtime tools** | **MCP** | The wire standard for tool discovery + execution. Aisotropy already supports it (`WithMCPServer`, ADR-0016 lever #2). | Behavior, but as an **explicit, scope-gated** tool call |

**Aisotropy today** adds primitives via **Go (typed) + MCP + wiring-YAML**: `WithTypedTool[T]`,
`WithToolExecutor`, `WithMCPServer`, `WithPersona` (a **literal** base prompt), `OptionsFromConfig`
(`aisotropy/config.go` — "deployment VALUES only"). There is **no** markdown-as-behavior surface, by
design.

**The load-bearing tension.** Agent Skills' value *is* that a `SKILL.md`'s prose instructions get
loaded into the agent's context "on demand" (progressive disclosure). That is, verbatim, the pattern
[ADR-0002](0002-anti-framework-magic-struct-as-contract.md) rejects: *"if you don't know what string
was sent, it's a design bug."* Naively adopting Skills = a runtime that silently injects prompt text a
markdown file carries — the exact "magic" the whole project is built against. So the question is not
"yes/no support" but **at which layer, and where the anti-magic line falls.**

## Goals / Non-goals

**Goals**
- Interoperate with the ecosystem's momentum (Skills are adopted across ~40 agent clients; AGENTS.md
  across 60k repos) **without** abandoning the isolation/anti-magic moat.
- Give a precise, testable rule for *where* each standard is embraced.

**Non-goals**
- **Not** turning aisotropy into a markdown-behavior runtime. **Not** adopting opaque
  progressive-disclosure auto-injection. **Not** building the Skill-importer in this ADR (boundary +
  posture only).

## The rule (load-bearing)

> **A standard is an _import/packaging convention_ whose contents map to aisotropy's TYPED primitives
> and LITERAL personas. Behavior stays explicit — you still see every token. Nothing a file carries is
> silently injected at runtime.**

Concretely, per layer:

1. **AGENTS.md → adopt in-repo (dev-time).** Cheap, standard, tool-agnostic; generalizes the existing
   Claude-specific `CLAUDE.md`/`.claude/rules`. Added alongside this ADR. Zero product/runtime impact.
2. **MCP → already the runtime primitive standard.** A Skill's `scripts/` (executable behavior) maps to
   an **MCP server** (or a typed `ToolExecutor`) — scope-gated, ctx-scoped, explicit. This is the
   *correct* home for a skill's *code*.
3. **OKF → already supported as knowledge-as-data.** A Skill's `references/`/`assets/` (non-behavioral
   content) map to the [knowledge-ingestion surface (ADR-0018)](0018-knowledge-ingestion-surface.md)
   (`IngestKnowledge` → `KindDocument`). No behavior, no magic.
4. **Agent Skills / `SKILL.md` → supported as an IMPORT surface, under the boundary.** A future
   `aisotropy` import maps a skill folder to primitives: `scripts/` → MCP/typed tools (rule 2);
   `references/`/`assets/` → ingested knowledge (rule 3); the `SKILL.md` **instructions** → a
   **`Persona`/prompt fragment that becomes a literal string the operator registered and can read**,
   *not* an auto-injected hidden template; `name`/`description` → tool/persona registration metadata.
   The progressive-disclosure "activation" is allowed only as **operator-visible** selection (like
   `product_id` → persona today), never as silent runtime injection.

If a Skill can only work by injecting prose the operator never sees, aisotropy **does not** run it as
magic — it surfaces the string as a literal persona the operator owns, or declines. That is the test.

## Options considered

### Option A — Ignore the standards; stay Go+MCP only — rejected
Simplest, fully on-thesis, but cedes ecosystem interop (portable skills, dev-tool onboarding) for no
reason: AGENTS.md and OKF cost nothing and MCP is already in. Under-reaches.

### Option B — Adopt Agent Skills natively, including runtime progressive-disclosure injection — rejected
Maximum ecosystem fit, but it **is** ADR-0002's magic: a markdown file silently steering the payload.
Regresses the project's founding invariant. Rejected outright.

### Option C — Embrace each standard at its layer; Skills as import-to-typed-primitives under the anti-magic boundary — chosen
Gets the interop (AGENTS.md in-repo, OKF for content, MCP for code, Skills importable) while the
behavior a Skill carries becomes **explicit aisotropy primitives** (typed tools + literal personas),
never hidden runtime templating. The moat and the ecosystem, both.

## Consequences

**Positive**
- Aisotropy is legible to the ecosystem (AGENTS.md for dev tools; MCP already; a clear Skills-import
  story) without a single opaque-prompt path.
- Producer direction unlocked: aisotropy's own tools can later be *exported* as MCP and/or published as
  Agent Skills for other clients — reuse in both directions.

**Negative / debt**
- The Skill-importer is real code not yet written; until then "we support Agent Skills" means "the
  boundary and mapping are decided," not "there is an importer." Stated honestly to avoid overclaim.
- Some skills lean entirely on auto-injected prose; those import as literal personas (operator sees the
  text) or are declined — a deliberate friction that is the anti-magic tax.

## Open questions

1. **Exact Skill-import mapping + CLI** (`aisotropy import-skill <dir>` → generated `WithTypedTool`/
   `WithMCPServer`/`WithPersona` wiring), and how progressive-disclosure "activation" maps to
   operator-visible persona selection.
2. **Producer side:** publish aisotropy's demo/typed tools *as* Agent Skills + MCP servers for other
   clients? (interop the other way).
3. **Versioning/trust** of imported skills (they are third-party behavior + code — supply-chain and
   scope-gating apply, per the license/security gates).

## References

- [AGENTS.md](../../AGENTS.md) (in-repo, added with this ADR) · [agents.md](https://agents.md/) · [agentskills.io](https://agentskills.io/)
- [ADR-0002 — Anti-framework-magic](0002-anti-framework-magic-struct-as-contract.md) — the gate this decision is measured against.
- [ADR-0016 — Dual surface (MCP lever #2)](0016-dual-surface-sealed-runtime-composition-sdk.md) · [ADR-0013 — Public extension surface](0013-public-extension-surface-facade-vs-promote.md)
- [ADR-0018 — Knowledge-ingestion surface (OKF/Documents)](0018-knowledge-ingestion-surface.md)
- [vision/principles.md](../vision/principles.md) — OKF dogfooding · [what Aisotropy is](../architecture/what-aisotropy-is.md)
