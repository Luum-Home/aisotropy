# multi-tenant — isolation by construction (lesson 03)

Aisotropy's core differentiator, as a runnable demo: one tenant writes a secret
into its memory, and a **different tenant cannot see it** — not because a filter
happened to catch it, but because every memory tier is scoped from the
`(TenantID, UserID)` identity carried in `ctx`, and **no code path widens that
scope**.

This is a separate Go module (it `replace`s the repo root) that builds on
Aisotropy's **public surface only** — `aisotropy.New()`, `app.Turn(...)`,
`aisotropy.WithLLM(...)`, and `aisotropy.WithPersona(...)`. It cannot import
`internal/…`, exactly like a third-party consumer, so this is the isolation
property proven at the surface a third party actually consumes.

It is the public-API sibling of the internal black-box isolation test
([`aisotropy/isolation_e2e_test.go`](../../aisotropy/isolation_e2e_test.go)),
which drives the same guarantee over real HTTP. Here we drive it in-process via
`App.Turn`.

## How the demo makes the leak (or its absence) visible

The public API hands you a reply, not the internal system prompt — and the
system prompt is exactly where each tenant's remembered context is hydrated. So
the demo installs a tiny **recorder** LLM client via the public `WithLLM` seam
that captures the hydrated system prompt of every turn. The demo runs three
turns:

1. **Write** — tenant A (`acme`/`alice`) stores a distinctive secret.
2. **Positive control** — tenant A asks again; its own secret **must** hydrate
   back, otherwise the isolation check would be vacuous (a scope that retrieves
   nothing "isolates" trivially).
3. **Isolation proof** — tenant B (`globex`/`bob`) asks for "the code that was
   stored", using the retrieval words but **not** the marker itself. If B's
   system prompt ever contained A's marker, that would be a leak.

The recorder is not a real model; the isolation being shown is a property of the
runtime's memory scoping, not of the model — a real provider changes nothing
about it.

## Run it

Keyless — no API key, no Docker:

```
go run .
```

## What you'll see

```
--- tenant A (acme/alice) recall — its own system prompt ---
  You are VaultBot. Remember what each user tells you.

  Most recent messages in this session:
  user: Store this for me: vaultcode-orangutan7431
  assistant: acknowledged

--- tenant B (globex/bob) probe — its own system prompt ---
  You are VaultBot. Remember what each user tells you.

================ ISOLATION RESULT ================
tenant A sees its OWN secret        : true  (positive control — retrieval works)
tenant B sees tenant A's secret     : false (must be false — isolation by construction)

PASS: the same secret that tenant A recalls is invisible to tenant B.
```

Tenant A's secret hydrates into A's own system prompt but is **absent** from
tenant B's — even though B explicitly asked for it. The program exits non-zero
if either the positive control fails or the isolation proof leaks, so a green
run is a real assertion.

## Why this holds

`App.Turn` maps your `(TenantID, UserID, SessionID)` onto the same pipeline the
HTTP `POST /v1/.../chat` route uses. Identity enters `ctx` once, and every
working-memory, facts, and semantic read is scoped from that `ctx` — the
isolation is structural, not a post-retrieval filter that could be forgotten on
a new code path. See [`../memory-sessions`](../memory-sessions) for the
companion lesson on what persists *within* a single session.
