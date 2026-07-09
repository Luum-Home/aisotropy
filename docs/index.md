---
type: Knowledge Base Root
title: Aisotropy — Knowledge Base
description: Knowledge base for the multi-tenant, local-first, sovereign AI agent framework in Go+Rust. Structured as an OKF (Open Knowledge Format) bundle.
tags: [aisotropy, root, okf, architecture]
timestamp: 2026-07-08T00:00:00Z
---

# Aisotropy — Knowledge Base

> **Aisotropy** is an AI agent framework/runtime that is **multi-tenant, local-first,
> sovereign, and anti-magic**, built in **Go + Rust**.
> It's not "a chat with AI": it's a precision engineering engine where the *Go struct is
> the single source of truth* that governs HTTP validation, the OpenAPI schema, and the
> contract enforced on the LLM, all at once. See the [thesis](vision/thesis.md).

## How to read this knowledge base

This folder **is not a pile of loose design docs**: it's an **OKF bundle**
([Open Knowledge Format](vision/principles.md#open-knowledge-format-okf), Google Cloud v0.1) — a directory of
markdown with YAML frontmatter, an `index.md` per level, and cross-references that form a
knowledge graph. It is both human-readable and consumable by an agent as its curated
knowledge base (Karpathy's "LLM-wiki" pattern, which **replaces RAG** for stable
knowledge). The very framework we're documenting uses this same pattern for its memory —
this doc is dogfooding of the architecture.

**Writing convention:** every document starts with YAML frontmatter (`type` mandatory;
`title`, `description`, `tags`, `timestamp` standard) and links to related concepts with
relative markdown links. See [principles](vision/principles.md).

## Knowledge map

| Section | What it contains |
|---|---|
| [Vision](vision/index.md) | The thesis, the *why*, the principles (Karpathy + OKF), **the compression manifesto (Bellard)**, the glossary. |
| [Research bets](research/index.md) | **The revolutionary layer:** compression as the cognitive core — 6 actionable bets with success criteria measured in bits. |
| [Architecture](architecture/index.md) | The "LLM OS" model, request lifecycle, tiered memory, consistency, multi-tenancy, granular control, observability. |
| [Components](components/index.md) | Deep dive per piece of the stack: Huma, Gollem, BadgerDB, Qdrant, rqlite/LiteFS, DataFusion. |
| [Decisions (ADRs)](decisions/index.md) | Record of architectural decisions with trade-offs. |
| [Guides](guides/index.md) | Task-oriented how-tos, starting with **building your own agent** on the public extension surface. |
| [Roadmap](roadmap/index.md) | Executable MVP-first plan with OKRs and milestones. |

## Primary sources

This knowledge base distills the project's two origin design conversations (raw brainstorm
transcripts). Those transcripts are kept locally as source material and are intentionally not
published in this repository.

## Status

- **Phase:** Phase 0–1 of the roadmap implemented — contracts + walking skeleton with real SSE streaming. See the [roadmap](roadmap/index.md).
- **Framework name:** **Aisotropy** (decided). See [ADR-0000](decisions/index.md).
- **Stack:** closed and 100% MIT/Apache-2.0 (zero BSL). See [architecture](architecture/overview.md).
