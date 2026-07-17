# Containerized build+boot of the Orbit-on-Flue arena, for the DOCKER smoke.
#
# Official packaging: https://flueframework.com/docs/ecosystem/deploy/docker/
#   Flue DOES ship an official container recipe: base `node:22-slim`, build with
#   `flue build --target node` -> `dist/`, then `CMD ["node", "dist/server.mjs"]`
#   (Hono server; PORT-configurable). This image tracks that official pattern —
#   same base family (node:22-*-slim), same `flue build --target node` step, same
#   `node dist/server.mjs` entrypoint. Deltas from the doc's template, all
#   arena-specific: (1) pnpm (this arena's locked package manager — pnpm-lock.yaml)
#   instead of npm; (2) an in-container OFFLINE fake-model sidecar so the smoke
#   needs no API key and no network; (3) single-stage (Flue externalizes deps, so
#   node_modules is required at runtime either way — see the doc's own note).
#
# Same app the native smokes drive (scripts/smoke.mjs, scripts/isolation.mjs),
# but built and run entirely inside a plain Node container — proving Flue's
# `--target node` output is a self-contained `node dist/server.mjs` process with
# no native addons. Flue's file-backed durable store uses Node's *built-in*
# `node:sqlite` (DatabaseSync), so no compiler / better-sqlite3 / .node binary is
# needed: a stock `node:22` image is enough.
#
# The container is self-contained and offline: it runs BOTH the deterministic
# fake OpenAI-wire model (scripts/fake-model.mjs) and the Flue server, so the
# only thing crossing the boundary is the Flue HTTP port. No API key, no network
# at runtime.
#
# Build context is the arena root (arenas/flue). Build with docker/run.sh.
# syntax=docker/dockerfile:1

FROM node:22-bookworm-slim

# pnpm via corepack, pinned to the version the arena locked against.
ENV PNPM_HOME=/pnpm
ENV PATH=$PNPM_HOME:$PATH
ENV CI=true
RUN corepack enable && corepack prepare pnpm@11.5.2 --activate

WORKDIR /app

# 1) Dependencies first (cache-friendly layer). A fresh in-container install
#    resolves the LINUX platform binaries for esbuild/rollup that Flue's Vite
#    build needs — this is why we do NOT copy the host's node_modules.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
# pnpm 11 treats ignored postinstall build scripts (sharp/workerd/genai/… — none
# of which the node-target build actually needs) as a hard ERR_PNPM_IGNORED_BUILDS
# and exits non-zero, even though node_modules is fully populated. Tolerate that
# non-fatal notice, then hard-assert the Flue CLI really landed — so a genuinely
# broken install still fails the build. esbuild/rollup platform binaries arrive as
# regular optional deps (not build scripts), so the Vite build works without them.
RUN pnpm install --frozen-lockfile || true
RUN test -x node_modules/.bin/flue

# 2) App sources + the fake model the container runs alongside the server.
COPY flue.config.ts ./
COPY src ./src
COPY scripts/fake-model.mjs ./scripts/fake-model.mjs

# 3) flue build --target node  ->  dist/server.mjs (+ assets).
#    Invoke the Flue CLI binary directly, NOT via `pnpm run`: `pnpm run` re-runs a
#    deps-status check that would re-raise ERR_PNPM_IGNORED_BUILDS and abort.
RUN node_modules/.bin/flue build --target node && test -f dist/server.mjs

# Runtime wiring. The Flue server (app.ts) defaults ORBIT_FAKE_MODEL_URL to
# 127.0.0.1:4117 — the in-container fake model — so this is belt-and-suspenders.
# ORBIT_DB_PATH points the durable store at a real file inside the container.
ENV PORT=8080 \
    ORBIT_FAKE_MODEL_URL=http://127.0.0.1:4117/v1 \
    ORBIT_DB_PATH=/app/data/orbit.db
RUN mkdir -p /app/data

EXPOSE 8080

# Start the offline fake model in the background, then exec the Flue server as
# PID 1's foreground child. The fake model reads $PORT first, so pin it to 4117
# explicitly (otherwise it would grab the server's $PORT=8080 and collide). If the
# model dies the server stays up (smoke still gets deterministic replies — never a
# fake pass).
CMD ["sh", "-c", "PORT=4117 node scripts/fake-model.mjs 4117 & exec node dist/server.mjs"]
