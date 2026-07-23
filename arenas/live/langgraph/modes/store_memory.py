"""User-scoped long-term memory over LangGraph's OWN ``BaseStore`` primitive —
the fair, idiomatic engine for the four memory-extension / tenancy dimensions
(``memory_cross_session``, ``memory_semantic_recall``, ``memory_consolidation``,
``isolation_forgetful``).

WHY THIS MODULE EXISTS (fairness). ``modes.memory`` proves LangGraph's
``SqliteSaver`` *checkpointer* persists a thread across processes, but it keys
purely by ``thread_id`` == ``session_id`` — a session-keyed transcript store.
The memory-extension dimensions (docs/architecture/arena-memory-extensions.md)
VARY the ``session_id`` while HOLDING ``user_id`` constant across invocations,
which defeats a session-keyed store by construction: session B is fresh, so a
thread checkpointer finds nothing. Only genuine *user-scoped* memory recalls the
fact. Rather than hand-roll such a store (the strawman the fairness protocol —
docs/architecture/arena-fairness-protocol.md — names as debt #1), this module
wires LangGraph's OWN official long-term-memory primitive:

  * ``langgraph.store.base.BaseStore`` — NATIVE. ``store.put((user_id,), key,
    value)`` / ``store.search((user_id,), query=...)``. Namespace tuples are
    LangGraph's hierarchical user-scoping mechanism; the ``index`` config with
    an embedder makes ``search`` a real semantic (vector) retrieval.
    Real path: ``langgraph.store.sqlite.SqliteStore`` (on-disk, survives the
    process boundary — the whole point). Stub path: ``langgraph.store.memory
    .InMemoryStore`` (LangGraph's own real in-memory store; per-process, so it
    proves the code path runs offline, not cross-process persistence — exactly
    the same honesty caveat ``common.checkpointer_context`` already carries).
    https://langchain-ai.github.io/langgraph/concepts/memory/#long-term-memory
    https://blog.langchain.com/semantic-search-for-langgraph-memory/

  * ``langmem.create_memory_store_manager`` — OFFICIAL-EXTENSION (langmem SDK).
    A ``MemoryStoreManager`` invoked SYNCHRONOUSLY (``.invoke({"messages": …})``)
    on the consolidation directive, distilling the conversation into the same
    store namespace. https://langchain-ai.github.io/langmem/
    Fallback (documented compromise): if langmem raises in-process — under the
    offline stub the ``FakeListChatModel`` cannot drive langmem's trustcall
    structured extraction, and a real run could hit a transient extraction
    failure — the raw turns written on the persist invocation are ALREADY in the
    store, so recall still works off the store's own write path. That fallback
    is reported in the reply text, never silently swallowed.

The e5 embedder is the SAME shared embed server ``modes.rag`` uses
(``OpenAIEmbeddings`` against ``EMBED_URL``, ``multilingual-e5-small-query``) —
a like-for-like semantic comparison with every other RAG/semantic cell.

ISOLATION (``isolation_forgetful``). ``run_forgetful`` performs the omitted-scope
read at ``BaseStore``'s OWN API boundary: it calls ``store.search((user_id,),
…)`` with ``user_id`` forwarded RAW (blank ""), never backfilling a default.
Whatever ``BaseStore`` returns is LangGraph's verdict, not adapter glue — the
fairness constraint docs/architecture/arena-tenancy-levels.md §5 mandates.
"""
from __future__ import annotations

import contextlib
import hashlib
import os
import time
import traceback
import uuid
from typing import Any

from . import common

MODE = "memory"
FORGETFUL_MODE = "isolation_forgetful"

# On-disk BaseStore file for the real cross-process path. A SEPARATE file from
# modes.memory's checkpointer (`.data/checkpoints.sqlite`) and the hand-rolled
# ScopedStore (`.data/scoped_store.sqlite`): this is LangGraph's BaseStore, a
# distinct primitive. Overridable so a per-RUN data root can be injected by the
# harness (see the cross-run-contamination caveat in README.md).
DEFAULT_STORE_DB = os.environ.get(
    "STORE_DB_PATH", os.path.join(common.ADAPTER_DIR, ".data", "store_memory.sqlite")
)

# multilingual-e5-small is 384-dim (same model modes.rag embeds queries with).
# Overridable via env so a wrong guess is a config fix, not a code change; the
# real semantic-recall quality is a real-run MUST-CHECK regardless.
E5_DIMS = int(os.environ.get("EMBED_DIMS", "384"))

