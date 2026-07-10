# Changelog — `github.com/Luum-Home/aisotropy/aisotropy`

The public adoption-surface package. Pre-1.0 (0.x): **additive within a minor; a
breaking change is allowed only on a minor bump, with a migration note here**
(ADR-0013). `internal/…` packages are out of scope — they churn freely.

## [Unreleased] — 0.x

### Added

- **`WithTypedTool[T](name, description, requiredScope, handler)`** — register a
  tool whose arguments are a Go struct `T` (ADR-0016 lever #1 / typed-tool-args
  SDD). The struct drives, at once: the JSON schema the model is shown (native
  tool-calling), the **validation** of the model's returned arguments against that
  schema, the decoding into `T`, and a **fail-closed reflection loop on inputs** —
  if the arguments violate the schema (wrong type, or a constraint:
  `enum`/`minimum`/`maximum`/`required`/`pattern`) or do not decode into `T`, the
  runtime surfaces the error back to the model to self-correct (bounded by the
  agent's step budget) instead of coercing bad data or failing the request. This
  is the tool-input arm of the "struct as the single contract" thesis (ADR-0002).
  Nested/non-string fields survive on the native tool-calling path; on the
  structured-output fallback (a client without native tool-calling, e.g. the
  keyless stub) arguments arrive flattened to strings — the documented 0.x limit.
- **`WithMCPServer(requiredScope, command, args...)`** — expose a Model Context
  Protocol server's tools as scope-gated Aisotropy tools (ADR-0016 lever #2:
  polyglot tool execution). The server is any stdio executable speaking MCP, in
  **any language**; the Go core keeps owning tenant scoping and identity. Each
  discovered tool carries the server's JSON Schema (so args are validated/reflected
  like any typed tool) and dispatches to the server; the subprocess lifecycle is
  runtime-owned (closed on shutdown). Connection is best-effort — a server that
  fails to start is logged and skipped, never downing the runtime. The consumer
  imports no MCP library: command, args, and scope are the whole surface.
- **Declarative wiring config** (ADR-0016 lever #5) — `Config`, `LoadConfigFile`,
  `OptionsFromConfig`, `OptionsFromConfigFile`. A YAML (or JSON) file carries
  DEPLOYMENT VALUES only — `addr`, `rate_limit`, `cost_pricing`, literal
  `personas`, and `mcp_servers` — and turns into builder `Option`s that compose
  with the Go-only options (`WithToolExecutor`/`WithTypedTool`/`WithLLM`). It
  deliberately cannot declare a Go-behavior tool or the LLM client (anti-magic,
  ADR-0002); MCP servers are allowed because their behavior is the external
  process the file only points at. Config values are defaults a later Go option
  overrides. Sample: `examples/aisotropy.example.yaml`.

### Changed (non-breaking)

- `Tool` (alias of `internal/tools.Tool`) gained an optional `Parameters`
  (JSON-Schema) field, set by `WithTypedTool`. Existing `WithTool` registrations
  are unaffected (empty `Parameters` = untyped, free-form args, exactly as before).

### Unchanged (explicitly preserved)

- **`WithToolExecutor` / the `ToolExecutor` (`map[string]string`) seam is intact.**
  Typed tools are additive sugar over it; untyped/dynamic tools keep the string-map
  path. No consumer signature changed — this release is **not breaking**.
  (Resolves ADR-0013 open question #1: typed args landed **without** widening or
  breaking the `map[string]string` contract.)

### Dependencies

- Added `github.com/santhosh-tekuri/jsonschema/v6` (**BSD-2**, license-policy
  SAFE) to enforce the generated tool-arg schema server-side (the constraint
  validation above). It validates against exactly the schema the model was shown.

### Known limitations / follow-ups

- Structured-output fallback path (no native tool-calling, e.g. the keyless stub)
  still receives args flattened to strings — nested/typed fidelity is guaranteed
  only on the native path (0.x limit).
- Typed-tool friction recording (Phase 6 behavioral memory) is not yet wired for
  the typed/MCP path; untyped tools record friction as before.
- MCP integration is stdio-only for now (gollem also offers HTTP); no in-repo
  end-to-end test spawns a real MCP server — the adapter is unit-tested with a
  fake client, the subprocess wiring is exercised manually.
</content>
