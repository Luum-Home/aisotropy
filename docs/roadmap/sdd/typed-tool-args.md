---
type: SDD
title: "SDD — Typed tool arguments (WithTypedTool[T]) + fail-closed reflection on tool inputs"
description: Spec-driven design for Phase 9 lever #1 (ADR-0016). Replaces the stringly-typed map[string]string tool-argument contract with a struct-as-contract path — a generic WithTypedTool[T] whose T drives the tool's JSON schema handed to the model (native tool-calling, ADR-0012), decodes+validates the model's arguments into T, and extends the fail-closed reflection loop from the chat response to tool INPUTS. Preserves the double scope-gate, the ToolExecutor low-level seam, and the anti-magic gate.
tags: [sdd, phase-9, tools, typed, struct-as-contract, reflection, facade, adr-0016, adr-0012, adr-0002]
timestamp: 2026-07-09T00:00:00Z
status: delivered
---

# SDD — Typed tool arguments (`WithTypedTool[T]`) + fail-closed reflection on tool inputs

> **As-built (2026-07-09, delivered).** Implemented end to end and green
> (`go build ./...`, `go vet ./...`, `go test ./...` = 18 pkgs ok; `examples/weatherbot`
> nested-module build + `grep internal/` = 0). Two honest refinements vs. the original design
> below:
> - **§4.2 refinement — additive, not in-place widening.** `llm.ToolCall.Args` (`map[string]string`)
>   was **kept**; a new `llm.ToolCall.RawArgs json.RawMessage` was **added** alongside it (and
>   `llm.ToolSpec.Parameters`, `tools.Tool.Parameters`). This preserves every existing consumer and
>   the low-level `ToolExecutor` seam (R7) with zero breakage — a strictly better outcome than the
>   "widen in place" wording, and it resolves ADR-0013 open-question #1 **without** a breaking change.
>   The native path (`RunToolAgent` → Gollem bridge → `OpenAICompatClient`) now prefers `RawArgs`, so
>   nested/typed fields survive to the handler.
> - **Validation depth — now full (R3 100%).** Initial slice enforced only type/structure; a
>   follow-up added `internal/llm.CompileSchema`/`SchemaValidator` (santhosh-tekuri/jsonschema v6,
>   BSD-2) so args are validated against **exactly the generated schema** the model was shown —
>   `enum`/`minimum`/`maximum`/`required`/type all enforced, each violation reflecting
>   (`TestCompileSchema_EnforcesConstraints`, `TestNativeToolAgent_TypedTool_ConstraintViolationReflects`).
>   The validate+decode+dispatch handler lives in `internal/api.TypedHandlerFor[T]`; the façade
>   `WithTypedTool` delegates to it. **This same validator backs MCP tools** (lever #2,
>   `internal/api.BuildMCPTools` / `mcp_tools.go`), so an MCP server's args are validated against its
>   advertised `inputSchema` too. Recorded in [`aisotropy/CHANGELOG.md`](../../../aisotropy/CHANGELOG.md).

> Lever #1 of [ADR-0016](../../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md).
> Highest leverage, lowest cost now, and the most **on-thesis** change in the repo: it completes
> "the struct as the single contract" ([ADR-0002](../../decisions/0002-anti-framework-magic-struct-as-contract.md))
> at the one place it is not yet applied — tool I/O.

## 1. Problem & motivation

Today a tool's arguments are `map[string]string` end to end:
[`agentloop.ToolExecutor.Execute(ctx, name, args map[string]string)`](../../../internal/agentloop/agentloop.go),
[`agentloop.Decision.Args`](../../../internal/agentloop/agentloop.go),
[`llm.ToolCall.Args`](../../../internal/llm/tools.go) (even on the native tool-calling path), and the
public façade alias `aisotropy.ToolExecutor`. [`llm.ToolSpec`](../../../internal/llm/tools.go) carries
only `{Name, Description}` — **no parameter schema** — so the model is never told a tool's argument
shape, and whatever string map it emits is passed through unvalidated.

