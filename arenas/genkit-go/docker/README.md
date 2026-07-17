# Genkit-Go arena — containerized isolation smoke

`Dockerfile` + `run.sh` package the arena's live server (`cmd/server`, `:4116`,
offline fake model) as a production-shaped Go image and drive the tenant-isolation
assertion against the **containerized** server over HTTP. `run.sh` skips cleanly
(exit 0) when Docker is unavailable — it never fakes a pass.

## Relation to the official Genkit packaging

**Official source:** <https://genkit.dev/go/docs/cloud-run> (also
<https://genkit.dev/go/docs/deploy>) — Genkit's official Go deploy path does **not**
publish a Dockerfile. It ships to Cloud Run via buildpacks
(`gcloud run deploy --port 3400`, with the server binding `0.0.0.0:$PORT`) and marks
production with `GENKIT_ENV=prod` (which disables the dev-mode Reflection server).

**How this image relates: MINIMAL VARIANT (explicit-Dockerfile equivalent).** It is
the same Go-on-Cloud-Run container shape a buildpack produces — a static Go binary,
production env, single listen port — made explicit, hermetic, and offline for the
smoke. `GENKIT_ENV=prod` matches the documented production setting; the arena fixes
the listen port at `4116` rather than reading `$PORT`/`3400`, and lands the binary on
`gcr.io/distroless/static-debian12:nonroot`. No behavior is leaner than the official
path — it just makes the container build reproducible instead of buildpack-implicit.

## Run it

```bash
bash arenas/genkit-go/docker/run.sh
```

Builds `genkit-go-arena:smoke`, runs the container, waits for `/healthz`, then proves
`acme/alice` replays its own secret while a different tenant reusing the same
`session_id` does not — the server's `tenant:user:session` namespacing, re-proven
through Docker + HTTP.
