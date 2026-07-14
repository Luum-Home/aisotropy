---
type: Reference
title: CLI tools — every binary under cmd/
description: What each cmd/ binary is for — the product runtime (aisotropyd) versus the dev/instrument tools (x, bitrate-harness, embed-bench, skill-import) — how to run each one, its flags and env vars, and which compiled outputs are gitignored.
tags: [guides, reference, cli, binaries, dev-tools, runtime]
timestamp: 2026-07-14T00:00:00Z
---

# CLI tools — every binary under cmd/

`cmd/` holds five binaries (2026-07-14). Exactly one — `aisotropyd` — is product surface; the
other four are development tools or measurement instruments. All are run with `go run ./cmd/<name>`
from the repository root; the compiled dev-tool outputs (`/x`, `/bitrate-harness`, `/skill-import`,
`/embed-bench`, and the root `/aisotropyd` binary) are **gitignored** — only the source directories
are tracked.

| Binary | Kind | One line |
|---|---|---|
| [`cmd/aisotropyd`](#aisotropyd) | **Product surface** | The agent runtime server (demo wiring of the public `aisotropy` builder) |
| [`cmd/x`](#x) | Dev tool | Interactive TUI + CLI task runner for the repo's build/test/lint tasks |
| [`cmd/bitrate-harness`](#bitrate-harness) | Dev instrument | Bet-6 longitudinal bits-per-token pipeline against production-like tiers |
| [`cmd/embed-bench`](#embed-bench) | Dev instrument | Bet-5 (ADR-0023) cold-subprocess vs warm-daemon embedding latency benchmark |
| [`cmd/skill-import`](#skill-import) | Dev tool | ADR-0022 Agent-Skills importer — maps a `SKILL.md` folder to aisotropy primitives |

## aisotropyd

**Product surface.** The Aisotropy agent runtime binary. Since Phase 8
([ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md)) it is a thin ~51-line
consumer of the public builder `aisotropy.App`: it registers three **demo** tools (`search_docs`,
`read_financials` gated on `read:financial-data`, `write_tasks` gated on `write:tasks`), the echo
`DemoExecutor`, and the default persona, then calls `App.Run`. All real wiring — memory tiers, the
outbox worker, rate limiting, circuit breaker, cost meter, the HTTP routes — lives in
[`aisotropy/app.go`](../../aisotropy/app.go), not here. The demo tools are placeholders so the
binary boots and exercises the scope-gated agent loop; a real bot replaces them (see
[Build your own agent](build-your-own-agent.md)).

```bash
go run ./cmd/aisotropyd            # serves on :8080 by default
```

Key env vars (the contract listed in `cmd/aisotropyd/main.go`): `AISOTROPY_ADDR`,
`AISOTROPY_DATA_DIR`, `AISOTROPY_DEDICATED_TENANTS`, `RQLITE_URL`, `QDRANT_*`,
`AISOTROPY_CHEAP_MODEL`, `AISOTROPY_SLEEPCYCLE_BIN`, `AISOTROPY_RATE_PER_SEC`, `AISOTROPY_BURST`,
`ANTHROPIC_API_KEY`. No flags. Endpoints it serves are documented in the
[API reference](../architecture/api-reference.md).

## x

**Dev tool.** Aisotropy's developer console: an interactive Bubbletea TUI for the project's dev
tasks, which doubles as a plain CLI (the same task registry drives both local development and
CI/scripts). Run it from the repository root — task commands are relative to it.

```bash
go run ./cmd/x                     # interactive TUI
go run ./cmd/x list                # print the task registry
go run ./cmd/x <task>              # run one task non-interactively (exit code = task result)
```

Registered task keys (`cmd/x/registry.go`): `build`, `fmt`, `fmt-fix`, `vet`, `lint`, `test`,
`race`, `bench`, `example-build`, `example-vet`, `example-build-helpdesk`, `example-vet-helpdesk`,
`example`, `run`, `check` (build + fmt + vet + test + example in sequence — the quality gates),
`dup`, `complexity`, `deadcode`, `cover`, `vuln`, `deps`, `audit`, `tasks`.

Env vars: `AISOTROPY_X_COVER_THRESHOLD` — overrides the `cover` task's minimum total coverage
percent (default 60). The stray root binary `./x` produced by `go build ./cmd/x` is gitignored.

## bitrate-harness

**Dev instrument** (Bet-6 / KR8.1). Drives the full bits-per-token-per-tenant pipeline against a
production-like deployment: it runs the same coordinator → scorer → store path as the in-process
sims, but against the real networked tiers (rqlite facts + Qdrant semantic) when they are
reachable, boots the real `aisotropyd` (the `aisotropy.App` builder), and reads the trend back
over the real HTTP surface (`GET /health`, `GET /bitrate`). Honesty note from its own header: the
evolving tenant is still **simulated** (an authored accumulation schedule), not real users over
weeks — the harness validates the instrument end to end, not field data.

```bash
# Production-like run (real tiers):
docker compose up -d rqlite qdrant
AISOTROPY_BITRATE_BIN=/abs/wrapper.sh \
  RQLITE_URL=http://localhost:4001 QDRANT_HOST=localhost QDRANT_PORT=6334 \
  go run ./cmd/bitrate-harness

# In-process fallback (no Docker): drop RQLITE_URL / QDRANT_HOST.
```

Flags: `-addr` (HTTP listen address for the booted aisotropyd, default `:18080`), `-http-proof`
(boot the real server and read `GET /health` + `GET /bitrate` back, default `true`).
Env vars: `AISOTROPY_BITRATE_BIN` (required — path to a scorer wrapper that execs
`spikes/compression/.venv/bin/python spikes/compression/bitrate_scorer.py`; if unset or missing
the harness **SKIPs cleanly with exit 0** — it never fabricates a curve), `RQLITE_URL`,
`QDRANT_HOST` / `QDRANT_PORT` (tier selection, mirroring the runtime's own precedence). It sets
`AISOTROPY_DATA_DIR` itself to a temp dir for the run. The root `/bitrate-harness` binary is
gitignored.

## embed-bench

**Dev instrument** (Bet-5 / [ADR-0023](../decisions/0023-sovereign-local-embedder-semantic-tier.md)). The latency
benchmark that decided the warm-daemon embed path: it compares a fresh candle subprocess per
embedding (the cold `LocalEmbedder` path — spawn + model load + infer) against a warm candle
daemon (the same binary run with `--serve`, model loaded once, requests piped over stdin/stdout).
Same crate, same weights. It prints p50/p95/mean/min for both paths and a numeric verdict (the
spawn+load tax and the warm speedup ratio). If the embed binary or model dir is missing it
**SKIPs cleanly (exit 0)**, like `bitrate-harness` — it fabricates no numbers.

```bash
AISOTROPY_EMBED_BIN=crates/embed/target/release/embed \
AISOTROPY_EMBED_MODEL_DIR=/abs/model-dir \
  go run ./cmd/embed-bench -n 30 -m 200
```

Flags: `-n` (cold-subprocess iterations, default 30), `-m` (warm-daemon iterations, default 200).
Env vars: `AISOTROPY_EMBED_BIN` (the built embed binary), `AISOTROPY_EMBED_MODEL_DIR` (a dir with
`config.json` + `model.safetensors` + `tokenizer.json`). The root `/embed-bench` binary is
gitignored.

## skill-import

**Dev tool** (ADR-0022 import surface). Reads an Agent Skill folder (agentskills.io format: a
`SKILL.md` with YAML frontmatter + optional `scripts/`, `references/`, `assets/`) and maps it onto
aisotropy's public primitives — typed tools / MCP servers for scripts, a **literal** persona for
instructions, ingested knowledge for references. Nothing a skill carries is silently injected at
runtime (the anti-magic gate, ADR-0002). Full walkthrough:
[Importing Agent Skills](importing-agent-skills.md).

```bash
# Preview (writes nothing): print the mapping report + illustrative wiring
go run ./cmd/skill-import examples/skills/pdf-form-filler

# Generate: WRITE a compilable Go consumer module for the skill
go run ./cmd/skill-import -out /tmp/pdfbot examples/skills/pdf-form-filler
```

Flags: `-out <dir>` (switch from preview to generate mode; the generated module gets its own
`go.mod` with a `replace` back to this repo, mirroring `examples/weatherbot`, and must pass the
operator's review — persona string, scope gates, exec bridges — before standing up). No env vars.
Refusals abort generation rather than emit unsafe wiring: a script tool with an empty required
scope, or a prose-only skill that presumes silent always-on injection. Skills whose frontmatter
declares an AGPL-3.0/SSPL license trip the constitutional license gate in the preview report. The
root `/skill-import` binary is gitignored.

## References

- [API reference](../architecture/api-reference.md) — the routes `aisotropyd` serves, including
  the ops endpoints (`/healthz`, `/health`, `/costs`, `/bitrate`) the instruments read back.
- [Build your own agent](build-your-own-agent.md) — the intended replacement for the demo wiring
  in `cmd/aisotropyd`.
- [Importing Agent Skills](importing-agent-skills.md) — the `skill-import` walkthrough.
- Source: `cmd/*/main.go` (each binary's header comment is its authoritative contract).
