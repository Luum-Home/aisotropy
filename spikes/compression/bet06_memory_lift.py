"""
bet06_memory_lift.py — validates (or refutes) Bet 6: "bits-per-token as a memory metric".

Hypothesis (docs/research/bet-06-bits-per-token-metric.md):
  bpt(tenant) = -(1/N) * sum(log2 P(token_i | context, memory)).
  lift = bpt_without_memory - bpt_with_memory.
  If the memory is relevant, it should REDUCE the bits/token needed to
  predict a real session text from the user (lift > 0).

This experiment:
  1. Defines a "user session" text that mentions things related to
     the surprising facts from the Bet 1 experiment (the AWS São
     Paulo deploy, Monday reports). The session text and memory facts are
     intentionally in Spanish — see surprisal.py's module docstring ("Why
     the experimental data is in Spanish") for why they are not translated.
  2. Measures the text's bits/token WITHOUT memory (context="", same as
     bet01) and WITH memory (context = some relevant surprising facts,
     concatenated, acting as "hydrated memory").
  3. Computes lift = bpt_without_memory - bpt_with_memory and prints both numbers.

Uses whatever model surprisal.py loaded (default: DeepESP/gpt2-spanish;
override with the SURPRISAL_MODEL env var, e.g. SURPRISAL_MODEL=distilgpt2
to reproduce the first-attempt run documented in README.md).
"""

from __future__ import annotations

from surprisal import surprisal_bits, backend_info

# Session text: the user talks about things related to their "surprising"
# facts (bet01) — the AWS São Paulo deploy and the Monday 8am reports.
SESSION_TEXT = (
    "Hoy tuvimos un problema con el deploy en producción. "
    "El backend tardó más de lo normal en responder desde São Paulo. "
    "Necesito que el reporte esté listo antes de las 8am del lunes. "
    "Voy a revisar los logs de AWS para entender qué pasó con la base de datos de staging."
)

# Relevant memory: subset of the surprising facts from bet01 that
# directly relate to the session text.
RELEVANT_MEMORY_FACTS = [
    "el backend de producción del usuario corre en AWS São Paulo",
    "el usuario prefiere recibir reportes los lunes a las 8am",
    "la contraseña de la base de datos de staging se rotó el 3 de marzo",
]

CONTEXT_WITHOUT_MEMORY = ""
CONTEXT_WITH_MEMORY = ". ".join(RELEVANT_MEMORY_FACTS) + "."


def main():
    info = backend_info()
    print(f"# Backend: {'real LM -> ' + str(info['model_name']) if info['using_lm'] else 'N-GRAM FALLBACK'}")
    print(f"# Reason: {info['reason']}")
    print()
    print(f"# Session text: {SESSION_TEXT!r}")
    print()
    print(f"# Memory (context with memory): {CONTEXT_WITH_MEMORY!r}")
    print()

    without_memory = surprisal_bits(SESSION_TEXT, CONTEXT_WITHOUT_MEMORY)
    with_memory = surprisal_bits(SESSION_TEXT, CONTEXT_WITH_MEMORY)

    unit = without_memory["unit"]

    print(f"# WITHOUT memory: bits_total={without_memory['bits_total']:.2f}  bits/{unit}={without_memory['bits_per_token']:.4f}  n_tokens={without_memory['n_tokens']}")
    print(f"# WITH memory: bits_total={with_memory['bits_total']:.2f}  bits/{unit}={with_memory['bits_per_token']:.4f}  n_tokens={with_memory['n_tokens']}")

    lift = without_memory["bits_per_token"] - with_memory["bits_per_token"]
    lift_total = without_memory["bits_total"] - with_memory["bits_total"]

    print()
    print(f"# LIFT (bpt_without_memory - bpt_with_memory) = {lift:.4f} bits/{unit}")
    print(f"# LIFT (bits_total_without - bits_total_with) = {lift_total:.2f} total bits")

    if lift > 0:
        print(f"# -> POSITIVE: memory REDUCES the surprisal of the session text ({lift:.4f} bits/{unit} saved).")
    elif lift == 0:
        print("# -> NULL: memory didn't change the surprisal of the session text.")
    else:
        print(f"# -> NEGATIVE: memory INCREASED the surprisal of the session text ({-lift:.4f} bits/{unit} extra cost).")

    return without_memory, with_memory, lift


if __name__ == "__main__":
    main()
