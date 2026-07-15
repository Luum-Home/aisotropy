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

**Convention — namespace prefix match.**

LangGraph's `Store.search()` uses prefix matching on namespaces. If you search `('acme',)`, it
matches ALL tenants whose namespace starts with `'acme'` — including `'acme-prod'`,
`'acme-staging'`, etc. This is a **prefix-match leak**.

```python
# The correct path:
from langgraph.store.memory import InMemoryStore

store = InMemoryStore()
# Namespace is (tenant_id, user_id, session_id) — full tuple
store.search(("tenantA", "userA", "session1"))

# THE LEAK: prefix match on partial namespace
store.search(("acme",))  # ← matches acme, acme-prod, acme-staging, etc.
# → scans across tenants whose namespace starts with "acme"
```

The leak is structural: `Store.search()` is a prefix scanner, not a key lookup. Even with
namespaces, a short prefix scans across tenants.

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
| Multi-tenant isolation | Convention (prefix-match) | `Store.search(('acme',))` is prefix-match → cross-tenant scan |

## DX notes

**What's great:**
- Largest ecosystem — hiring pool, community, integrations, pre-built chains.
- `create_agent` in one line for single-tenant — fastest time-to-first-prototype.
- Checkpointers provide durable execution out of the box.
- LangGraph 1.0 shipped with stable API (after v0.x churn).

**What's painful:**
- Prefix-match `Store.search()` is a fundamental isolation weakness — a short namespace prefix
  scans across tenants. This is the class of bug that aisotropy's exact-key scoping prevents.
- LangSmith (production tracing/persistence) is paid — the free tier is limited, full features
  are Enterprise-gated. This is a sovereignty concern for regulated deployments.
- Python 3.14 compatibility wall — many packages (including some LangChain deps) don't have
  wheels yet.
- Repackaging churn: parsers moved from `langgraph` to `langchain-classic` in v1.0.

## Adversarial isolation result

**LEAK CONFIRMED (prefix-match).** The smoke test proves that `Store.search()` with a partial
namespace prefix scans across tenants. A namespace like `('acme',)` matches all tenants
starting with `'acme'` — `acme`, `acme-prod`, `acme-staging`, etc.

See `scripts/smoke.py` for the exact test — it creates tenants with overlapping namespace
prefixes and proves the prefix-match leak.
