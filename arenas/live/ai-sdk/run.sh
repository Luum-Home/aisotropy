#!/usr/bin/env bash
# Sources the repo-root .env (agnes-2.0-flash OpenAI-compatible wiring) and pipes stdin
# through to the AI SDK adapter, streaming stdout through unchanged.
#
# Usage:
#   echo '{"system":"...","messages":[...]}' | bash arenas/live/ai-sdk/run.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

set -a
source "$REPO_ROOT/.env"
set +a

exec node "$SCRIPT_DIR/index.mjs"
