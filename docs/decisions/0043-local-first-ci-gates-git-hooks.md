---
type: ADR
title: "ADR-0043 — Local-first CI gates: git hooks over paid GitHub Actions"
description: New quality and supply-chain gates (govulncheck, dependency-staleness, build-context secret scan, and the examples-module build/vet check) run as LOCAL git hooks installed under .git/hooks/, not as additional paid GitHub Actions jobs. Operator cost decision — Actions minutes are billed, and the existing ci.yml correctness gate already spends them; growing that surface with more Action jobs multiplies the cost. Hooks are a fast, zero-marginal-cost first line, accepted as opt-in and bypassable.
tags: [decisions, adr, ci, git-hooks, supply-chain, govulncheck, cost, tooling]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0043 — Local-first CI gates: git hooks over paid GitHub Actions

## Status

✅ **Accepted (2026-07-17).** Operator-approved. Supersedes no prior decision; governs how
*new* quality/supply-chain gates are hosted. The existing correctness workflow
(`.github/workflows/ci.yml` — gofmt/vet/build/`test -race`/coverage) is unchanged.

## Context

A security readiness audit surfaced missing supply-chain instrumentation (no `govulncheck`, no
dependency-staleness check, no build-context secret fence) and the [example modules'](../architecture/adoption-surface.md)
silent-rot gap (the nested `examples/*` modules are separate Go modules that the root
`go test ./...` deliberately does not descend into, so a public-API break in `aisotropy/` does not
fail any gate).

The obvious home for these gates is GitHub Actions. But **Actions minutes are billed**, and the
existing `ci.yml` correctness gate already consumes them on every push/PR. Adding `govulncheck`,
SBOM generation, dependency scanning, and a per-example-module build as more Action jobs multiplies
that recurring cost for a small team. The operator directed: **do not grow paid-Actions usage —
prefer local git hooks.**

## Decision

New quality and supply-chain gates are implemented as **local git hooks + scripts**, not Actions:

- `scripts/supply-chain-guard.sh` — a **pre-push** gate: `govulncheck ./...` (**blocking** — non-zero
  exit fails the push), `go list -m -u all` dependency-staleness (advisory), and a build-context
  secret scan (advisory). Reuses the exact `govulncheck` invocation already defined in
  `cmd/x/analysis.go`.
- `scripts/install-hooks.sh` — an idempotent installer that symlinks the guard into
  `.git/hooks/pre-push`, backing up any pre-existing hook.
- The **examples-module build/vet check** (guarding public-API rot across the nested `examples/*`
  modules) lives in the same pre-push hook, not a workflow.
- **Dependabot** (GitHub-hosted automation, same billed category) is **not** used; the local
  dependency-staleness check substitutes.

Non-Action image hardening is kept regardless of this decision, because it is not an Actions cost:
base images pinned by immutable `@sha256:` digest (`Dockerfile`, `deploy/Dockerfile.sleepcycle`) and
`.env`/`*.pem`/`*.key`/`*secret*` fenced out of the Docker build context via `.dockerignore`.

## Consequences

- **Zero incremental paid-Actions cost** for the new gates.
- **Trade-off (accepted):** a local pre-push hook is opt-in (a developer must run
  `scripts/install-hooks.sh`) and bypassable (`git push --no-verify`). It is a fast first line, not
  a server-enforced gate. For a small team avoiding CI spend, this is the accepted posture; a future
  move to a server-side gate (self-hosted runner, or a paid-Actions job if the budget changes) can
  reuse the same scripts unchanged.
- **The gate is proven non-trivial:** `govulncheck`'s first run blocked on three reachable Go stdlib
  CVEs (`GO-2026-5037` crypto/x509, `GO-2026-5039` net/textproto, `GO-2026-5856` crypto/tls),
  remediated by pinning `toolchain go1.26.5` in `go.mod` and bumping the Docker builder images to the
  patched toolchain.
- The correctness `ci.yml` continues to run as-is; this ADR is the reference for *why* new gates are
  hook-hosted, so a future contributor does not "fix" the missing coverage by adding paid Action jobs.

## References

- `scripts/supply-chain-guard.sh`, `scripts/install-hooks.sh`, `docs/deployment/supply-chain.md`
- [CVE remediation — July 2026 Go stdlib advisories](../security/cve-remediation-2026-07.md) — the
  three reachable CVEs this gate caught on its first run, with authoritative sources and the
  toolchain-pin fix
- [ADR-0025 — Security-review hardening](0025-security-hardening-authn-ops.md) (the audit lineage;
  distinct decision — authn/ops/TLS, not CI hosting)
- [Adoption surface as built](../architecture/adoption-surface.md) (the example-modules rot gap)
