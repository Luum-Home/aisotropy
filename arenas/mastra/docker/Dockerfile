# Mastra arena — minimal containerized server (mock model, no API key).
#
# Boots the smallest real Mastra app the arena's isolation smoke can be driven
# against over HTTP: a Mastra Agent on an offline AI-SDK v5 mock model + real
# @mastra/memory (LibSQLStore). See docker/server.mjs and docker/run.sh.
#
# The build context is THIS docker/ directory (self-contained: it declares its own
# pinned deps in docker/package.json so `npm install --omit=dev` is reproducible
# and does not depend on the arena's ad-hoc, undeclared root node_modules).
#
# node:22-slim: @mastra/core@1.51.0 declares engines.node >=22.13.0. Node 20 runs
# it (the smoke passes) but trips EBADENGINE warnings; 22-slim is the supported,
# warning-free engine and matches the arena's host toolchain.
FROM node:22-slim

WORKDIR /app

# Install ONLY runtime deps (no devDependencies). Pinned in docker/package.json:
#   @mastra/core@1.51.0, @mastra/memory@1.23.0, @mastra/libsql@1.16.0
COPY package.json ./
RUN npm install --omit=dev --no-audit --no-fund

# App source (single file, zero web-framework deps — plain node:http).
COPY server.mjs ./

ENV PORT=4111
# Keep memory on-disk inside the container (LibSQLStore file url).
ENV AISOTROPY_DATA_DIR=/app/data
EXPOSE 4111

# No ANTHROPIC_API_KEY / OPENAI_API_KEY: the arena server uses a mock model.
CMD ["node", "server.mjs"]
