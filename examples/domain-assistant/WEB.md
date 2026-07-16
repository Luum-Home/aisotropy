# The dev UI (`WithDevUI` → `GET /ui`)

This example calls `aisotropy.WithDevUI()`, which mounts a **single, self-
contained** browser chat page at `GET /ui`. It exists so you can exercise the
whole streaming pipeline by hand — type a message, watch the response stream —
without hand-rolling a client.

## Open it

With the server running (`go run .`), browse to:

```
http://localhost:8080/ui
```

The page ships with default fields already matching this example:

| Field | Default | Why it matches |
|---|---|---|
| `tenant` | `acme` | the tenant this example seeds the FAQ for |
| `user` | `dev` | any id; identity is trusted verbatim under `NoAuth` |
| `product_id` | `dev-ui` | one of the ids the Nimbus persona is registered under |
| `session_id` | `dev-session` | groups turns into one working-memory session |
| bearer token | *(empty)* | not needed — this example runs `WithAuthenticator(NoAuth())` |

So you can just start chatting: ask "How do sync conflicts work?" or "What's the
free plan storage limit?" and the seeded FAQ is hydrated into the system prompt.

## What it is (and is not)

- It is **embedded** at build time (`//go:embed`), inline CSS + vanilla JS,
  **zero external requests** — no CDN, no framework, no fonts. The binary carries
  no external asset dependency.
- It POSTs to the app's **own** streaming endpoint
  (`/v1/tenant/{t}/user/{u}/chat/stream`) and parses the named-event SSE wire
  (`token` / `done` / `error`) with `fetch` + `ReadableStream`.
- It is mounted **outside** the `/v1` auth middleware (like `/healthz`), so it is
  **UNAUTHENTICATED**: anyone who can reach the process can open it.

## Do not ship it to production

`WithDevUI` is a **development convenience**. On boot the runtime logs a loud
warning that the page is unauthenticated and not for production. For a real
frontend, build your own against the AG-UI (`/chat/agui`) or Vercel-AI-SDK
(`/chat/aisdk`) protocol adapters, or the native SSE endpoint, behind your own
authenticated surface — and run the server under a real authenticator instead of
`NoAuth()`.

## Guardrail visibility in the UI

The dev UI uses the **streaming** endpoint. The `grounded-support` guardrail in
this example still runs on the streamed response and logs its verdict to the
server's stderr, but the visible **refusal substitution** only happens on the
**non-streaming** `App.Turn` path with `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT=true`
(see [`../guardrail-policy`](../guardrail-policy)). To watch the check fire while
using the UI, tail the server log for `[grounded-support]` lines.
