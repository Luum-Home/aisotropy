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
pytest tests/ -q                          # 21 passed — isolation + strengths as executable tests
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

## What Letta does genuinely well

The isolation smokes below are adversarial by design. In fairness, Letta is a
**memory-OS product**, and it is genuinely strong at things aisotropy does not
productize the same way. Each strength is grounded in the pinned
`letta-client==1.12.1` surface; run-verified where the offline/no-key path allows.

```bash
python scripts/strengths_demo.py          # exit 0 — self-editing memory run-verified
```

**1. Self-editing core memory (MemGPT heritage) — RUN-VERIFIED.**
An agent rewrites its **own** working memory in place. Real surface:
`client.blocks.update(block_id, value=...)` and the agent-scoped
`client.agents.blocks.update(block_label, agent_id, value=...)`; with
`include_base_tools=True` the LLM drives exactly this via the `core_memory_append`
/ `core_memory_replace` base tools. `scripts/strengths_demo.py` + `tests/test_strengths.py`
show a real `BlockResponse` being rewritten across turns with the edit persisting
as durable state. aisotropy has tiered memory, but memory there is
**runtime-hydrated and deterministic** — it does **not** ship an LLM that edits its
own core-memory blocks. Different philosophy, and Letta's is a real product feature.

**2. Sleep-time memory consolidation — DESCRIBED (server-backed).**
`client.agents.create(enable_sleeptime=True)` — the param's own docstring: *"If set
to True, memory management will move to a background agent thread."* A paired
background agent consolidates memory during idle time (related in-band lever:
`client.agents.messages.compact(...)`, *"Summarize an agent's conversation history."*).
The lever is SDK-verified offline; execution needs a live server + provider key, so
it is labelled **NOT-RUN**, not faked. This is the direct analog of aisotropy's
DataFusion sleep cycle — except Letta ships it as a product API today, while
aisotropy's is dormant behind `AISOTROPY_SLEEPCYCLE_BIN`.

**3. Stateful agent-as-a-service (durable `AgentState`) — DESCRIBED (server-backed).**
A Letta agent is a durable server-side object: the real `AgentState` carries `id`,
`memory` / `blocks`, `tools`, `sources`, `enable_sleeptime`, `identity_ids`, and its
message history persists (`client.agents.messages.list(agent_id)`). The agent
survives process/server restarts because state lives in the server's DB — the MemGPT
"agent-as-a-service" heritage. The state **shape** is shown offline against the real
type; cross-restart durability needs the real server+DB (**NOT-RUN**). aisotropy is
a stateless-per-request runtime that hydrates memory from its tiers each call — a
deliberately different model, and Letta's persistent-agent product is a genuine
strength for long-lived assistants.

**The honest two-sided read:** Letta is genuinely great at *stateful, self-editing,
self-consolidating memory* (its memory-OS home turf). aisotropy differs on a narrow
but load-bearing axis — **isolation-by-default and deterministic memory** — and, as
the next section shows, carries a single-org caveat Letta developers must design
around at the app layer.

## Isolation model

**Single-org. Isolate per-agent-by-convention, then map caller → agent_id at the app layer.**

Letta OSS is a **single organization**: there is no framework binding between an
authenticated caller and a tenant. The isolation story has **two surfaces**, and the
difference between them is the whole point.

**Org-wide primitives — optional, developer-passed filters (under-scope and you leak):**

```python
client.blocks.list()                       # no filter -> EVERY tenant's block (org-wide scan)
client.blocks.retrieve(block_id)           # raw id -> no ownership check (needs a foreign id)
client.passages.search(tags=None)          # no agent_id/archive_id/tags -> archival across the org
client.blocks.list(identity_id="wrong")    # wrong identity -> the wrong tenant's blocks
```

**Agent-scoped subresources — `agent_id` REQUIRED, isolated by construction:**

