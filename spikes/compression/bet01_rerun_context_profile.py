"""
bet01_rerun_context_profile.py — the DECISIVE re-run of Bet 1
("surprisal-gated memory"), applying the two method fixes the prior spike's
own diagnosis prescribed.

Prior runs (see README.md + docs/research/bet-01-surprisal-gated-memory.md)
measured surprisal with context="" ("in a vacuum") and found NO clean gap
between "redundant" and "surprising" facts:
    distilgpt2            -> 2/7 redundant + 6/8 surprising correct
    DeepESP/gpt2-spanish  -> 1/10 redundant + 3/10 surprising correct (worse)
Diagnosis: sentence-level bits/token with context="" captures
syntactic/lexical predictability, NOT the semantic novelty the gate needs.

This re-run changes the METHOD, not the scale, with two fixes:

  FIX 1 — condition on a REAL user profile.
      Instead of context="", condition each fact's surprisal on a short,
      synthetic-but-realistic user profile. A "redundant" fact (already
      implied by the profile / true of any user like this one) should now
      get LOW surprisal; a "surprising" fact (novel vs the profile) should
      stay HIGH. This tests semantic-novelty-given-context.

  FIX 2 — normalize for lexical rarity.
      Raw bits flag a fact as "surprising" merely for containing rare
      tokens (named entities, dates). We remove that confound two ways:
        (a) unigram baseline  : subtract a subword-unigram lexical-rarity
                                baseline from the raw context="" bits.
        (b) self baseline     : gain = s0 - sP, i.e. profile-conditioned
                                bits subtracted from the SAME fact's own
                                unconditioned bits. Lexical rarity cancels
                                EXACTLY because it is the same token string
                                in both terms; what remains is purely "how
                                much did the profile explain this fact".

Conditions evaluated (all against the same 10 redundant + 10 surprising
facts as the prior spike):

  C0 PRIOR   metric = s0 = surprisal(x | "")            [reproduce prior null]
  C1 FIX1    metric = sP = surprisal(x | profile)       [profile only]
  C2 FIX2    metric = s0 - unigram_baseline(x)          [rarity-normalized, no profile]
  C3 BOTH    metric = gain = s0 - sP                    [profile + exact rarity cancel]

Gate semantics (per the bet): STORE a fact iff it is SURPRISING.
  - By sP  (C0/C1): store iff sP is HIGH.
  - By gain (C3)  : store iff gain is LOW (profile did NOT already explain it)
                    -> we orient the metric as (-gain) so "higher = more
                    surprising" is consistent across all conditions.

Separation is reported four ways per condition, so no single knob can hide
a null result:
  - mean gap  : mean(surprising) - mean(redundant) in the oriented direction
  - AUC       : threshold-FREE rank separation (0.5 = none, 1.0 = perfect)
  - best-tau  : max achievable binary accuracy over all thresholds (N/20)
  - naive-tau : midpoint-of-extremes accuracy, the EXACT rule the prior
                spike reported, for apples-to-apples comparison.

--------------------------------------------------------------------------
BIAS GUARD (read this before trusting any positive result)
--------------------------------------------------------------------------
Profile authorship is a degree of freedom that can MANUFACTURE a gap: if the
profile verbatim-restates the redundant facts, they trivially get low
surprisal by lexical copying, not semantic implication. Guards applied:
  1. The profile does NOT verbatim-list the redundant test facts; it
     describes the user generically and lets the facts be *implied*.
  2. The profile contains NONE of the surprising facts (no cat, no allergy,
     no birthday, no Bariloche, no Rutamax, no aisotropy, no AWS/SP, no
     Monday reports, no staging password) and deliberately avoids even the
     "remoto" token that a surprising fact happens to contain.
  3. The run REPORTS per-fact lexical (token) overlap between profile and
     each fact, and correlates overlap with the gain metric, so the reader
     can see whether the separation is driven by verbatim copy (overlap) or
     by genuine semantic implication (low-overlap redundant facts also
     getting explained).
Uses whatever model surprisal.py loaded (default: DeepESP/gpt2-spanish).
"""

