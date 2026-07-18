#!/usr/bin/env python3
"""Populate the REAL Qdrant RAG collection(s) from the corpus.

Pipeline (all real, no stubs):
  corpus/*.md  --(embed server, "passage:" prefix)-->  384-dim vectors
               --(Qdrant REST upsert)-->  one collection per target

- Creates each target collection (size 384, distance Cosine) if it does not
  exist (recreated idempotently — delete then create).
- Embeds each corpus doc ONCE through the running embed server
  (127.0.0.1:8899) using the PASSAGE role (model id `...-passage`), the
  correct e5 side for stored documents, then reuses the same embeddings for
  every target collection (embedding the whole corpus once per collection
  would be N times the real embed-server cost for identical vectors).
- Stores payload {doc_id, title, text} per point so a search returns the
  human-readable source, not just an id.

COLLISION FIX (arenas/live/runner/main.go frameworkCollectionEnv): the live
arena runner sets a UNIQUE QDRANT_COLLECTION per framework
(arena_rag_<framework>) so multiple adapters sharing one real Qdrant
instance never read/write each other's vectors — 6 adapters previously
defaulted to the SAME literal "arena_rag" collection. This script's default
mode (no ARENA_COLLECTION/QDRANT_COLLECTION override) auto-discovers every
adapter under arenas/live/*/run.sh (excluding the aisotropy reference
adapter, which self-ingests the corpus into its OWN tiered memory at
runtime — see arenas/live/aisotropy/README.md "rag" mode) and seeds
arena_rag_<framework> for each, so the per-framework collections the runner
now points adapters at are actually populated, not silently empty.

Only stdlib (urllib) — runnable with any Python 3, no venv required.

Usage:
  python3 populate-qdrant.py                    # seed arena_rag_<fw> for every discovered adapter
  ARENA_COLLECTION=arena_rag python3 populate-qdrant.py   # seed exactly ONE named collection (legacy/manual/smoke-test mode)
Env overrides:
  QDRANT_URL       (default http://localhost:6333)
  EMBED_URL        (default http://127.0.0.1:8899)
  ARENA_COLLECTION / QDRANT_COLLECTION  (if EITHER is set: seed ONLY that one collection,
                                          exact single-collection legacy behavior)
"""
from __future__ import annotations

import glob
import json
import os
import sys
import urllib.request
import urllib.error

QDRANT_URL = os.environ.get("QDRANT_URL", "http://localhost:6333").rstrip("/")
EMBED_URL = os.environ.get("EMBED_URL", "http://127.0.0.1:8899").rstrip("/")
DIM = 384
HERE = os.path.dirname(os.path.abspath(__file__))
CORPUS_DIR = os.path.join(HERE, "corpus")
LIVE_DIR = os.path.dirname(HERE)  # arenas/live

# Adapter directories that are NOT frameworks under test — never treated as a
# seed target even if a stray run.sh appeared there.
_NON_ADAPTER_DIRS = {"runner", "tasks", "_shared"}
# aisotropy self-ingests the corpus into its OWN tiered memory at runtime
# (POST /v1/.../knowledge) rather than reading a pre-populated Qdrant
# collection like every other adapter — seeding a collection for it here
# would be inert (never read) and the runner's frameworkCollectionEnv
# already excludes it from the per-framework override for the same reason.
_SELF_INGESTING_ADAPTERS = {"aisotropy"}


