# Dify arena — containerized OFFLINE simulation smoke

This directory runs the Dify arena's **offline, labelled simulation** inside a clean
container. It does **not** run real Dify. It builds a small Python image and runs the
arena's simulation of Dify's data plane — Dify's real multi-tenant schema transcribed
into Dify's own ORM (SQLAlchemy 2.0) — over in-memory SQLite, keyless and network-free.

- `Dockerfile` — minimal non-root image that installs `requirements.txt`
  (SQLAlchemy 2.0 + pytest) and runs `tests/`, `scripts/smoke.py`, and
  `scripts/product_isolation_demo.py`.
- `run.sh` — builds the image, runs the simulation suite inside it with
  `--network none`, asserts the pytest/positive-control/leak markers, then removes
  the container and image.

## How this relates to Dify's official packaging

**Official source:**
<https://github.com/langgenius/dify/blob/main/docker/docker-compose.yaml>
(guide: <https://docs.dify.ai/en/self-host/deploy/quick-start/docker-compose>).

Dify's **official** deployment is a **multi-service Docker Compose stack**
(`docker compose up -d` from `dify/docker`), roughly nine containers: `api` (Flask),
`worker` (Celery), `web`, `db` (Postgres), `redis`, `sandbox`, `ssrf_proxy`, `nginx`,
and a vector store (Weaviate by default). We **deliberately do not reproduce it** for
two reasons: Dify ships **no in-process Python library** (the only way to run an agent
is to POST to a live server), and its modified-Apache-2.0 `LICENSE` §1a **forbids
multi-tenant operation without a commercial license**. So real Dify is
source-verified-not-executed; this single small image runs the labelled simulation of
its data plane offline instead. See `../README.md`.

## Run

```bash
bash arenas/dify/docker/run.sh
```

Skips cleanly (exit 0) if Docker is unavailable — it never fakes a pass.
