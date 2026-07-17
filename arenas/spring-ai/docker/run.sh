#!/usr/bin/env bash
# Containerized smoke for the Spring AI Orbit arena.
#
# Builds the multi-stage image (Maven build stage -> slim JRE runtime stage) and
# runs the container, which executes the four arena demos — SmokeMain,
# ProductIsolationMain, FeatureDemoMain, StrengthsMain — offline with the keyless
# EchoChatModel and asserts every one exits 0 (the isolation smoke passes INSIDE
# the container). Cleans up the image afterward.
#
# HONEST NUANCE: this arena is a JVM library run via `main` methods, not an HTTP
# server, so "containerized smoke" == run those mains in a container and check
# exit codes — not a server-boot/health-probe test.
#
# SKIPS CLEANLY (exit 0) if Docker is unavailable, so it is safe in CI/hosts
# without a Docker daemon.
#
#   bash docker/run.sh          # build + run + assert + cleanup
#
set -euo pipefail

cd "$(dirname "$0")/.."   # arena root = the Docker build context

IMAGE="spring-ai-arena:smoke"

# ── Skip cleanly when Docker is absent or the daemon is unreachable ──────────
if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker not on PATH — containerized smoke skipped (native path still runs via scripts/run.sh)."
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker CLI present but daemon unreachable — containerized smoke skipped."
  exit 0
fi

cleanup() { docker image rm -f "$IMAGE" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> Building image ${IMAGE} (Maven build stage -> eclipse-temurin:21-jre runtime)..."
docker build -f docker/Dockerfile -t "$IMAGE" .

# Report image size (human-readable) — the JVM runtime is expectedly heavier
# than a Go/distroless static image; that's the honest JVM footprint.
size_bytes="$(docker image inspect "$IMAGE" --format '{{.Size}}' 2>/dev/null || echo 0)"
if [ "$size_bytes" -gt 0 ] 2>/dev/null; then
  size_mb=$(( size_bytes / 1024 / 1024 ))
  echo "==> Image size: ${size_mb} MB (JVM runtime image — heavier than a static Go image, as expected)."
fi

echo "==> Running container (executes the four arena demos, keyless EchoChatModel)..."
set +e
docker run --rm "$IMAGE"
rc=$?
set -e

echo
if [ "$rc" -eq 0 ]; then
  echo "RESULT: exit 0 — all four demos passed INSIDE the container (isolation smoke healthy)."
else
  echo "RESULT: exit ${rc} — containerized smoke FAILED (a demo did not exit 0)." >&2
fi
exit "$rc"
