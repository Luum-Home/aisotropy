#!/usr/bin/env bash
# Start the REAL multilingual-e5-small /embeddings server on 127.0.0.1:8899
# in its own isolated venv. Idempotent: reuses the venv if it already exists.
#
#   ./start-embed-server.sh            # foreground
#   ./start-embed-server.sh &          # background
#
# Verify once up:
#   curl -s 127.0.0.1:8899/health
#   curl -s 127.0.0.1:8899/embeddings \
#     -H 'content-type: application/json' \
#     -d '{"model":"multilingual-e5-small-query","input":"hello"}' | head -c 200
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRV="$HERE/embed-server"
VENV="$SRV/.venv"
HOST="127.0.0.1"
PORT="8899"

# Interpreter preference. This used to say "3.14 may not yet have wheels" — that
# is no longer true: a rebuild under Python 3.14.6 on 2026-07-27 installed torch
# and sentence-transformers cleanly and served real 384-dim e5 vectors. The
# preference order is kept anyway (older, longest-tested runtimes first) and the
# install now FAILS LOUDLY with a remedy rather than assuming a version is fine.
PY="${ARENA_EMBED_PYTHON:-}"
if [ -z "$PY" ]; then
  if command -v python3.13 >/dev/null 2>&1; then PY="python3.13"
  elif command -v python3.12 >/dev/null 2>&1; then PY="python3.12"
  elif command -v python3.11 >/dev/null 2>&1; then PY="python3.11"
  else PY="python3"; fi
fi
echo "PROGRESS: using interpreter $($PY --version 2>&1) ($PY)" >&2

# Test the INTERPRETER, not the directory. A venv's bin/python is a symlink to
# the exact interpreter that created it, so upgrading or removing that Python
# leaves a directory that still exists and no longer works: every command in it
# dies with "bad interpreter: No such file or directory". The old `[ ! -d ]`
# check saw the directory, took the reuse branch, and the server failed to boot
# — silently breaking every RAG and semantic run that depends on this embedder.
# Observed exactly that on 2026-07-27 after the host's python3.13 went away.
if [ ! -x "$VENV/bin/python" ]; then
  if [ -d "$VENV" ]; then
    echo "PROGRESS: venv at $VENV is stale (its interpreter is gone) — rebuilding" >&2
    rm -rf "$VENV"
  fi
  echo "PROGRESS: creating venv at $VENV" >&2
  "$PY" -m venv "$VENV"
  # shellcheck disable=SC1091
  source "$VENV/bin/activate"
  python -m pip install --quiet --upgrade pip
  echo "PROGRESS: installing deps (first run pulls torch, a few minutes)" >&2
  if ! python -m pip install --quiet -r "$SRV/requirements.txt"; then
    echo "ERROR: dependency install failed under $($PY --version 2>&1)." >&2
    echo "       torch/sentence-transformers may not publish wheels for this Python yet." >&2
    echo "       Install a supported interpreter (3.11-3.13) and re-run, or pin one:" >&2
    echo "         ARENA_EMBED_PYTHON=python3.13 $0" >&2
    exit 2
  fi
else
  # shellcheck disable=SC1091
  source "$VENV/bin/activate"
fi

# --workers > 1: the e5 encode is CPU-bound (holds the GIL), so a single uvicorn
# worker SERIALIZES every embed request across all arena adapters. Under a
# concurrency>1 correctness sweep the 30-message semantic-recall persists queue
# behind one another, blow each adapter's per-turn budget, and surface as
# cascading "context deadline exceeded" (rqlite) / "TimeoutError" (node) errors
# that are pure infra contention, not framework quality. N worker PROCESSES
# (each loads its own ~120MB model copy) parallelize the CPU work. Env-tunable.
WORKERS="${ARENA_EMBED_WORKERS:-4}"
echo "PROGRESS: starting embed server on http://$HOST:$PORT (workers=$WORKERS)" >&2
exec uvicorn app:app --app-dir "$SRV" --host "$HOST" --port "$PORT" --log-level warning --workers "$WORKERS"
