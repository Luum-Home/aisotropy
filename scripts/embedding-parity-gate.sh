#!/usr/bin/env bash
# Embedding-parity gate — ADR-0064 Layer 4's precondition.
#
# WHY THIS MUST EXIST BEFORE ANY EMBEDDER SWAP. Swapping the embedding server
# (e.g. the single-process FastAPI e5 server for HuggingFace TEI) is not a
# drop-in: e5 models are trained with instruction prefixes ("query: " /
# "passage: ") and degrade badly without the right one. Our current server picks
# the prefix from the MODEL-ID SUFFIX (`...-query` / `...-passage`, see
# arenas/live/_shared/embed-server/app.py); TEI has no such convention — it uses
# `--default-prompt-name` per instance or a per-request `prompt_name`. A swap
# that gets this wrong does not fail loudly. It returns vectors of the right
# shape, in the right dimension, with a 200 OK — and every retrieval quietly
# gets worse. Nothing in the stack would catch it: the health check passes, the
# arena's RAG score drops a few points, and the cause is invisible.
#
# So the swap is gated on evidence: embed the SAME inputs through both servers
# and require near-identical vectors. Cosine similarity is the right measure —
# it is exactly what retrieval ranks on, so parity in cosine is parity in the
# only property that matters downstream.
#
# USAGE
#   scripts/embedding-parity-gate.sh <baseline-url> <candidate-url> [model-id]
#
#   # sanity-check the gate itself (a server must agree with itself: 1.000000)
#   scripts/embedding-parity-gate.sh http://127.0.0.1:8899 http://127.0.0.1:8899
#
#   # the real gate, once a candidate is running
#   scripts/embedding-parity-gate.sh http://127.0.0.1:8899 http://127.0.0.1:8080
#
# EXIT CODES: 0 = parity holds (safe to swap) · 1 = parity FAILED (do not swap)
#             2 = could not evaluate (a server unreachable, bad response)
#
# A gate that cannot run must never absolve: unreachable/garbage responses exit
# 2, never 0.
set -uo pipefail

BASELINE="${1:-}"
CANDIDATE="${2:-}"
MODEL="${3:-multilingual-e5-small-query}"
# Cosine below this fails the gate. 0.999 leaves room for float32 vs float64
# accumulation differences between two implementations of the same model while
# still catching a wrong/missing instruction prefix, which moves cosine far more
# (typically well below 0.95).
THRESHOLD="${EMBED_PARITY_THRESHOLD:-0.999}"

if [ -z "$BASELINE" ] || [ -z "$CANDIDATE" ]; then
  sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
fi

# Inputs deliberately span the shapes that expose a prefix/tokenisation
# mismatch: a short query, a long passage, punctuation, non-ASCII, and a
# near-duplicate pair (whose RELATIVE ordering must survive the swap).
INPUTS='[
  "What is the refund window?",
  "The Lost Property Office retains recovered items for 60 days before they are donated to charity.",
  "¿Dónde está la oficina de objetos perdidos?",
  "refund window",
  "refund windows"
]'

probe() { # probe <url> -> prints the embeddings JSON, or empty on failure
  curl -s -m 30 -X POST "$1/embeddings" \
    -H 'content-type: application/json' \
    -d "{\"model\":\"$MODEL\",\"input\":$INPUTS,\"input_type\":\"query\"}" 2>/dev/null
}

echo "embedding-parity-gate"
echo "  baseline : $BASELINE"
echo "  candidate: $CANDIDATE"
echo "  model    : $MODEL"
echo "  threshold: $THRESHOLD (cosine)"
echo "-------------------------------------------------------------"

A="$(probe "$BASELINE")"
if [ -z "$A" ]; then
  echo "CANNOT EVALUATE: baseline $BASELINE did not answer /embeddings" >&2
  exit 2
fi
B="$(probe "$CANDIDATE")"
if [ -z "$B" ]; then
  echo "CANNOT EVALUATE: candidate $CANDIDATE did not answer /embeddings" >&2
  exit 2
fi

A="$A" B="$B" THRESHOLD="$THRESHOLD" python3 - <<'PY'
import json, math, os, sys

def vectors(raw, who):
    try:
        doc = json.loads(raw)
    except Exception as e:
        print(f"CANNOT EVALUATE: {who} returned non-JSON ({e})", file=sys.stderr)
        sys.exit(2)
    data = doc.get("data")
    if not isinstance(data, list) or not data:
        print(f"CANNOT EVALUATE: {who} returned no embeddings", file=sys.stderr)
        sys.exit(2)
    out = []
    for item in data:
        v = item.get("embedding")
        if not isinstance(v, list) or not v:
            print(f"CANNOT EVALUATE: {who} returned an empty embedding", file=sys.stderr)
            sys.exit(2)
        out.append([float(x) for x in v])
    return out

def cosine(u, v):
    num = sum(a * b for a, b in zip(u, v))
    du = math.sqrt(sum(a * a for a in u))
    dv = math.sqrt(sum(b * b for b in v))
    if du == 0 or dv == 0:
        return 0.0
    return num / (du * dv)

a = vectors(os.environ["A"], "baseline")
b = vectors(os.environ["B"], "candidate")
threshold = float(os.environ["THRESHOLD"])

if len(a) != len(b):
    print(f"FAIL: baseline returned {len(a)} vectors, candidate {len(b)}", file=sys.stderr)
    sys.exit(1)
if len(a[0]) != len(b[0]):
    print(f"FAIL: dimension mismatch — baseline {len(a[0])}, candidate {len(b[0])}", file=sys.stderr)
    sys.exit(1)

worst, failures = 1.0, 0
for i, (u, v) in enumerate(zip(a, b)):
    c = cosine(u, v)
    worst = min(worst, c)
    status = "ok" if c >= threshold else "FAIL"
    if c < threshold:
        failures += 1
    print(f"  input[{i}] cosine={c:.6f}  {status}")

# Relative ordering is the property retrieval actually depends on: even if the
# absolute vectors drifted, a swap that reorders near-duplicates changes results.
def rank_gap(vs):
    return cosine(vs[3], vs[4])  # the near-duplicate pair

ga, gb = rank_gap(a), rank_gap(b)
print(f"  near-duplicate similarity: baseline={ga:.6f} candidate={gb:.6f} (delta={abs(ga-gb):.6f})")

print("-------------------------------------------------------------")
print(f"  dimension: {len(a[0])}   vectors: {len(a)}   worst cosine: {worst:.6f}")
if failures:
    print(f"VERDICT: FAIL — {failures} input(s) below {threshold}. DO NOT SWAP: the")
    print("         candidate is not computing the same embedding (most often a")
    print("         missing or wrong e5 instruction prefix).")
    sys.exit(1)
print("VERDICT: PASS — the candidate reproduces the baseline's embeddings.")
sys.exit(0)
PY
