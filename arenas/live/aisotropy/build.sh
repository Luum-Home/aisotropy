#!/usr/bin/env bash
# build.sh — compile the WASM tool guest, then the adapter binary.
#
# The wasm_tool mode embeds calc.wasm via go:embed, so the guest MUST be built
# before the adapter. This script is the single build path shared by run.sh (the
# native runner) and the Dockerfile build stage, so both produce a byte-identical
# adapter with the embedded sandboxed tool.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_BIN="${1:-${SCRIPT_DIR}/adapter-bin}"

# 1) Compile the WASM tool guest (wasip1 reactor) into calc.wasm.
if [[ ! -f "${SCRIPT_DIR}/calc.wasm" ]] || \
   [[ -n "$(find "${SCRIPT_DIR}/testdata/wasmguest" -name '*.go' -newer "${SCRIPT_DIR}/calc.wasm" 2>/dev/null)" ]]; then
  echo "PROGRESS: building WASM tool guest (calc.wasm)..." >&2
  ( cd "${SCRIPT_DIR}" && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o calc.wasm ./testdata/wasmguest )
fi

# 2) Build the adapter binary (embeds calc.wasm).
echo "PROGRESS: building aisotropy adapter -> ${OUT_BIN}" >&2
( cd "${SCRIPT_DIR}" && go build -o "${OUT_BIN}" . )
