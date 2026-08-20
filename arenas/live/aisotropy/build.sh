#!/usr/bin/env bash
# build.sh — compile the WASM tool guest, then the adapter binary.
#
# The wasm_tool mode embeds calc.wasm via go:embed, so the guest MUST be built
# before the adapter. This script is the single build path shared by run.sh (the
# native runner), the Dockerfile build stage, and scripts/supply-chain-guard.sh
# (which calls it with --prepare-only), so all three produce the same embedded
# sandboxed tool from one piece of freshness logic.
#
# USAGE:
#   build.sh [OUT_BIN]        build calc.wasm (if stale/missing) + the adapter
#   build.sh --prepare-only   build ONLY the generated artifacts the module needs
#                             to compile (calc.wasm). No adapter binary is
#                             produced and no OUT_BIN is written. This is what a
#                             build GATE needs: calc.wasm is .gitignored, so a
#                             fresh clone has no such file and `go build ./...`
#                             fails on the go:embed pattern before it can check
#                             anything else.
set -euo pipefail

PREPARE_ONLY=0
if [[ "${1:-}" == "--prepare-only" ]]; then
  PREPARE_ONLY=1
  shift
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_BIN="${1:-${SCRIPT_DIR}/adapter-bin}"

# 1) Compile the WASM tool guest (wasip1 reactor) into calc.wasm.
if [[ ! -f "${SCRIPT_DIR}/calc.wasm" ]] || \
   [[ -n "$(find "${SCRIPT_DIR}/testdata/wasmguest" -name '*.go' -newer "${SCRIPT_DIR}/calc.wasm" 2>/dev/null)" ]]; then
  echo "PROGRESS: building WASM tool guest (calc.wasm)..." >&2
  ( cd "${SCRIPT_DIR}" && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o calc.wasm ./testdata/wasmguest )
fi

if [[ "${PREPARE_ONLY}" -eq 1 ]]; then
  exit 0
fi

# 2) Build the adapter binary (embeds calc.wasm).
echo "PROGRESS: building aisotropy adapter -> ${OUT_BIN}" >&2
( cd "${SCRIPT_DIR}" && go build -o "${OUT_BIN}" . )