from __future__ import annotations

import math
from typing import List, Dict

import surprisal as S
from surprisal import surprisal_bits, backend_info

# ----------------------------------------------------------------------
# The fact corpus — IDENTICAL to bet01_surprisal_gate.py (10 + 10), so the
# only thing that changes vs the prior run is the METHOD, not the data.
# ----------------------------------------------------------------------
REDUNDANT_FACTS = [
    "el usuario usa una computadora",
    "el usuario escribe en español",
    "el usuario tiene un teclado",
    "el usuario usa internet para trabajar",
    "el usuario tiene una cuenta de correo electrónico",
    "el usuario interactúa con un asistente de inteligencia artificial",
    "el usuario guarda archivos en su computadora",
    "el usuario tiene un teléfono celular",
    "el usuario navega por internet todos los días",
    "el usuario escribe correos electrónicos en su trabajo",
]

SURPRISING_FACTS = [
    "el backend de producción del usuario corre en AWS São Paulo",
    "el usuario prefiere recibir reportes los lunes a las 8am",
    "el usuario tiene una alergia severa al maní",
    "la contraseña de la base de datos de staging se rotó el 3 de marzo",
    "el usuario trabaja en una startup de logística llamada Rutamax",
    "el proyecto favorito del usuario se llama aisotropy",
    "el usuario vive en Bariloche y trabaja remoto",
    "el usuario prefiere que las respuestas del asistente sean en tono informal",
    "el gato del usuario se llama Fitzgerald",
    "el usuario cumple años el 17 de noviembre",
]

# ----------------------------------------------------------------------
# The synthetic-but-realistic user profile (FIX 1).
# Written to IMPLY the redundant facts generically without verbatim-listing
# them, and to contain NONE of the surprising facts. See BIAS GUARD above.
# A trailing period + newline gives the LM a clean sentence boundary before
# the fact under test.
# ----------------------------------------------------------------------
USER_PROFILE = (
    "Perfil del usuario. "
    "Es un profesional que trabaja en el área de tecnología y se dedica al "
    "desarrollo de software. Pasa la mayor parte de su jornada frente a la "
    "pantalla de su equipo, leyendo y redactando textos para su trabajo. Se "
    "comunica todos los días por medios digitales con su equipo y con otras "
    "personas. Su idioma habitual de trabajo y de comunicación es el español. "
    "Organiza su día alrededor de tareas de oficina digitales y depende de "
    "servicios en línea para casi todo lo que hace. "
)

# Neutral background corpus for the FIX-2 subword-unigram lexical-rarity
# baseline. Reuses surprisal.py's BASE_CORPUS (generic Spanish computer/office
# phrasing) plus the profile itself, so the baseline reflects "ordinary
# Spanish" token frequencies, not this experiment's specific facts.
BACKGROUND_CORPUS = S.BASE_CORPUS + "\n" + USER_PROFILE


# ======================================================================
# Per-token surprisal helper (reuses surprisal.py's loaded LM) — needed for
# the FIX-2 unigram baseline, which works at the subword-token level.
# ======================================================================
def _lm_per_token(text: str, context: str):
    """Return list of (token_id, bits) for the `text` tokens conditioned on
    `context`, using surprisal.py's already-loaded model/tokenizer."""
    torch = S._torch
    tokenizer = S._tokenizer
    model = S._model

    ctx_ids = tokenizer(context, return_tensors="pt")["input_ids"] if context else None
    if ctx_ids is None or ctx_ids.shape[1] == 0:
        bos = tokenizer.bos_token_id if tokenizer.bos_token_id is not None else tokenizer.eos_token_id
        ctx_ids = torch.tensor([[bos]])

    text_ids = tokenizer(text, return_tensors="pt")["input_ids"]
    full = torch.cat([ctx_ids, text_ids], dim=1)
    n_ctx = ctx_ids.shape[1]
    n_txt = text_ids.shape[1]
    with torch.no_grad():
        logits = model(full).logits[0]
    logp = torch.log_softmax(logits, dim=-1)
    seq = full[0]
    out = []
    for i in range(n_ctx, n_ctx + n_txt):
        tid = seq[i].item()
        bits = -logp[i - 1, tid].item() / math.log(2)
        out.append((tid, bits))
    return out


