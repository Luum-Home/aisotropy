# syntax=docker/dockerfile:1
#
# Multi-stage build for the Genkit-Go arena's live server (cmd/server).
# Packages the REAL Firebase Genkit build (offline fake model) as a production
# Go image: static binary on gcr.io/distroless/static-debian12:nonroot.
#
# Official packaging: https://genkit.dev/go/docs/cloud-run — Genkit's official Go
# deploy path does NOT publish a Dockerfile; it ships to Cloud Run via buildpacks
# (`gcloud run deploy --port 3400`, server binding 0.0.0.0:$PORT) and sets prod
# via GENKIT_ENV. This image is the explicit-Dockerfile MINIMAL VARIANT of that
# buildpack path: the same Go-on-Cloud-Run container shape (static binary, prod
# env, single listen port) made hermetic and offline for the smoke — GENKIT_ENV=prod
# matches the documented production setting (no dev Reflection server); the arena
# fixes the listen port at 4116 instead of reading $PORT/3400.
#
# The dotprompt files are compiled into the binary via go:embed
# (orbit/prompts/*.prompt), so the runtime image needs nothing but the binary.
#
# Build context is the arena root (arenas/genkit-go), not this docker/ dir:
#   docker build -f docker/Dockerfile -t genkit-go-arena:smoke .
#
# ---- build stage -----------------------------------------------------------
# go.mod pins `go 1.25.0` (genkit v1.10.0 requires it), so the builder is 1.25.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
WORKDIR /src

# Cache module downloads on their own layer.
COPY go.mod go.sum ./
RUN go mod download

# Build the fully static server binary (CGO off -> runs on distroless static).
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime stage ---------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

# prod keeps Genkit from starting its dev-mode Reflection server (port 4116
# would otherwise collide, and dev mode expects a local toolchain).
ENV GENKIT_ENV=prod

# Non-root (uid 65532) can bind 4116 since it is > 1024.
EXPOSE 4116

COPY --from=build /out/server /server

ENTRYPOINT ["/server"]
