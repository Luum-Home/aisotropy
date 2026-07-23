---
type: ADR
title: "ADR-0065 — Local OpenTelemetry backend: .NET Aspire Dashboard (MIT), not OpenObserve (AGPL) or HyperDX (heavy)"
description: aisotropyd is already OTLP-native and backend-agnostic (otlptracehttp + a redacting exporter, gated by OTEL_EXPORTER_OTLP_ENDPOINT), so the local trace-viewer is purely an operator choice. This ADR picks the standalone .NET Aspire Dashboard for local development because it is MIT-licensed (gate-clean — OpenObserve is AGPL-3.0 and blocked by the license gate; HyperDX is MIT but ~13x the idle RAM and ~8x the disk), OTLP-native (zero code change), and its ephemeral in-RAM persistence is a feature for local dev. Production stays backend-agnostic.
tags: [decisions, adr, observability, opentelemetry, otlp, aspire-dashboard, local-dev, license-policy]
timestamp: 2026-07-23T00:00:00Z
supersedes: []
revisits: []
---

# ADR-0065 — Local OTel backend: .NET Aspire Dashboard

## Status

**✅ Accepted (2026-07-23).** For **local development**, aisotropyd's OTLP traces are viewed in the
**standalone .NET Aspire Dashboard** (a single MIT-licensed container). This is an operator /
dev-infra choice only — **no product code changes**, because the code is already OTLP-native and
backend-agnostic (see Context). **Production stays backend-agnostic**: any OTLP collector/backend
the operator points `OTEL_EXPORTER_OTLP_ENDPOINT` at works unchanged.

## Context (code-truth: we are already OTLP-native and backend-agnostic)

`internal/llm/otel.go` builds an OpenTelemetry `TracerProvider` whose exporter is the standard
**`otlptracehttp`**, activated only when `OTEL_EXPORTER_OTLP_ENDPOINT` /
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set (unset → no-op tracer, `runtime-as-built.md:587`).
Spans carry `gen_ai.*` semantic-convention attributes plus `tenant_id`/`user_id`. A
**`redactingExporter`** wraps the OTLP exporter and strips prompt/response content before export
(the P0 telemetry-content fix, commit `7b229be`), so sending traces anywhere — including a local
dashboard — never leaks user content. Export fails **open** (falls back to no-op on a
misconfigured collector), so a missing/broken backend never breaks a request.

Consequence: aisotropyd speaks plain OTLP/HTTP to **whatever** listens. Nothing is bundled or
provisioned today (no observability service in `docker-compose.yml`); the runbooks already assume
an external collector (`docs/operations/runbooks.md:267` → `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318`).
So "which backend" is a **deployment choice, not an architecture change** — the only open question
was the *local* viewer.

## Decision

Use the **standalone .NET Aspire Dashboard** container as the local OTLP trace viewer.

### The decisive factor is the license gate, then footprint

The repo's constitutional gate is **100% MIT/Apache/BSD; AGPL/SSPL/BSL are BLOCKED without an ADR**
(`.claude/rules/constitutional-gates.md`, `license-policy.md`). Applied to the three candidates:

| Candidate | License | Verdict | Idle RAM | Disk | CPU idle | Persistence |
|---|---|---|---|---|---|---|
| **Aspire Dashboard** | **MIT** (microsoft/aspire) | ✅ **chosen** | **64 MiB** | **292 MB** | **0.3%** | ephemeral (RAM) |
| HyperDX | MIT | ✅ allowed, rejected on weight | 823 MiB | 2.48 GB | 6.5% | persistent |
| OpenObserve | **AGPL-3.0** (moved from Apache) | 🚫 **blocked by the gate** | ~512 MiB | ~200 MB | — | persistent |

- **OpenObserve is eliminated by the license gate**, not the footprint — its community edition is
  AGPL-3.0 (a commercial enterprise license exists, but adopting the AGPL OSS edition in a
  product repo is exactly what the gate forbids without an explicit exception). Even run as a
  separate service, an AGPL observability backend is a residual-risk we decline when an MIT peer
  exists.
- **Between the two MIT options**, Aspire wins on weight for a laptop dev loop: **~13× less idle
  RAM and ~8× less disk than HyperDX** (footprint numbers measured locally, operator-supplied).
- **Ephemeral (in-RAM) persistence is a feature for local dev**, not a limitation: traces reset
  per session, nothing to prune, no disk growth — you spin it up to inspect the last run and
  throw it away. Long-term retention is a *production* concern, handled by a persistent OTLP
  backend there (unchanged code).

### Wiring (follow-up, no code change)

- Add a **dev-only** Aspire Dashboard service to a compose overlay (e.g.
  `docker-compose.observability.yml`): `mcr.microsoft.com/dotnet/aspire-dashboard`, UI on `18888`,
  OTLP ingest on `18889` (confirm exact port mapping + the startup login token at wiring time).
- Point aisotropyd at it: `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:18889` (or the container
  DNS name under compose).
- The `redactingExporter` continues to strip content — verify a captured span in the dashboard
  shows `gen_ai.*` + `tenant_id`/`user_id` but **no** prompt/response text.

## Consequences

- **Zero product coupling.** The choice lives entirely in dev infra + one env var; swapping to
  Grafana Tempo / Jaeger / a vendor OTLP endpoint later is a config change. We do **not** take a
  dependency on Aspire in code.
- **Gate-clean.** No AGPL/SSPL surface enters the repo; the MIT dashboard is a separate container
  we neither modify nor redistribute.
- **Privacy preserved.** Content redaction (ADR-lineage of commit `7b229be`) means even a local
  dashboard sees only structural spans, never user content — safe for a repo going public and for
  screenshots/demos.
- **Prod unaffected.** Production observability remains an operator decision against the same OTLP
  contract; this ADR scopes only the local viewer.

## Sources

- .NET Aspire (MIT) + standalone dashboard as an OTLP endpoint:
  <https://github.com/microsoft/aspire/blob/main/LICENSE.TXT>,
  <https://learn.microsoft.com/en-us/dotnet/core/diagnostics/observability-otlp-example>
- OpenObserve AGPL-3.0 (Apache → AGPL move):
  <https://openobserve.ai/blog/what-are-apache-gpl-and-agpl-licenses-and-why-openobserve-moved-from-apache-to-agpl/>,
  <https://github.com/openobserve/openobserve/blob/main/LICENSE>
- HyperDX (MIT), footprint comparison context:
  <https://openalternative.co/compare/hyperdx/vs/openobserve>
