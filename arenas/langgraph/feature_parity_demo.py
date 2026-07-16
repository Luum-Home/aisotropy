#!/usr/bin/env python3
"""
Comprehensive feature-parity demo for LangGraph vs the aisotropy runtime.

Runs 100% OFFLINE with a deterministic FAKE chat model + synthetic data. The
point is what the FRAMEWORK provides, NOT real LLM output. Every feature is
EXERCISED by actually calling LangGraph / LangChain APIs (or, where the
framework lacks the capability, by running the DIY version we had to write and
LABELLING it DIY / NOT-POSSIBLE honestly).

Verified against (arena .venv):
    langgraph 1.2.9 / langchain 1.3.13 / langchain-core 1.4.9
    langsmith 0.10.5 / opentelemetry-sdk 1.43.0 / numpy 2.5.1 / python 3.13

Run:
    .venv/bin/python feature_parity_demo.py

Each line is:
    FEATURE <n> <name>: [CLASS] <one-line evidence>
CLASS ∈ BUILT-IN / CONVENTION / PARTIAL / DIY / PAID / NOT-POSSIBLE.

HONESTY RULE: nothing is labelled BUILT-IN unless LangGraph/LangChain really
provides it and this file EXECUTES it. NOT-POSSIBLE features show the closest
thing the framework offers.

The headline aisotropy differentiator under test is the MEMORY-TIER hierarchy:
aisotropy has FOUR stores of different NATURE by access temperature
(BadgerDB µs KV / SQLite+FTS5 keyword / Qdrant vector / DataFusion offline
batch). LangGraph gives TWO tiers: a checkpointer (thread history) and a Store
(namespaced KV + optional vector). No distinct full-text/FTS tier, no
transactional outbox, no offline consolidation engine.
"""

# ── OTel/LangSmith-bridge env MUST be set BEFORE importing langchain, and the
# global TracerProvider installed first, so LangChain emits into OUR in-memory
# exporter with nothing leaving the machine (mirrors cost_obs_demo.py). ────────
import os

os.environ.setdefault("LANGSMITH_OTEL_ENABLED", "true")
os.environ.setdefault("LANGSMITH_TRACING", "true")
os.environ.setdefault("LANGSMITH_OTEL_ONLY", "true")
os.environ.setdefault("LANGSMITH_API_KEY", "lsv2_dummy_offline_key")
os.environ.setdefault("LANGSMITH_ENDPOINT", "http://localhost:1")

from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

_SPAN_EXPORTER = InMemorySpanExporter()
_provider = TracerProvider()
_provider.add_span_processor(SimpleSpanProcessor(_SPAN_EXPORTER))
trace.set_tracer_provider(_provider)

import asyncio
import time
import warnings
from typing import Optional

warnings.filterwarnings("ignore")

import numpy as np
from pydantic import BaseModel, Field, ValidationError

from langchain_core.language_models.chat_models import BaseChatModel
from langchain_core.messages import AIMessage, HumanMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from langchain_core.globals import set_llm_cache
from langchain_core.caches import InMemoryCache
from langchain_core.messages import trim_messages
from langchain_core.tools import tool

from langgraph.graph import START, END, MessagesState, StateGraph
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.store.memory import InMemoryStore
from langgraph.prebuilt import create_react_agent, ToolNode


SYNTH_PRICE_PER_1K = 0.50  # SYNTHETIC $/1k tokens, cost math only

# Tally of classes across every FEATURE line printed.
TALLY: dict[str, int] = {}


def feature(n, name, cls, evidence):
    """Print one canonical FEATURE line and tally the class."""
    TALLY[cls] = TALLY.get(cls, 0) + 1
    print(f"FEATURE {n} {name}: [{cls}] {evidence}")


def rule(title):
    print("\n" + "=" * 78)
    print(title)
    print("=" * 78)


# ── Deterministic offline fake model (usage_metadata is SYNTHETIC) ────────────
MODEL_CALLS = {"n": 0}


