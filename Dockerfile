# syntax=docker/dockerfile:1

# --- Build stage -------------------------------------------------------
# All of aisotropyd's dependencies (modernc.org/sqlite, dgraph/badger,
# qdrant/go-client over gRPC, huma, charm) are pure Go — no CGO required —
# so CGO_ENABLED=0 produces a fully static binary that runs unmodified on
# distroless/scratch. See docs/deployment/distroless.md for the measured
# size/startup/RAM numbers behind this choice.
# Base images are pinned by immutable @sha256 digest (supply-chain gate: a moving
# tag can be repointed at a malicious layer; a digest cannot). The trailing tag on
# each FROM stays human-readable and lets Dependabot's `docker` ecosystem propose
# digest bumps. Digests are multi-arch manifest-list digests, so they still resolve
# per-platform (amd64/arm64). Refresh with `docker pull <tag> && docker inspect
# --format '{{index .RepoDigests 0}}' <tag>`. See docs/deployment/supply-chain.md.
# golang:1.26.5 == go1.26.5, matches go.mod's `toolchain go1.26.5` — a patched toolchain
# that fixes the reachable stdlib CVEs GO-2026-5037/5039/5856 (govulncheck pre-push gate).
FROM golang:1.26.5@sha256:ae5a2316d12f3e78fd99177dad452e6ad4f240af2d71d57b480c3477f250fec6 AS builder

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
# distroless/static-debian12 pinned by digest (same supply-chain rationale as above).
FROM gcr.io/distroless/static-debian12:nonroot@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b

COPY --from=builder /aisotropyd /aisotropyd

# Apache-2.0 §4(d): a distributed derivative must carry the attribution notices
# of its Apache-2.0 deps (today: wazero's NOTICE) — and, since ADR-0080, of this
# project itself, whose own NOTICE travels with LICENSE.
# See THIRD-PARTY-NOTICES at the repo root for the audited dependency inventory.
COPY --from=builder /src/LICENSE /src/NOTICE /src/THIRD-PARTY-NOTICES /licenses/

EXPOSE 8080

ENTRYPOINT ["/aisotropyd"]