def _build_unigram_baseline(corpus: str) -> Dict:
    """Subword-unigram lexical-rarity baseline in bits, add-one smoothed,
    over the model tokenizer. bits(token) = -log2((count+1)/(total+V))."""
    tokenizer = S._tokenizer
    ids = tokenizer(corpus)["input_ids"]
    from collections import Counter
    counts = Counter(ids)
    total = sum(counts.values())
    V = tokenizer.vocab_size
    table: Dict = {}
    for tid, c in counts.items():
        table[tid] = -math.log2((c + 1) / (total + V))
    table["__default__"] = -math.log2(1 / (total + V))  # unseen token rarity
    return table


def _unigram_bits_per_token(text: str, baseline: Dict) -> float:
    tokenizer = S._tokenizer
    ids = tokenizer(text)["input_ids"]
    if not ids:
        return 0.0
    default = baseline["__default__"]
    vals = [baseline.get(tid, default) for tid in ids]
    return sum(vals) / len(vals)


# ======================================================================
# Separation statistics
# ======================================================================
def _auc(surprising_vals: List[float], redundant_vals: List[float]) -> float:
    """Mann-Whitney AUC: P(surprising metric > redundant metric), ties=0.5.
    Oriented so higher metric = more surprising. 0.5 = no separation."""
    wins = 0.0
    n = 0
    for s in surprising_vals:
        for r in redundant_vals:
            n += 1
            if s > r:
                wins += 1.0
            elif s == r:
                wins += 0.5
    return wins / n if n else 0.5


def _best_threshold_accuracy(surprising_vals, redundant_vals):
    """Max binary accuracy over all candidate thresholds; oriented higher =
    surprising (store above tau). Returns (accuracy, tau, n_red_ok, n_sur_ok)."""
    allv = sorted(set(surprising_vals + redundant_vals))
    cands = [allv[0] - 1e-9] + [(allv[i] + allv[i + 1]) / 2 for i in range(len(allv) - 1)] + [allv[-1] + 1e-9]
    best = None
    for tau in cands:
        red_ok = sum(1 for r in redundant_vals if r < tau)     # redundant below
        sur_ok = sum(1 for s in surprising_vals if s >= tau)   # surprising above
        acc = (red_ok + sur_ok) / (len(surprising_vals) + len(redundant_vals))
        if best is None or acc > best[0]:
            best = (acc, tau, red_ok, sur_ok)
    return best


def _naive_midpoint_accuracy(surprising_vals, redundant_vals):
    """The EXACT rule the prior spike reported: tau = midpoint between the
    most-surprising redundant value and the least-surprising surprising
    value (extremes), oriented higher = surprising. For direct comparability."""
    max_red = max(redundant_vals)
    min_sur = min(surprising_vals)
    tau = (max_red + min_sur) / 2
    red_ok = sum(1 for r in redundant_vals if r < tau)
    sur_ok = sum(1 for s in surprising_vals if s >= tau)
    return tau, red_ok, sur_ok


def _cohens_d(surprising_vals, redundant_vals):
    import statistics as st
    ms, mr = st.mean(surprising_vals), st.mean(redundant_vals)
    n1, n2 = len(surprising_vals), len(redundant_vals)
    v1 = st.pvariance(surprising_vals) if n1 > 1 else 0.0
    v2 = st.pvariance(redundant_vals) if n2 > 1 else 0.0
    pooled = math.sqrt(((n1 * v1) + (n2 * v2)) / (n1 + n2)) if (n1 + n2) else 0.0
    return (ms - mr) / pooled if pooled else 0.0


