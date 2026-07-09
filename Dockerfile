# syntax=docker/dockerfile:1

# --- Build stage -------------------------------------------------------
# All of aisotropyd's dependencies (modernc.org/sqlite, dgraph/badger,
# qdrant/go-client over gRPC, huma, charm) are pure Go — no CGO required —
# so CGO_ENABLED=0 produces a fully static binary that runs unmodified on
# distroless/scratch. See docs/deployment/distroless.md for the measured
# size/startup/RAM numbers behind this choice.
FROM golang:1.26 AS builder

WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /aisotropyd ./cmd/aisotropyd

# --- Final stage ---------------------------------------------------------
# distroless/static-debian12:nonroot: no shell, no package manager, no libc
# beyond what's statically linked into the binary itself, runs as a
# non-root UID by default. Safe because the binary above is fully static.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /aisotropyd /aisotropyd

EXPOSE 8080

ENTRYPOINT ["/aisotropyd"]
