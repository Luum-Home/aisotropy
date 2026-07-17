#!/usr/bin/env bash
# DOCKER lane for the Flue arena — builds the image, boots the container, waits
# for readiness, drives the isolation smoke against the CONTAINERIZED server over
# HTTP, asserts, and cleans up. ADDITIVE to the native smokes (scripts/*.mjs),
# which still run with `pnpm smoke` / `pnpm isolation`.
#
# Skips CLEANLY (exit 0) when Docker is unavailable — it never fakes a pass.
#
#   Usage:  bash arenas/flue/docker/run.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARENA="$(cd "$HERE/.." && pwd)"

IMAGE="aisotropy-flue-arena:smoke"
CONTAINER="aisotropy-flue-arena-smoke"
HOST_PORT="${FLUE_DOCKER_PORT:-18080}"
BASE="http://127.0.0.1:${HOST_PORT}"

# --- Clean skip when Docker is absent or its daemon is not running ------------
if ! command -v docker >/dev/null 2>&1; then
	echo "SKIP: docker binary not found — Docker smoke skipped (native smokes unaffected)."
	exit 0
fi
if ! docker info >/dev/null 2>&1; then
	echo "SKIP: docker daemon not reachable — Docker smoke skipped (native smokes unaffected)."
	exit 0
fi

cleanup() {
	docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
	# Best-effort image removal; ignore if another run is using it.
	docker rmi "$IMAGE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== [1/4] docker build ($IMAGE) — pnpm install + flue build --target node =="
docker build -t "$IMAGE" -f "$HERE/Dockerfile" "$ARENA"

echo
echo "== [2/4] docker run (container $CONTAINER, host port $HOST_PORT → 8080) =="
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" -p "${HOST_PORT}:8080" "$IMAGE" >/dev/null

echo
echo "== [3/4] wait for containerized Flue server readiness (GET /health) =="
ready=""
for i in $(seq 1 60); do
	if curl -fsS "${BASE}/health" >/dev/null 2>&1; then
		ready="yes"
		echo "  ready after ~$((i))s: $(curl -fsS "${BASE}/health")"
		break
	fi
	# Fail fast if the container died during boot.
	if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
		echo "  ERROR: container exited during boot. Logs:"
		docker logs "$CONTAINER" 2>&1 | tail -40 || true
		exit 1
	fi
	sleep 1
done
if [ -z "$ready" ]; then
	echo "  ERROR: container did not become ready in time. Logs:"
	docker logs "$CONTAINER" 2>&1 | tail -40 || true
	exit 1
fi

echo
echo "== [4/4] drive isolation smoke against the container over HTTP =="
set +e
( cd "$ARENA" && node docker/smoke-http.mjs "$BASE" )
smoke_rc=$?
set -e

echo
size_bytes="$(docker image inspect "$IMAGE" --format '{{.Size}}' 2>/dev/null || echo 0)"
if [ "$size_bytes" -gt 0 ] 2>/dev/null; then
	printf "image size: %s bytes (~%d MB)\n" "$size_bytes" "$((size_bytes / 1024 / 1024))"
fi

if [ "$smoke_rc" -eq 0 ]; then
	echo "DOCKER SMOKE: PASSED"
else
	echo "DOCKER SMOKE: FAILED (rc=$smoke_rc)"
fi
exit "$smoke_rc"
