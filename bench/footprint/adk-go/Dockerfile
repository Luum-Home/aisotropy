# syntax=docker/dockerfile:1
# Same-language control for the footprint benchmark: the REAL Google ADK Go
# framework (google.golang.org/adk/v2) packaged IDENTICALLY to aisotropy —
# multi-stage build, CGO_ENABLED=0 static binary, final stage
# gcr.io/distroless/static-debian12:nonroot, /healthz on :8080. Because the
# packaging is byte-for-byte the same recipe aisotropy uses, any image/RSS gap
# vs aisotropy is a FRAMEWORK difference, not a language one — that is the whole
# reason this subject exists.

# --- Build stage -------------------------------------------------------
FROM golang:1.26 AS builder

WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /adk-go-footprint .

# --- Final stage ---------------------------------------------------------
# distroless/static-debian12:nonroot — the exact same base aisotropy ships on.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /adk-go-footprint /adk-go-footprint

EXPOSE 8080

ENTRYPOINT ["/adk-go-footprint"]
