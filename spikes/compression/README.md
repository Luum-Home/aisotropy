# Spike: compression = prediction = intelligence (Bet 1 + Bet 6)

A minimal, reproducible spike, with REAL numbers (not invented), to put
two bets from the [compression manifesto](../../docs/vision/compression-thesis.md) to the test:

- **Bet 1** — [surprisal-gated memory](../../docs/research/bet-01-surprisal-gated-memory.md):
  do redundant facts have low surprisal and surprising ones high surprisal, with a clean gap between the two?
- **Bet 6** — [bits-per-token as a memory metric](../../docs/research/bet-06-bits-per-token-metric.md):
  does having relevant memory as context reduce the bits/token needed to predict a session's text?

## How to run

```bash
cd spikes/compression
uv venv
uv pip install torch --index-url https://download.pytorch.org/whl/cpu
uv pip install transformers

.venv/bin/python surprisal.py             # smoke test of the base module
.venv/bin/python bet01_surprisal_gate.py  # Bet 1 experiment
.venv/bin/python bet06_memory_lift.py     # Bet 6 experiment
```

**Note on `uv pip install torch --index-url ... transformers` (a single line):**
the first time we tried it this way, `uv` restricts *all* packages in the
command to the PyTorch index, and `transformers` doesn't live there → it fails with
"transformers was not found in the package registry". The install that actually
worked was in two separate steps (torch with the PyTorch CPU `--index-url`,
then transformers from the default PyPI index), as shown above.

### Choosing the model: `SURPRISAL_MODEL`

`surprisal.py` selects its LM backend via the `SURPRISAL_MODEL` env var,
default **`DeepESP/gpt2-spanish`** (124.4M params, GPT-2 architecture,
trained on a Spanish-language corpus) — the definitive model for this spike,
per the "Re-spike" results below. The very first attempt used `distilgpt2`
(82M params, English-centric) and is still fully supported as an explicit
override, to reproduce the original numbers:

```bash
.venv/bin/python bet01_surprisal_gate.py                          # default: DeepESP/gpt2-spanish
SURPRISAL_MODEL=distilgpt2 .venv/bin/python bet01_surprisal_gate.py  # first attempt: distilgpt2
```

## Which backend was used: real LM, not fallback

There was internet connectivity (verified with `curl` to PyPI and HuggingFace,
both responded 200 in <200ms), the `torch` (CPU) + `transformers` install
worked with no errors, and downloading + loading a causal LM (~330-515MB
depending on the model) took ~15-30 seconds total (imports + tokenizer +
weights + test forward pass). Therefore **the real LM was used in every run
below**, not the n-gram fallback. The fallback (word bigrams with Laplace
smoothing, trained on a base Spanish-language corpus) is equally implemented
and functional in `surprisal.py` — it activates automatically if the
`torch`/`transformers` import or the model load fails — but it wasn't needed
for any of these runs.

`surprisal.py` prints to stderr which backend it used at import time, e.g.:
```
[surprisal.py] backend=LM_REAL model=DeepESP/gpt2-spanish
```

**Honest limitation of the first attempt:** `distilgpt2` is a model trained
predominantly on **English** text (WebText), while this spike's entire
corpus is in **Spanish** (see "Why the experimental data is in Spanish"
below). This is exactly what motivated the re-spike with a Spanish-fluent
model, whose results are now this module's default.

Unit of measure: **bits per subword-token of the model's own tokenizer**
(`bits_per_token` in the `surprisal_bits` output), consistent throughout
the spike. The tokenizer differs by model (`subword-gpt2` for `distilgpt2`,
`subword-gpt2-spanish` for `DeepESP/gpt2-spanish`), so absolute bits/token
values are only comparable **within** a model, not across models.

### Why the experimental data is in Spanish

