# Data license — `testdata/xquad_slice.json`

`testdata/xquad_slice.json` is a **derived retrieval slice** of the **XQuAD**
benchmark, produced by `scripts/fetch_xquad.py`.

| Field | Value |
|---|---|
| Upstream dataset | **XQuAD** (Cross-lingual Question Answering Dataset), v1.1 |
| Authors | Mikel Artetxe, Sebastian Ruder, Dani Yogatama (DeepMind), 2020 |
| Upstream source | https://huggingface.co/datasets/google/xquad · https://github.com/google-deepmind/xquad |
| Upstream license | **CC-BY-SA-4.0** (https://creativecommons.org/licenses/by-sa/4.0/) |
| Derived-from | The English (`xquad.en`) and Spanish (`xquad.es`) *validation* configs |
| This slice's license | **CC-BY-SA-4.0** (ShareAlike — same as upstream) |

## Why this license clears the gate

The stack's constitutional license gate (`.claude/rules/constitutional-gates.md`,
`license-policy.md`) **blocks copyleft/source-available _software_ licenses**
(AGPL, SSPL, BSL). CC-BY-SA-4.0 is **not** one of those — it is a *content*
license, not a software license, so the software gate does not reach it.

But it is **NOT permissive**: CC-BY-SA-4.0 is a **share-alike (copyleft) content
license**. It permits commercial and SaaS use, but it *requires* attribution AND
that redistributed adaptations carry the same license. An earlier revision of
this file called it "permissive" and said it "grants unrestricted use" — both
wrong, and dangerous in a way the compliant handling below masks: a future
maintainer reading that sentence could treat any other CC-BY-SA dataset as
drop-in, which it is not. The handling was always correct; only this rationale
was. This file is:

1. **Attributed** — provenance and authorship are recorded here and inline in
   the JSON (`source`, `license`, `attribution` fields).
2. **ShareAlike-compliant** — this derived slice is redistributed under the same
   CC-BY-SA-4.0 terms.

XQuAD itself is a professional translation of a subset of the **SQuAD v1.1**
(Rajpurkar et al., 2016) dev set, which is also released CC-BY-SA-4.0.

## Note on the preference for CC-BY / CC0

The eval harness (`bench/rag-quality`) is **dataset-agnostic**: it reads any
gold set in the `xquad_slice.json` shape (`corpora` + `slices` + qrels). If a
strictly non-ShareAlike source is later preferred, clean alternatives exist and
can be dropped in without code changes — e.g. **Tatoeba** bitext (CC-BY-2.0) or
**MQA** (CC0-1.0). XQuAD was chosen here because it is *parallel* across EN/ES
(the same paragraphs translated), which is what makes a clean **cross-lingual**
retrieval measurement possible.

## Regenerating

```bash
python3 bench/rag-quality/scripts/fetch_xquad.py
```

This re-fetches the EN/ES validation configs from the public Hugging Face
datasets-server (no auth required) and rewrites `testdata/xquad_slice.json`.
