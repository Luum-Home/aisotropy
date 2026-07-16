---
type: Positioning
title: Why Go — the wedge
description: The sharp, honest case for aisotropy's positioning — Go has agent orchestration libraries but no isolation-first, self-hostable agent runtime with key-brokering; and in Go (unlike Python/TS) the audience's values line up with aisotropy's differentiators. This is the "who is this for" answer.
tags: [vision, positioning, go, wedge, competitive, isolation, key-brokering]
timestamp: 2026-07-16T00:00:00Z
---

# Why Go — the wedge

> This is the positioning argument, not a feature list. For the deep, cited competitor scan it
> rests on, see [the competitive landscape](../research/competitive-landscape.md). For the
> executed, adversarial isolation proofs referenced below, see [the arenas](../../arenas/).

## The honest claim

Not "Go has nothing for agents." That is false and would get called out. The defensible,
verified claim is narrower and stronger:

> **Go has agent orchestration *libraries*, but no isolation-first, self-hostable agent *runtime*
> with key-brokering and multi-tenant isolation by construction.**

There is a real difference between a library you wire up and a runtime with a spine. The spine —
tenant isolation enforced by the type system, sandboxed execution, an API key the agent never
holds — is exactly what nobody in Go ships.

### What exists in Go, and why it isn't that

| In Go | But |
|---|---|
| **Eino** (ByteDance / CloudWeGo) | Serious orchestration (graphs, ADK-style checkpoint/resume), Apache-2.0 — but tied to the CloudWeGo/Volcengine ecosystem, and it is an *orchestration framework*, not an isolation runtime. No tenant-isolation-by-construction, no key-brokering. |
| **Genkit-Go** (Google / Firebase) | Firebase-tethered; multi-tenancy is developer-passed convention. In [the arena](../../arenas/genkit-go) we reproduced a real cross-tenant leak through the stable framework with a wrong session key. No spine. |
| **ADK-Go** (Google) | Newer, orchestration-focused; agent/tool graphs, not an isolation boundary. |
| **LangChainGo** (`tmc/langchaingo`) | An unofficial, community-maintained port — thin, no isolation model. |

Each is real; none is an *isolation-first runtime*. That gap is aisotropy's, and it is narrow
enough to be defensible.

## Why this resolves "who is the buyer?"

The recurring doubt across this project has been *"is there a customer — does this make sense?"*.
The Go framing answers it better than any other language would, because of **audience alignment**:

- In **Python / TypeScript**, you would be selling *isolation* to an audience that mostly wants
  *magic* — fast prototypes, batteries-included DX, a big ecosystem. There aisotropy loses on the
  axis that market rewards (DX, ecosystem, hiring), and its differentiators read as overhead.
- In **Go**, the people building agents are **backend, infrastructure, and platform engineers**.
  They already think in single static binaries, permissive licensing (no copyleft), tenant
  boundaries, resource limits, and production operability. **Aisotropy's edges are their
  defaults.** The differentiator and the audience's values line up — and they line up here in a
  way they do not anywhere else.

So the reframe is: aisotropy is not "another agent framework, worse than Pydantic AI." It is **the
Go agent runtime a platform engineer would choose — because it is the only one that speaks their
language.**

## The job it's hired for

Not "I want a chatbot." That is the Python/TS toy, and those frameworks do it well. The Go dev's
actual, unserved pain is:

> **"I'm building the backend / platform that runs *other people's* agents — per-tenant, per-task,
> sometimes untrusted — embedded in my Go service, at scale."**

That is the job. It needs, all at once: multi-tenant data isolation that cannot leak by
construction; sandboxed execution for untrusted agent work; an API key the running agent never
holds (key-brokering); and it all embeds in a Go service as a library, not a separate Python/TS
process. No Go framework covers that job. Aisotropy is built for exactly it:

- [`App.Turn`](../guides/build-your-own-agent.md) — one in-process conversational turn, called
  like a function.
- `App.RunTask` / `App.RunTasks` — sandboxed, key-brokered task agents, run one or many
  concurrently (see [ADR-0036](../decisions/0036-sandboxed-task-agent-key-brokering.md)).
- Tenant isolation by construction under all of it — the unscoped read is unrepresentable.

## The honest caveat

This is a *positioning* wedge, validated on the technical axis (the spine is real, tested, and
adversarially proven). It is **not yet validated on the market axis** — whether enough Go platform
teams feel this pain acutely enough to adopt is a customer-discovery question, not an engineering
one. The wedge is sharp and defensible; the demand is still to be tested. Do not confuse "the
argument is strong" with "the market is proven."
