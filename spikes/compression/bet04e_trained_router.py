"""
bet04e_trained_router.py — the OFF-THESIS test the compression signals failed:
can a TRAINED classifier on cheap features, and/or a measured FRONTIER-vs-CHEAP
ANSWER-QUALITY GAP, route difficulty where no single intrinsic bits-signal could?

Context (docs/research/bet-04-compressibility-routing.md + ADR-0021):
  Difficulty routing is REFUTED for every cheap INTRINSIC signal on this corpus:
      compressibility bits/token   rho = 0.41
      embedding-density (bet05)    rho = -0.52
      base cascade output-entropy  rho = 0.56
      instruct self-confidence     rho = 0.42-0.50   (bet04c)
      raw byte length (the confound) rho = 0.94  -- beats them ALL.
  Every one of those is a SINGLE raw signal, and every >=0.6 was length in
  disguise. The bet doc's own escape hatch: "a viable router needs a TRAINED
  CLASSIFIER or a measured frontier-vs-cheap answer-quality gap." This spike
  tests BOTH claims, honestly, on the SAME 30-prompt corpus (apples-to-apples).

  The stakes are precise. Raw length already scores rho=0.94 IN-SAMPLE *because
  this corpus was hand-labeled with a length gradient* (short easy prompts, long
  hard prompts). In a real query log length will NOT track difficulty like that,
  so the product-relevant question is NOT "can we beat 0.94" (length already
  wins on THIS corpus) but "does a LEARNED combination of cheap features add
  difficulty signal that is INDEPENDENT of length?" If the embedding only
  re-encodes length, a trained router is just a length ruler with extra steps.

--------------------------------------------------------------------------
APPROACH 1 — trained classifier on cheap features, cross-validated
--------------------------------------------------------------------------
Features per prompt (all cheap, no frontier call):
  * e5 embedding (384-d, intfloat/multilingual-e5-small, MIT) -- SEMANTIC
  * cheap scalars: raw_bytes, lm_n_tokens (length); lm_bits_per_token,
    lm_bits_total, gzip_comp_bits, gzip_ratio (the refuted intrinsic signals)
Target: the ordinal difficulty label (1..5).
Models: RIDGE regression on the ordinal target (regression framing) and
L2 LOGISTIC regression on the binary easy(<=2)-vs-hard(>=4) split (drops rank 3
-> 24 prompts). Both LEAVE-ONE-OUT cross-validated (N=30 is tiny; LOO is the
lowest-variance option). Feature standardization is refit INSIDE each fold
(no leakage). alpha is swept over a grid and the WHOLE curve is reported so no
single regularizer is cherry-picked.
Feature sets compared under identical CV:
  LEN         = [raw_bytes, lm_n_tokens]                (length only)
  SCALARS     = LEN + [bits/tok, bits_total, gz_bits, gz_ratio]
  EMB         = the 384-d embedding only
  EMB+LEN     = embedding + length scalars
  EMB+SCALARS = embedding + all scalars
Metrics: Spearman rho of held-out predictions vs true difficulty (regression);
LOO AUC (logistic). THE decisive test: LENGTH-RESIDUAL — regress difficulty on
length, then CV-predict the residual from the embedding. If the embedding
predicts length-orthogonal difficulty variance (rho>0, perm p<0.05), it adds
real signal; if not, the "trained classifier" is length with extra steps.

--------------------------------------------------------------------------
APPROACH 2 — frontier-vs-cheap answer-quality gap (RouteLLM-style)
--------------------------------------------------------------------------
For each prompt: greedy answer from a CHEAP model (Qwen2.5-0.5B-Instruct) and a
STRONGER model (Qwen2.5-1.5B-Instruct -- both Apache-2.0, both already cached;
3B is not cached and would need a download, so 1.5B is the stronger rung and
this limitation is disclosed). GAP = 1 - cosine(e5(cheap_ans), e5(strong_ans)).
Hypothesis: on EASY prompts the two models say the same thing (small gap ->
keep cheap); on HARD prompts they diverge (large gap -> escalate). Report
Spearman rho(gap, difficulty) + the length confounds rho(gap, prompt_len) and
rho(gap, answer_len). Set RUN_GAP=0 to skip (approach 1 stands alone).

Usage:
    .venv/bin/python bet04e_trained_router.py
    RUN_GAP=0 .venv/bin/python bet04e_trained_router.py            # approach 1 only
    N_GEN=72 STRONG_MODEL=Qwen/Qwen2.5-1.5B-Instruct .venv/bin/python bet04e_trained_router.py
"""

