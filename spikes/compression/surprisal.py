"""
surprisal.py — computes the surprisal (bits) of a text under a predictive model.

Bet 1 / Bet 6 (see docs/research/) define surprisal(x) = -log2 P(x | context).
This module exposes ONE public function, surprisal_bits(text, context=""), with
two interchangeable backends:

  1. Real LM (preferred): a causal HuggingFace `transformers` model + `torch`
     CPU, selected via the SURPRISAL_MODEL env var (default:
     `DeepESP/gpt2-spanish` — see "Why the default model is Spanish" below).
     Tokenizes `context + text`, runs the causal model, computes the
     log-softmax of the logits, and sums -log2 P(real_token) ONLY over the
     `text` tokens, conditioning each token on everything before it (context
     + previous `text` tokens already generated). This is exact surprisal
     under that LM.

  2. Fallback (if no real LM is available: no torch/transformers, no
     internet to download weights, or timeout): a word bigram model
     trained on a base Spanish-language corpus, with add-one
     (Laplace) smoothing. Gives real -log2 P(text) (not made up) under a
     simple but genuine model.

Both backends return TOTAL bits and bits/TOKEN (token unit: subword
of the model's tokenizer for the LM case, words for the fallback case —
indicated explicitly in each result via the "unit" key).

--------------------------------------------------------------------------
Model selection (SURPRISAL_MODEL env var)
--------------------------------------------------------------------------
Default: `DeepESP/gpt2-spanish`. This is the DEFINITIVE model for this spike
(see README.md, "Re-spike" section): a first attempt used `distilgpt2`
(English-centric) and left Bet 1 inconclusive with an open question of
whether the model's poor Spanish coverage was the cause. The re-spike with
`DeepESP/gpt2-spanish` is what this module now defaults to; `distilgpt2` is
kept fully working as a documented alternative:

    SURPRISAL_MODEL=distilgpt2 .venv/bin/python bet01_surprisal_gate.py

--------------------------------------------------------------------------
Why the experimental data is in Spanish
--------------------------------------------------------------------------
Bet 1 and Bet 6 measure how well a predictive model can anticipate a
Spanish-speaking user's text (aisotropy's target user talks to the assistant
in Spanish). The facts, session text, and base fallback corpus are
DELIBERATELY written in Spanish: surprisal is measured in bits under a
*specific* model for a *specific* language distribution, so translating the
corpus to English would measure a different (and irrelevant) quantity for
this spike's question — it would defeat the purpose of the experiment, not
just change its presentation language. Only the CODE (identifiers, comments,
docs) is in English.

This module decides the backend ONCE, at import time, and prints to stderr
which one it used and why. The rest of the spike (bet01_*, bet06_*) is
backend-agnostic: it only calls surprisal_bits().
"""

from __future__ import annotations

import math
import os
import re
import sys
from collections import Counter, defaultdict
from typing import Dict

# --------------------------------------------------------------------------
# Backend 1 attempt: real LM (CPU, torch + transformers), model configurable
# via SURPRISAL_MODEL. Default is the definitive Spanish model for this
# spike; distilgpt2 (the first attempt, English-centric) remains available
# as an explicit override.
# --------------------------------------------------------------------------

DEFAULT_MODEL = "DeepESP/gpt2-spanish"

USING_LM = False
MODEL_NAME = None
BACKEND_REASON = None

_tokenizer = None
_model = None
_torch = None

_HF_CACHE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".hf_cache")


def _try_load_lm(model_name: str) -> bool:
    global _tokenizer, _model, _torch, USING_LM, MODEL_NAME, BACKEND_REASON
    try:
        os.environ.setdefault("HF_HOME", _HF_CACHE_DIR)
        os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
        import torch  # type: ignore
        from transformers import AutoTokenizer, AutoModelForCausalLM  # type: ignore

        tokenizer = AutoTokenizer.from_pretrained(model_name)
        model = AutoModelForCausalLM.from_pretrained(model_name)
        model.eval()

        _tokenizer = tokenizer
        _model = model
        _torch = torch
        USING_LM = True
        MODEL_NAME = model_name
        BACKEND_REASON = f"Real LM loaded OK: {model_name} (CPU, torch+transformers)"
        return True
    except Exception as exc:  # noqa: BLE001 - we want to catch any failure and fall back
        BACKEND_REASON = f"Real LM NOT available ({model_name}): {type(exc).__name__}: {exc}"
        return False


