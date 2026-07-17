"""Real, OpenAI-compatible /embeddings server for the live arena.

Backed by the REAL `intfloat/multilingual-e5-small` model (384-dim) — the same
embedding model aisotropy uses (see internal/semantic, ADR-0049/0054). No stub,
no mock: sentence-transformers loads the actual weights and returns real vectors.

Why this exists
---------------
The shared chat model for the arena is agnes-2.0-flash (OpenAI-compatible chat),
but agnes exposes NO /embeddings endpoint. Every framework adapter that wants to
do real RAG needs a real embedder. This process is that embedder, spoken in the
OpenAI embeddings dialect so any OpenAI-compatible client can point at it by just
changing base_url.

The e5 prefix contract (important — do not skip)
------------------------------------------------
e5 models are trained with instruction prefixes and degrade noticeably without
them. The convention (matching aisotropy's Go wiring, ADR-0054):

    "query: "    -> for SEARCH QUERIES (the thing you embed to search with)
    "passage: "  -> for STORED DOCUMENTS (the corpus you index)

A search only works when BOTH sides used their correct prefix. This server never
guesses silently in a way that hides mistakes; it resolves the role in this order:

    1. explicit request field  "input_type": "query" | "passage" | "document"
       ("document" is treated as "passage")
    2. model-id suffix         "...-query"  -> query,  "...-passage" -> passage
    3. default                 -> "query"   (and logs a warning to STDERR)

So an adapter has two ergonomic ways to pick the role:
    - POST {"model": "multilingual-e5-small-passage", "input": [...]}  (index side)
    - POST {"model": "multilingual-e5-small", "input": "...", "input_type": "query"}

Endpoints
---------
    POST /embeddings   OpenAI-compatible: {"model","input"[, "input_type"]}
                       -> {"object":"list","data":[{"embedding":[...384...]}],
                           "model":..., "usage":{...}}
    GET  /health       {"status":"ok","model":...,"dim":384}
    GET  /             same as /health (liveness convenience)

License: sentence-transformers (Apache-2.0), fastapi (MIT), uvicorn (BSD),
pydantic (MIT), torch (BSD-3). All permissive — passes the repo license gate.
"""

from __future__ import annotations

import logging
import os
import sys
from typing import List, Optional, Union

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer

MODEL_ID = os.environ.get("ARENA_EMBED_MODEL", "intfloat/multilingual-e5-small")
EXPECTED_DIM = 384
QUERY_PREFIX = "query: "
PASSAGE_PREFIX = "passage: "

logging.basicConfig(
    stream=sys.stderr,
    level=logging.INFO,
    format="PROGRESS: embed-server %(levelname)s %(message)s",
)
log = logging.getLogger("embed-server")

log.info("loading model %s (first run downloads weights, ~120MB)...", MODEL_ID)
_model = SentenceTransformer(MODEL_ID)
_dim = _model.get_sentence_embedding_dimension()
if _dim != EXPECTED_DIM:
    log.warning("model dim %d != expected %d", _dim, EXPECTED_DIM)
log.info("model loaded: dim=%d", _dim)

app = FastAPI(title="arena e5 embeddings", version="1.0.0")


class EmbeddingsRequest(BaseModel):
    input: Union[str, List[str]]
    model: str = "multilingual-e5-small"
    # Non-standard but harmless extension: lets a caller pick the e5 role
    # without swapping the model id. OpenAI clients that don't send it are fine.
    input_type: Optional[str] = None
    # Accepted and ignored (OpenAI clients may send it); we always return floats.
    encoding_format: Optional[str] = None


def _resolve_prefix(model: str, input_type: Optional[str]) -> str:
    """Resolve the e5 instruction prefix. See module docstring for precedence."""
    if input_type:
        it = input_type.strip().lower()
        if it == "query":
            return QUERY_PREFIX
        if it in ("passage", "document", "doc"):
            return PASSAGE_PREFIX
        raise HTTPException(
            status_code=400,
            detail=f"invalid input_type {input_type!r}; want query|passage|document",
        )
    m = model.strip().lower()
    if m.endswith("-query"):
        return QUERY_PREFIX
    if m.endswith("-passage") or m.endswith("-document"):
        return PASSAGE_PREFIX
    log.warning(
        "no input_type and model id %r has no -query/-passage suffix; "
        "defaulting to 'query:' prefix. Index the corpus with 'passage:'.",
        model,
    )
    return QUERY_PREFIX


@app.get("/")
@app.get("/health")
def health():
    return {"status": "ok", "model": MODEL_ID, "dim": _dim}


@app.post("/embeddings")
@app.post("/v1/embeddings")
def embeddings(req: EmbeddingsRequest):
    texts = [req.input] if isinstance(req.input, str) else list(req.input)
    if not texts:
        raise HTTPException(status_code=400, detail="input must be non-empty")
    prefix = _resolve_prefix(req.model, req.input_type)
    prefixed = [prefix + t for t in texts]

    # normalize_embeddings=True -> unit vectors, so Qdrant Cosine and dot agree.
    vectors = _model.encode(
        prefixed, normalize_embeddings=True, convert_to_numpy=True
    )
    data = [
        {"object": "embedding", "index": i, "embedding": vec.tolist()}
        for i, vec in enumerate(vectors)
    ]
    # e5 has no public tokenizer usage contract like OpenAI; report a real,
    # honest proxy: whitespace token count of the prefixed inputs. Never faked.
    prompt_tokens = sum(len(t.split()) for t in prefixed)
    return {
        "object": "list",
        "data": data,
        "model": req.model,
        "usage": {"prompt_tokens": prompt_tokens, "total_tokens": prompt_tokens},
    }