from __future__ import annotations

import os
import sys
import time
from typing import Dict, List, Sequence, Tuple

os.environ.setdefault(
    "HF_HOME", os.path.join(os.path.dirname(os.path.abspath(__file__)), ".hf_cache")
)
os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")

import numpy as np  # noqa: E402

# Reuse the EXACT corpus + stats + cheap signals so every number is
# apples-to-apples with the refuted intrinsic signals.
import bet04_compressibility_routing as bet04  # noqa: E402

PROMPTS: List[Tuple[int, str]] = bet04.PROMPTS
spearman = bet04.spearman
pearson = bet04.pearson
perm_pvalue = bet04.perm_pvalue
gzip_signal = bet04.gzip_signal
lm_signal = bet04.lm_signal

from bet01_rerun_context_profile import _auc  # noqa: E402

# The e5 embedder + cosine come straight from bet05 (loads the MIT model once).
from bet05_embedding_density_probe import embed, cos  # noqa: E402

RUN_GAP = os.environ.get("RUN_GAP", "1") == "1"
CHEAP_MODEL = os.environ.get("CHEAP_MODEL", "Qwen/Qwen2.5-0.5B-Instruct")
STRONG_MODEL = os.environ.get("STRONG_MODEL", "Qwen/Qwen2.5-1.5B-Instruct")
N_GEN = int(os.environ.get("N_GEN", "72"))
SYSTEM_MSG = (
    "Sos un asistente de inteligencia artificial que responde en español de forma "
    "clara, precisa y útil."
)

ALPHAS = [0.3, 1.0, 3.0, 10.0, 30.0, 100.0, 300.0]


# ==========================================================================
# numpy learners (no sklearn in the venv) — LOO CV, in-fold standardization
# ==========================================================================
def _standardize_fit(X: np.ndarray):
    mu = X.mean(axis=0)
    sd = X.std(axis=0)
    sd[sd < 1e-9] = 1e-9
    return mu, sd


def ridge_loo(X: np.ndarray, y: np.ndarray, alpha: float) -> np.ndarray:
    """Leave-one-out ridge predictions. Standardization refit per fold; target
    centered per fold; intercept = training mean. Returns held-out preds."""
    n, d = X.shape
    preds = np.zeros(n)
    idx = np.arange(n)
    for i in range(n):
        tr = idx != i
        Xtr, ytr = X[tr], y[tr]
        mu, sd = _standardize_fit(Xtr)
        A = (Xtr - mu) / sd
        ybar = ytr.mean()
        b = ytr - ybar
        w = np.linalg.solve(A.T @ A + alpha * np.eye(d), A.T @ b)
        xi = (X[i] - mu) / sd
        preds[i] = ybar + xi @ w
    return preds


def logistic_loo(X: np.ndarray, y: np.ndarray, alpha: float,
                 iters: int = 500, lr: float = 0.3) -> np.ndarray:
    """Leave-one-out L2-regularized logistic regression. Returns held-out
    P(y=1) scores (used only for AUC ranking). Full-batch gradient descent on
    standardized features; the intercept is unregularized."""
    n, d = X.shape
    scores = np.zeros(n)
    idx = np.arange(n)
    for i in range(n):
        tr = idx != i
        Xtr, ytr = X[tr], y[tr]
        mu, sd = _standardize_fit(Xtr)
        A = (Xtr - mu) / sd
        m = A.shape[0]
        w = np.zeros(d)
        b = 0.0
        for _ in range(iters):
            z = np.clip(A @ w + b, -30.0, 30.0)
            p = 1.0 / (1.0 + np.exp(-z))
            g = p - ytr
            gw = A.T @ g / m + (alpha / m) * w
            gb = g.mean()
            w -= lr * gw
            b -= lr * gb
        xi = (X[i] - mu) / sd
        scores[i] = 1.0 / (1.0 + np.exp(-np.clip(xi @ w + b, -30.0, 30.0)))
    return scores


