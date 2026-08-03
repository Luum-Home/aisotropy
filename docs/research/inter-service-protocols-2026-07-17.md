---
type: Research
title: "Inter-service protocols for aisotropy — sync, streaming, and async chaining (2026)"
description: >
  Phase-0 research answering "can microservices chain aisotropy — as a service or
  as a framework — and what are the optimal communication forms?" Covers REST/OpenAPI,
  gRPC, Apache Thrift, GraphQL, tRPC, oRPC, WebSockets, SSE, WebTransport (synchronous +
  streaming) AND async/event-driven chaining (NATS JetStream, Kafka, RabbitMQ, Valkey,
  MQTT). Grounded in aisotropy code + ADRs. READ-ONLY; proposes a Phase-2 test plan.
tags: [research, protocols, grpc, sse, websocket, webtransport, graphql, mcp, a2a, async, nats, kafka, streaming, multi-tenancy]
status: research (Phase 0 — read-only; benchmarks NOT run)
timestamp: 2026-07-17T00:00:00Z
---

# Inter-service protocols for aisotropy (2026)

> **Scope + method.** Read-only Phase-0 research. Every aisotropy claim is grounded in a
> file/ADR; every non-code claim carries a source URL. No code was modified and no benchmark
> was run — §7 *proposes* an exhaustive Phase-2 test plan the operator approves before any run.
> Protocol capabilities were verified against 2026 authoritative sources (specs, docs,
> benchmarks); where a protocol is narrower than its marketing (oRPC, WebTransport, gRPC-Web,
> Thrift) that is stated honestly rather than smoothed over.

---

## 1. Current state (code-truth)

### 1.1 The public surface as built

aisotropy already ships a **contract-first HTTP surface**: `chi` router → `humachi` adapter →
Huma v2 API → `internal/api` handlers (`aisotropy/app.go:953-1015`). The Go struct **is** the
contract (ADR-0002), which drives both HTTP validation and the auto-generated **OpenAPI 3.1**
document served at `/openapi.json` with docs at `/docs`.

Routes (grounded in `aisotropy/app.go` and `internal/api/*.go`):

| Route | Shape | Transport | Notes |
|---|---|---|---|
| `POST /v1/tenant/{t}/user/{u}/chat` | request/response | JSON over HTTP | fail-closed reflection loop (`internal/api/chat.go`) |
| `POST /v1/.../chat/stream` | server→client stream | **SSE** (Huma `sse.Register`) | token-by-token |
| `POST /v1/.../chat/aisdk` | server→client stream | **SSE** | Vercel AI-SDK costume over the same `StreamCore` |
| `POST /v1/.../chat/agui` | server→client stream | **SSE** | AG-UI costume over the same `StreamCore` |
| `POST /v1/.../chat/agent` | request/response | JSON over HTTP | bounded tool loop, `agentMaxSteps = 3` (`app.go:57`) |
| `GET /healthz` `/health` `/costs` `/bitrate` `/openapi.json` `/docs` | request/response | JSON | outside the OpenAPI contract; ops routes gated by `AISOTROPY_OPS_TOKEN` |

The three SSE routes are all served over the **same** streaming pipeline (`StreamCore`); the two
"costumes" are re-encodings of one token stream, not separate engines
(`aisotropy/sse_buffering.go:15-18`).

### 1.2 SSE hardening that already exists

`aisotropy/sse_buffering.go` installs a `chi` middleware that sets **`X-Accel-Buffering: no`** on
exactly the three SSE suffixes so a default-configured reverse proxy (nginx buffers by default;
Caddy does not) cannot collect the event stream and deliver it as one delayed dump. It is
**defense in depth** — the reference `deploy/proxy/nginx.conf` also disables buffering, but the
header makes the stream survive an operator who forgets. Graceful shutdown waits
`shutdownTimeout = 10s` for in-flight requests, "mainly open SSE streams" (`app.go:52`).

> **Update (2026-07-17, post-report):** the recommendation below was implemented — the
> `:443 ssl; http2 on;` SSE block is now **ACTIVE** in the reference `deploy/proxy/nginx.conf`.
> The `:80`-only analysis in this section is the pre-fix baseline that motivated the change.

**Verified edge fact (load-bearing for §5.1):** the reference `deploy/proxy/nginx.conf`
client-facing server **listened on `:80` HTTP/1.1** at report time; `http2 on` appeared **only in a
commented-out `:443 ssl` block** (`deploy/proxy/nginx.conf:42, 79-86`). So out of the box the
browser-facing edge is **HTTP/1.1**, which means the **6-SSE-connections-per-domain cap applies
today** unless the operator enables the TLS/HTTP-2 block. The HTTP/2 recommendation below is
therefore a concrete config change, not a no-op.

### 1.3 Cancellation / disconnect semantics

