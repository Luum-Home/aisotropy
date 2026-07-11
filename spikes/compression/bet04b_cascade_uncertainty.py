"""
bet04b_cascade_uncertainty.py — the ONE remaining on-thesis test of Bet 4's GOAL.

Bet 4's goal (difficulty-aware routing: send hard prompts to an expensive model,
easy prompts to a cheap one) survives ADR-0021 only if difficulty can be read from
a CHEAP, length-INDEPENDENT signal. ADR-0021 refuted every PROMPT-INTRINSIC TEXT
signal on the SAME 30-prompt corpus:
    compressibility bits/token   rho = 0.41   (below the 0.6 bar)
    raw byte length              rho = 0.94   (every signal that clears 0.6 is
                                               length in disguise)
    embedding-density (bet05)    also refuted
Established finding: difficulty is COMPUTATIONAL, not textual. Prompt text does not
carry it.

This spike tests the last on-thesis angle that is NOT a prompt-text property: the
cheap MODEL's OWN behaviour (cascade routing), on the exact same 30 prompts, so the
numbers are directly comparable to the refuted ones.

Two signals, both in bits, both from a cheap base LM (distilgpt2 and gpt2-spanish):

  SIGNAL 1 — OUTPUT-UNCERTAINTY (the cascade signal).
    Greedily generate a short continuation (N_GEN tokens) after the prompt and take
    the MEAN per-token Shannon entropy (bits) of the model's next-token distribution
    over that continuation = "how uncertain the cheap model is once it starts to
    answer here". Cascade hypothesis: harder prompt -> cheap model more uncertain ->
    escalate to the expensive model.
    This is a MODEL-BEHAVIOUR signal, not a prompt-text signal — the whole point.

  SIGNAL 2 — PROMPT-PERPLEXITY UNDER A SYSTEM/ASSISTANT PREFIX.
    The bet-04 variant the prior spike explicitly did NOT run: it scored the prompt
    with context="" (surprisal "in a vacuum", rho=0.41). Here we score the prompt's
    bits/token conditioned on a realistic assistant SYSTEM PREFIX. Hypothesis: with
    proper conditioning the perplexity tracks difficulty better than the vacuum run.
    We ALSO reproduce the vacuum run (context="") as an in-script control, so any
    lift is attributable to the conditioning and not to a different model/tokenizer.

Analysis (mirrors bet04 exactly, reusing its corpus + stats):
  * Spearman rho of each signal vs the human difficulty labels, with permutation p.
  * Length-confound check per signal: rho(signal, raw_byte_length). A signal that
    only correlates via length is refuted like the others.
  * Routing-split sim at the median threshold: dangerous misroutes (hard -> cheap).
  * Direct comparison table vs the refuted signals (0.41 compressibility, 0.94 len).

IMPORTANT CAVEAT (stated in the report too): distilgpt2 / DeepESP/gpt2-spanish are
BASE LMs, NOT instruction-tuned assistants. Their generation on reasoning prompts
can be high-entropy EVERYWHERE (they ramble regardless of difficulty), which would
make the cascade signal uninformative through no fault of the routing IDEA. This
spike therefore distinguishes "the cascade idea fails" from "these base models
cannot test it".

Usage:
    .venv/bin/python bet04b_cascade_uncertainty.py
    N_GEN=32 .venv/bin/python bet04b_cascade_uncertainty.py
"""

from __future__ import annotations

import math
import os
import sys
import time
from typing import Dict, List, Sequence, Tuple

# Reuse the EXACT bet-04 corpus + stats so this is apples-to-apples with the
# refuted compressibility / length / embedding-density signals.
import bet04_compressibility_routing as bet04

PROMPTS: List[Tuple[int, str]] = bet04.PROMPTS
spearman = bet04.spearman
pearson = bet04.pearson
perm_pvalue = bet04.perm_pvalue
ascii_scatter = bet04.ascii_scatter

# Realistic Spanish assistant system prefix for SIGNAL 2 (the untested variant).
# This is what a real deployment would prepend before the user's query; it is the
# "proper conditioning" the prior context="" run lacked.
SYSTEM_PREFIX = (
    "Sos un asistente de inteligencia artificial que responde en español de forma "
    "clara, precisa y útil. El usuario te hace la siguiente consulta: "
)

