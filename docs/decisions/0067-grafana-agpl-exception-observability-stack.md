---
type: ADR
title: "ADR-0067 — Grafana AGPL-3.0 exception for the local observability stack"
description: The license-policy hard gate (.claude/rules/constitutional-gates.md) blocks any copyleft dependency "without an explicit ADR." deploy/observability/docker-compose.observability.yml already runs Grafana 11.4.0 (AGPL-3.0) as a separate, unmodified, HTTP-only service with a documented Perses (Apache-2.0) swap — the license-policy.md Exceptions shape is satisfied, but the ADR the gate requires did not exist. This ADR is that ADR — it formalizes the Grafana exception condition-by-condition, extends the same exception to the (currently unimplemented) Loki overlay if it ever ships, and records two adjacent 2026-07-23 audit verdicts (k6 external-binary AGPL, gollem-langsmith missing-LICENSE-but-out-of-graph) so the license posture for this pass is traceable from one document.
tags: [decisions, adr, license-policy, agpl, grafana, loki, observability, constitutional-gates, supply-chain]
timestamp: 2026-07-23T00:00:00Z
supersedes: []
revisits: [0065, 0068]
---

# ADR-0067 — Grafana AGPL-3.0 exception for the local observability stack

## Status

**✅ Accepted (2026-07-23).** Grafana 11.4.0 (AGPL-3.0) is approved as the dashboard UI in
`deploy/observability/docker-compose.observability.yml`, under the five conditions enumerated in
Decision below. This is the missing ADR the constitutional gate requires — it does not change any
running config; Grafana was already wired (ADR-0065's follow-up, `docker-compose.observability.yml`)
before this document existed. The exception explicitly **extends to Loki** (same license, same
shape) should the optional log-overlay mentioned in the observability README ever be implemented; it
does **not** need a second ADR to do so.

## Context

### The gate this ADR satisfies

`.claude/rules/constitutional-gates.md` → **License policy (repo-wide, hard gate)**:

> The stack is deliberately **100% MIT / Apache-2.0**… Do not introduce a new dependency with a
> copyleft (GPL/AGPL) or source-available/BUSL license **without an explicit ADR**.

`.claude/rules/cos/license-policy.md` additionally defines an **Exceptions** clause: a restrictive
license "may be considered" only if it runs as a **completely separate service**, its **source is
not modified**, it is consumed only via a **documented public API**, the **residual risk is
documented**, and a **human operator explicitly approves the exception before integration** —
adding: "the preferred decision is to choose a permissively licensed alternative" even when all
conditions hold.

### What is already true on disk (code-truth, not a proposal)

`deploy/observability/docker-compose.observability.yml:106` runs `grafana/grafana:11.4.0`. Grafana
core is **AGPL-3.0** (Grafana Labs relicensed from Apache-2.0 in 2021). The
`deploy/observability/README.md` **Licensing** section already argues the Exceptions shape applies —
separate service, unmodified, HTTP-only, documented, optional, with a Perses swap path — and that
argument is accurate (verified against the compose file and the README's own "Files" inventory: no
Grafana source is vendored anywhere in the repo, `git grep -l grafana -- '*.go'` returns no matches
outside `deploy/observability/`). What was **missing** is the ADR itself: the gate's text is
unconditional ("without an explicit ADR"), and prose in a README does not satisfy a rule that names
the artifact type explicitly. This ADR closes that gap without changing the running stack.

### Why this matters now (Loki)

`deploy/observability/README.md:81` and `deploy/observability/otel-collector/config.yaml:24`
mention **Loki** as an optional overlay for log-derived panels (circuit-breaker trips, quota
rejections, guardrail hits — signals that today have no metric, per the README's "Residual gaps"
§2). Loki is **also AGPL-3.0** (same 2021 Grafana Labs relicensing) and is **not implemented** —
no `docker-compose.*.yml` references it, no config exists. Left as a bare mention, a future agent
could read "Loki" next to "optional overlay" and wire it up believing the license question was
already settled by the Grafana precedent, without checking that Loki's obligations are evaluated
independently (a log-aggregation AGPL service ingesting `slog` output is a materially different
integration than a read-only dashboard UI). This ADR removes that ambiguity: implementing the Loki
overlay is covered by **this same exception**, under the **same five conditions**, and does not
require opening a new ADR discussion — but it does require the implementer to verify the five
conditions still hold for that specific integration (see Decision, condition 5).

## Decision

Accept Grafana (and, prospectively, Loki if implemented) as an **AGPL-3.0 exception** under
`license-policy.md`'s Exceptions clause, subject to all five conditions holding simultaneously:

1. **Separate, unmodified service.** Grafana runs as the stock `grafana/grafana:11.4.0` OCI image
   (`docker-compose.observability.yml:106`). No Grafana source is vendored, forked, or patched in
   this repo — verified: `git grep -rn "grafana" -- '*.go' '*.rs'` returns no matches (Grafana is
   referenced only in `deploy/observability/` config/docs, never in Go or Rust source).
2. **Consumed only via its documented public API/UI.** Interaction is exclusively HTTP: the
   provisioning API for datasources/dashboards (`grafana/provisioning/`) and the browser UI
   (`localhost:3000`). aisotropyd never imports, links, or calls into Grafana as a library — it has
   no awareness Grafana exists; the coupling is one-directional (Grafana reads Prometheus, which
   reads OTLP-derived metrics from aisotropyd's traces).
3. **Optional, not load-bearing.** `AISOTROPY_ADDR`/`aisotropyd` boots and serves production traffic
   with zero knowledge of `deploy/observability/`. The compose file is a separate, operator-invoked
   overlay (`docker compose -f deploy/observability/docker-compose.observability.yml up -d`); nothing
   in `cmd/aisotropyd` or `aisotropy/app.go` references it.
4. **Documented residual risk + permissive alternative.** `deploy/observability/README.md` §Licensing
   states the risk (AGPL-3.0 core, relicensed 2021) and names the swap: **Perses** (Apache-2.0,
   CNCF sandbox project) as a drop-in dashboard UI over the same Prometheus datasource and PromQL —
   Prometheus itself is already Apache-2.0, so no metrics-store migration is needed. This ADR is the
   permanent home for that risk statement; the README should point here (see the companion doc
   change) rather than re-litigate it.
5. **Operator approval, recorded here.** This ADR **is** that approval — the same posture ADR-0065
   already established for the local OTel viewer's license gate (choosing MIT Aspire Dashboard over
   AGPL OpenObserve). Unlike ADR-0065, which resolved the gate by picking a permissive tool, this ADR
   resolves it by **accepting** a restrictive one under the Exceptions clause, because Grafana's
   dashboard/alerting maturity is materially ahead of Perses' today and the isolation conditions
   above hold cleanly. **Extension to Loki**: if/when the Loki overlay in
   `deploy/observability/README.md:81` is implemented, conditions 1–4 must be re-verified against
   the actual Loki wiring (a log *ingester* has a different blast-radius shape than a dashboard
   *reader* — e.g., confirm no aisotropyd code path depends on Loki being up, and that `slog` output
   reaches it only via a log-shipping sidecar/driver, never an in-process Loki client library). If
   conditions 1–4 hold for that wiring, condition 5 (operator approval) is **already satisfied by
   this ADR** — no second ADR is required, only a note in the implementing PR/commit citing
   ADR-0067.

### Rejected alternative: block Grafana, ship Perses now

Considered and rejected for **this** pass — not because Perses is unsuitable (it satisfies the gate
natively, Apache-2.0, no exception needed), but because the existing Grafana dashboards
(`deploy/observability/grafana/dashboards/*.json`: Overview, Cost, Reliability) are already built,
tested against the running stack, and documented in the README's residual-gaps section with
dashboard-specific caveats. Migrating to Perses is a real option and stays open — the README's
"Permissive swap" paragraph is the standing pointer — but redoing three dashboards' worth of JSON to
close an exception that is already narrow, documented, and reversible is not proportionate today.

## Adjacent 2026-07-23 license-audit verdicts (recorded here for traceability)

Two other findings from the same audit pass are **not** exceptions (no copyleft dependency is being
introduced) but are recorded here so the full 2026-07-23 license posture lives in one place:

- **k6 (AGPL-3.0) — confirmed, no action needed.** `bench/streaming/README.md:83` cites k6 as
  AGPL-3.0; verified against `github.com/grafana/k6`'s upstream LICENSE (2026-07-23) — the citation
  is **correct as-is**. k6 is invoked as an **external benchmarking binary** (`k6 run …`), never
  imported, linked, or vendored as a Go dependency (absent from `go.mod`/`go.sum`). Same isolation
  shape as Grafana (separate process, unmodified, CLI-only interface) but does not need this
  exception because it never enters the dependency graph at all — a locally-invoked tool, not a
  linked or embedded component.
- **`github.com/fugue-labs/gollem-langsmith@v0.0.2` — missing LICENSE, out of aisotropy's graph.**
  Verified 2026-07-23: no `LICENSE`/`LICENSE.md` file in the packaged module (`$(go env
  GOMODCACHE)/github.com/fugue-labs/gollem-langsmith@v0.0.2/`) nor in the upstream GitHub repo. It is
  **not** a dependency of aisotropy: absent from the root `go.mod`/`go.sum`, and
  `git grep -rn "gollem-langsmith" -- ':!external'` returns zero matches in code aisotropy owns. It
  is pulled in only by the CLI tooling of the vendored upstream clone at `external/gollem` (a
  dependency of gollem's own tooling, not of aisotropy). Risk is assessed **low** and the
  responsibility is **upstream's** (gollem, not aisotropy). **Operator decision (2026-07-23):
  document in-repo, do not report upstream** — the gap is recorded here and enforced locally
  instead of filed as an external issue. Enforcement is the whitelist entry in
  `scripts/license-audit.sh` plus this **tripwire**: if `gollem-langsmith` ever appears in the
  root `go.mod`/`go.sum`, or any aisotropy-owned code imports it
  (`git grep -rn "gollem-langsmith" -- ':!external'` stops returning empty), the whitelist entry
  MUST be removed and the module treated as a **BLOCKER** until upstream publishes a LICENSE — a
  module with no license grants no rights (all rights reserved by default), so adoption without
  one is not a CAUTION, it is prohibited. This is *not* a license-policy exception (nothing here
  is a copyleft/restrictive license being adopted) — it is a missing-license hygiene gap in a
  transitive dependency of a vendored clone, tracked here so it isn't silently dropped between
  audits.

## Consequences

- **Gate satisfied.** `.claude/rules/constitutional-gates.md`'s "without an explicit ADR" clause is
  now met for Grafana; `scripts/license-audit.sh` (companion to this ADR) treats
  `grafana/grafana:11.4.0` as an **approved exception**, not a blocker, citing this ADR.
- **Loki pre-cleared, conditionally.** Implementing the Loki overlay no longer requires pausing to
  open a license discussion — it requires the implementer to check conditions 1–4 against the actual
  wiring and cite ADR-0067, which is strictly less friction than before while still preventing a
  silent, unreviewed AGPL log-ingester from landing.
- **No code or running-config change.** Grafana was already running under conditions that happen to
  satisfy the Exceptions clause; this ADR is retroactive paperwork closing a process gap, not a
  behavior change.
- **Perses swap stays live as the permissive escape hatch.** Documented in
  `deploy/observability/README.md` §Licensing; revisit if Grafana's AGPL terms change materially or
  if a future audit tightens the gate to disallow exceptions entirely.
- **gollem-langsmith: documented, not reported.** Operator decision 2026-07-23: the
  missing-LICENSE gap is documented in-repo (above) and enforced by the `scripts/license-audit.sh`
  whitelist + tripwire, instead of filing an upstream issue. No code change required; the tripwire
  turns the entry into a BLOCKER the moment the module enters aisotropy's dependency graph.

## Sources

- Grafana relicensing to AGPL-3.0 (2021): <https://grafana.com/licensing/>,
  <https://github.com/grafana/grafana/blob/main/LICENSE>
- Loki license (same AGPL-3.0 family): <https://github.com/grafana/loki/blob/main/LICENSE>
- Perses (Apache-2.0, CNCF sandbox): <https://github.com/perses/perses/blob/main/LICENSE>
- k6 upstream license (verified 2026-07-23): <https://github.com/grafana/k6/blob/master/LICENSE.md>
- gollem-langsmith (no LICENSE found, verified 2026-07-23):
  <https://github.com/fugue-labs/gollem-langsmith>
- `.claude/rules/constitutional-gates.md` §License policy; `.claude/rules/cos/license-policy.md`
  §Exceptions
- ADR-0065 (`0065-local-otel-backend-aspire-dashboard.md`) — the sibling decision that resolved the
  *local OTel viewer* gate by picking a permissive tool instead of an exception; same gate, opposite
  resolution strategy, both valid per `license-policy.md`.
