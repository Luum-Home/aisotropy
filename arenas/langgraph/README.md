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

Runs the adversarial isolation matrix using LangGraph's in-memory checkpointer. No LLM API key needed.

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

**Convention — namespace prefix match (element-wise).**

LangGraph's `Store.search(namespace_prefix)` is an **element-wise** prefix scan over the
namespace tuple: a stored namespace matches when the search tuple is a prefix of it. The leak
is a **short prefix over a shared parent element** — a search tuple shallower than full scoping
returns every tenant nested beneath it.

```python
# The correct path — full-depth namespace, exact scoping:
from langgraph.store.memory import InMemoryStore

store = InMemoryStore()
# Namespace is (org, tenant_id, user_id) — full tuple
store.put(("acme", "tenantA", "userA"), "msg-001", {"content": "secret"})
store.search(("acme", "tenantA", "userA"))   # stays inside tenantA

# THE LEAK: one element short of full scoping
store.search(("acme",))   # ← element-wise prefix → returns tenantA AND tenantB (all of org "acme")
```

Note: the match is element-wise, **not** string-prefix. `store.search(("tenant",))` does *not*
match `("tenantA", ...)` because `"tenant" != "tenantA"`. The real, reproducible leak is the
shallow-prefix scan over a shared namespace root: `Store.search()` is a prefix scanner, not a
key lookup, and the developer owns the prefix depth — the framework never checks it against the
authenticated tenant.

## Features covered

| Feature | Status | Notes |
|---|---|---|
| Structured output | Built-in | `with_structured_output` + `OutputFixingParser` (opt-in) |
| Tool calling | Built-in | Graph nodes as tools |
| Streaming | Built-in | `StreamingStdioCallbackHandler` |
| Working memory | Built-in | Checkpointers + Store |
| Semantic memory | Built-in | Store + semantic cache |
| Facts memory | DIY | Not provided |
| Cost governance | DIY | `UsageLimits` per-run (via LangChain) |
| Observability | Paid | LangSmith SaaS (sales-gated) |
| Multi-tenant isolation | Convention (prefix-match) | `Store.search(('acme',))` is an element-wise short-prefix scan → cross-tenant read of every tenant under `acme` |

## DX notes

**What's great:**
- Largest ecosystem — hiring pool, community, integrations, pre-built chains.
- `create_agent` in one line for single-tenant — fastest time-to-first-prototype.
- Checkpointers provide durable execution out of the box.
- LangGraph 1.0 shipped with stable API (after v0.x churn).

**What's painful:**
- Element-wise prefix-match `Store.search()` is a fundamental isolation weakness — a namespace
  prefix shallower than full scoping scans across every tenant nested under it. This is the class
  of bug that aisotropy's exact-key scoping prevents.
- LangSmith (production tracing/persistence) is paid — the free tier is limited, full features
  are Enterprise-gated. This is a sovereignty concern for regulated deployments.
- Python 3.14 compatibility wall — many packages (including some LangChain deps) don't have
  wheels yet.
- Repackaging churn: parsers moved from `langgraph` to `langchain-classic` in v1.0.

## Adversarial isolation result

**LEAK CONFIRMED (element-wise short-prefix scan).** The smoke test (`.venv/bin/python
scripts/smoke.py`, exits 0) verifies the positive control (exact `store.get(ns_a, "msg-001")`
reads tenantA's marker) and then reproduces the leak: with tenants namespaced as
`(org, tenant, user)`, `store.search(("acme",))` — one element short of full scoping — returns
items from **both** `("acme","tenantA","userA")` and `("acme","tenantB","userB")`, surfacing
tenantA's marker in a cross-tenant scan.

See `scripts/smoke.py` for the exact test. The match is element-wise, so the leak is the
shallow-prefix scan over a shared namespace root — not a string-prefix match on the tenant name.
