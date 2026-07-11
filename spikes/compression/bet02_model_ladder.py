"""
bet02_model_ladder.py — Bet 2 capacity-ceiling sweep.

Thin runner that invokes the EXISTING bet02 codec
(`bet02_lm_arithmetic_codec.py`) once per model across a ladder of
progressively larger GPT-2-class LMs, then tabulates bits/char vs model size.

Purpose (ADR-0021 / bet-02): the codec already proved lossless and beat gzip
~42% with distilgpt2 (2.34 bits/char) but did NOT reach the < 1 bit/char
target. bet-02's verdict was "capacity ceiling, not method failure". This
sweep tests that: does a bigger model bend bits/char toward < 1.0, and how
fast?

It does NOT rewrite the codec. It shells out to the codec as a subprocess
(fresh process per model, so memory does not accumulate) with the SAME default
text sample, parses the printed metrics, and prints a comparison table.

Models are GPT-2 family (OpenAI GPT-2 weights are MIT-licensed):
  distilgpt2   ~82M
  gpt2         124M
  gpt2-medium  355M
  gpt2-large   774M

Usage:
  python bet02_model_ladder.py                          # full default ladder
  python bet02_model_ladder.py --models distilgpt2 gpt2 # subset
  python bet02_model_ladder.py --text-file sample.txt   # custom text (both
                                                         # codec and reporting
                                                         # use the same file)

CPU note: each model does O(N) forwards twice (encode + decode). Bigger models
are much slower on CPU. Use --models to trim the ladder, or --text-file with a
shorter sample, if a run is too slow. Whatever you change, it is recorded in
the printed header so the comparison stays honest.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
CODEC = os.path.join(HERE, "bet02_lm_arithmetic_codec.py")

# (name, approx param count) — GPT-2 family, MIT-licensed (OpenAI GPT-2).
DEFAULT_LADDER = [
    ("distilgpt2", "82M"),
    ("gpt2", "124M"),
    ("gpt2-medium", "355M"),
    ("gpt2-large", "774M"),
]

# Lines emitted by the codec that we scrape.
PATTERNS = {
    "chars": re.compile(r"# text:\s*(\d+)\s*chars"),
    "gzip_bpc": re.compile(r"gzip -9\s*:\s*([\d.]+)", re.M),
    "lm_bpc": re.compile(r"LM \+ arithmetic \(packed\):\s*([\d.]+)"),
    "tokens": re.compile(r"tokens encoded\s*:\s*(\d+)"),
    "lossless": re.compile(r"lossless roundtrip\s*:\s*(\S+)"),
    "under1": re.compile(r"under 1\.0 bits/char\s*:\s*(\S+)"),
}


def run_one(model: str, window: int, text_file: str | None, python: str) -> dict:
    cmd = [python, CODEC, "--model", model, "--window", str(window)]
    if text_file:
        cmd += ["--text-file", text_file]
    env = dict(os.environ)
    # Share the big HF cache so we don't re-download per invocation.
    env.setdefault("HF_HOME", os.path.expanduser("~/.cache/huggingface"))
    t0 = time.time()
    proc = subprocess.run(cmd, capture_output=True, text=True, env=env)
    secs = time.time() - t0
    out = proc.stdout
    res: dict = {"model": model, "secs": secs, "rc": proc.returncode}
    if proc.returncode != 0:
        res["error"] = (proc.stderr or "")[-800:]
        return res
    for key, pat in PATTERNS.items():
        m = pat.search(out)
        res[key] = m.group(1) if m else None
    # numeric coercions
    for k in ("chars", "tokens"):
        if res.get(k) is not None:
            res[k] = int(res[k])
    for k in ("gzip_bpc", "lm_bpc"):
        if res.get(k) is not None:
            res[k] = float(res[k])
    res["lossless_yes"] = (res.get("lossless") == "YES")
    return res


def main() -> int:
    ap = argparse.ArgumentParser(description="Bet 2 — model-size ladder sweep")
    ap.add_argument("--models", nargs="*", default=None,
                    help="subset of model names (default: full GPT-2 ladder)")
    ap.add_argument("--window", type=int, default=512)
    ap.add_argument("--text-file", default=None,
                    help="text sample; passed to the codec verbatim")
    ap.add_argument("--python", default=sys.executable)
    ap.add_argument("--json-out", default=None)
    args = ap.parse_args()

    if args.models:
        known = {n: p for n, p in DEFAULT_LADDER}
        ladder = [(n, known.get(n, "?")) for n in args.models]
    else:
        ladder = DEFAULT_LADDER

    sample_desc = args.text_file if args.text_file else "CHAT_SAMPLE (default, 3298 chars)"
    print("# Bet 2 — capacity-ceiling model ladder")
    print(f"# codec : {os.path.basename(CODEC)} (unchanged)")
    print(f"# sample: {sample_desc}")
    print(f"# window: {args.window} tokens")
    print(f"# models: {', '.join(n for n, _ in ladder)}")
    print("# GPT-2 family weights are MIT-licensed (OpenAI GPT-2).")
    print()

    results = []
    for name, params in ladder:
        print(f"[run] {name} ({params}) ...", file=sys.stderr, flush=True)
        r = run_one(name, args.window, args.text_file, args.python)
        r["params"] = params
        results.append(r)
        if r.get("rc") != 0:
            print(f"[run] {name} FAILED rc={r.get('rc')}: {r.get('error','')[:200]}",
                  file=sys.stderr)
        else:
            print(f"[run] {name}: bits/char={r.get('lm_bpc')} "
                  f"lossless={r.get('lossless')} ({r['secs']:.0f}s)",
                  file=sys.stderr, flush=True)

    # ---- table ----
    print("## LADDER RESULTS")
    hdr = f"{'model':<14}{'params':>8}{'tokens':>8}{'bits/char':>11}{'lossless':>10}{'vs gzip':>10}{'<1.0?':>7}{'secs':>8}"
    print(hdr)
    print("-" * len(hdr))
    for r in results:
        if r.get("rc") != 0:
            print(f"{r['model']:<14}{r['params']:>8}{'—':>8}{'FAILED':>11}{'—':>10}{'—':>10}{'—':>7}{r['secs']:>8.0f}")
            continue
        lm = r["lm_bpc"]
        gz = r["gzip_bpc"]
        vs_gzip = f"-{(1 - lm / gz) * 100:.0f}%" if gz else "—"
        under1 = "YES" if (lm is not None and lm < 1.0) else "no"
        print(f"{r['model']:<14}{r['params']:>8}{r.get('tokens','—'):>8}"
              f"{lm:>11.4f}{('YES' if r['lossless_yes'] else 'NO'):>10}"
              f"{vs_gzip:>10}{under1:>7}{r['secs']:>8.0f}")

    # ---- trend ----
    ok = [r for r in results if r.get("rc") == 0 and r.get("lm_bpc") is not None]
    print()
    print("## TREND (bits/char vs model size)")
    if len(ok) >= 2:
        first, last = ok[0], ok[-1]
        delta = last["lm_bpc"] - first["lm_bpc"]
        print(f"  {first['model']} ({first['params']}) {first['lm_bpc']:.4f}  ->  "
              f"{last['model']} ({last['params']}) {last['lm_bpc']:.4f}   "
              f"(Δ {delta:+.4f} bits/char)")
        any_under = any(r["lm_bpc"] < 1.0 for r in ok)
        print(f"  any model < 1.0 bits/char: {'YES' if any_under else 'NO'}")
        best = min(ok, key=lambda r: r["lm_bpc"])
        print(f"  best: {best['model']} @ {best['lm_bpc']:.4f} bits/char")
    else:
        print("  not enough successful runs to compute a trend")

    if args.json_out:
        with open(args.json_out, "w") as fh:
            json.dump(results, fh, indent=2)
        print(f"\n# wrote {args.json_out}", file=sys.stderr)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
