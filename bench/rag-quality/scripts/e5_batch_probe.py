#!/usr/bin/env python3
"""Independent, BATCHED real-e5 cross-check of the retrieval numbers.

The Go harness (bench/rag-quality) queries the retriever ONE query at a time —
faithful to the product path, but on a slow CPU box the ~600 sequential
embed calls do not finish in a reasonable window. This script measures the SAME
thing (same `crates/embed` binary, same multilingual-e5-small weights, same
e5 `query:`/`passage:` prefixes, same cosine ranking, same recall/MRR/nDCG math
as internal/eval) but embeds the ENTIRE corpus + all queries in ONE batched call
to the embed binary, so it completes in seconds. The numbers it reports are the
real semantic retrieval quality of the e5 embedder; the Go harness is the
product-path implementation of the identical measurement (proven correct on the
deterministic embedder).

Usage:
  AISOTROPY_EMBED_MODEL_DIR=<model dir> \
  python3 bench/rag-quality/scripts/e5_batch_probe.py [gold.json] [embed_bin]
"""

import json
import math
import os
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
DEFAULT_DATA = Path(__file__).resolve().parents[1] / "testdata" / "xquad_slice.json"
DEFAULT_BIN = ROOT / "crates" / "embed" / "target" / "release" / "embed"


# e5 caps at 512 positions and the `--input` subprocess path does not truncate,
# so cap passage text to a token-safe character budget (multilingual tokenizes
# to more tokens/char than English, so stay conservative). Truncation is
# standard RAG chunking anyway; the gold answer sits early in each paragraph.
MAX_CHARS = 900


def embed_batch(bin_path, model_dir, items):
    """items: list of (text, prefix). Returns list of vectors, in order."""
    with tempfile.NamedTemporaryFile("w", suffix=".ndjson", delete=False) as inf:
        for i, (text, prefix) in enumerate(items):
            rec = {"id": f"r{i}", "text": text[:MAX_CHARS]}
            if prefix:
                rec["prefix"] = prefix
            inf.write(json.dumps(rec, ensure_ascii=False) + "\n")
        in_path = inf.name
    out_path = in_path + ".out"
    env = dict(os.environ, AISOTROPY_EMBED_MODEL_DIR=str(model_dir))
    proc = subprocess.run(
        [str(bin_path), "--input", in_path, "--output", out_path],
        env=env, capture_output=True, text=True,
    )
    if proc.returncode != 0:
        sys.exit(f"embed binary failed (rc={proc.returncode}): {proc.stderr.strip()[:600]}")
    vecs = [None] * len(items)
    for line in open(out_path, encoding="utf-8"):
        line = line.strip()
        if not line:
            continue
        row = json.loads(line)
        idx = int(row["id"][1:])
        vecs[idx] = row["embedding"]
    os.unlink(in_path)
    os.unlink(out_path)
    return vecs


def cosine(a, b):
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(y * y for y in b))
    return dot / (na * nb) if na and nb else 0.0


def recall_at_k(ranked_ids, relevant, k):
    return 1.0 if any(r in relevant for r in ranked_ids[:k]) else 0.0


def mrr(ranked_ids, relevant):
    for i, rid in enumerate(ranked_ids):
        if rid in relevant:
            return 1.0 / (i + 1)
    return 0.0


def ndcg_at_k(ranked_ids, relevant, k):
    dcg = 0.0
    for i, rid in enumerate(ranked_ids[:k]):
        if rid in relevant:
            dcg += 1.0 / math.log2(i + 2)
    ideal = min(len(relevant), k)
    idcg = sum(1.0 / math.log2(r + 1) for r in range(1, ideal + 1))
    return dcg / idcg if idcg else 0.0


def main():
    data = Path(sys.argv[1]) if len(sys.argv) > 1 else DEFAULT_DATA
    bin_path = Path(sys.argv[2]) if len(sys.argv) > 2 else DEFAULT_BIN
    model_dir = os.environ.get("AISOTROPY_EMBED_MODEL_DIR")
    if not model_dir:
        sys.exit("AISOTROPY_EMBED_MODEL_DIR must be set")

    d = json.load(open(data, encoding="utf-8"))

    # Build one flat batch: every corpus passage + every query, tagged so we can
    # split the vectors back out after a single embed call.
    items, tags = [], []
    corpus_vecs = {lang: {} for lang in d["corpora"]}
    for lang, docs in d["corpora"].items():
        for doc in docs:
            tags.append(("corpus", lang, doc["id"]))
            items.append((doc["text"], "passage: "))
    # queries: dedupe by (lang, text)
    qseen = {}
    for s in d["slices"]:
        for c in s["cases"]:
            key = (s["query_lang"], c["query"])
            if key not in qseen:
                qseen[key] = None
    query_index = {}
    for (lang, text) in qseen:
        query_index[(lang, text)] = len(items)
        tags.append(("query", lang, text))
        items.append((text, "query: "))

    print(f"batched embed: {len(items)} texts (one call) ...", file=sys.stderr)
    vecs = embed_batch(bin_path, model_dir, items)

    # unpack
    ci = 0
    qvec = {}
    for t, v in zip(tags, vecs):
        if t[0] == "corpus":
            corpus_vecs[t[1]][t[2]] = v
        else:
            qvec[(t[1], t[2])] = v

    print(f"model dim: {len(vecs[0])}")
    header = f"{'slice':16s} {'N':>4s} {'R@1':>7s} {'R@5':>7s} {'R@10':>7s} {'MRR@10':>7s} {'nDCG@10':>7s}"
    print(header)
    print("-" * len(header))

    macro = [0.0] * 5
    for s in d["slices"]:
        clang = s["corpus_lang"]
        corpus_ids = list(corpus_vecs[clang].keys())
        agg = [0.0] * 5
        n = 0
        for c in s["cases"]:
            q = qvec[(s["query_lang"], c["query"])]
            sims = sorted(
                ((cosine(q, corpus_vecs[clang][cid]), cid) for cid in corpus_ids),
                reverse=True,
            )
            ranked = [cid for _, cid in sims]
            rel = set(c["relevant_ids"])
            agg[0] += recall_at_k(ranked, rel, 1)
            agg[1] += recall_at_k(ranked, rel, 5)
            agg[2] += recall_at_k(ranked, rel, 10)
            agg[3] += mrr(ranked[:10], rel)
            agg[4] += ndcg_at_k(ranked, rel, 10)
            n += 1
        agg = [x / n for x in agg]
        for i in range(5):
            macro[i] += agg[i]
        print(f"{s['name']:16s} {n:4d} {agg[0]:7.3f} {agg[1]:7.3f} {agg[2]:7.3f} {agg[3]:7.3f} {agg[4]:7.3f}")
    macro = [x / len(d["slices"]) for x in macro]
    print("-" * len(header))
    print(f"{'MACRO-AVG':16s} {'':>4s} {macro[0]:7.3f} {macro[1]:7.3f} {macro[2]:7.3f} {macro[3]:7.3f} {macro[4]:7.3f}")


if __name__ == "__main__":
    main()
