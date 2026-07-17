#!/usr/bin/env python3
"""Fetch a small, self-contained multilingual retrieval slice from XQuAD.

XQuAD (google/xquad, CC-BY-SA-4.0) is a PARALLEL cross-lingual QA benchmark:
the same 1190 SQuAD v1.1 dev questions + their source paragraphs, professionally
translated into 12 languages and aligned by a shared question `id`. We reduce it
to a retrieval task (the standard QA->IR construction): the gold "relevant
passage" for a question is the paragraph it was written about.

Because the paragraphs are parallel across languages, one fetch yields four
retrieval slices with real qrels:
  - en_monolingual : English question   -> English corpus
  - es_monolingual : Spanish question   -> Spanish corpus
  - cross_en_to_es : English question   -> Spanish corpus  (cross-lingual)
  - cross_es_to_en : Spanish question   -> English corpus  (cross-lingual)

The cross-lingual slices are the ones that prove a MULTILINGUAL embedder actually
projects both languages into one shared space: an English query must find the
Spanish paragraph that answers it, with no lexical overlap to lean on.

Output: a single self-contained JSON (corpora + slices + qrels + provenance)
under testdata/, so the Go harness needs no network and the license/attribution
travel with the data.

This script is a one-shot data-prep tool (NOT part of the Go build). It hits the
Hugging Face datasets-server rows API (public, no auth for XQuAD).
"""

import json
import sys
import time
import urllib.request
import urllib.parse
from pathlib import Path

DATASET = "google/xquad"
ROWS_PER_LANG = 800  # bounded slice: keeps testdata + eval runtime small
PAGE = 100
LANGS = {"en": "xquad.en", "es": "xquad.es"}
OUT = Path(__file__).resolve().parents[1] / "testdata" / "xquad_slice.json"
MAX_QUERIES_PER_SLICE = 150  # subsample queries; full corpus kept as distractors


def fetch_rows(config: str, n: int):
    """Fetch the first n rows of a config's validation split, paginated."""
    rows = []
    offset = 0
    while offset < n:
        length = min(PAGE, n - offset)
        params = urllib.parse.urlencode(
            {
                "dataset": DATASET,
                "config": config,
                "split": "validation",
                "offset": offset,
                "length": length,
            }
        )
        url = f"https://datasets-server.huggingface.co/rows?{params}"
        for attempt in range(4):
            try:
                with urllib.request.urlopen(url, timeout=30) as resp:
                    data = json.load(resp)
                break
            except Exception as exc:  # noqa: BLE001 - one-shot data prep
                if attempt == 3:
                    raise
                print(f"  retry {attempt+1} ({config} @ {offset}): {exc}", file=sys.stderr)
                time.sleep(1.5 * (attempt + 1))
        batch = data.get("rows", [])
        if not batch:
            break
        for r in batch:
            rows.append(r["row"])
        offset += length
        print(f"  {config}: {len(rows)} rows", file=sys.stderr)
    return rows


def build():
    per_lang = {}
    for lang, config in LANGS.items():
        print(f"fetching {config} ...", file=sys.stderr)
        per_lang[lang] = fetch_rows(config, ROWS_PER_LANG)

    # XQuAD rows are index-aligned across parallel configs and also carry a
    # shared question `id`. Verify alignment defensively before trusting it.
    en, es = per_lang["en"], per_lang["es"]
    n = min(len(en), len(es))
    if n == 0:
        raise SystemExit("no rows fetched")
    for i in range(n):
        if en[i]["id"] != es[i]["id"]:
            raise SystemExit(
                f"alignment broke at row {i}: en id {en[i]['id']} != es id {es[i]['id']}"
            )

    # Build a deduplicated paragraph corpus per language. A paragraph is shared
    # by several questions, so dedup by exact text and assign a stable id.
    def build_corpus(rows):
        by_text = {}
        order = []
        for r in rows[:n]:
            ctx = r["context"].strip()
            if ctx not in by_text:
                cid = f"{'x'}-{len(order)}"  # placeholder, replaced below
                by_text[ctx] = len(order)
                order.append(ctx)
        return by_text, order

    en_idx, en_order = build_corpus(en)
    es_idx, es_order = build_corpus(es)

    def corpus_docs(order, prefix):
        return [{"id": f"{prefix}-{i}", "text": t} for i, t in enumerate(order)]

    corpora = {
        "en": corpus_docs(en_order, "en"),
        "es": corpus_docs(es_order, "es"),
    }

    # Per-question mapping to its gold paragraph id in each language.
    qmap = []
    for i in range(n):
        qmap.append(
            {
                "id": en[i]["id"],
                "q_en": en[i]["question"].strip(),
                "q_es": es[i]["question"].strip(),
                "doc_en": f"en-{en_idx[en[i]['context'].strip()]}",
                "doc_es": f"es-{es_idx[es[i]['context'].strip()]}",
            }
        )

    sample = qmap[:MAX_QUERIES_PER_SLICE]

    def cases(query_key, doc_key):
        return [
            {
                "name": f"q-{m['id']}",
                "query": m[query_key],
                "relevant_ids": [m[doc_key]],
            }
            for m in sample
        ]

    slices = [
        {
            "name": "en_monolingual",
            "description": "English question retrieves its English source paragraph.",
            "query_lang": "en",
            "corpus_lang": "en",
            "cases": cases("q_en", "doc_en"),
        },
        {
            "name": "es_monolingual",
            "description": "Spanish question retrieves its Spanish source paragraph.",
            "query_lang": "es",
            "corpus_lang": "es",
            "cases": cases("q_es", "doc_es"),
        },
        {
            "name": "cross_en_to_es",
            "description": "Cross-lingual: English question retrieves the parallel Spanish paragraph.",
            "query_lang": "en",
            "corpus_lang": "es",
            "cases": cases("q_en", "doc_es"),
        },
        {
            "name": "cross_es_to_en",
            "description": "Cross-lingual: Spanish question retrieves the parallel English paragraph.",
            "query_lang": "es",
            "corpus_lang": "en",
            "cases": cases("q_es", "doc_en"),
        },
    ]

    out = {
        "name": "xquad-multilingual-retrieval-slice",
        "source": "google/xquad — XQuAD v1.1 (Artetxe, Ruder, Yogatama 2020), validation split",
        "source_url": "https://huggingface.co/datasets/google/xquad",
        "license": "CC-BY-SA-4.0",
        "license_url": "https://creativecommons.org/licenses/by-sa/4.0/",
        "attribution": (
            "XQuAD is released by DeepMind under CC-BY-SA-4.0. It is a professional "
            "translation of a subset of the SQuAD v1.1 (Rajpurkar et al. 2016) dev set. "
            "This file is a derived retrieval slice (QA->IR reduction) of the EN and ES "
            "validation configs and is redistributed under the same CC-BY-SA-4.0 terms. "
            "See LICENSE-DATA.md in this directory."
        ),
        "generated_by": "bench/rag-quality/scripts/fetch_xquad.py",
        "rows_fetched_per_lang": ROWS_PER_LANG,
        "aligned_pairs": n,
        "queries_per_slice": len(sample),
        "corpus_sizes": {"en": len(corpora["en"]), "es": len(corpora["es"])},
        "corpora": corpora,
        "slices": slices,
    }

    OUT.parent.mkdir(parents=True, exist_ok=True)
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, indent=1)
    print(
        f"wrote {OUT} — corpora en={len(corpora['en'])} es={len(corpora['es'])}, "
        f"{len(sample)} queries/slice, {len(slices)} slices",
        file=sys.stderr,
    )


if __name__ == "__main__":
    build()