```python
client.agents.blocks.list(agent_id)               # only THIS agent's core memory
client.agents.passages.search(agent_id, query=..) # only THIS agent's archives
```

The agent-scoped reads require an `agent_id` (the SDK rejects an empty one; the route is
`/v1/agents/{agent_id}/archival-memory/search`) and cannot span agents. Letta's **documented
multi-tenant pattern** is therefore **one agent per principal**
([docs.letta.com/guides/agents/multi-user](https://docs.letta.com/guides/agents/multi-user)):
isolation is by `agent_id`, and the app maps verified caller → `agent_id`. A competent
developer using this pattern isolates by construction — no tags to forget.

What remains, and what the smoke proves: **the org-wide primitives carry no caller binding**,
so the correct scoped read and a leaky org-wide read are **one argument apart**, and nothing
in the framework stops a developer from reaching for the unscoped primitive. Per-*user*
**key** isolation (a separate API key per principal) exists only in **Letta Cloud** via
projects. (Letta's older per-principal **identities** are being deprecated in favour of
per-agent scoping; in 1.12.1 the SDK exposes only `agents.identities.attach/detach`.) Memory
is also **self-managed by the LLM via tool calls** rather than runtime-hydrated (aisotropy's
is deterministic and auditable).

```text
Letta's memory hierarchy (all real SDK types in this arena):
  Core Memory     -> Block   (in-context editable blocks; RAM)      client.blocks / agents.blocks
  Recall Memory   -> messages (out-of-context searchable history)   agents.messages
  Archival Memory -> Passage (unbounded vector store; cold)         client.passages / archives
```

## Features covered

| # | Feature | Status | Notes (real API surface) |
|---|---|---|---|
| 1 | Multi-tenant isolation | **Convention** (org-wide) / **by agent_id** (agent-scoped) | Org-wide `identity_id`/`project_id`/`tags` developer-passed → wrong/absent scope = cross-read (proven in `scripts/smoke.py` + `tests/`). Agent-scoped `agents.blocks.list`/`agents.passages.search` isolate by `agent_id`; documented pattern = one agent per principal. |
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
- `python scripts/smoke.py` -> **exit 0** (positive control held; tenant/user under-scoping leak demonstrated on 3 org-wide primitives)
- `python scripts/product_isolation_demo.py` -> **exit 0** (agent-scoped reads isolate by construction; org-wide footguns noted)
- `python scripts/strengths_demo.py` -> **exit 0** (self-editing core memory run-verified; sleep-time + durable AgentState SDK-verified, server exec labelled NOT-RUN)
- `pytest tests/ -q` -> **21 passed**

**Not executed** (needs a live server + DB + provider key): a real `agents.create` /
`messages.create` agent turn. The live code path exists in `orbit/agent.py`/`orbit/client.py`
and is exercised only when `LETTA_BASE_URL` points at a real server.

## Adversarial isolation result

**Narrow thesis, honestly bounded.** OSS Letta is a **single org** with no framework-enforced
bind between an authenticated caller and a tenant. On the **org-wide** primitives — unscoped
`blocks.list()`, raw `blocks.retrieve(id)`, unscoped `passages.search()` — under-scoping
returns another tenant's data; the correct read and the leak are one argument apart
(`scripts/smoke.py`, `tests/test_isolation.py`). The **agent-scoped** subresources
(`agents.blocks.list(agent_id)`, `agents.passages.search(agent_id)`) DO isolate by
construction, so a competent developer using Letta's documented one-agent-per-principal
pattern avoids the org-wide footguns (`scripts/product_isolation_demo.py`,
`tests/test_product_isolation.py`) — the residual gap is that binding is single-org and
app-level (map verified caller → `agent_id`), not framework-level.

**vs. aisotropy:** aisotropy binds all four scope axes `(tenant, user, session, product)`
from `context.Context` at the request boundary by default, enforced per-statement — the
unscoped read is *unrepresentable* and there is no org-wide primitive to fall back to.
