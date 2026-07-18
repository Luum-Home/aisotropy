"""Shared helpers for the Pydantic AI exhaustive multi-mode live-arena adapter.

Centralizes env/config reading, the agnes chat-model builder, and the real
e5-embedding + Qdrant retrieval helpers so every mode in `adapter.py` shares
one source of truth. Hosts are ENV-CONFIGURABLE so a Docker container can
override them to reach the host machine (`host.docker.internal`).

Never logs credential values -- only presence/absence and non-secret
metadata (byte counts, hit counts, latencies).
"""

from __future__ import annotations

import os
import sys


def log(msg: str) -> None:
    print(f"PROGRESS: {msg}", file=sys.stderr, flush=True)


class ConfigError(RuntimeError):
    """Missing required env config. The caller turns this into the result JSON's `error`."""


_STUB_VECTOR_DIM = 8  # arbitrary, offline-only -- no real embedding model backs this


def is_stub() -> bool:
    """True when `ARENA_STUB=1` is set in the environment.

    When true, every mode swaps its real dependencies for offline, in-process substitutes:
    `build_model()` returns Pydantic AI's own `pydantic_ai.models.test.TestModel` instead of the
    real agnes `OpenAIChatModel`; `embed_query_vector()` / `qdrant_search()` below return a fixed
    vector / canned passages instead of calling the real e5 server / Qdrant; and
    `arena_memory_store.py`'s persistence functions fall back to an in-process dict instead of
    calling rqlite. No `OPENAI_*` / `EMBED_URL` / `QDRANT_URL` / `RQLITE_URL` env is required and
    no network I/O happens for any mode.
    """
    return os.environ.get("ARENA_STUB", "") == "1"


def chat_env() -> tuple[str, str, str]:
    """Return (model_name, base_url, api_key) for the agnes chat model.

    Raises ConfigError (never prints the key) if any of the three is unset.
    """
    model_name = os.environ.get("OPENAI_MODEL", "")
    base_url = os.environ.get("OPENAI_BASE_URL", "")
    api_key = os.environ.get("OPENAI_API_KEY", "")
    if not model_name or not base_url or not api_key:
        raise ConfigError(
            "Missing one of OPENAI_MODEL / OPENAI_BASE_URL / OPENAI_API_KEY in environment"
        )
    return model_name, base_url, api_key


def current_model_name() -> str:
    """Best-effort model name for error reporting, even before chat_env() validates."""
    return os.environ.get("OPENAI_MODEL", "unknown")


def embed_base_url() -> str:
    return os.environ.get("EMBED_URL", "http://127.0.0.1:8899").rstrip("/")


def qdrant_base_url() -> str:
    return os.environ.get("QDRANT_URL", "http://localhost:6333").rstrip("/")


def rqlite_base_url() -> str:
    return os.environ.get("RQLITE_URL", "http://localhost:4001").rstrip("/")


def build_model():
    """Construct the chat model Pydantic AI's `Agent` runs against.

    Real agnes (`OpenAIChatModel` over an OpenAI-compatible endpoint) by default. When
    `ARENA_STUB=1` (`is_stub()`), returns Pydantic AI's own offline test double instead --
    `pydantic_ai.models.test.TestModel` (`pydantic_ai/models/test.py`): a framework-native model
    implementation built for exactly this purpose (no network I/O, deterministic-shape
    responses -- auto-calls any tools present, then a plain/tool-schema-shaped final answer). Not
    a hand-rolled fake; it ships inside `pydantic-ai-slim` itself. No `OPENAI_*` env is required
    in stub mode.

    Returns (model, model_name). Raises ConfigError if env is incomplete (only in non-stub mode).
    """
    if is_stub():
        from pydantic_ai.models.test import TestModel

        model_name = "pydantic_ai.models.test.TestModel (ARENA_STUB=1)"
        log("build_model: ARENA_STUB=1 -- using pydantic_ai.models.test.TestModel, no network call")
        return TestModel(), model_name

    from pydantic_ai.models.openai import OpenAIChatModel
    from pydantic_ai.providers.openai import OpenAIProvider

    model_name, base_url, api_key = chat_env()
    model = OpenAIChatModel(model_name, provider=OpenAIProvider(base_url=base_url, api_key=api_key))
    return model, model_name


def embed_query_vector(text: str) -> list[float]:
    """Embed a search query through the REAL e5 server using the `query:` role.

    Uses the OpenAI-compatible embeddings client (per the task brief) pointed
    at EMBED_URL. The e5 server does not validate the API key, so a
    placeholder satisfies the client's local non-empty check without ever
    touching the real agnes credential.

    When ARENA_STUB=1, returns a fixed in-process vector instead -- no e5 call, no network I/O.
    The vector's values carry no semantic meaning; it only exists to give `qdrant_search()` a
    same-shaped argument to accept in stub mode too.
    """
    if is_stub():
        log("embed_query_vector: ARENA_STUB=1 -- returning a fixed in-process vector, no e5 call")
        return [0.1] * _STUB_VECTOR_DIM

    import openai

    client = openai.OpenAI(base_url=f"{embed_base_url()}/v1", api_key="arena-embed-no-auth-required")
    resp = client.embeddings.create(model="multilingual-e5-small-query", input=text)
    return list(resp.data[0].embedding)


def qdrant_search(vector: list[float], top_k: int = 3, collection: str = "arena_rag") -> list[dict]:
    """Real top-k retrieval against the REAL Qdrant `arena_rag` collection via qdrant-client.

    Returns a list of {doc_id, title, text, score} dicts, highest score first.

    When ARENA_STUB=1, returns a small canned passage list instead -- no Qdrant call, no network
    I/O. Content is a labeled stub, never mistakable for a real retrieved passage.
    """
    if is_stub():
        log(f"qdrant_search: ARENA_STUB=1 -- returning {top_k} canned passages, no Qdrant call")
        return [
            {
                "doc_id": f"stub-doc-{i}",
                "title": f"ARENA_STUB canned passage {i}",
                "text": "Offline stub passage -- no real Qdrant retrieval occurred (ARENA_STUB=1).",
                "score": round(1.0 - i * 0.1, 3),
            }
            for i in range(top_k)
        ]

    from qdrant_client import QdrantClient

    client = QdrantClient(url=qdrant_base_url())
    hits = client.query_points(
        collection_name=collection, query=vector, limit=top_k, with_payload=True
    ).points
    return [
        {
            "doc_id": h.payload.get("doc_id"),
            "title": h.payload.get("title"),
            "text": h.payload.get("text"),
            "score": h.score,
        }
        for h in hits
    ]
