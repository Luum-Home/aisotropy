#!/usr/bin/env bash
# Spawn the nimbus-status MCP stdio server. A client (an aisotropy live-arena
# adapter, or `smoke.sh`, or any MCP-capable framework) execs this script and
# talks JSON-RPC 2.0 over its stdin/stdout — never HTTP, never a port.
#
# Usage:
#   ./start.sh                 # run in foreground, stdio wired to the caller
#
# An adapter's `extensibility` mode spawns this exact command as a child
# process and connects its MCP client to the child's stdin/stdout. See
# ./README.md for the full extensibility-mode contract.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$DIR"

if [ ! -d node_modules ]; then
  echo "PROGRESS: node_modules missing — running npm install" >&2
  npm install --no-audit --no-fund >&2
fi

exec node server.mjs
