# Multi-stage build for the ADK Go arena server (cmd/server, :4114).
#
# Official packaging: https://google.github.io/adk-docs/deploy/cloud-run/ — ADK's
# own `adk deploy cloud_run` generates a multi-stage, CGO_ENABLED=0 static Go
# binary onto gcr.io/distroless/static-debian11 (EXPOSE 8080). This image is
# ALIGNED with that generated pattern: same multi-stage + CGO-free static build
# on the distroless/static family. It differs only in hardening/arena specifics
# (distroless static-debian12:nonroot instead of static-debian11; arena port 4114
# instead of 8080; the arena's own cmd/server entrypoint instead of the adk
# launcher). Not a leaner shortcut — the idiomatic ADK container shape.
#
# Proves the REAL google.golang.org/adk/v2 minimal app (real llmagent + runner +
# session.Service, offline FakeModel) runs CONTAINERIZED as a production-shaped
# Go image: static binary on distroless nonroot, no shell, no libc.
#
# NOTE: ADK v2's go.mod declares `go 1.25.0`, so the build stage MUST be Go 1.25+
# (golang:1.25). A 1.24 base fails with "go.mod requires go >= 1.25.0".
#
# Build context is the arena root (arenas/adk-go/), NOT docker/:
#   docker build -f docker/Dockerfile -t adk-go-arena:smoke .
#
# ── Stage 1: build a static, CGO-free linux binary ──────────────────────────
FROM golang:1.25-bookworm AS build
WORKDIR /src

# Cache modules first (go.mod/go.sum change less often than source).
COPY go.mod go.sum ./
RUN go mod download

# Then the source and static build.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ── Stage 2: distroless static, nonroot ─────────────────────────────────────
# static-debian12 has no libc/shell — the CGO-free binary needs neither.
# :nonroot runs as uid 65532; :4114 is unprivileged so no root is required.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/server /server
EXPOSE 4114
USER nonroot:nonroot
ENTRYPOINT ["/server"]
