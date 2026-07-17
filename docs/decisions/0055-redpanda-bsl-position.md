---
type: Decision
title: "ADR-0055 — Redpanda's BSL position for the async broker: a clarification, not a license exception"
description: Resolves the async benchmark's flag that Redpanda's server ships under BSL (a license-gate blocker). Because aisotropy speaks the Kafka wire protocol via franz-go (BSD-3, gate-passing) and carries NO Redpanda-specific code or dependency in its runtime, the BSL applies only to a Redpanda server an operator chooses to run — the operator's own infrastructure decision, not an aisotropy code dependency. aisotropy therefore needs no BSL exception. The sovereign, gate-clean async default remains NATS JetStream (Apache-2.0), with Apache Kafka the permissive Kafka-API option.
tags: [decision, adr, licensing, bsl, redpanda, kafka, franz-go, nats, async, broker, license-gate]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0055 — Redpanda's BSL position for the async broker: a clarification, not a license exception

## Status

✅ **accepted (2026-07-17)** — a **clarification**, not an aisotropy license exception.
Follows the code-truth discipline (`.claude/rules/prune-evidence-gate.md`): every claim
below about what the runtime depends on is verified against the module graph
(`go.mod`/`go.sum`, `git grep`), not asserted from a doc. Companion to the async broker
benchmark (`bench/async/`) and its README verdict.

## Context

The async broker benchmark (`bench/async/`) measured four transports — NATS JetStream,
Apache Kafka, Redpanda, and Valkey — and produced a clear verdict:

> **NATS JetStream (Apache-2.0) is the recommended, gate-clean default**
> (`bench/async/README.md`, results row: "✅ recommended default").

Redpanda was measured **for completeness of the comparison**, but the benchmark flagged
it as a **license-gate blocker**. The exact label the bench attaches is honest and loud:

- `bench/async/cmd/bench/main.go` connects the Redpanda run with the license string
  `"BSL (source-available) [LICENSE-GATE BLOCKER]"`.
- `bench/async/broker_kafka.go` documents in-source that the **server** license is BSL
  and "NOT adoptable without an ADR exception per `.claude/rules/cos/license-policy.md`".

This ADR resolves that flag. The license gate
(`.claude/rules/cos/license-policy.md`, and the repo-wide license clause in
`.claude/rules/constitutional-gates.md`) blocks BSL / SSPL / source-available licenses
as **dependencies** of aisotropy — the stack is deliberately 100% MIT / Apache-2.0.
Redpanda's server ships under the **Business Source License 1.1** (source-available;
each release converts to Apache-2.0 on a rolling delay — roughly four years per
Redpanda's own change-date terms). The question the flag raises is therefore:
**does measuring, or running against, Redpanda put a BSL dependency inside aisotropy?**

Two code-grounded facts frame the answer:

1. **aisotropy speaks the Kafka wire protocol via `franz-go` (BSD-3-Clause,
   gate-passing) — not via any Redpanda-specific client.** The benchmark's Kafka client
   (`bench/async/broker_kafka.go`) uses `github.com/twmb/franz-go` (`.../pkg/kgo`,
   `.../pkg/kadm`). The **same** `KafkaBroker` adapter drives two servers with very
   different licenses — Apache Kafka (server: Apache-2.0) and Redpanda (server: BSL) —
   because the client is agnostic to which Kafka-API server answers on the wire. The
   BSL never touches the client; it lives entirely in the server binary an operator
   would deploy.

2. **The runtime carries NO Redpanda-specific code or dependency, and no franz-go
   dependency at all.** The aisotropy runtime module is
   `github.com/Luum-Home/aisotropy`; its `go.mod`/`go.sum` contain **zero** references
   to `franz-go`, `twmb`, or `redpanda` (verified: `grep -in "franz\|redpanda\|twmb"
   go.mod go.sum` → none). Even franz-go — the gate-*passing* BSD-3 client — is not a
   runtime dependency: it lives only in a **separate, non-imported Go module**,
   `github.com/aisotropy/bench-async` (`bench/async/go.mod`), which the root module does
   not require. Redpanda appears in the tree only as a benchmark harness image string
   and README/results prose (`git grep -il redpanda` → `bench/async/*` and one research
   doc), never as compiled runtime code.

