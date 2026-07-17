# ADK-Go arena — containerized isolation smoke

`Dockerfile` + `run.sh` package the arena's live server (`cmd/server`, `:4114`,
offline `FakeModel`) as a production-shaped Go image and drive the tenant-isolation
assertion against the **containerized** server over HTTP. `run.sh` skips cleanly
(exit 0) when Docker is unavailable — it never fakes a pass.

## Relation to the official ADK packaging

**Official source:** <https://google.github.io/adk-docs/deploy/cloud-run/> — ADK's
own `adk deploy cloud_run` compiles the Go entry point with `CGO_ENABLED=0` and
generates a multi-stage Dockerfile that lands the static binary on
`gcr.io/distroless/static-debian11`, `EXPOSE 8080` (generator source:
`cmd/adkgo/internal/deploy/cloudrun/cloudrun.go`).

**How this image relates: ALIGNED.** It uses the same idiomatic ADK container
shape — multi-stage build, `CGO_ENABLED=0` static binary, distroless/static
runtime. It differs only in hardening and arena specifics: the runtime is
`gcr.io/distroless/static-debian12:nonroot` (newer patch level, non-root UID
65532) instead of `static-debian11`; the port is the arena's `4114` instead of
`8080`; and the entrypoint is the arena's own `cmd/server` rather than the ADK
launcher. It is not a leaner shortcut — it matches how ADK itself ships a Go agent.

## Run it

```bash
bash arenas/adk-go/docker/run.sh
```

Builds `adk-go-arena:smoke`, runs the container, waits for `/healthz`, then proves
that `acme/alice` recalls its own secret while `globex/alice` (same `session_id`)
does not — the composite-key tenant isolation, re-proven end-to-end through Docker.
