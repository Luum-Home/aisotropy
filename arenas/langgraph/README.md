# LangGraph Arena

> **Framework:** LangChain + LangGraph (Python)
> **DX rating:** Largest ecosystem and hiring pool, single-tenant fastest (`create_agent` in one line).
> **Pain points:** Churn from repackaging v1 (parsers moved to langchain-classic), Python 3.14 wall,
> production tracing/persistence (LangSmith, Platform) is paid.

## Setup

```bash
# Requires Python 3.13 (3.14 has no wheels yet)
python3.13 -m venv .venv
source .venv/bin/activate
pip install langgraph langchain-openai httpx pydantic

# Optional: for live LLM mode
export OPENAI_API_KEY=sk-...
# or
export OPENROUTER_API_KEY=sk-or-...
```

## Smoke test (offline, stub LLM)

```bash
python smoke.py
```

Runs the isolation matrix on LangGraph's in-memory `Store` — Part A reproduces the naive-config
short-prefix bug, Part B closes it with an identity-bound (`@auth.on.store()`-style) layer. No LLM
API key needed.

## Containerized smoke (Docker)

```bash
bash docker/run.sh
```

Proves the framework's minimal app boots and serves HTTP **inside a container** with the keyless
stub, then drives the isolation assertion over real HTTP against that container: a secret is written
as tenant A, and a cross-tenant read as tenant B must **not** see it. The script builds the image
(`docker/Dockerfile`, Python 3.13 + `langgraph 1.2.9`), runs it, waits for `/healthz`, asserts, and
cleans up the container and image on exit. If Docker is absent it **skips cleanly** (exit 0 with a
message) — it never fakes a pass.

This is **additive** — the offline `scripts/smoke.py` above still runs and still proves both postures
at the store layer. The serve path wires the Part B "correct posture" (identity-bound namespaces via
the memory endpoints in `orbit/server.py`), so what the HTTP smoke proves is the **enforced-correct**
posture: a cross-tenant read is structurally blocked through the endpoint. The Part A naive-config
bug is reproduced by the offline smoke, not exposed over HTTP (the server never offers the
under-scoped query).

## Live serve

```bash
# Starts a FastAPI/uvicorn app at :4112 with the LangGraph server pattern
uvicorn orbit.server:app --port 4112

# Test it:
curl -N -X POST http://localhost:4112/v1/tenant/acme/user/alice/chat/stream \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"hello","session_id":"s1"}'
```

## Isolation model

**LangGraph gives you the tools; you wire them.** Two postures, both real:

**Naive posture — the raw `InMemoryStore` primitive + namespace convention.** LangGraph's
`Store.search(namespace_prefix)` is an **element-wise** prefix scan over the namespace tuple: a
stored namespace matches when the search tuple is a prefix of it. On the bare primitive, prefix
depth is a **developer convention** — a search tuple shallower than full scoping returns every
tenant nested beneath it. This is the same under-scoped-query property any KV store has when you
hit it with no authorization layer above (aisotropy's own BadgerDB/Qdrant included). It is a
developer bug, not a framework defect.

```python
from langgraph.store.memory import InMemoryStore

store = InMemoryStore()
# Namespace is (org, tenant_id, user_id) — full tuple
store.put(("acme", "tenantA", "userA"), "msg-001", {"content": "secret"})
store.search(("acme", "tenantA", "userA"))   # correct: stays inside tenantA

# THE NAIVE BUG: one element short of full scoping
store.search(("acme",))   # ← element-wise prefix → returns tenantA AND tenantB (all of org "acme")
```

Note: the match is element-wise, **not** string-prefix. `store.search(("tenant",))` does *not*
match `("tenantA", ...)` because `"tenant" != "tenantA"`. The reproduced bug is the shallow-prefix
scan over a shared namespace root.

**Correct posture — deep hierarchical namespaces + `@auth.on.store()` identity binding.**
LangGraph Platform/Server ships an `@auth.on.store()` authorization handler (importable from
`langgraph_sdk`) that runs on **every** store op (put/get/search/delete) and can rewrite/assert
the namespace to the authenticated caller's identity. Combined with deep namespaces like
`(org, user, product)`, a handler that asserts `namespace[:3] == (org, user, product)` makes both
the shallow-prefix scan and a wrong-key read **structurally impossible**. This is the direct
analogue of aisotropy's Scope. A competent dev **can** make the cross-tenant/cross-product read
impossible — the demos below prove it with an identity-bound store facade (plain app code) that
closes both leaks on the OSS store.

```python
# The @auth.on.store() analogue as plain app code (see scripts/smoke.py PART B):
class TenantScopedStore:
    def __init__(self, store, org, tenant, user):
        self._store, self._id = store, (org, tenant, user)
    def get(self, ns, key):
        if tuple(ns[:3]) != self._id:      # reject a wrong/forgotten element
            raise PermissionError(ns)
        return self._store.get(ns, key)
    def search(self, ns_prefix, **kw):     # rewrite: identity wins over caller prefix
        return self._store.search(self._id, **kw)
```

**Residual caveat:** the `@auth.on.store()` namespace-**rewrite** runs inside a LangGraph server
(self-hosted or the managed Platform tier — a sovereignty consideration). The namespace-**assertion**
pattern shown above is plain app code that runs on the OSS store with no server at all.

## What LangGraph does genuinely well — RUN-VERIFIED via `strengths_demo.py`

Isolation-by-default is the narrow axis where aisotropy differs. On the axis LangGraph is built
for — **durable, stateful graph orchestration** — LangGraph wins outright, and aisotropy has **no
equivalent**: aisotropy is a request/response LLM-OS with a memory hierarchy, with no durable graph,
no checkpoint/resume, no human-in-the-loop pause topology, and no time-travel. Saying so plainly is
what makes the rest of this arena credible.

Run: `.venv/bin/python strengths_demo.py` (exit 0; OFFLINE, no LLM). Every strength below is
exercised against the pinned **langgraph 1.2.9** public API and the demo prints the observed result.

- **S1 — Durable execution + human-in-the-loop.** API: `langgraph.graph.StateGraph`,
  `langgraph.checkpoint.memory.InMemorySaver`, `langgraph.types.interrupt` / `Command`. A graph
  pauses mid-run at `interrupt()`; its state lives in the checkpointer, not the graph object. The
  demo **deletes the compiled graph instance**, builds a **brand-new one on the same saver**, and
  it recovers the pending node + interrupt payload and resumes to completion via
  `Command(resume=...)`. That is durable execution plus a first-class approval gate.
- **S2 — Time-travel.** API: `CompiledGraph.get_state_history(config)`. Every super-step is a
  persisted checkpoint (demo enumerated 5); the demo **forks a new run from a past checkpoint**
  (`steps=0`) and replays forward. aisotropy has no checkpoint timeline to travel.
- **S3 — Streaming modes on a cyclic graph.** API: `CompiledGraph.stream(..., stream_mode=...)` +
  `add_conditional_edges` (cycles). A self-looping node (agent-loop topology) is observed two ways
  in the same run: full state snapshots (`values`) vs. per-node deltas (`updates`). aisotropy
  streams LLM token deltas over SSE but has no graph-topology stream.

The honest read of the whole arena: **LangGraph is genuinely great at durable stateful graphs**
(and has the largest ecosystem); **aisotropy differs on one narrow axis** — multi-tenant
isolation-by-default via an unforgeable `Scope` — plus governance defaults and self-hosted
sovereignty. Different tools for different jobs.

## Features covered

| Feature | Status | Notes |
|---|---|---|
| Structured output | Built-in | `with_structured_output` + `OutputFixingParser` (opt-in) |
| Tool calling | Built-in | Graph nodes as tools |
| Streaming | Built-in | `StreamingStdioCallbackHandler` |
| Working memory | Built-in | Checkpointers + Store |
| Semantic memory | Built-in | Store + semantic cache |
| Facts memory | DIY | Not provided |
| Cost governance | Partial / DIY | Per-call `usage_metadata` is built-in; no pre-spend token gate (LangChain has no `UsageLimits`), so the per-tenant quota gate is DIY — see FEATURES.md |
| Observability | Built-in offline / Paid managed | gen_ai OTel spans reach your own exporter offline (via the `langsmith` SDK bridge); the managed LangSmith dashboard is paid — see FEATURES.md |
| Multi-tenant isolation | Tools provided; you wire them | Raw store: namespace depth is convention (shallow prefix → cross-tenant scan, a dev bug). Deep namespaces + `@auth.on.store()` identity binding make cross-tenant reads structurally impossible |

## DX notes

**What's great:**
- Largest ecosystem — hiring pool, community, integrations, pre-built chains.
- `create_agent` in one line for single-tenant — fastest time-to-first-prototype.
- Checkpointers provide durable execution out of the box.
- LangGraph 1.0 shipped with stable API (after v0.x churn).

**What's painful:**
- Isolation is opt-in wiring, not a default. On the raw `Store`, namespace depth is developer
  convention — a shallow-prefix `Store.search()` scans across tenants (a dev bug the primitive
  doesn't catch). The fix exists and works — deep namespaces + an `@auth.on.store()` handler —
  but you must remember to wire it. aisotropy binds its 3 axes from `context.Context` by default
  (corrected 2026-08-04 — `product_id` is a persona selector, in no key and no filter),
  as an unforgeable Scope value object, so a wrong/forgotten key is unrepresentable.
- LangSmith (production tracing/persistence) is paid — the free tier is limited, full features
  are Enterprise-gated. This is a sovereignty concern for regulated deployments.
- Python 3.14 compatibility wall — many packages (including some LangChain deps) don't have
  wheels yet.
- Repackaging churn: parsers moved from `langgraph` to `langchain-classic` in v1.0.

## Adversarial isolation result

**Both postures verified (`.venv/bin/python scripts/smoke.py`, exits 0).**

- **Part A — naive config (raw store):** the positive control passes (exact
  `store.get(ns_a, "msg-001")` reads tenantA's marker), then the short-prefix bug reproduces —
  with tenants namespaced as `(org, tenant, user)`, `store.search(("acme",))` returns items from
  **both** `("acme","tenantA","userA")` and `("acme","tenantB","userB")`. This is a
  developer-under-scoped query, not a framework defect: any KV store hit with no auth layer
  behaves the same way.
- **Part B — correct config:** the same short-prefix search, run through an identity-bound store
  facade (the `@auth.on.store()` analogue that forces `namespace[:3]` to the caller), returns
  **only** the caller's own namespace. The cross-tenant scan is eliminated.

`product_isolation_demo.py` does the same for the product axis: Part A reproduces the shallow-prefix
and wrong-product bugs, Part B closes both. The honest thesis: LangGraph provides the isolation
tools (deep namespaces + the auth handler); aisotropy wires the equivalent by default as an
unforgeable Scope. Isolation is an application-boundary property in both.