# ======================================================================
# Bias-guard leakage proxy
# ======================================================================
def _token_overlap(fact: str, profile: str) -> float:
    """Fraction of fact's content (lowercased word) tokens that also appear
    in the profile — a leakage proxy for the BIAS GUARD."""
    fw = set(S._tokenize_words(fact))
    pw = set(S._tokenize_words(profile))
    if not fw:
        return 0.0
    stop = {"el", "la", "los", "las", "un", "una", "de", "del", "en", "su", "sus",
            "y", "a", "por", "para", "con", "que", "se", "al", "lo"}
    content = fw - stop
    if not content:
        return 0.0
    return len(content & pw) / len(content)


def _report_condition(name: str, desc: str, red_vals, sur_vals, unit: str, orient_note: str):
    auc = _auc(sur_vals, red_vals)
    d = _cohens_d(sur_vals, red_vals)
    acc, tau, red_ok, sur_ok = _best_threshold_accuracy(sur_vals, red_vals)
    ntau, nred_ok, nsur_ok = _naive_midpoint_accuracy(sur_vals, red_vals)
    mean_r = sum(red_vals) / len(red_vals)
    mean_s = sum(sur_vals) / len(sur_vals)
    print(f"\n=== {name} — {desc} ===")
    print(f"  orientation: {orient_note}")
    print(f"  mean redundant = {mean_r:8.4f} {unit}   mean surprising = {mean_s:8.4f} {unit}")
    print(f"  mean GAP (surprising - redundant) = {mean_s - mean_r:+.4f} {unit}   Cohen's d = {d:+.3f}")
    print(f"  AUC (threshold-free rank separation) = {auc:.3f}   [0.5=none, 1.0=perfect]")
    print(f"  best-tau accuracy   = {int(round(acc*20))}/20  ({red_ok}/10 redundant, {sur_ok}/10 surprising)  tau={tau:.4f}")
    print(f"  naive-midpoint (prior-spike rule) = {nred_ok}/10 redundant, {nsur_ok}/10 surprising  tau={ntau:.4f}")
    return {"name": name, "auc": auc, "d": d, "best_acc": acc, "gap": mean_s - mean_r,
            "naive_red": nred_ok, "naive_sur": nsur_ok, "best_red": red_ok, "best_sur": sur_ok}


