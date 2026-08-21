---
type: ADR
title: "ADR-0081 — Working memory moves to the networked tier; the quota bucket does not"
description: >-
  The multi-replica question, decided by two measurements nobody had taken. Working memory and its
  transactional outbox move to rqlite: a two-write transaction commits at p50 8.44 ms, which is ~7%
  of a hydration that already costs ~120 ms and sits off the read path anyway. The per-tenant quota
  bucket stays embedded: its local Allow measures 7.7 µs, so the same commit is ~1,100x — paid before
  every model call, including the calls it is about to reject. Two agents had disagreed on exactly
  this coupling; the number settled it. Records what is decided, the two constraints the port must
  honour (AppendWithOutbox atomicity, and a drain that is at-least-once because it has no claim
  step), and what remains open.
tags: [decisions, adr, multi-replica, working-memory, outbox, quota, rqlite, measurement]
timestamp: 2026-08-05T00:00:00Z
---

# ADR-0081 — Working memory moves to the networked tier; the quota bucket does not

## Status

🟡 **Decided; the seam is built, the networked backend is not.** Updated 2026-08-10.

This previously read "zero lines of the port are written", and that is no longer true. What exists
now is precisely the thing this ADR deferred its remaining decisions behind — *"deferred until the
interface exists"*:

- **`memory.WorkingBackend`** ([`internal/memory/backend.go`](../../internal/memory/backend.go)) — the
  tenant-UNAWARE driver interface, shape copied from `facts.DocBackend` rather than invented, with
  `AppendMessageWithEvent` as a **single** method because a decorator cannot make two network
  round-trips atomic and this ADR's first constraint says that atomicity must survive.
- **The sealed decorator** ([`backend_store.go`](../../internal/memory/backend_store.go)) — `namespaceFor`
  is unexported, so a consumer can neither compose a namespace nor hand the driver one it chose.
  `EraseSubject` is a namespace **drop** rather than a scan, which is what stops an erase from
  silently missing a key shape nobody remembered to enumerate.
- **A conformance suite** ([`plugintest.RunWorkingBackendSuite`](../../internal/plugintest/working.go))
  with a reference implementation, sitting alongside `RunDocBackendSuite` exactly as this ADR
  specified. Its isolation subtest asserts the driver never receives a decodable identity.

**What this does NOT mean.** No networked backend implements the interface, and `WorkingMemory` is
not yet built on it, so the shared topology this ADR exists to unblock still does not exist — the
robustness audit's blocker stands, with a narrower reason than before. Two honest limits are recorded
rather than glossed: the suite **cannot** prove crash-atomicity (no black-box suite can kill a process
mid-commit), so `AppendMessageWithEvent`'s guarantee remains the implementer's obligation; and the
suite was itself falsified against four deliberately broken drivers before being trusted, because a
suite that has only ever passed is not evidence that it can fail.

What had already changed before this: the work stopped being an open question of unknown size,
because the two measurements that gate the design exist and disagree with the assumption it had been
sized on.

## Context

Every piece of control state in aisotropyd lives in the process that serves the request. Working
memory is an embedded BadgerDB; the quota token bucket lives in the same store; the cost meter was a
map in RAM until [`1ce0195`](../../). The consequences are documented in the
[threat model](../architecture/threat-model.md)'s gap ledger and the [runbook](../deployment/runbook.md):
N replicas give a tenant roughly N times its rate, a session landing on the wrong replica finds no
history, and — with persistence enabled — the second replica does not start at all, because BadgerDB
takes an exclusive flock.

This had been sized at "months", and that estimate rested on a latency argument: moving working
memory off an in-process store would surrender the µs reads the project's own positioning quotes.
**That argument was wrong, and the document that refutes it was already in the repository.**
[ADR-0076](0076-as-built-deltas-parallel-hydration-shutdown-join.md) measured hydration at ~120 ms,
dominated by facts (~120 ms) and semantic (2 × 60 ms) fanned out in parallel, against working-memory
reads of 1.7 µs (`Summary`) and 20 µs (`Recent`) — three orders of magnitude below the tiers they run
alongside. The stale line lived in an agent-facing rule file that said hydration was still
sequential, and because every agent in this repo loads those rules at startup, the wrong premise
propagated into the estimate. That is the second-order lesson and it is recorded in
[`5c5716b`](../../).

## The measurements

Taken 2026-08-04 against `rqlite/rqlite:8.36.3`, single node, image already local, on a machine at
94% swap. Two hundred iterations per case over the HTTP API in one process, so no per-request spawn
cost is included.

| | p50 | p95 | min |
|---|---|---|---|
| rqlite, one write | 9.46 ms | 57.93 ms | 3.39 ms |
| **rqlite, two writes in ONE transaction** | **8.44 ms** | 28.57 ms | 4.82 ms |

For comparison, measured in-process: `Limiter.Allow` **7.7 µs**, `WorkingMemory.Summary` **1.7 µs**,
`Recent` **20 µs**, `AppendRead` **123 µs**; full hydration **~120 ms**.

**Honest limits, stated because the numbers are load-bearing.** This is a *single node*: real Raft
with followers adds round-trips, so 8.44 ms is a floor and not a forecast. And the machine was
loaded — the p50s are usable, the p95s are contaminated, visible in the one-write case reporting a
p95 *worse* than the two-write case, which is impossible except as noise. Neither caveat changes the
decision: reversing it would need the commit to fall three orders of magnitude for the quota, or rise
one for working memory.

## Decision

**Working memory and its transactional outbox move to the networked tier.** 8.44 ms against a
~120 ms hydration is ~7%, and `AppendWithOutbox` is on the *write* path — it runs after the turn, not
inside the hydration a user waits for. The real cost to a request is smaller than the 7%.

