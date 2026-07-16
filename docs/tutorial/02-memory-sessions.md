---
type: Tutorial
title: "02 — Memory & sessions"
description: What persists across Turn calls — working memory keyed by SessionID, and the facts/semantic tiers behind it.
tags: [tutorial, memory, sessions, working-memory]
timestamp: 2026-07-16T00:00:00Z
---

# 02 — Memory & sessions

**Goal:** understand what persists between turns, and how to control it.

The runtime is built **once, lazily, on the first `Turn`** and reused across every later call. That's
why memory persists: the working-memory store, the facts store, and the semantic index all live on
the shared runtime, not per-call. The knob you hold is `SessionID`.

```go
app := aisotropy.New()
defer func() { _ = app.Close() }()

id := aisotropy.ChatInput{TenantID: "acme", UserID: "alice", SessionID: "s1"}

id.Prompt = "my favorite city is Rosario"
_, _ = app.Turn(ctx, id)

id.Prompt = "what's the weather in my favorite city?"
out, _ := app.Turn(ctx, id) // same SessionID -> the first turn is in working memory
fmt.Println(out.Reply)
```

Reusing `SessionID: "s1"` is what threads the conversation: the second turn hydrates the first from
**working memory** (the last N messages + a condensed summary buffer) before the model call. Change
the `SessionID` and you start a fresh conversation; keep it and the history follows.

Working memory is only the hot tier. Behind it, as turns accumulate, the runtime consolidates into
the **facts** tier (keyword-searchable rules and preferences) and the **semantic** tier (embeddings
for similarity retrieval) — the [memory-by-temperature](../concepts.md#2-memory-has-a-temperature)
model. You don't wire those here; they hydrate automatically. You *fill* the semantic tier explicitly
in [lesson 05](05-knowledge-rag.md).

**Honesty note.** Under the keyless stub the reply is canned, so you won't see the model *use* the
remembered city — but the hydration still runs (the prior turn is in the prompt the stub receives).
A real provider key makes the recall visible.

**Runnable example:** [`memory-sessions`](../../examples/memory-sessions) — two turns sharing a
`SessionID`, showing the second turn seeing the first.

**Depth:** [tiered memory](../architecture/memory-tiers.md) and
[ADR-0031 — embeddable in-process Turn](../decisions/0031-embeddable-in-process-turn.md) (the shared
lazily-built runtime).

Next: [03 — Multi-tenant isolation](03-multi-tenant.md).