def _req(method: str, url: str, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("content-type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")


def embed_passage(text: str) -> list:
    status, resp = _req(
        "POST",
        f"{EMBED_URL}/embeddings",
        {"model": "multilingual-e5-small-passage", "input": text},
    )
    if status != 200:
        raise RuntimeError(f"embed failed {status}: {resp}")
    vec = resp["data"][0]["embedding"]
    if len(vec) != DIM:
        raise RuntimeError(f"embed dim {len(vec)} != {DIM}")
    return vec


def parse_doc(path: str):
    with open(path, encoding="utf-8") as f:
        content = f.read().strip()
    lines = content.splitlines()
    title = lines[0].lstrip("# ").strip() if lines else os.path.basename(path)
    return title, content


def discover_adapter_names() -> list[str]:
    """Every arenas/live/<fw>/run.sh directory name, excluding non-adapter
    dirs and self-ingesting adapters (see _SELF_INGESTING_ADAPTERS)."""
    names = []
    for run_sh in sorted(glob.glob(os.path.join(LIVE_DIR, "*", "run.sh"))):
        name = os.path.basename(os.path.dirname(run_sh))
        if name in _NON_ADAPTER_DIRS or name in _SELF_INGESTING_ADAPTERS:
            continue
        names.append(name)
    return names


def target_collections() -> list[str]:
    """Resolve which collection(s) to seed this run.

    Explicit override (ARENA_COLLECTION or QDRANT_COLLECTION set) => seed
    exactly that one collection — the legacy single-collection behavior,
    still what manual smoke-testing (smoke-rag.py) and CI want. No override
    => seed arena_rag_<fw> for every discovered adapter, matching what the
    runner's frameworkCollectionEnv actually points each adapter at.
    """
    override = os.environ.get("ARENA_COLLECTION") or os.environ.get("QDRANT_COLLECTION")
    if override:
        return [override]
    names = discover_adapter_names()
    if not names:
        # No adapters discovered (e.g. run from an unexpected cwd) — fall
        # back to the legacy single default so the script still does
        # something useful rather than silently seeding nothing.
        return ["arena_rag"]
    return [f"arena_rag_{name.replace('-', '_')}" for name in names]


def load_corpus_points() -> list[dict]:
    """Embed every corpus doc ONCE, returning point dicts (without an id —
    the caller assigns ids per collection so multiple collections can be
    upserted from the same embedded payload)."""
    docs = sorted(glob.glob(os.path.join(CORPUS_DIR, "*.md")))
    if not docs:
        raise RuntimeError(f"no corpus docs in {CORPUS_DIR}")
    print(f"PROGRESS: found {len(docs)} corpus docs", file=sys.stderr)
    points = []
    for i, path in enumerate(docs, start=1):
        title, text = parse_doc(path)
        vec = embed_passage(text)
        points.append(
            {
                "vector": vec,
                "payload": {
                    "doc_id": os.path.basename(path),
                    "title": title,
                    "text": text,
                },
            }
        )
        print(f"PROGRESS: embedded [{i}/{len(docs)}] {os.path.basename(path)}", file=sys.stderr)
    return points


def seed_collection(collection: str, base_points: list[dict]) -> tuple[bool, int]:
    """Create (idempotently) and upsert one collection from pre-embedded
    points. Returns (ok, points_count)."""
    # Recreate the collection (idempotent): delete then create with the
    # right vector params. Only THIS collection is touched, never others.
    _req("DELETE", f"{QDRANT_URL}/collections/{collection}")
    status, resp = _req(
        "PUT",
        f"{QDRANT_URL}/collections/{collection}",
        {"vectors": {"size": DIM, "distance": "Cosine"}},
    )
    if status not in (200, 201):
        print(f"PROGRESS: create collection {collection} failed {status}: {resp}", file=sys.stderr)
        return False, 0
    print(f"PROGRESS: collection {collection} ready (size={DIM}, Cosine)", file=sys.stderr)

    points = [{"id": i, "vector": p["vector"], "payload": p["payload"]} for i, p in enumerate(base_points, start=1)]
    status, resp = _req(
        "PUT",
        f"{QDRANT_URL}/collections/{collection}/points?wait=true",
        {"points": points},
    )
    if status not in (200, 201):
        print(f"PROGRESS: upsert to {collection} failed {status}: {resp}", file=sys.stderr)
        return False, 0

    status, resp = _req("GET", f"{QDRANT_URL}/collections/{collection}")
    count = resp.get("result", {}).get("points_count")
    print(f"PROGRESS: {collection} upsert done, points_count={count}", file=sys.stderr)
    return count == len(points), (count or 0)


def main() -> int:
    collections = target_collections()
    print(f"PROGRESS: target collection(s): {', '.join(collections)}", file=sys.stderr)

    try:
        base_points = load_corpus_points()
    except RuntimeError as e:
        print(f"PROGRESS: {e}", file=sys.stderr)
        return 1

    results = {}
    all_ok = True
    for coll in collections:
        ok, count = seed_collection(coll, base_points)
        results[coll] = {"points_count": count, "ok": ok}
        all_ok = all_ok and ok

    print(json.dumps({"collections": results, "dim": DIM}))
    return 0 if all_ok else 2


if __name__ == "__main__":
    sys.exit(main())
