---
type: Section Index
title: Guides
description: Task-oriented guides for building on Aisotropy — starting with how a third party defines their own agent against the public extension surface (Phase 8).
tags: [guides, howto, adoption]
timestamp: 2026-07-09T00:00:00Z
---

# Guides

Practical, task-oriented walkthroughs (distinct from the [architecture](../architecture/index.md)
concepts and the [decisions](../decisions/index.md) log).

## Documents

- [Build your own agent](build-your-own-agent.md) — Define a bot on top of Aisotropy from your own
  Go module, without forking: register a tool, a real executor, and a persona against the public
  `aisotropy` package (Phase 8 / [ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md)).
- [Importing Agent Skills](importing-agent-skills.md) — Map an open `SKILL.md` skill folder to
  Aisotropy's typed primitives + a **literal** persona (no hidden injection), with the `cmd/skill-import`
  PoC ([ADR-0022](../decisions/0022-agent-config-standards-import-surfaces.md)).
- [Writing a plugin](writing-a-plugin.md) — Ship a capability without recompiling the core: the
  `aisotropy-plugin.yaml` manifest, the trust model, scope-gating, and the license gate, with a
  runnable `kind: mcp` template ([`examples/plugin-template`](../../examples/plugin-template),
  ADR-0016 lever #2).
