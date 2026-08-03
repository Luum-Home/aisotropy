# Contributing to Aisotropy

Thanks for looking at Aisotropy. It's an early (0.x) project — a multi-tenant,
local-first AI-agent runtime in Go — with **no production users yet**. That's a
good thing for a contributor: the surface is still small, the conventions are
written down, and a well-scoped PR can land quickly. This guide covers how to
build, test, and lint locally, the house rules that keep the codebase honest,
and how to propose a larger change.

## Prerequisites

- **Go** — the exact version is pinned in [`go.mod`](go.mod) (`go 1.25.1` at time
  of writing). CI reads it from there via `go-version-file`; use the same.
- **Docker** (optional) — only needed to run the sandbox / networked-memory
  integration tests. Without it, those tests **skip** (they never fake — see
  below); everything else still runs.
- **git** — a couple of sandbox tests shell out to `git`; they skip if it's not
  on `PATH`.

The default build is a single self-contained binary: no external services are
required to build, test, or run the in-process defaults (BadgerDB working
memory, in-process SQLite+FTS5 facts, in-process cosine semantic search).

## Build, test, lint

The repo root is one Go module. The `arenas/*` and `examples/*` directories are
**separate modules** (each has its own `go.mod`) — runnable demos, not part of
the shipped runtime — so the root commands below do not descend into them.

```bash
# Build everything
go build ./...

# Format check (fix in place with: gofmt -w <dirs>)
gofmt -l internal aisotropy cmd/aisotropy

# Static analysis
go vet ./...

# Unit + integration tests, with the race detector and coverage
go test ./... -race -coverprofile=coverage.out -covermode=atomic

# Coverage floor over the promoted packages (internal/... + aisotropy + cmd/aisotropy)
.github/scripts/coverage-floor.sh coverage.out 70
```

These are exactly the steps CI runs (see
[`.github/workflows/ci.yml`](.github/workflows/ci.yml)). If they pass locally,
CI should be green.

### Docker-gated tests

Tests that need a real container or a networked backend (Docker sandbox, live
rqlite, live Qdrant, the Rust sleep-cycle worker) follow a strict rule:
**skip, never fake.** If Docker/the service isn't present, the test calls
`t.Skip(...)` with a clear reason instead of stubbing out the dependency and
pretending it passed. So:

- On a machine **with** Docker, `go test ./...` exercises the real container
  round-trips (including the key-brokering invariant — the agent sandbox never
  receives `ANTHROPIC_API_KEY`).
- On a machine **without** Docker, the same command passes but those tests
  report as skipped. CI runs on `ubuntu-latest`, which has Docker, so the gated
  tests actually execute there.

### `manual` / live-LLM tests

Some tests hit a real model provider or a manually-provisioned service. They sit
behind the `manual` build tag (`//go:build manual`) and are **off by default**.
Run them explicitly only when you've set the relevant env vars (e.g.
`ANTHROPIC_API_KEY`, `QDRANT_HOST`, `RQLITE_URL`):

```bash
go test -tags manual ./internal/semantic/...
```

### The coverage floor

CI enforces a weighted statement-coverage floor over the **promoted** packages
only — `internal/...`, `aisotropy/`, and `cmd/aisotropy/` — excluding thin CLI
mains and demo/bench code that are intentionally not unit-tested. The floor is a
**ratchet**: it starts conservative (70%, just under the current tree) and is
raised as coverage lands, toward the project's goal of ~98% on business logic.
If you add real logic, add real tests for it; don't write tests-for-the-metric.

## House rules

Aisotropy has two load-bearing design commitments. New code is expected to honor
them, and reviewers will push back if it doesn't.

### Anti-magic ("the struct is the contract")

No opaque abstractions. A single Go struct is the single source of truth that
drives HTTP validation, the generated OpenAPI 3.1 schema, and the contract
enforced on the LLM — all at once. Prefer explicit, readable wiring over
reflection-heavy framework magic. See
[ADR-0002](docs/decisions/0002-anti-framework-magic-struct-as-contract.md).

### Isolation by construction

Multi-tenancy is a hard invariant, not a feature flag:

- `tenant_id` travels **only** via `context.Context`, injected once at the HTTP
  handler boundary from the validated path param. Never read tenant identity
  from a body/header/query as the authorization source.