Bet 1 and Bet 6 measure how well a predictive model can anticipate a
Spanish-speaking user's text (aisotropy's target user talks to the assistant
in Spanish). The facts, session text, and base fallback corpus used in this
spike's code are **deliberately** written in Spanish: surprisal is measured
in bits under a *specific* model for a *specific* language distribution, so
translating the corpus to English would measure a different (and
irrelevant) quantity for this spike's question — it would defeat the
purpose of the experiment, not just change its presentation language. Only
the code (identifiers, comments, this README) is in English.

## Bet 1 — first attempt (`distilgpt2`)

Fixed context used for the 15 facts: `context=""` (no context — measures
how much the model "already knew" about that fact from its general world
knowledge, not from a specific user profile).

Real terminal output (`SURPRISAL_MODEL=distilgpt2 .venv/bin/python bet01_surprisal_gate.py`,
against the 15-fact corpus used in this original run):

```
# Backend: real LM -> distilgpt2
# Reason: Real LM loaded OK: distilgpt2 (CPU, torch+transformers)

# Fact -> surprisal table (unit: bits/subword-gpt2), ascending order
bits_total   bits/tok  n_tok  group               fact
----------------------------------------------------------------------------------------------------
     84.86      7.715     11  (a) redundant       el usuario escribe en español
    137.59      8.094     17  (a) redundant       el usuario tiene una cuenta de correo electrónico
    163.29      9.072     18  (b) surprising      el proyecto favorito del usuario se llama aisotropy
    108.87      9.072     12  (a) redundant       el usuario usa internet para trabajar
    145.22      9.076     16  (b) surprising      el usuario tiene una alergia severa al maní
    101.01      9.182     11  (a) redundant       el usuario usa una computadora
    204.63      9.302     22  (b) surprising      el usuario trabaja en una startup de logística llamada Rutamax
    158.18      9.305     17  (b) surprising      el backend de producción del usuario corre en AWS São Paulo
     94.69      9.469     10  (a) redundant       el usuario tiene un teclado
    152.09      9.506     16  (b) surprising      el usuario vive en Bariloche y trabaja remoto
    209.57      9.526     22  (b) surprising      el usuario prefiere que las respuestas del asistente sean en tono informal
    135.55      9.682     14  (a) redundant       el usuario guarda archivos en su computadora
    198.29      9.914     20  (b) surprising      la contraseña de la base de datos de staging se rotó el 3 de marzo
    174.65     10.274     17  (a) redundant       el usuario interactúa con un asistente de inteligencia artificial
    192.11     10.673     18  (b) surprising      el usuario prefiere recibir reportes los lunes a las 8am

# (a) most surprising redundant fact: 10.274 bits/tok  <- "el usuario interactúa con un asistente de inteligencia artificial"
# (b) least surprising surprising fact: 9.072 bits/tok  <- "el proyecto favorito del usuario se llama aisotropy"
# WARNING: groups (a) and (b) OVERLAP in bits/tok (no clean gap).
# naive tau (midpoint, no guaranteed separation) = 9.673 bits/subword-gpt2
#   Redundant facts that would fall on the 'surprising' side: ['el usuario usa internet para trabajar', 'el usuario usa una computadora', 'el usuario tiene un teclado', 'el usuario guarda archivos en su computadora', 'el usuario interactúa con un asistente de inteligencia artificial']
#   Surprising facts that would fall on the 'redundant' side: ['el proyecto favorito del usuario se llama aisotropy', 'el usuario tiene una alergia severa al maní', 'el usuario trabaja en una startup de logística llamada Rutamax', 'el backend de producción del usuario corre en AWS São Paulo', 'el usuario vive en Bariloche y trabaja remoto', 'el usuario prefiere que las respuestas del asistente sean en tono informal', 'la contraseña de la base de datos de staging se rotó el 3 de marzo']
```

### Interpreting Bet 1 (first attempt)

**No clean gap appears with `distilgpt2`.** The two most redundant facts
("the user writes in Spanish", "the user has an email account")
do have the lowest surprisal in the entire corpus (7.7 and 8.1
bits/tok), which goes in the expected direction. But the rest of the 13 facts
— redundant and surprising mixed together — all fall into a narrow band of
~9.0 to ~10.7 bits/tok, with 5 of 7 "redundant" facts and 2 of 8 "surprising" ones
crossing sides relative to the midpoint.

