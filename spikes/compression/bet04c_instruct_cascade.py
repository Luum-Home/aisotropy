"""
bet04c_instruct_cascade.py — the fair test bet04b could NOT run: does an
INSTRUCTION-TUNED cheap model's ANSWER-CONFIDENCE route difficulty, length-
independently, on the exact same 30-prompt corpus?

Context (docs/decisions/0021-... §2 + the cascade carve-out; docs/research/
bet-04-compressibility-routing.md):
  Every PROMPT-INTRINSIC TEXT signal was refuted on this corpus:
      compressibility bits/token   rho = 0.41  (below the 0.6 bar)
      embedding-density (bet05)    rho = -0.52 (refuted)
      raw byte length              rho = 0.94  (the confound; anything >=0.6 is length)
  bet04b tried the cheap MODEL's own behaviour (cascade output-entropy) but used
  BASE LMs (distilgpt2, gpt2-spanish). Base LMs ramble at ~constant entropy on
  reasoning prompts, so the cascade signal came out flat — a MODEL limitation,
  not a refutation of the cascade IDEA. base-model cascade output-entropy landed
  around rho = 0.56 and was not cleanly length-independent.

  This spike runs the cascade IDEA with a real INSTRUCT model (Qwen2.5-0.5B-
  Instruct, Apache-2.0, multilingual) that actually TRIES to answer via its chat
  template. If its answer-confidence tracks difficulty length-independently
  (rho >= 0.6, and rho(difficulty) meaningfully > rho(length)), the Bet-4 cascade
  carve-out is validated as a real (if off-compression-thesis) cost-routing path.
  If it ALSO collapses to length / stays < 0.6, difficulty routing is genuinely
  hard even for a model that answers.

Signals (all on the SAME 30 prompts; bits where applicable):
  1. GREEDY ANSWER MEAN ENTROPY (bits/token) — mean Shannon entropy of the model's
     next-token distribution over its OWN greedy answer. Low = confident.
  2. GREEDY ANSWER MEAN NEG-LOGPROB (bits/token) — mean -log2 P(chosen token) of the
     greedy answer. Complementary confidence read; low = confident.
  3. SELF-CONSISTENCY DISAGREEMENT over K sampled answers (temperature):
       (a) answer-length std (tokens) — high = unstable = uncertain.
       (b) mean pairwise Jaccard DISTANCE over answer token-sets — high = the model
           says different things each time = uncertain.

Analysis (mirrors bet04 / bet04b exactly, reusing bet04's corpus + stats):
  * Spearman rho of each signal vs the human difficulty labels + permutation p.
  * LENGTH-CONFOUND per signal: rho(signal, prompt_bytes) AND rho(signal, answer_len).
  * Routing sim at the median threshold: dangerous hard->cheap misroutes.
  * Comparison vs base-model cascade 0.56, compressibility 0.41, embedding -0.52, len 0.94.

Usage:
    .venv/bin/python bet04c_instruct_cascade.py
    N_GEN=96 K_SAMPLES=3 INSTRUCT_MODEL=Qwen/Qwen2.5-0.5B-Instruct \
        .venv/bin/python bet04c_instruct_cascade.py
"""

from __future__ import annotations

import math
import os
import time
from typing import Dict, List, Sequence, Tuple

# Reuse the EXACT bet-04 corpus + stats -> apples-to-apples with the refuted signals.
import bet04_compressibility_routing as bet04

PROMPTS: List[Tuple[int, str]] = bet04.PROMPTS
spearman = bet04.spearman
pearson = bet04.pearson
perm_pvalue = bet04.perm_pvalue
ascii_scatter = bet04.ascii_scatter

MODEL_NAME = os.environ.get("INSTRUCT_MODEL", "Qwen/Qwen2.5-0.5B-Instruct")
N_GEN = int(os.environ.get("N_GEN", "96"))          # greedy answer length (tokens)
K_SAMPLES = int(os.environ.get("K_SAMPLES", "3"))    # self-consistency samples
TEMP = float(os.environ.get("SAMPLE_TEMP", "0.7"))
N_GEN_SAMPLE = int(os.environ.get("N_GEN_SAMPLE", "64"))  # shorter samples to save CPU
SEED = int(os.environ.get("SEED", "0"))

