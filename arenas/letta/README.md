# Letta (MemGPT) Arena

> **Framework:** Letta / MemGPT — real dependency: [`letta-client`](https://pypi.org/project/letta-client/) `1.12.1` (Apache 2.0, Python)
> **Positioning:** Closest conceptual neighbor to aisotropy. Memory hierarchy is explicitly OS-inspired, nearly isomorphic to aisotropy's framing.
> **Threat level:** Validates the memory-OS thesis but leaves aisotropy's exact combination (Go, isolation-by-construction, runtime-hydrated deterministic memory) open.

## What is real here vs. what is simulated (read this first)

This arena **actually depends on the real Letta SDK** and builds Orbit against its
**real, current API**. It is not a hand-rolled model of Letta.

| Piece | Status |
|---|---|
| `letta-client==1.12.1` (official Stainless SDK) | **Real dependency** — `pip install`ed, imported, pinned in `requirements.txt` |
| Memory/state types (`BlockResponse`, `AgentState`, `Passage`, response-format schemas) | **Real** — every stored object in the smoke is a genuine SDK Pydantic instance |
| Client resources (`client.agents` / `.blocks` / `.passages` / `.archives` / `.identities`) | **Real** — used on the live path; their filter/param names are asserted against the real API |
| Orbit agent-create payload (`orbit/agent.py:build_agent_payload`) | **Real** — every key is validated to be an actual `agents.create` parameter |
| A running Letta **agent** (LLM step, `messages.create`) | **NOT run here** — needs a live server + DB + provider key (see below) |
| The offline block/passage **data plane** used by the smoke | **Labelled behavioral simulation** — real types, real server *filter contract*, deterministic responder instead of the LLM |

**Why a simulation at all.** Letta OSS is a **server**: agents, memory blocks, and
archival passages are durable server-side objects backed by Postgres/SQLite, and an
agent turn calls out to an LLM provider. `letta-client` is a *client* for that server
(`Letta(base_url=...)`). This environment has **no running server and no LLM key**, and
this arena **does not fake a server connection**. To still prove feature #1 (tenant
isolation) against Letta's *real* state model, `orbit/memory.py` reconstructs only the
block/passage tables and replicates the server's **documented, developer-passed** filter
contract (`identity_id` / `project_id` / `label` / `tags`) using the genuine SDK types.
It is clearly labelled as such in the source.

The heavy OSS server package (`pip install letta`, resolves to `letta-0.16.8`) **does
resolve on Python 3.13** — but it pulls ~200 transitive deps (SQLAlchemy, alembic,
FastAPI, temporalio, onnxruntime, llama-index, provider SDKs) and then still requires a
running `letta server` + database + an LLM provider key to execute an agent. That is out
of scope for an offline, keyless smoke.

## Setup

```bash
# Python 3.13 (3.14 wheels for some transitive deps are still catching up)
python3.13 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt          # letta-client==1.12.1 + pytest
```

## Smoke test (offline, no server, no key)

```bash
python scripts/smoke.py                   # exit 0 = harness healthy + leak demonstrated
```

Runs the adversarial isolation matrix over the **real Letta types**. No LLM key, no
server. If `LETTA_BASE_URL` is set it additionally attempts a **real read-only server
ping** and reports the result honestly (it never fabricates success).

```bash
pytest tests/ -q                          # 7 passed — isolation asserted as executable tests
```

## Live serve (needs a real Letta server)

```bash
# 1. Start a real Letta server (self-hosted OSS): Docker or the CLI from `pip install letta`
docker run -p 8283:8283 letta/letta:latest        # or: letta server
# 2. Point Orbit at it and give the SERVER a provider key
export LETTA_BASE_URL=http://localhost:8283
export OPENAI_API_KEY=sk-...               # consumed by the server, not by this client
python scripts/smoke.py                    # now also pings the live server
```

