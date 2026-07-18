# aisotropy-live-arena — multi-stage build.
#
# IMPORTANT: this adapter's go.mod carries `replace github.com/Luum-Home/aisotropy
# => ../../..`, so building it needs the aisotropy module source (the façade +
# internal/) from the repo root. The Docker BUILD CONTEXT is therefore the REPO
# ROOT (see run-docker.sh), and this file COPYs only the pieces the build needs —
# never .git, binaries, or unrelated trees.
#
# Stage 1 compiles the WASM tool guest (wasm_tool mode) into calc.wasm, then the
# adapter binary (which embeds calc.wasm via go:embed). Every dependency is pure
# Go (badger, modernc.org/sqlite, qdrant grpc, wazero — all CGO-free), so the
# binary is fully static and ships on distroless.
#
# The agnes API key is NEVER baked into the image: it is read from the environment
# at container start (see run-docker.sh), exactly like the native run.sh path.

FROM golang:1.25-bookworm AS build
WORKDIR /src

# Root aisotropy module source the adapter's replace directive resolves against.
COPY go.mod go.sum ./
COPY aisotropy ./aisotropy
COPY internal ./internal

# The adapter module + the shared RAG corpus (rag mode ingests it).
COPY arenas/live/aisotropy ./arenas/live/aisotropy
COPY arenas/live/_shared/corpus ./arenas/live/_shared/corpus

WORKDIR /src/arenas/live/aisotropy

# 1) WASM tool guest (wasip1 reactor) -> calc.wasm (embedded by the adapter).
RUN GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o calc.wasm ./testdata/wasmguest

# 2) Static adapter binary.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build -trimpath -ldflags="-s -w" -o /out/aisotropy-live-adapter .

# distroless nonroot runs as 65532:65532 with no shell; pre-create the writable
# on-disk data dir (BadgerDB working memory) with that ownership.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aisotropy-live-adapter /usr/local/bin/aisotropy-live-adapter
COPY --from=build /src/arenas/live/_shared/corpus /corpus
COPY --from=build --chown=65532:65532 /out/data /data

ENV AISOTROPY_ARENA_CORPUS_DIR=/corpus \
    AISOTROPY_DATA_DIR=/data

WORKDIR /data
ENTRYPOINT ["/usr/local/bin/aisotropy-live-adapter"]
