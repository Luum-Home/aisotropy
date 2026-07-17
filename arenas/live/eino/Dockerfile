# eino-live-arena — multi-stage build.
#
# Stage 1 builds a static, CGO-free binary (every dependency in this module,
# including the pure-Go modernc.org/sqlite driver used by memory mode, is
# CGO-free — see README.md#dependencies-and-licenses). Stage 2 ships just
# that binary on a minimal distroless base (no shell, no package manager —
# smaller attack surface), which still carries CA certificates so the
# outbound HTTPS call to the real agnes endpoint verifies correctly.
#
# The agnes API key is NEVER baked into the image: it is read from the
# environment at container start (see run-docker.sh), exactly like the
# native run.sh path.

FROM golang:1.25-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build -trimpath -ldflags="-s -w" -o /out/eino-live-arena .

# distroless/static-debian12:nonroot runs as UID:GID 65532:65532 with no
# shell, so the memory-mode SQLite data directory must be created and
# chowned here (in the root-privileged build stage) and copied in with
# explicit ownership — WORKDIR alone would create it root-owned and
# unwritable by the nonroot runtime user.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/eino-live-arena /usr/local/bin/eino-live-arena
COPY --from=build --chown=65532:65532 /out/data /data

WORKDIR /data
ENV EINO_ARENA_MEMORY_DB=/data/memory.db

ENTRYPOINT ["/usr/local/bin/eino-live-arena"]