def main():
    info = backend_info()
    if not info["using_lm"]:
        print("# WARNING: running on the N-GRAM FALLBACK, not a real LM. Results are not the decisive test.")
    print(f"# Backend: {'real LM -> ' + str(info['model_name']) if info['using_lm'] else 'N-GRAM FALLBACK'}")
    print(f"# Reason: {info['reason']}")
    print(f"# Profile (FIX 1) conditioned on:\n#   {USER_PROFILE.strip()}")

    facts = [(f, "redundant") for f in REDUNDANT_FACTS] + [(f, "surprising") for f in SURPRISING_FACTS]

    baseline = _build_unigram_baseline(BACKGROUND_CORPUS) if info["using_lm"] else None

    rows = []
    for fact, group in facts:
        s0 = surprisal_bits(fact, "")["bits_per_token"]
        sP = surprisal_bits(fact, USER_PROFILE)["bits_per_token"]
        if baseline is not None:
            uni = _unigram_bits_per_token(fact, baseline)
            fix2 = s0 - uni  # raw bits normalized by lexical rarity (no profile)
        else:
            uni, fix2 = float("nan"), float("nan")
        gain = s0 - sP  # bits/tok the profile saved; lexical rarity cancels
        overlap = _token_overlap(fact, USER_PROFILE)
        rows.append({"fact": fact, "group": group, "s0": s0, "sP": sP,
                     "uni": uni, "fix2": fix2, "gain": gain, "overlap": overlap})

    unit = "bits/tok"

    print(f"\n# Per-fact table (sorted by gain = s0 - sP, i.e. bits/tok the profile saved)")
    print(f"{'s0(ctx=)':>10}  {'sP(prof)':>10}  {'gain':>7}  {'fix2':>7}  {'ovlp':>5}  {'group':<11}  fact")
    print("-" * 118)
    for r in sorted(rows, key=lambda x: -x["gain"]):
        print(f"{r['s0']:>10.3f}  {r['sP']:>10.3f}  {r['gain']:>7.3f}  {r['fix2']:>7.3f}  {r['overlap']:>5.2f}  {r['group']:<11}  {r['fact']}")

    red = {k: [r[k] for r in rows if r["group"] == "redundant"] for k in ("s0", "sP", "fix2", "gain")}
    sur = {k: [r[k] for r in rows if r["group"] == "surprising"] for k in ("s0", "sP", "fix2", "gain")}

    results = []
    results.append(_report_condition(
        "C0 PRIOR", "surprisal(x | empty) — reproduce prior null", red["s0"], sur["s0"], unit,
        "higher s0 = more surprising (store above tau)"))
    results.append(_report_condition(
        "C1 FIX1", "surprisal(x | profile) — profile only", red["sP"], sur["sP"], unit,
        "higher sP = more surprising (store above tau)"))
    if baseline is not None:
        results.append(_report_condition(
            "C2 FIX2", "s0 - unigram_baseline — rarity-normalized, no profile", red["fix2"], sur["fix2"], unit,
            "higher excess-over-rarity = more surprising"))
    results.append(_report_condition(
        "C3 BOTH", "-(gain) = sP - s0 — profile + exact lexical-rarity cancel",
        [-v for v in red["gain"]], [-v for v in sur["gain"]], unit,
        "LOW gain = surprising; oriented as -(gain) so higher = surprising"))

    print("\n=== BIAS-GUARD diagnostics (profile leakage) ===")
    ov_red = [r["overlap"] for r in rows if r["group"] == "redundant"]
    ov_sur = [r["overlap"] for r in rows if r["group"] == "surprising"]
    print(f"  mean content-token overlap with profile: redundant={sum(ov_red)/len(ov_red):.3f}  surprising={sum(ov_sur)/len(ov_sur):.3f}")
    low_ov_red = [r for r in rows if r["group"] == "redundant" and r["overlap"] <= 0.34]
    print(f"  redundant facts with LOW (<=0.34) profile overlap: {len(low_ov_red)}")
    for r in sorted(low_ov_red, key=lambda x: -x["gain"]):
        print(f"    gain={r['gain']:+.3f}  ovlp={r['overlap']:.2f}  \"{r['fact']}\"")
    print("  (If these low-overlap redundant facts still show high gain, the separation")
    print("   is semantic implication, not verbatim copying. If gain tracks overlap only,")
    print("   the gap is manufactured — disclosed either way.)")

    print("\n=== VERDICT SUMMARY (AUC, threshold-free) ===")
    for r in results:
        print(f"  {r['name']:<9} AUC={r['auc']:.3f}  best-acc={int(round(r['best_acc']*20))}/20  "
              f"naive={r['naive_red']}+{r['naive_sur']}/20  gap={r['gap']:+.3f}  d={r['d']:+.2f}")
    c0 = next(r for r in results if r["name"] == "C0 PRIOR")
    c3 = next(r for r in results if r["name"] == "C3 BOTH")
    print(f"\n  PRIOR context=empty AUC = {c0['auc']:.3f}  ->  BOTH-fixes AUC = {c3['auc']:.3f}  "
          f"(delta {c3['auc']-c0['auc']:+.3f})")
    return results


if __name__ == "__main__":
    main()