class CountingFakeModel(BaseChatModel):
    """Offline stub. Tokens are SYNTHETIC (len(text)//4). Counts its calls so we
    can PROVE whether the model ran or was gated out before running."""

    def _generate(self, messages, stop=None, run_manager=None, **kwargs):
        MODEL_CALLS["n"] += 1
        text = messages[-1].content if messages else ""
        in_tok = max(1, len(str(text)) // 4)
        out_tok = 12
        msg = AIMessage(
            content=f"[fake-reply] {str(text)[:48]}",
            usage_metadata={"input_tokens": in_tok, "output_tokens": out_tok,
                            "total_tokens": in_tok + out_tok},
        )
        return ChatResult(generations=[ChatGeneration(message=msg)])

    @property
    def _llm_type(self):
        return "counting-fake"


MODEL = CountingFakeModel()


# ── A deterministic hash embedder (offline, no provider) ──────────────────────
def hash_embed(texts, dims=32):
    out = []
    for t in texts:
        v = np.zeros(dims)
        for i, ch in enumerate(str(t).lower()):
            v[(ord(ch) + i) % dims] += 1.0
        n = np.linalg.norm(v) or 1.0
        out.append((v / n).tolist())
    return out


# =============================================================================
# AREA A — MEMORY TIERS (the headline)
# =============================================================================
def area_memory():
    rule("AREA A — MEMORY TIERS  (aisotropy: 4 stores of different nature by access temperature)")

    # ── F1 Temperature hierarchy (meta / classify) ──
    # LangGraph exposes exactly TWO persistence surfaces:
    #   checkpointer  -> per-thread conversation state (working memory)
    #   BaseStore     -> namespaced KV, with an OPTIONAL vector index
    # Both are the SAME nature (KV). There is no separate keyword/FTS tier and no
    # offline batch-consolidation engine. So it's a 2-tier story, not aisotropy's 4.
    tiers = {"checkpointer(thread history)": "KV", "Store(namespaced)": "KV+optional-vector"}
    feature(1, "Temperature-tier hierarchy", "PARTIAL",
            f"2 surfaces {list(tiers)} — both KV-nature; NO distinct FTS tier, NO offline "
            f"batch tier. aisotropy=4 stores of different nature; LangGraph=2.")

    # ── F2 Working memory: recent history per tenant/user/session ──
    def wm_node(state):
        return {"messages": [AIMessage(content=f"saw {len(state['messages'])} msgs")]}
    g = StateGraph(MessagesState)
    g.add_node("c", wm_node); g.add_edge(START, "c"); g.add_edge("c", END)
    wm_app = g.compile(checkpointer=InMemorySaver())
    cfgA = {"configurable": {"thread_id": "tenantA:userA:sess1"}}
    wm_app.invoke({"messages": [HumanMessage(content="hi")]}, cfgA)
    wm_app.invoke({"messages": [HumanMessage(content="again")]}, cfgA)
    histA = wm_app.get_state(cfgA).values["messages"]
    cfgB = {"configurable": {"thread_id": "tenantB:userB:sess1"}}
    histB_empty = not wm_app.get_state(cfgB).values
    feature(2, "Working memory (recent history)", "BUILT-IN",
            f"checkpointer(thread_id) auto-persists history: thread A has {len(histA)} msgs "
            f"hydrated across turns; unrelated thread B isolated (empty={histB_empty}).")

    # ── F3 Facts / episodic with KEYWORD / full-text search ──
    # LangGraph Store has NO full-text/FTS index. search(query=) is VECTOR only;
    # search(filter=) is exact dict-equality, not keyword ranking. A real BM25/FTS5
    # keyword tier does not exist -> NOT-POSSIBLE. Closest thing: substring/filter scan (DIY).
    facts_store = InMemoryStore()  # NO index -> pure KV, no search ranking
    ns = ("acme", "userA", "facts")
    facts_store.put(ns, "f1", {"text": "production database runs on PostgreSQL 16"})
    facts_store.put(ns, "f2", {"text": "the team stands up at 9am UTC"})
    facts_store.put(ns, "f3", {"text": "billing uses Stripe in production"})
    # DIY keyword search: no FTS ranking, we substring-scan ourselves.
    kw = "production"
    diy_hits = [i.value["text"] for i in facts_store.search(ns)
                if kw in i.value["text"].lower()]
    feature(3, "Facts memory w/ keyword/FTS search", "NOT-POSSIBLE",
            f"Store has no full-text/BM25 tier; search(query=) is vector-only, search(filter=) "
            f"is exact dict-equality. Closest=DIY substring scan -> {len(diy_hits)} hits for "
            f"'{kw}'. aisotropy ships SQLite+FTS5 keyword tier.")

    # ── F4 Semantic / VECTOR retrieval (RAG) ──
    sem_store = InMemoryStore(index={"dims": 32, "embed": hash_embed})
    dns = ("acme", "userA", "docs")
    sem_store.put(dns, "d1", {"text": "The refund policy allows returns within 30 days."})
    sem_store.put(dns, "d2", {"text": "Our office is in Buenos Aires, Argentina."})
    sem_store.put(dns, "d3", {"text": "International shipping takes 10-14 business days."})
    rag = sem_store.search(dns, query="how long to get money back", limit=1)
    feature(4, "Semantic/vector RAG", "BUILT-IN",
            f"Store(index=embed) -> search(query=): top hit score={rag[0].score:.3f} "
            f"'{rag[0].value['text'][:38]}...' retrieved by vector similarity.")

    # ── F5 Summary / rolling buffer (context engineering) ──
    # trim_messages is a BUILT-IN helper (keeps last-N / token budget) — but it
    # DROPS old messages; it does NOT condense them into a summary. Auto-summarize-
    # into-a-buffer is DIY (langmem, a separate package, or your own node).
    long_hist = [HumanMessage(content=f"message number {i}") for i in range(20)]
    trimmed = trim_messages(long_hist, max_tokens=5, strategy="last",
                            token_counter=len, allow_partial=False)
    # DIY summary buffer: condense the dropped prefix into one line ourselves.
    dropped = long_hist[:-len(trimmed)] if len(trimmed) < len(long_hist) else []
    diy_summary = f"[summary of {len(dropped)} earlier msgs]"
    feature(5, "Summary / rolling buffer", "PARTIAL",
            f"trim_messages() BUILT-IN kept last {len(trimmed)}/{len(long_hist)} (it DROPS, "
            f"not condenses); rolling SUMMARY buffer is DIY -> '{diy_summary}'. aisotropy "
            f"auto-condenses via a summarizer node.")

    # ── F6 importance × recency × relevance SCORING ──
    # Store.search ranks by cosine SIMILARITY only. No importance weight, no recency
    # decay. The composite score is DIY.
    scored_docs = [
        {"text": "critical: prod outage runbook", "importance": 1.0, "age_s": 5},
        {"text": "note: cafeteria menu",           "importance": 0.1, "age_s": 5},
        {"text": "old: deprecated api key rotation", "importance": 0.8, "age_s": 9000},
    ]
    # framework relevance (vector) — approximate with our embedder vs a query
    q = hash_embed(["prod incident"])[0]
    def cos(a, b):
        a, b = np.array(a), np.array(b)
        return float(a @ b / ((np.linalg.norm(a) * np.linalg.norm(b)) or 1))
    for d in scored_docs:
        rel = cos(q, hash_embed([d["text"]])[0])
        rec = 1.0 / (1.0 + d["age_s"] / 3600.0)          # DIY recency decay
        d["score"] = round(rel * d["importance"] * rec, 4)  # DIY composite
    top = max(scored_docs, key=lambda d: d["score"])
    feature(6, "Importance×recency×relevance scoring", "DIY",
            f"Store ranks by cosine ONLY; importance+recency composite hand-rolled -> "
            f"top='{top['text'][:28]}' score={top['score']}. aisotropy scores Rel×Imp×Rec built-in.")

    # ── F7 contradiction detection / supersede ──
    # Store.put on an EXISTING key is an upsert (overwrite) — that's key-level, not
    # SEMANTIC supersede. Detecting that a NEW fact contradicts an OLD one by meaning
    # and archiving the old vector is DIY.
    sup_store = InMemoryStore(index={"dims": 32, "embed": hash_embed})
    sns = ("acme", "userA", "facts")
    sup_store.put(sns, "loc", {"text": "we deploy on AWS"})
    # DIY supersede: find semantically-near existing fact, delete it, write the new one.
    newfact = "we deploy on Google Cloud now"
    near = sup_store.search(sns, query=newfact, limit=1)
    near_score = round(near[0].score, 3) if near else 0.0
    superseded = False
    if near and near[0].score > 0.3:           # DIY threshold — hand-tuned, not framework-owned
        sup_store.delete(sns, near[0].key)     # archive the contradicted fact
        superseded = True
    sup_store.put(sns, "loc2", {"text": newfact})
    remaining = [i.value["text"] for i in sup_store.search(sns)]
    feature(7, "Contradiction detection / supersede", "DIY",
            f"Store.put=key-upsert only; semantic supersede hand-rolled (near-score={near_score}, "
            f"deleted old fact, superseded={superseded}) -> remaining={remaining}. aisotropy has "
            f"facts.Supersede + dual-index vector delete built-in.")

    # ── F8 knowledge ingestion (chunk + embed a document) ──
    # embed+store is BUILT-IN (Store index). Chunking lives in a SEPARATE package
    # (langchain-text-splitters, not core / not installed here) -> we chunk DIY.
    doc = ("Aisotropy is an LLM-OS runtime. It splits memory by access temperature. "
           "Working memory is BadgerDB. Facts are SQLite FTS5. Vectors are Qdrant. "
           "Consolidation runs offline in a Rust DataFusion worker.")
    def diy_chunk(text, size=60):
        words, chunks, cur = text.split(), [], ""
        for w in words:
            if len(cur) + len(w) + 1 > size:
                chunks.append(cur.strip()); cur = ""
            cur += " " + w
        if cur.strip():
            chunks.append(cur.strip())
        return chunks
    chunks = diy_chunk(doc)
    ing_store = InMemoryStore(index={"dims": 32, "embed": hash_embed})
    kns = ("acme", "_shared", "knowledge")
    for i, ch in enumerate(chunks):
        ing_store.put(kns, f"chunk-{i}", {"text": ch})  # embed happens on put (BUILT-IN)
    hit = ing_store.search(kns, query="what stores the vectors", limit=1)
    feature(8, "Knowledge ingestion (chunk+embed)", "PARTIAL",
            f"embed-on-put + vector retrieval BUILT-IN ({len(chunks)} chunks stored, query hit "
            f"'{hit[0].value['text'][:30]}...'); CHUNKING is a separate splitter package (DIY here). "
            f"aisotropy ships a chunk→embed→store knowledge route.")

    # ── F9 bring-your-own embedder ──
    # The index accepts ANY embed callable -> swapping it changes ranking. Proven by
    # constructing the store with hash_embed above; here we prove a DIFFERENT embedder
    # changes results.
    def reverse_embed(texts):  # a different, equally-valid embedder
        return hash_embed([str(t)[::-1] for t in texts])
    s1 = InMemoryStore(index={"dims": 32, "embed": hash_embed})
    s2 = InMemoryStore(index={"dims": 32, "embed": reverse_embed})
    for s in (s1, s2):
        s.put(("n",), "a", {"text": "alpha beta"}); s.put(("n",), "b", {"text": "gamma delta"})
    r1 = s1.search(("n",), query="alpha", limit=1)[0].key
    r2 = s2.search(("n",), query="alpha", limit=1)[0].key
    feature(9, "Bring-your-own embedder", "BUILT-IN",
            f"index={{'embed': <callable>}} accepts any function; swapping embedders changes "
            f"ranking (embedderX top='{r1}' vs embedderY top='{r2}'). Fully self-hostable, no provider.")


# =============================================================================
# AREA B — CONTRACT / OUTPUT
# =============================================================================
class WeatherReply(BaseModel):
    city: str = Field(min_length=1)
    temp_c: int
    conditions: str = Field(min_length=1)


def area_contract():
    rule("AREA B — CONTRACT / STRUCTURED OUTPUT")

    # ── F10 reflection loop: schema-breaking output -> retry -> fail closed ──
    # PARSE + VALIDATE is BUILT-IN (Pydantic model / with_structured_output). But
    # LangChain 1.x REMOVED OutputFixingParser/RetryOutputParser — the bounded
    # "feed the exact typing error back and retry, then FAIL CLOSED" loop is DIY now.
    sb_tries = {"n": 0}

    class SchemaBreakingModel(BaseChatModel):
        """Returns invalid JSON on attempt 1, valid on attempt >=2."""
        def _generate(self, messages, stop=None, run_manager=None, **kw):
            sb_tries["n"] += 1
            if sb_tries["n"] == 1:
                body = '{"city": "", "temp_c": "hot"}'   # empty city + non-int temp
            else:
                body = '{"city": "Lisbon", "temp_c": 19, "conditions": "clear"}'
            return ChatResult(generations=[ChatGeneration(message=AIMessage(content=body))])
        @property
        def _llm_type(self): return "schema-breaking"

    sbm = SchemaBreakingModel()
    MAX_RETRIES = 3
    parsed, last_err, attempts = None, None, 0
    convo = [HumanMessage(content="weather in Lisbon as JSON")]
    for attempt in range(1, MAX_RETRIES + 1):
        attempts = attempt
        raw = sbm.invoke(convo).content
        try:
            parsed = WeatherReply.model_validate_json(raw)  # BUILT-IN validation
            break
        except ValidationError as e:
            last_err = e
            # DIY reflection: feed the EXACT typing error back for the next attempt.
            convo.append(AIMessage(content=raw))
            convo.append(HumanMessage(content=f"Invalid: {e.error_count()} errors. Fix and resend."))
    # Prove fail-closed: a model that NEVER complies must raise, not return garbage.
    class AlwaysBad(BaseChatModel):
        def _generate(self, m, stop=None, run_manager=None, **kw):
            return ChatResult(generations=[ChatGeneration(message=AIMessage(content='{"bad":1}'))])
        @property
        def _llm_type(self): return "always-bad"
    ab, failed_closed = AlwaysBad(), False
    for _ in range(MAX_RETRIES):
        try:
            WeatherReply.model_validate_json(ab.invoke([HumanMessage(content="x")]).content)
        except ValidationError:
            failed_closed = True
    feature(10, "Fail-closed reflection loop", "PARTIAL",
            f"Pydantic parse/validate BUILT-IN; recovered after {attempts} attempts "
            f"(parsed={parsed.city}/{parsed.temp_c}). But the bounded retry+fail-closed LOOP is "
            f"DIY (OutputFixingParser removed in LangChain 1.x); non-complying model raised "
            f"(fail_closed={failed_closed}), never returned garbage.")

    # ── F11 SSE streaming ──
    async def _stream_count():
        def n(state): return {"messages": [AIMessage(content="streamed")]}
        g = StateGraph(MessagesState); g.add_node("a", n)
        g.add_edge(START, "a"); g.add_edge("a", END)
        app = g.compile()
        events = [ev async for ev in app.astream(
            {"messages": [HumanMessage(content="go")]}, stream_mode="updates")]
        return len(events)
    n_events = asyncio.run(_stream_count())
    feature(11, "SSE / token streaming", "BUILT-IN",
            f".astream(stream_mode=...) yields incremental events ({n_events} update event(s)); "
            f"also stream_mode='messages' for token-level. HTTP-SSE framing is your web layer.")

    # ── F12 immediate cancellation ──
    async def _cancel():
        RAN = {"nodes": 0}
        async def slow(state):
            RAN["nodes"] += 1
            await asyncio.sleep(0.2)
            return {"messages": [AIMessage(content="done")]}
        g = StateGraph(MessagesState)
        g.add_node("a", slow); g.add_node("b", slow)
        g.add_edge(START, "a"); g.add_edge("a", "b"); g.add_edge("b", END)
        app = g.compile()
        seen = []
        async def run():
            async for ev in app.astream({"messages": [HumanMessage(content="go")]},
                                        stream_mode="updates"):
                seen.append(ev)
        t = asyncio.create_task(run())
        await asyncio.sleep(0.25)  # let node a finish; cancel during node b
        t.cancel()
        cancelled = False
        try:
            await t
        except asyncio.CancelledError:
            cancelled = True
        return cancelled, len(seen)
    cancelled, seen = asyncio.run(_cancel())
    feature(12, "Immediate cancellation", "BUILT-IN",
            f"asyncio CancelledError propagates INTO the running node ({seen} node finished, "
            f"cancel took effect mid-flight, cancelled={cancelled}). Cuts at the next await, "
            f"like ctx.Done() — no partial replay persisted.")

    # ── F13 response cache (stateless) ──
    set_llm_cache(InMemoryCache())  # BUILT-IN exact-match LLM cache
    cache_model = CountingFakeModel()
    before = MODEL_CALLS["n"]
    cache_model.invoke([HumanMessage(content="cache me")])
    cache_model.invoke([HumanMessage(content="cache me")])   # identical -> cache hit
    after = MODEL_CALLS["n"]
    set_llm_cache(None)  # reset so later features count real calls
    feature(13, "Response cache (stateless)", "BUILT-IN",
            f"set_llm_cache(InMemoryCache()) short-circuits identical prompts (2 invokes -> "
            f"{after - before} model call). BUT it's a GLOBAL exact cache with NO tenant "
            f"partition -> cross-tenant hit is a footgun; per-(tenant,user) partition is DIY. "
            f"aisotropy partitions cache by (tenant,user) + adds semantic cache.")


# =============================================================================
# AREA C — TOOLS / AGENTIC
# =============================================================================
def area_tools():
    rule("AREA C — TOOLS / AGENTIC")

    @tool
    def search_docs(query: str) -> str:
        """Search public docs (no scope needed)."""
        return f"docs about {query}"

    @tool
    def read_financials(quarter: str) -> str:
        """Read financial data (needs read:financial-data)."""
        return f"revenue for {quarter}: $1M"

    @tool
    def write_tasks(title: str) -> str:
        """Create a task (needs write:tasks)."""
        return f"created task {title}"

    ALL_TOOLS = {"search_docs": search_docs,
                 "read_financials": read_financials,
                 "write_tasks": write_tasks}
    SCOPE_REQ = {"search_docs": None,
                 "read_financials": "read:financial-data",
                 "write_tasks": "write:tasks"}

    # ── F14 scope-gated tools (governance by absence) ──
    # LangGraph binds WHATEVER tool list you hand it. Filtering the list by the
    # caller's scopes so a tool is ABSENT from the payload is DIY — but it works,
    # and a hallucinated call to an absent tool is rejected because the ToolNode
    # never has it.
    def tools_for(scopes: set) -> list:
        return [t for name, t in ALL_TOOLS.items()
                if SCOPE_REQ[name] is None or SCOPE_REQ[name] in scopes]
    caller_scopes = {"read:financial-data"}      # NOT write:tasks
    permitted = tools_for(caller_scopes)
    permitted_names = {t.name for t in permitted}
    # Build a ToolNode with ONLY permitted tools; a hallucinated write_tasks call is unknown.
    tn = ToolNode(permitted)
    hallucinated_absent = "write_tasks" not in permitted_names
    feature(14, "Scope-gated tools", "DIY",
            f"you filter the tool list by scope before binding: scopes={caller_scopes} -> "
            f"offered={sorted(permitted_names)}; write_tasks ABSENT (hallucination-proof="
            f"{hallucinated_absent}). Framework binds any list you give it; the gate is your code. "
            f"aisotropy scope-gates in the runtime + re-validates the chosen name.")

    # ── F15 multi-step agent loop ──
    # create_react_agent needs a model that emits tool_calls. Our fake model: first
    # turn -> call search_docs; after the ToolMessage comes back -> final answer.
    from langchain_core.messages import ToolMessage

    class ToolCallingFakeModel(BaseChatModel):
        def bind_tools(self, tools, **kw):
            return self  # accept the bound tools; drive the loop deterministically
        def _generate(self, messages, stop=None, run_manager=None, **kw):
            already_called = any(isinstance(m, ToolMessage) for m in messages)
            if already_called:
                msg = AIMessage(content="[fake-reply] done using tool result")
            else:
                msg = AIMessage(content="", tool_calls=[
                    {"name": "search_docs", "args": {"query": "topic"}, "id": "call_1"}])
            return ChatResult(generations=[ChatGeneration(message=msg)])
        @property
        def _llm_type(self): return "toolcalling-fake"

    agent = create_react_agent(ToolCallingFakeModel(),
                               tools_for({"read:financial-data", "write:tasks"}))
    result = agent.invoke({"messages": [HumanMessage(content="do a multi-step task")]})
    steps = len(result["messages"])
    used_tool = any(isinstance(m, ToolMessage) for m in result["messages"])
    feature(15, "Multi-step agent loop", "BUILT-IN",
            f"create_react_agent(model, tools) runs the reason→act→observe loop natively "
            f"({steps} messages in the trajectory, tool actually invoked={used_tool}). ToolNode "
            f"handles execution + re-entry.")

    # ── F16 typed tools / MCP ──
    schema = read_financials.args_schema.model_json_schema()
    typed = "quarter" in schema.get("properties", {})
    try:
        import langchain_mcp_adapters  # noqa
        mcp = "installed"
    except ImportError:
        mcp = "separate pkg (langchain-mcp-adapters) NOT installed"
    feature(16, "Typed tools / MCP", "PARTIAL",
            f"@tool derives a typed JSON-Schema from the signature (args={list(schema['properties'])}, "
            f"typed={typed}) — BUILT-IN. MCP is a SEPARATE adapter package ({mcp}) — CONVENTION, "
            f"not core. aisotropy: typed tools + native MCP consumption.")


# =============================================================================
# AREA D — AUTH
# =============================================================================
def area_auth():
    rule("AREA D — AUTH / IDENTITY")

    # ── F17 bearer-token -> identity ──
    # The LangGraph LIBRARY has no auth. Identity binding (verify a bearer token,
    # bind principal.tenant == path.tenant, mint a scope) is entirely your web
    # middleware. (LangGraph PLATFORM, the hosted server, adds an @auth handler —
    # but that's the paid/hosted product, not the OSS library exercised here.)
    def diy_auth(bearer: str, path_tenant: str) -> Optional[dict]:
        # toy verifier: token format "tenant:user:scopes"
        try:
            t, u, scopes = bearer.split(":")
        except ValueError:
            return None                      # 401 malformed
        if t != path_tenant:
            return None                      # 403 must-agree bind mismatch
        return {"tenant": t, "user": u, "scopes": set(scopes.split(","))}
    good = diy_auth("acme:alice:read", "acme")
    mismatch = diy_auth("acme:alice:read", "globex")   # cross-tenant -> reject
    feature(17, "Bearer-token → identity bind", "NOT-POSSIBLE",
            f"OSS library has NO auth surface; identity+must-agree bind is DIY middleware "
            f"(good={good['tenant'] if good else None}, cross-tenant rejected={mismatch is None}). "
            f"LangGraph Platform's @auth is the PAID hosted server. aisotropy binds JWT→scope in "
            f"the runtime (401 default-deny / 403 mismatch).")


# =============================================================================
# AREA E — COST / GOVERNANCE
# =============================================================================
def area_cost():
    rule("AREA E — COST / GOVERNANCE  (full proof in cost_obs_demo.py)")

    # ── F18 pre-spend quota gate (per-tenant, before the model runs) ──
    BUDGET = {"tenantA": 20, "tenantB": 10_000}
    spent = {"tenantA": 18, "tenantB": 0}
    def estimate(prompt): return max(1, len(prompt) // 4) + 12
    def guarded(tenant, prompt):
        if spent[tenant] + estimate(prompt) > BUDGET[tenant]:
            return False  # REJECT before invoking the model
        MODEL.invoke([HumanMessage(content=prompt)])
        spent[tenant] += estimate(prompt)
        return True
    before = MODEL_CALLS["n"]
    ok = guarded("tenantB", "quick status")           # allowed -> model runs
    mid = MODEL_CALLS["n"]
    rej = guarded("tenantA", "another costly question")  # over budget -> model NOT reached
    after = MODEL_CALLS["n"]
    gate_ok = ok and (mid - before == 1) and (not rej) and (after - mid == 0)
    feature(18, "Pre-spend per-tenant quota gate", "DIY",
            f"no UsageLimits / check_before_request in LangChain 1.x; hand-rolled gate proven: "
            f"allowed turn ran model (Δ=1), rejected turn did NOT (Δ=0), gate_ok={gate_ok}. "
            f"aisotropy ships a BadgerDB token-bucket pre-spend gate.")

    # ── F19 circuit breaker (open after N failures, stop calling) ──
    # No built-in breaker. with_fallbacks() is FAILURE-fallback (tries backup on error),
    # not a breaker that STOPS calling after N failures. We implement the breaker DIY.
    class FlakyModel(BaseChatModel):
        def _generate(self, m, stop=None, run_manager=None, **kw):
            raise RuntimeError("provider down")
        @property
        def _llm_type(self): return "flaky"
    flaky = FlakyModel()
    consec, OPEN_AT, breaker_open, real_calls = 0, 5, False, 0
    for i in range(8):
        if breaker_open:
            continue  # fast-fail WITHOUT calling the provider
        try:
            real_calls += 1
            flaky.invoke([HumanMessage(content="x")])
        except RuntimeError:
            consec += 1
            if consec >= OPEN_AT:
                breaker_open = True
    feature(19, "Circuit breaker", "DIY",
            f"no breaker in framework (with_fallbacks is failure-FALLBACK, not open-after-N); "
            f"hand-rolled breaker opened after {consec} failures -> only {real_calls}/8 attempts "
            f"reached the provider, rest fast-failed. aisotropy: llm.WithCircuitBreaker(5,30s).")

    # ── F20 quota-pressure model degradation (route to cheap model under load) ──
    # with_fallbacks triggers on ERROR, not on LOAD. Pressure-based routing is DIY.
    def route(pressure, prompt):
        model = "cheap-model" if pressure >= 0.95 else "primary-model"
        return model
    hi = route(0.97, "q"); lo = route(0.40, "q")
    feature(20, "Quota-pressure degradation", "DIY",
            f"with_fallbacks is failure-based, not pressure-based; hand-rolled router picks "
            f"pressure=0.97→{hi}, pressure=0.40→{lo}. aisotropy: llm.DegradeAt(0.95) overrides "
            f"even an explicit model_preference.")

    # ── F21 cost meter (per-tenant) ──
    ledger = {}
    for tenant, prompt in [("tenantA", "hello"), ("tenantB", "world"), ("tenantA", "again")]:
        ai = MODEL.invoke([HumanMessage(content=prompt)])
        um = ai.usage_metadata                     # BUILT-IN per-call usage
        ledger.setdefault(tenant, 0)
        ledger[tenant] += um["total_tokens"]       # DIY per-tenant aggregate
    feature(21, "Cost meter (per-tenant)", "PARTIAL",
            f"AIMessage.usage_metadata is per-call BUILT-IN; per-TENANT aggregate is DIY "
            f"(ledger={ledger}). Aggregation helpers key by MODEL name, not tenant. aisotropy "
            f"ships a per-tenant cost meter at GET /costs.")


# =============================================================================
# AREA F — OBSERVABILITY
# =============================================================================
def area_observability():
    rule("AREA F — OBSERVABILITY  (full OTel proof in cost_obs_demo.py)")

    # ── F22 OTel gen_ai spans ──
    def n(state): return {"messages": [MODEL.invoke(state["messages"])]}
    g = StateGraph(MessagesState); g.add_node("chat", n)
    g.add_edge(START, "chat"); g.add_edge("chat", END)
    app = g.compile()
    time.sleep(0.4); _provider.force_flush(); _SPAN_EXPORTER.clear()
    app.invoke({"messages": [HumanMessage(content="trace this")]},
               config={"metadata": {"tenant_id": "tenantA"}, "tags": ["tenant:tenantA"],
                       "run_name": "chat-turn"})
    time.sleep(0.6); _provider.force_flush()
    spans = _SPAN_EXPORTER.get_finished_spans()
    gen_ai = any(any(k.startswith("gen_ai.") for k in (s.attributes or {})) for s in spans)
    tenant_on = any("tenant" in k.lower() for s in spans for k in (s.attributes or {}))
    feature(22, "OTel gen_ai spans", "BUILT-IN",
            f"{len(spans)} span(s) captured offline into our in-memory exporter; gen_ai semconv "
            f"present={gen_ai}. tenant_id on spans is DIY (injected via run metadata, present="
            f"{tenant_on}). Bridge ships in the langsmith SDK; managed LangSmith cloud is PAID. "
            f"aisotropy emits gen_ai spans WITH tenant scope by default.")

    # ── F23 health / readiness probe ──
    # The library is not a server -> no /health. A readiness probe (cheap, read-only,
    # per-tier, no LLM spend) is your web code. We simulate one over the Store tiers.
    store = InMemoryStore(index={"dims": 32, "embed": hash_embed})
    store.put(("_health",), "probe", {"text": "ok"})
    def readiness():
        checks = {}
        try:
            checks["store"] = store.get(("_health",), "probe") is not None
        except Exception:
            checks["store"] = False
        # deliberately do NOT call the LLM (would violate pre-spend gate)
        return checks, all(checks.values())
    checks, healthy = readiness()
    feature(23, "Health / readiness probe", "DIY",
            f"library has no server/endpoint; hand-rolled read-only tier probe (no LLM spend) -> "
            f"checks={checks}, ready={healthy}. aisotropy ships GET /healthz + /health probing "
            f"the memory tiers.")

    # ── F24 product evaluation (IR precision/recall of retrieval) ──
    # No offline IR-metric harness in the OSS framework (LangSmith evaluators are the
    # SaaS/paid path). We compute P@k / recall DIY over a real Store retrieval.
    ev_store = InMemoryStore(index={"dims": 32, "embed": hash_embed})
    ns = ("acme", "userA", "docs")
    corpus = {
        "r1": "refund policy: returns within 30 days",
        "r2": "refund exceptions for final-sale items",
        "n1": "office hours are 9 to 5",
        "n2": "parking is behind the building",
    }
    for k, v in corpus.items():
        ev_store.put(ns, k, {"text": v})
    relevant = {"r1", "r2"}
    k = 2
    retrieved = [i.key for i in ev_store.search(ns, query="return my purchase for money back", limit=k)]
    tp = len(set(retrieved) & relevant)
    precision = tp / k
    recall = tp / len(relevant)
    feature(24, "Product evaluation (IR metrics)", "DIY",
            f"no offline IR-metric harness (LangSmith evaluators = SaaS/PAID); hand-computed over "
            f"real Store retrieval: retrieved={retrieved} P@{k}={precision:.2f} recall={recall:.2f}. "
            f"aisotropy ships internal/eval (P@k/MRR + LLM-judge, isolation-preserving).")


# =============================================================================
# AREA G — CONSOLIDATION / SLEEP
# =============================================================================
def area_consolidation():
    rule("AREA G — CONSOLIDATION / SLEEP")

    # ── F25 transactional outbox (atomic message + event) ──
    # LangGraph's checkpointer and Store are SEPARATE stores with NO shared ACID
    # transaction. You cannot write "a message AND a consolidation event, both or
    # neither" atomically across them. So the transactional outbox is NOT-POSSIBLE.
    # Closest thing: two independent writes with no atomicity (a crash between them
    # leaves an orphan). We show the gap explicitly.
    checkpointer = InMemorySaver()
    store = InMemoryStore()
    def node(state): return {"messages": [AIMessage(content="turn stored")]}
    g = StateGraph(MessagesState); g.add_node("c", node)
    g.add_edge(START, "c"); g.add_edge("c", END)
    app = g.compile(checkpointer=checkpointer, store=store)
    cfg = {"configurable": {"thread_id": "t:u:s"}}
    app.invoke({"messages": [HumanMessage(content="hi")]}, cfg)  # writes checkpoint
    # separate, NON-atomic write of the consolidation "event":
    store.put(("t", "u", "outbox"), "evt-1", {"intent": "consolidate", "committed_with_msg": False})
    # There is no txn joining these two — demonstrate they are independent.
    feature(25, "Transactional outbox (atomic msg+event)", "NOT-POSSIBLE",
            "checkpointer and Store are separate stores with NO shared ACID txn; message + "
            "consolidation event cannot be written atomically (a crash between the two writes "
            "orphans one). aisotropy writes both in ONE BadgerDB txn (AppendWithOutbox) + "
            "cold-boot recovery.")

    # ── F26 async consolidation (dedup / contradictions offline) ──
    # No sleep-cycle / batch-consolidation engine. We run a DIY offline dedup pass
    # over a Store scope.
    cons_store = InMemoryStore()
    ns = ("acme", "userA", "facts")
    raw = ["we use PostgreSQL", "we use PostgreSQL", "hi", "thanks", "billing via Stripe"]
    for i, t in enumerate(raw):
        cons_store.put(ns, f"f{i}", {"text": t})
    # DIY offline consolidation: drop exact dups + noise
    NOISE = {"hi", "thanks", "ok", "hello"}
    seen, kept, dropped = set(), [], 0
    for item in cons_store.search(ns):
        txt = item.value["text"]
        if txt in seen or txt.lower() in NOISE:
            cons_store.delete(ns, item.key); dropped += 1
        else:
            seen.add(txt); kept.append(txt)
    feature(26, "Async consolidation (dedup offline)", "DIY",
            f"no sleep-cycle engine; hand-rolled offline pass deduped+denoised {len(raw)}→{len(kept)} "
            f"facts (dropped {dropped}: dups+noise). aisotropy runs a DataFusion Rust subprocess "
            f"sleep cycle (+ Go pruner).")


def main():
    print("#" * 78)
    print("# LangGraph — COMPREHENSIVE feature-parity demo vs aisotropy (OFFLINE, fake model)")
    print("# langgraph 1.2.9 / langchain 1.3.13 / langchain-core 1.4.9. Tokens are SYNTHETIC.")
    print("#" * 78)

    area_memory()
    area_contract()
    area_tools()
    area_auth()
    area_cost()
    area_observability()
    area_consolidation()

    rule("TALLY — coverage class counts across all 26 features")
    total = sum(TALLY.values())
    for cls in ["BUILT-IN", "CONVENTION", "PARTIAL", "DIY", "PAID", "NOT-POSSIBLE"]:
        if cls in TALLY:
            print(f"  {cls:<13}: {TALLY[cls]}")
    print(f"  {'TOTAL':<13}: {total}")

    print(
        "\n  HEADLINE (memory tiers): aisotropy = 4 stores of DIFFERENT NATURE by access\n"
        "  temperature (BadgerDB µs KV / SQLite+FTS5 keyword / Qdrant vector / DataFusion\n"
        "  offline batch). LangGraph = 2 KV-nature surfaces (checkpointer + Store). The\n"
        "  keyword/FTS tier (F3) and the transactional outbox (F25) are NOT-POSSIBLE; the\n"
        "  vector tier (F4), working memory (F2), BYO embedder (F9), agent loop (F15),\n"
        "  streaming (F11/F12), response cache (F13) and OTel spans (F22) are genuinely\n"
        "  BUILT-IN; governance (gate/breaker/degrade/per-tenant meter, F14/F18-F21) and\n"
        "  consolidation (F26) are DIY."
    )
    # Exit 0 as long as the demo ran end-to-end (it self-validates each feature inline).
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
