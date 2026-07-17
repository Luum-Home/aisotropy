#!/usr/bin/env python3
"""End-to-end RAG retrieval smoke test (no LLM needed).

For each question in rag_questions.jsonl:
  embed the question ("query:" prefix) via the embed server
    -> Qdrant top-k search against arena_rag
    -> report the top-1 retrieved doc title
    -> for non-trap questions, PASS if a supporting_doc is in the top-k.

This proves the retrieval half of RAG works end to end against the REAL
embed server and REAL Qdrant. Only stdlib. Exit 0 iff every non-trap
question retrieves one of its supporting docs in the top-k.

Env: QDRANT_URL, EMBED_URL, ARENA_COLLECTION, TOPK (default 3)
"""
from __future__ import annotations

import json
import os
import sys
import urllib.request
import urllib.error

QDRANT_URL = os.environ.get("QDRANT_URL", "http://localhost:6333").rstrip("/")
EMBED_URL = os.environ.get("EMBED_URL", "http://127.0.0.1:8899").rstrip("/")
COLLECTION = os.environ.get("ARENA_COLLECTION", "arena_rag")
TOPK = int(os.environ.get("TOPK", "3"))
HERE = os.path.dirname(os.path.abspath(__file__))


def _req(method, url, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("content-type", "application/json")
    with urllib.request.urlopen(req, timeout=120) as r:
        raw = r.read().decode()
        return r.status, (json.loads(raw) if raw else {})


def embed_query(text):
    _, resp = _req(
        "POST",
        f"{EMBED_URL}/embeddings",
        {"model": "multilingual-e5-small-query", "input": text},
    )
    return resp["data"][0]["embedding"]


def search(vec, k):
    _, resp = _req(
        "POST",
        f"{QDRANT_URL}/collections/{COLLECTION}/points/search",
        {"vector": vec, "limit": k, "with_payload": True},
    )
    return resp.get("result", [])


def main():
    qs = []
    with open(os.path.join(HERE, "rag_questions.jsonl"), encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                qs.append(json.loads(line))

    all_pass = True
    for q in qs:
        hits = search(embed_query(q["question"]), TOPK)
        top = hits[0] if hits else None
        top_title = top["payload"]["title"] if top else "(none)"
        top_doc = top["payload"]["doc_id"] if top else "(none)"
        top_score = round(top["score"], 4) if top else None
        retrieved = [h["payload"]["doc_id"] for h in hits]
        if q.get("trap"):
            verdict = "TRAP (no gold doc; retrieval still runs)"
            ok = True
        else:
            ok = any(d in retrieved for d in q["supporting_docs"])
            verdict = "PASS" if ok else "FAIL"
            all_pass = all_pass and ok
        print(
            f"{q['id']}: {verdict}\n"
            f"    Q: {q['question']}\n"
            f"    top1: {top_doc}  ('{top_title}')  score={top_score}\n"
            f"    topk: {retrieved}  expected: {q['supporting_docs']}"
        )
    print("\nRESULT:", "ALL NON-TRAP PASS" if all_pass else "SOME FAIL")
    return 0 if all_pass else 1


if __name__ == "__main__":
    sys.exit(main())
