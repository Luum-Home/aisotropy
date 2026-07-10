"""
bitrate_scorer.py — batch surprisal scorer CLI for Bet 6 (bits-per-token-per-tenant).

This is the Phase 1 OFFLINE reference-model scorer the Go core (internal/bitrate)
invokes as a subprocess, exactly like the sleep-cycle worker (ADR-0005): Go
materializes an NDJSON request file, execs this CLI with --input/--output, and
parses the NDJSON results back. No serving provider exposes logprobs, so bits/
token is measured here, out of band, by a real reference LM (or the license-clean
n-gram fallback when torch/weights are unavailable) — never on the request path.

Contract (mirrors internal/bitrate/record.go and worker.go):

  input  (NDJSON, one request per line):
    {"id": "...", "text": "...", "context": "..."}

  output (NDJSON, one result per line, same order as input):
    {"id": "...", "bits_total": F, "bits_per_token": F, "n_tokens": N,
     "model_id": "<model>@<revision-or-'pinned'>", "unit": "..."}

  stdout (one final summary line):
    {"scored": N, "model_id": "...", "backend": "LM_REAL|FALLBACK_NGRAM"}

Exit 0 on success; non-zero with a stderr diagnostic on failure.

Model: the pinned default here is `distilgpt2` (Apache-2.0, license-clean per
the repo's 100%-MIT/Apache stack rule), NOT surprisal.py's Spanish default —
this CLI sets SURPRISAL_MODEL=distilgpt2 as a floor before importing surprisal,
while still honoring an explicit SURPRISAL_MODEL override from the environment.
The chosen model_id is echoed on every result row so a downstream lift is always
attributable to the exact model that produced it.
"""

from __future__ import annotations

import argparse
import json
import os
import sys

# Pin the license-clean default BEFORE importing surprisal: surprisal.py decides
# its backend ONCE at import time from SURPRISAL_MODEL. setdefault preserves an
# explicit operator override (e.g. SURPRISAL_MODEL=DeepESP/gpt2-spanish) while
# making distilgpt2 the default for the Go-invoked scorer.
os.environ.setdefault("SURPRISAL_MODEL", "distilgpt2")

# surprisal.py lives next to this file; Python puts the script's directory on
# sys.path[0], so this plain import resolves without packaging.
from surprisal import surprisal_bits, backend_info  # noqa: E402


def _model_id(info: dict) -> str:
    """Stable model identifier echoed on every row: '<model>@<revision>'.

    The HF revision is not exposed by surprisal.py's loader, so a pinned default
    reports '@pinned'. The fallback n-gram model reports its own identity so a
    fallback-produced lift is never silently attributed to an LM.
    """
    if info.get("using_lm") and info.get("model_name"):
        return f"{info['model_name']}@pinned"
    return "ngram-bigram-laplace@pinned"


def _backend_label(info: dict) -> str:
    return "LM_REAL" if info.get("using_lm") else "FALLBACK_NGRAM"


def _score_line(raw: str, model_id: str) -> dict:
    """Score one input request line into one output result dict."""
    req = json.loads(raw)
    rid = req.get("id", "")
    text = req.get("text", "") or ""
    context = req.get("context", "") or ""
    res = surprisal_bits(text, context)
    return {
        "id": rid,
        "bits_total": res["bits_total"],
        "bits_per_token": res["bits_per_token"],
        "n_tokens": res["n_tokens"],
        "model_id": model_id,
        "unit": res["unit"],
    }


def run(input_path: str, output_path: str) -> int:
    info = backend_info()
    model_id = _model_id(info)
    backend = _backend_label(info)

    scored = 0
    with open(input_path, "r", encoding="utf-8") as fin, open(
        output_path, "w", encoding="utf-8"
    ) as fout:
        for lineno, raw in enumerate(fin, start=1):
            raw = raw.strip()
            if not raw:
                continue
            try:
                row = _score_line(raw, model_id)
            except (json.JSONDecodeError, KeyError, ValueError) as exc:
                print(
                    f"bitrate_scorer: input line {lineno}: {type(exc).__name__}: {exc}",
                    file=sys.stderr,
                )
                return 2
            fout.write(json.dumps(row, ensure_ascii=False) + "\n")
            scored += 1

    # Final one-line summary on stdout (the Go worker reads it for backend/model
    # observability; the result rows themselves come from the output file).
    print(json.dumps({"scored": scored, "model_id": model_id, "backend": backend}))
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Batch surprisal (bits/token) scorer for Bet 6."
    )
    parser.add_argument("--input", required=True, help="NDJSON request file")
    parser.add_argument("--output", required=True, help="NDJSON result file to write")
    args = parser.parse_args(argv)

    try:
        return run(args.input, args.output)
    except OSError as exc:
        print(f"bitrate_scorer: {type(exc).__name__}: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
