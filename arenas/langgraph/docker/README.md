# LangGraph arena — containerized isolation smoke

This directory containerizes the LangGraph arena so the tenant-isolation assertion
can be driven over real HTTP against a containerized process, offline and keyless.

- `Dockerfile` — minimal single-process image (FastAPI + uvicorn on `:4112`,
  backed by LangGraph's real `InMemoryStore`).
- `run.sh` — builds the image, boots it, waits for `/healthz`, drives the
  cross-tenant isolation matrix over HTTP, then removes the container **and** image.

## How this relates to LangGraph's official packaging

**Official source:** <https://docs.langchain.com/langsmith/deploy-standalone-server>
(image built by the `langgraph-cli` via `langgraph build`).

LangGraph's **official** container is a full **Agent Server** produced by
`langgraph build`, and at runtime it **requires two backing services**: Redis
(pub-sub broker for streaming background runs) and Postgres (assistants, threads,
runs, thread state, long-term memory, and the "exactly once" background task
queue). That is a deliberately heavy, production-grade stack.

This smoke deliberately uses a **minimal single-process variant** instead: standing
up Redis + Postgres would defeat the point, which is to prove **containerized tenant
isolation OFFLINE and KEYLESS**. The minimal image serves the same HTTP surface
backed by LangGraph's own real `InMemoryStore`, so what it proves is the enforced
identity-bound namespace posture over the wire — not a substitute for the official
Agent Server, which we do not reproduce here.

## Run

```bash
bash arenas/langgraph/docker/run.sh
```

Skips cleanly (exit 0) if Docker is unavailable — it never fakes a pass. The native
offline smoke (`scripts/smoke.py`) still proves both store-layer postures.
