#!/usr/bin/env bash
# Tear down the local aisotropy cluster created by up.sh.
#   deploy/local/down.sh              # auto-detect engine, delete cluster
#   ENGINE=k3d deploy/local/down.sh
set -euo pipefail

CLUSTER="${CLUSTER:-aisotropy}"
ENGINE="${ENGINE:-auto}"
have() { command -v "$1" >/dev/null 2>&1; }
log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

if [[ "$ENGINE" == "auto" ]]; then
  if have kind && kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then ENGINE=kind
  elif have k3d && k3d cluster list 2>/dev/null | awk '{print $1}' | grep -qx "$CLUSTER"; then ENGINE=k3d
  else log "no kind/k3d cluster named '$CLUSTER' found — nothing to do."; exit 0
  fi
fi

case "$ENGINE" in
  kind) log "deleting kind cluster '$CLUSTER'..."; kind delete cluster --name "$CLUSTER";;
  k3d)  log "deleting k3d cluster '$CLUSTER'...";  k3d cluster delete "$CLUSTER";;
  *) echo "unknown ENGINE '$ENGINE'" >&2; exit 1;;
esac
