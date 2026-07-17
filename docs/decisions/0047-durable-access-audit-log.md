---
type: ADR
title: "ADR-0047 — Durable, structured access-audit log independent of any OTLP backend"
description: The OTel gen_ai serving span carries tenant/user but is a no-op unless an OTLP endpoint is wired, and the cost meter is in-memory/per-process, so there was NO durable, retained who-accessed-what-tenant-when record a SOC2/ISO auditor can sample (Gap 8). This ADR adds internal/audit — an append-only access-audit sink (JSONL file selected by AISOTROPY_AUDIT_LOG, slog fallback default) plus a /v1 chi middleware emitting one Event per served request with identity + request metadata ONLY. Honest residual — retained but NOT cryptographically tamper-evident; a hash-chain/WORM sink is the named follow-on.
tags: [decisions, adr, security, audit, observability, soc2, iso27001, compliance, honesty]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0047 — Durable, structured access-audit log independent of any OTLP backend

## Status

✅ **Accepted (2026-07-17).** Security fix closing the last data-governance audit finding
(**Gap 8, MEDIUM**), alongside the at-rest-encryption ADRs ([0044](0044-working-memory-at-rest-encryption.md),
[0045](0045-facts-tier-at-rest-encryption-posture.md)) and subject erasure ([0046](0046-subject-level-erasure-gdpr-art17.md)).

## Context

An auditor mapping the runtime to **SOC 2 (CC6.1 logical-access, CC7.2 monitoring)** or
**ISO 27001 (A.12.4 logging & monitoring)** asks a simple question: *show me a durable, retained
record of who accessed which tenant, when.* As built, the runtime could not answer it durably:

- The OTel `gen_ai` serving span (ADR-0010) carries `tenant_id`/`user_id`, but the tracer provider
  is a **no-op unless an OTLP endpoint is wired** — no exporter, no record. Audit availability cannot
  depend on an operator having stood up an external tracing backend.
- The cost meter (`internal/costmeter`) is **in-memory and per-process** — it aggregates spend, it is
  not an access log, and it evaporates on restart.

So there was no retained, sample-able access trail. That is a real compliance gap, independent of any
external observability stack.

## Decision

Add a first-class, self-contained access-audit log that is **always on by default** and upgrades to a
durable file with one env var.

1. **`internal/audit`** — an append-only audit sink.
   - **`Event`**: `timestamp` (UTC, request start), `tenant_id`, `user_id`, `method`, `route` (URL
     PATH only), `status`, `request_id` (correlation id), `latency_ms`. **Identity + request metadata
     ONLY.**
   - **`Sink` interface** (`Record(Event)` + `io.Closer`), with three implementations:
     - **`JSONLSink`** — one JSON object per line, `O_APPEND|O_CREATE` file at `0o600`, writes guarded
       by a mutex so concurrent requests never interleave a partial line; `fsync` on `Close`. Selected
       by `AISOTROPY_AUDIT_LOG=<path>`.
     - **`SlogSink`** — emits each Event on a dedicated `component=access_audit` slog logger. The
       always-available **default** so there is *always* a trail (shippable to a retained log pipeline).
     - **`NopSink`** — the explicit `AISOTROPY_AUDIT_DISABLE=true` opt-out.
   - **`FromEnv()`** selects the sink; a **set-but-unopenable** `AISOTROPY_AUDIT_LOG` **fails the boot**
     (fail closed) rather than silently serving without the requested durable trail.

2. **`internal/api/audit_middleware.go`** — a **chi middleware** emitting exactly one `Event` per
   `/v1` request **after** the handler. It reads `tenant_id`/`user_id` from the URL **path** (the
   already-validated identity — the ADR-0024 must-agree bind proves path == verified principal), never
   the body. It wraps the `ResponseWriter` with chi's `WrapResponseWriter` to observe the status and
   latency **without buffering** — so a streaming (`/chat/stream`) response is audited on completion
   with its token-by-token flushing intact (the wrapper preserves `http.Flusher`).

3. **Wiring (`aisotropy/app.go`, `aisotropy/audit.go`)** — `Run` builds the sink (`WithAuditSink`
   wins, else `audit.FromEnv`) and `router.Use`s the middleware just inside `RequestID` and **outside**
   `Recoverer`, so a panicking request is still audited (as the recovered `500`). The sink is
   `fsync`+closed on graceful shutdown. `WithAuditSink` + the `AuditEvent`/`AuditSink` type aliases let
   a third-party consumer ship the trail to their own backend (`internal/audit` is not importable
   externally).

## Privacy contract (hard boundary)

The audit Event records identity + request metadata and **never** the prompt body, model response,
request/response headers (the bearer token lives there), query string, or any other payload. `route`
is `r.URL.Path` only — never `r.URL.RawQuery` — so no caller-supplied value smuggles PII/secrets into
the log. A schema-guard test (`TestEvent_NoBodyOrSecretFields`) and end-to-end leak assertions fail the
build if a body/secret field ever appears.

## Multi-tenancy posture

The audit log is an **operator artifact**. It records `tenant_id`/`user_id` for attribution and is
**not a per-tenant-readable surface** — no request path exposes any tenant's audit entries. Retention,
rotation, and access control of the sink are operator responsibilities.

## Honest residual — tamper-evidence

The JSONL sink is **append-only and retained, but NOT cryptographically tamper-evident.** An operator
(or an attacker with write access to the file) can edit or truncate it, and nothing in the file proves
otherwise. This is **retention, not tamper-proofing** — do not represent it as tamper-proof. A
**hash-chained sink** (each line commits to the prior line's digest) or a **WORM sink** (append-only
object store with an object-lock/retention policy) is the **named follow-on** for regimes that require
tamper-*evidence*, and drops in behind the same `Sink` interface / `WithAuditSink` seam with no
call-site change.

## Consequences

- **Positive:** a durable, sample-able access trail exists by default with zero configuration (slog),
  and becomes a retained file with one env var — no external OTLP/tracing backend required. The seam
  (`Sink` + `WithAuditSink`) makes the tamper-evident upgrade a drop-in. SSE is unaffected; panics are
  still audited; the boot fails closed if a requested durable log cannot be written.
- **Negative / honest deltas:** the default JSONL sink is not tamper-evident (follow-on above); the
  trail is per-process at the file layer (operators aggregate via a log pipeline or shared volume);
  `fsync` is on `Close` (not per-event) so a hard power loss can lose the tail of un-synced entries — a
  deliberate latency trade-off for an in-request write, documented rather than hidden.

## Alternatives considered

- **Rely on the OTel span + an OTLP collector.** Rejected as the *primary* control: it makes audit
  availability depend on an external backend being wired, and the default no-op tracer records nothing.
  The two are complementary — tracing for performance, this for a self-contained access record.
- **Per-event `fsync`.** Rejected as the default: it adds disk-sync latency to every `/v1` request.
  `fsync`-on-close bounds the loss window; a stricter durability mode can be a future sink variant.
- **Reuse the cost meter.** Rejected: it is an in-memory spend aggregate, not a retained per-request
  access record — wrong data model and wrong lifetime.
