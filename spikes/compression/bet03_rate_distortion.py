"""
bet03_rate_distortion.py — tests (or refutes) Bet 3: "context as rate-distortion".

Hypothesis (docs/research/bet-03-rate-distortion-context.md):
  Context assembly is a RATE-DISTORTION problem (Shannon). Given a token budget
  B (the RATE), choose the memory subset that MINIMIZES task distortion. The
  claim under test: at a FIXED budget B, *smart* selection of what to inject
  achieves LOWER distortion than *naive* inclusion — i.e. choosing WHAT to
  remember beats just choosing HOW MUCH.

  Framing (reusing Bet 6's instrument, spikes/compression/surprisal.py):
    RATE       = B, the token budget of injected memory context.
    DISTORTION = the held-out probe's bits/token (bpt) given that context.
                 A relevant, well-chosen context lowers the probe's bpt
                 (the model predicts the real session text with fewer bits).
                 Distortion here = predictive cost of the probe = 1 / quality.
  So a LOWER bpt at the same B = LESS distortion at the same rate = better.

--------------------------------------------------------------------------------
The minimum experiment (a rate-distortion curve)
--------------------------------------------------------------------------------
 1. A POOL of M candidate memory fragments about ONE tenant's world. The pool
    deliberately mixes RELEVANT fragments (about the tenant's backend, deploys,
    reports...) with DISTRACTORS (cooking, football, weather... unrelated),
    at varied lengths, so that *which* fragments you pick actually matters. A
    fixed HELD-OUT PROBE (a realistic session text) references several relevant
    facts. Data is Spanish (same rationale as surprisal.py's docstring); code
    is English.

 2. For a range of token budgets B (0, 64, 128, 256, 512 subword tokens), build
    the injected context THREE ways and measure the probe's bpt (distortion):
      - NAIVE          : fill B with fragments in a fixed "recency" order
                         (seeded shuffle of the pool — no relevance awareness).
      - SMART (lexical): a CHEAP, model-free relevance proxy — rank fragments by
                         Jaccard word-overlap with the probe, fill B best-first.
                         This is the honest, product-realistic selector (no
                         oracle LM in the ranking loop).
      - SMART (LM)     : an ORACLE greedy — rank fragments by REAL per-token bpt
                         reduction they give the probe in isolation, fill
                         best-first. Upper bound on what selection can buy.

    In ALL three, DISTORTION is measured by the real LM bpt of the probe given
    the assembled context (never by the ranking proxy).

 3. Two rate-distortion curves (naive vs smart) + the distortion GAP at each B.

 4. VERDICT: if the smart curve sits BELOW the naive curve at fixed B, Bet 3's
    rate-distortion framing holds (selection > volume). If they overlap, at
    this scale selection doesn't help — a valuable null result.

Usage:
    .venv/bin/python bet03_rate_distortion.py
    SURPRISAL_MODEL=distilgpt2 .venv/bin/python bet03_rate_distortion.py
"""

from __future__ import annotations

import random
from typing import Dict, List, Tuple

import surprisal
from surprisal import surprisal_bits, backend_info

# --------------------------------------------------------------------------
# The tenant world: ONE fixed held-out probe + a pool of candidate fragments.
# --------------------------------------------------------------------------
# Held-out probe: a realistic Spanish session message from the tenant. It
# references SEVERAL relevant facts (São Paulo backend, Celery/Redis, the
# Monday sales report, the staging DB, Mercado Pago timeouts, the month-end
# campaign). This is the text whose bits/token we minimize = the distortion.
PROBE = (
    "Hola, necesito ayuda con el despliegue de anoche. El backend en São Paulo "
    "tardó mucho en responder y varias tareas de Celery quedaron encoladas en Redis. "
    "Además el reporte de ventas del lunes salió con datos incompletos desde la base "
    "de staging. ¿Podés revisar si el timeout de Mercado Pago afectó los pagos "
    "durante la campaña de fin de mes?"
)

