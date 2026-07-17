# Flue arena — Docker smoke

Containerized build+boot of the Orbit-on-Flue arena. Builds `flue build --target
node` inside a Node container, boots the compiled `dist/server.mjs`, and drives the
arena's isolation assertion over HTTP. Run with `./run.sh` (skips cleanly if Docker
is unavailable).

## Relation to Flue's official packaging

**Official source:** Flue ships an official container recipe —
<https://flueframework.com/docs/ecosystem/deploy/docker/> (Node target:
<https://flueframework.com/docs/ecosystem/deploy/node/>). The documented pattern is
base **`node:22-slim`**, build with **`flue build --target node`** → **`dist/`**,
then **`CMD ["node", "dist/server.mjs"]`** (Hono server, PORT-configurable).

**How this image relates:** it **tracks the official pattern** — same base family
(`node:22-bookworm-slim`), same `flue build --target node` build step, same
`node dist/server.mjs` entrypoint. Arena-specific deltas from the doc's template:

1. **pnpm** instead of npm — this arena is locked with `pnpm-lock.yaml` /
   `pnpm-workspace.yaml`, so pnpm is the reproducible choice here.
2. An in-container **offline fake-model sidecar** (`scripts/fake-model.mjs`) so the
   smoke needs **no API key and no network** — only the Flue HTTP port crosses the
   boundary.
3. **Single-stage** — Flue externalizes dependencies, so `node_modules` is required
   at runtime regardless (the official doc notes the same), making a second stage
   little gain for this smoke.
