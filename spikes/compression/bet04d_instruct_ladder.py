"""
bet04d_instruct_ladder.py — the COMPETENCE-LADDER test bet04c motivated: was the
0.5B instruct-cascade failure a MODEL-COMPETENCE ceiling (a 0.5B model too weak to
have discriminating self-confidence) or a MECHANISM failure (self-confidence just
does not route difficulty, at any size)?

Context (docs/decisions/0021-...md §cascade carve-out; docs/research/bet-04-...md;
spikes/compression/bet04c_instruct_cascade.py):
  bet04c ran the cascade IDEA with a real INSTRUCT model, Qwen2.5-0.5B-Instruct.
  Its answer-confidence signals landed at rho(difficulty) = 0.42-0.50 — length-
  driven and BELOW the base-model cascade's 0.56 — so the cascade carve-out was
  marked "tested & refuted" AT 0.5B. bet04c ALSO caught an artifact we must avoid:
  its self-consistency LENGTH-STD metric hit rho = -0.61 ONLY because hard prompts
  saturated the token cap (all K samples share length == cap -> std -> 0). That is
  a generation-cap CENSORING effect, not self-consistency. This spike therefore
  drops length-std entirely and uses a CAP-SAFE embedding-agreement metric.

  The open question bet04c left: 0.5B might simply be too weak to HAVE calibrated
  confidence. This spike answers it with a two-point ladder of the SAME 30 prompts:
      Qwen2.5-0.5B-Instruct  (baseline)
      Qwen2.5-1.5B-Instruct  (3x params — the primary "more competent" datapoint)
  Qwen2.5-3B-Instruct is DELIBERATELY EXCLUDED: its HF license is the Qwen Research
  License ("other"), NOT Apache-2.0 (verified via huggingface_hub.model_info), so it
  violates aisotropy's 100%-permissive stack gate. It is not a CPU-budget skip; it
  is a license skip. A clean 0.5B -> 1.5B ladder is the deliverable.

  KEY READ: does rho(difficulty) RISE 0.5B -> 1.5B? A rising trend past the 0.6 bar
  (with a >=0.2 gap over the length confound) => the 0.5B competence ceiling WAS the
  problem and cascade routing is viable with a competent cheap model. rho staying in
  the ~0.5 length-driven band as competence triples => MECHANISM failure: self-
  confidence is not a difficulty router regardless of model size.

Confidence signals per model (FIXED to avoid bet04c's artifacts):
  1. GREEDY MEAN PER-TOKEN ENTROPY (bits) over a fixed max_new_tokens. High = uncertain.
     Reports greedy CAP-RATE so any residual truncation confound stays visible.
  2. GREEDY MEAN NEG-LOGPROB (bits/token). High = uncertain.
  3. SELF-CONSISTENCY by EMBEDDING AGREEMENT (cap-safe, NOT length/Jaccard): sample K
     answers at T=0.7, embed each with intfloat/multilingual-e5-small (MIT), compute
     MEAN PAIRWISE COSINE DISTANCE = disagreement. High disagreement = uncertain.
     This does not depend on answer length, so the cap artifact cannot forge it.
     Reports sample CAP-RATE too, purely as an observability check.

Analysis (mirrors bet04 / bet04c; reuses bet04's corpus + stats + bet05's e5 embedder):
  * Per model, per signal: Spearman rho(signal, difficulty) + permutation p.
  * LENGTH CONFOUND per signal: rho(signal, prompt_bytes) AND rho(signal, answer_len).
    KILL CRITERION = the GAP: rho(difficulty) must exceed the larger length rho by
    >= 0.20 to count as a REAL difficulty signal (not length in disguise).
  * The ladder table: rho(difficulty) for each signal at 0.5B vs 1.5B, + trend.
  * Compared to: length 0.94, compressibility 0.41, base-cascade 0.56, 0.5B 0.42-0.50.

Usage:
    .venv/bin/python bet04d_instruct_ladder.py
    LADDER="Qwen/Qwen2.5-0.5B-Instruct,Qwen/Qwen2.5-1.5B-Instruct" \
        N_GEN=96 K_SAMPLES=4 .venv/bin/python bet04d_instruct_ladder.py
"""

from __future__ import annotations

import math
import os
import time
from typing import Dict, List, Sequence, Tuple

