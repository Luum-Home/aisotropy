#!/usr/bin/env bash
# One-command local bring-up for aisotropy on a lightweight cluster.
#
# Detects Kind or k3d (whichever is installed), creates a throwaway cluster,
# builds the Distroless image from the repo Dockerfile, side-loads it into the
# cluster's nodes, and installs aisotropy — Helm if available, else raw Kustomize.
#
# The result is the MINIMAL path: one binary, in-process memory tiers, no
# external services. Opt into networked tiers with TIERS=networked.
#
# Usage:
#   deploy/local/up.sh                 # auto-detect Kind or k3d, minimal
#   ENGINE=kind deploy/local/up.sh     # force Kind
#   ENGINE=k3d  deploy/local/up.sh     # force k3d
#   INSTALLER=kustomize deploy/local/up.sh   # force kubectl -k over Helm
#   TIERS=networked deploy/local/up.sh # add rqlite + Qdrant
#
# For OrbStack / Talos / an existing cluster, skip this script — its kube-context
# already exists, so just build+push (or load) the image and run the
# `helm install` / `kubectl apply -k` at the bottom against your current context.
# See deploy/local/README.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${CLUSTER:-aisotropy}"
IMAGE="${IMAGE:-aisotropy:local}"
NAMESPACE="${NAMESPACE:-aisotropy}"
TIERS="${TIERS:-minimal}"        # minimal | networked
INSTALLER="${INSTALLER:-auto}"   # auto | helm | kustomize
ENGINE="${ENGINE:-auto}"         # auto | kind | k3d

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# --- 0. Preconditions ------------------------------------------------------
have docker || die "docker not found — needed to build the image (Docker Desktop, OrbStack, Colima, or Podman-as-docker)."
have kubectl || die "kubectl not found — install it (brew install kubectl)."

# --- 1. Pick a cluster engine ---------------------------------------------
if [[ "$ENGINE" == "auto" ]]; then
  if have kind; then ENGINE=kind
  elif have k3d; then ENGINE=k3d
  else die "neither kind nor k3d found — install one (brew install kind  OR  brew install k3d)."
  fi
fi
log "cluster engine: $ENGINE"

# --- 2. Create the cluster (idempotent) -----------------------------------
case "$ENGINE" in
  kind)
    if kind get clusters 2>/dev/null | grep -qx -- "$CLUSTER"; then
      log "kind cluster '$CLUSTER' already exists — reusing."
    else
      log "creating kind cluster '$CLUSTER'..."
      kind create cluster --name "$CLUSTER"
    fi
    kubectl config use-context "kind-$CLUSTER"
    ;;
  k3d)
    if k3d cluster list 2>/dev/null | awk '{print $1}' | grep -qx -- "$CLUSTER"; then
      log "k3d cluster '$CLUSTER' already exists — reusing."
    else
      log "creating k3d cluster '$CLUSTER'..."
      k3d cluster create "$CLUSTER"
    fi
    kubectl config use-context "k3d-$CLUSTER"
    ;;
  *) die "unknown ENGINE '$ENGINE' (use kind or k3d)";;
esac

# --- 3. Build the Distroless image ----------------------------------------
log "building image $IMAGE from $REPO_ROOT/Dockerfile ..."
docker build -t "$IMAGE" "$REPO_ROOT"

# --- 4. Side-load the image into the cluster ------------------------------
# Local clusters do not share the host Docker image store, so the tag must be
# imported explicitly. imagePullPolicy is IfNotPresent so it is never re-pulled.
case "$ENGINE" in
  kind) log "loading $IMAGE into kind..."; kind load docker-image "$IMAGE" --name "$CLUSTER";;
  k3d)  log "importing $IMAGE into k3d...";  k3d image import "$IMAGE" -c "$CLUSTER";;
esac

# --- 5. Install ------------------------------------------------------------
if [[ "$INSTALLER" == "auto" ]]; then
  if have helm; then INSTALLER=helm; else INSTALLER=kustomize; fi
fi
log "installer: $INSTALLER   tiers: $TIERS"

if [[ "$INSTALLER" == "helm" ]]; then
  HELM_ARGS=(--namespace "$NAMESPACE" --create-namespace
             --set "image.repository=${IMAGE%%:*}" --set "image.tag=${IMAGE##*:}")
  [[ "$TIERS" == "networked" ]] && HELM_ARGS+=(--set tiers.networked.enabled=true)
  helm upgrade --install aisotropy "$REPO_ROOT/deploy/helm/aisotropy" "${HELM_ARGS[@]}" --wait --timeout 180s
else
  if [[ "$TIERS" == "networked" ]]; then
    kubectl apply -k "$REPO_ROOT/deploy/k8s/overlays/networked-tiers"
  else
    kubectl apply -k "$REPO_ROOT/deploy/k8s/base"
  fi
  log "waiting for aisotropyd to be ready..."
  kubectl -n "$NAMESPACE" rollout status deploy/aisotropyd --timeout=180s
fi

# --- 6. Show how to reach it ----------------------------------------------
SVC="aisotropyd"
[[ "$INSTALLER" == "helm" ]] && SVC="aisotropy"
cat <<EOF

\033[1;32maisotropy is up on $ENGINE cluster '$CLUSTER'.\033[0m

Port-forward and check health:

  kubectl -n $NAMESPACE port-forward svc/$SVC 8080:8080 &
  curl -s localhost:8080/healthz        # -> {"status":"ok"}
  curl -s localhost:8080/health         # -> {"status":"ok"|"degraded",...}

Tear down:

  deploy/local/down.sh
EOF
