# syntax=docker/dockerfile:1
# Minimal idiomatic Mastra deployment image: official node:22-slim base + a
# production-only npm install of @mastra/core. Smallest STANDARD way to ship a
# Node framework app (slim, prod deps only). Not distroless — the Node runtime
# ships in the image, the same honest language asymmetry vs the Go subjects
# noted in README.md.
FROM node:22-slim

WORKDIR /app

COPY package.json .
RUN npm install --omit=dev --no-audit --no-fund

COPY app.mjs .

EXPOSE 8080
CMD ["node", "app.mjs"]