# Reuse the EXACT bet-04 corpus + stats -> apples-to-apples with every refuted signal.
import bet04_compressibility_routing as bet04
# Reuse bet05's sovereign e5 embedder (MIT, multilingual) for the cap-safe metric.
# NOTE: importing bet05 loads the e5 model at import time (its documented side effect).
import bet05_embedding_density_probe as bet05

PROMPTS: List[Tuple[int, str]] = bet04.PROMPTS
spearman = bet04.spearman
perm_pvalue = bet04.perm_pvalue
ascii_scatter = bet04.ascii_scatter
embed = bet05.embed          # mean-pooled, L2-normalized e5 embeddings
cos = bet05.cos              # cosine of two already-normalized vectors

# Ladder: 0.5B baseline + 1.5B primary datapoint. 3B excluded on LICENSE grounds
# (Qwen Research License, not Apache-2.0). Both below are Apache-2.0 (verified).
DEFAULT_LADDER = "Qwen/Qwen2.5-0.5B-Instruct,Qwen/Qwen2.5-1.5B-Instruct"
LADDER = [m.strip() for m in os.environ.get("LADDER", DEFAULT_LADDER).split(",") if m.strip()]

N_GEN = int(os.environ.get("N_GEN", "96"))               # greedy answer length (tokens)
K_SAMPLES = int(os.environ.get("K_SAMPLES", "4"))         # self-consistency samples
TEMP = float(os.environ.get("SAMPLE_TEMP", "0.7"))
N_GEN_SAMPLE = int(os.environ.get("N_GEN_SAMPLE", "64"))  # shorter samples to save CPU
SEED = int(os.environ.get("SEED", "0"))

# Same Spanish assistant system message as bet04c (deployed-assistant framing).
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
    n_params = sum(p.numel() for p in mdl.parameters())
    return torch, tok, mdl, n_params


def build_prompt_ids(torch, tok, user_text: str):
    msgs = [{"role": "system", "content": SYSTEM_MSG},
            {"role": "user", "content": user_text}]
    enc = tok.apply_chat_template(
        msgs, add_generation_prompt=True, return_tensors="pt", return_dict=True
    )
    return enc["input_ids"]


# --------------------------------------------------------------------------
# SIGNAL 1 + 2 — greedy answer confidence (unchanged from bet04c; the two greedy
# signals are IDENTICAL to bet04c's so the 0.5B row here reproduces bet04c).
#   entropy_i    = H(softmax(logits_i))     -> mean = SIGNAL 1
#   neglogprob_i = -log2 P(argmax_i)        -> mean = SIGNAL 2   (both in bits)
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
    scores = out.scores
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
        if chosen == eos:
            break
        entropies.append(H_nats / ln2)
        neglogps.append(-logp[chosen].item() / ln2)
        n_real += 1
    if not entropies:
        logits = scores[0][0]
        logp = torch.log_softmax(logits, dim=-1)
        p = torch.exp(logp)
        entropies = [-(p * logp).sum().item() / ln2]
        neglogps = [-logp[int(gen_tokens[0].item())].item() / ln2]
        n_real = 1
    greedy_capped = 1.0 if n_real >= n_gen else 0.0
    return {
        "mean_entropy_bits": sum(entropies) / len(entropies),
        "mean_neglogprob_bits": sum(neglogps) / len(neglogps),
        "answer_len": float(n_real),
        "greedy_capped": greedy_capped,
    }


# --------------------------------------------------------------------------
# SIGNAL 3 — CAP-SAFE self-consistency via EMBEDDING agreement.
# Sample K answers at temperature; embed each with e5; the disagreement is the
# MEAN PAIRWISE COSINE DISTANCE (1 - cos) over the K answer embeddings. High =
# the model says semantically DIFFERENT things each time = uncertain. Crucially
# length-independent: a hard prompt whose K samples all saturate the token cap
# (identical length) can STILL disagree semantically, so the cap cannot forge
# this signal the way it forged bet04c's length-std.
# --------------------------------------------------------------------------
def self_consistency_embed(torch, tok, mdl, ids, k: int, temp: float, n_gen: int,
                           seed: int) -> Dict[str, float]:
    texts: List[str] = []
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
        lengths.append(len(toks))
        txt = tok.decode(gen, skip_special_tokens=True).strip()
        texts.append(txt if txt else " ")  # guard: never embed the empty string
    # Embed all K answers in one batch; mean pairwise cosine DISTANCE.
    embs = embed(texts)  # (K, H), L2-normalized
    dists: List[float] = []
    for i in range(len(texts)):
        for j in range(i + 1, len(texts)):
            dists.append(1.0 - cos(embs[i], embs[j]))
    embed_disagree = sum(dists) / len(dists) if dists else 0.0
    cap_rate = sum(1 for L in lengths if L >= n_gen) / len(lengths)
    return {"embed_disagree": embed_disagree, "cap_rate": cap_rate,
            "mean_sample_len": sum(lengths) / len(lengths)}