Consequences:

- Realistic tools (nested objects, numbers, enums, arrays) cannot be expressed. The competitive/
  adoption analysis flags this as the #1 ergonomic ceiling for "build rapidly".
- The [struct-as-contract thesis](../../vision/thesis.md) governs HTTP + OpenAPI + the LLM
  *response* but **not tool inputs** — the single gap in the thesis.
- The [fail-closed reflection loop](../../../.claude/rules/constitutional-gates.md) protects the chat
  response schema but **not** tool arguments: a malformed tool call is silently stringly-coerced,
  not repaired.

## 2. Scope

**In scope:** a generic typed-tool registration and execution path; extending `ToolSpec` with a
parameter schema; widening the tool-argument carrier; a fail-closed reflect-on-inputs step;
preserving the double scope-gate; a façade option; keeping the existing `map[string]string`
`ToolExecutor` as a low-level seam; the `examples/weatherbot` upgrade as the proof.

**Out of scope:** L2 composition SDK, MCP-backed tools (lever #2 — but this SDD's arg carrier MUST
be MCP-compatible, see R5), the Anthropic-SDK `ToolCaller` (ADR-0012 follow-up), streaming tool
calls.

## 3. Requirements (each verifiable)

| ID | Requirement | Verification |
|---|---|---|
| **R1** | A consumer registers a tool whose args are a Go struct `T` with jsonschema tags, from the public façade only: `aisotropy.WithTypedTool[T](name, desc, scope, handler)`. | `examples/weatherbot` registers a typed tool importing only `…/aisotropy`; `grep -rn internal/ examples/weatherbot` = 0; `go build ./examples/weatherbot` (nested module) exits 0. |
| **R2** | The tool's JSON schema derived from `T` is handed to the model via the native tool-calling path: `llm.ToolSpec` carries a `Parameters` schema and `ToolCaller.CallTools` sends it. | Unit test: `ToolSpec.Parameters` for a struct `T` equals the expected JSON Schema; a fake `ToolCaller` records that it received a non-empty schema. |
| **R3** | The model's returned arguments are decoded + validated into `T` before the handler runs. Invalid args (missing required field, wrong type, failed constraint) do NOT reach the handler. | Unit test: a `ToolCall` with args violating `T` returns a typed error and the handler is never invoked (spy handler call-count = 0). |
| **R4** | A schema-invalid tool call triggers a **bounded fail-closed reflection retry** with the exact validation error fed back, mirroring the chat-response reflection loop — not a silent stringly fallback. | Unit test: first model turn emits invalid args, second emits valid; handler runs once with the corrected `T`; retries capped at N; exhaustion returns a typed error, never a coerced call. |
| **R5** | The argument carrier is JSON-structured (`map[string]any` or `json.RawMessage`), compatible with Gollem MCP's `CallTool(ctx, name, map[string]any)`, so lever #2 (MCP tools) reuses this path unchanged. | Type check: `ToolCall.Args`/handler-decode path round-trips a nested JSON object; a table test decodes the same payload MCP would send. |
| **R6** | The **double scope-gate is preserved**: a typed tool is offered only when its `RequiredScope` is granted, and a returned tool name outside the permitted set never executes. | The existing scope-gate tests (`agentloop`, `chat_agent`) still pass, plus a new test for the typed path: an unpermitted typed tool is neither in `ToolSpec`s sent nor executable. |
| **R7** | The low-level `agentloop.ToolExecutor` (`map[string]string`) remains a valid seam (native path absent → structured-output fallback still works); typed tools degrade to a documented behavior when the client is not a `ToolCaller`. | `stub` client (no `ToolCaller`) path: a typed tool either falls back to the string path with a documented limitation or is cleanly reported unsupported — asserted by test, not left implicit. |
| **R8** | Anti-magic (ADR-0002): the schema is derived from a **real Go struct** the consumer declares (like the HTTP contract), not a hidden DSL; the handler is Go behavior. No templating, no reflection-magic beyond struct→JSON-Schema (the same mechanism Huma already uses). | Review gate + doc: the typed-tool path uses the same struct→schema derivation family as the Huma contract; no string-templated tool definitions. |
| **R9** | Backwards compatibility is explicit: this is a 0.x façade change. If `aisotropy.ToolExecutor`'s signature changes, ship a CHANGELOG + migration note (ADR-0013 policy). Prefer additive (`WithTypedTool` alongside `WithToolExecutor`). | `CHANGELOG` entry present; `cmd/aisotropyd` + `examples/weatherbot` both build; `go test ./...` green. |

