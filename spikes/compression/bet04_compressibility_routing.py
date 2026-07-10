"""
bet04_compressibility_routing.py — tests (or refutes) Bet 4: "compressibility routing".

Hypothesis (docs/research/bet-04-compressibility-routing.md):
  A prompt's COMPRESSIBILITY, measured in bits, CORRELATES with its DIFFICULTY.
  If it does, a cheap compressibility signal can route easy prompts (compressible
  -> few bits) to a cheap model and hard prompts (incompressible -> many bits) to
  an expensive/frontier model, lowering cost WITHOUT lowering quality.

  Bet 4 reuses the SAME surprisal signal as Bet 1 (see spikes/compression/surprisal.py)
  but applies it to a DIFFERENT axis: not "is this fact worth storing?" (novelty)
  but "is this query hard to ANSWER?" (difficulty). This experiment is the honest
  minimum test of whether bits <-> difficulty holds at all.

--------------------------------------------------------------------------------
The minimum experiment
--------------------------------------------------------------------------------
 1. A hand-authored labeled set of ~30 Spanish prompts spanning a DIFFICULTY
    spectrum (rank 1 trivial -> rank 5 very hard). The difficulty rank is an
    ORDINAL human label; the labeling rubric is documented in DIFFICULTY_RUBRIC
    below and IS the main bias source (see the report / Trust Report).

    The prompts are in Spanish because the LM surprisal backend defaults to
    DeepESP/gpt2-spanish and aisotropy's target user talks in Spanish (same
    rationale as surprisal.py's module docstring). gzip is language-agnostic.

 2. Two cheap COMPRESSIBILITY signals per prompt, both in bits, no frontier call:
      (a) LM surprisal: bits/token AND bits_total under the small LM (surprisal.py).
      (b) gzip: compressed size in bits AND the gzip compression RATIO
          (compressed/raw) — a pure-stdlib, model-free proxy for redundancy.

 3. Correlation vs difficulty rank: Spearman (primary — difficulty is ordinal)
    and Pearson, per signal, each with a permutation-test p-value (no scipy in
    the venv, so both coefficients and the p-value are implemented here). Plus an
    ASCII scatter of each signal vs difficulty, and a LENGTH-confound check
    (does length alone already predict difficulty?).

 4. Routing simulation: pick a bits threshold, route low-bits -> cheap /
    high-bits -> expensive, and report the cheap-routed fraction plus how well
    the split respects the true easy/hard labels.

Usage:
    .venv/bin/python bet04_compressibility_routing.py
    SURPRISAL_MODEL=distilgpt2 .venv/bin/python bet04_compressibility_routing.py
"""

from __future__ import annotations

import gzip
import random
from typing import Callable, Dict, List, Sequence, Tuple

from surprisal import surprisal_bits, backend_info

# --------------------------------------------------------------------------
# Difficulty rubric (the labeling — MAIN BIAS SOURCE, documented on purpose)
# --------------------------------------------------------------------------
# Difficulty = how much reasoning/knowledge a model needs to ANSWER the prompt
# CORRECTLY, independent of surface form. This is a subjective ordinal label
# authored by one person; a different annotator would shift some ranks by +-1.
DIFFICULTY_RUBRIC = {
    1: "Trivial: greeting / social / single obvious fact. A tiny model nails it.",
    2: "Simple: one-hop factual lookup or a one-line definition.",
    3: "Moderate: needs a short structured explanation or a small computation.",
    4: "Hard: multi-step reasoning, a word problem, or non-trivial code/design.",
    5: "Very hard: long multi-step reasoning / proof / multi-constraint synthesis.",
}