`context.Context` is the process descriptor: tenant/user identity enters ctx **once** at the Huma
handler boundary and propagates down the whole call chain (ADR-0011, `internal/api/chat.go`). A
disconnected client cancels that ctx, which **stops the upstream LLM call immediately** — a
constitutional gate (`.claude/rules/constitutional-gates.md` → "Streaming (SSE) handlers must
respect context cancellation immediately"). This is a property of the transport pairing: it holds
cleanly for **any transport whose disconnect closes the request context** (HTTP request/response,
SSE, HTTP/2 stream reset, gRPC ctx). It is the single most important lens for evaluating a new
transport below.

### 1.4 Two entry points — the same artifact (ADR-0013 / ADR-0016)

- **(A) As a service** — `App.Run(ctx)` serves the runtime over HTTP (`app.go:930`).
- **(B) As a framework** — `App.Turn(ctx, ChatInput)` runs one chat turn **in-process, no HTTP
  hop**, at full parity with `POST /v1/.../chat` (`aisotropy/turn.go:89`). With
  `WithDefaultTenant` the multi-tenant surface **degenerates** to N=1 rather than being bypassed
  (ADR-0016 §Embeddability). Isolation still holds by construction: identity travels only via ctx
  (`turn.go:16-27`).

### 1.5 Polyglot + agent-interop boundaries already chosen

ADR-0016 (Accepted 2026-07-09) already decided the polyglot story:

- **MCP** (Model Context Protocol) is the **polyglot tool-execution boundary** — `WithMCPServer`
  exposes a stdio MCP server's tools as scope-gated aisotropy tools; the core stays Go, owns
  scoping, injects the scoped ctx (`app.go:594-623`). MCP tools merge into the **same** scope-gated
  registry as native tools.
- **OpenAPI-generated typed clients** are the boundary for **polyglot callers** (TS/Python clients
  that *talk to* a hosted aisotropy) — free, no core change.
- A **TypeScript authoring SDK** was explicitly **deferred** until 2–3 real consumers shape it.

### 1.6 What `RunTasks` actually is (code-truth correction)

`App.RunTasks` (`aisotropy/runtasks.go`) is **behind the `//go:build keybroker` tag** — an
optional flagship module, not the default runtime. It is a **concurrent fan-out of sandboxed
agentic *coding* tasks** (each task gets its own git worktree + its own sandbox container; the
key-broker invariant keeps the provider key host-side, never inside a container). It is **not** a
generic "fan out a chat turn to N remote aisotropy services" primitive. Its `OnEvent` callback is
an index-tagged progress stream a consumer *can* multiplex into an SSE channel
(`runtasks.go:29-45`). Treat "multi-aisotropy fan-out across services" as a **capability to design**
(§5.2), not one that ships today.

### 1.7 aisotropy is already async internally

This is the anchor for §6. aisotropy is **not** a pure request/response system:

- **Transactional outbox** in BadgerDB (`internal/memory/outbox.go`): on the hot path a chat
  message and its "intent to consolidate" are written in **one ACID transaction**
  (`AppendWithOutbox`), so a crash can never leave a message without its event or vice versa.
- **Async consolidation worker** (`RunOutboxWorker`) drains pending events into the facts +
  semantic sinks with **at-least-once delivery and exponential backoff** (200 ms → 30 s), deleting
  an event only once the sink confirms.
- **Cold-boot recovery** (`RecoverOutbox`) drains orphaned events on the next start — same code
  path, no special recovery mode (`outbox.go:14-21, 166-174`).
- **Idempotent sinks** (`internal/consolidation/sink.go`): "deliberately idempotent end to end …
  upserts by a stable key", so at-least-once redelivery is safe.
- The **outbox event envelope already carries `Tenant/User/Session`** (`outbox.go` `OutboxEvent`)
  and the event ID is derived from the message reference so re-enqueue is idempotent at the event
  layer.

So async inter-service chaining is an **extension of an existing, tested pattern** (durable
outbox + idempotent consumer + tenant-in-envelope), not a new concept.

---

## 2. The evaluation lens (what "optimal" means here)

A protocol is judged for a **Go, multi-tenant, streaming AI runtime** on:

1. **Request/response fit** — service-to-service call ergonomics + Go codegen.
2. **LLM token-streaming fit** — can it stream tokens as they are produced, unidirectionally,
   with low first-token latency?
3. **Backpressure / flow-control** — does the transport signal a slow consumer, or does the
   producer bloat buffers?
4. **Multiplexing & head-of-line (HoL) blocking** — HTTP/1.1 (one stream per TCP conn, 6/domain in
   browsers) vs HTTP/2 (multiplexed, but **TCP-level HoL** under loss) vs HTTP/3/QUIC (stream-level
   independence). ([SSE limits](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events), [HTTP/2 vs HTTP/3 HoL](https://systemdr.systemdrd.com/p/understanding-head-of-line-blocking))
5. **Reconnection / resumability** — can a dropped stream resume without re-running the turn?
6. **Polyglot codegen** — can a non-Go caller talk to it cheaply?
7. **Browser vs service-to-service** — many transports are one but not the other.
8. **Cancellation/disconnect** — does client disconnect cancel the request ctx (§1.3)?
9. **Multi-tenant isolation over the wire** — path/JWT identity → ctx, never body-as-authz.
10. **Ops complexity** — proxies, HTTP/3 UDP, schema registries, extra daemons.

---

## 3. Protocol × concern decision matrix (synchronous + streaming)

Scores are qualitative: **A** = strong fit, **B** = workable, **C** = poor/edge-case,
**—** = not applicable. Rationale + source per row follows the table.

| Protocol | Req/resp fit | Token-streaming fit | Backpressure | Multiplex / HoL | Resumability | Polyglot codegen | Browser | Service→service | Ops |
|---|---|---|---|---|---|---|---|---|---|
| **REST + OpenAPI** (baseline) | **A** | C (not a stream) | HTTP/TCP | HTTP/2 multiplex | — | **A** (OpenAPI gen) | **A** | **A** | **A** |
| **SSE** (current) | — | **A** | HTTP/TCP window + `X-Accel-Buffering` | HTTP/2 multiplex; HTTP/1.1 6-conn cap | **B** (`Last-Event-ID`) | **A** (plain HTTP) | **A** | **B** | **A** |
| **gRPC** (+ server-streaming) | **A** | **A** | **A** (HTTP/2 flow-control) | HTTP/2 multiplex; TCP HoL | C (manual) | **A** (protoc, Go 1st-class) | **C** (needs gRPC-Web + proxy) | **A** | **B** (HTTP/2 e2e, proxies) |
| **Connect (ConnectRPC)** | **A** | **A** | **A** | HTTP/1.1 **or** HTTP/2 | C (manual) | **A** (proto; browser native) | **B** (no proxy needed) | **A** | **A** |
| **Apache Thrift** | **A** | C (RPC-first, weak stream story) | pluggable transport | own multiplex transport | C | **B** (many langs, aging) | **C** | **B** (legacy) | **B** |
| **GraphQL** (+ subscriptions) | **B** (query graph) | **B** (`graphql-sse`/`@stream`) | via SSE/WS | via chosen transport | **B** (SSE) | **B** (schema, no LLM edge) | **A** | **B** | **B** (server + schema) |
| **tRPC** | **B** (TS only) | **B** (TS only) | — | — | — | **—** (no Go) | **A** (TS) | C | **A** (if all-TS) |
| **oRPC** | **B** (TS only) | **B** (SSE, TS only) | — | — | **B** (event-iterator) | **—** (no Go server) | **A** (TS) | C | **A** (if all-TS) |
| **WebSocket** | C (not req/resp) | **A** (full-duplex) | **B** (app-level) | own frame multiplex; TCP HoL | C (app-level) | **B** (raw, no schema) | **A** | **B** | **B** |
| **WebTransport** (HTTP/3/QUIC) | C | **A** | **A** (QUIC per-stream) | **A** (QUIC stream-level, no HoL) | **B** (streams; app resume) | C (young ecosystem, no Go server-std) | **A** (Baseline 2026) | C (immature server-side) | C (UDP/HTTP/3, cert churn) |

### 3.1 Row rationale (sourced)

**REST + OpenAPI (baseline).** The correct default for request/response and the one aisotropy
already generates for free from the struct-contract. It is *not* a streaming transport — its value
is universal reach and codegen. Keep it as the public request/response surface.

**SSE (current).** In 2026 SSE is the **de-facto token-streaming transport**: Anthropic, OpenAI,
and Google all stream tokens over SSE, and both **MCP and A2A use SSE for streaming**
([Procedure](https://procedure.tech/blogs/the-streaming-backbone-of-llms-why-server-sent-events-(sse)-still-wins-in-2025)).
Its win is fit-for-purpose: unidirectional server→client text with automatic reconnection and
built-in resumability via `Last-Event-ID`
([MDN](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events)).
Its one real trap is the **HTTP/1.1 6-connections-per-domain cap** — each open SSE stream
permanently occupies one of six slots — which **HTTP/2 multiplexing eliminates** (Chromium default
100 streams/conn, server-configurable higher)
([MDN](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events),
[dev.to](https://dev.to/abhivyaktii/understanding-server-sent-events-sse-and-why-http2-matters-1cj7)).
Serve SSE over HTTP/2 and the classic SSE objection disappears. Disconnect cancels the request ctx
→ aisotropy's LLM-cancel gate holds natively (§1.3).

**gRPC (+ server-streaming).** The clear winner for **internal, high-throughput,
service-to-service** calls in 2026: HTTP/2 framing, Protobuf, first-class Go tooling, and — unlike
SSE — **built-in HTTP/2 flow-control giving real backpressure** on a slow consumer. Reported
migration gains are workload-dependent (~25–35% streaming latency reduction, ~40–60% throughput
for high-frequency small messages), and gRPC "wins for high-frequency small-message traffic
(>1000 RPM)" while "SSE wins for browser-facing and simpler deployment"
([charlessieg.com](https://www.charlessieg.com/articles/real-time-messaging-protocols-grpc-websocket-sse-deep-dive.html)).
**Server-streaming maps cleanly onto token streaming.** The cost is browser reach: **gRPC needs
gRPC-Web + a translating proxy (Envoy)**, browsers cannot use client/bidi streaming, and
server-streaming in the browser only works in the text subprotocol
([grpc-web DeepWiki](https://deepwiki.com/grpc/grpc-web/1.1-features-and-limitations),
[MS Learn](https://learn.microsoft.com/en-us/aspnet/core/grpc/grpcweb?view=aspnetcore-9.0)).

**Connect / ConnectRPC** (worth calling out even though not in the original list, because it
dominates the gRPC-vs-REST tradeoff for a Go core). Connect is a Go library serving **gRPC,
gRPC-Web, and its own Connect protocol from one server, chosen by `Content-Type`**, works over
**HTTP/1.1 or HTTP/2**, supports all streaming subtypes, and is **browser-native without a
translating proxy**
([connectrpc.com](https://connectrpc.com/), [Buf](https://buf.build/blog/connect-web-protobuf-grpc-in-the-browser)).
It is the pragmatic bridge: proto contracts + gRPC performance internally, no Envoy, browser
callable. Flagged as the strongest gRPC-family option for aisotropy's "both A and B" posture.

**Apache Thrift.** Still maintained and still used where already established, but in 2026 "choosing
it … requires a deliberate reason, not just familiarity" — gRPC has the tooling, ecosystem, and
infra integration (Envoy, Kubernetes) momentum; "most new services choose gRPC; Thrift persists in
established polyglot stacks"
([200ok](https://www.200oksolutions.com/blog/grpc-vs-thrift-cloud-native-microservices/),
[latchkey](https://latchkey.dev/learn/tool-comparisons/grpc-vs-thrift)). Thrift's streaming story
is weak relative to gRPC/HTTP2. **No reason to adopt for a greenfield Go AI runtime.**

**GraphQL (+ subscriptions).** A *query-shape* API, not a transport, and a poor fit for
**token streaming** specifically — the LLM edge is a linear token stream, not a graph selection.
Subscriptions are moving **off WebSockets toward SSE**: the ecosystem now recommends
`graphql-sse` / `graphql-ws` over the legacy `subscriptions-transport-ws`, and one major vendor
explicitly argues to **deprecate GraphQL subscriptions over WebSockets in favor of SSE**
([WunderGraph](https://wundergraph.com/blog/deprecate_graphql_subscriptions_over_websockets),
[graphql.org](https://graphql.org/learn/subscriptions/)). Adopting GraphQL would add a schema
server and resolver layer that fights aisotropy's struct-as-contract model for zero token-streaming
benefit. **Not recommended as a core surface.**

**tRPC.** End-to-end type safety by **sharing TypeScript types between client and server — it is
TS-only by construction**. There is **no Go server**. It cannot be aisotropy's core surface; it is
only relevant to a hypothetical TS authoring SDK (deferred, ADR-0016). **Excluded for a Go core.**

**oRPC.** Verified against 2026 sources: oRPC is a **TypeScript** RPC framework whose distinctive
value is a **first-class OpenAPI story with no codegen** (dual `RPCHandler`/`OpenAPIHandler`) plus
**type-safe SSE/streaming** via an event-iterator abstraction
([orpc.dev](https://orpc.dev/), [oRPC SSE](https://deepwiki.com/middleapi/orpc/11.4-server-sent-events)).
It is real and capable — but its **server is TypeScript**; there is no Go server implementation.
For aisotropy it is a **client-side** option at most (a TS caller of aisotropy's OpenAPI), which
the OpenAPI-generated-client decision (ADR-0016) already covers. **Do not overclaim it as a
service-to-service option for a Go core** — it is not.

**WebSocket.** Full-duplex, browser-native, mature. For **token streaming it is overkill**: the
LLM edge is server→client only, and SSE gives you that plus free reconnection with less code. WS
earns its keep only when the *client* must also stream to the server mid-turn (live audio, collab
cursors, bidirectional agent negotiation). Backpressure and reconnection are **app-level** (you
build them). Still rides TCP, so it shares TCP HoL under loss.

**WebTransport (HTTP/3 / QUIC).** The genuinely new 2026 datapoint: WebTransport reached
**Baseline in March 2026** — Chrome, Edge, Firefox, and **Safari 26.4** all ship it, no flags
([webrtc.ventures](https://webrtc.ventures/2026/04/webtransport-is-now-baseline-what-it-means-for-real-time-media/),
[MDN](https://developer.mozilla.org/en-US/docs/Web/API/WebTransport_API)). Its structural
advantage is real: QUIC gives **stream-level independence — a lost packet blocks only its own
stream**, eliminating the TCP head-of-line blocking that stalls *all* HTTP/2 streams on loss (HTTP/3
"reduces latency by 55% on high-loss networks")
([systemdr](https://systemdr.systemdrd.com/p/understanding-head-of-line-blocking)). **But**: the
*browser* side is Baseline while the **server-side ecosystem is young** — there is no
std-library-grade Go WebTransport server, ops means HTTP/3 over UDP with its own cert/proxy story,
and the honest industry read is **"prefer WebTransport, fall back to WebSocket," with SSE-over-HTTP
remaining the token-streaming standard "for at least the next 2–3 years until WebTransport
matures"**
([websocket.org](https://websocket.org/guides/future-of-websockets/),
[Procedure](https://procedure.tech/blogs/the-streaming-backbone-of-llms-why-server-sent-events-(sse)-still-wins-in-2025)).
**Verdict: watch, prototype behind a flag, do not adopt as the default token transport yet** (§5.3).

### 3.2 MCP and A2A (agent/tool interop — already in aisotropy's orbit)

- **MCP** deprecated its old HTTP+SSE transport in favor of **Streamable HTTP** (spec 2025-03-26,
  retained Nov 2025): a single endpoint over POST+GET, SSE now *optional* (on-demand upgrade),
  with **session IDs (`MCP-Session-Id`) and resumable streams via `Last-Event-Id`** — the server
  replays missed events so an agent does not lose tool results on reconnect
  ([MCP transports](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports),
  [fka.dev](https://blog.fka.dev/blog/2025-06-06-why-mcp-deprecated-sse-and-go-with-streamable-http/)).
  aisotropy uses MCP over **stdio** today (`app.go:606`); a **Streamable-HTTP MCP client/server**
  is the natural next MCP surface for networked polyglot tools.
- **A2A** (Agent2Agent) reached **v1.0 under the Linux Foundation** in 2026 with a layered model:
  data model in Protobuf, and **three protocol bindings — JSON-RPC 2.0 over HTTPS (primary), gRPC,
  and HTTP/JSON/REST — with SSE for streaming and webhooks for long-running async tasks**
  ([a2a-protocol.org](https://a2a-protocol.org/v0.3.0/specification/),
  [IBM](https://www.ibm.com/think/topics/agent2agent-protocol)). A2A's own hybrid (JSON-RPC/REST
  sync + SSE stream + webhook async) is a **strong external validation of the combined stack this
  report recommends** (§5.4).

---

## 4. Scenario answers

### 4.1 Scenario 1 — aisotropy AS a service, consumed by other microservices

- **Optimal request/response:** keep **contract-first OpenAPI/REST** (already free from the
  struct-contract) as the *public, universal* surface. For **internal, high-throughput,
  latency-sensitive** service→service calls, offer **gRPC (or Connect) server-streaming** as an
  additive internal surface. Connect is preferred over raw gRPC because it serves gRPC + gRPC-Web +
  REST-ish from one Go server with **no Envoy**, over HTTP/1.1 or HTTP/2
  ([connectrpc.com](https://connectrpc.com/)).
- **Optimal token streaming:** **SSE remains the default** (it is what aisotropy already does, what
  every LLM provider does, what MCP/A2A stream over, and it gives aisotropy's disconnect→LLM-cancel
  gate for free). **Serve it over HTTP/2** to kill the 6-connection cap. Where a *pure internal*
  consumer wants tokens with real HTTP/2 flow-control backpressure, **gRPC server-streaming** is
  the internal alternative.

### 4.2 Scenario 2 — aisotropy AS a framework, embedded in a Go microservice

- The host service calls **`App.Turn(ctx, …)` in-process — no wire protocol at all** for the
  aisotropy call itself (`turn.go:89`). The *host* then chains OUT to other services over whatever
  those services speak (gRPC/REST/broker). aisotropy contributes **ctx-scoped isolation** to those
  outward calls: identity is already in ctx, so an outward gRPC/REST call inherits the tenant scope
  by passing the same ctx.
- **Multi-aisotropy fan-out** ("`RunTasks` across services") is **not** what today's `RunTasks`
  does (§1.6). The correct design is **async fan-out over a broker** (§5.2, §6) or a bounded
  concurrent gRPC/Connect scatter-gather — *not* extending the sandbox-coding `RunTasks`. Stated
  honestly so the operator does not over-read an existing primitive.

---

## 5. Recommended "optimal comms" stack (opinionated)

### 5.1 Public surface — keep contract-first, add an internal RPC lane

1. **Default public request/response:** **OpenAPI/REST** (unchanged). It is the moat-consistent
   choice — the struct is the contract, codegen is free, reach is universal.
2. **Default public token streaming:** **SSE**, kept, **served over HTTP/2** so multiplexing
   removes the per-domain connection cap. This is the lowest-risk, highest-fit choice and matches
   the entire LLM industry + MCP + A2A. **Concrete action:** the reference nginx edge is HTTP/1.1
   today (verified, §1.2) — enable the `:443 ssl` + `http2 on` block (already present, commented) so
   browser clients multiplex SSE over one connection instead of burning 6 per domain.
3. **New internal lane (additive, opt-in):** **Connect/gRPC server-streaming** for high-throughput
   service→service calls and internal token streaming where HTTP/2 flow-control backpressure
   matters. Prefer **Connect** to avoid the Envoy/gRPC-Web tax while staying gRPC-compatible.
4. **Agent/tool interop:** **MCP** (already chosen, ADR-0016) — evolve from stdio to **Streamable
   HTTP** for networked polyglot tools (session resumability is a real robustness win). Offer an
   **A2A binding** (JSON-RPC/REST + SSE + webhook) if aisotropy must be *called by* other agent
   frameworks — it costs little because aisotropy already speaks REST + SSE.

### 5.2 Fan-out across services

Do **not** repurpose `RunTasks`. Use **async broker fan-out** (§6): publish N tenant-scoped task
messages, workers (aisotropy instances) consume, results return as **completion events / webhooks**.
This decouples producers from consumers and gives redelivery + backpressure the sync path can't.

### 5.3 WebTransport — watch, don't adopt (yet)

WebTransport is Baseline in browsers (2026) and structurally beats SSE/WS on lossy networks
(QUIC stream-level HoL isolation). But server-side Go maturity and ops (HTTP/3/UDP, certs) are not
where SSE is, and the standard advice is progressive enhancement with SSE/WS fallback. **Action:**
put it in the Phase-2 test plan (§7) as a measured comparison; adopt only if the loss-network
numbers justify the ops cost for a concrete deployment. Do not make it the default token transport.

### 5.4 Why tRPC/oRPC do not fit the core

Both are **TypeScript server frameworks**; aisotropy's core is Go and stays Go (ADR-0016 non-goal:
"not committing to a TypeScript authoring SDK now"). They are relevant only as *TS clients* of
aisotropy's OpenAPI — already covered by the OpenAPI-generated-client decision. Adopting either as a
service surface would mean a parallel TS engine, exactly the trap ADR-0016 refused.

### 5.5 Coherent combined stack (one picture)

```
                         PUBLIC (universal reach)                 INTERNAL (perf)
  request/response  →    OpenAPI 3.1 / REST  ───────────────►     Connect / gRPC (unary)
  live tokens       →    SSE over HTTP/2     ───────────────►     gRPC / Connect server-stream
  tools (polyglot)  →    MCP (stdio → Streamable HTTP)            (ctx-scoped, scope-gated)
  agent interop     →    A2A (JSON-RPC/REST + SSE + webhook)
  fan-out / events  →    async broker (NATS JetStream default; Kafka at scale)  ── §6
  cancellation      →    client disconnect cancels ctx → LLM call stops  (holds across all HTTP-family transports)
```

---

## 6. Asynchronous / event-driven chaining

### 6.1 It is an extension of what already ships

aisotropy already implements the hard half of async messaging **inside the process** (§1.7):
durable transactional outbox, at-least-once worker with backoff, cold-boot recovery, idempotent
upsert sinks, and a **tenant-scoped event envelope**. Inter-service async chaining is the same
pattern with the sink replaced by (or teed to) a **network broker**. The design question is not
"how do we do async" — it's "which broker, and how does the tenant envelope survive the network".

### 6.2 Broker decision matrix (license gate applied)

The constitutional license gate (`.claude/rules/constitutional-gates.md`, `license-policy.md`)
**blocks AGPL / SSPL / BSL / ELv2 / source-available** for this SaaS and requires
**MIT/BSD/Apache/(MPL with care)**. Applied per broker:

| Broker | License | Gate | Delivery semantics | Ordering | Throughput (2026 refs) | Durability | Ops | Fit w/ outbox |
|---|---|---|---|---|---|---|---|---|
| **NATS + JetStream** | **Apache-2.0** | ✅ PASS | at-most-once (core) / at-least-once / **exactly-once** (JetStream) | per-subject via single publisher; consumer sequencing | ~200k–400k msg/s persisted | replicated streams | **low** (single Go binary, no ZK) | **best** — lightest self-hostable Apache-2.0 default |
| **Apache Kafka** | **Apache-2.0** | ✅ PASS | at-least-once default; **EOS** via idempotent producer + transactions | **per-partition** strict | ~500k–1M+ msg/s batched | log, long **replay** | **high** (cluster, retention, partitions) | strong where **replay/scale** dominate |
| **Redpanda** | **BSL 1.1** (community) | ❌ **FAIL** | Kafka-compatible | per-partition | very high | log | medium | **blocked by gate** — do not adopt |
| **RabbitMQ / AMQP** | **MPL-2.0** | ⚠️ CAUTION (permitted) | at-most-once / at-least-once | per-queue FIFO | ~50k–100k msg/s durable | queues | medium | fine for routing-rich workflows |
| **Valkey (Streams)** | **BSD-3-Clause** | ✅ PASS | at-least-once (consumer groups + ACK) | per-stream | high (in-mem) | AOF/RDB (weaker than log brokers) | **low** | good lightweight option; **use Valkey, not Redis** |
| **Redis (Streams)** | **RSALv2 / SSPL / AGPLv3** tri | ❌ **FAIL** | — | — | — | — | — | **blocked** (all three arms fail the gate) |
| **MQTT** (Mosquitto EDL / EMQX Apache-2.0) | **BSD-like / Apache-2.0** | ✅ PASS (permissive broker) | QoS 0/1/2 (2 = exactly-once) | per-topic (weak) | high (small msgs) | broker-dependent | low | edge/device fan-in, not core event log |
| Cloud pub/sub (SQS/SNS, GCP Pub/Sub) | managed | ⚠️ **sovereignty caveat** | at-least-once (FIFO variants) | limited | managed | managed | **none (managed)** | violates self-hostable/sovereign posture (ADR-0020) |

Sources for the capability/license claims:
[NATS/Kafka/RabbitMQ semantics + licenses](https://www.index.dev/skill-vs-skill/backend-kafka-vs-rabbitmq-vs-nats),
[NATS Apache-2.0 + JetStream EOS](https://nats.io/about/),
[Kafka Apache-2.0 + per-partition order](https://en.wikipedia.org/wiki/Apache_Kafka),
[RabbitMQ MPL](https://en.wikipedia.org/wiki/RabbitMQ),
[Redis tri-license RSALv2/SSPL/AGPLv3](https://redis.io/legal/licenses/),
[Valkey BSD-3 Linux Foundation fork](https://www.softwareseni.com/the-redis-valkey-fork-how-enterprises-rapidly-migrated-after-the-sspl-license-change/).

> **Honest note on "exactly-once".** EOS is subtle and **scoped**: Kafka's EOS holds within its
> own transactional boundary (producer→topic→consumer offset commit), not across an arbitrary
> external side-effect; JetStream's exactly-once relies on publisher dedup windows + consumer ACK.
> The robust posture aisotropy **already** takes is the correct one regardless of broker:
> **at-least-once delivery + idempotent consumers** (`sink.go` upserts by key). Keep that;
> treat EOS as a latency/complexity optimization, not a correctness crutch.

### 6.3 Recommended async chaining pattern for aisotropy

1. **Keep the internal BadgerDB outbox as the source of truth.** It already gives crash-safe
   enqueue and cold-boot recovery. Do **not** replace it with a broker — the broker is a *sink/tee*,
   not the transactional record.
2. **Add an optional outbox → broker relay.** A new drain target publishes each drained
   `OutboxEvent` to the broker (tenant/user/session already in the envelope). Because the outbox is
   at-least-once and the downstream is idempotent, the relay inherits correct semantics.
3. **Default broker: NATS JetStream** — the lightest self-hostable **Apache-2.0** option (single Go
   binary, no ZooKeeper), with at-least-once + optional exactly-once. **Escalate to Kafka** only
   when **replay or very high throughput** dominates (Kafka's log + partitions earn their ops cost
   there). **Valkey Streams** is the minimal option when a broker is overkill but a network queue is
   needed.
4. **Expose a tenant-scoped async task surface:** `submit-now, notify-on-done`. A caller POSTs a
   task, gets a task ID immediately, and receives completion via **webhook/callback** (A2A-style
   push notification to a client-supplied secure URL) or by subscribing to a **per-tenant completion
   subject/topic**. This is the natural network generalization of `RunTasks`'s `OnEvent` completion
   callback (§1.6) and of A2A's webhook model for long-running tasks
   ([A2A async/webhook](https://a2a-protocol.org/v0.3.0/specification/)).
5. **Dead-letter queue + bounded redelivery** for events a consumer repeatedly fails, mirroring the
   outbox's backoff ceiling (30 s) rather than retrying forever.

### 6.4 Multi-tenant isolation over async — the honest boundary

Inside the process, isolation is **by construction**: no type can mint a `Scope`
(ADR-0011/ADR-0016). **Once a message leaves the process, the Go type system cannot enforce that
guarantee** — a broker moves bytes, not `Scope` values. State this plainly. The boundary becomes:

- **`tenant_id` in the message envelope/headers** (the outbox envelope already carries it), plus
- **per-tenant subjects/topics/partitions** (e.g. `events.{tenant}.consolidate`) so a consumer
  subscription is scoped by subject, plus
- **consumer-side re-scoping**: the consuming aisotropy instance injects `tenant_id` from the
  envelope into ctx at ingestion — the **same "identity → ctx once, then by-construction"** move the
  HTTP boundary makes (§1.3). The envelope + subject + consumer-side scoping **is** the isolation
  contract across a broker; there is no stronger guarantee available over the wire, and pretending
  otherwise would be a strawman.

### 6.5 How async decouples for robustness (the operator's priority)

The sync path (REST/gRPC/SSE) couples caller and callee in time — if the callee is down, the call
fails. The async path **decouples**: the outbox durably holds the event, the broker buffers,
consumers scale independently, and redelivery + DLQ absorb transient failures. aisotropy's cold-boot
`RecoverOutbox` already proves the pattern's resilience within the process; extending it across a
broker extends that resilience across services — at the cost of eventual (not immediate)
consistency, which is the correct tradeoff for fan-out and fire-and-forget task completion.

---

## 7. PROPOSED Phase-2 exhaustive test plan (DO NOT RUN — operator approves first)

All benchmarks run against a controlled harness; **no run happens until the operator approves**.
Report p50/p95/p99, not just means. Fix the model behind a deterministic stub or a token-rate
generator so transport — not model latency — is measured.

### 7.1 Token-streaming latency & throughput (transport bake-off)

- **Transports:** SSE/HTTP1.1, SSE/HTTP2, gRPC server-streaming, Connect server-streaming,
  WebSocket, WebTransport/HTTP3.
- **Metrics:** **time-to-first-token (TTFT)**, inter-token latency distribution, sustained
  tokens/s, CPU/mem per open stream, max concurrent streams before degradation.
- **Load:** N concurrent tenants ∈ {10, 100, 1 000, 5 000}, each holding an open stream; verify the
  **HTTP/1.1 6-connection cap** manifests for SSE/1.1 and **does not** for SSE/2.

### 7.2 Backpressure under slow consumers

- Throttle the client read rate to X% of production rate; measure server-side buffer growth and
  memory. Expected: gRPC/Connect (HTTP/2 flow-control) and WebTransport (QUIC) signal backpressure;
  SSE relies on TCP window + `X-Accel-Buffering`. Confirm aisotropy does not OOM under a stalled
  reader.

### 7.3 Cancellation / disconnect correctness

- Kill the client mid-stream for each transport; assert the **upstream LLM call is cancelled**
  (constitutional gate, §1.3) via a counter on the stub client. Measure cancel-propagation latency.
  This is a **correctness gate**, not a perf number — any transport that fails it is disqualified as
  a token surface.

### 7.4 Reconnection / resumability

- Drop the connection at 50% of a stream; test resume for SSE (`Last-Event-ID`), MCP Streamable
  HTTP (`Last-Event-Id` replay), WebSocket (app-level), WebTransport (app-level). Assert **no
  duplicated and no lost tokens** on resume.

### 7.5 HoL blocking under packet loss

- Inject 1–5% loss (tc/netem). Compare HTTP/2 (SSE, gRPC) vs HTTP/3 (WebTransport) tail latency
  across **many multiplexed streams on one connection**. Expected: HTTP/2 tail degrades (TCP HoL),
  HTTP/3 holds (QUIC stream-independence)
  ([systemdr](https://systemdr.systemdrd.com/p/understanding-head-of-line-blocking)). This is the
  one test that could justify adopting WebTransport.

### 7.6 Async: outbox → broker → consumer

- **Throughput:** drive the outbox at rising rates; measure end-to-end outbox-enqueue → broker →
  consumer-ACK latency and sustained msg/s for **NATS JetStream, Kafka, RabbitMQ, Valkey Streams**.
- **Delivery-semantics-under-failure:** kill a consumer mid-batch; assert **at-least-once
  redelivery** and that the idempotent sink produces **no duplicate facts/vectors** (reuse the
  existing `sink_test.go` idempotency assertion as the oracle).
- **DLQ:** force a poison message; assert bounded redelivery then DLQ, no infinite retry.
- **Local harness (floci.io, MIT):** run §7.6/§7.7 against **cloud-managed** queues (AWS
  SQS/SNS, Azure Service Bus/Event Hubs, GCP Pub/Sub) locally and cost-free via
  [floci.io](https://floci.io/)'s emulators — a dev/test complement to the self-hostable
  NATS/Kafka/RabbitMQ/Valkey row above; the self-hostable brokers stay the production
  recommendation (sovereignty). floci also gives the Tier-1 Lambda-container serverless
  proof cost-free (see `docs/deployment/serverless-edge.md`).

### 7.7 Async: tenant isolation across the broker

- Publish interleaved events for tenants A and B on per-tenant subjects; assert a consumer
  subscribed to A **never** receives B, and that consumer-side ctx re-scoping rejects an envelope
  whose `tenant_id` fails `memory.Key.Validate`. This is the **isolation correctness gate** for the
  async path — a failure here blocks the feature regardless of throughput.

### 7.8 License-gate CI check (not a benchmark, but part of Phase 2)

- Add an automated gate asserting any adopted broker client dependency resolves to
  MIT/BSD/Apache/MPL; fail the build on AGPL/SSPL/BSL/ELv2 (catches a Redpanda/Redis client sneaking
  in transitively).

---

## 8. Honest risks

1. **HTTP/2 for SSE is an ops precondition, not automatic.** The 6-connection fix only lands if the
   edge (proxy/LB) actually serves SSE over HTTP/2 end-to-end; a mis-configured HTTP/1.1 hop
   re-introduces the cap.
2. **gRPC/Connect adds a second contract surface** alongside OpenAPI — real maintenance and a
   struct-vs-proto duplication risk. Mitigate by generating proto from the same source of truth or
   scoping gRPC to internal-only.
3. **WebTransport server maturity in Go is the gating unknown** — browser Baseline ≠ production Go
   server. Do not commit before §7.5 numbers.
4. **Async isolation cannot be type-enforced over the wire** (§6.4). The envelope + subject +
   consumer-side re-scoping is the *whole* contract; a bug there is a cross-tenant leak the compiler
   won't catch. It needs the §7.7 test as a permanent gate.
5. **Broker license drift** (Redis→SSPL/AGPL, Redpanda BSL) is exactly the trap the gate exists for;
   pin **Valkey/NATS/Kafka** and CI-check transitive clients (§7.8).
6. **EOS temptation.** Chasing exactly-once instead of leaning on the already-idempotent sink adds
   complexity for a guarantee that is narrower than it sounds (§6.2 note).
7. **Two persistence substrates.** ADR-0016 §Open-Q3 already refused a second durable store next to
   BadgerDB outbox + rqlite; an async broker must be a **sink/tee**, not a competing transactional
   record, or it re-opens that conflict.

---

## TRUST_REPORT: SCORE=82 STATUS=MEDIUM EVIDENCE=9 UNCERTAINTIES=6
---
Score: 82/100

**Top-line recommended stack.** Keep **contract-first OpenAPI/REST** as the universal public
request/response surface and **SSE (served over HTTP/2)** as the default token-streaming surface —
it is what aisotropy already ships, what the whole LLM industry + MCP + A2A stream over, and it
gives aisotropy's disconnect→LLM-cancel gate for free. Add an **internal Connect/gRPC
server-streaming lane** for high-throughput service→service calls and backpressured internal token
streaming (Connect over raw gRPC to skip the Envoy/gRPC-Web tax). Keep **MCP** as the polyglot tool
boundary (evolve stdio → Streamable HTTP) and offer an **A2A** binding for agent interop. For
**fan-out and fire-and-forget**, extend the existing transactional outbox into an **outbox→broker
relay with NATS JetStream (Apache-2.0) as the lightest self-hostable default, Kafka where
replay/scale dominate, Valkey Streams (BSD) as the lightweight option** — all gate-passing;
**Redis, Redpanda, and cloud-managed pub/sub are rejected** (license/sovereignty). **tRPC/oRPC are
TS-only and do not fit the Go core; Thrift has no reason over gRPC; GraphQL adds a schema layer for
no token-streaming benefit; WebTransport is watch-and-prototype, not adopt-yet.**

WHAT I VERIFIED (evidence):
- Current surface, SSE routes, `X-Accel-Buffering` middleware, 10s SSE-drain shutdown, ctx-cancel
  gate — read in `aisotropy/app.go`, `aisotropy/sse_buffering.go`, and the constitutional-gates rule.
- `App.Turn` in-process parity + isolation-by-ctx — read in `aisotropy/turn.go`.
- `RunTasks` is `//go:build keybroker` sandbox-coding fan-out, NOT remote chat fan-out — read in
  `aisotropy/runtasks.go` (corrected the operator's framing honestly).
- Transactional outbox: one-ACID-tx enqueue, at-least-once + 200ms→30s backoff, `RecoverOutbox`
  cold-boot drain, tenant-in-envelope, idempotent upsert sink — read in `internal/memory/outbox.go`
  and `internal/consolidation/sink.go`.
- ADR-0016 polyglot decisions (MCP boundary, OpenAPI clients, TS SDK deferred, L0/L1/L2, node gets
  scoped ctx) — read in `docs/decisions/0016-...md`.
- Every protocol capability + 2026 status is backed by a cited source URL (gRPC/SSE, WebTransport
  Baseline, oRPC TS-only, Thrift status, MCP Streamable HTTP, A2A v1.0, HTTP/2-vs-HTTP/3 HoL, SSE
  6-conn cap, broker semantics + licenses, Redis tri-license, Valkey BSD).

UNSURE ABOUT:
1. Exact Go server-side maturity of WebTransport libraries in mid-2026 — sources confirm browser
   Baseline but I did not verify a specific production-grade Go server; §7.5 is gated on real numbers.
2. RESOLVED — verified `deploy/proxy/nginx.conf`: the client-facing edge listens on `:80` HTTP/1.1
   by default (`http2 on` only in the commented `:443` block), so the 6-connection cap applies today
   and the HTTP/2 recommendation is a concrete, actionable config change (folded into §1.2 / §5.1).
3. Precise 2026 throughput numbers per broker are from secondary benchmark posts, not first-party
   reproductions — treat the ranges as directional, which is why §7.6 re-measures them.
4. Redpanda community edition license — I asserted BSL 1.1 from general knowledge + the gate's own
   BSL entry; I did not fetch Redpanda's current LICENSE file this session. Flagged as FAIL
   conservatively; verify before any adoption.
5. Whether a Connect/gRPC surface can share the struct-as-contract source cleanly, or would
   duplicate proto — an architecture question for Phase 1, not settled here.
6. I did not read the full 1558-line `app.go` (read ~1039 lines) or the `deploy/proxy/` dir; a
   streaming/route detail could live in the unread tail, though the SSE/route/gate claims are
   corroborated across `sse_buffering.go` and the architecture rule.

HUMAN SHOULD CHECK:
- Confirm the reference proxy serves SSE over HTTP/2 (else the 6-connection-cap fix is moot).
- Approve/trim the §7 Phase-2 plan before any benchmark runs.
- Confirm Redpanda's current license before it is even prototyped (gate FAIL asserted).