## 4. Design

### 4.1 Public surface (façade, additive)

```go
// aisotropy/tools.go (new) — public, generic sugar over the internal seam.

// TypedTool binds an args struct T (jsonschema-tagged) to a handler. T drives
// the schema the model sees AND the decode/validation of what it returns.
func WithTypedTool[T any](name, description, requiredScope string,
    handler func(ctx context.Context, args T) (string, error)) Option
```

`WithToolExecutor` (map[string]string) stays for the low-level / dynamic case. `WithTypedTool`
accumulates like `WithTool`. Internally each typed tool contributes: (a) a `tools.Tool{Name,
Description, RequiredScope}` to the scope-gated registry (unchanged gate), and (b) a schema +
type-erased decode-and-dispatch closure registered in a typed-executor the builder installs.

### 4.2 `internal/llm` — carry the schema and structured args

```go
type ToolSpec struct {
    Name        string
    Description string
    Parameters  json.RawMessage // NEW: JSON Schema for the args (empty = no-arg tool)
}

type ToolCall struct {
    Name string
    Args json.RawMessage // WIDENED from map[string]string (see R5; MCP-compatible)
}
```

`ToolCaller.CallTools` sends `Parameters` to the provider's native tool API (the
`OpenAICompatClient` from ADR-0012 already speaks OpenAI function schemas — it maps `Parameters`
straight through). Providers without native tools keep the structured-output fallback; there the
schema is rendered into the system prompt fragment and the JSON object is parsed from text (R7).

### 4.3 Typed executor + reflect-on-inputs

A new `internal/tools` (or `internal/agentloop`) typed dispatcher:

1. Receives a `ToolCall` (name already scope-re-validated — R6).
2. Looks up the registered `(schema, decoder)` for the name.
3. `json.Unmarshal(call.Args, &t)` + schema validation. On failure → **reflect**: re-issue the
   tool turn with the validation error appended (bounded by the same `MaxSteps`/retry budget the
   chat-response `Reflect[T]` uses, ADR-0008), never a silent coerce (R4).
4. On success → invoke the consumer handler `handler(ctx, t)`. `ctx` is the request's already-scoped
   context (ADR-0016 invariant): the handler reads identity via `aisotropy.TenantFromContext`, never
   a `Scope`.

The fail-closed contract now covers **response schema AND tool-input schema** — closing the gate
the constitutional rules only half-covered.

### 4.4 What does NOT change

- The double scope-gate (`tools.Registry.Permitted` + post-parse re-validation) — reused verbatim.
- `agentloop.ToolExecutor(map[string]string)` — remains for dynamic/low-level tools and the
  non-`ToolCaller` fallback.
- `internal/` seal, tenancy, memory tiers — untouched (this is a tool-I/O change only).

## 5. Task breakdown

- [x] **T1** — Widen `llm.ToolSpec` (`Parameters`) and `llm.ToolCall.Args` → `json.RawMessage`;
  update `OpenAICompatClient.CallTools` to send/parse schema; update the structured-output fallback
  renderer. (R2, R5, R7)
- [x] **T2** — Struct→JSON-Schema derivation for `T` (reuse the Huma/invopop jsonschema family
  already in `go.mod`; no new dep — license-check per ADR-0002/license-policy). (R2, R8)