# Each entry: (difficulty_rank, prompt). 6 prompts per rank = 30 total.
# Deliberately mixes SHORT hard prompts and LONG easy prompts to expose the
# length/rarity confound instead of hiding it.
PROMPTS: List[Tuple[int, str]] = [
    # -------- rank 1: trivial --------
    (1, "hola"),
    (1, "gracias, muy amable"),
    (1, "¿cómo estás hoy?"),
    (1, "buenos días, ¿todo bien?"),
    (1, "¿cuál es la capital de Francia?"),
    (1, "decime la fecha de hoy por favor"),
    # -------- rank 2: simple --------
    (2, "¿qué es una API?"),
    (2, "¿cuánto es 15 por 12?"),
    (2, "¿en qué año llegó el hombre a la luna?"),
    (2, "traducí la palabra 'perro' al inglés"),
    (2, "¿qué significa la sigla HTTP?"),
    (2, "nombrá tres planetas del sistema solar"),
    # -------- rank 3: moderate --------
    (3, "explicá la diferencia entre TCP y UDP en pocas líneas"),
    (3, "¿cómo funciona el DNS cuando escribo una dirección web?"),
    (3, "resumí qué es la fotosíntesis para un chico de diez años"),
    (3, "escribí una función en python que invierta una lista"),
    (3, "¿qué ventajas tiene una base de datos relacional frente a una NoSQL?"),
    (3, "calculá el interés simple de 1000 pesos al 5% anual durante 3 años"),
    # -------- rank 4: hard --------
    (4, "un tren sale a 60 km/h y otro a 90 km/h en sentido contrario desde ciudades a 300 km, ¿en cuánto tiempo se cruzan?"),
    (4, "diseñá el esquema de tablas para un sistema de reservas de hotel con habitaciones, clientes y pagos"),
    (4, "explicá por qué el algoritmo quicksort es O(n log n) en promedio pero O(n^2) en el peor caso"),
    (4, "escribí una expresión regular que valide direcciones de correo electrónico y explicá cada parte"),
    (4, "si tres máquinas fabrican tres piezas en tres minutos, ¿cuántas máquinas hacen cien piezas en cien minutos?"),
    (4, "proponé una estrategia de caché para una API con lecturas frecuentes y escrituras raras, justificando la invalidación"),
    # -------- rank 5: very hard --------
    (5, "demostrá formalmente que la raíz cuadrada de dos es un número irracional"),
    (5, "diseñá un sistema distribuido multi-tenant que garantice aislamiento de datos, cuotas por inquilino y consistencia eventual, y discutí los trade-offs de CAP"),
    (5, "derivá la complejidad temporal amortizada de un arreglo dinámico que duplica su capacidad, usando el método del potencial"),
    (5, "planificá una migración de una base de datos monolítica a microservicios sin downtime, incluyendo doble escritura, backfill y corte final"),
    (5, "dado un grafo dirigido con pesos posiblemente negativos, explicá cuándo conviene Bellman-Ford sobre Dijkstra y demostrá por qué Dijkstra falla con aristas negativas"),
    (5, "optimizá una consulta SQL con tres joins y una subconsulta correlacionada que tarda 40 segundos, explicando índices, plan de ejecución y reescritura"),
]


# --------------------------------------------------------------------------
# Compressibility signals (bits) — cheap, no frontier call
# --------------------------------------------------------------------------
def gzip_signal(text: str) -> Dict[str, float]:
    """gzip compressibility of a prompt.

    raw_bytes: UTF-8 length. comp_bits: 8 * gzip payload size (header/trailer
    stripped so short strings aren't dominated by the ~18-byte gzip envelope).
    ratio: comp_bytes / raw_bytes (LOW ratio = very compressible = redundant).
    """
    raw = text.encode("utf-8")
    full = gzip.compress(raw, 9)
    # gzip: 10-byte header + 8-byte trailer (crc32 + isize). Strip both so the
    # measured payload reflects the DEFLATE body, not the fixed envelope.
    payload = max(len(full) - 18, 0)
    raw_len = max(len(raw), 1)
    return {
        "gzip_comp_bits": payload * 8.0,
        "gzip_ratio": payload / raw_len,  # compressed/raw; <1 means compressible
        "raw_bytes": float(len(raw)),
    }


def lm_signal(text: str) -> Dict[str, float]:
    r = surprisal_bits(text, "")  # context="" — surprisal "in a vacuum"
    return {
        "lm_bits_total": r["bits_total"],
        "lm_bits_per_token": r["bits_per_token"],
        "lm_n_tokens": float(r["n_tokens"]),
        "lm_unit": r["unit"],
    }


# --------------------------------------------------------------------------
# Statistics (pure Python — no scipy in the venv)
# --------------------------------------------------------------------------
def _rank(xs: Sequence[float]) -> List[float]:
    """Average-rank of xs (ties share the mean of their rank positions)."""
    order = sorted(range(len(xs)), key=lambda i: xs[i])
    ranks = [0.0] * len(xs)
    i = 0
    while i < len(order):
        j = i
        while j + 1 < len(order) and xs[order[j + 1]] == xs[order[i]]:
            j += 1
        avg = (i + j) / 2.0 + 1.0  # ranks are 1-based
        for k in range(i, j + 1):
            ranks[order[k]] = avg
        i = j + 1
    return ranks


def pearson(x: Sequence[float], y: Sequence[float]) -> float:
    n = len(x)
    mx = sum(x) / n
    my = sum(y) / n
    num = sum((xi - mx) * (yi - my) for xi, yi in zip(x, y))
    dx = sum((xi - mx) ** 2 for xi in x) ** 0.5
    dy = sum((yi - my) ** 2 for yi in y) ** 0.5
    if dx == 0 or dy == 0:
        return 0.0
    return num / (dx * dy)


def spearman(x: Sequence[float], y: Sequence[float]) -> float:
    return pearson(_rank(x), _rank(y))


