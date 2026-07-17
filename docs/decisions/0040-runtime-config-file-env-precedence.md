---
type: ADR
title: "ADR-0040 — Runtime config FILE (YAML + TOML) that mirrors the AISOTROPY_* env surface, under an explicit env > file > default precedence"
description: >
  Until now the runtime was configured by ENV VARS only (AISOTROPY_* plus the service vars RQLITE_URL /
  QDRANT_* / ANTHROPIC_API_KEY), read via scattered os.Getenv calls in aisotropy/app.go and
  aisotropy/authenv.go. There was no runtime config FILE. This ADR adds one, additively and 100%
  backward-compatibly: a RuntimeConfig struct (aisotropy/runtimeconfig.go) whose fields mirror the existing
  env knobs ONE-FOR-ONE — it invents no new schema — loadable from YAML (goccy/go-yaml, already a
  dependency) or TOML (github.com/BurntSushi/toml v1.6.0, MIT). The file is resolved from the new
  AISOTROPY_CONFIG_FILE env var, an explicit WithConfigFile(path) option, or the default search paths
  ./aisotropy.runtime.{yaml,yml,toml}. Loading MATERIALIZES each populated field into the process
  environment, but only for variables not already set — so the precedence is exactly explicit ENV VAR >
  config-file value > built-in default, and the existing os.Getenv resolvers are untouched. An app with no
  config file materializes nothing and boots byte-for-byte as before. Secrets (JWT secret, API keys, ops
  token) are loadable from the file for local/dev convenience but env / a secret manager remains preferred,
  and their values are never logged. STATUS: accepted — shipped in aisotropy/runtimeconfig.go with
  aisotropy/runtimeconfig_test.go.
tags: [decisions, adr, config, env, precedence, yaml, toml, backward-compatibility]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0040 — Runtime config FILE mirroring the `AISOTROPY_*` env surface, under `env > file > default` precedence

## Status

✅ **Accepted — shipped 2026-07-17** (`aisotropy/runtimeconfig.go` + `aisotropy/runtimeconfig_test.go`;
wired into `aisotropy/app.go` via `WithConfigFile` and the top of `startRuntime`). Additive over
[ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md)'s wiring `Config` and complementary to
[ADR-0024](0024-authenticated-identity-seam.md)'s env-driven auth: this ADR closes the "no runtime config file" gap without
changing any existing default.

## Context

The runtime was configured by **environment variables only**. `aisotropy/app.go` and
`aisotropy/authenv.go` read ~30 knobs directly through `os.Getenv`: `AISOTROPY_ADDR`, `AISOTROPY_RATE_PER_SEC`,
`AISOTROPY_BURST`, `AISOTROPY_DATA_DIR`, `AISOTROPY_REQUIRE_TLS`, `AISOTROPY_DEDICATED_TENANTS`, the memory-tier
knobs (`AISOTROPY_SEMANTIC_TURN_TTL/MAX`, `AISOTROPY_FACT_MIN_IMPORTANCE`), the guardrail toggles
(`AISOTROPY_GUARDRAILS_BLOCK_OUTPUT/INJECTION`, `AISOTROPY_GUARDRAILS_LLM_JUDGE`), cost/vision/embed knobs, the
service URLs (`RQLITE_URL`, `QDRANT_HOST/PORT/COLLECTION/API_KEY/USE_TLS`), the provider key
(`ANTHROPIC_API_KEY`), and the JWT verification family (`AISOTROPY_JWT_*`).

There was a config FILE already — but a **different** one. The `Config` in `aisotropy/config.go`
([ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md)) is a **wiring** surface: it carries deployment values that map
to Go builder *options* (addr, rate limit, cost pricing, personas, MCP servers). It is deliberately narrow
(the anti-magic rule, [ADR-0002](0002-anti-framework-magic-struct-as-contract.md)) and does **not** cover the operational env knobs
above. So an operator who wanted to pin, say, `AISOTROPY_SEMANTIC_TURN_TTL` or the Qdrant host had exactly one
channel: the process environment. That is fine for 12-factor deployments but awkward for local runs, for
committing a non-secret baseline, and for operators who prefer a single reviewed file over a shell full of
`export`s.

The gap: **no runtime config file, and no TOML.** The requirement is to add both — additively, and without
changing the behavior of any app that does not use them.

## Decision

Add a **runtime config file** that mirrors the existing env surface one-for-one, resolved and applied under an
explicit precedence, and wired into the single construction path both entry points already share.

### 1. `RuntimeConfig` — mirror, don't reinvent

`aisotropy/runtimeconfig.go` defines `RuntimeConfig`, a flat struct whose every field corresponds to **exactly
one** environment variable (documented in a trailing comment on each field). It is **not** a new schema: it is
the env surface expressed as a file. Numeric/bool knobs are **pointer** fields (`*int` / `*float64` / `*bool`)
so "absent" (nil — leave the env/default alone) is distinct from a zero value the operator set on purpose
(e.g. `qdrant_use_tls: false`).

### 2. Two formats, chosen by extension

`LoadRuntimeConfigFile(path)` selects the parser by file extension:

