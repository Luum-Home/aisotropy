# Pydantic AI arena — containerized isolation smoke

This directory containerizes the Pydantic AI arena orbit app so the tenant-isolation
assertion can be driven over real HTTP against a containerized process, keyless.

- `Dockerfile` — minimal single-process image (FastAPI + uvicorn on `:4110`,
  keyless `TestModel` stub, non-root, with a container-local `HEALTHCHECK`).
- `run.sh` — builds the image, boots it, waits for `/healthz`, drives the
  path-scoped cross-tenant isolation matrix over HTTP, then removes the container
  (and, by default, the image).

## How this relates to Pydantic AI's official packaging

**Official source:** <https://ai.pydantic.dev/>

Pydantic AI ships **no official Docker image and no official Dockerfile** — it is a
plain Python library built to "bring the FastAPI feeling to GenAI." Its official
deployment story is therefore standard Python packaging (install the wheel, run your
ASGI app), with optional **durable-execution backends** (Temporal / DBOS / Prefect)
for long-running workflows. A single-process FastAPI/uvicorn container like this one
**is** that idiomatic pattern; there is no heavier "official" image to align to.

### Version pin — Pydantic AI v2 (verified)

`requirements.txt` and the Dockerfile pin **`pydantic-ai>=2.12.0`**. On PyPI the
current latest is exactly **`2.12.0`**, so the pin resolves to **v2.12.0** — a v2
release. Verify with:

```bash
curl -fsS https://pypi.org/pypi/pydantic-ai/json | python3 -c "import sys,json;print(json.load(sys.stdin)['info']['version'])"
# -> 2.12.0
```

## Run

```bash
bash arenas/pydantic-ai/docker/run.sh
```

Skips cleanly (exit 0) if Docker is unavailable — it never fakes a pass.
`KEEP_IMAGE=1` keeps the built image; default removes it.