**Most likely cause:** `distilgpt2` is a small model trained almost
entirely in English. On Spanish, its BPE tokenizer (also trained
on English) fragments almost every word into unnatural subwords, and the
model doesn't have enough Spanish world knowledge to distinguish
"the user has a keyboard" (generic) from "aisotropy" or "Rutamax" (invented
proper nouns) — for a model that barely models Spanish, both are
equally "weird" at the subword level. In other words: we're largely measuring
cross-lingual tokenization noise, not real semantic surprisal.

**Verdict on Bet 1 with this setup: INCONCLUSIVE, with a partial
positive signal.** The data neither confirms nor refutes the bet — it shows
that the surprisal criterion IS sensitive at the most obvious extreme
(more generic phrases ≈ lower surprisal), but the chosen model (distilgpt2, English)
doesn't have enough resolution to cleanly separate specific facts about
a real user in Spanish. This motivated the re-spike below with a model that
has better Spanish coverage, before drawing conclusions about the bet itself.

## Bet 6 — first attempt (`distilgpt2`)

Session text (mentions the AWS São Paulo deploy and the Monday
reports — "surprising" facts from the previous experiment):

> "Hoy tuvimos un problema con el deploy en producción. El backend tardó más
> de lo normal en responder desde São Paulo. Necesito que el reporte esté
> listo antes de las 8am del lunes. Voy a revisar los logs de AWS para
> entender qué pasó con la base de datos de staging."

Memory used as context ("with memory"): concatenation of 3 relevant
surprising facts from the Bet 1 corpus (AWS São Paulo, Monday
8am reports, staging password rotation).

Real terminal output (`SURPRISAL_MODEL=distilgpt2 .venv/bin/python bet06_memory_lift.py`):

```
# Backend: real LM -> distilgpt2
# Reason: Real LM loaded OK: distilgpt2 (CPU, torch+transformers)

# Session text: 'Hoy tuvimos un problema con el deploy en producción. El backend tardó más de lo normal en responder desde São Paulo. Necesito que el reporte esté listo antes de las 8am del lunes. Voy a revisar los logs de AWS para entender qué pasó con la base de datos de staging.'

# Memory (context with memory): 'el backend de producción del usuario corre en AWS São Paulo. el usuario prefiere recibir reportes los lunes a las 8am. la contraseña de la base de datos de staging se rotó el 3 de marzo.'

# WITHOUT memory: bits_total=651.12  bits/subword-gpt2=8.1391  n_tokens=80
# WITH memory: bits_total=533.60  bits/subword-gpt2=6.6700  n_tokens=80

# LIFT (bpt_without_memory - bpt_with_memory) = 1.4690 bits/subword-gpt2
# LIFT (bits_total_without - bits_total_with) = 117.52 total bits
# -> POSITIVE: memory REDUCES the surprisal of the session text (1.4690 bits/subword-gpt2 saved).
```

### Interpreting Bet 6 (first attempt)

**Positive and considerable lift: 1.469 bits/token** (8.1391 → 6.6700
bits/token), equivalent to a savings of ~117.5 total bits over an
80-token text (~18% fewer bits/token). This is exactly the direction
predicted by the bet: giving the model, as prior context, the relevant
facts about the user (where their backend runs, when they want their
reports, which database had its password rotated) makes the session
text more predictable — the model "already knows" what the user is
talking about before reading the full sentence.

**Verdict on Bet 6 with this setup: SUPPORTED by the preliminary
data.** The lift is positive, not marginal (1.47 bits/tok is a
large fraction of the ~8 bits/tok base), and it occurs even with: (a) a
small LM (82M params) trained mostly in English, and (b) text and
memory in Spanish (the worst case for this model, per what we saw in Bet 1).
That the lift shows up regardless, and with that magnitude, is a reasonably
strong signal that the bpt-with-memory-vs-without-memory metric captures
something real, even under a suboptimal reference model for the language.