N_GEN = int(os.environ.get("N_GEN", "28"))  # continuation length for SIGNAL 1
MODELS = ["distilgpt2", "DeepESP/gpt2-spanish"]

_HF_CACHE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".hf_cache")


# --------------------------------------------------------------------------
# Model loading (both models directly, so both can be tested in one run;
# the surprisal.py singleton only holds one model at a time).
# --------------------------------------------------------------------------
def load_model(name: str):
    os.environ.setdefault("HF_HOME", _HF_CACHE_DIR)
    os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
    import torch  # type: ignore
    from transformers import AutoTokenizer, AutoModelForCausalLM  # type: ignore

    tok = AutoTokenizer.from_pretrained(name)
    mdl = AutoModelForCausalLM.from_pretrained(name)
    mdl.eval()
    return torch, tok, mdl


# --------------------------------------------------------------------------
# SIGNAL 2 scorer — mirrors surprisal.py::_surprisal_bits_lm EXACTLY
# (same BOS handling for empty context, same "logit i predicts token i+1"),
# but parameterized by model so we can (a) pass a real system prefix and
# (b) run both models. context="" reproduces the refuted vacuum run.
# --------------------------------------------------------------------------
def prompt_bits(torch, tok, mdl, text: str, context: str) -> Dict[str, float]:
    ctx_ids = tok(context, return_tensors="pt")["input_ids"] if context else None
    if ctx_ids is None or ctx_ids.shape[1] == 0:
        bos_id = tok.bos_token_id if tok.bos_token_id is not None else tok.eos_token_id
        ctx_ids = torch.tensor([[bos_id]])
    text_ids = tok(text, return_tensors="pt")["input_ids"]
    full_ids = torch.cat([ctx_ids, text_ids], dim=1)
    n_ctx = ctx_ids.shape[1]
    n_text = text_ids.shape[1]
    with torch.no_grad():
        logits = mdl(full_ids).logits[0]
    log_probs = torch.log_softmax(logits, dim=-1)
    seq = full_ids[0]
    total_bits = 0.0
    for i in range(n_ctx, n_ctx + n_text):
        logp = log_probs[i - 1, seq[i].item()].item()
        total_bits += -logp / math.log(2)
    return {
        "bits_total": total_bits,
        "bits_per_token": total_bits / n_text if n_text else 0.0,
        "n_tokens": float(n_text),
    }


# --------------------------------------------------------------------------
# SIGNAL 1 scorer — output-uncertainty via greedy generation.
# Generate N_GEN tokens greedily (KV-cached) and return the MEAN per-token
# Shannon entropy (bits) of the next-token distribution over the continuation.
# Entropy is computed over the FULL distribution at each step, independent of
# which token greedy decoding picks.
# --------------------------------------------------------------------------
def output_entropy_bits(torch, tok, mdl, text: str, n_gen: int) -> Dict[str, float]:
    ids = tok(text, return_tensors="pt")["input_ids"]
    ln2 = math.log(2)
    entropies: List[float] = []
    past = None
    cur = ids
    with torch.no_grad():
        for _ in range(n_gen):
            out = mdl(cur, past_key_values=past, use_cache=True)
            past = out.past_key_values
            logits = out.logits[0, -1]  # next-token logits
            logp = torch.log_softmax(logits, dim=-1)
            p = torch.exp(logp)
            # H = -sum p*log2 p  (bits); done in nats then / ln2
            H_nats = -(p * logp).sum().item()
            entropies.append(H_nats / ln2)
            nxt = int(torch.argmax(logits).item())
            cur = torch.tensor([[nxt]])
    mean_H = sum(entropies) / len(entropies) if entropies else 0.0
    return {
        "mean_entropy_bits": mean_H,
        "max_entropy_bits": max(entropies) if entropies else 0.0,
        "min_entropy_bits": min(entropies) if entropies else 0.0,
    }


