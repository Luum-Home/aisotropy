# Mastra arena — Docker smoke

Containerized isolation smoke for the Mastra arena. Builds a minimal Node image,
boots it, and asserts tenant isolation over HTTP (tenant A reads its own secret; a
cross-tenant read is refused by Mastra's thread↔resource ownership guard). Run with
`./run.sh` (skips cleanly if Docker is unavailable).

## Relation to Mastra's official packaging

**Official source:** Mastra's own deployment path is documented at
<https://mastra.ai/docs/deployment/building-mastra> and
<https://mastra.ai/docs/deployment/mastra-server>. The canonical build is
`mastra build`, which emits a **self-contained `.mastra/output/index.mjs` Node
server**, started with `node .mastra/output/index.mjs` (or `mastra start`).
Mastra publishes **no official Dockerfile** — the docs describe the built-output
server and leave the container recipe to the operator.

**How this image relates:** it is a deliberate **minimal variant**, not the
`mastra build` output. The arena smoke drives custom HTTP endpoints (`/remember`,
`/recall`, `/chat`, `/healthz`) that the stock generated server does not expose, so
we wrap the **real `@mastra/core` Agent + `@mastra/memory` (LibSQLStore)** in a tiny
`node:http` server (`server.mjs`). The isolation guard under test is Mastra's own —
only the HTTP surface is hand-rolled. See the top-of-`Dockerfile` note for the same
rationale.

- Base: `node:22-slim` (matches `@mastra/core` `engines.node >=22.13.0`).
- Runtime deps pinned in `docker/package.json`; no API key (offline mock model).
