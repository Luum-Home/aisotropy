# AGENTS.md — instructions for AI coding agents working on aisotropy

> A README **for coding agents** ([agents.md](https://agents.md/) open format). Human docs live in
> `README.md` and `docs/`. This file is the predictable place for the context an AI dev tool needs to
> work on this repo. It is **dev-time context, not a runtime bot-definition format** — how a consumer
> *adds primitives to an aisotropy bot* is Go + MCP + wiring-YAML (see below), not this file.

## What this project is

Aisotropy is a **multi-tenant, local-first, sovereign AI-agent runtime** in Go + Rust — both a
hostable runtime (A) and a composable framework (B) (ADR-0016). Not a chat wrapper: a single Go
**struct is the source of truth** governing HTTP validation, the OpenAPI 3.1 schema, and the LLM
contract at once. Read [`docs/architecture/what-aisotropy-is.md`](docs/architecture/what-aisotropy-is.md)
first if unsure what category of thing this is.

## Build / test / verify commands

```sh
go build ./...                 # build everything (root module)
go test ./...                  # full test suite (fast; excludes //go:build manual)
go vet ./...                   # vet
gofmt -l aisotropy/ cmd/ internal/   # must print nothing
go run ./cmd/x <task>          # dev console / CI dispatcher: build|fmt|vet|lint|test|race|bench|example
( cd crates/sleep-cycle && cargo build --release )      # Rust worker (ADR-0005)
( cd crates/bitrate-scorer && cargo build --release )   # Rust bpt scorer (ADR-0014 Phase 2)
```

- **`//go:build manual`** tests (e.g. `internal/api/*_manual_test.go`, `internal/bitrate/bitrate_manual_test.go`)
  are opt-in, excluded from the default suite, and exercise real external deps (LLM/scorer/Qdrant/rqlite).
  Do **not** add them to the default CI path.
- Nested example modules have their own `go.mod`: `examples/weatherbot`, `examples/helpdesk`
  (build via `go run ./cmd/x example`).

## Non-negotiables (constitutional gates — see `.claude/rules/constitutional-gates.md`)

1. **Anti-magic / struct-as-contract (ADR-0002).** No hidden prompt templating; you build the payload,
   you see every token. Behavior lives in **Go** (typed), never in a declarative file. Config (YAML) is
   **wiring/deployment values only** (`aisotropy/config.go`), never behavior.
2. **Tenant isolation by construction (ADR-0011).** `tenant_id`/`user_id` travel only via
   `context.Context`, injected once at the Huma boundary. A memory `Scope` is obtained **only** through
   `…FromContext(ctx)` — never constructed from raw strings. A composed node never mints a `Scope`.
3. **Sovereignty / license (ADR-0001).** 100% MIT/Apache-2.0 — no GPL/AGPL/BSL/source-available deps,
   ever, without an ADR. Secrets via env vars only; never committed, never logged.
4. **Fail-closed structured output & SSE ctx-cancellation** on the LLM path (constitutional gates).

## How primitives are added (the extension surface — for consumers, not this file)

A third party builds a bot from another Go module against the public `aisotropy` package
([`docs/architecture/adoption-surface.md`](docs/architecture/adoption-surface.md)):
`WithTypedTool[T]`, `WithToolExecutor`, `WithMCPServer` (polyglot tool execution, ADR-0016),
`WithPersona` (a **literal** base prompt), `OptionsFromConfig` (wiring-YAML). The `internal/` engine
is sealed; only the façade + composition SDK are public.

## Layout

- `cmd/aisotropyd` — the binary (a thin ~51-line consumer of the `aisotropy.App` builder).
- `aisotropy/` — the **public** package (façade + builder; ADR-0013/0016). Public API surface.
- `internal/` — the sealed engine (llm, memory, facts, semantic, tenancy, routing, tools, agentloop,
  bitrate, consolidation, sleepcycle, costmeter, engram, health, api). Not importable externally.
- `crates/` — Rust workers (sleep-cycle dedup; bitrate bpt scorer).
- `docs/` — OKF knowledge base. Start at [`docs/architecture/reality-ledger.md`](docs/architecture/reality-ledger.md)
  (what is REAL vs dormant vs aspirational) and [`docs/decisions/`](docs/decisions/) (ADRs 0000–00NN).
- `spikes/compression/` — research spikes (Python + venv, gitignored); numbers surfaced to
  [`docs/research/evaluation.md`](docs/research/evaluation.md).

## Conventions

- One ADR per significant decision (`docs/decisions/NNNN-*.md`); update `docs/decisions/index.md` and,
  for measured claims, `docs/research/evaluation.md`. **No claim without a number** ("Bellard rule").
- Go: standard `gofmt`; snake_case is not used (Go idiom). Rust: `cargo fmt` + `clippy` clean.
- Commit messages: imperative, scoped (`feat(bitrate): …`, `docs(adr): …`). **Do NOT add a
  `Co-Authored-By` trailer.**
- Reconstruction phase: rewrite over patching; fix compile errors in-session; don't defer to "future
  work" without a tracking entry.

## Related standards this repo relates to

- **AGENTS.md** (this file) — dev-time. **MCP** — the runtime tool-execution standard aisotropy
  already supports (`WithMCPServer`). **Agent Skills (SKILL.md) / OKF** — see
  [ADR-0022](docs/decisions/0022-agent-config-standards-import-surfaces.md) for how (and where the
  anti-magic line falls) they are supported as *import/packaging* surfaces, not opaque runtime behavior.
