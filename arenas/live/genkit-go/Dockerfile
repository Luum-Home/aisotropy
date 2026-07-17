# syntax=docker/dockerfile:1
#
# Multi-stage build for the exhaustive genkit-go live-arena adapter. Same
# static-binary-on-distroless shape as the repo root Dockerfile and the
# sibling arenas/genkit-go/docker/Dockerfile — CGO_ENABLED=0 because every
# dependency here (Genkit, the OpenAI-compatible client, qdrant/go-client's
# gRPC transport) is pure Go.
#
# Build context is THIS directory (arenas/live/genkit-go), not the repo root:
#   docker build -t genkit-go-live-arena:exhaustive arenas/live/genkit-go
#
# The image never bakes in OPENAI_API_KEY or any other credential — every
# secret and every infra endpoint (OPENAI_*, EMBED_URL, QDRANT_URL,
# RQLITE_URL) is a RUNTIME env var supplied by run-docker.sh / `docker run -e`.
# See test-env.sh for the automated proof that the built image contains no
# key literal.

# ---- build stage ------------------------------------------------------
FROM golang:1.25-bookworm AS build
WORKDIR /src

# Cache module downloads on their own layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/genkit-go-live-arena .

# ---- runtime stage ------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/genkit-go-live-arena /genkit-go-live-arena

ENTRYPOINT ["/genkit-go-live-arena"]