# Realistic Spanish assistant system message so the model answers like a deployed
# assistant would (same spirit as bet04b's SYSTEM_PREFIX, but as a chat role).
SYSTEM_MSG = (
    "Sos un asistente de inteligencia artificial que responde en español de forma "
    "clara, precisa y útil."
)

_HF_CACHE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".hf_cache")


def load_model(name: str):
    os.environ.setdefault("HF_HOME", _HF_CACHE_DIR)
    os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
    import torch  # type: ignore
    from transformers import AutoTokenizer, AutoModelForCausalLM  # type: ignore

    tok = AutoTokenizer.from_pretrained(name)
    mdl = AutoModelForCausalLM.from_pretrained(name, dtype=torch.float32)
    mdl.eval()
    return torch, tok, mdl


def build_prompt_ids(torch, tok, user_text: str):
    msgs = [{"role": "system", "content": SYSTEM_MSG},
            {"role": "user", "content": user_text}]
    enc = tok.apply_chat_template(
        msgs, add_generation_prompt=True, return_tensors="pt", return_dict=True
    )
    return enc["input_ids"]


# --------------------------------------------------------------------------
# SIGNAL 1 + 2 — greedy answer confidence.
# One greedy generate with output_scores gives, per generated step:
#   entropy_i     = H(softmax(logits_i))                 -> mean = SIGNAL 1
#   neglogprob_i  = -log2 P(argmax_i)                    -> mean = SIGNAL 2
# Both in bits. answer_len = number of generated tokens (before EOS).
# --------------------------------------------------------------------------
def greedy_confidence(torch, tok, mdl, ids, n_gen: int) -> Dict[str, float]:
    ln2 = math.log(2)
    with torch.no_grad():
        out = mdl.generate(
            ids,
            max_new_tokens=n_gen,
            do_sample=False,
            num_beams=1,
            output_scores=True,
            return_dict_in_generate=True,
            pad_token_id=tok.eos_token_id,
        )
    seqs = out.sequences[0]
    gen_tokens = seqs[ids.shape[1]:]
    scores = out.scores  # tuple len == #generated steps, each [1, vocab]
    entropies: List[float] = []
    neglogps: List[float] = []
    eos = tok.eos_token_id
    n_real = 0
    for i, logits in enumerate(scores):
        logits = logits[0]
        logp = torch.log_softmax(logits, dim=-1)
        p = torch.exp(logp)
        H_nats = -(p * logp).sum().item()
        chosen = int(gen_tokens[i].item())
        # Stop counting at EOS: entropy of tokens after the answer ends is noise.
        if chosen == eos:
            break
        entropies.append(H_nats / ln2)
        neglogps.append(-logp[chosen].item() / ln2)
        n_real += 1
    if not entropies:  # model emitted EOS immediately; fall back to first step
        logits = scores[0][0]
        logp = torch.log_softmax(logits, dim=-1)
        p = torch.exp(logp)
        entropies = [-(p * logp).sum().item() / ln2]
        neglogps = [-logp[int(gen_tokens[0].item())].item() / ln2]
        n_real = 1
    # Did greedy run to the token cap (i.e. no natural EOS)? A capped answer means
    # answer_len is a CENSORED observation, not the model's true answer length.
    greedy_capped = 1.0 if n_real >= n_gen else 0.0
    return {
        "mean_entropy_bits": sum(entropies) / len(entropies),
        "mean_neglogprob_bits": sum(neglogps) / len(neglogps),
        "answer_len": float(n_real),
        "greedy_capped": greedy_capped,
        "answer_text": tok.decode(gen_tokens, skip_special_tokens=True),
    }


