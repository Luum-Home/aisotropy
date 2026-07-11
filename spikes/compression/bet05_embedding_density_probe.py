"""
bet05_embedding_density_probe.py — the DECISIVE ADR-0021 open-question #1 spike.

ADR-0021 measured that a small LM's subword `bits/token` (surprisal) is a
*lexical* signal, not the *semantic* one the two most product-relevant bets
need. It concluded:
  - Bet 1 (surprisal-gated memory) tops out at AUC 0.72 separating
    redundant-vs-surprising facts (no clean gap).
  - Bet 4 (compressibility routing) tops out at Spearman rho=0.41 vs
    difficulty; the only signals that clear 0.6 are raw length in disguise
    (plain byte length scores rho=0.94).
It then HOPED (open question #1) that a SEMANTIC signal — embedding-density /
semantic distance, the Bet-5 sovereign-NN-core territory — would separate
where surprisal failed. This spike RUNS that experiment. It does NOT assume a
positive result; a negative finding (embedding-density ALSO fails / is ALSO
length in disguise) is reported honestly, because that would be the important
result: the semantic-distance hope is not supported and the bets are harder
than the ADR assumed.

--------------------------------------------------------------------------
The signal: SOVEREIGN semantic embedding-density
--------------------------------------------------------------------------
Model: intfloat/multilingual-e5-small (MIT license — verified via
huggingface_hub.model_info; multilingual incl. Spanish 'es'; 117M params).
Loaded via transformers AutoModel + mean-pooling over the last hidden state
with the attention mask, then L2-normalized. Cosine similarity of the
normalized vectors = semantic proximity. No sentence-transformers dependency.

e5 models were trained with instruction prefixes; for a symmetric
similarity task the model card recommends the "query: " prefix on BOTH sides.
We follow that convention (EMB_PREFIX). This is a documented modeling choice,
disclosed as an uncertainty in the Trust Report.

--------------------------------------------------------------------------
Two sub-experiments (SAME corpora as the surprisal spikes — apples-to-apples)
--------------------------------------------------------------------------
Sub-experiment 1 (Bet 1) — redundant vs surprising facts:
    Reuses bet01_rerun_context_profile's IDENTICAL 10+10 facts and the SAME
    USER_PROFILE. For each fact: cosine(fact, profile). Hypothesis: redundant
    facts (implied by the profile) are semantically CLOSE (high sim);
    surprising facts (novel) are FAR (low sim). Score = distance = -sim,
    oriented "higher = more surprising", so the AUC is DIRECTLY comparable to
    the surprisal re-run's 0.72 AUC on this same corpus.

Sub-experiment 2 (Bet 4) — easy vs hard prompts:
    Reuses bet04_compressibility_routing's IDENTICAL 30 difficulty-labeled
    prompts. Two semantic "novelty" signals:
      (a) ANCHOR novelty = -mean cosine sim to a small hand-authored set of
          "easy/known domain" anchor sentences. Harder/novel prompts should
          be FARTHER from the easy manifold. (Anchor authorship is a bias
          source — see BIAS GUARD; that is why (b) exists.)
      (b) kNN density novelty = -mean cosine sim to the prompt's k nearest
          neighbors among the OTHER 29 prompts. Fully UNSUPERVISED — no
          anchor authorship. An isolated prompt (low neighbor density) is
          novel.
    Report Spearman rho vs difficulty for each, DIRECTLY comparable to
    compressibility rho=0.41 and raw-length rho=0.94. CRUCIAL length-confound
    check: also report rho(semantic signal, raw byte length) — if the
    semantic signal is just length again, say so.

--------------------------------------------------------------------------
BIAS GUARD (read before trusting any positive result)
--------------------------------------------------------------------------
1. PROFILE authorship (Bet 1): the SAME degree of freedom that could inflate
   the surprisal profile run inflates this one. We reuse bet01's profile
   verbatim (no new authoring) and REPORT per-fact profile token-overlap and
   its correlation with the embedding distance, plus the separation on the
   low-overlap redundant subset, so the reader sees whether closeness is
   semantic implication or lexical echo.
2. ANCHOR authorship (Bet 4): the easy-anchor set is hand-authored and can
   trivially make near-duplicate rank-1 prompts look "easy". This is
   disclosed; the UNSUPERVISED kNN signal is the bias-free cross-check.
3. LENGTH confound (Bet 4): reported explicitly as rho(signal, length).

Self-contained: only stdlib + torch/transformers + the two sibling spikes
(imported ONLY to reuse their exact corpora + stats helpers, guaranteeing the
comparison is apples-to-apples). Importing them loads the surprisal LM once
(a harmless side effect of the shared module); this spike does not call it.
"""

from __future__ import annotations

import os
import sys
from typing import List, Sequence

os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")

