# Containerized runtime for the Pydantic AI exhaustive multi-mode live-arena
# adapter. Reads one JSON request on stdin, writes one JSON result on stdout
# (see README.md for the full multi-mode contract) -- this is a CLI tool, not
# a server, so there is no EXPOSE/HEALTHCHECK surface.
#
# SECURITY: no credential is ever baked into this image. OPENAI_API_KEY /
# OPENAI_BASE_URL / OPENAI_MODEL are read from the process environment at
# container *runtime* only (`docker run -e ...`), never as a Dockerfile
# ENV/ARG default and never written into any layer. `test-env.sh` asserts
# this with `docker run --rm <img> env` and a `docker history` grep.
#
# Build (context = this directory):
#   docker build -t pydantic-ai-live-arena:latest arenas/live/pydantic-ai
# Run: see run-docker.sh (sources the repo-root .env for the runtime -e flags
# and rewrites EMBED_URL/QDRANT_URL/RQLITE_URL to host.docker.internal).

FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

# Dependency layer first for build-cache reuse across code edits.
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Adapter code only -- no .env, no venv, no test scripts baked into the image.
COPY adapter.py arena_common.py arena_memory_store.py arena_tools.py ./

# Non-root runtime.
RUN useradd --create-home --uid 10001 appuser && chown -R appuser:appuser /app
USER appuser

ENTRYPOINT ["python", "adapter.py"]