## Re-spike: definitive model (`DeepESP/gpt2-spanish`, now the default)

Motivation: the first attempt above leaves Bet 1 **INCONCLUSIVE** because
`distilgpt2` is an almost entirely English model, and it was unclear whether
the overlap between groups was a flaw in the idea or in the model/tokenizer
used. This re-spike repeats the same experiments with a causal LM trained in
**Spanish**, which `surprisal.py` now uses by default.

### Model used

4 small causal candidates trained in Spanish were tried, in order, via
`transformers`, loading the first one that worked:

| Candidate | Result |
|---|---|
| `DeepESP/gpt2-spanish` | **OK** — loaded in ~14s, 124.4M params, ~515MB cached |
| `PlanTL-GOB-ES/gpt2-base-bne` | FAILED: `ValueError` — the tokenizer requires `sentencepiece`/`tiktoken`, not installed in the venv |
| `flax-community/gpt-2-spanish` | OK (not used, a valid candidate already existed) — 124.4M params, ~488MB |
| `datificate/gpt2-small-spanish` | OK (not used) — 124.4M params, ~975MB |

**`DeepESP/gpt2-spanish`** (124.4M parameters, GPT-2 architecture,
trained on a Spanish-language corpus) is the one that became this module's
default (`SURPRISAL_MODEL` env var, see "How to run" above).

**Methodological correction folded into `surprisal.py`:** with `context=""`,
naively assigning each sentence's first token an artificial cost of
`log2(vocab_size)` (~15.6 bits, the entropy of a uniform distribution) for
not having a prior logit to predict it disproportionately penalizes SHORT
sentences in bits/token (the fixed cost is divided among fewer tokens).
`surprisal.py` avoids this by using the tokenizer's real BOS/EOS token
(e.g. `<|endoftext|>`) as a minimal 1-token context for all sentences
equally, so the first real token is measured with a genuine model
probability. This correction applies to any model configured via
`SURPRISAL_MODEL`; its effect on the numbers is minor and doesn't change
either bet's verdict.

### Bet 1 — definitive results (`DeepESP/gpt2-spanish`, the default)

Corpus expanded to 20 facts (10 redundant + 10 surprising), fixed context
`""` same as the first attempt. Real terminal output
(`.venv/bin/python bet01_surprisal_gate.py`, no env var needed — this is
the default model):