# RELEVANT fragments: real facts about this tenant's world that the probe draws
# on. A good selector should prefer these. Varied lengths on purpose.
RELEVANT_FRAGMENTS = [
    "el backend de producción del equipo corre en AWS São Paulo con Django y PostgreSQL",
    "los despliegues a producción se hacen los martes y los jueves por la noche",
    "el equipo usa Celery con Redis como broker para las tareas en segundo plano",
    "el reporte de ventas se genera automáticamente todos los lunes a las 8 de la mañana",
    "la base de datos de staging se llama tienda_staging y se restaura cada semana",
    "el sistema de pagos integra con Mercado Pago y a veces falla por timeouts en el checkout",
    "el pico de tráfico ocurre durante la campaña de descuentos de fin de mes",
    "María es la líder técnica del equipo de e-commerce y revisa los despliegues",
    "los logs de producción se envían a CloudWatch en la región de São Paulo",
    "cuando Redis se satura las tareas de Celery se encolan y los reportes se atrasan",
]

# DISTRACTOR fragments: plausible tenant memory, but IRRELEVANT to this probe.
# They fill a budget without lowering the probe's bpt (that's the whole point:
# naive inclusion spends rate on these; smart selection skips them).
DISTRACTOR_FRAGMENTS = [
    "la receta de la abuela lleva dos huevos, harina, azúcar y ralladura de limón",
    "el partido del domingo terminó dos a uno con un gol en el último minuto",
    "el pronóstico anuncia lluvias fuertes y viento del sudeste durante el fin de semana",
    "la próxima reunión del club de lectura será sobre una novela de ciencia ficción",
    "para hacer buen café conviene moler el grano justo antes de preparar la taza",
    "el gimnasio abre a las seis de la mañana y cierra a las once de la noche",
    "las plantas de interior necesitan riego moderado y luz indirecta constante",
    "el museo de arte moderno inauguró una muestra de escultura contemporánea",
    "el tren a la costa sale cada dos horas desde la estación central de la ciudad",
    "la banda toca el viernes en un bar del centro con entrada libre y gratuita",
    "el perro necesita una vacuna anual y un baño cada dos o tres semanas",
    "la clase de yoga de los martes se enfoca en respiración y estiramientos suaves",
]

POOL: List[Tuple[str, str]] = (
    [("relevant", f) for f in RELEVANT_FRAGMENTS]
    + [("distractor", f) for f in DISTRACTOR_FRAGMENTS]
)

BUDGETS = [0, 64, 128, 256, 512]  # token budgets B (the RATE), in model subwords
SEED = 20260710


# --------------------------------------------------------------------------
# Tokenization: count budget in the SAME unit the bpt is measured in.
# --------------------------------------------------------------------------
def count_tokens(text: str) -> int:
    """Token length of `text` in the scorer's unit (LM subwords, else words)."""
    if surprisal.USING_LM and surprisal._tokenizer is not None:
        return len(surprisal._tokenizer(text)["input_ids"])
    return len(surprisal._tokenize_words(text))


def assemble(fragments: List[str]) -> str:
    """Concatenate selected fragments into one injected-context string."""
    return ". ".join(fragments) + ("." if fragments else "")


def distortion(context: str) -> float:
    """DISTORTION = the probe's bits/token given `context` (lower = better)."""
    return surprisal_bits(PROBE, context)["bits_per_token"]


# --------------------------------------------------------------------------
# Relevance proxies for the SMART selectors (ranking only; NOT distortion).
# --------------------------------------------------------------------------
_STOP = {
    "el", "la", "los", "las", "un", "una", "de", "del", "en", "y", "a", "con",
    "que", "se", "por", "para", "su", "es", "al", "lo", "como", "más", "o",
    "the", "of", "and",
}


def _words(text: str) -> set:
    return {w for w in surprisal._tokenize_words(text) if w not in _STOP}


def lexical_score(fragment: str, probe: str) -> float:
    """Cheap model-free relevance: Jaccard word-overlap with the probe."""
    a, b = _words(fragment), _words(probe)
    if not a or not b:
        return 0.0
    return len(a & b) / len(a | b)


def lm_reduction_per_token(fragment: str, base_bpt: float) -> float:
    """Oracle proxy: real per-token bpt reduction this fragment gives in isolation."""
    n = max(count_tokens(fragment), 1)
    return (base_bpt - distortion(fragment)) / n


# --------------------------------------------------------------------------
# Budget-fill: pick fragments in a given ORDER until the next won't fit in B.
# --------------------------------------------------------------------------
def fill_budget(ordered_fragments: List[str], budget: int) -> Tuple[List[str], int]:
    """Greedy pack: walk the ordered list, add a fragment if it fits in B."""
    selected: List[str] = []
    used = 0
    for frag in ordered_fragments:
        t = count_tokens(frag)
        if used + t <= budget:
            selected.append(frag)
            used += t
    return selected, used


