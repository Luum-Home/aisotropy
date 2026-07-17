# Containerize the Letta arena's OFFLINE smoke (labelled behavioral simulation).
#
# Upstream (official) packaging: https://docs.letta.com/guides/docker  (image: letta/letta,
#   https://hub.docker.com/r/letta/letta) — minimal variant because Letta's
#   official image is a full SERVER: `docker run -v ~/.letta/.persist/pgdata:\
#   /var/lib/postgresql/data -p 8283:8283 -e OPENAI_API_KEY=... letta/letta:latest`
#   bundles Postgres+pgvector and exposes the agent REST API on :8283, and an agent
#   turn calls a live LLM provider. Reproducing that needs a running server, a
#   Postgres volume, and a provider key — the opposite of an OFFLINE, KEYLESS smoke.
#   This variant instead installs the real `letta-client` SDK and exercises the real
#   Letta Pydantic types against the documented server filter contract with a
#   deterministic responder — a labelled simulation of the server plane, NOT the
#   server. See docker/README.md and ../README.md.
#
# WHAT THIS PROVES — and, honestly, what it does NOT:
#   PROVES:  the arena's offline isolation harness runs end-to-end in a CLEAN
#            container — real `letta-client==1.12.1` installed fresh from
#            requirements.txt, the real Letta Pydantic types (BlockResponse /
#            Passage / AgentState) imported, and the adversarial isolation matrix
#            (scripts/smoke.py + tests/test_isolation.py) executed with NO LLM key
#            and NO Letta server. It validates that the arena is self-contained and
#            reproducible on a bare Python 3.13 base, not just on the host venv.
#
#   DOES NOT: run a live Letta SERVER. Letta OSS is a server (agents + memory blocks
#            + archival passages are durable Postgres/SQLite-backed objects, and an
#            agent turn calls an LLM provider). `letta-client` is a *client* for that
#            server. The block/passage data plane exercised in-container is the
#            arena's LABELLED SIMULATION of the server plane (real types + the real,
#            documented server filter contract, deterministic responder instead of
#            the LLM) — see ../README.md "What is real here vs. what is simulated".
#            A live-server containerized smoke additionally needs a running
#            `letta server` + Postgres + a provider key — out of scope for an
#            offline, keyless run and deliberately NOT faked here.
#
# Build context is the ARENA ROOT (arenas/letta/), NOT docker/, so requirements.txt
# and the orbit/ package resolve. Driven by docker/run.sh:
#   docker build -f docker/Dockerfile -t letta-arena:smoke .
#
# Pinned to Python 3.13 to match the arena (3.14 wheels for some deps still lag).
FROM python:3.13-slim

# Fail loudly, no .pyc noise, unbuffered stdout so the smoke's prints stream live.
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /arena

# Install deps first (requirements.txt changes less often than source) — real
# letta-client==1.12.1 + pytest + opentelemetry-sdk, exactly as the host uses.
COPY requirements.txt ./
RUN pip install -r requirements.txt

# Then the arena source. .dockerignore keeps .venv/caches out of the context.
COPY orbit/   ./orbit/
COPY scripts/ ./scripts/
COPY tests/   ./tests/

# Make `orbit` importable regardless of entrypoint cwd.
ENV PYTHONPATH=/arena

# Belt-and-suspenders: this image runs the OFFLINE plane. `orbit.config.is_live`
# keys solely off LETTA_BASE_URL, so an empty value forces the offline memory
# model (no server, no key). run.sh also never sets it.
ENV LETTA_BASE_URL=""

# Default: the offline adversarial isolation smoke (exit 0 = harness healthy +
# leak demonstrated on the org-wide primitives). run.sh overrides CMD to also run
# the pytest isolation assertion inside the same image.
CMD ["python", "scripts/smoke.py"]
