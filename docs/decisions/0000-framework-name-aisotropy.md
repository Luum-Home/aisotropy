---
type: ADR
title: "ADR-0000 — Framework name: Aisotropy"
description: Retroactive record of the project's name — Aisotropy (AI + isotropy) — as decided in the original design conversations, before any ADR existed.
tags: [decisions, adr, naming, retroactive]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0000 — Framework name: Aisotropy

## Status

**✅ Accepted — retroactive formalization (2026-07-08).** The project is named **Aisotropy**
(AI + isotropy). This decision predates this knowledge base and this ADR: it was closed in the
project's original design conversations and has been listed as "accepted" in
[docs/decisions/index.md](index.md) since that index was written, with no corresponding ADR
file. This document is that missing file, written after the fact from the surviving record.

## Context

Every artifact in this repository — the README, the `docs/` knowledge base, the binary name
(`aisotropyd`), the environment variable prefix (`AISOTROPY_*`) — depends on the project having
a settled name before anything else could be written consistently. [docs/index.md](../index.md)
states the name as already decided: *"Framework name: **Aisotropy** (decided)."*

Per [docs/index.md — Primary sources](../index.md#primary-sources), this knowledge base
distills two origin design-conversation transcripts that are **kept locally and intentionally
not published in this repository**. The naming decision was made in those conversations. This
ADR can only record what survived into the published knowledge base — it does not have access
to the original transcript to recover reasoning that wasn't carried forward.

## Goals / Non-goals

**Goals**

- Give the "accepted" naming decision already listed in [docs/decisions/index.md](index.md) a
  real, citable ADR file, consistent with how ADR-0004 through ADR-0010 are recorded.
- Record the etymology as documented: **AI** + **isotropy** (physics: uniform behavior
  regardless of direction/orientation).

**Non-goals**

- **Not** re-opening the naming decision or proposing alternatives — it is already shipped
  throughout the codebase, docs, and binary name.
- **Not** a trademark, branding, or logo decision — none of that is recorded anywhere in this
  knowledge base.

## Options considered

Only one candidate name survives in the published source material: **Aisotropy**. No
alternative names, spellings, or rejected candidates are recorded in
[docs/decisions/index.md](index.md) or anywhere under `docs/vision/`. Unlike ADR-0004/0005/
0006/0007 (which document multi-option research with a comparison table), this decision has no
"Option B" to compare against in the written record.

### Option A — "Aisotropy" (AI + isotropy) — accepted

- Portmanteau of **AI** and **isotropy** — a physics term for a property that is identical in
  every direction, independent of orientation.
- Used consistently as the project's declared identity in [docs/index.md](../index.md): *"an
  AI agent framework/runtime that is multi-tenant, local-first, sovereign, and anti-magic."*

## Trade-off comparison table

Not applicable. With a single recorded option and no alternative candidates preserved in the
knowledge base, there is nothing to compare — see Open Questions below for the resulting gap.

## Recommended decision

Keep **Aisotropy** as the project name. It is already the name used throughout the codebase
(`cmd/aisotropyd`), the environment variable convention (`AISOTROPY_*`, e.g.
`AISOTROPY_ADDR`, `AISOTROPY_DATA_DIR`), the README, and every document in this knowledge base.
There is no competing option on record and no indication in the surviving material that the
name is in question.

## Consequences

**Positive**

- A single, consistent identity across code, config, docs, and the binary name — no naming
  drift to reconcile.
- The name is short, unclaimed in this domain, and encodes the project's stated intent (uniform
  behavior/isolation, see the multi-tenant isolation guarantees in
  [multi-tenancy](../architecture/multi-tenancy.md)), though that connection between the name
  and the isolation guarantee is an inference from the term "isotropy," not a stated rationale
  in the source material.

**Negative / technical debt**

- None identified specifically to the naming choice itself.

## Open questions

1. **Missing original rationale.** The two source design-conversation transcripts that produced
   this decision are intentionally not published in this repository (per
   [docs/index.md](../index.md#primary-sources)). Whether "isotropy" was chosen for a specific
   stated reason (e.g., the uniform multi-tenant isolation guarantee, uniform behavior across
   providers) or purely as wordplay on "AI" is not recoverable from what's published. If that
   context resurfaces, this ADR should be updated rather than left to imply a rationale that
   was never actually recorded.
2. **Alternative names considered.** Not recorded anywhere in the published knowledge base. If
   they existed in the original transcripts, they are not part of this retroactive record.

## References

- [docs/decisions/index.md](index.md) — original "accepted" ledger entry for ADR-0000.
- [docs/index.md](../index.md) — "Framework name: Aisotropy (decided)" and the note that the
  origin transcripts are not published in this repository.

---

Cross-links: [decisions index](index.md) · [knowledge base root](../index.md)