def gap_verdict(rho: float, p: float, rho_plen: float, rho_alen: float) -> Tuple[str, float]:
    """Kill criterion: a REAL difficulty signal needs |rho(diff)| >= 0.6, p<0.05,
    AND a >= 0.20 GAP over the larger length correlation. Returns (verdict, gap)."""
    rho_len = max(abs(rho_plen), abs(rho_alen))
    gap = abs(rho) - rho_len
    strong = abs(rho) >= 0.6 and p < 0.05
    if strong and gap >= 0.20:
        return "USABLE (>=0.6, gap>=0.20 over length)", gap
    if strong and gap < 0.20:
        return "LENGTH-CONFOUNDED (>=0.6 but gap<0.20)", gap
    if abs(rho) >= 0.3 and gap < 0.20:
        return "WEAK+LENGTH-DRIVEN", gap
    if abs(rho) >= 0.3:
        return "WEAK (0.3-0.6)", gap
    return "ABSENT (<0.3)", gap


def run_model(name: str, difficulty, prompt_bytes) -> Dict:
    t0 = time.time()
    torch, tok, mdl, n_params = load_model(name)
    load_s = time.time() - t0

    sig_H: List[float] = []
    sig_NLP: List[float] = []
    sig_ED: List[float] = []
    answer_len: List[float] = []
    greedy_cap: List[float] = []
    sample_cap: List[float] = []

    print(f"\n{'#'*112}")
    print(f"# MODEL: {name}  ({n_params/1e6:.1f}M params)  loaded in {load_s:.1f}s")
    print(f"# N_GEN(greedy)={N_GEN}  K_SAMPLES={K_SAMPLES}@T={TEMP} (sample len {N_GEN_SAMPLE})")
    print(f"{'#'*112}")
    print(f"# {'diff':>4} {'H(bits)':>8} {'NLP(bits)':>9} {'embDis':>7} {'ansLen':>6} "
          f"{'gCap':>4} {'sCap':>4} {'pB':>4}  prompt")
    print("-" * 112)
    t1 = time.time()
    for diff, prompt in PROMPTS:
        ids = build_prompt_ids(torch, tok, prompt)
        g = greedy_confidence(torch, tok, mdl, ids, N_GEN)
        sc = self_consistency_embed(torch, tok, mdl, ids, K_SAMPLES, TEMP, N_GEN_SAMPLE, SEED)
        sig_H.append(g["mean_entropy_bits"])
        sig_NLP.append(g["mean_neglogprob_bits"])
        sig_ED.append(sc["embed_disagree"])
        answer_len.append(g["answer_len"])
        greedy_cap.append(g["greedy_capped"])
        sample_cap.append(sc["cap_rate"])
        pp = prompt if len(prompt) <= 40 else prompt[:37] + "..."
        print(f"# {diff:>4} {g['mean_entropy_bits']:>8.3f} {g['mean_neglogprob_bits']:>9.3f} "
              f"{sc['embed_disagree']:>7.3f} {g['answer_len']:>6.0f} {g['greedy_capped']:>4.0f} "
              f"{sc['cap_rate']:>4.1f} {len(prompt.encode()):>4}  {pp}")
    gen_s = time.time() - t1

    g_cap_frac = sum(greedy_cap) / len(greedy_cap)
    s_cap_frac = sum(sample_cap) / len(sample_cap)
    rho_gcap_diff = spearman(greedy_cap, difficulty)
    print(f"#\n# CAP EXPOSURE: greedy hit the {N_GEN}-tok cap on {sum(greedy_cap):.0f}/{len(PROMPTS)} "
          f"({g_cap_frac:.0%}); mean sample cap-rate={s_cap_frac:.0%}; "
          f"rho(greedy_capped,difficulty)={rho_gcap_diff:+.3f}")
    print(f"#   -> greedy entropy/neg-logprob are cap-affected ONLY where gCap=1; the embedding")
    print(f"#      disagreement metric is cap-INDEPENDENT by construction (semantic, not length).")
    print(f"# gen time for 30 prompts: {gen_s:.0f}s")

    signals = {
        "greedy entropy (SIG1)": sig_H,
        "greedy neg-logprob (SIG2)": sig_NLP,
        "self-consist embedDisagree (SIG3)": sig_ED,
    }

    print(f"\n# Correlation vs difficulty + length-confound (Spearman)  [model: {name}]")
    print(f"# {'signal':<34} {'rho_diff':>8} {'p(perm)':>8} {'rho_pLen':>8} {'rho_aLen':>8} "
          f"{'gap':>6}   verdict")
    print("-" * 118)
    summary = {}
    for sname, vals in signals.items():
        rd = spearman(vals, difficulty)
        pd = perm_pvalue(vals, difficulty, spearman)
        rpl = spearman(vals, prompt_bytes)
        ral = spearman(vals, answer_len)
        v, gap = gap_verdict(rd, pd, rpl, ral)
        summary[sname] = {"rho": rd, "p": pd, "rho_plen": rpl, "rho_alen": ral,
                          "gap": gap, "verdict": v}
        print(f"# {sname:<34} {rd:>+8.3f} {pd:>8.4f} {rpl:>+8.3f} {ral:>+8.3f} {gap:>+6.2f}   {v}")

    ral_diff = spearman(answer_len, difficulty)
    print(f"# {'(ref) greedy answer_len vs diff':<34} {ral_diff:>+8.3f}")

    return {
        "name": name, "params_m": n_params / 1e6, "summary": summary,
        "g_cap_frac": g_cap_frac, "s_cap_frac": s_cap_frac,
        "answer_len_rho": ral_diff, "gen_s": gen_s,
    }