# --------------------------------------------------------------------------
# SIGNAL 3 — self-consistency disagreement over K sampled answers.
#   length_std  : std of answer token counts across the K samples.
#   jaccard_dist: mean pairwise Jaccard DISTANCE over answer token-SETS
#                 (1 - |A&B|/|A|B|). High = the model disagrees with itself.
# --------------------------------------------------------------------------
def self_consistency(torch, tok, mdl, ids, k: int, temp: float, n_gen: int, seed: int) -> Dict[str, float]:
    token_sets: List[set] = []
    lengths: List[int] = []
    for s in range(k):
        torch.manual_seed(seed + s)
        with torch.no_grad():
            out = mdl.generate(
                ids,
                max_new_tokens=n_gen,
                do_sample=True,
                temperature=temp,
                top_p=0.95,
                num_beams=1,
                pad_token_id=tok.eos_token_id,
            )
        gen = out[0][ids.shape[1]:]
        toks = [int(t) for t in gen if int(t) != tok.eos_token_id]
        token_sets.append(set(toks))
        lengths.append(len(toks))
    # length std
    mL = sum(lengths) / len(lengths)
    length_std = (sum((x - mL) ** 2 for x in lengths) / len(lengths)) ** 0.5
    # mean pairwise Jaccard distance
    dists: List[float] = []
    for i in range(len(token_sets)):
        for j in range(i + 1, len(token_sets)):
            a, b = token_sets[i], token_sets[j]
            union = len(a | b)
            inter = len(a & b)
            jac = (inter / union) if union else 1.0
            dists.append(1.0 - jac)
    jaccard_dist = sum(dists) / len(dists) if dists else 0.0
    # Fraction of the K samples that hit the generation cap. When most samples of
    # a hard prompt cap out, they all share length == n_gen -> length_std collapses
    # to ~0. That makes length_std a TRUNCATION ARTIFACT (low std <-> hard), not a
    # real self-consistency read. cap_rate exposes exactly that failure mode.
    cap_rate = sum(1 for L in lengths if L >= n_gen) / len(lengths)
    return {"length_std": length_std, "jaccard_dist": jaccard_dist, "cap_rate": cap_rate}