def perm_pvalue(
    x: Sequence[float],
    y: Sequence[float],
    stat: Callable[[Sequence[float], Sequence[float]], float],
    n_perm: int = 20000,
    seed: int = 1234,
) -> float:
    """Two-sided permutation p-value: P(|stat(shuffled)| >= |observed|)."""
    observed = abs(stat(x, y))
    rng = random.Random(seed)
    y_list = list(y)
    # Exact enumeration is 30! — far too large; use a Monte-Carlo permutation.
    count = 0
    for _ in range(n_perm):
        rng.shuffle(y_list)
        if abs(stat(x, y_list)) >= observed - 1e-12:
            count += 1
    return (count + 1) / (n_perm + 1)  # +1 smoothing (never reports p=0)


# --------------------------------------------------------------------------
# ASCII scatter (self-contained, no matplotlib dependency)
# --------------------------------------------------------------------------
def ascii_scatter(
    xs: Sequence[float], ys: Sequence[int], xlabel: str, width: int = 56, height: int = 12
) -> str:
    xmin, xmax = min(xs), max(xs)
    ymin, ymax = min(ys), max(ys)
    span_x = (xmax - xmin) or 1.0
    span_y = (ymax - ymin) or 1.0
    grid = [[" "] * width for _ in range(height)]
    for x, y in zip(xs, ys):
        cx = int((x - xmin) / span_x * (width - 1))
        cy = int((y - ymin) / span_y * (height - 1))
        row = height - 1 - cy
        cell = grid[row][cx]
        grid[row][cx] = "*" if cell == " " else ("2" if cell == "*" else "+")
    lines = [f"difficulty (rank {ymax} top .. {ymin} bottom)  vs  {xlabel}"]
    for r, row in enumerate(grid):
        yval = ymax - (r / (height - 1)) * span_y
        lines.append(f"{yval:5.1f} |" + "".join(row))
    lines.append("      +" + "-" * width)
    lines.append(f"       {xmin:<.2f}{' ' * (width - 12)}{xmax:>.2f}")
    return "\n".join(lines)