def main():
    difficulty = [d for d, _ in PROMPTS]
    prompt_bytes = [float(len(p.encode("utf-8"))) for _, p in PROMPTS]
    rho_len_diff = spearman(prompt_bytes, difficulty)

    print("# bet04d — INSTRUCT COMPETENCE LADDER: does answer-confidence route difficulty")
    print("#          BETTER as the model gets more competent (0.5B -> 1.5B)?")
    print(f"# corpus: bet04's 30 difficulty-labeled Spanish prompts (6 per rank 1..5)")
    print(f"# ladder: {LADDER}")
    print(f"# length baseline: rho(prompt_bytes, difficulty) = {rho_len_diff:+.3f}  (bet04 reported 0.94)")
    print(f"# to beat: base-cascade 0.56 ; compressibility 0.41 ; 0.5B-instruct 0.42-0.50 ; length 0.94")
    print(f"# KILL CRITERION: a signal counts as a REAL difficulty router only if")
    print(f"#   |rho(difficulty)| >= 0.60, p<0.05, AND gap>=0.20 over max(|rho_promptLen|,|rho_ansLen|).")
    print(f"# NOTE: Qwen2.5-3B-Instruct EXCLUDED — Qwen Research License ('other'), not Apache-2.0.")

    runs = [run_model(name, difficulty, prompt_bytes) for name in LADDER]

    # -------- The ladder table: does rho(difficulty) rise with competence? --------
    sig_names = ["greedy entropy (SIG1)", "greedy neg-logprob (SIG2)",
                 "self-consist embedDisagree (SIG3)"]
    print(f"\n{'='*118}")
    print("# COMPETENCE LADDER — Spearman rho(signal, difficulty) as params rise")
    print(f"{'='*118}")
    header = f"# {'signal':<34}" + "".join(f"{r['name'].split('/')[-1].replace('Qwen2.5-','').replace('-Instruct',''):>16}" for r in runs) + f"{'trend':>10}"
    print(header)
    print("-" * 118)
    for sn in sig_names:
        cells = ""
        rhos = []
        for r in runs:
            s = r["summary"][sn]
            rhos.append(s["rho"])
            cells += f"{s['rho']:>+9.2f}(g{s['gap']:+.2f})"[:16].rjust(16)
        trend = (rhos[-1] - rhos[0]) if len(rhos) >= 2 else 0.0
        arrow = "RISE" if trend >= 0.10 else ("FALL" if trend <= -0.10 else "FLAT")
        print(f"# {sn:<34}" + cells + f"{arrow:>6}{trend:>+5.2f}")
    print(f"#\n# cell = rho(difficulty) with (g=gap over length). RISE = +>=0.10 from smallest to largest.")

    # cap-rate row for context
    print(f"#\n# {'greedy cap-rate':<34}" + "".join(f"{r['g_cap_frac']:>15.0%} " for r in runs))
    print(f"# {'sample cap-rate':<34}" + "".join(f"{r['s_cap_frac']:>15.0%} " for r in runs))
    print(f"# {'answer_len rho(diff)':<34}" + "".join(f"{r['answer_len_rho']:>+15.2f} " for r in runs))

    # -------- Direct comparison to all prior refuted/known signals --------
    print(f"\n{'='*118}\n# DIRECT COMPARISON — rho vs difficulty (higher AND gap>=0.20 over length = better)\n{'='*118}")
    print(f"# {'signal':<44} {'rho':>7}   note")
    print("-" * 118)
    print(f"# {'raw byte length (bet04)':<44} {0.94:>7.2f}   IS the confound")
    print(f"# {'base-cascade output-entropy (bet04b)':<44} {0.56:>7.2f}   base LM, not length-clean")
    print(f"# {'0.5B-instruct answer-conf (bet04c)':<44} {'0.42-0.50':>7}   length-driven (refuted at 0.5B)")
    print(f"# {'compressibility bits/tok (bet04)':<44} {0.41:>7.2f}   below bar")
    print(f"# {'embedding-density (bet05)':<44} {-0.52:>7.2f}   refuted (ADR-0021)")
    for r in runs:
        tag = r["name"].split("/")[-1]
        for sn in sig_names:
            s = r["summary"][sn]
            print(f"# {tag+' '+sn:<44} {s['rho']:>+7.2f}   gap={s['gap']:+.2f} p={s['p']:.3f}  {s['verdict']}")

    # -------- Verdict --------
    print(f"\n{'='*118}\n# VERDICT\n{'='*118}")
    usable = []
    for r in runs:
        for sn in sig_names:
            s = r["summary"][sn]
            if "USABLE" in s["verdict"]:
                usable.append((r["name"], sn, s["rho"], s["gap"]))
    # trend per signal
    rising = []
    for sn in sig_names:
        rhos = [r["summary"][sn]["rho"] for r in runs]
        if len(rhos) >= 2 and (rhos[-1] - rhos[0]) >= 0.10:
            rising.append((sn, rhos[0], rhos[-1]))

    if usable:
        print("# At least one signal is USABLE (>=0.6, gap>=0.20 over length):")
        for name, sn, rho, gap in usable:
            print(f"#   -> {name.split('/')[-1]} / {sn}: rho={rho:+.3f} gap={gap:+.2f}")
        print("# => the 0.5B ceiling WAS (at least partly) the problem: a more competent cheap")
        print("#    instruct model gives a usable, length-independent difficulty signal.")
    else:
        print("# NO signal at ANY rung reaches USABLE (>=0.6 AND gap>=0.20 over length).")
    if rising:
        print("# Signals whose rho(difficulty) ROSE with competence (>=+0.10 across the ladder):")
        for sn, lo, hi in rising:
            print(f"#   -> {sn}: {lo:+.2f} -> {hi:+.2f}")
        print("#    A rising-but-still-sub-0.6 trend hints the ceiling matters but 1.5B is not enough.")
    else:
        print("# rho(difficulty) did NOT rise with competence -> tripling params did not help.")
        print("# => MECHANISM FAILURE: self-confidence is not a difficulty router regardless of size,")
        print("#    consistent with RouteLLM/FrugalGPT (self-confidence is weak; the robust router is a")
        print("#    TRAINED classifier or a cheap-vs-frontier ANSWER-QUALITY gap, not self-confidence).")
    print("#")
    print("# UNTESTED RUNG: a FRONTIER model (>>1.5B, beyond CPU here) is the only rung not measured.")
    print("#   If mechanism failure holds at 0.5B AND 1.5B with no upward trend, a frontier jump is")
    print("#   unlikely to convert self-confidence into a clean router; a trained/answer-gap router is")
    print("#   the recommended path either way.")
    print("# CAVEATS: N=30 single-annotator ordinal labels; CPU forces the model-size cap at 1.5B;")
    print("#   'hard for THIS model' (competence) is conflated with 'hard in general' (difficulty);")
    print(f"#   self-consistency uses K={K_SAMPLES} samples at T={TEMP} (more would tighten the estimate).")


if __name__ == "__main__":
    main()