def routing_sim(signal: Sequence[float], difficulty: Sequence[int]) -> Dict[str, float]:
    pairs = sorted(zip(signal, difficulty))
    n = len(pairs)
    theta = pairs[n // 2][0]
    cheap = [d for s, d in zip(signal, difficulty) if s < theta]
    exp = [d for s, d in zip(signal, difficulty) if s >= theta]
    return {
        "theta": theta,
        "n_cheap": len(cheap),
        "n_exp": len(exp),
        "hard_sent_cheap": sum(1 for d in cheap if d >= 4),   # DANGEROUS (quality loss)
        "easy_sent_exp": sum(1 for d in exp if d <= 2),        # wasteful ($$)
        "mean_diff_cheap": (sum(cheap) / len(cheap)) if cheap else float("nan"),
        "mean_diff_exp": (sum(exp) / len(exp)) if exp else float("nan"),
    }


def verdict(rho: float, p: float, rho_prompt_len: float, rho_ans_len: float) -> str:
    strong = abs(rho) >= 0.6 and p < 0.05
    rho_len = max(abs(rho_prompt_len), abs(rho_ans_len))
    # Length-driven if the difficulty correlation is NOT meaningfully larger than the
    # length correlation (gap < 0.15), and length itself is a substantial correlate
    # (>=0.4). This catches signals whose apparent difficulty rho is really just length,
    # even when the length rho sits a hair under 0.6 (the rigid old 0.6 floor missed those).
    length_driven = rho_len >= 0.40 and (abs(rho) - rho_len) < 0.15
    if strong and not length_driven:
        return "USABLE (>=0.6, length-independent)"
    if strong and length_driven:
        return "LENGTH-CONFOUNDED (>=0.6 but tracks length)"
    if abs(rho) >= 0.3 and length_driven:
        return "WEAK+LENGTH-DRIVEN (rho~=rho_len)"
    if abs(rho) >= 0.3:
        return "WEAK (0.3-0.6)"
    return "ABSENT (<0.3)"


def main():
    difficulty = [d for d, _ in PROMPTS]
    prompt_bytes = [float(len(p.encode("utf-8"))) for _, p in PROMPTS]
    rho_len_diff = spearman(prompt_bytes, difficulty)

    print("# bet04c — INSTRUCT-model answer-confidence cascade (the fair Signal-1 test)")
    print(f"# model: {MODEL_NAME}  (Apache-2.0, instruction-tuned, multilingual)")
    print(f"# corpus: bet04's 30 difficulty-labeled Spanish prompts (6 per rank 1..5)")
    print(f"# N_GEN(greedy)={N_GEN}  K_SAMPLES={K_SAMPLES}@T={TEMP} (len {N_GEN_SAMPLE})")
    print(f"# length baseline: rho(prompt_bytes, difficulty) = {rho_len_diff:+.3f}  (bet04 reported 0.94)")
    print(f"# to beat: base-cascade entropy 0.56 ; compressibility 0.41 ; embedding -0.52 ; length 0.94")

    t0 = time.time()
    torch, tok, mdl = load_model(MODEL_NAME)
    print(f"# loaded in {time.time()-t0:.1f}s\n")

    sig_H: List[float] = []
    sig_NLP: List[float] = []
    sig_lenstd: List[float] = []
    sig_jac: List[float] = []
    answer_len: List[float] = []
    greedy_cap: List[float] = []
    sample_cap: List[float] = []

    print(f"# {'diff':>4} {'H(bits)':>8} {'NLP(bits)':>9} {'len_std':>7} {'jaccD':>6} {'ansLen':>6} {'gCap':>4} {'sCap':>4} {'pB':>4}  prompt")
    print("-" * 128)
    for diff, prompt in PROMPTS:
        ids = build_prompt_ids(torch, tok, prompt)
        g = greedy_confidence(torch, tok, mdl, ids, N_GEN)
        sc = self_consistency(torch, tok, mdl, ids, K_SAMPLES, TEMP, N_GEN_SAMPLE, SEED)
        sig_H.append(g["mean_entropy_bits"])
        sig_NLP.append(g["mean_neglogprob_bits"])
        sig_lenstd.append(sc["length_std"])
        sig_jac.append(sc["jaccard_dist"])
        answer_len.append(g["answer_len"])
        greedy_cap.append(g["greedy_capped"])
        sample_cap.append(sc["cap_rate"])
        pp = prompt if len(prompt) <= 40 else prompt[:37] + "..."
        print(f"# {diff:>4} {g['mean_entropy_bits']:>8.3f} {g['mean_neglogprob_bits']:>9.3f} "
              f"{sc['length_std']:>7.2f} {sc['jaccard_dist']:>6.3f} {g['answer_len']:>6.0f} "
              f"{g['greedy_capped']:>4.0f} {sc['cap_rate']:>4.1f} {len(prompt.encode()):>4}  {pp}")

    # Truncation exposure: how many prompts capped out. If capping correlates with
    # difficulty, then answer_len AND length_std are censored -> any correlation they
    # carry is a generation-cap artifact, not model behaviour.
    g_cap_frac = sum(greedy_cap) / len(greedy_cap)
    rho_gcap_diff = spearman(greedy_cap, difficulty)
    print(f"#\n# TRUNCATION EXPOSURE: greedy hit the {N_GEN}-tok cap on {sum(greedy_cap):.0f}/{len(PROMPTS)} "
          f"prompts ({g_cap_frac:.0%}); rho(greedy_capped, difficulty) = {rho_gcap_diff:+.3f}")
    print(f"#   -> answer_len and self-consistency length_std are CENSORED by the cap; treat any")
    print(f"#      correlation they carry as a truncation artifact, not a difficulty read.")

    signals = {
        "greedy entropy (SIG1)": sig_H,
        "greedy neg-logprob (SIG2)": sig_NLP,
        "self-consist len-std (SIG3a)": sig_lenstd,
        "self-consist jaccardD (SIG3b)": sig_jac,
    }

    print(f"\n# Correlation vs difficulty + length-confound (Spearman)")
    print(f"# {'signal':<30} {'rho_diff':>8} {'p(perm)':>8} {'rho_pLen':>8} {'rho_aLen':>8}   verdict")
    print("-" * 108)
    summary = {}
    for sname, vals in signals.items():
        rd = spearman(vals, difficulty)
        pd = perm_pvalue(vals, difficulty, spearman)
        rpl = spearman(vals, prompt_bytes)
        ral = spearman(vals, answer_len)
        v = verdict(rd, pd, rpl, ral)
        summary[sname] = (rd, pd, rpl, ral, v)
        print(f"# {sname:<30} {rd:>+8.3f} {pd:>8.4f} {rpl:>+8.3f} {ral:>+8.3f}   {v}")

    # answer_len itself vs difficulty (does the instruct model just write more for hard?)
    ral_diff = spearman(answer_len, difficulty)
    print(f"# {'(ref) greedy answer_len':<30} {ral_diff:>+8.3f} {'--':>8} {'--':>8} {'--':>8}   reference")

    print()
    print(ascii_scatter(sig_H, difficulty, f"{MODEL_NAME.split('/')[-1]} greedy entropy (bits)"))
    print()
    print(ascii_scatter(sig_NLP, difficulty, f"{MODEL_NAME.split('/')[-1]} greedy neg-logprob (bits)"))
    print()
    print(ascii_scatter(sig_jac, difficulty, "self-consistency jaccard distance"))

    print("\n# Routing split at median threshold (low signal -> cheap; high -> expensive):")
    for sname in signals:
        r = routing_sim(signals[sname], difficulty)
        print(f"#   {sname:<30} theta={r['theta']:.3f} cheap={r['n_cheap']}/{len(PROMPTS)} exp={r['n_exp']} "
              f"DANGEROUS hard->cheap={r['hard_sent_cheap']} wasteful easy->exp={r['easy_sent_exp']} "
              f"meanDiff cheap={r['mean_diff_cheap']:.2f} vs exp={r['mean_diff_exp']:.2f}")

    print(f"\n{'='*108}\n# DIRECT COMPARISON — Spearman rho vs difficulty (higher AND length-independent = better)\n{'='*108}")
    print(f"# {'signal':<40} {'rho':>7}   note")
    print("-" * 108)
    print(f"# {'raw byte length (bet04)':<40} {0.94:>7.2f}   IS the confound (refuted)")
    print(f"# {'base-cascade output-entropy (bet04b)':<40} {0.56:>7.2f}   base LM, not length-clean")
    print(f"# {'compressibility bits/tok (bet04)':<40} {0.41:>7.2f}   below 0.6 bar (refuted)")
    print(f"# {'embedding-density (bet05)':<40} {-0.52:>7.2f}   refuted (ADR-0021)")
    for sname, (rd, pd, rpl, ral, v) in summary.items():
        print(f"# {sname+' [instruct]':<40} {rd:>+7.2f}   rho_pLen={rpl:+.2f} rho_aLen={ral:+.2f} p={pd:.3f}  {v}")

    print(f"\n{'='*108}\n# VERDICT\n{'='*108}")
    usable = [(s, v[0]) for s, v in summary.items() if "USABLE" in v[4]]
    if usable:
        print("# At least one INSTRUCT answer-confidence signal is USABLE and length-independent (>=0.6):")
        for s, rd in usable:
            print(f"#   -> {s}  rho={rd:+.3f}")
        print("# => Bet-4 CASCADE CARVE-OUT VALIDATED: an instruct model's answer-confidence is a real,")
        print("#    length-independent difficulty router (off-compression-thesis cost-routing path).")
    else:
        best = max(summary.items(), key=lambda kv: abs(kv[1][0]))
        print("# NO instruct answer-confidence signal reaches a usable, length-independent rho>=0.6.")
        print(f"#   strongest: {best[0]} rho={best[1][0]:+.3f} ({best[1][4]})")
        print("# => Even a model that ANSWERS does not cleanly proxy difficulty on this corpus.")
        print("#    Difficulty routing stays genuinely hard: needs a trained classifier or a real")
        print("#    frontier-vs-cheap answer-quality gap, not cheap self-confidence.")
    print("#")
    print("# TRUNCATION NOTE: self-consist len-std correlates with difficulty ONLY because hard")
    print("#   answers saturate the token cap (all K samples share length -> std->0). It is a")
    print("#   CENSORING artifact, not self-consistency; excluded from any USABLE claim above.")
    print("# CAVEAT: a 0.5B model's OWN competence ceiling confounds 'hard for THIS model' vs")
    print("#   'hard in general'. N=30, single-annotator ordinal labels. Self-consistency uses K")
    print(f"#   ={K_SAMPLES} samples at T={TEMP}; more samples would tighten the disagreement estimate.")


if __name__ == "__main__":
    main()