# --------------------------------------------------------------------------
# Routing-split sim at the median threshold (reuses bet04's semantics but
# returns structured numbers so we can tabulate misroutes across signals).
# --------------------------------------------------------------------------
def routing_sim(signal: Sequence[float], difficulty: Sequence[int]) -> Dict[str, float]:
    pairs = sorted(zip(signal, difficulty))
    n = len(pairs)
    theta = pairs[n // 2][0]
    cheap = [d for s, d in zip(signal, difficulty) if s < theta]
    exp = [d for s, d in zip(signal, difficulty) if s >= theta]
    hard_sent_cheap = sum(1 for d in cheap if d >= 4)   # DANGEROUS (quality loss)
    easy_sent_exp = sum(1 for d in exp if d <= 2)        # wasteful ($$)
    return {
        "theta": theta,
        "n_cheap": len(cheap),
        "n_exp": len(exp),
        "hard_sent_cheap": hard_sent_cheap,
        "easy_sent_exp": easy_sent_exp,
        "mean_diff_cheap": (sum(cheap) / len(cheap)) if cheap else float("nan"),
        "mean_diff_exp": (sum(exp) / len(exp)) if exp else float("nan"),
    }


def verdict(rho: float, p: float, rho_len: float) -> str:
    strong = abs(rho) >= 0.6 and p < 0.05
    length_driven = abs(rho_len) >= 0.6 and abs(rho) - abs(rho_len) < 0.15
    if strong and not length_driven:
        return "USABLE (>=0.6, length-independent)"
    if strong and length_driven:
        return "LENGTH-CONFOUNDED (>=0.6 but tracks length)"
    if abs(rho) >= 0.3:
        return "WEAK (0.3-0.6)"
    return "ABSENT (<0.3)"


# --------------------------------------------------------------------------
def run_for_model(name: str, difficulty: List[int], raw_len: List[float]):
    print(f"\n{'#'*78}\n# MODEL: {name}   (base LM, NOT instruction-tuned — see caveat)\n{'#'*78}")
    t0 = time.time()
    torch, tok, mdl = load_model(name)
    print(f"# loaded in {time.time()-t0:.1f}s ; N_GEN={N_GEN} ; N prompts={len(PROMPTS)}")

    sig_entropy: List[float] = []
    sig_ppl_ctx: List[float] = []   # SIGNAL 2: prompt bits/tok WITH system prefix
    sig_ppl_vac: List[float] = []   # control: prompt bits/tok in a vacuum (context="")

    print(f"\n# {'diff':>4} {'H_out(bits)':>11} {'ppl_ctx':>8} {'ppl_vac':>8} {'rawB':>5}  prompt")
    print("-" * 110)
    for diff, prompt in PROMPTS:
        h = output_entropy_bits(torch, tok, mdl, prompt, N_GEN)["mean_entropy_bits"]
        pc = prompt_bits(torch, tok, mdl, prompt, SYSTEM_PREFIX)["bits_per_token"]
        pv = prompt_bits(torch, tok, mdl, prompt, "")["bits_per_token"]
        sig_entropy.append(h)
        sig_ppl_ctx.append(pc)
        sig_ppl_vac.append(pv)
        p = prompt if len(prompt) <= 52 else prompt[:49] + "..."
        print(f"# {diff:>4} {h:>11.3f} {pc:>8.3f} {pv:>8.3f} {len(prompt.encode()):>5}  {p}")

    signals = {
        "output-entropy (SIG1)": sig_entropy,
        "prompt-ppl w/ ctx (SIG2)": sig_ppl_ctx,
        "prompt-ppl vacuum (ctrl)": sig_ppl_vac,
    }

    print(f"\n# Correlation vs difficulty (Spearman) + length-confound rho(signal,len)")
    print(f"# {'signal':<26} {'rho_diff':>9} {'p(perm)':>8} {'rho_len':>8}   verdict")
    print("-" * 92)
    summary = {}
    for sname, vals in signals.items():
        rd = spearman(vals, difficulty)
        pd = perm_pvalue(vals, difficulty, spearman)
        rl = spearman(vals, raw_len)
        v = verdict(rd, pd, rl)
        summary[sname] = (rd, pd, rl, v)
        print(f"# {sname:<26} {rd:>+9.3f} {pd:>8.4f} {rl:>+8.3f}   {v}")

    # Scatters for the two NEW signals
    print()
    print(ascii_scatter(sig_entropy, difficulty, f"{name} output-entropy (bits)"))
    print()
    print(ascii_scatter(sig_ppl_ctx, difficulty, f"{name} prompt-ppl w/ system ctx (bits/tok)"))

    # Routing sims for the two new signals
    print("\n# Routing split at median threshold:")
    for sname in ["output-entropy (SIG1)", "prompt-ppl w/ ctx (SIG2)"]:
        r = routing_sim(signals[sname], difficulty)
        print(
            f"#   {sname:<26} theta={r['theta']:.3f}  cheap={r['n_cheap']}/{len(PROMPTS)} exp={r['n_exp']}  "
            f"DANGEROUS hard->cheap={r['hard_sent_cheap']}  wasteful easy->exp={r['easy_sent_exp']}  "
            f"meanDiff cheap={r['mean_diff_cheap']:.2f} vs exp={r['mean_diff_exp']:.2f}"
        )
    return summary


def main():
    difficulty = [d for d, _ in PROMPTS]
    raw_len = [float(len(p.encode("utf-8"))) for _, p in PROMPTS]

    # Length baseline on THIS corpus (should reproduce bet04's rho=0.94).
    rho_len_diff = spearman(raw_len, difficulty)

    print("# bet04b — cascade / output-uncertainty routing (the on-thesis reframe of Bet 4)")
    print(f"# corpus: bet04's 30 difficulty-labeled Spanish prompts (6 per rank 1..5)")
    print(f"# length baseline: rho(raw_len, difficulty) = {rho_len_diff:+.3f}  (bet04 reported 0.94)")
    print(f"# refuted signals to beat: compressibility bits/tok rho=0.41 ; raw length rho=0.94")

    all_summ = {}
    for m in MODELS:
        all_summ[m] = run_for_model(m, difficulty, raw_len)

    # Direct comparison table
    print(f"\n{'='*92}\n# DIRECT COMPARISON — Spearman rho vs difficulty (higher & length-independent = better)\n{'='*92}")
    print(f"# {'signal':<34} {'rho':>7}   {'length-confound / note'}")
    print("-" * 92)
    print(f"# {'raw byte length (bet04)':<34} {0.94:>7.2f}   IS the confound (refuted)")
    print(f"# {'compressibility bits/tok (bet04)':<34} {0.41:>7.2f}   below 0.6 bar (refuted)")
    print(f"# {'embedding-density (bet05)':<34} {'--':>7}   refuted (ADR-0021)")
    for m in MODELS:
        for sname, (rd, pd, rl, v) in all_summ[m].items():
            tag = f"{m.split('/')[-1]}"
            print(f"# {sname+' ['+tag+']':<34} {rd:>+7.2f}   rho_len={rl:+.2f}  p={pd:.3f}  {v}")

    print(f"\n{'='*92}\n# VERDICT\n{'='*92}")
    usable = []
    for m in MODELS:
        for sname, (rd, pd, rl, v) in all_summ[m].items():
            if "USABLE" in v:
                usable.append(f"{sname} [{m.split('/')[-1]}] rho={rd:+.2f}")
    if usable:
        print("# At least one signal is USABLE and length-independent (>=0.6):")
        for u in usable:
            print(f"#   -> {u}")
        print("# => Bet 4's GOAL is SALVAGEABLE via cascade/uncertainty routing (mechanism, not compressibility).")
    else:
        print("# NO signal reaches a usable, length-independent rho>=0.6.")
        print("# => Bet 4 and its goal stay CLOSED for cheap prompt/base-model signals.")
        print("#    Difficulty routing needs a trained classifier or a real assistant-model cascade")
        print("#    (a separate, off-compression-thesis bet).")
    print("#")
    print("# CAVEAT: distilgpt2 / gpt2-spanish are BASE LMs, not instruction-tuned assistants.")
    print("#   If output-entropy is high & flat across difficulty, that is a BASE-MODEL limitation")
    print("#   (they ramble regardless of difficulty), not necessarily a refutation of the cascade")
    print("#   IDEA. An instruction-tuned cheap model is the fair test of Signal 1.")


if __name__ == "__main__":
    main()