# How many memories to retrieve and inject on a recall turn. Small on purpose:
# injecting only the top-k (not the whole persisted transcript) is the efficient
# semantic-recall behavior the design rewards in the token table.
SEARCH_LIMIT = int(os.environ.get("MEMORY_SEARCH_LIMIT", "5"))

# Stub embedder dimensionality — small, deterministic, offline.
_STUB_DIMS = 64


def _stub_embed(texts: list[str]) -> list[list[float]]:
    """Deterministic hash-to-vector fake embedder (ARENA_STUB, offline).

    NOT semantic: it maps text to a fixed vector by SHA-256 so the real
    ``BaseStore`` index/search machinery runs with zero network. Genuine
    semantic recall (a paraphrase with no shared tokens matching the needle)
    needs the real e5 embedder — a real-run MUST-CHECK, never claimed offline.
    """
    out: list[list[float]] = []
    for t in texts:
        digest = hashlib.sha256((t or "").encode("utf-8")).digest()
        # Repeat the 32-byte digest to fill _STUB_DIMS, normalize to [0,1].
        raw = (digest * ((_STUB_DIMS // len(digest)) + 1))[:_STUB_DIMS]
        out.append([b / 255.0 for b in raw])
    return out


def _semantic_index() -> dict[str, Any]:
    """BaseStore ``IndexConfig`` enabling semantic ``search``.

    Real: ``langchain_openai.OpenAIEmbeddings`` against the shared e5 embed
    server — the exact wiring ``modes.rag`` uses. Stub: ``_stub_embed``.
    """
    if common.is_stub():
        return {"dims": _STUB_DIMS, "embed": _stub_embed, "fields": ["text"]}
    from langchain_openai import OpenAIEmbeddings

    embeddings = OpenAIEmbeddings(
        base_url=common.DEFAULT_EMBED_URL,
        api_key=common.DEFAULT_EMBED_API_KEY,
        model=common.DEFAULT_EMBED_MODEL_QUERY,
        check_embedding_ctx_length=False,
    )
    return {"dims": E5_DIMS, "embed": embeddings, "fields": ["text"]}


@contextlib.contextmanager
def _store_context(db_path: str, index: dict[str, Any]):
    """Yield LangGraph's ``BaseStore``.

    Real: ``SqliteStore`` (on-disk ``db_path``, survives the process boundary —
    required because persist and recall are SEPARATE processes). Stub:
    ``InMemoryStore`` (per-process; proves the path runs offline, NOT
    cross-process persistence — same caveat as ``checkpointer_context``).

    Evidence seam: ``MEMORY_STORE_ONDISK=1`` forces the on-disk ``SqliteStore``
    even under ``ARENA_STUB=1`` (keeping the stub MODEL). Off by default; it
    exists ONLY so an OFFLINE run can demonstrate that BaseStore's cross-process
    persist→recall machinery actually works (with the hash embedder), isolating
    the "needs the real e5 embedder" caveat to SEMANTIC quality alone. The arena
    stub lane never sets it, so default stub behavior is unchanged.
    """
    force_ondisk = os.environ.get("MEMORY_STORE_ONDISK", "").strip().lower() in ("1", "true", "yes")
    if common.is_stub() and not force_ondisk:
        from langgraph.store.memory import InMemoryStore

        yield InMemoryStore(index=index)
        return

    from langgraph.store.sqlite import SqliteStore

    os.makedirs(os.path.dirname(db_path) or ".", exist_ok=True)
    with SqliteStore.from_conn_string(db_path, index=index) as store:
        store.setup()
        yield store


def _is_consolidate_directive(user_texts: list[str]) -> bool:
    """True when this turn is the arena's memory_consolidation directive.

    The runner inserts a single directive turn between persist and recall
    (arenas/live/runner/tasks.go ``consolidateDirective``) whose intent is
    "consolidate and durably remember". Matching the distinctive word
    "consolidate" on a lone-turn invocation routes to the langmem path; a normal
    persist/recall turn (which never contains that word in this suite) does not.
    Coupled to the directive's INTENT, not its exact string — documented here so
    the coupling is visible, not hidden.
    """
    if len(user_texts) != 1:
        return False
    return "consolidate" in user_texts[0].lower()


def _collect_texts(payload: dict[str, Any]) -> list[str]:
    """Non-empty user/assistant message contents to persist as memory items."""
    texts: list[str] = []
    system = payload.get("system")
    if system:
        texts.append(str(system))
    for m in payload.get("messages", []) or []:
        content = m.get("content", "")
        if content:
            texts.append(str(content))
    return texts


def run(payload: dict[str, Any], *, user_id: str) -> dict[str, Any]:
    """User-scoped memory turn over ``BaseStore`` (namespace ``(user_id,)``).

    Every turn: (1) semantic-``search`` the user's namespace for memories
    relevant to the incoming turn and inject them, (2) call the model, (3)
    ``put`` this turn's content into the namespace so a LATER session (different
    ``session_id``, same ``user_id``) recalls it. The consolidation directive
    additionally runs langmem synchronously.
    """
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    namespace = (user_id,)
    query = common.last_user_content(payload)
    user_texts = [
        m.get("content", "")
        for m in payload.get("messages", []) or []
        if m.get("role") == "user"
    ]
    db_path = payload.get("store_db_path") or DEFAULT_STORE_DB
    index = _semantic_index()

    try:
        from langchain_core.messages import HumanMessage, SystemMessage
    except Exception as exc:  # pragma: no cover - import failure path
        return common.envelope(mode=MODE, model=model_name, error=f"import failure: {exc}")

    try:
        with _store_context(db_path, index) as store:
            # memory_consolidation directive: distill the conversation so far via
            # langmem (official primitive), synchronously, into the same namespace.
            if _is_consolidate_directive(user_texts):
                detail = _consolidate(store, namespace, model_name, api_key, base_url)
                return common.envelope(mode=MODE, model=model_name, reply=detail)

            start = time.monotonic()

            # (1) Retrieve relevant user-scoped memories at BaseStore's boundary.
            memories: list[str] = []
            if query:
                try:
                    hits = store.search(namespace, query=query, limit=SEARCH_LIMIT)
                    memories = [
                        str(h.value.get("text", "")) for h in hits if h.value.get("text")
                    ]
                except Exception as exc:  # search must never crash the turn
                    common.log(f"BaseStore.search failed (non-fatal): {exc}")

            # (2) Call the model with retrieved memories injected.
            llm = common.build_chat_model(model_name, api_key, base_url)
            sys_parts = []
            caller_system = (payload.get("system") or "").strip()
            if caller_system:
                sys_parts.append(caller_system)
            if memories:
                sys_parts.append(
                    "Relevant facts you durably remember about this user from earlier "
                    "sessions:\n" + "\n".join(f"- {m}" for m in memories)
                )
            lc_messages: list[Any] = []
            if sys_parts:
                lc_messages.append(SystemMessage(content="\n\n".join(sys_parts)))
            for m in payload.get("messages", []) or []:
                role = m.get("role")
                content = m.get("content", "")
                if role == "user":
                    lc_messages.append(HumanMessage(content=content))
                elif role == "assistant":
                    from langchain_core.messages import AIMessage

                    lc_messages.append(AIMessage(content=content))
            response = llm.invoke(lc_messages)
            reply = response.content if isinstance(response.content, str) else str(response.content)
            input_tokens, output_tokens = common.extract_usage(response)

            # (3) Persist this turn's content as user-scoped memory items so a
            # later session recalls it. put() carries index=["text"] so the
            # value is semantically searchable.
            for text in _collect_texts(payload):
                store.put(namespace, uuid.uuid4().hex, {"text": text}, index=["text"])

            latency_ms = int((time.monotonic() - start) * 1000)
            return common.envelope(
                mode=MODE,
                model=model_name,
                reply=reply,
                input_tokens=input_tokens,
                output_tokens=output_tokens,
                latency_ms=latency_ms,
            )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")


def _consolidate(
    store: Any, namespace: tuple[str, ...], model_name: str, api_key: str | None, base_url: str | None
) -> str:
    """Run langmem's memory manager synchronously over the accumulated raw turns.

    Reads the raw memory items already persisted under ``namespace`` (written by
    the persist invocation), feeds them to ``MemoryStoreManager.invoke`` as a
    message trajectory, and lets langmem distill/consolidate them back into the
    same store. Returns a human-readable detail string for the reply.

    Compromise (documented, per task): if langmem cannot complete in-process —
    the offline stub's ``FakeListChatModel`` cannot drive langmem's trustcall
    structured extraction — this falls back to the store's own write path (the
    raw items are already present, so recall still works) and SAYS SO.
    """
    try:
        raw_hits = store.search(namespace, limit=100)
        messages = [
            {"role": "user", "content": str(h.value.get("text", ""))}
            for h in raw_hits
            if h.value.get("text")
        ]
    except Exception as exc:
        return f"consolidate: could not read raw memories ({exc}); recall rides the store write path"

    if not messages:
        return "consolidate: no raw memories to consolidate; recall rides the store write path"

    try:
        from langmem import create_memory_store_manager

        model = common.build_chat_model(model_name, api_key, base_url)
        # langmem gets its OWN sub-namespace, DISJOINT from the raw (user_id,)
        # items: its MemoryStoreManager reconciles against existing memories in
        # its namespace and expects THEM to carry its own schema ("kind"/
        # "content"). Pointing it at the raw {"text": …} items raises
        # KeyError('kind') on the REAL model too — a wiring bug, not a stub
        # artifact. A recall search under the (user_id,) PREFIX still sees both
        # the raw items and this sub-namespace's distilled memories, so recall
        # is unaffected by the split.
        langmem_ns = namespace + ("langmem",)
        manager = create_memory_store_manager(model, namespace=langmem_ns, store=store)
        manager.invoke({"messages": messages})
        return "consolidate: langmem MemoryStoreManager.invoke completed synchronously"
    except Exception as exc:
        # Documented fallback — NOT a silent swallow. The raw turns persisted on
        # the persist invocation remain searchable, so recall still succeeds off
        # BaseStore's own write path; only the langmem distillation was skipped.
        common.log(f"langmem consolidation fell back to store write path: {exc}")
        return f"consolidate: langmem unavailable in-process ({type(exc).__name__}); fell back to BaseStore write path"


def run_forgetful(payload: dict[str, Any]) -> dict[str, Any]:
    """``isolation_forgetful`` — the omitted-scope read at BaseStore's boundary.

    The recall invocation arrives with ``user_id`` DELIBERATELY BLANK (the
    forgetful integrator mistake). This handler forwards that blank straight to
    ``BaseStore.search((user_id,), …)`` — never backfilling a default — so
    LangGraph's OWN store decides the outcome:

      * SqliteStore (real path): an empty-string namespace component behaves as a
        match-all prefix — the search RETURNS another user's memory → the model's
        reply carries the scoped fact → the runner scores it a LEAK (L1 breach).
      * InMemoryStore (stub path): the same call returns EMPTY → silent_partition.

    Either way LangGraph does NOT refuse (it is L1: namespaces are organizational,
    not access control), which is exactly what this dimension measures. The
    verdict is the FRAMEWORK's, produced at its API boundary, not adapter glue.
    """
    isolationForgetfulMode = FORGETFUL_MODE
    model_name, api_key, base_url, err = common.resolve_chat_env(isolationForgetfulMode)
    if err:
        return err

    user_id = payload.get("user_id")
    user_id = user_id if isinstance(user_id, str) else ""
    namespace = (user_id,)  # BLANK forwarded RAW — the forgetful mistake, verbatim.
    query = common.last_user_content(payload)
    db_path = payload.get("store_db_path") or DEFAULT_STORE_DB
    index = _semantic_index()

    try:
        from langchain_core.messages import HumanMessage, SystemMessage
    except Exception as exc:  # pragma: no cover
        return common.envelope(mode=isolationForgetfulMode, model=model_name, error=f"import failure: {exc}")

    try:
        with _store_context(db_path, index) as store:
            memories: list[str] = []
            if query:
                # THE omitted-scope read, at BaseStore's own API boundary.
                hits = store.search(namespace, query=query, limit=SEARCH_LIMIT)
                memories = [str(h.value.get("text", "")) for h in hits if h.value.get("text")]

            llm = common.build_chat_model(model_name, api_key, base_url)
            sys_text = ""
            if memories:
                sys_text = (
                    "Relevant facts you durably remember:\n"
                    + "\n".join(f"- {m}" for m in memories)
                )
            lc_messages: list[Any] = []
            if sys_text:
                lc_messages.append(SystemMessage(content=sys_text))
            lc_messages.append(HumanMessage(content=query))
            response = llm.invoke(lc_messages)
            reply = response.content if isinstance(response.content, str) else str(response.content)
            input_tokens, output_tokens = common.extract_usage(response)

            return common.envelope(
                mode=isolationForgetfulMode,
                model=model_name,
                reply=reply,
                input_tokens=input_tokens,
                output_tokens=output_tokens,
                scoped_recall={
                    "omitted_scope_hits": len(memories),
                    "namespace_searched": list(namespace),
                },
            )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=isolationForgetfulMode, model=model_name, error=f"{type(exc).__name__}: {exc}")