# ==========================================================================
# Feature construction
# ==========================================================================
def build_features():
    difficulty = np.array([d for d, _ in PROMPTS], dtype=float)
    prompts = [p for _, p in PROMPTS]
    raw_len = np.array([float(len(p.encode("utf-8"))) for p in prompts])

    scal_rows = []
    for p in prompts:
        g = gzip_signal(p)
        lm = lm_signal(p)
        scal_rows.append([
            g["raw_bytes"], lm["lm_n_tokens"],          # length (cols 0,1)
            lm["lm_bits_per_token"], lm["lm_bits_total"],
            g["gzip_comp_bits"], g["gzip_ratio"],
        ])
    scalars = np.array(scal_rows, dtype=float)          # (30, 6)
    length_cols = scalars[:, :2]                        # raw_bytes, n_tokens

    # e5 embeddings (already L2-normalized inside embed()).
    emb = np.stack([embed(p)[0].numpy() for p in prompts])  # (30, 384)

    return {
        "difficulty": difficulty,
        "prompts": prompts,
        "raw_len": raw_len,
        "scalars": scalars,
        "length_cols": length_cols,
        "emb": emb,
    }


# ==========================================================================
# APPROACH 1 — trained classifier, cross-validated
# ==========================================================================
def approach1(F):
    print("\n" + "=" * 80)
    print("APPROACH 1 — trained classifier on cheap features (LEAVE-ONE-OUT CV)")
    print("=" * 80)

    y = F["difficulty"]
    emb = F["emb"]
    scalars = F["scalars"]
    length_cols = F["length_cols"]
    raw_len = F["raw_len"]

    feat_sets = {
        "LEN (len only)":     length_cols,
        "SCALARS (len+bits)": scalars,
        "EMB (384-d)":        emb,
        "EMB+LEN":            np.hstack([emb, length_cols]),
        "EMB+SCALARS":        np.hstack([emb, scalars]),
    }

    # In-sample reference correlations (NOT cross-validated) --------------
    rho_len_insample = spearman(list(raw_len), list(y))
    print(f"\n# In-sample reference (NOT CV): raw byte length vs difficulty "
          f"Spearman rho = {rho_len_insample:+.3f}")
    print("#   (this is the 0.94 confound from ADR-0021 — the bar a length-free")
    print("#    signal must clear ON A REAL LOG where length won't track difficulty.)")

    # --- REGRESSION framing: LOO Spearman of held-out preds, alpha sweep ---
    print("\n# --- Ridge regression on ordinal difficulty: LOO-CV Spearman(pred, true) ---")
    print("#   full alpha sweep reported (no cherry-picking); higher alpha = more shrinkage")
    header = "# {:<20}".format("feature set") + "".join(f"a={a:<7g}" for a in ALPHAS) + " | best"
    print(header)
    print("-" * len(header))
    reg_best = {}
    for name, X in feat_sets.items():
        row = []
        for a in ALPHAS:
            pred = ridge_loo(X, y, a)
            row.append(spearman(list(pred), list(y)))
        best_a = ALPHAS[int(np.argmax(row))]
        reg_best[name] = (max(row), best_a, row)
        cells = "".join(f"{v:<+9.3f}" for v in row)
        print(f"# {name:<20}{cells}| {max(row):+.3f}@a{best_a:g}")

    # --- CLASSIFICATION framing: binary easy(<=2) vs hard(>=4), LOO AUC ----
    print("\n# --- Logistic regression, binary easy(<=2) vs hard(>=4): LOO-CV AUC ---")
    print("#   rank-3 prompts dropped -> 24 prompts (12 easy / 12 hard). AUC>0.5 = signal.")
    mask = (y <= 2) | (y >= 4)
    yb = (y[mask] >= 4).astype(float)
    print(f"#   binary N = {int(mask.sum())}  ({int((yb==0).sum())} easy / {int((yb==1).sum())} hard)")
    header2 = "# {:<20}".format("feature set") + "".join(f"a={a:<7g}" for a in ALPHAS) + " | best"
    print(header2)
    print("-" * len(header2))
    clf_best = {}
    for name, X in feat_sets.items():
        Xb = X[mask]
        row = []
        for a in ALPHAS:
            sc = logistic_loo(Xb, yb, a)
            row.append(_auc(list(sc[yb == 1]), list(sc[yb == 0])))
        best_a = ALPHAS[int(np.argmax(row))]
        clf_best[name] = (max(row), best_a, row)
        cells = "".join(f"{v:<9.3f}" for v in row)
        print(f"# {name:<20}{cells}| {max(row):.3f}@a{best_a:g}")

    # --- DECISIVE test: does EMB predict LENGTH-ORTHOGONAL difficulty? ------
    print("\n# --- DECISIVE length-residual test ---")
    print("#   1) regress difficulty on length (LOO ridge) -> length-explained part")
    print("#   2) residual = true difficulty - length prediction  (length removed)")
    print("#   3) CV-predict that residual from the EMBEDDING alone.")
    print("#   If embedding predicts length-orthogonal difficulty (rho>0, p<0.05),")
    print("#   it adds REAL signal beyond length. If ~0, it's length in disguise.")
    len_pred = ridge_loo(length_cols, y, 3.0)
    residual = y - len_pred
    # sweep alpha for the embedding->residual model, report best + its perm p
    res_rows = []
    for a in ALPHAS:
        rp = ridge_loo(emb, residual, a)
        res_rows.append((a, spearman(list(rp), list(residual))))
    best_a, best_rho = max(res_rows, key=lambda t: t[1])
    rp_best = ridge_loo(emb, residual, best_a)
    p_res = perm_pvalue(list(rp_best), list(residual), spearman)
    print(f"\n#   length-only LOO Spearman(pred,true) = {spearman(list(len_pred), list(y)):+.3f}")
    print("#   embedding -> length-residual, alpha sweep:")
    for a, r in res_rows:
        print(f"#       a={a:<6g} Spearman(pred, residual) = {r:+.3f}")
    print(f"#   BEST embedding->residual Spearman = {best_rho:+.3f} @a{best_a:g}  "
          f"(perm p = {p_res:.4f})")

    # --- headline comparison ----------------------------------------------
    print("\n# --- APPROACH 1 HEADLINE (CV, held-out) ---")
    print(f"#   LEN-only        regression LOO rho = {reg_best['LEN (len only)'][0]:+.3f}"
          f"   |  logistic AUC = {clf_best['LEN (len only)'][0]:.3f}")
    print(f"#   EMB-only        regression LOO rho = {reg_best['EMB (384-d)'][0]:+.3f}"
          f"   |  logistic AUC = {clf_best['EMB (384-d)'][0]:.3f}")
    print(f"#   EMB+LEN         regression LOO rho = {reg_best['EMB+LEN'][0]:+.3f}"
          f"   |  logistic AUC = {clf_best['EMB+LEN'][0]:.3f}")
    print(f"#   EMB+SCALARS     regression LOO rho = {reg_best['EMB+SCALARS'][0]:+.3f}"
          f"   |  logistic AUC = {clf_best['EMB+SCALARS'][0]:.3f}")
    d_emb_over_len = reg_best["EMB+LEN"][0] - reg_best["LEN (len only)"][0]
    print(f"#   delta (EMB+LEN - LEN), regression LOO rho = {d_emb_over_len:+.3f}  "
          "(>0 & residual test significant => embedding adds length-free signal)")
    return {"reg": reg_best, "clf": clf_best,
            "residual_rho": best_rho, "residual_p": p_res,
            "rho_len_insample": rho_len_insample}


