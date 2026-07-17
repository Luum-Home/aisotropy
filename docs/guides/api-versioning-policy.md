---
type: Guide
title: "API versioning & deprecation policy (0.x)"
description: The versioning and deprecation contract for aisotropyd's HTTP API. Because the Go struct IS the OpenAPI contract (Huma), a struct change is a contract change. Documents the 0.x additive-within-a-minor / breaking-on-a-minor-bump rule (ADR-0013), the /v1 path prefix, what counts as breaking, and how a consumer pins and migrates.
tags: [guides, adoption, api, versioning, deprecation, openapi, contract]
timestamp: 2026-07-17T00:00:00Z
---

# API versioning & deprecation policy (0.x)

This is the stability contract for aisotropyd's HTTP API and the importable `aisotropy` Go package. It
tells a consumer what may change, when, and how they will find out — so they can pin a version and
migrate deliberately.

> **Status: the project is pre-1.0 (0.x), reconstruction phase.** The guarantees below are the *0.x*
> guarantees. They tighten at `v1` (see [Road to v1](#road-to-v1)).

## The load-bearing fact: the struct IS the contract

aisotropyd uses [Huma v2](../../.claude/rules/architecture.md), where a Go request/response struct is
the **single source of truth**: it drives HTTP validation *and* generates the OpenAPI 3.1 schema served
at `GET /openapi.json`
([ADR-0002 — anti-framework-magic / struct-as-contract](../decisions/0002-anti-framework-magic-struct-as-contract.md)).

The direct consequence for versioning:

> **A change to a contract struct is a change to the API.** Editing a JSON tag, removing a field,
> tightening a validation (`maxLength`, `required`), or changing a type re-generates `/openapi.json`
> and re-shapes every generated client. There is no separate "API version" to bump independently of the
> code — the code is the version. This is why the [constitutional gates](../../.claude/rules/constitutional-gates.md)
> treat "changing a field's JSON tag or removing a field" as a breaking-change gate, not a silent patch.

## The `/v1` path prefix

Every contract route is mounted under **`/v1`**, e.g.
`POST /v1/tenant/{tenant_id}/user/{user_id}/chat`. The prefix is the **major** version of the wire
contract. Its rules:

- **Additive and non-breaking changes stay on `/v1`.** New routes, new *optional* request fields, and
  new response fields do not move the prefix.
- **A breaking change to the wire shape would introduce `/v2`**, served alongside `/v1` for a
  deprecation window — never an in-place break of `/v1`. During 0.x no `/v2` is planned; the escape
  hatch for breaks is the minor-version bump (below), because there are not yet enough external
  consumers to justify parallel major paths.
- Routes **outside** the contract (`/healthz`, `/health`, `/costs`, `/bitrate`, `/openapi.json`,
  `/docs`) are operational endpoints, not part of the `/v1` versioned surface, and may change with a
  CHANGELOG note.

## What counts as breaking vs. additive

| Change | Classification | Allowed in a 0.x minor? |
|---|---|---|
| Add a new route under `/v1` | Additive | Yes, any minor |
| Add a new **optional** request field | Additive | Yes, any minor |
| Add a new response field | Additive¹ | Yes, any minor |
| Add a new SSE `event:` type a client can ignore | Additive | Yes, any minor |
| Remove or rename a request/response field | **Breaking** | Only on a **minor bump** + CHANGELOG + migration note |
| Change a field's type or JSON tag | **Breaking** | Only on a **minor bump** + CHANGELOG + migration note |
| Make an optional field required, or tighten validation | **Breaking** | Only on a **minor bump** + CHANGELOG + migration note |
| Change a route path or method | **Breaking** | Only on a **minor bump** + CHANGELOG + migration note |
| Change the meaning of an existing field without changing its shape | **Breaking (semantic)** | Only on a **minor bump** + CHANGELOG + migration note |

¹ Additive for well-behaved clients. Generated clients here (`openapi-typescript`,
`openapi-python-client`) tolerate unknown/new response fields; a client that asserts an exact response
shape should treat new response fields as a change to review.

## The 0.x rule (ADR-0013)

Per [ADR-0013 — public extension surface](../decisions/0013-public-extension-surface-facade-vs-promote.md),
the stability promise is scoped to the public surface and pinned at **0.x**:

- **Additive within a minor.** Within a `0.N.x` line, only additive/non-breaking changes ship. A
  consumer on `0.N.a` can move to `0.N.b` without code changes.
- **Breaking allowed on a minor bump — never silently.** A breaking change (table above) may land only
  on a **minor bump** (`0.N` → `0.N+1`) and only *with a CHANGELOG entry + a migration note*. The bar is
  documentation, not a freeze: reconstruction phase deliberately keeps the freedom to fix the shape,
  but the break must be announced and migratable.
- **This governs both surfaces**: the HTTP `/v1` contract (the structs behind `/openapi.json`) and the
  importable `aisotropy` Go package (type aliases + the functional-options builder). A concrete
  worked example is the `ToolExecutor` `args map[string]string` decision in ADR-0013 (Open Question 1):
  it was frozen for 0.x precisely so a later widening to `map[string]any` would be a *documented* 0.x
  change, not a silent one.

Under SemVer's 0.x convention the minor position carries breaking changes while the project is pre-1.0;
this policy adopts that convention and adds the mandatory CHANGELOG + migration-note gate on top.

## How a consumer pins and migrates

**Pin.**

- *Go consumers* pin the module version in `go.mod` (`require github.com/Luum-Home/aisotropy v0.N.x`)
  — Go modules already forbid a silent breaking upgrade within a minor.
- *Wire consumers* (TS/Python/UI) pin against a **saved copy of `/openapi.json`** for a known server
  version (the snapshot at [`examples/clients/openapi.json`](../../examples/clients/openapi.json) is
  exactly this), and generate their client from that saved spec — not from a live, possibly-newer
  server. This makes the client reproducible and pins the contract shape.

**Detect a break.** Re-fetch `/openapi.json` from the target server and regenerate:

```bash
curl -s http://<server>/openapi.json -o examples/clients/openapi.json
# TypeScript
cd examples/clients/typescript && npm run generate && npm run typecheck
# Python
cd examples/clients/python && openapi-python-client generate --path ../openapi.json \
  --meta none --output-path ./aisotropy_client --overwrite
```

Because the client is typed against the generated schema, a breaking contract change surfaces as a
**compile/type error** (`tsc --noEmit`) or a changed model/import (Python) — the safety property. A
purely additive change regenerates cleanly.

**Migrate.** Read the CHANGELOG entry and migration note attached to the minor bump, apply the field/route
changes it describes, regenerate, and re-run the type/import gate until green.

## Deprecation

- A field or route being retired is **first marked deprecated** in the same minor (documented in the
  CHANGELOG and, where expressible, via the OpenAPI `deprecated` flag on the field/operation), kept
  working, then removed **no earlier than the next minor bump**.
- SSE events are forward-compatible by construction: clients dispatch on the `event:` name and ignore
  unknown types, so adding event types is additive and retiring one follows the deprecate-then-remove
  path above.

## Road to v1

ADR-0013 commits to `v1` only **after 2–3 real external consumers exist** and the surface has proven
stable. At `v1`:

- Breaking changes to `/v1` stop being a minor-bump affair and require a parallel `/v2` with an
  explicit deprecation window.
- The `aisotropy` Go package follows standard SemVer (breaking changes require a major bump / `/v2`
  module path).

Until then, treat every minor bump as a "read the CHANGELOG before upgrading" event.

## References

- [ADR-0013 — public extension surface (façade + builder, 0.x pin)](../decisions/0013-public-extension-surface-facade-vs-promote.md)
- [ADR-0002 — anti-framework-magic / struct-as-contract](../decisions/0002-anti-framework-magic-struct-as-contract.md)
- [Constitutional gates — backward-compatible API contracts](../../.claude/rules/constitutional-gates.md)
- [Client SDKs guide](client-sdks.md) — generating and regenerating typed clients.
