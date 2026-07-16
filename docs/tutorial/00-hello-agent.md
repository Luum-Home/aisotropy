---
type: Tutorial
title: "00 — Hello agent"
description: Run one full chat turn in-process with App.Turn, keyless — the smallest Aisotropy program.
tags: [tutorial, turn, hello-agent, keyless]
timestamp: 2026-07-16T00:00:00Z
---

# 00 — Hello agent

**Goal:** run one full chat turn in-process, with no server, no API key, and no database.

Aisotropy is a **library first**. You build the runtime once with `aisotropy.New()` and call
`app.Turn(ctx, in)` like a function — it runs the same pipeline the HTTP `/chat` route does (identity
→ ctx, quota gate, guardrails, memory hydration, model call), minus the network hop. With no options,
`New()` uses the **keyless stub** model and in-memory stores, so this boots with zero setup.

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

func main() {
	app := aisotropy.New()
	defer func() { _ = app.Close() }()

	out, err := app.Turn(context.Background(), aisotropy.ChatInput{
		TenantID:  "acme",  // isolation identity — enters ctx, scopes every memory tier
		UserID:    "alice",
		SessionID: "s1",     // reuse across calls to keep working memory
		Prompt:    "weather in Rosario?",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out.Reply)
	fmt.Println("stub model:", out.Stub) // true until a real provider key is set
}
```

`TenantID` and `UserID` are the **isolation identity** — they are top-level fields, never part of a
message body, because inside `Turn` they enter `context.Context` and scope every memory read. That's
the [isolation-by-construction](../concepts.md#3-isolation-is-by-construction-not-by-convention) idea
in its smallest form.

**Honesty note.** `out.Stub == true` means no real model ran: the stub returns a canned reply so the
whole pipeline is exercised keyless. Export `ANTHROPIC_API_KEY` (or `OPENAI_API_KEY` /
`OPENROUTER_API_KEY`) and the identical `Turn` call drives a real model — no code change.

**Runnable example:** [`examples/weatherbot`](../../examples/weatherbot) — `cd examples/weatherbot && go run .`
(it serves over HTTP; the in-process `Turn` shape is the snippet above).

**Depth:** [ADR-0031 — embeddable in-process Turn](../decisions/0031-embeddable-in-process-turn.md).

Next: [01 — A typed tool](01-typed-tool.md).