# ==========================================================================
# APPROACH 2 — frontier-vs-cheap answer-quality gap
# ==========================================================================
def _load_causal(name: str):
    import torch  # type: ignore
    from transformers import AutoTokenizer, AutoModelForCausalLM  # type: ignore
    tok = AutoTokenizer.from_pretrained(name)
    mdl = AutoModelForCausalLM.from_pretrained(name, dtype=torch.float32)
    mdl.eval()
    return torch, tok, mdl


def _greedy_answer(torch, tok, mdl, user_text: str, n_gen: int) -> str:
    msgs = [{"role": "system", "content": SYSTEM_MSG},
            {"role": "user", "content": user_text}]
    ids = tok.apply_chat_template(
        msgs, add_generation_prompt=True, return_tensors="pt", return_dict=True
    )["input_ids"]
    with torch.no_grad():
        out = mdl.generate(ids, max_new_tokens=n_gen, do_sample=False, num_beams=1,
                           pad_token_id=tok.eos_token_id)
    gen = out[0][ids.shape[1]:]
    return tok.decode(gen, skip_special_tokens=True).strip()


def approach2(F):
    print("\n" + "=" * 80)
    print("APPROACH 2 — frontier-vs-cheap ANSWER-QUALITY GAP (RouteLLM-style)")
    print(f"  cheap  = {CHEAP_MODEL}")
    print(f"  strong = {STRONG_MODEL}   (3B not cached; 1.5B is the stronger rung)")
    print(f"  gap = 1 - cosine(e5(cheap_answer), e5(strong_answer));  N_GEN={N_GEN}")
    print("=" * 80)

    difficulty = F["difficulty"]
    prompts = F["prompts"]
    raw_len = F["raw_len"]

    t0 = time.time()
    print(f"\n[gap] loading cheap model {CHEAP_MODEL} ...", file=sys.stderr)
    tc, tokc, mdlc = _load_causal(CHEAP_MODEL)
    print(f"[gap] loading strong model {STRONG_MODEL} ...", file=sys.stderr)
    ts, toks, mdls = _load_causal(STRONG_MODEL)
    print(f"[gap] models loaded in {time.time()-t0:.0f}s", file=sys.stderr)

    rows = []
    for i, (d, p) in enumerate(zip(difficulty, prompts)):
        ta = time.time()
        ac = _greedy_answer(tc, tokc, mdlc, p, N_GEN)
        as_ = _greedy_answer(ts, toks, mdls, p, N_GEN)
        ec = embed(ac)[0]
        es = embed(as_)[0]
        gap = 1.0 - cos(ec, es)
        ans_len = float(len(ac.encode("utf-8")) + len(as_.encode("utf-8"))) / 2.0
        rows.append({"diff": d, "prompt": p, "gap": gap,
                     "cheap": ac, "strong": as_, "ans_len": ans_len})
        print(f"[gap] {i+1:>2}/30 diff={int(d)} gap={gap:.4f} "
              f"({time.time()-ta:.0f}s)", file=sys.stderr)

    gaps = [r["gap"] for r in rows]
    ans_lens = [r["ans_len"] for r in rows]
    diffs = [r["diff"] for r in rows]

    print(f"\n# Per-prompt answer-gap (sorted by difficulty)")
    print(f"{'diff':>4}  {'gap':>7}  {'promptB':>7}  {'ansB':>6}  prompt")
    print("-" * 90)
    for r in sorted(rows, key=lambda z: (z["diff"], z["gap"])):
        pl = float(len(r["prompt"].encode("utf-8")))
        pp = r["prompt"] if len(r["prompt"]) <= 50 else r["prompt"][:47] + "..."
        print(f"{int(r['diff']):>4}  {r['gap']:>7.4f}  {pl:>7.0f}  {r['ans_len']:>6.0f}  {pp}")

    rho_diff = spearman(gaps, diffs)
    p_diff = perm_pvalue(gaps, diffs, spearman)
    rho_plen = spearman(gaps, list(raw_len))
    rho_alen = spearman(gaps, ans_lens)
    pr_diff = pearson(gaps, diffs)
    verdict = ("USABLE" if abs(rho_diff) >= 0.6 and p_diff < 0.05
               else ("WEAK" if abs(rho_diff) >= 0.3 else "ABSENT"))

    print("\n# --- APPROACH 2 correlations ---")
    print(f"#   Spearman rho(gap, difficulty)   = {rho_diff:+.3f}  (perm p = {p_diff:.4f})  -> {verdict}")
    print(f"#   Pearson  r  (gap, difficulty)   = {pr_diff:+.3f}")
    print(f"#   LENGTH CONFOUND rho(gap, prompt_len) = {rho_plen:+.3f}")
    print(f"#   LENGTH CONFOUND rho(gap, answer_len) = {rho_alen:+.3f}")
    print(f"#   easy(<=2) mean gap = {np.mean([g for g,dd in zip(gaps,diffs) if dd<=2]):.4f}"
          f"   hard(>=4) mean gap = {np.mean([g for g,dd in zip(gaps,diffs) if dd>=4]):.4f}")
    print()
    print(bet04.ascii_scatter(gaps, [int(d) for d in diffs], "answer-quality gap (1 - cos(cheap, strong))"))
    return {"rho_diff": rho_diff, "p_diff": p_diff, "rho_plen": rho_plen,
            "rho_alen": rho_alen, "verdict": verdict, "rows": rows}


