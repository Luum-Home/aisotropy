---
type: Section Index
title: Observability example
description: The docker-compose observability overlay for aisotropy — the OTel collector/backend wiring you layer on top of the runtime to see spans and metrics.
tags: [examples, observability, otel, docker-compose]
timestamp: 2026-08-03T00:00:00Z
---

# Observability example

This subsection has no Markdown documents; it ships a single runnable overlay asset.

| Asset | What it covers |
|---|---|
| `docker-compose.observability.yml` | The compose overlay that stands up the OTel collector/backend so aisotropy's emitted spans and metrics have somewhere to land. Layer it on top of the runtime. |

See the [Observability design](../../architecture/observability-design.md) and
[Observability](../../architecture/observability.md) architecture docs for what the runtime emits.
