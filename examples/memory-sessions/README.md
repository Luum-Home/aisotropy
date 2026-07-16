# memory-sessions — sessions & working memory

A runnable lesson on what a **session** is in Aisotropy, and what actually
**persists between two turns** of the same conversation.

This is a separate Go module (it `replace`s the repo root) that builds on
Aisotropy's **public surface only** — `aisotropy.New()`, `app.Turn(...)`,
`aisotropy.WithLLM(...)`, and `aisotropy.WithPersona(...)`. It cannot import
`internal/…`, exactly like a third-party consumer.

## What a session is

A session is identified by the `(TenantID, UserID, SessionID)` triple you pass
to `App.Turn`. Reuse that triple across calls and the runtime keeps a **working
memory** buffer for it: each turn's user message and the assistant's reply are
appended, and on the **next** turn they are hydrated back into the system prompt
the model is shown, under *"Most recent messages in this session"*. That is how
the agent "remembers" what you said a moment ago without you resending it.

Change the `SessionID` and it is a **different conversation** with its own empty
buffer — nothing carries over.

## How the demo makes persistence visible

The public API hands you a reply, not the internal system prompt. So to *show*
persistence, the demo installs a tiny **recorder** LLM client via the public
`WithLLM` seam. It is a legitimate consumer-supplied client (`WithLLM` is public,
`LLMClient` is public): it captures the exact system prompt the runtime hydrates
for each turn and returns a canned, schema-valid reply. Then the program prints
the two turns' system prompts side by side.

### Honest note on the model

The recorder is **not a real model** — it does not reason; it only lets us read
back the hydrated context. This is the same role the built-in keyless stub plays,
but one whose *input* we can inspect. Point Aisotropy at a real provider (a real
`WithLLM` client, or set `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` /
`OPENROUTER_API_KEY` and drop `WithLLM`) and the **same** memory hydration feeds a
model that actually uses the remembered context to answer.

## Run it

Keyless — no API key, no Docker:

```
go run .
```

## What you'll see

Turn 1's user message ("Remember that my favorite color is teal.") is **absent**
from turn 1's own system prompt — nothing had been persisted yet — but **present**
in turn 2's system prompt, hydrated back from working memory:

```
================ TURN 2 ================
user prompt : What is my favorite color?
--- system prompt the model was shown on turn 2 ---
  You are NotesBot. Keep track of what the user tells you in this session.

  Most recent messages in this session:
  user: Remember that my favorite color is teal.
  assistant: Got it — noted for this session.

================ WHAT PERSISTED ================
turn-1 message present in turn-1 system prompt : false (nothing persisted yet)
turn-1 message present in turn-2 system prompt : true (hydrated from working memory)

PASS: the session remembered turn 1 when serving turn 2.
```

## Try it yourself

- **Break the session:** change the `SessionID` on turn 2 and re-run. The recall
  disappears — a different session id is a different conversation.
- **Cross-tenant isolation:** a different `TenantID`/`UserID` also gets its own
  isolated memory. That is the subject of the sibling
  [`../multi-tenant`](../multi-tenant) example (lesson 03).
