---
type: Guide
title: Supply-chain hardening — local gates + digest-pinned images
description: The supply-chain security posture — govulncheck + dependency scanning as a local pre-push git hook (no extra paid GitHub Actions), base images pinned by immutable @sha256 digest, and secrets fenced out of the Docker build context.
tags: [deployment, security, supply-chain, govulncheck, docker, hooks]
timestamp: 2026-07-17T00:00:00Z
---

# Supply-chain hardening

This repo already has strong **correctness** gates in `.github/workflows/ci.yml`
(gofmt · vet · build · `test -race` · coverage floor). This document covers the
**supply-chain** gates layered on top of that, and why they run where they run.

## Local-first, not more paid Actions

The supply-chain checks run as a **local git hook**, not as additional GitHub
Actions steps. The existing CI workflow already consumes paid Actions minutes on
the correctness gate; adding `govulncheck`, SBOM generation, and dependency
scanning as more Action jobs would multiply that cost. Running them locally,
before a push leaves the machine, gives the same protection with no extra hosted
minutes. This local-first CI posture — hosting new quality/supply-chain gates as
git hooks rather than more paid Actions — is the decision recorded in
[ADR-0043](../decisions/0043-local-first-ci-gates-git-hooks.md). (The audit that
surfaced these gaps sits alongside the authn/ops/TLS hardening of
[ADR-0025](../decisions/0025-security-hardening-authn-ops.md); that ADR is the
security lineage, not the CI-hosting decision.)

Trade-off, stated honestly: a local hook only protects developers who installed
it, and it can be bypassed (`--no-verify`). It is a fast, zero-cost first line of
defence, not an un-bypassable server-side gate. If a hosted enforcement point is
ever wanted, the same `scripts/supply-chain-guard.sh` can be called from a runner
without change — the logic lives in the script, not in any workflow.

## Install the gate

Git hooks live in `.git/hooks/` and are **not** tracked by git, so the committed,
reviewable artifacts are the scripts under `scripts/` plus the installer. Run once
after cloning:

```bash
scripts/install-hooks.sh
```

This symlinks `scripts/supply-chain-guard.sh` into `.git/hooks/pre-push`
(idempotent; an unrelated existing hook is backed up, not clobbered).

## What the gate checks (`scripts/supply-chain-guard.sh`)

| # | Check | Blocking? | What it does |
|---|-------|-----------|--------------|
| 1 | `govulncheck ./...` | **Yes** — non-zero exit fails the push | Reuses the exact `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` invocation already defined in `cmd/x/analysis.go` (`govulncheckCmd`). `govulncheck` exits non-zero **only** when a vulnerability is reachable from code this module actually calls, so an advisory in an unused transitive dep does not block — low false positives by design. |
| 2 | Dependency staleness | No — advisory | `go list -m -u all` prints modules with a newer version available. This is the local stand-in for Dependabot, which is GitHub-hosted and intentionally **not** enabled here. Review and bump deliberately. |
| 3 | Build-context secret scan | No — advisory | Flags tracked files matching the patterns `.dockerignore` excludes (`.env`, `*.pem`, `*.key`, `*secret*`), so a secret is noticed before a `COPY . .` in any builder stage could bake it into an image layer. |

Only the **root** module is scanned (`internal/…`, `aisotropy`, `cmd/…`). The
`arenas/*` and `examples/*` directories are separate Go modules — runnable demos,
not the shipped runtime — and are out of scope, matching the note in `ci.yml`.

**Bypass (use sparingly):**

```bash
SKIP_SUPPLY_CHAIN_GUARD=1 git push …   # or the git built-in: git push --no-verify
```

## Base images pinned by digest

`Dockerfile` and `deploy/Dockerfile.sleepcycle` pin every `FROM` by immutable
`@sha256:` digest. A moving tag (`golang:1.25`, `debian:bookworm-slim`) can be
repointed at a different — potentially malicious — layer at any time; a digest
cannot. The readable tag is kept inline so the digest's provenance is obvious and
so Dependabot's `docker` ecosystem could propose digest bumps if it is ever
enabled.

| Image | Tag | Pinned digest |
|-------|-----|---------------|
| `golang` | `1.25` (go1.25.12) | `sha256:d2e20dc1b35aefd666909163e4ace41efb521359aa2ce31fff59d86837050f6f` |
| `gcr.io/distroless/static-debian12` | `nonroot` | `sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b` |
| `rust` | `1-bookworm` | `sha256:77fac8b98f9f46062bb680b6d25d5bcaabfc400143952ebc572e924bcbedc3fa` |
| `debian` | `bookworm-slim` | `sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818` |

The Go builder was moved from `golang:1.26` to `golang:1.25` to **align with
`go.mod`'s `go 1.25.1` directive** (and with CI, which resolves its Go from
`go-version-file: go.mod`) — the container build now uses the same language
version the module declares. The digests are multi-arch manifest-list digests, so
they still resolve per-platform (amd64/arm64).

Refresh a digest when bumping a tag:

```bash
docker pull golang:1.25
docker inspect --format '{{index .RepoDigests 0}}' golang:1.25
```

## Secrets kept out of the build context

`.dockerignore` excludes `.env`, `.env.*`, `*.pem`, `*.key`, and `*secret*` from
the Docker build context. Without this, a `COPY . .` in any builder stage would
copy a stray secret into an image layer — recoverable from the image history even
if a later stage appears to remove it. This is belt-and-suspenders with the
advisory secret scan in the pre-push gate above.

## Deliberately deferred

- **Image signing (cosign / Sigstore):** not added in this pass. Signing is most
  valuable once images are published to a registry consumers pull from; it adds
  key-management and a verification step at deploy time that this local-first,
  self-hosted posture does not yet need. It is a deliberate omission, not an
  oversight — revisit when images are published for third-party consumption.
- **SBOM generation:** an SBOM is only useful when tied to a published,
  distributed artifact (to answer "what is in the image you shipped?"). Until
  images are published, the dependency inventory is fully reconstructable from
  the committed `go.mod`/`go.sum` and `Cargo.lock`. Generate one on demand with
  `syft` against a locally built image when a distribution point exists.
- **Dependabot:** GitHub-hosted, same paid-automation category being avoided.
  Check #2 above (`go list -m -u all`) is the local equivalent for surfacing
  stale dependencies.