So the BSL applies to a **Redpanda server an operator chooses to run** — i.e. the
operator's own infrastructure and deployment decision — **not to any aisotropy code
dependency**. There is no BSL code inside aisotropy to gate.

## Decision

**aisotropy has no Redpanda dependency, so its 100%-permissive (MIT / Apache-2.0)
license posture is unaffected. This is a clarification, not a license exception:
aisotropy needs no BSL exception because it carries no BSL code.**

Concretely:

1. **No exception is granted or required.** The license-gate exception process in
   `.claude/rules/cos/license-policy.md` (§Exceptions) exists for adopting a restrictive
   dependency **into the project**. Nothing is being adopted into aisotropy here, so the
   process does not apply. The benchmark's `[LICENSE-GATE BLOCKER]` label correctly
   means "do not vendor the Redpanda server as an aisotropy dependency" — and nothing
   proposes to.

2. **Operator guidance (their BSL discretion).** An operator MAY point aisotropy's
   Kafka-API async path at a Redpanda broker **at their own discretion and under their
   own BSL compliance responsibility** — notably Redpanda's BSL "competing product"
   restriction (you may not offer the BSL-covered software as a competing commercial
   service before its change date). That is a decision about the operator's own
   infrastructure, made and owned by the operator, exactly as with any other external
   broker they self-host or subscribe to. aisotropy neither ships nor endorses the
   Redpanda server; it only speaks the Kafka wire protocol that Redpanda happens to
   serve.

3. **The sovereign, gate-clean default is unchanged.** **NATS JetStream (Apache-2.0)**
   remains the recommended default async broker (`bench/async/README.md` verdict).
   Where an operator specifically wants Kafka semantics, **Apache Kafka (server:
   Apache-2.0), reached via franz-go (BSD-3)** is the permissive, gate-passing Kafka-API
   option. Redpanda is a measured data point for the comparison, not a default and not a
   recommendation.

## Consequences

**Easier / clarified:**

- The `[LICENSE-GATE BLOCKER]` label in `bench/async/` now has a canonical resolution to
  cite: it blocks *vendoring the Redpanda server as a dependency*, which no one is doing;
  it does not block *measuring* Redpanda or an operator *choosing* it.
- The 100%-permissive stack claim (`.claude/rules/constitutional-gates.md` §License
  policy) stands without an asterisk — the benchmark introduces no BSL code into the
  supply chain.
- Operators get an honest, bounded answer: a permissive default (NATS JetStream), a
  permissive Kafka-API option (Apache Kafka via franz-go), and a documented "at your own
  BSL discretion" path to Redpanda if they want it.

**Harder / boundary to respect (the honest limit):**

- This clarification is valid **only while no Redpanda-specific client, agent, or SDK is
  vendored** into the runtime. If aisotropy ever added a build/runtime dependency on a
  Redpanda-licensed component (as opposed to the license-agnostic Kafka wire protocol via
  franz-go), **that** would trigger the license gate and require an explicit,
  operator-signed exception per `.claude/rules/cos/license-policy.md` §Exceptions. No such
  dependency exists or is proposed.
- The franz-go / Kafka-API async path itself is a **benchmark module today**
  (`github.com/aisotropy/bench-async`), not wired into the runtime. Any future promotion
  of a Kafka-API broker into the runtime must re-verify, at that time, that it lands on
  franz-go (BSD-3) or another gate-passing client — never on a Redpanda-specific,
  BSL-covered library.
- Redpanda's BSL is time-bounded (rolling conversion to Apache-2.0 per its change date),
  so this position is stable regardless: today it's an operator-discretion server; older
  releases become Apache-2.0 over time. Either way, aisotropy's posture does not depend
  on it.

## Verdict (one line operators can cite)

> **aisotropy carries no Redpanda code or dependency — it speaks the license-agnostic
> Kafka wire protocol via franz-go (BSD-3). Redpanda's BSL is a property of a server an
> operator may choose to run at their own BSL discretion, not an aisotropy dependency; so
> aisotropy's 100%-permissive posture is unaffected and the gate-clean async default
> remains NATS JetStream (Apache-2.0), with Apache Kafka the permissive Kafka-API
> option.**