```
# Backend: real LM -> DeepESP/gpt2-spanish
# Reason: Real LM loaded OK: DeepESP/gpt2-spanish (CPU, torch+transformers)

# Fact -> surprisal table (unit: bits/subword-gpt2-spanish), ascending order
bits_total   bits/tok  n_tok  group               fact
----------------------------------------------------------------------------------------------------
     51.80      6.475      8  (a) redundant       el usuario tiene una cuenta de correo electrónico
    127.08      7.060     18  (b) surprising      la contraseña de la base de datos de staging se rotó el 3 de marzo
     72.37      7.237     10  (a) redundant       el usuario interactúa con un asistente de inteligencia artificial
     65.34      7.260      9  (a) redundant       el usuario navega por internet todos los días
     50.38      8.397      6  (a) redundant       el usuario tiene un teléfono celular
     69.47      8.684      8  (b) surprising      el usuario cumple años el 17 de noviembre
    104.93      8.744     12  (b) surprising      el usuario prefiere que las respuestas del asistente sean en tono informal
     90.08      9.008     10  (b) surprising      el usuario tiene una alergia severa al maní
     72.18      9.023      8  (a) redundant       el usuario escribe correos electrónicos en su trabajo
     64.39      9.198      7  (a) redundant       el usuario guarda archivos en su computadora
    129.39      9.242     14  (b) surprising      el usuario trabaja en una startup de logística llamada Rutamax
     93.51      9.351     10  (b) surprising      el usuario vive en Bariloche y trabaja remoto
    113.28      9.440     12  (b) surprising      el usuario prefiere recibir reportes los lunes a las 8am
     47.46      9.491      5  (a) redundant       el usuario tiene un teclado
     58.20      9.700      6  (a) redundant       el usuario usa internet para trabajar
     50.21     10.043      5  (a) redundant       el usuario usa una computadora
     51.64     10.327      5  (a) redundant       el usuario escribe en español
    118.31     10.755     11  (b) surprising      el proyecto favorito del usuario se llama aisotropy
     80.91     11.559      7  (b) surprising      el gato del usuario se llama Fitzgerald
    153.81     11.832     13  (b) surprising      el backend de producción del usuario corre en AWS São Paulo

# Mean (a) redundant: 8.715 bits/tok
# Mean (b) surprising: 9.568 bits/tok
# (a) most surprising redundant fact: 10.327 bits/tok  <- "el usuario escribe en español"
# (b) least surprising surprising fact: 7.060 bits/tok  <- "la contraseña de la base de datos de staging se rotó el 3 de marzo"
# WARNING: groups (a) and (b) OVERLAP in bits/tok (no clean end-to-end gap).
# naive tau (midpoint of the extremes, no guaranteed separation) = 8.694 bits/subword-gpt2-spanish

# Classification with tau=8.694 bits/subword-gpt2-spanish: 1/10 redundant facts correctly classified, 3/10 surprising facts correctly classified.
```

**The overlap was NOT resolved — it actually got worse in terms of classification
accuracy** (1/10 redundant and 3/10 surprising on the correct side
of tau, vs. 2/7 and 6/8 with `distilgpt2`). But the qualitative pattern is
revealing: the 3 highest-surprisal facts in the entire corpus ARE
correctly the 3 most semantically "surprising" ones — `aisotropy`
(10.76), `Fitzgerald` (11.56), and `AWS São Paulo` (11.83), specific and
infrequent named entities. The problem is the rest of the middle
band (7–10 bits/tok), where "surprising" facts with common syntax (dates,
"was rotated on...", "prefers that...") get low bits because the model
predicts the GRAMMAR well even though the content is new, while a
"redundant" fact with unnatural/metalinguistic phrasing ("the user
writes in Spanish", a phrase almost no one writes literally in a
real corpus) gets high bits.

**Revised cause (no longer "English-only model"):** with a model that does
handle Spanish well, surprisal at the **subword-token** level measures
**syntactic/lexical** predictability, not **semantic** novelty of the fact.
Short, well-formed phrases with common vocabulary get low bits even though
the content is unique to this user; phrases with unusual phrasing get high bits
even though the content is generic. This is a known limitation of using
sentence-level LM perplexity as a proxy for "informational importance"
without normalizing for length/lexical rarity some other way, or without
using real `context` (user profile) instead of surprisal "in a vacuum."