# ==========================================================================
def main():
    print("#" * 80)
    print("# bet04e — trained router + answer-quality gap (OFF the compression thesis)")
    print(f"# corpus: {len(PROMPTS)} difficulty-labeled prompts (6/rank), reused from bet04")
    print("#" * 80)

    F = build_features()
    a1 = approach1(F)

    a2 = None
    if RUN_GAP:
        a2 = approach2(F)
    else:
        print("\n[approach 2 skipped: RUN_GAP=0]")

    # ---- FINAL VERDICT ----------------------------------------------------
    print("\n" + "#" * 80)
    print("# FINAL VERDICT (honest; N=30 -> high CV variance, single-annotator labels)")
    print("#" * 80)
    reg = a1["reg"]
    clf = a1["clf"]
    print(f"# APPROACH 1 (trained classifier, LOO CV):")
    print(f"#   LEN-only     reg rho={reg['LEN (len only)'][0]:+.3f}  AUC={clf['LEN (len only)'][0]:.3f}")
    print(f"#   EMB-only     reg rho={reg['EMB (384-d)'][0]:+.3f}  AUC={clf['EMB (384-d)'][0]:.3f}")
    print(f"#   EMB+LEN      reg rho={reg['EMB+LEN'][0]:+.3f}  AUC={clf['EMB+LEN'][0]:.3f}")
    print(f"#   length-residual test: EMB->residual rho={a1['residual_rho']:+.3f} (p={a1['residual_p']:.4f})")
    print(f"#     => embedding adds length-free difficulty signal? "
          f"{'YES' if a1['residual_rho'] >= 0.3 and a1['residual_p'] < 0.05 else 'NO / not significant'}")
    if a2 is not None:
        print(f"# APPROACH 2 (answer-quality gap): rho(gap,difficulty)={a2['rho_diff']:+.3f} "
              f"(p={a2['p_diff']:.4f}) -> {a2['verdict']}")
        print(f"#   length confound rho(gap,prompt_len)={a2['rho_plen']:+.3f} "
              f"rho(gap,answer_len)={a2['rho_alen']:+.3f}")
    print("#" * 80)
    return a1, a2


if __name__ == "__main__":
    main()
