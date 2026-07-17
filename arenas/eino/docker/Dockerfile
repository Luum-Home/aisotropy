# Eino arena — CONTAINERIZED smoke image.
#
# Packages the arena's live Orbit server (cmd/server, port 4113) the way a
# production Go service is shipped: multi-stage build -> a static, CGO-free
# binary -> distroless/static-debian12:nonroot (no shell, no libc, non-root UID).
#
# The image runs the REAL CloudWeGo Eino runtime with the offline StubChatModel,
# so it boots with no API key. Build context is the arena module root
# (arenas/eino), NOT this docker/ dir:
#
#   docker build -f docker/Dockerfile -t eino-arena-smoke:local .
#
# (arenas/eino/docker/run.sh does exactly that, then drives the isolation smoke
# against the containerized server over HTTP.)

# ---- Stage 1: build a static binary ----
FROM golang:1.25-bookworm AS build
WORKDIR /src

# Prime the module cache off the manifests first so edits to source don't bust it.
COPY go.mod go.sum ./
RUN go mod download

# Bring in the rest of the arena and compile cmd/server.
COPY . .
# CGO off + stripped => a fully static ELF that runs on distroless/static.
# -trimpath keeps the binary reproducible (no host paths baked in).
RUN CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=mod \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Fail the build early if the binary is not actually static.
RUN ! ldd /out/server 2>/dev/null | grep -q '=>' || (echo "binary is dynamically linked" && exit 1)

# ---- Stage 2: distroless runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server

# Real Eino runtime + stub model: no API key needed to boot.
ENV ORBIT_ADDR=:4113
EXPOSE 4113

# nonroot UID (65532) is provided by the base image; no shell exists in it.
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