_requested_model = os.environ.get("SURPRISAL_MODEL", DEFAULT_MODEL)
_lm_ok = _try_load_lm(_requested_model)

if _lm_ok:
    print(f"[surprisal.py] backend=LM_REAL model={MODEL_NAME}", file=sys.stderr)
else:
    print(f"[surprisal.py] backend=FALLBACK_NGRAM reason={BACKEND_REASON}", file=sys.stderr)


# --------------------------------------------------------------------------
# Backend 2: word n-gram fallback (bigrams, Laplace/add-one)
# --------------------------------------------------------------------------

_WORD_RE = re.compile(r"[a-záéíóúüñ0-9]+", re.IGNORECASE)


def _tokenize_words(text: str):
    return _WORD_RE.findall(text.lower())


# Base Spanish-language corpus to train the fallback bigram model.
# Represents "the model's general prior knowledge" — everyday, neutral
# phrases about using computers/internet/assistants, so that the
# REDUNDANT facts in the facts corpus (bet01) get high probability
# (low surprisal) under this model, and SPECIFIC facts get low
# probability (high surprisal) — same as expected from a real LM trained
# on massive internet text. See module docstring ("Why the experimental
# data is in Spanish") for why this corpus is not translated.
BASE_CORPUS = """
el usuario usa una computadora todos los días para trabajar y estudiar.
mucha gente escribe en español en sus documentos y correos electrónicos.
un teclado y un mouse son periféricos comunes de cualquier computadora.
internet se usa para trabajar, comunicarse, buscar información y estudiar.
el usuario tiene una cuenta de correo electrónico para recibir mensajes.
muchas personas necesitan ayuda con tareas técnicas de vez en cuando.
los asistentes de inteligencia artificial ayudan a responder preguntas.
el usuario guarda archivos importantes en su computadora todos los días.
la gente usa aplicaciones de mensajería para hablar con amigos y familia.
un navegador web permite visitar páginas de internet y leer noticias.
las personas suelen tener una contraseña para acceder a sus cuentas.
el usuario revisa su correo electrónico por la mañana y por la tarde.
mucha gente trabaja desde su casa usando una computadora e internet.
los datos se guardan en archivos y carpetas dentro de la computadora.
una persona común usa el teclado para escribir texto en español.
el usuario abre el navegador para buscar información en internet.
la mayoría de la gente tiene un teléfono además de una computadora.
escribir correos y documentos es una tarea habitual de oficina.
el usuario aprende a usar nuevas herramientas de software con el tiempo.
las empresas modernas dependen de internet para operar todos los días.
""".strip()


class _BigramModel:
    """Word bigram model with add-one (Laplace) smoothing."""

    def __init__(self, corpus_text: str):
        tokens = ["<s>"] + _tokenize_words(corpus_text) + ["</s>"]
        self.vocab = set(tokens)
        self.unigram_counts: Counter = Counter(tokens)
        self.bigram_counts: Dict[str, Counter] = defaultdict(Counter)
        for a, b in zip(tokens, tokens[1:]):
            self.bigram_counts[a][b] += 1
        self.V = len(self.vocab)  # vocabulary size for Laplace

    def extend_vocab(self, extra_text: str):
        """Allows using `context` as additional "memory" that temporarily
        extends the model's vocabulary/counts (simulates the system having
        already seen those facts before evaluating the new text)."""
        tokens = _tokenize_words(extra_text)
        for t in tokens:
            self.vocab.add(t)
        self.V = len(self.vocab)

    def logprob_bigram(self, prev: str, word: str) -> float:
        # P(word | prev) with Laplace: (count(prev,word)+1) / (count(prev)+V)
        c_bigram = self.bigram_counts.get(prev, Counter()).get(word, 0)
        c_prev = self.unigram_counts.get(prev, 0)
        p = (c_bigram + 1) / (c_prev + self.V)
        return math.log2(p)


_bigram_model = _BigramModel(BASE_CORPUS) if not _lm_ok else None


# --------------------------------------------------------------------------
# Public API
# --------------------------------------------------------------------------