# --------------------------------------------------------------------------
def main():
    info = backend_info()
    print("# Bet 3 — context as rate-distortion (RATE = token budget B, DISTORTION = probe bpt)")
    print(f"# Backend: {'real LM -> ' + str(info['model_name']) if info['using_lm'] else 'N-GRAM FALLBACK'}")
    print(f"# Reason: {info['reason']}")
    print(f"# Pool: M={len(POOL)} fragments ({len(RELEVANT_FRAGMENTS)} relevant + {len(DISTRACTOR_FRAGMENTS)} distractors)")
    print(f"# Probe ({count_tokens(PROBE)} tokens): {PROBE!r}")
    print()

    base_bpt = distortion("")  # B=0 baseline: no memory injected
    print(f"# Baseline distortion (B=0, empty context): bpt = {base_bpt:.4f}")
    print()

    frags = [f for _, f in POOL]
    kinds = {f: k for k, f in POOL}

    # ---- Ordering per strategy (independent of B) ----
    # NAIVE: fixed seeded shuffle = "arbitrary/recency" order, no relevance.
    naive_order = list(frags)
    random.Random(SEED).shuffle(naive_order)

    # SMART (lexical): rank by cheap Jaccard overlap with the probe, desc.
    lex_scores = {f: lexical_score(f, PROBE) for f in frags}
    smart_lex_order = sorted(frags, key=lambda f: lex_scores[f], reverse=True)

    # SMART (LM oracle): rank by real per-token bpt reduction in isolation, desc.
    lm_red = {f: lm_reduction_per_token(f, base_bpt) for f in frags}
    smart_lm_order = sorted(frags, key=lambda f: lm_red[f], reverse=True)

    # ---- Per-fragment diagnostic table (honesty: show the ranking inputs) ----
    print("# Per-fragment ranking inputs (proxy scores vs the REAL marginal bpt reduction)")
    print(f"# {'kind':<10} {'ntok':>4} {'lexJacc':>8} {'lm_red/tok':>11}  fragment")
    print("-" * 118)
    for f in sorted(frags, key=lambda f: lm_red[f], reverse=True):
        p = f if len(f) <= 60 else f[:57] + "..."
        print(f"# {kinds[f]:<10} {count_tokens(f):>4} {lex_scores[f]:>8.3f} {lm_red[f]:>11.5f}  {p}")
    print()

    # ---- Rate-distortion sweep ----
    strategies: Dict[str, List[str]] = {
        "naive": naive_order,
        "smart_lex": smart_lex_order,
        "smart_lm": smart_lm_order,
    }

    curves: Dict[str, List[float]] = {name: [] for name in strategies}
    used_tok: Dict[str, List[int]] = {name: [] for name in strategies}
    n_rel: Dict[str, List[int]] = {name: [] for name in strategies}

    for B in BUDGETS:
        for name, order in strategies.items():
            if B == 0:
                sel, used = [], 0
            else:
                sel, used = fill_budget(order, B)
            bpt = distortion(assemble(sel))
            curves[name].append(bpt)
            used_tok[name].append(used)
            n_rel[name].append(sum(1 for f in sel if kinds[f] == "relevant"))

    # ---- Results table ----
    print("# ================= RATE-DISTORTION TABLE =================")
    print("# distortion = probe bits/token given the injected context (LOWER = better)")
    print(f"# {'B':>4} | {'naive':>8} {'sm_lex':>8} {'sm_lm':>8} | {'gap_lex':>8} {'gap_lm':>8} | {'relN(nv/lx/lm)':>15} | {'usedTok(nv/lx/lm)':>18}")
    print("-" * 104)
    for i, B in enumerate(BUDGETS):
        n_bpt = curves["naive"][i]
        lx_bpt = curves["smart_lex"][i]
        lm_bpt = curves["smart_lm"][i]
        gap_lex = n_bpt - lx_bpt   # >0 => smart_lex has LOWER distortion (better)
        gap_lm = n_bpt - lm_bpt
        rel_s = f"{n_rel['naive'][i]}/{n_rel['smart_lex'][i]}/{n_rel['smart_lm'][i]}"
        tok_s = f"{used_tok['naive'][i]}/{used_tok['smart_lex'][i]}/{used_tok['smart_lm'][i]}"
        print(f"# {B:>4} | {n_bpt:>8.4f} {lx_bpt:>8.4f} {lm_bpt:>8.4f} | {gap_lex:>+8.4f} {gap_lm:>+8.4f} | {rel_s:>15} | {tok_s:>18}")
    print("-" * 104)
    print("# gap_lex = bpt(naive) - bpt(smart_lex);  gap_lm = bpt(naive) - bpt(smart_lm).")
    print("# gap > 0  => smart selection achieves LOWER distortion at the same budget (Bet 3 supported).")
    print()

    # ---- ASCII rate-distortion curves ----
    print(_ascii_rd(BUDGETS, curves))
    print()

    # ---- Verdict (numbers-first) ----
    active = [i for i, B in enumerate(BUDGETS) if B > 0]
    lex_gaps = [curves["naive"][i] - curves["smart_lex"][i] for i in active]
    lm_gaps = [curves["naive"][i] - curves["smart_lm"][i] for i in active]
    mean_lex = sum(lex_gaps) / len(lex_gaps)
    mean_lm = sum(lm_gaps) / len(lm_gaps)
    lex_wins = sum(1 for g in lex_gaps if g > 1e-4)
    lm_wins = sum(1 for g in lm_gaps if g > 1e-4)

    print("# ===================== VERDICT (numbers) =====================")
    print(f"#   budgets compared (B>0): {[BUDGETS[i] for i in active]}")
    print(f"#   SMART-lexical (cheap proxy):   mean gap = {mean_lex:+.4f} bpt, wins {lex_wins}/{len(active)} budgets")
    print(f"#   SMART-LM      (oracle greedy): mean gap = {mean_lm:+.4f} bpt, wins {lm_wins}/{len(active)} budgets")
    supported_lm = mean_lm > 1e-3 and lm_wins >= len(active) - 1
    supported_lex = mean_lex > 1e-3 and lex_wins >= len(active) - 1
    if supported_lm:
        print("#   -> Bet 3 SUPPORTED (oracle): a rate-distortion selector achieves lower")
        print("#      distortion than naive inclusion at fixed budget. Selection > volume.")
        if supported_lex:
            print("#   -> And a CHEAP lexical proxy also beats naive (product-viable, no oracle).")
        else:
            print("#   -> BUT the cheap lexical proxy does NOT reliably beat naive: the win")
            print("#      needs a good ranker (the oracle), which is exactly Bet 3's open problem.")
    else:
        print("#   -> Bet 3 NOT SUPPORTED at this scale: the smart and naive curves overlap")
        print("#      (selection did not lower distortion at fixed budget). Null result.")
    print("# =============================================================")

    return curves, {"mean_lex": mean_lex, "mean_lm": mean_lm}


