# syntax=docker/dockerfile:1
#
# Docker smoke for the eve arena — ADDITIVE to the native run in ../README.md.
# Builds the real eve app (npm install + `eve build`) and boots the compiled
# Nitro server (`.output/server/index.mjs`) with the OFFLINE deterministic mock
# model (agent/lib/model.ts) — no API key, no network at runtime. HTTP on :4117.
#
# eve hard-requires Node >= 24 (its CLI refuses older Node); the node:24 base
# satisfies that requirement for BOTH the build (`eve build`) and the runtime
# (`eve/client` when ../scripts/smoke.mjs is driven inside the container).

########################################
# Stage 1 — builder: install deps + compile the agent dir into a Nitro server
########################################
FROM node:24-bookworm-slim AS builder
WORKDIR /app

# Deps first for layer caching (eve@0.24.6, ai, zod — the arena's declared tree).
# `npm install` (not `npm ci`) matches the arena's documented setup, which bumps
# the installed tree to eve@0.24.6.
COPY package.json package-lock.json ./
RUN npm install --no-audit --no-fund

# Authored sources the compiler discovers, then the real rolldown/Nitro build.
# Produces .output/server/index.mjs (~229 kB entry; ~5.7 MB self-contained tree).
COPY tsconfig.json ./
COPY agent ./agent
COPY scripts ./scripts
RUN node node_modules/eve/bin/eve.js build

########################################
# Stage 2 — runtime: boot the compiled server with the mock model
########################################
FROM node:24-bookworm-slim AS runtime
WORKDIR /app

ENV NODE_ENV=production \
    PORT=4117 \
    HOST=0.0.0.0 \
    EVE_HOST=http://127.0.0.1:4117

# The compiled server is self-contained. node_modules + scripts are carried over
# so the arena's OWN scripts/smoke.mjs (typed `eve/client`) can drive this exact
# server from inside the container, on the Node 24 the framework mandates.
COPY --from=builder /app/.output ./.output
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/scripts ./scripts
COPY --from=builder /app/package.json ./package.json

EXPOSE 4117

# Nitro's node-server preset honours PORT/HOST. Offline: mock model, no network.
CMD ["node", ".output/server/index.mjs"]
