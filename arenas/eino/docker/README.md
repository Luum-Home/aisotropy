# Eino arena — containerized isolation smoke

`Dockerfile` + `run.sh` package the arena's live Orbit server (`cmd/server`,
`:4113`, offline `StubChatModel`) as a production-shaped Go image and drive the
tenant-isolation assertion against the **containerized** server over HTTP. `run.sh`
skips cleanly (exit 0) when Docker is unavailable — it never fakes a pass.

## Relation to the official Eino packaging

**Official source:** <https://github.com/cloudwego/eino-examples/tree/main/quickstart/eino_assistant>
(CloudWeGo docs: <https://www.cloudwego.io/docs/eino/>) — CloudWeGo's own reference
Dockerfile is a multi-stage build (`golang:1.22` → `alpine:3.21`) with a
`CGO_ENABLED=0 GOOS=linux` static binary, `EXPOSE 8080`, and the final stage
installing `ca-certificates` + `redis`.

**How this image relates: MINIMAL VARIANT.** It keeps the same multi-stage +
CGO-free static build, but lands on `gcr.io/distroless/static-debian12:nonroot`
and drops `ca-certificates` and `redis`. The reference example needs those because
it calls a real hosted model API (outbound TLS) and a Redis vector store; the arena
runs the offline `StubChatModel` with no outbound calls and no Redis, so a hermetic
distroless static image is both leaner and closer to the smoke's actual dependency
surface. The build pipeline (static Go binary, non-root, single listen port) is the
same idiomatic shape.

## Run it

```bash
bash arenas/eino/docker/run.sh
```

Builds `eino-arena-smoke:local`, runs the container, waits for `/healthz`, then
proves tenant A (`acme`) reads back its own working-memory secret while tenant B
(`globex`, same `session_id`) does not — isolation re-proven through Docker + HTTP.