def _surprisal_bits_lm(text: str, context: str) -> dict:
    torch = _torch
    tokenizer = _tokenizer
    model = _model

    ctx_ids = tokenizer(context, return_tensors="pt")["input_ids"] if context else None

    # When context=="", instead of assigning the text's first token an
    # artificial cost of log2(vocab_size) (the entropy of a UNIFORM
    # distribution over the vocabulary, which isn't the model's real
    # prediction and penalizes SHORT sentences more in bits/token, since
    # that fixed cost gets divided among fewer tokens), we use the
    # tokenizer's real BOS/EOS token (e.g. <|endoftext|>) as a minimal
    # 1-token context for ALL sentences equally. This way the text's real
    # first token IS measured with a genuine model probability (conditioned
    # on the sequence-start token), consistent between short and long
    # sentences.
    if ctx_ids is None or ctx_ids.shape[1] == 0:
        bos_id = tokenizer.bos_token_id if tokenizer.bos_token_id is not None else tokenizer.eos_token_id
        ctx_ids = torch.tensor([[bos_id]])

    text_ids = tokenizer(text, return_tensors="pt")["input_ids"]
    full_ids = torch.cat([ctx_ids, text_ids], dim=1)
    n_ctx = ctx_ids.shape[1]

    n_text_tokens = text_ids.shape[1]

    with torch.no_grad():
        out = model(full_ids)
        logits = out.logits[0]  # (seq_len, vocab)

    log_probs = torch.log_softmax(logits, dim=-1)

    total_bits = 0.0
    seq = full_ids[0]
    # The logit at position i predicts the token at position i+1.
    for i in range(n_ctx, n_ctx + n_text_tokens):
        pred_pos = i - 1
        token_id = seq[i].item()
        logp_natural = log_probs[pred_pos, token_id].item()
        # torch's log_softmax gives the natural log; convert to base 2.
        bits = -logp_natural / math.log(2)
        total_bits += bits

    bits_per_token = total_bits / n_text_tokens if n_text_tokens > 0 else 0.0

    return {
        "bits_total": total_bits,
        "bits_per_token": bits_per_token,
        "n_tokens": n_text_tokens,
        "unit": "subword-" + MODEL_NAME.split("/")[-1],
        "backend": f"LM:{MODEL_NAME}",
    }


def _surprisal_bits_fallback(text: str, context: str) -> dict:
    # Copy of the base model; if there's context, we treat it as "memory"
    # that extends the seen vocabulary (we don't recount context bigrams
    # to keep the base model stable and the comparison between facts clean,
    # but the context IS used in bet06 by summing its real bigrams).
    model = _bigram_model

    words = ["<s>"] + _tokenize_words(text) + ["</s>"]
    ctx_words = _tokenize_words(context) if context else []

    # If there's context, we prepend it as real bigram history
    # (the context DOES participate as prior text conditioning the first
    # token of `text`).
    if ctx_words:
        full = ["<s>"] + ctx_words + _tokenize_words(text) + ["</s>"]
        start_idx = len(["<s>"] + ctx_words)
    else:
        full = words
        start_idx = 1  # skip the initial <s>

    total_bits = 0.0
    n_tokens = 0
    for i in range(start_idx, len(full)):
        prev = full[i - 1]
        word = full[i]
        lp2 = model.logprob_bigram(prev, word)
        total_bits += -lp2
        n_tokens += 1

    bits_per_token = total_bits / n_tokens if n_tokens > 0 else 0.0

    return {
        "bits_total": total_bits,
        "bits_per_token": bits_per_token,
        "n_tokens": n_tokens,
        "unit": "word",
        "backend": "fallback:bigram-laplace",
    }


def surprisal_bits(text: str, context: str = "") -> dict:
    """Computes the surprisal of `text` in bits, conditioned on `context`.

    Returns a dict with: bits_total, bits_per_token, n_tokens, unit, backend.
    """
    if USING_LM:
        return _surprisal_bits_lm(text, context)
    return _surprisal_bits_fallback(text, context)


def backend_info() -> dict:
    return {
        "using_lm": USING_LM,
        "model_name": MODEL_NAME,
        "reason": BACKEND_REASON,
    }


if __name__ == "__main__":
    info = backend_info()
    print("Backend info:", info)
    example = surprisal_bits("el usuario usa una computadora")
    print("Example:", example)
