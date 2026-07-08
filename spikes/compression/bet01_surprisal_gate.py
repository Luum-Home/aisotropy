"""
bet01_surprisal_gate.py — validates (or refutes) Bet 1: "surprisal-gated memory".

Hypothesis (docs/research/bet-01-surprisal-gated-memory.md):
  A memory candidate fact should only be stored if it's SURPRISING (high
  surprisal in bits) under the model. Redundant/obvious facts (low
  surprisal) are discarded.

This experiment:
  1. Defines 20 Spanish-language "user facts" (10 redundant/obvious, 10
     surprising/specific). The fact STRINGS are intentionally in Spanish —
     see surprisal.py's module docstring ("Why the experimental data is in
     Spanish") for why they are not translated.
  2. Computes surprisal_bits(fact) for each one (context="" fixed — no
     context, to measure how much the model "already knew" about that fact
     from its general knowledge, with no additional clues).
  3. Sorts from lowest to highest surprisal and prints the table.
  4. Proposes a threshold tau based on the real GAP between the most
     surprising (a) fact and the least surprising (b) fact — NOT a
     number invented a priori.

Uses whatever model surprisal.py loaded (default: DeepESP/gpt2-spanish;
override with the SURPRISAL_MODEL env var, e.g. SURPRISAL_MODEL=distilgpt2
to reproduce the first-attempt run documented in README.md).
"""

from __future__ import annotations

from surprisal import surprisal_bits, backend_info

# Group (a): redundant / obvious facts about any AI assistant user.
# The model (real LM or n-gram) should assign them low surprisal because
# they're generic, highly probable phrases.
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

# Group (b): surprising / specific facts about THIS particular user.
# No generic model can predict them from world knowledge — they are
# real information and should have high surprisal.
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

FIXED_CONTEXT = ""  # no context: measures surprisal "in a vacuum" (the model's world knowledge)


def main():
    info = backend_info()
    print(f"# Backend: {'real LM -> ' + str(info['model_name']) if info['using_lm'] else 'N-GRAM FALLBACK'}")
    print(f"# Reason: {info['reason']}")
    print()

    rows = []
    for fact in REDUNDANT_FACTS:
        r = surprisal_bits(fact, FIXED_CONTEXT)
        rows.append({"fact": fact, "group": "(a) redundant", **r})
    for fact in SURPRISING_FACTS:
        r = surprisal_bits(fact, FIXED_CONTEXT)
        rows.append({"fact": fact, "group": "(b) surprising", **r})

    rows.sort(key=lambda f: f["bits_per_token"])

    unit = rows[0]["unit"]
    print(f"# Fact -> surprisal table (unit: bits/{unit}), ascending order")
    print(f"{'bits_total':>10}  {'bits/tok':>9}  {'n_tok':>5}  {'group':<18}  fact")
    print("-" * 100)
    for f in rows:
        print(f"{f['bits_total']:>10.2f}  {f['bits_per_token']:>9.3f}  {f['n_tokens']:>5}  {f['group']:<18}  {f['fact']}")

    bits_a = [f["bits_per_token"] for f in rows if f["group"] == "(a) redundant"]
    bits_b = [f["bits_per_token"] for f in rows if f["group"] == "(b) surprising"]
    max_bits_a = max(bits_a)
    min_bits_b = min(bits_b)
    mean_a = sum(bits_a) / len(bits_a)
    mean_b = sum(bits_b) / len(bits_b)

    fact_max_a = next(f["fact"] for f in rows if f["group"] == "(a) redundant" and f["bits_per_token"] == max_bits_a)
    fact_min_b = next(f["fact"] for f in rows if f["group"] == "(b) surprising" and f["bits_per_token"] == min_bits_b)

    print()
    print(f"# Mean (a) redundant: {mean_a:.3f} bits/tok")
    print(f"# Mean (b) surprising: {mean_b:.3f} bits/tok")
    print(f"# (a) most surprising redundant fact: {max_bits_a:.3f} bits/tok  <- \"{fact_max_a}\"")
    print(f"# (b) least surprising surprising fact: {min_bits_b:.3f} bits/tok  <- \"{fact_min_b}\"")

    overlap_a = [f for f in rows if f["group"] == "(a) redundant" and f["bits_per_token"] > min_bits_b]
    overlap_b = [f for f in rows if f["group"] == "(b) surprising" and f["bits_per_token"] < max_bits_a]

    if max_bits_a < min_bits_b:
        tau = (max_bits_a + min_bits_b) / 2
        print(f"# Real GAP detected: {min_bits_b - max_bits_a:.3f} bits/tok of clean separation.")
        print(f"# proposed tau (gap midpoint) = {tau:.3f} bits/{unit}")
    else:
        # No clean separation: the groups overlap. We report this
        # honestly instead of forcing a threshold that separates nothing.
        tau = (max_bits_a + min_bits_b) / 2
        print("# WARNING: groups (a) and (b) OVERLAP in bits/tok (no clean end-to-end gap).")
        print(f"# naive tau (midpoint of the extremes, no guaranteed separation) = {tau:.3f} bits/{unit}")
        if overlap_a:
            print(f"#   Redundant facts that would fall on the 'surprising' side (bits/tok > min of (b)={min_bits_b:.3f}): {[f['fact'] for f in overlap_a]}")
        if overlap_b:
            print(f"#   Surprising facts that would fall on the 'redundant' side (bits/tok < max of (a)={max_bits_a:.3f}): {[f['fact'] for f in overlap_b]}")

    n_a_wrong = len(overlap_a)
    n_b_wrong = len(overlap_b)
    print()
    print(f"# Classification with tau={tau:.3f} bits/{unit}: {len(REDUNDANT_FACTS)-n_a_wrong}/{len(REDUNDANT_FACTS)} redundant facts correctly classified, {len(SURPRISING_FACTS)-n_b_wrong}/{len(SURPRISING_FACTS)} surprising facts correctly classified.")

    return rows, tau


if __name__ == "__main__":
    main()
