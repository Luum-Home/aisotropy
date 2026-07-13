---
type: How-To
title: Importing Agent Skills into Aisotropy
description: The precise mapping from an Agent Skill (agentskills.io — SKILL.md + scripts/ + references/) to Aisotropy's typed primitives and literal personas, under the anti-magic boundary (ADR-0022 / ADR-0002). Scripts become scope-gated typed tools or MCP servers, instructions become a literal Persona the operator can read, references become ingested knowledge — nothing is silently injected at runtime.
tags: [guide, howto, adoption, agent-skills, skill-md, interop, anti-magic, mcp, personas, knowledge]
timestamp: 2026-07-13T00:00:00Z
---

# Importing Agent Skills into Aisotropy

[Agent Skills](https://agentskills.io) package a capability as a folder: a
`SKILL.md` (YAML frontmatter — `name`, `description` — plus markdown
instructions) with optional `scripts/` (executable behavior), `references/`, and
`assets/` (content). Aisotropy **supports Skills as an import surface**
([ADR-0022](../decisions/0022-agent-config-standards-import-surfaces.md)), not as
an opaque runtime: every part of a skill maps to an explicit Aisotropy primitive
you can read before it runs.

The load-bearing rule ([ADR-0002](../decisions/0002-anti-framework-magic-struct-as-contract.md)):

> A standard is an **import/packaging convention** whose contents map to
> Aisotropy's **typed primitives** and **literal personas**. Behavior stays
> explicit — you still see every token. Nothing a file carries is silently
> injected at runtime.

Agent Skills' native value is that a `SKILL.md`'s prose is loaded into the
model's context "on demand" by progressive disclosure. That auto-injection is
exactly what Aisotropy refuses. The importer keeps the interop and drops the
magic: a skill's instructions become a **literal `Persona` string the operator
registers and selects by `product_id`**, never an unseen template.

## The mapping

| Skill artifact | What it carries | Maps to (public `aisotropy` surface) | ADR |
|---|---|---|---|
| `SKILL.md` `name` / `description` | metadata | Tool / persona registration metadata (tool name, description) | 0022 |
| `SKILL.md` markdown **instructions** | prompt behavior | `WithPersona(productID, Persona{BasePrompt: <literal instruction text>})` — the operator sees the exact string | 0022 / 0002 |
| `scripts/<x>` with a **declared arg schema** | executable behavior | `WithTypedTool[T](name, description, requiredScope, handler)` — args become a Go struct `T` (the struct-as-contract), fail-closed on bad input | 0016 lever #1 |
| `scripts/<x>` with **no declared schema** | executable behavior | `WithMCPServer(requiredScope, command, args...)` — wrapped as a stdio MCP tool server, its own schema advertised | 0016 lever #2 |
| `references/` · `assets/` (text) | non-behavioral content | `App.IngestKnowledge` / `IngestTenantKnowledge` → `KindDocument` (RAG, retrieved on relevance) | 0018 |
| declared **required scope** | authorization | `Tool.RequiredScope` — the tool is offered to the model only when the request's granted scopes include it | 0016 |

Progressive-disclosure **activation** maps to **operator-visible `product_id`
selection** (the caller passes `product_id` on the chat request, exactly as for
any registered persona today) — never silent runtime injection.

### How a skill declares the mapping

The importer reads an optional `metadata.aisotropy` block in the SKILL.md
frontmatter — a free-form extension point the format already sanctions. It is
the operator-visible import manifest: everything in it becomes an explicit
primitive. When the block is absent, the importer applies safe defaults (scripts
→ MCP with an **empty** scope the operator must fill; instructions → a persona
keyed on the skill `name`; every `references/`/`assets/` file → ingestible
knowledge).

```yaml
metadata:
  aisotropy:
    persona-product-id: pdf-form-filler   # product_id the persona registers under
    required-scope: write:pdf             # -> Tool.RequiredScope on every script tool
    scripts:
      - path: scripts/fill_form.py
        interpreter: python3
        map-to: typed-tool                # "typed-tool" | "mcp"
        tool-name: fill_pdf_form
        args:                             # declared inputs -> typed struct T / MCP schema
          - {name: source_path,  type: string, required: true,  description: "..."}
          - {name: answers_json, type: string, required: true,  description: "..."}
          - {name: flatten,      type: bool,   required: false, description: "..."}
    references:
      - {path: references/acroform-fields.md, doc-id: pdf-form-filler/acroform-fields}
```

## Anti-magic rules — what is refused

The importer never emits hidden injection. Concretely it refuses / holds when:

1. **Instructions-only skill that presumes silent injection.** A skill's prose is
   *always* surfaced as a literal `Persona` the operator registers and selects.
   If a skill can only work by injecting prose the operator never sees, Aisotropy
   does not run it as magic — it surfaces the string as a literal persona the
   operator owns, or declines. Frontmatter/prose hints like "always inject" or
   "load on every request" are **not** honored as ambient activation; they are
   mapped to explicit `product_id` selection (and flagged in the report).
2. **Ungated executable behavior.** A `scripts/` tool with an **empty**
   `required-scope` is held: the importer will not emit an ungated tool that runs
   third-party code. The operator must assign a scope first (governance by
   absence of information — an unscoped tool the model shouldn't have is simply
   never registered).
3. **Behavior smuggled as data.** `references/`/`assets/` are ingested as
   `KindDocument` knowledge (retrieved by RAG on relevance), never promoted into
   the base prompt. Content is content; only a `Persona` (a literal string) or a
   tool (typed/​MCP) carries behavior.

## Import flow

```
skill/ (SKILL.md + scripts/ + references/)
   │
   ▼  skill-import (reads, never writes; never injects)
   ├─ frontmatter name/description  ─────────────►  registration metadata
   ├─ markdown instructions         ─────────────►  WithPersona(product_id, {BasePrompt: <literal>})
   ├─ scripts/ (schema declared)    ─────────────►  WithTypedTool[T](name, desc, scope, handler)
   ├─ scripts/ (no schema)          ─────────────►  WithMCPServer(scope, interpreter, path)
   └─ references/, assets/          ─────────────►  App.IngestTenantKnowledge(... KindDocument)
   │
   ▼  operator review  (reads the generated persona string + scope gates)
   │
   ▼  aisotropy.New(opts...) + IngestTenantKnowledge(...)   ← explicit, hand-owned wiring
```

The importer is a code-generator **preview**, deliberately not an auto-applier.
The operator reads the generated persona string and the scope gates, writes the
tool handler that bridges to the script, and stands the bot up. That review step
is the anti-magic tax ADR-0022 accepts.

## Worked example

A runnable sample skill lives at
[`examples/skills/pdf-form-filler`](../../examples/skills/pdf-form-filler): a
realistic `SKILL.md` (fill AcroForm PDFs), a `scripts/fill_form.py` stub, and a
`references/acroform-fields.md` knowledge file. The PoC importer
[`cmd/skill-import`](../../cmd/skill-import) reads that folder and prints the
mapping plus compile-ready wiring:

```
go run ./cmd/skill-import examples/skills/pdf-form-filler
```

The resolved wiring it emits (against the public
`github.com/Luum-Home/aisotropy/aisotropy` surface):

```go
// fill_pdf_form: typed args derived from the skill's declared script inputs.
type FillPdfFormArgs struct {
    SourcePath  string `json:"source_path"  jsonschema:"description=Path to the blank fillable PDF.,required"`
    AnswersJSON string `json:"answers_json" jsonschema:"description=JSON object of AcroForm field name -> value.,required"`
    Flatten     bool   `json:"flatten"      jsonschema:"description=Flatten the form so fields are no longer editable."`
}

app := aisotropy.New(
    aisotropy.WithPersona("pdf-form-filler", aisotropy.Persona{
        BasePrompt: `# PDF form filler

You fill fillable (AcroForm) PDF forms. Follow this procedure exactly.
... (the literal SKILL.md instructions — the operator sees every line) ...`,
    }),
    aisotropy.WithTypedTool[FillPdfFormArgs](
        "fill_pdf_form",
        "Fill AcroForm fields in a fillable PDF ...", // from SKILL.md description
        "write:pdf",                                   // Tool.RequiredScope
        fillPdfFormArgsHandler,                        // operator-written bridge to the script
    ),
)

// references/acroform-fields.md -> ingested knowledge (ADR-0018), a runtime call:
// content, _ := os.ReadFile(".../references/acroform-fields.md")
// app.IngestTenantKnowledge(ctx, tenantID, aisotropy.Document{
//     ID: "pdf-form-filler/acroform-fields", Content: string(content), Source: "references/acroform-fields.md"})
```

`fillPdfFormArgsHandler` is the operator-written bridge that shells out to
`scripts/fill_form.py` with the decoded, schema-validated `FillPdfFormArgs`. The
importer does **not** generate execution glue: running third-party code is an
explicit, reviewed step (see Trust & security below).

If the same skill shipped `fill_form.py` **without** a declared arg schema, it
would instead map to `WithMCPServer("write:pdf", "python3", ".../fill_form.py")`
— the script advertises its own schema over MCP, still scope-gated, still
explicit.

## Trust & security

An imported skill is **third-party behavior + code**. The same gates that govern
any dependency apply:

- **Scope-gating is mandatory for `scripts/`.** Every script tool carries a
  `RequiredScope`; the double scope-gate (the tool is only injected into the
  payload when the request grants the scope, and the executor runs under the
  caller's `ctx`-scoped identity) applies exactly as to a hand-written tool. The
  importer holds any script tool with an empty scope.
- **Supply-chain apply.** Executing a skill's script means running code you did
  not write. Vet it, pin it, and prefer the MCP path (the script runs as a
  separate, sandboxable subprocess whose lifecycle the runtime owns) over
  in-process glue. Treat a skill's `license` like any other dependency license —
  the constitutional [license policy](../../.claude/rules/cos/license-policy.md)
  blocks AGPL/SSPL/BSL without an ADR, and the importer flags a block-listed
  license.
- **Tenant isolation is unchanged.** A script tool's handler reads identity from
  `ctx` (`TenantFromContext`) and can never construct a memory scope or cross
  tenants (ADR-0011) — importing a skill cannot widen that surface.
- **The persona is a literal you own.** Because instructions land as a
  `Persona{BasePrompt: <string>}`, a malicious "ignore your rules" instruction in
  a skill is visible in the registered persona at review time, not a hidden
  runtime payload.

## Status

Per [ADR-0022](../decisions/0022-agent-config-standards-import-surfaces.md), the
**boundary and mapping are decided**, and `cmd/skill-import` is a working PoC
that prints the wiring. It is a *preview generator*, not yet an
`aisotropy import-skill` that writes a wired module — that, and the producer
direction (exporting Aisotropy's own tools *as* Skills), remain the ADR's open
questions.

## References

- [ADR-0022 — Agent-config standards as import surfaces](../decisions/0022-agent-config-standards-import-surfaces.md)
- [ADR-0002 — Anti-framework-magic](../decisions/0002-anti-framework-magic-struct-as-contract.md) — the gate this mapping is measured against.
- [ADR-0016 — Dual surface (MCP + typed tools + config wiring)](../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md)
- [ADR-0018 — Knowledge-ingestion surface](../decisions/0018-knowledge-ingestion-surface.md)
- [Build your own agent](build-your-own-agent.md) · [Adoption surface as built](../architecture/adoption-surface.md)
- [`examples/skills/pdf-form-filler`](../../examples/skills/pdf-form-filler) · [`cmd/skill-import`](../../cmd/skill-import)
