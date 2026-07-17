# LangGraph live-arena adapter — containerized runtime.
#
# The image never bakes in OPENAI_API_KEY (or any credential): there is no
# ARG/ENV for it anywhere below, no COPY of the repo-root .env, and no
# credential is written into any layer. The key is injected exclusively at
# `docker run` time via `-e OPENAI_API_KEY` (see run-docker.sh / test-env.sh
# assertion (b), which greps the built image and its layer history for the
# literal key value and expects zero hits).
FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY adapter.py .
COPY modes ./modes

# Real-infra host reachability: from inside the container, "localhost"
# means the container itself. host.docker.internal resolves to the Docker
# host, where the real embed server (127.0.0.1:8899) and real Qdrant
# (localhost:6333) actually listen. Overridable at `docker run` time via
# `-e EMBED_URL=... -e QDRANT_URL=...` for non-Docker-Desktop hosts.
ENV EMBED_URL=http://host.docker.internal:8899
ENV QDRANT_URL=http://host.docker.internal:6333
# memory mode's checkpoint db lives inside the container's filesystem by
# default (ephemeral unless a volume is mounted at /data) — cross-container
# persistence needs `docker run -v <host-dir>:/data`.
ENV CHECKPOINT_DB_PATH=/data/checkpoints.sqlite
RUN mkdir -p /data

ENTRYPOINT ["python", "adapter.py"]