def _ascii_rd(budgets, curves, width: int = 52, height: int = 14) -> str:
    """Plot the rate-distortion curves: x = budget B, y = distortion (bpt)."""
    all_y = [v for c in curves.values() for v in c]
    ymin, ymax = min(all_y), max(all_y)
    span_y = (ymax - ymin) or 1.0
    xmin, xmax = min(budgets), max(budgets)
    span_x = (xmax - xmin) or 1.0
    marks = {"naive": "N", "smart_lex": "L", "smart_lm": "M"}
    grid = [[" "] * width for _ in range(height)]
    for name, c in curves.items():
        for B, y in zip(budgets, c):
            cx = int((B - xmin) / span_x * (width - 1))
            cy = int((y - ymin) / span_y * (height - 1))
            row = height - 1 - cy
            grid[row][cx] = marks[name]
    lines = ["# Rate-distortion curves  (N=naive, L=smart_lex, M=smart_lm)",
             "# y = distortion (probe bpt, LOWER=better) ; x = budget B (tokens)"]
    for r, row in enumerate(grid):
        yval = ymax - (r / (height - 1)) * span_y
        lines.append(f"# {yval:6.3f} |" + "".join(row))
    lines.append("#        +" + "-" * width)
    lines.append(f"#        {xmin:<.0f}{' ' * (width - 8)}{xmax:>.0f}  (budget B)")
    return "\n".join(lines)


if __name__ == "__main__":
    main()