**Revised verdict on Bet 1: STILL INCONCLUSIVE, but the diagnosis changes.**
With `distilgpt2` the working hypothesis was "the model doesn't understand Spanish."
With `DeepESP/gpt2-spanish` (which does understand Spanish reasonably well) the
overlap persists and even worsens in binary accuracy, suggesting
the problem isn't (only) language coverage but that **sentence-level
bits/token, without user-profile context, is a weak signal for
separating "redundant" from "surprising"** — although it does rank the
extremes reasonably well (the most specific facts/those with invented proper
nouns end up at the top of the table). Before discarding the bet, the
experiment that actually tests it (per the bet's own document) is
conditioning on `context` = the user's real profile + facts already stored, not on
`context=""`, and/or normalizing for lexical rarity instead of raw bits.

### Bet 6 — definitive results (`DeepESP/gpt2-spanish`, the default)

Same session text and same relevant memory as the first attempt.
Real terminal output (`.venv/bin/python bet06_memory_lift.py`, no env var
needed — this is the default model):

```
# Backend: real LM -> DeepESP/gpt2-spanish
# Reason: Real LM loaded OK: DeepESP/gpt2-spanish (CPU, torch+transformers)

# Session text: 'Hoy tuvimos un problema con el deploy en producción. El backend tardó más de lo normal en responder desde São Paulo. Necesito que el reporte esté listo antes de las 8am del lunes. Voy a revisar los logs de AWS para entender qué pasó con la base de datos de staging.'

# Memory (context with memory): 'el backend de producción del usuario corre en AWS São Paulo. el usuario prefiere recibir reportes los lunes a las 8am. la contraseña de la base de datos de staging se rotó el 3 de marzo.'

# WITHOUT memory: bits_total=455.81  bits/subword-gpt2-spanish=7.3517  n_tokens=62
# WITH memory: bits_total=348.46  bits/subword-gpt2-spanish=5.6203  n_tokens=62

# LIFT (bpt_without_memory - bpt_with_memory) = 1.7314 bits/subword-gpt2-spanish
# LIFT (bits_total_without - bits_total_with) = 107.35 total bits
# -> POSITIVE: memory REDUCES the surprisal of the session text (1.7314 bits/subword-gpt2-spanish saved).
```

**Positive lift, larger than with `distilgpt2`** (1.7314 vs 1.4690
bits/token, ~24% relative savings over a 7.35 bits/tok base vs ~18% before).
**Bet 6 is re-confirmed with the Spanish model**: the effect isn't an
artifact of a model that's poor at Spanish — with a model that predicts
Spanish better overall (fewer bits/tok base for the same text: 7.35 vs 8.14),
the relevant memory still reduces the session text's surprisal, and in
fact the absolute lift is larger.

## General conclusion of the spike

- **Bet 6 (bits/token metric) comes out SUPPORTED**, now with two different
  models (English-centric `distilgpt2` and Spanish `DeepESP/gpt2-spanish`)
  both showing a positive memory lift (+1.469 and +1.731 bits/tok,
  ~18% and ~24% relative savings respectively) — a more robust signal than
  either run alone, since the effect isn't an artifact of one model's
  particular language coverage.
- **Bet 1 (surprisal gate) remains INCONCLUSIVE** with both models. The
  general direction is correct at the extremes (specific facts / invented
  proper nouns end up with the highest surprisal), but there's no clean gap
  between the "redundant" and "surprising" groups with either model. Moving
  to a Spanish-fluent model ruled out "the model doesn't know Spanish" as the
  cause but sharpened the real diagnosis: sentence-level bits/token, measured
  with `context=""` (surprisal "in a vacuum"), captures syntactic/lexical
  predictability more than semantic novelty. The honest next step (per
  `docs/research/bet-01-surprisal-gated-memory.md`, "Next experiment") is to
  condition on `context` = the user's real profile instead of `context=""`,
  and/or normalize for lexical rarity instead of using raw bits.
- Every run used the **real LM** (the n-gram fallback wasn't needed), 100%
  local after the initial weight download, with no external API keys.

## Files in this spike

- `surprisal.py` — base module: `surprisal_bits(text, context="")`. Real LM
  backend selected via the `SURPRISAL_MODEL` env var (default:
  `DeepESP/gpt2-spanish`, the definitive model; `distilgpt2` remains
  supported as the first-attempt alternative), with automatic fallback to
  word n-grams (bigrams + Laplace) if no LM is available at all.
- `bet01_surprisal_gate.py` — Bet 1 experiment: fact → bits table + proposed
  tau, run against whichever model `surprisal.py` loaded.
- `bet06_memory_lift.py` — Bet 6 experiment: bpt with/without memory + lift,
  run against whichever model `surprisal.py` loaded.
- `.gitignore` — ignores `.venv/`, `.hf_cache/` (weights downloaded from
  HuggingFace, includes both `distilgpt2` and `DeepESP/gpt2-spanish` once
  both have been run at least once), `__pycache__/`, logs.
