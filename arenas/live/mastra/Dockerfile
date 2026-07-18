# Live arena adapter: Mastra -- containerized runtime.
#
# The image bakes in NO credentials. OPENAI_API_KEY (and every other secret) is
# supplied ONLY at `docker run` time via --env / --env-file, exactly like the
# native `run.sh` path sources them from the repo-root .env at process start.
# `test-env.sh` asserts this: a built image must contain no key literal in any
# layer, and `docker run --rm <img> env` must show zero credential values baked in.

FROM node:22-slim

WORKDIR /app

# Install dependencies first so the layer is cached across code-only rebuilds.
# (node:22-slim rather than alpine: Mastra's libsql/native transitive deps ship
# glibc prebuilt binaries; the musl base can miss a matching prebuild.)
COPY package.json package-lock.json ./
RUN npm ci --omit=dev

# Adapter source -- deliberately narrow COPY (no .env, no node_modules from the
# host, no test/smoke artifacts) so nothing host-local or secret leaks into a layer.
COPY index.mjs ./
COPY lib ./lib

# No ENV lines for OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL / EMBED_URL /
# QDRANT_URL / QDRANT_COLLECTION / RQLITE_URL / AISOTROPY_MASTRA_LIBSQL here on
# purpose -- every one of them is a runtime `docker run -e` value, never an
# image-build-time default.

ENTRYPOINT ["node", "index.mjs"]
