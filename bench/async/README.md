# Async broker benchmark — outbox → broker → consumer

A self-contained **prototype + benchmark** of aisotropy's async consolidation path
carried over a message **broker** instead of the in-process worker. It drains an
outbox-shaped event stream, publishes each event to a broker on a **per-tenant
subject**, and consumes+ACKs it downstream — measuring latency, throughput,
delivery-under-failure, and tenant isolation for four self-hostable brokers.

Everything lives under `bench/async/**` with its own `go.mod`. It does **not**
modify `internal/`, `aisotropy/app.go`, or anything else in the main module.

## What it measures

| Broker | Server license | Client lib (license) | Gate |
|---|---|---|---|
| **NATS JetStream** | Apache-2.0 | `nats-io/nats.go` (Apache-2.0) | ✅ recommended default |
| **Apache Kafka** | Apache-2.0 | `twmb/franz-go` (BSD-3) | ✅ gate-passing Kafka-API option |
| **Redpanda** | **BSL (source-available)** | `twmb/franz-go` (BSD-3) | 🚫 **LICENSE-GATE BLOCKER** — measured only |
| **Valkey Streams** | BSD-3-Clause | `valkey-io/valkey-go` (Apache-2.0) | ✅ gate-passing |

> **Redpanda license flag (honest):** the Redpanda **server** ships under the
> **Business Source License (BSL)**, which
> `.claude/rules/cos/license-policy.md` lists as a 🚫 BLOCKER for SaaS /
> managed-service use. It is included here **because the operator asked for a
> measured comparison**, not because it is adoptable. Adopting the Redpanda
> *server* would require an explicit ADR exception. Its Kafka-protocol
> compatibility means the **Apache-2.0 Apache-Kafka** server is the drop-in,
> gate-passing substitute for the same client code (`franz-go`). All Go client
> libraries used are MIT/Apache/BSD and pass the gate unchanged.

## Fidelity to the real outbox

`envelope.go` mirrors `internal/memory.OutboxEvent`
(`internal/memory/outbox.go`) **byte-for-byte** by JSON tag
(`id/tenant/user/session/content/created_at`). It is duplicated, not imported,
because Go forbids importing an `internal/` package across a module boundary and
this bench is its own module. The one added field, `_bench_pub_nanos`, is a
benchmark-only publish timestamp for latency and is ignored by any real consumer.

`oracle.go` is the **idempotency oracle**: the stand-in for the real
`ConsolidationSink`, whose contract states delivery is *at-least-once* and the
sink *MUST be idempotent*. It creates one fact per unique event ID and treats
every re-delivery of that ID as an idempotent no-op — so `facts == unique ids`
(no duplicate facts) while `deliveries > facts` proves redelivery happened.

`relay.go` is the **prototype relay**: it drains an `OutboxSource` (in-memory
stand-in for the BadgerDB `outbox:pending:*` scan) and publishes each event with
confirmation. A publish failure re-queues the event, exactly as the real
`RunOutboxWorker` leaves a failed event pending — at-least-once handoff.

## The four scenarios (`scenarios.go`)

1. **Latency + throughput** — one consumer per tenant drains while the relay
   publishes N events with 8 confirmed-publish workers. Latency is
   `consumer-ack-time − relay-publish-time` (single process, one monotonic
   clock, no distributed-clock skew). Throughput is confirmed publishes / drain
   wall-time — the rate an at-least-once outbox can actually hand off.
2. **Delivery under failure** — publish a backlog, run consumer #1 that ACKs a
   prefix then is **hard-closed with in-flight messages unacked** (a crash
   analogue: a dead process cannot signal), then consumer #2 (same durable /
   group / PEL identity) drains the rest. Assert **every id delivered**
   (at-least-once) and **facts == published** (idempotency held). `duplicates > 0`
   proves a real redelivery occurred.
3. **Tenant isolation** — publish to tenant A and tenant B; a consumer bound to
   A's subject/topic/stream must observe **zero** B envelopes.

Each scenario uses **disjoint tenant names** so no broker state bleeds between
tests.

## Per-broker isolation model

- **NATS JetStream** — one stream `bench.evt.>`; consumers filter on
  `bench.evt.<tenant>` (server-side subject filter). Native dedup via
  `Nats-Msg-Id = id`. Redelivery via `AckWait` expiry (push consumer).
- **Kafka / Redpanda** — one topic per tenant `bench.evt.<tenant>`; consumer
  subscribes to exactly one topic. Redelivery via consumer-group **uncommitted
  offsets** re-fetched by the next member.
- **Valkey Streams** — one stream key per tenant `bench:stream:<tenant>` +
  consumer group; a consumer reads only its tenant's key. Redelivery via the
  group **PEL** (pending-entries list): a same-named restarted consumer re-reads
  its unacked entries (`XREADGROUP id 0`) before new ones (`id >`).

## Running

```bash
cd bench/async
go run ./cmd/bench                 # all four brokers
go run ./cmd/bench -only nats,kafka # subset
go run ./cmd/bench -n 5000 -redeliver 500 -iso 1000  # bigger workload
```

The runner spins each broker in Docker (`asyncbench-*` containers), waits for
readiness, runs the scenarios, tears down the streams/topics it created, and
`docker rm -f -v`s the container. It **only ever touches `asyncbench-*`
containers** — never `aisotropy-rqlite-1`, `aisotropy-qdrant-1`, woocommerce, or
CI-runner containers.

Ports (host): NATS `14222`, Kafka `19092`, Redpanda `19093`, Valkey `16379` —
all chosen to avoid the existing aisotropy service ports.

## floci.io — the cloud-managed-queue local path

A production deployment may prefer a **cloud-managed** queue (AWS SQS/SNS, Azure
Service Bus, GCP Pub/Sub) instead of self-hosting. Those cannot be benchmarked
offline, but [**floci.io**](https://floci.io) (MIT) emulates them locally so the
relay's *managed-queue* adapter can be developed and tested **cost-free** against
a local SQS/Service-Bus/Pub-Sub-compatible endpoint:

```bash
# illustrative local dev/test path (not run in this bench):
docker run -d --name asyncbench-floci -p 4566:4566 <floci.io image>
# point an SQS-shaped relay adapter at http://localhost:4566
```

Status here: **documented, not measured** — this run benchmarks the four
self-hostable brokers end-to-end. floci.io is the **local emulation** lane for a
future managed-queue adapter (dev/test only). **Production still targets a
self-hostable, gate-passing broker (NATS JetStream or Apache Kafka)**; a managed
queue would be an operator deployment choice validated locally via floci.io, and
its SDK/license would need its own gate check before adoption.

## Results

See `results.txt` (regenerated by each run) for the measured latency
percentiles, throughput, redelivery, and isolation verdicts, plus a JSON
appendix.
