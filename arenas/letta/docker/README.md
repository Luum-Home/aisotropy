# Letta arena — containerized OFFLINE isolation smoke

This directory runs the Letta arena's **offline** isolation harness inside a clean
container to prove it is self-contained and reproducible on a bare Python base —
real `letta-client` SDK installed fresh, real Letta Pydantic types imported, the
adversarial isolation matrix executed with **no LLM key and no Letta server**.

- `Dockerfile` — minimal image that installs `requirements.txt`
  (`letta-client==1.12.1`) and runs `scripts/smoke.py` / `tests/test_isolation.py`.
- `run.sh` — builds the image, runs the smoke + pytest inside it with
  `--network=none`, asserts the positive control and the leak finding, then removes
  the image.

## How this relates to Letta's official packaging

**Official source:** <https://docs.letta.com/guides/docker> — image
[`letta/letta`](https://hub.docker.com/r/letta/letta).

Letta's **official** image is a full **server**:

```bash
docker run \
  -v ~/.letta/.persist/pgdata:/var/lib/postgresql/data \
  -p 8283:8283 \
  -e OPENAI_API_KEY="..." \
  letta/letta:latest
```

It bundles Postgres + `pgvector`, exposes the agent REST API on `:8283`, and every
agent turn calls a live LLM provider. Reproducing that requires a running server, a
persistent Postgres volume, and a provider key — the opposite of an offline, keyless
smoke. This **minimal variant** instead installs the real `letta-client` SDK and
exercises the **real Letta Pydantic types** against Letta's documented server filter
contract with a deterministic responder in place of the LLM: a **labelled simulation**
of the server plane, explicitly **not** the live server (which is out of scope
offline and deliberately not faked). See `../README.md` for the "real vs. simulated"
ledger.

## Run

```bash
bash arenas/letta/docker/run.sh
```

Skips cleanly (exit 0) if the Docker daemon is unavailable — it never fakes a pass.