# ----------------------------------------------------------------------
# Reuse the EXACT corpora + statistics from the surprisal spikes so every
# number here is apples-to-apples with ADR-0021's 0.72 AUC and 0.41 rho.
# (These imports load the surprisal LM as a side effect; unused here.)
# ----------------------------------------------------------------------
from bet01_rerun_context_profile import (  # noqa: E402
    REDUNDANT_FACTS,
    SURPRISING_FACTS,
    USER_PROFILE,
    _token_overlap,
    _auc,
    _best_threshold_accuracy,
    _cohens_d,
)
from bet04_compressibility_routing import (  # noqa: E402
    PROMPTS,
    spearman,
    pearson,
    perm_pvalue,
    ascii_scatter,
)

import torch  # noqa: E402
from transformers import AutoModel, AutoTokenizer  # noqa: E402

# ----------------------------------------------------------------------
# The sovereign embedding model (MIT, multilingual). Load once.
# ----------------------------------------------------------------------
EMB_MODEL = os.environ.get("EMB_MODEL", "intfloat/multilingual-e5-small")
EMB_PREFIX = os.environ.get("EMB_PREFIX", "query: ")  # e5 symmetric-similarity convention

print(f"[bet05] loading embedding model: {EMB_MODEL} (prefix={EMB_PREFIX!r})", file=sys.stderr)
_tok = AutoTokenizer.from_pretrained(EMB_MODEL)
_mdl = AutoModel.from_pretrained(EMB_MODEL)
_mdl.eval()
_n_params = sum(p.numel() for p in _mdl.parameters())
print(f"[bet05] loaded OK — {_n_params/1e6:.1f}M params", file=sys.stderr)


def embed(texts) -> "torch.Tensor":
    """Mean-pooled, L2-normalized sentence embeddings (transformers only)."""
    if isinstance(texts, str):
        texts = [texts]
    batch = [EMB_PREFIX + t for t in texts]
    enc = _tok(batch, padding=True, truncation=True, max_length=512, return_tensors="pt")
    with torch.no_grad():
        out = _mdl(**enc)
    last = out.last_hidden_state                      # (B, T, H)
    mask = enc["attention_mask"].unsqueeze(-1).float()  # (B, T, 1)
    summed = (last * mask).sum(dim=1)                 # (B, H)
    counts = mask.sum(dim=1).clamp(min=1e-9)          # (B, 1)
    mean = summed / counts
    return torch.nn.functional.normalize(mean, p=2, dim=1)  # (B, H)


def cos(a: "torch.Tensor", b: "torch.Tensor") -> float:
    """Cosine similarity of two already-L2-normalized vectors."""
    return float((a * b).sum().item())


