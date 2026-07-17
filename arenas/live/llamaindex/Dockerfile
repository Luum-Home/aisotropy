# LlamaIndex live-arena adapter — container image.
#
# Runs the exact same adapter.py (STDIN JSON -> STDOUT JSON, mode-dispatched) inside a
# container, reaching:
#   - agnes-2.0-flash over its PUBLIC OpenAI-compatible URL (OPENAI_BASE_URL) — no host
#     networking needed, it's already internet-reachable from inside the container;
#   - the LOCAL e5 embed server and Qdrant, which run on the HOST, via
#     `host.docker.internal` (EMBED_URL / QDRANT_URL overridden at `docker run` time by
#     run-docker.sh — never baked into the image).
#
# The agnes API key is NEVER baked into the image: no ARG/ENV sets it here, no RUN command
# ever sees it (installs are public PyPI only), and it is only ever injected at
# `docker run -e OPENAI_API_KEY` time by run-docker.sh, sourced from the repo-root .env at
# that point. See test-env.sh assertion (b), which greps the built image's layers/history
# and a `docker run --rm <img> env` for the key literal and asserts zero hits.
FROM python:3.13-slim

WORKDIR /app

# Only the direct+pinned requirements — no repo-root .env, no key, ever copied in.
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY adapter.py .

# No CMD env defaults for OPENAI_*/EMBED_URL/QDRANT_URL here on purpose: adapter.py fails
# closed (uniform error envelope, non-zero exit) if OPENAI_* is missing at `docker run` time,
# and EMBED_URL/QDRANT_URL fall back to their loopback defaults (127.0.0.1/localhost) which
# are wrong *inside* a container — run-docker.sh always overrides both explicitly.
ENTRYPOINT ["python3", "adapter.py"]
