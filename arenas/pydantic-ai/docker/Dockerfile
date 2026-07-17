# Minimal containerized image for the Pydantic AI arena orbit app.
#
# Upstream (official) packaging: https://ai.pydantic.dev/  — this image IS idiomatic: Pydantic
#   AI ships NO official Docker image and NO official Dockerfile. It is a plain
#   Python library ("bring the FastAPI feeling to GenAI"), so the official
#   deployment story is standard Python packaging — install the wheel, run your
#   ASGI app — with optional durable-execution backends (Temporal / DBOS / Prefect)
#   for long-running workflows. A single-process FastAPI/uvicorn container like
#   this one is exactly that idiomatic pattern, run keyless (TestModel stub) so the
#   isolation smoke needs no provider key. See docker/README.md.
#
# Serves orbit.app (FastAPI/uvicorn on :4110) with the KEYLESS stub model
# (pydantic_ai.models.test.TestModel) — no ANTHROPIC_API_KEY / OPENAI_API_KEY
# is read or needed. The point is deployment realism: prove the framework's
# minimal app boots and serves the same HTTP surface inside a container.
#
# Build context is the ARENA ROOT (arenas/pydantic-ai), so:
#   docker build -f docker/Dockerfile -t pydantic-ai-arena:smoke .
#
# Runtime deps are pinned to match the arena's requirements.txt, minus logfire
# (heavy OTel tracer, NOT imported by orbit/app.py — omitted to keep the image
# minimal and the build fast). Faithful to the pinned pydantic-ai>=2.12.0.

FROM python:3.13-slim

# No .pyc, unbuffered stdout so container logs stream to `docker logs`.
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PORT=4110

WORKDIR /app

# Dependency layer first for build-cache reuse across code edits.
RUN pip install --no-cache-dir \
        "pydantic-ai>=2.12.0" \
        "fastapi>=0.115.0" \
        "uvicorn>=0.34.0" \
        "pydantic>=2.0.0"

# App code (only the package the server needs).
COPY orbit/ ./orbit/

# Non-root runtime.
RUN useradd --create-home --uid 10001 appuser
USER appuser

EXPOSE 4110

# Container-local healthcheck against the liveness route.
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=5 \
    CMD python -c "import urllib.request,sys; sys.exit(0) if urllib.request.urlopen('http://127.0.0.1:4110/healthz', timeout=2).status==200 else sys.exit(1)"

# Serve. Bind 0.0.0.0 so the port is reachable from the host publish mapping.
CMD ["python", "-m", "uvicorn", "orbit.app:app", "--host", "0.0.0.0", "--port", "4110"]
