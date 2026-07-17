# Containerized Cloudflare Agents arena smoke.
#
# Official runtime & deploy target: a Cloudflare Worker/Agent's REAL deploy target
# is Cloudflare's EDGE via `wrangler deploy` (https://developers.cloudflare.com/workers/wrangler/commands/)
# — a Worker is NOT shipped as a Docker image. The runtime itself is `workerd`
# (https://github.com/cloudflare/workerd), the open-source "JavaScript / Wasm
# server runtime based on the same code that powers Cloudflare Workers"; one of
# its stated use cases is running Workers apps "on your own infrastructure".
# `wrangler dev` runs that SAME workerd locally via Miniflare — the same runtime
# used in production (https://developers.cloudflare.com/workers/development-testing/).
# This image therefore runs workerd-in-Docker as the officially-supported
# SELF-HOST / local-fidelity stand-in for the edge runtime, NOT the production
# deploy path (which needs no container at all).
#
# HONEST FRAMING: Cloudflare Agents are Durable Objects that run on `workerd`
# (Cloudflare's Apache-2.0 Workers runtime), NOT on a generic Linux container
# process. Production durability + global uniqueness + per-object isolation are
# RENTED Cloudflare-platform properties you cannot self-host (README "Sovereignty
# caveat"). What DOES containerize is the *local-fidelity* path: `wrangler dev`
# embeds `workerd` + `miniflare`, giving real Durable Objects with their own
# embedded SQLite inside the container — the same runtime the native `pnpm smoke`
# and `pnpm dev` use, just wrapped in a reproducible image instead of the host.
#
# We must run a FRESH install inside the image: the host node_modules ships the
# darwin workerd binary (`@cloudflare/workerd-darwin-*`); a Linux container needs
# `@cloudflare/workerd-linux-*`, pulled by installing in-image. Debian (glibc)
# base, not Alpine — workerd links glibc and does not run cleanly on musl.
FROM node:22-bookworm-slim

# curl is used by the container HEALTHCHECK to poll the DO-backed endpoint.
RUN apt-get update \
  && apt-get install -y --no-install-recommends curl ca-certificates \
  && rm -rf /var/lib/apt/lists/*

# pnpm via corepack — the arena is a pnpm workspace (pnpm-workspace.yaml).
RUN corepack enable

WORKDIR /arena

# Install deps first (better layer caching). Copy only manifests, then install so
# the Linux-native workerd optional dependency is resolved for this platform.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
# --frozen-lockfile: reproducible, fails loudly if the lockfile drifts.
RUN pnpm install --frozen-lockfile

# Source + config for the Worker/DO and the (vitest) workers-pool smoke.
COPY wrangler.jsonc tsconfig.json vitest.config.ts ./
COPY src ./src
COPY test ./test

# wrangler dev serves the real DO-backed HTTP endpoint from inside the container.
# 0.0.0.0 so the published port is reachable from the host; metrics off + CI=1 to
# keep it non-interactive and offline (no telemetry, no update prompt).
ENV CI=1 \
    WRANGLER_SEND_METRICS=false
EXPOSE 8787

# Liveness: the /healthz route only answers once workerd has booted the Worker.
HEALTHCHECK --interval=3s --timeout=3s --start-period=5s --retries=20 \
  CMD curl -fsS http://127.0.0.1:8787/healthz || exit 1

# Default: boot wrangler dev (local mode is the v4 default; embeds workerd).
CMD ["pnpm", "exec", "wrangler", "dev", "--ip", "0.0.0.0", "--port", "8787"]
