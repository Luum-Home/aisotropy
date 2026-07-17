#!/usr/bin/env python3
"""Populate the REAL Qdrant `arena_rag` collection from the corpus.

Pipeline (all real, no stubs):
  corpus/*.md  --(embed server, "passage:" prefix)-->  384-dim vectors
               --(Qdrant REST upsert)-->  collection `arena_rag`

- Creates `arena_rag` (size 384, distance Cosine) if it does not exist.
- Embeds each corpus doc through the running embed server (127.0.0.1:8899)
  using the PASSAGE role (model id `...-passage`), the correct e5 side for
  stored documents.
- Stores payload {doc_id, title, text} per point so a search returns the
  human-readable source, not just an id.

Only stdlib (urllib) — runnable with any Python 3, no venv required.

Usage:
  python3 populate-qdrant.py
Env overrides:
  QDRANT_URL     (default http://localhost:6333)
  EMBED_URL      (default http://127.0.0.1:8899)
  ARENA_COLLECTION (default arena_rag)
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
COLLECTION = os.environ.get("ARENA_COLLECTION", "arena_rag")
DIM = 384
HERE = os.path.dirname(os.path.abspath(__file__))
CORPUS_DIR = os.path.join(HERE, "corpus")


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


def main() -> int:
    docs = sorted(glob.glob(os.path.join(CORPUS_DIR, "*.md")))
    if not docs:
        print(f"PROGRESS: no corpus docs in {CORPUS_DIR}", file=sys.stderr)
        return 1
    print(f"PROGRESS: found {len(docs)} corpus docs", file=sys.stderr)

    # Recreate the collection (idempotent): delete then create with the right
    # vector params. We only touch `arena_rag`, never existing collections.
    _req("DELETE", f"{QDRANT_URL}/collections/{COLLECTION}")
    status, resp = _req(
        "PUT",
        f"{QDRANT_URL}/collections/{COLLECTION}",
        {"vectors": {"size": DIM, "distance": "Cosine"}},
    )
    if status not in (200, 201):
        print(f"PROGRESS: create collection failed {status}: {resp}", file=sys.stderr)
        return 1
    print(f"PROGRESS: collection {COLLECTION} ready (size={DIM}, Cosine)", file=sys.stderr)

    points = []
    for i, path in enumerate(docs, start=1):
        title, text = parse_doc(path)
        vec = embed_passage(text)
        points.append(
            {
                "id": i,
                "vector": vec,
                "payload": {
                    "doc_id": os.path.basename(path),
                    "title": title,
                    "text": text,
                },
            }
        )
        print(f"PROGRESS: embedded [{i}/{len(docs)}] {os.path.basename(path)}", file=sys.stderr)

    status, resp = _req(
        "PUT",
        f"{QDRANT_URL}/collections/{COLLECTION}/points?wait=true",
        {"points": points},
    )
    if status not in (200, 201):
        print(f"PROGRESS: upsert failed {status}: {resp}", file=sys.stderr)
        return 1

    status, resp = _req("GET", f"{QDRANT_URL}/collections/{COLLECTION}")
    count = resp.get("result", {}).get("points_count")
    print(f"PROGRESS: upsert done, points_count={count}", file=sys.stderr)
    print(json.dumps({"collection": COLLECTION, "points_count": count, "dim": DIM}))
    return 0 if count == len(docs) else 2


if __name__ == "__main__":
    sys.exit(main())