- Every read/write to a memory tier (working, facts, semantic) must be
  scoped/filtered by tenant. For vector search this means payload filtering
  *during* the scan, not a post-filter.
- No code path holds two tenants' data in the same buffer/goroutine scope
  without an explicit, reviewed reason.

If your change touches tenant isolation, the memory tiers, or the LLM call path,
expect extra scrutiny and include a test that proves the boundary holds.

### Dependency & license policy

The stack is deliberately **100% permissive (MIT / BSD / Apache-2.0 / ISC)**. Do not add a dependency
under a copyleft (GPL/AGPL) or source-available (BUSL/SSPL/ELv2) license without
an explicit ADR justifying it. Prefer the standard library; when a test needs to
stand in for an external service, use `net/http/httptest` and small in-memory
fakes rather than pulling in a mocking library.

## Proposing a larger change (ADRs)

Architecture-level decisions are recorded as ADRs under
[`docs/decisions/`](docs/decisions/), numbered sequentially (the latest is
`0038-*`). Each ADR follows a **Context → Options considered → Decision →
Consequences** structure (with explicit goals/non-goals and discarded
alternatives). To propose one:

1. Copy the shape of a recent ADR (e.g.
   [`0038-pluggable-sandbox-providers.md`](docs/decisions/0038-pluggable-sandbox-providers.md)).
2. Number it `NNNN-short-slug.md` (next free 4-digit number).
3. Add a row to [`docs/decisions/index.md`](docs/decisions/index.md).
4. Open a PR with the ADR and, ideally, the change it motivates.

For a small bug fix or a scoped test/coverage improvement, you don't need an
ADR — just open the PR.

## Commit & PR style

- Conventional-commit-style subjects are used throughout the history, e.g.
  `feat(api): ...`, `fix(memory): ...`, `test(facts): ...`, `docs: ...`,
  `chore: ...`. Keep the subject in the imperative mood and under ~72 chars.
- Reference the relevant ADR in the body when a change implements or revises one.
- Keep PRs focused; a green `go build ./...`, `go vet ./...`, `gofmt`, and
  `go test ./...` is the bar for review.

## License

The project is licensed under the [Apache License 2.0](LICENSE) ([ADR-0080](docs/decisions/0080-project-license-apache-2.md)).

Per **Apache-2.0 §5**, any contribution you intentionally submit for inclusion is
licensed under those same terms, unless you explicitly state otherwise. If your
employer holds rights in work you contribute, make sure you have their
permission before opening the PR.

**There is nothing to sign.** Code contributions are open under Apache-2.0 §5:
submit a PR and your contribution is licensed under the same terms as the
project. No CLA, no DCO, no paperwork.

You will find two CLA drafts in [`docs/legal/`](docs/legal/) — **ignore them.**
They are unreviewed drafts kept in the open so the eventual legal review has a
concrete object; both carry a DO-NOT-SIGN banner and neither is in force.
Nobody should sign anything today, and nobody will be asked to sign anything
retroactively.

**What this means for your contribution, stated plainly, because it is
permanent.** Under §5 the project may distribute your code under Apache-2.0 and
nothing else. If a CLA is introduced later (it may be — see
[ADR-0080](docs/decisions/0080-project-license-apache-2.md)), it will govern
contributions made *after* it exists. Yours stays under the terms you submitted
it under. The practical consequence for the project, not for you: code merged
before that point cannot be offered under any other licence without asking you
first — which is a cost the project accepts in exchange for being open to
contributions now.

If you want the reasoning behind all of this — why Apache-2.0, why a CLA is
still on the table, and what the drafts would change if one ever lands — it is
in [`docs/legal/`](docs/legal/README.md) and
[ADR-0080](docs/decisions/0080-project-license-apache-2.md). Reading it is
optional; contributing today requires nothing from you but the PR.

Two things the license does *not* cover, and that a contribution must respect:

- **`NOTICE`** is part of the distribution (§4(d)). If your change adds a
  dependency that ships its own NOTICE, add it to
  [`THIRD-PARTY-NOTICES`](THIRD-PARTY-NOTICES).
- **`bench/rag-quality/testdata/xquad_slice.json`** is CC-BY-SA-4.0, not
  Apache-2.0 — see [`bench/rag-quality/LICENSE-DATA.md`](bench/rag-quality/LICENSE-DATA.md).