On the live path, `orbit/agent.py:create_agent` calls the real
`client.agents.create(...)` and `send_message` calls the real
`client.agents.messages.create(...)`.

## Isolation model

**Convention — app-level identities/tags/projects, developer-passed.**

Letta's multi-tenancy is a set of **optional query parameters the developer chooses to
pass** on every read: `identity_id`, `project_id`, `tags`. Nothing binds an authenticated
caller to a tenant. Concretely, from the real API:

```python
client.blocks.list()                       # no filter -> EVERY tenant's block (org-wide scan)
client.blocks.retrieve(block_id)           # raw id -> no tenant check at all
client.passages.search(tags=None)          # no tag scope -> archival across tenants
client.blocks.list(identity_id="wrong")    # wrong identity -> the wrong tenant's blocks
```

The correct read and the leaky read are **one argument apart**. Per-*user* **key**
isolation (a separate API key per principal) exists only in **Letta Cloud** via projects;
OSS runs as a single org. Memory is also **self-managed by the LLM via tool calls** rather
than runtime-hydrated (aisotropy's is deterministic and auditable).

```text
Letta's memory hierarchy (all real SDK types in this arena):
  Core Memory     -> Block   (in-context editable blocks; RAM)      client.blocks / agents.blocks
  Recall Memory   -> messages (out-of-context searchable history)   agents.messages
  Archival Memory -> Passage (unbounded vector store; cold)         client.passages / archives
```

## Features covered

| # | Feature | Status | Notes (real API surface) |
|---|---|---|---|
| 1 | Multi-tenant isolation | **Convention** | `identity_id`/`project_id`/`tags` developer-passed; wrong/absent scope = cross-read. Proven in `scripts/smoke.py` + `tests/`. |
| 2 | Structured output + retry | Built-in (no fail-closed loop) | `response_format` = `json_schema` on `agents.create`; validation is provider-side, no aisotropy-style reflection retry |
| 3 | Tool calling | Built-in | LLM self-manages memory via base tools (`core_memory_append`, `archival_memory_*`) |
| 4 | Streaming | Built-in | `agents.messages.create(..., stream=True)` / `.stream()` |
| 5 | Working memory | Built-in | Core Memory `Block`s (in-context, editable) |
| 6 | Semantic memory | Built-in | Archival Memory `Passage`s (vector store) |
| 7 | Facts memory (journal) | Partial | Recall Memory (searchable message history), not a dedicated FTS journal |
| 8 | Cost governance | DIY | Not built-in |
| 9 | Observability | Partial/DIY | Server ships OTel exporters; no per-tenant `/costs` surface |
| 10 | Sovereignty / licensing | Apache 2.0 | Self-hostable (Docker/Postgres) |
| 11 | Language | Python | Server-side durable objects, not in-process |

Full per-feature comparison in [`FEATURES.md`](./FEATURES.md).

## What actually executed (closed loop)

Offline, on Python 3.13, no server, no LLM key:

- `python -m compileall orbit scripts tests` -> OK
- `import orbit` (+ all submodules) -> OK
- `build_agent_payload` keys all validated against the real `AgentCreateParams` -> OK
- `python scripts/smoke.py` -> **exit 0** (positive control held; leak demonstrated on 3 real primitives)
- `pytest tests/ -q` -> **7 passed**

**Not executed** (needs a live server + DB + provider key): a real `agents.create` /
`messages.create` agent turn. The live code path exists in `orbit/agent.py`/`orbit/client.py`
and is exercised only when `LETTA_BASE_URL` points at a real server.

## Adversarial isolation result

**LEAK CONFIRMED — convention-based, on the real state model.** Letta's multi-tenancy is
app-level via developer-passed `identity_id`/`project_id`/`tags`. There is no
framework-level enforcement: unscoped `blocks.list()`, raw `blocks.retrieve(id)`, and
unscoped `passages.search()` each return another tenant's data. See `scripts/smoke.py`.
