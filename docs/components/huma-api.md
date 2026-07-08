---
type: Component
title: Huma — Contract and API Layer
description: Aisotropy's perimeter guardian — validates HTTP against the Go struct, extracts identity/scopes into the ctx, autogenerates OpenAPI 3.1, and sustains SSE streaming with cancellation.
tags: [components, huma, api, openapi, contracts, go, sse]
timestamp: 2026-07-08T00:00:00Z
---

# Huma — Contract and API Layer

**Language / license:** Go / MIT · **Repo:** `github.com/danielgtaylor/huma`.

## Role in the system

Huma is the **syscall layer** of the [LLM OS](../architecture/overview.md#llm-os) model: the point
where the outside world (raw, potentially hostile HTTP) is converted into typed, trusted data
before touching any business logic or the LLM. It is the first *gate* of the
[request lifecycle](../architecture/overview.md#lifecycle-of-a-request) (step 1) and
the last point of contact with the client in step 6 (output stream).

It does not replace the HTTP router — it mounts **on top of** one via an adapter (Chi, Go 1.22+'s
stdlib `http.ServeMux`, or others; "bring your own router"). Huma provides the contract and
documentation layer on top, not the routing itself.

## Responsibilities

| Responsibility | How |
|---|---|
| Validate HTTP input/output | Reflects over the endpoint's Go struct, derives a JSON Schema, and automatically validates body, query, headers, and path params. Errors as `application/problem+json` (RFC 9457). |
| Autogenerate OpenAPI 3.1 | The spec comes straight from the same struct — no parallel YAML that goes out of sync. Interactive docs served at `/docs` (Stoplight Elements). |
| Extract identity and scopes | A middleware/security handler decodes the auth token and **injects `tenant_id`, `user_id`, and `scopes` into the `context.Context`** that travels through the rest of the pipeline. No business handler parses auth headers on its own. |
| SSE streaming with cancellation | Chat endpoints expose Server-Sent Events; if the client closes the connection, the handler's `ctx` is cancelled and that cancellation propagates to [Gollem](gollem-orchestrator.md), which cuts the in-flight LLM call. |
| Content negotiation | JSON by default, CBOR optional; automatic PATCH via JSON Merge Patch (RFC 7386) or JSON Patch (RFC 6902). |

## Integration surface

```
Raw HTTP → [router: chi/stdlib] → [Huma: validates + extracts ctx] → business handler
                                            │
                                            ▼
                              ctx{tenant_id, user_id, scopes}
                                            │
                                            ▼
                                    Gollem (orchestrator)
```

- **With the single struct:** the same Go type Huma uses to validate the request is what
  [Gollem](gollem-orchestrator.md) uses to force the LLM's structured output. A single source of
  truth — see [the struct as single contract](../vision/thesis.md#the-struct-as-the-single-source-of-truth).
- **With `context.Context`:** Huma is the only layer authorized to *write*
  `tenant_id`/`user_id`/`scopes` into the ctx. Everything that runs afterward (memory hydration,
  Gollem, tools) only *reads* from the ctx — there is never global state or a second identity
  parse. See [ctx-based isolation](../vision/principles.md#derived-operating-principles).
- **With working memory:** the handler, once the ctx is populated, triggers the parallel
  hydration of [BadgerDB](badgerdb-working-memory.md)/[rqlite](rqlite-facts-memory.md)/[Qdrant](qdrant-semantic-memory.md)
  described in step 3 of the lifecycle — Huma knows nothing about these layers, it just delivers a clean ctx.
- **With quota control:** the token-bucket (step 2 of the lifecycle) runs as huma-aware
  middleware, after identity extraction and before memory hydration.

## Key configuration / patterns

- **One handler, one input struct, one output struct.** No generic handlers with `map[string]any` —
  that reintroduces the magic the project rejects (see [zero magic](../vision/principles.md#derived-operating-principles)).
- **Explicit security handler**, not hidden implicit middleware: the extraction of
  `tenant_id`/`user_id`/scopes must be readable, auditable code, a direct requirement of the
  anti-magic principle.
- **SSE as a first-class Huma operation**, not a hack outside the framework — this way the
  contract (headers, event format) is documented in the same OpenAPI spec.
- *(Exact SDK signatures — function names for registering operations, exact shape of the
  security handler, streaming API — to verify against the real API before coding.)*

## Gotchas and risks

- **The router adapter matters.** Huma doesn't impose a router; if the project switches from Chi to
  another one, it's necessary to revalidate that the adapter propagates the cancellable `ctx` the same way in SSE.
- **Ctx cancellation is not automatic across the whole chain.** If some intermediate point (e.g. a
  memory-hydration goroutine) doesn't respect `ctx.Done()`, the token savings from cancelling
  an SSE connection don't materialize — every ctx consumer needs to be audited.
- **Strict validation vs. contract evolution.** Since the struct governs HTTP + OpenAPI + LLM
  schema at once ([thesis](../vision/thesis.md#the-struct-as-the-single-source-of-truth)), a field change
  breaks all three planes simultaneously — a consistency advantage, but it demands API
  versioning discipline.
- **Not an agent framework.** Huma knows nothing about LLMs, tools, or reflection loops — that
  responsibility is 100% [Gollem](gollem-orchestrator.md)'s. Confusing layers couples the HTTP contract
  with cognitive logic.

## Related decisions

- [ADR-0002](../decisions/index.md) — anti-framework-magic: the struct as single contract is the
  basis for why Huma (reflection over types) is preferred over frameworks with their own DSLs.
- [ADR-0001](../decisions/index.md) — Go+Rust in-process-first stack: Huma runs in-process, with no
  network latency, consistent with the golden rule of the [stack](../architecture/overview.md).
- See also the open question about multi-tenancy strategy ([ADR-0007](../decisions/index.md)):
  directly affects what Huma injects into the ctx.

## References

- Repo: [github.com/danielgtaylor/huma](https://github.com/danielgtaylor/huma)
- Cognitive counterpart: [Gollem](gollem-orchestrator.md)
- [Architecture overview](../architecture/overview.md) · [Design principles](../vision/principles.md) · [Thesis](../vision/thesis.md)
