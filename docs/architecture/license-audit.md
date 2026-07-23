---
type: Reference
title: "Go dependency + compose-image license audit"
description: The constitutional license gate applied to the repo's Go module graph (root + 23 nested modules — arenas/*, bench/*, examples/*, external/gollem) and every docker-compose*.yml image. Result — 0 BLOCKER, 2 documented exceptions, reproducible with scripts/license-audit.sh.
tags: [licenses, go, docker, supply-chain, constitutional-gate, license-policy]
timestamp: 2026-07-23T00:00:00Z
---

# Go dependency + compose-image license audit

This is the Go/Docker sibling of [`rust-dependency-licenses.md`](rust-dependency-licenses.md) — the
constitutional license-gate review ([license-policy](../../.claude/rules/cos/license-policy.md),
[constitutional-gates](../../.claude/rules/constitutional-gates.md)) applied to the **Go module
graph** (24 real `go.mod` files: the root module plus `arenas/*`, `bench/*`, `examples/*`,
`external/gollem`, `arenas/live/*`) and every `image:` line in every `docker-compose*.yml`.

## Method (reproduce anytime)

```bash
scripts/license-audit.sh
```

Read-only, deterministic, no network calls, no `go mod download` (a module missing from
`$(go env GOMODCACHE)` is reported as `MISSING_FROM_CACHE`, never auto-fetched — populate the cache
first with `go mod download` in the relevant module directory if a full run is needed). For each
direct (non-`// indirect`) `require` in each `go.mod`, resolves the module in the local Go module
cache, looks for a `LICENSE`/`COPYING` file, and greps it for prohibited copyleft/source-available
text (`GNU AFFERO|GNU GENERAL PUBLIC|Server Side Public|Business Source|Elastic License|Commons
Clause|Functional Source`). Separately, checks every compose `image:` against an explicit allowlist
derived from the compose files themselves.

## Result (2026-07-23) — PASS: exit 0

```
go.mod files scanned:        24
direct deps enumerated:      98
first-party (local replace): 13   (replaced with a local filesystem path — same repo, not audited)
whitelisted (known, docs'd): 2    (see below)
SAFE (permissive license):   83
findings from this pass:     0

compose files scanned: 5
images found:          12
images allowlisted:    12
```

**0 BLOCKER.** No prohibited license text was found in any resolved `LICENSE`/`COPYING` file, and
every compose image is on the allowlist.

## The 2 documented exceptions (not blockers — verified, not silently passed)

- **`github.com/fugue-labs/gollem-langsmith@v0.0.2`** — no `LICENSE` file upstream (verified
  2026-07-23 against the packaged module and the GitHub repo). **Not an aisotropy dependency**:
  absent from the root `go.mod`/`go.sum`; pulled only by `external/gollem`'s own CLI tooling.
  Risk: low, upstream's responsibility. **Operator decision (2026-07-23): documented in-repo,
  not reported upstream.** Tripwire: if this module ever enters the root `go.mod`/`go.sum` or is
  imported by aisotropy-owned code, remove the whitelist entry and treat it as a **BLOCKER** until
  upstream publishes a LICENSE (no license = no rights granted). See
  [ADR-0067](../decisions/0067-grafana-agpl-exception-observability-stack.md) "Adjacent 2026-07-23
  license-audit verdicts" for the full writeup.
- **`github.com/cloudwego/eino-ext/components/model/openai@v0.1.13`** — a monorepo nested-module
  packaging artifact: the `eino-ext` repo root **is Apache-2.0** (verified 2026-07-23 via
  `gh api repos/cloudwego/eino-ext/license` → `spdx_id: apache-2.0`), but Go's module zip for this
  per-directory nested module does not include the repo-root `LICENSE` file — so the script's
  per-module scan reports `UNKNOWN` for a module that is, in fact, Apache-2.0. Whitelisted in
  `scripts/license-audit.sh` with the verification command inline; re-run that command if the
  pinned version bumps.

Both exceptions are hardcoded, commented whitelist entries in `scripts/license-audit.sh` (not a
blanket suppression) — a *new* unresolved module still fails the gate.

## Docker image allowlist

`scripts/license-audit.sh` checks every `image:` in `docker-compose.yml`,
`docker-compose.sleepcycle.yml`, `test/proxy/docker-compose.yml`, and
`deploy/observability/docker-compose.observability.yml` against an explicit allowlist. One entry is
a deliberate copyleft exception, not an oversight:

| Image | License | Status |
|---|---|---|
| `grafana/grafana` | AGPL-3.0 | **Exception** — [ADR-0067](../decisions/0067-grafana-agpl-exception-observability-stack.md) |
| `mcr.microsoft.com/dotnet/aspire-dashboard` | MIT | Allowed — [ADR-0065](../decisions/0065-local-otel-backend-aspire-dashboard.md) |
| everything else (`qdrant/qdrant`, `rqlite/rqlite`, `otel/opentelemetry-collector-contrib`, `prom/*`, `json-exporter`, `caddy`, `nginx`) | Apache-2.0 / MIT / BSD-2 | Allowed — no exception needed |

An image not on this list is a **finding** (exit 1) — add it to the allowlist in
`scripts/license-audit.sh` only after checking its license, and cite the check inline as a comment.