# ======================================================================
# SUB-EXPERIMENT 1 — Bet 1: redundant vs surprising via cosine(fact, profile)
# ======================================================================
def bet1_redundant_vs_surprising():
    print("\n" + "=" * 78)
    print("SUB-EXPERIMENT 1 — Bet 1: semantic distance fact<->profile")
    print("  hypothesis: redundant facts CLOSE to profile (high sim),")
    print("              surprising facts FAR (low sim).")
    print("=" * 78)

    prof = embed(USER_PROFILE)[0]

    rows = []
    for fact in REDUNDANT_FACTS:
        e = embed(fact)[0]
        rows.append({"fact": fact, "group": "redundant",
                     "sim": cos(e, prof), "overlap": _token_overlap(fact, USER_PROFILE)})
    for fact in SURPRISING_FACTS:
        e = embed(fact)[0]
        rows.append({"fact": fact, "group": "surprising",
                     "sim": cos(e, prof), "overlap": _token_overlap(fact, USER_PROFILE)})

    red_sim = [r["sim"] for r in rows if r["group"] == "redundant"]
    sur_sim = [r["sim"] for r in rows if r["group"] == "surprising"]

    # Score oriented "higher = more surprising" == distance == -sim, so AUC is
    # directly comparable to the surprisal re-run (P(surprising > redundant)).
    red_dist = [-s for s in red_sim]
    sur_dist = [-s for s in sur_sim]
    auc = _auc(sur_dist, red_dist)
    d = _cohens_d(sur_dist, red_dist)
    acc, tau, red_ok, sur_ok = _best_threshold_accuracy(sur_dist, red_dist)

    print(f"\n# Per-fact cosine(fact, profile), sorted (redundant should be at top = closest)")
    print(f"{'sim':>8}  {'ovlp':>5}  {'group':<11}  fact")
    print("-" * 96)
    for r in sorted(rows, key=lambda x: -x["sim"]):
        print(f"{r['sim']:>8.4f}  {r['overlap']:>5.2f}  {r['group']:<11}  {r['fact']}")

    mean_r = sum(red_sim) / len(red_sim)
    mean_s = sum(sur_sim) / len(sur_sim)
    print(f"\n  mean cosine  redundant = {mean_r:.4f}   surprising = {mean_s:.4f}   "
          f"(redundant - surprising = {mean_r - mean_s:+.4f})")
    print(f"  AUC (distance as surprising-score, threshold-free) = {auc:.3f}   [0.5=none, 1.0=perfect]")
    print(f"  Cohen's d (distance) = {d:+.3f}")
    print(f"  best-threshold accuracy = {int(round(acc*20))}/20  "
          f"({red_ok}/10 redundant below tau, {sur_ok}/10 surprising above)  tau_dist={tau:.4f}")

    # ---- BIAS GUARD: is closeness semantic implication or lexical echo? ----
    print("\n  --- BIAS GUARD (profile-overlap leakage) ---")
    ov_red = [r["overlap"] for r in rows if r["group"] == "redundant"]
    ov_sur = [r["overlap"] for r in rows if r["group"] == "surprising"]
    print(f"  mean content-token overlap w/ profile: redundant={sum(ov_red)/len(ov_red):.3f}  "
          f"surprising={sum(ov_sur)/len(ov_sur):.3f}")
    ov_all = [r["overlap"] for r in rows]
    sim_all = [r["sim"] for r in rows]
    print(f"  corr(overlap, cosine-sim) Spearman = {spearman(ov_all, sim_all):+.3f}  "
          f"(high => closeness is lexical echo, not semantic implication)")
    low_ov_red = [r for r in rows if r["group"] == "redundant" and r["overlap"] <= 0.34]
    if low_ov_red:
        lo_sim = [r["sim"] for r in low_ov_red]
        print(f"  low-overlap (<=0.34) redundant facts: n={len(low_ov_red)}  "
              f"mean sim={sum(lo_sim)/len(lo_sim):.4f}  vs surprising mean={mean_s:.4f}")
        print("  (if these low-overlap redundant facts are STILL closer than surprising,")
        print("   the separation is genuine semantic implication, not verbatim copy.)")

    print(f"\n  >>> COMPARE: embedding AUC {auc:.3f}  vs  surprisal re-run AUC 0.72 (same corpus)")
    return {"auc": auc, "d": d, "best_acc": acc, "mean_red": mean_r, "mean_sur": mean_s}


# ======================================================================
# SUB-EXPERIMENT 2 — Bet 4: semantic novelty vs difficulty
# ======================================================================
# Easy/known-domain anchors (hand-authored — ANCHOR AUTHORSHIP BIAS SOURCE).
# Written to describe the "trivial/common" manifold WITHOUT verbatim-copying
# the exact rank-1 prompts, but some semantic overlap with greetings is
# unavoidable and is exactly what the kNN cross-check controls for.
EASY_ANCHORS = [
    "hola, buenos días, ¿cómo va todo?",
    "gracias, muy amable, que tengas un buen día",
    "una pregunta cotidiana con una respuesta simple y directa",
    "un dato básico de cultura general que casi todos conocen",
    "una consulta sencilla de todos los días sin complicaciones",
    "un saludo informal y una charla trivial de rutina",
]


def _knn_density_novelty(embs: List["torch.Tensor"], k: int) -> List[float]:
    """For each item: mean cosine sim to its k nearest neighbors among the
    OTHERS. Return NOVELTY = -density (isolated => novel => high). Unsupervised."""
    n = len(embs)
    nov = []
    for i in range(n):
        sims = sorted((cos(embs[i], embs[j]) for j in range(n) if j != i), reverse=True)
        dens = sum(sims[:k]) / max(len(sims[:k]), 1)
        nov.append(-dens)
    return nov


def _corr_block(name: str, signal: Sequence[float], difficulty: Sequence[int],
                raw_len: Sequence[float]) -> dict:
    rs = spearman(signal, difficulty)
    ps = perm_pvalue(signal, difficulty, spearman)
    rp = pearson(signal, difficulty)
    rlen = spearman(signal, raw_len)
    verdict = "USABLE" if abs(rs) >= 0.6 and ps < 0.05 else ("WEAK" if abs(rs) >= 0.3 else "ABSENT")
    print(f"  {name:<26} rho(diff)={rs:+.3f} (p={ps:.4f})  Pearson={rp:+.3f}  "
          f"rho(length)={rlen:+.3f}  -> {verdict}")
    return {"name": name, "rho": rs, "p": ps, "rho_len": rlen, "verdict": verdict}