**The per-tenant quota bucket stays embedded, sharded by replica count.** Not because 1,100x is a
large ratio, but because of *where* the gate runs: `Allow` executes before the model call, on every
request, including the ones it is about to reject. Spending 8 ms — p95 29 ms — to avoid a model call
inverts the purpose of a pre-spend gate, and the
[constitutional gate](../architecture/constitutional-gates.md) it implements is specifically "reject
or degrade *before* spending". The fleet honours its configured rate by division instead:
`NewLimiterForReplicas` ([`1ce0195`](../../)) divides rate and burst by the declared replica count.
That is less precise on burst and correct on sustained rate.

Two opus agents reviewing this independently reached opposite recommendations — one proposed rqlite
with optimistic CAS under `Strong` consistency for the quota, the other argued the RMW must not pay
Raft quorum. Both arguments were sound in prose. The measurement is what separated them, and it is
the reason this ADR leads with numbers rather than reasoning.

## The two constraints the port must honour

**1. `AppendWithOutbox` is atomic and must stay atomic.** It writes the message and the outbox event
in one transaction so that "both are durable or neither is, even on a crash mid-commit"
(`internal/memory/outbox.go`). That is the guarantee the whole consolidation pipeline rests on:
lose it and events are dropped or duplicated with no way to tell which. rqlite has transactions, so
this is a port rather than a redesign — but it is the constraint that makes it a port and not a
rename.

**2. The drain is at-least-once, and now provably so.** `drainOutboxOnce` reads every pending event
in one read transaction, then calls the sink and deletes each key afterwards. There is no claim, no
lease, no compare-and-delete. That is sound today only because one worker exists per store. With a
shared store, two drainers read the same pending set — measured with two concurrent drainers in
[`internal/memory/outbox_concurrent_drain_test.go`](../../internal/memory/outbox_concurrent_drain_test.go):
**the same event reached the sink twice, and no event was lost.**

So a networked tier must either add claiming, or lean on the sink's idempotency. `internal/consolidation`
asserts "idempotency of both writes" in a comment and `OutboxEvent.ID` is derived from the message ref
rather than generated — both good signs, and until that test neither was more than a comment. The
practical consequence: **that idempotency is now load-bearing rather than a nicety**, and a change
that weakens it breaks a property the port depends on.

## What is NOT decided

- ~~**The store interface.** A `WorkingStore` that speaks `memory.Key` and never a bare `string` is
  the shape that keeps isolation at L3; one that takes a composed string key demotes it to
  convention.~~

  **Superseded 2026-08-05 — that was backwards, and the repository already had the right answer.**
  `internal/facts/backend.go` and `internal/semantic/backend.go` (ADR-0034, Tier-2 open core) settled
  this shape: the driver is deliberately **tenant-UNAWARE** and takes an **opaque `ns string`**, while
  isolation is supplied by a **sealed decorator** whose namespace derivation (`namespaceFor`) is
  *unexported*. Its own doc comment states the property: "a community backend can never see or choose
  the tenant, so a buggy backend cannot widen a search past its namespace."

  An interface speaking `memory.Key` would hand every backend the tenant identity — strictly worse.
  L3 is preserved *because* the driver receives a string it did not compose and cannot decode, not in
  spite of it. So the open question is not "which type does the signature take" but the narrower
  "which operations does the working tier need", and the shape is copied rather than invented:
  `WorkingBackend` mirrors `DocBackend`, with the sealed decorator living in `internal/memory` and a
  conformance suite alongside `plugintest.RunDocBackendSuite` — which already includes an
  isolation-seal proof, so the seal is tested by an existing harness rather than a new one.

  Recorded as a correction rather than edited away, because the error is instructive: this ADR
  reasoned from first principles about a property the codebase had already solved twice, which is the
  failure [dependency-reuse-first](../architecture/dependency-reuse-first.md) names — applied here to
  our own prior art rather than to a third-party library.

  **Settled 2026-08-10.** The operations the working tier needs are now enumerated in
  `memory.WorkingBackend`, and building it surfaced one thing the analysis above had not: today's
  keys embed the identity in plaintext (`tenant:{t}:user:{u}:session:{s}:chat:{seq}`), so forwarding
  them to a driver would have leaked exactly what the seal exists to hide. The port therefore
  re-derives keys — hashed namespace per (tenant, user), session as a separate parameter inside it —
  rather than passing the existing ones through. That is a migration concern for the wiring step, not
  a free rename.
- **Whether quota and working memory share a backend.** They need not: the decision above already
  puts them on different tiers, and this ADR does not require rqlite to serve both.
- **Multi-node commit cost.** The floor is measured; the production number is not.
- **Whether claiming is added anyway.** Relying on sink idempotency is cheaper and is what the
  evidence supports; adding a claim step is more robust and costs a schema. Deferred until the
  interface exists.

## Consequences

Multi-replica deployment stops being blocked on an unknown and becomes bounded work with two written
constraints. The quota half already shipped. The session-continuity half is mitigated today by
ingress affinity ([`e2d97c0`](../../)), with its own honest limit: nginx's `upstream-hash-by` cannot
isolate the tenant/user path segment, so the real granularity is *(URI, user)* — a conversation
mixing `/chat` and `/chat/stream` lands on two replicas, and the OpenAI-compat route carries no path
identity at all.

What does not change: with working memory shared, `EraseSubject` still erases only the replica that
serves it. That receipt now says so ([`1ce0195`](../../)) rather than overstating its reach, and a
shared tier is what would let it stop being true.