- `.toml` → `github.com/BurntSushi/toml` **v1.6.0, MIT** (new dependency; passes the constitutional license
  gate — MIT/BSD/Apache only, never copyleft/BUSL).
- `.yaml` / `.yml` / `.json` / anything else → `github.com/goccy/go-yaml` (already a dependency; a JSON file
  parses too, YAML being a JSON superset).

A missing, unreadable, or **malformed** file returns a clear wrapped `aisotropy: …` error — never a panic. A
value whose type does not match the schema (`rate_per_sec: "abc"`) is a parser error caught at load, not a
surprise at request time.

### 3. Precedence: `env > file > default`, via env-materialization

Rather than thread a resolved struct through ~30 scattered `os.Getenv` call sites (large blast radius, high
regression risk), loading **materializes** the file: for each populated field, `materialize()` calls
`os.Setenv` **only when the variable is not already present** in the environment (`os.LookupEnv`). The result
is precisely:

```
explicit ENV VAR  >  config-file value  >  built-in default
```

- **Env wins**: a variable already in the environment is left untouched (precedence is by *presence*, so even
  an explicitly-empty env var beats the file).
- **File fills gaps**: a variable absent from the environment is set from the file, and the unchanged
  downstream `*FromEnv` resolver reads it.
- **Default stands**: a knob neither the env nor the file sets is never written, so the resolver returns its
  built-in default exactly as before.

Because a **no-config app materializes nothing**, its boot is byte-for-byte identical to before this ADR — the
backward-compatibility guarantee is structural, not merely tested.

### 4. Wiring: one seam, one materialization

Resolution honors, in order: an explicit `WithConfigFile(path)` option → the `AISOTROPY_CONFIG_FILE` env var →
the default search paths `./aisotropy.runtime.{yaml,yml,toml}`. The `.runtime.` infix deliberately keeps this
env-knob file from colliding with the [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) wiring file, whose
conventional name is `aisotropy.yaml` — two different schemas must not share a default filename.

Materialization runs at the **top of `startRuntime`** — the single dependency-construction path both `Run`
(HTTP) and `Turn` (in-process) boot from — **before** `llm.FromEnv()` or any `AISOTROPY_*` read. It is guarded
by a `sync.Once` on the `App`, so repeated boots neither re-read the file nor race on the process environment,
and a malformed file fails the boot (`Run`/`Turn` return the error) rather than panicking.

### 5. Secrets: loadable, but not preferred

The secret-bearing fields (`anthropic_api_key`, `jwt_secret`, `ops_token`, `qdrant_api_key`, `embed_api_key`)
are loadable from the file for local/dev convenience, but the doc comment states plainly that the environment
or a dedicated secret manager remains the **preferred** source in production, and a checked-in config file
must never carry them. `materialize` never logs secret *values*; the "runtime config file applied" log lists
only the applied key *names*, and secret names are tagged `(secret)`.

## Consequences

**Positive**
- Operators get a single reviewed file (YAML or TOML) for the full operational surface, without abandoning
  env-var deployment — the two compose under a clear precedence.
- Zero behavior change for existing deployments: no file ⇒ no materialization ⇒ identical boot.
- Minimal blast radius: one new file plus a ~15-line wiring hook; the ~30 `os.Getenv` resolvers are untouched.
- The public API grows by three symbols (`RuntimeConfig`, `LoadRuntimeConfigFile`,
  `LoadAndMaterializeRuntimeConfig`) and one option (`WithConfigFile`).

**Negative / honest scope**
- Materialization mutates **process-global** environment. This matches the existing design (the runtime
  already reads global env pervasively) and is safe for the intended one-time startup use, guarded per-`App`
  by `sync.Once`; it is **not** designed for many `App`s materializing different files concurrently in one
  process. Tests exercise the materialization seam sequentially and hermetically (snapshot/restore of the
  touched vars) rather than booting concurrent runtimes.
- Value-range validation (e.g. negative rate) is intentionally **not** duplicated here: bad values flow to the
  same downstream `*FromEnv` parsers that already warn-and-default on bad env input, keeping file and env
  handling identical. Only *syntactic* / *type* errors fail at load.
- The default filename is `aisotropy.runtime.*`, not `aisotropy.*`, to avoid colliding with the ADR-0016
  wiring file — a small naming cost for an unambiguous separation of the two config surfaces.

## Alternatives considered

- **Thread a resolved config struct through every call site.** Rejected: ~30 edits across `app.go` /
  `authenv.go`, high regression risk, and it would fork the env-reading logic. Materialization keeps one
  source of truth (the `*FromEnv` resolvers) and adds the file as an upstream fallback.
- **A single format (YAML only).** Rejected: the requirement is explicit — both YAML *and* TOML — and TOML is
  a common operator preference. Extension-based dispatch keeps the surface trivial.
- **Reuse the ADR-0016 `Config` file.** Rejected: that is a *wiring* surface (Go options, anti-magic), a
  different concern; overloading it would blur [ADR-0002](0002-anti-framework-magic-struct-as-contract.md)'s boundary. The two files
  are complementary and can be used together.