# --------------------------------------------------------------------------
# Routing simulation
# --------------------------------------------------------------------------
def routing_sim(
    bits: Sequence[float], difficulty: Sequence[int], signal_name: str
) -> str:
    """Route low-bits -> cheap, high-bits -> expensive at the MEDIAN threshold.

    Reports the cheap-routed fraction and how well the split respects the true
    easy(<=2)/hard(>=4) labels — a confusion view of the routing quality.
    """
    pairs = sorted(zip(bits, difficulty))
    n = len(pairs)
    theta = pairs[n // 2][0]  # median bits: ~50/50 split by construction
    cheap = [d for b, d in zip(bits, difficulty) if b < theta]
    exp = [d for b, d in zip(bits, difficulty) if b >= theta]
    cheap_frac = len(cheap) / n

    easy_cheap = sum(1 for d in cheap if d <= 2)
    easy_total = sum(1 for d in difficulty if d <= 2)
    hard_exp = sum(1 for d in exp if d >= 4)
    hard_total = sum(1 for d in difficulty if d >= 4)
    # A prompt is "misrouted" if it's hard(>=4) but sent cheap, or easy(<=2) sent expensive.
    hard_sent_cheap = sum(1 for d in cheap if d >= 4)
    easy_sent_exp = sum(1 for d in exp if d <= 2)

    out = [
        f"# Routing sim on {signal_name}: threshold theta = {theta:.4f} (median).",
        f"#   routed CHEAP (bits < theta): {len(cheap)}/{n} = {cheap_frac:.0%}   |   routed EXPENSIVE: {len(exp)}/{n}",
        f"#   easy(<=2) correctly sent cheap:      {easy_cheap}/{easy_total}",
        f"#   hard(>=4) correctly sent expensive:  {hard_exp}/{hard_total}",
        f"#   DANGEROUS misroutes (hard sent to CHEAP model): {hard_sent_cheap}   [these lose quality]",
        f"#   wasteful misroutes (easy sent to EXPENSIVE):    {easy_sent_exp}   [these waste money]",
        f"#   mean difficulty | cheap = {sum(cheap)/len(cheap):.2f}   vs   expensive = {sum(exp)/len(exp):.2f}",
    ]
    return "\n".join(out)


# --------------------------------------------------------------------------
def main():
    info = backend_info()
    print(f"# Backend: {'real LM -> ' + str(info['model_name']) if info['using_lm'] else 'N-GRAM FALLBACK'}")
    print(f"# Reason: {info['reason']}")
    print(f"# N prompts = {len(PROMPTS)}  (6 per difficulty rank, 1..5)")
    print("#")
    print("# Difficulty rubric (ordinal human label; MAIN BIAS SOURCE):")
    for k, v in DIFFICULTY_RUBRIC.items():
        print(f"#   {k}: {v}")
    print()

    rows = []
    for diff, prompt in PROMPTS:
        g = gzip_signal(prompt)
        lm = lm_signal(prompt)
        rows.append({"difficulty": diff, "prompt": prompt, **g, **lm})

    unit = rows[0]["lm_unit"]

    # Per-prompt table
    print(f"# Per-prompt signals (LM unit: bits/{unit})")
    hdr = f"{'diff':>4}  {'lm_bpt':>7}  {'lm_tot':>8}  {'lm_ntok':>7}  {'gz_bits':>7}  {'gz_ratio':>8}  {'rawB':>5}  prompt"
    print(hdr)
    print("-" * 130)
    for r in sorted(rows, key=lambda z: (z["difficulty"], z["lm_bits_per_token"])):
        p = r["prompt"] if len(r["prompt"]) <= 60 else r["prompt"][:57] + "..."
        print(
            f"{r['difficulty']:>4}  {r['lm_bits_per_token']:>7.3f}  {r['lm_bits_total']:>8.1f}  "
            f"{r['lm_n_tokens']:>7.0f}  {r['gzip_comp_bits']:>7.0f}  {r['gzip_ratio']:>8.3f}  "
            f"{r['raw_bytes']:>5.0f}  {p}"
        )
    print()

    difficulty = [r["difficulty"] for r in rows]

    # Correlations for each candidate signal
    signals = {
        "LM bits/token": [r["lm_bits_per_token"] for r in rows],
        "LM bits_total": [r["lm_bits_total"] for r in rows],
        "gzip comp_bits": [r["gzip_comp_bits"] for r in rows],
        "gzip ratio": [r["gzip_ratio"] for r in rows],
        # confound references:
        "raw_bytes (length)": [r["raw_bytes"] for r in rows],
        "LM n_tokens (length)": [r["lm_n_tokens"] for r in rows],
    }

    print("# Correlation of each signal vs difficulty rank (Spearman primary; Pearson secondary)")
    print(f"# {'signal':<22}  {'Spearman':>9}  {'p(perm)':>8}  {'Pearson':>8}  {'p(perm)':>8}")
    print("-" * 72)
    results = {}
    for name, vals in signals.items():
        rs = spearman(vals, difficulty)
        ps = perm_pvalue(vals, difficulty, spearman)
        rp = pearson(vals, difficulty)
        pp = perm_pvalue(vals, difficulty, pearson)
        results[name] = (rs, ps, rp, pp)
        print(f"# {name:<22}  {rs:>9.3f}  {ps:>8.4f}  {rp:>8.3f}  {pp:>8.4f}")
    print()
    print("# Interpretation guide: |rho| >= 0.6 with p < 0.05 = USABLE routing signal")
    print("#   (the bet doc's own success criterion). |rho| 0.3-0.6 = weak. < 0.3 = absent.")
    print()

    # Scatters for the two headline PER-PROMPT-NORMALIZED signals + one length ref
    print(ascii_scatter([r["lm_bits_per_token"] for r in rows], difficulty, "LM bits/token"))
    print()
    print(ascii_scatter([r["gzip_ratio"] for r in rows], difficulty, "gzip ratio (compressed/raw)"))
    print()
    print(ascii_scatter([r["lm_bits_total"] for r in rows], difficulty, "LM bits_total"))
    print()

    # Routing simulation on the strongest length-free signal AND on bits_total
    print(routing_sim([r["lm_bits_per_token"] for r in rows], difficulty, "LM bits/token"))
    print("#")
    print(routing_sim([r["lm_bits_total"] for r in rows], difficulty, "LM bits_total"))
    print()

    # Verdict banner (numbers-first)
    best = max(results.items(), key=lambda kv: abs(kv[1][0]))
    print("# ================= VERDICT (numbers) =================")
    for name in ["LM bits/token", "LM bits_total", "gzip comp_bits", "gzip ratio"]:
        rs, ps, rp, pp = results[name]
        verdict = "USABLE" if abs(rs) >= 0.6 and ps < 0.05 else ("WEAK" if abs(rs) >= 0.3 else "ABSENT")
        print(f"#   {name:<16} Spearman rho={rs:+.3f} (p={ps:.4f}) -> {verdict}")
    rs_len, ps_len, _, _ = results["raw_bytes (length)"]
    print(f"#   LENGTH confound: raw_bytes vs difficulty Spearman rho={rs_len:+.3f} (p={ps_len:.4f})")
    print(f"#   strongest signal overall: {best[0]} (|rho|={abs(best[1][0]):.3f})")
    print("# =====================================================")

    return rows, results


if __name__ == "__main__":
    main()