def bet4_easy_vs_hard():
    print("\n" + "=" * 78)
    print("SUB-EXPERIMENT 2 — Bet 4: semantic novelty vs difficulty")
    print("  hypothesis: harder/novel prompts are semantically FARTHER from the")
    print("              easy manifold (anchors) and from their neighbors (kNN).")
    print("=" * 78)

    difficulty = [d for d, _ in PROMPTS]
    prompts = [p for _, p in PROMPTS]
    raw_len = [float(len(p.encode("utf-8"))) for p in prompts]

    p_embs = [embed(p)[0] for p in prompts]
    a_embs = [embed(a)[0] for a in EASY_ANCHORS]

    # (a) ANCHOR novelty = -mean sim to easy anchors
    anchor_sim = [sum(cos(pe, ae) for ae in a_embs) / len(a_embs) for pe in p_embs]
    anchor_nov = [-s for s in anchor_sim]

    # (b) kNN density novelty (unsupervised), k=5
    K = int(os.environ.get("BET05_KNN_K", "5"))
    knn_nov = _knn_density_novelty(p_embs, K)

    # Per-prompt table
    print(f"\n# Per-prompt semantic signals (k={K} for kNN)")
    print(f"{'diff':>4}  {'anchSim':>8}  {'anchNov':>8}  {'knnNov':>8}  {'rawB':>5}  prompt")
    print("-" * 110)
    order = sorted(range(len(prompts)), key=lambda i: (difficulty[i], anchor_nov[i]))
    for i in order:
        p = prompts[i] if len(prompts[i]) <= 52 else prompts[i][:49] + "..."
        print(f"{difficulty[i]:>4}  {anchor_sim[i]:>8.4f}  {anchor_nov[i]:>8.4f}  "
              f"{knn_nov[i]:>8.4f}  {raw_len[i]:>5.0f}  {p}")

    print("\n# Correlation vs difficulty (Spearman primary, ordinal labels)")
    print(f"# {'signal':<26} {'rho(diff)':>9}  {'p(perm)':>8}  {'Pearson':>8}  {'rho(length)':>11}  verdict")
    print("-" * 92)
    results = []
    results.append(_corr_block("SEMANTIC anchor-novelty", anchor_nov, difficulty, raw_len))
    results.append(_corr_block("SEMANTIC kNN-novelty", knn_nov, difficulty, raw_len))
    # length confound reference (reproduce ADR-0021's rho=0.94)
    rlen_diff = spearman(raw_len, difficulty)
    plen = perm_pvalue(raw_len, difficulty, spearman)
    print(f"  {'raw byte length (ref)':<26} rho(diff)={rlen_diff:+.3f} (p={plen:.4f})  "
          f"{'':8}  {'':11}  -> LENGTH REFERENCE")

    print("\n# Interpretation guide: |rho|>=0.6 & p<0.05 = USABLE routing signal (bet doc bar).")
    print("#   rho(length) near +-1 on a signal => that signal is length in disguise.")

    print()
    print(ascii_scatter(anchor_nov, difficulty, "SEMANTIC anchor-novelty (-sim to easy anchors)"))
    print()
    print(ascii_scatter(knn_nov, difficulty, "SEMANTIC kNN-novelty (-neighbor density)"))

    best = max(results, key=lambda r: abs(r["rho"]))
    print("\n# ---- Bet 4 comparison to ADR-0021 ----")
    print(f"#   best SEMANTIC signal : {best['name']} rho={best['rho']:+.3f} (p={best['p']:.4f}) -> {best['verdict']}")
    print(f"#   its length confound  : rho(signal, length) = {best['rho_len']:+.3f}")
    print(f"#   compressibility (ADR): rho=0.41  |  raw length (ADR): rho=0.94  "
          f"|  here raw length: rho={rlen_diff:+.3f}")
    return {"results": results, "best": best, "rho_len_diff": rlen_diff}


def main():
    print(f"# Embedding model: {EMB_MODEL} (MIT, multilingual)  params={_n_params/1e6:.1f}M  prefix={EMB_PREFIX!r}")
    print(f"# Corpora reused verbatim from bet01_rerun_context_profile + bet04_compressibility_routing")

    b1 = bet1_redundant_vs_surprising()
    b4 = bet4_easy_vs_hard()

    print("\n" + "#" * 78)
    print("# FINAL NUMBERS (for ADR-0021 open-question #1)")
    print("#" * 78)
    print(f"# Bet 1  embedding-distance AUC = {b1['auc']:.3f}   (surprisal re-run: 0.72)")
    print(f"#        best-threshold acc      = {int(round(b1['best_acc']*20))}/20   Cohen's d = {b1['d']:+.2f}")
    best = b4["best"]
    print(f"# Bet 4  best semantic rho       = {best['rho']:+.3f} (p={best['p']:.4f})  [{best['name']}]")
    print(f"#        vs compressibility rho  = 0.41   vs raw-length rho = 0.94")
    print(f"#        semantic-vs-length rho  = {best['rho_len']:+.3f}   (near +-1 => length in disguise)")
    print("#" * 78)
    return b1, b4


if __name__ == "__main__":
    main()
