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

# Python 3.13 has stable torch/sentence-transformers wheels; 3.14 may not yet.
PY="${ARENA_EMBED_PYTHON:-}"
if [ -z "$PY" ]; then
  if command -v python3.13 >/dev/null 2>&1; then PY="python3.13"
  elif command -v python3.12 >/dev/null 2>&1; then PY="python3.12"
  elif command -v python3.11 >/dev/null 2>&1; then PY="python3.11"
  else PY="python3"; fi
fi
echo "PROGRESS: using interpreter $($PY --version 2>&1) ($PY)" >&2

if [ ! -d "$VENV" ]; then
  echo "PROGRESS: creating venv at $VENV" >&2
  "$PY" -m venv "$VENV"
  # shellcheck disable=SC1091
  source "$VENV/bin/activate"
  python -m pip install --quiet --upgrade pip
  echo "PROGRESS: installing deps (first run pulls torch, a few minutes)" >&2
  python -m pip install --quiet -r "$SRV/requirements.txt"
else
  # shellcheck disable=SC1091
  source "$VENV/bin/activate"
fi

echo "PROGRESS: starting embed server on http://$HOST:$PORT" >&2
exec uvicorn app:app --app-dir "$SRV" --host "$HOST" --port "$PORT" --log-level warning
