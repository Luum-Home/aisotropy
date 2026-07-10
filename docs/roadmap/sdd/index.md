---
type: Section Index
title: Spec-Driven Development (SDD) specs
description: Formal, per-feature specifications for Phase 9+ levers — the requirements/design/traceability layer that turns an accepted ADR into an implementable, testable unit. One doc per lever.
tags: [roadmap, sdd, specs, phase-9]
timestamp: 2026-07-10T00:00:00Z
---

# Spec-Driven Development (SDD) specs

Formal specs that bridge an accepted [ADR](../../decisions/index.md) to implementation: each one
states requirements, design, and a traceability map (every `R<n>` → a test or accepted proof)
before code is written, per the project's [SDD discipline](../../vision/principles.md).

| Spec | Lever | Status |
|---|---|---|
| [Typed tool args](typed-tool-args.md) | ADR-0016 lever #1 — `WithTypedTool[T]` + fail-closed reflection on tool INPUT schemas (the struct-as-contract extended from outputs to tool arguments) | delivered (see `internal/llm/tools.go` `ToolSpec.Parameters`) |

> Coverage note: only lever #1 has an SDD today. Several shipped levers (ADR-0016 #2/#3/#5,
> ADR-0018/0019 knowledge ingestion, the bitrate scorer) were delivered **without** a formal spec —
> retroactive SDDs for those are tracked as a documentation gap in the
> [reality ledger](../../architecture/reality-ledger.md) follow-ups.