## Non-goal of this pass: `k6`

`bench/streaming/README.md:83` cites `k6` as AGPL-3.0 — confirmed correct against upstream
(`github.com/grafana/k6`, verified 2026-07-23). `k6` is invoked as an **external CLI binary**
(`k6 run …`), never imported as a Go dependency (absent from any `go.mod`/`go.sum` in this repo), so
it never appears in this audit's Go-dependency sweep and needs no allowlist/exception entry —
recorded here, and in [ADR-0067](../decisions/0067-grafana-agpl-exception-observability-stack.md),
so it isn't mistaken for an unreviewed gap.

## Conclusion

The Go/Docker half of the stack upholds the repo's "100% permissive (MIT/BSD/Apache-2.0/ISC),
copyleft only by explicit exception" posture: 0 BLOCKER, 2 fully-documented exceptions (one out of aisotropy's dependency
graph entirely, one a false-positive of Go's monorepo module packaging — not a real license risk),
and 1 deliberate, ADR-backed copyleft exception in the Docker image layer (Grafana). Re-run
`scripts/license-audit.sh` whenever a `go.mod` or `docker-compose*.yml` changes.

## Registry verification of the "ecosystem-knowledge" verdicts (closed 2026-07-23)

The original pass left 14 verdicts backed only by ecosystem knowledge (no local cargo cache, no
venv for 4 Python manifests, 2 Windows-only Go modules never downloaded on this machine). All 14
were re-verified against their registries the same day — **every one permissive, zero surprises**:

| Dependency | Registry says | Source |
|---|---|---|
| candle-core / candle-nn / candle-transformers | MIT OR Apache-2.0 | crates.io API |
| tokenizers | Apache-2.0 | crates.io API |
| serde / serde_json | MIT OR Apache-2.0 | crates.io API |
| datafusion | Apache-2.0 | crates.io API |
| tokio | MIT | crates.io API |
| pipecat-ai | BSD-2-Clause | PyPI JSON API |
| httpx | BSD-3-Clause | PyPI JSON API |
| attrs | MIT | PyPI JSON API |
| python-dateutil | Dual Apache-2.0/BSD-3-Clause | PyPI JSON API |
| github.com/erikgeiser/coninput | MIT | GitHub license API |
| github.com/mattn/go-localereader | MIT | GitHub license API |

Reproduce (read-only; crates.io requires a User-Agent):

```bash
curl -s -A "license-audit" https://crates.io/api/v1/crates/tokio | jq -r '.versions[0] | "\(.num) \(.license)"'
curl -s https://pypi.org/pypi/pipecat-ai/json | jq -r '.info | "\(.version) \(.license_expression // .license)"'
curl -s https://api.github.com/repos/erikgeiser/coninput/license | jq -r .license.spdx_id
```

Resolved-version evidence (2026-07-23, closing the latest-vs-resolved caveat): `cargo fetch` +
`cargo metadata --offline` in each of the 4 crates confirms the licenses at the **exact resolved
versions** of the direct deps — candle-core/nn/transformers 0.9.2 (MIT OR Apache-2.0), serde
1.0.228 / serde_json 1.0.150 (MIT OR Apache-2.0), tokenizers 0.20.4 (Apache-2.0), datafusion
43.0.0 (Apache-2.0), tokio 1.52.3 (MIT). Reproduce:

```bash
cd crates/<crate> && cargo fetch -q && cargo metadata --format-version 1 --offline   | jq -r '.packages[] | "\(.name) \(.version): \(.license)"'
```

## Enforcement (wired 2026-07-23 — no longer memory-dependent)

The re-run is no longer something to remember; it fires automatically at both choke points:

- **Local, at commit time**: `scripts/install-hooks.sh` symlinks `.git/hooks/pre-commit` →
  `scripts/license-gate-precommit.sh`, a **conditional** gate: it runs
  `license-audit.sh --root-only` (~1s) only when the staged changes touch a dependency/image
  manifest (`go.mod`/`go.sum`, `docker-compose*.yml`, `Dockerfile*`, `Cargo.*`,
  `requirements*.txt`, `package.json`, `pyproject.toml`); every other commit pays ~0ms.
  Bypass (loud, one commit): `SKIP_LICENSE_AUDIT=1 git commit …` or `--no-verify`.
- **CI, on every push/PR**: the `License audit (root module + compose images)` step in
  `.github/workflows/ci.yml` runs `license-audit.sh --root-only` right after `go build` (so the
  module cache is populated) — a contributor without the local hook still cannot land an
  unreviewed dependency or image on `main`.
- **Scope note**: both gates use `--root-only` (shipped runtime + all compose images), matching
  CI's "only the root module is gated" philosophy. The nested demo modules (`arenas/*`,
  `examples/*`, `bench/*`) stay covered by the manual full sweep (`scripts/license-audit.sh`
  with no flag), which remains the audit-cadence tool.

Verified live 2026-07-23: positive path (staged `requirements*.txt` → gate triggers → 29 root
deps + 12 images → exit 0) and negative path (staged compose file with an unlisted image →
`UNLISTED_IMAGE` finding → exit 1, commit blocked).
