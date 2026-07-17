# Dify arena — CONTAINERIZED image for the OFFLINE, LABELLED SIMULATION.
#
# Upstream (official) packaging: https://github.com/langgenius/dify/blob/main/docker/docker-compose.yaml
#   (guide: https://docs.dify.ai/en/self-host/deploy/quick-start/docker-compose) —
#   minimal variant because Dify's official deployment is a multi-service compose
#   stack (`docker compose up -d` from dify/docker), roughly nine containers: api
#   (Flask), worker (Celery), web, db (Postgres), redis, sandbox, ssrf_proxy, nginx,
#   and a vector store (Weaviate by default). Dify ships NO in-process Python library
#   AND its modified-Apache-2.0 LICENSE §1a forbids multi-tenant operation without a
#   commercial license — so the real platform is deliberately NOT reproduced here. This
#   single small image runs the arena's labelled simulation of Dify's data plane
#   (Dify's real multi-tenant schema in Dify's own ORM, SQLAlchemy 2.0) offline,
#   keyless, network-free. See docker/README.md and ../README.md.
#
# HONEST SCOPE (read this): this image does NOT run real Dify. Dify has no
# in-process/embeddable Python library — it is a Flask + Celery + Postgres +
# Redis multi-container server platform, and its own modified-Apache-2.0 LICENSE
# §1a forbids operating it multi-tenant without a commercial license. So the real
# platform is SOURCE-VERIFIED, NOT EXECUTED (see ../README.md).
#
# What DOES run here: the arena's labelled behavioral SIMULATION of Dify's data
# plane — Dify's real multi-tenant schema (App.tenant_id, Conversation app_id-only,
# Dataset/DocumentSegment.tenant_id) transcribed into Dify's OWN ORM (SQLAlchemy
# 2.0) in orbit/, exercised by tests/ + scripts/smoke.py + scripts/
# product_isolation_demo.py against in-memory SQLite. This proves the arena's
# offline simulation boots and passes in a clean, keyless, network-free container
# — NOT that Dify itself ran.
#
# Build context is the arena root (arenas/dify/), NOT docker/:
#   docker build -f docker/Dockerfile -t dify-arena-sim:smoke .
# (docker/run.sh does exactly that, then asserts the simulation passes in-container.)

FROM python:3.12-slim AS base
WORKDIR /arena

# No .pyc litter, unbuffered stdout so the run.sh capture is live-ordered.
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

# Dify's real ORM (SQLAlchemy 2.0) + pytest + pip-licenses — the arena's deps.
# Cache the layer off requirements.txt so source edits don't bust it.
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

# Only the simulation sources — deliberately NOT the local .venv (host artifact).
COPY orbit/ ./orbit/
COPY scripts/ ./scripts/
COPY tests/ ./tests/

# Drop privileges: the simulation is read-only + in-memory SQLite, needs no root,
# no writable FS, no network, no API key.
RUN useradd -m -u 65532 -s /usr/sbin/nologin nonroot
USER nonroot:nonroot

# Default command = the full offline simulation suite. Any failure (positive
# control broken, pytest red) is a non-zero exit the container surfaces honestly.
# -p no:cacheprovider keeps pytest from writing a cache dir as the nonroot user.
CMD ["bash", "-c", "python -m pytest tests/ -q -p no:cacheprovider && python scripts/smoke.py && python scripts/product_isolation_demo.py"]