- [x] **T3** — Typed dispatcher: `(schema, decode, handler)` registry; decode+validate; invoke. (R3)
- [x] **T4** — Reflect-on-inputs: wire the validation error into the bounded retry, reusing the
  reflection budget; exhaustion → typed error. (R4)
- [x] **T5** — Façade `WithTypedTool[T]` in `aisotropy/tools.go`; builder threads the typed executor
  alongside the existing `ToolExecutor`. (R1, R9)
- [x] **T6** — Preserve + extend scope-gate tests for the typed path. (R6)
- [x] **T7** — Upgrade `examples/weatherbot` to a typed `GetWeatherArgs{City string; Units string
  \`json:"units" jsonschema:"enum=c,enum=f\"`}`; keep public-surface-only. (R1, R9)
- [x] **T8** — CHANGELOG + migration note; `docs/guides/build-your-own-agent.md` typed-tool section;
  note the `map[string]string`→structured resolution in ADR-0013 open-question #1 and ADR-0012. (R9)

## 6. Traceability

| Requirement | Tasks | Primary test/proof |
|---|---|---|
| R1 façade-only typed registration | T5, T7 | weatherbot nested-module build; `grep internal/` = 0 |
| R2 schema to model | T1, T2 | ToolSpec schema unit test; fake ToolCaller records schema |
| R3 decode+validate before handler | T3 | spy handler call-count = 0 on invalid |
| R4 reflect on inputs | T4 | invalid→valid retry test; cap + exhaustion error |
| R5 MCP-compatible carrier | T1 | nested-JSON round-trip table test |
| R6 double scope-gate | T6 | existing + new scope-gate tests green |
| R7 fallback seam intact | T1, T5 | stub-client path test |
| R8 anti-magic | T2, T8 | review gate + doc |
| R9 explicit 0.x compat | T5, T7, T8 | CHANGELOG; `go test ./...`; both modules build |

## 7. Verification (Definition of Done)

```bash
go build ./...                                   # core builds
(cd examples/weatherbot && go build ./... && go vet ./...)   # nested module builds on public surface
grep -rn "internal/" examples/weatherbot         # → 0 (public-surface-only preserved)
go test ./internal/llm/... ./internal/tools/... ./internal/agentloop/... ./internal/api/...  # tool + scope-gate + reflection tests
go test ./...                                    # full suite green
go run ./cmd/x check                             # repo quality gates (build+fmt+vet+test)
```

Done when: all commands exit 0; R1–R9 each have a passing test/proof in the table; the CHANGELOG
records the 0.x tool-arg evolution; and ADR-0013 open-question #1 is marked resolved by this SDD.

## 8. Risks

- **Provider schema fidelity** — native function-calling schema support varies per provider; the
  structured-output fallback (R7) is the floor, tested against the keyless stub.
- **Reflection cost** — reflect-on-inputs adds turns on malformed calls; bounded by the existing
  retry budget (no new unbounded loop).
- **Compat churn** — widening `ToolCall.Args` touches ADR-0012 code paths; mitigated by keeping
  `WithToolExecutor(map[string]string)` and shipping the change as an announced 0.x minor.

## 9. References

- [ADR-0016 — Dual surface](../../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md) (this is its lever #1)
- [ADR-0012 — Native tool-calling](../../decisions/0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) (the `ToolCaller`/`ToolSpec`/`ToolCall` seam)
- [ADR-0013 — Public extension surface](../../decisions/0013-public-extension-surface-facade-vs-promote.md) (open-question #1 this resolves; 0.x compat policy)
- [ADR-0002 — Anti-framework-magic](../../decisions/0002-anti-framework-magic-struct-as-contract.md) (struct-as-contract, the thesis this completes)
- [Phase 9 — Composition surface](../milestones.md#phase-9--composition-surface-dual-a--b)
