#!/usr/bin/env bash
# run.sh — the key-brokering arena, head-to-head against REAL Docker.
#
# Two parts, both docker-gated (exit 0 + SKIP when docker is absent, matching the
# repo's honesty gate so CI never fails merely because Docker is missing):
#
#   PART 1  A language-agnostic, pure-`docker` demonstration of the two execution
#           models at the container-env level — no Go build required, so it runs
#           even if the surrounding tree is mid-edit:
#             Side A  coding-CLI-in-a-box (the sandcastle model): the provider key
#                     is injected into the container env (`docker run -e KEY=...`)
#                     because the coding agent runs INSIDE the box → `printenv`
#                     from inside REVEALS the key.
#             Side B  aisotropy RunTask (the broker): the task container is started
#                     WITHOUT the key (host holds it) → `printenv` from inside does
#                     NOT reveal the key, and the sandboxed "hands" still do work.
#
#   PART 2  The strong, real-code proof: aisotropy's PUBLIC App.RunTask driven with
#           a live key in the HOST process, asserting the task container env is
#           key-free while a real commit merges to HEAD (TestKeyBrokering_HeadToHead).
#
# Nothing here needs a network or a real provider key: the host-side "model" is a
# deterministic scripted client, and the key is a runtime-assembled dummy canary.

set -euo pipefail

IMAGE="alpine:3.20"
# Assemble the canary at runtime — no key-shaped literal in the file.
CANARY="sk-""ant-""DUMMYcanary-key-brokering-arena-DO-NOT-LEAK"
KEY_NAME="ANTHROPIC_API_KEY"

SIDE_A_CTR="keybrokering-sandcastle-model-$$"
SIDE_B_CTR="keybrokering-aisotropy-model-$$"

cleanup() {
  docker rm -f "$SIDE_A_CTR" "$SIDE_B_CTR" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== key-brokering arena =="

if ! docker version >/dev/null 2>&1; then
  echo "SKIP: docker daemon unavailable (honesty gate) — nothing proven, exit 0."
  exit 0
fi

docker image inspect "$IMAGE" >/dev/null 2>&1 || {
  echo "-- pulling $IMAGE --"
  docker pull "$IMAGE" >/dev/null
}

echo
echo "-- PART 1: pure-docker head-to-head (language-agnostic) --"

# ---- Side A: coding-CLI-in-a-box (sandcastle key-injection model) ----
# The coding agent runs INSIDE the container, so the key is injected via `-e`.
docker run -d --name "$SIDE_A_CTR" -e "${KEY_NAME}=${CANARY}" "$IMAGE" sleep 120 >/dev/null
A_ENV="$(docker exec "$SIDE_A_CTR" printenv || true)"
if printf '%s\n' "$A_ENV" | grep -q "$CANARY"; then
  echo "Side A  coding-CLI-in-a-box : ${KEY_NAME} PRESENT in container env  (injected via 'docker run -e', sandcastle model)"
else
  echo "Side A  UNEXPECTED: key not present in container env" >&2
  exit 1
fi

# ---- Side B: aisotropy RunTask (the broker) ----
# The task container is started WITHOUT the key. The key lives only in THIS host
# shell's env (exported below) — the aisotropy model spends it host-side and never
# forwards it into the sandbox. Note: NO '-e ANTHROPIC_API_KEY' on the run line.
export "${KEY_NAME}=${CANARY}"   # key is now live in the HOST process...
docker run -d --name "$SIDE_B_CTR" "$IMAGE" sleep 120 >/dev/null   # ...but NOT passed in
B_ENV="$(docker exec "$SIDE_B_CTR" printenv || true)"
if printf '%s\n' "$B_ENV" | grep -q "$CANARY"; then
  echo "Side B  LEAK: task container env carries the key" >&2
  exit 1
fi
# The sandboxed "hands" still do work with no key present.
docker exec "$SIDE_B_CTR" sh -c 'echo brokered-hands > /tmp/note.txt' >/dev/null
HANDS="$(docker exec "$SIDE_B_CTR" cat /tmp/note.txt || true)"
if [ "$HANDS" = "brokered-hands" ]; then
  echo "Side B  aisotropy RunTask   : ${KEY_NAME} ABSENT in container env; hands still ran (wrote note.txt)"
else
  echo "Side B  UNEXPECTED: sandboxed hands did not run" >&2
  exit 1
fi

echo
echo "PART 1 PASS: coding-CLI-in-a-box: key PRESENT in container env; aisotropy RunTask: key ABSENT, hands still ran"

# ---- PART 2: the real-code proof via the public App.RunTask ----
echo
echo "-- PART 2: real-code proof (aisotropy public App.RunTask) --"
if ! command -v go >/dev/null 2>&1; then
  echo "SKIP PART 2: go toolchain unavailable; PART 1 already proved the container-env fact."
  exit 0
fi

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
# Scoped to this arena package only (never a repo-wide build).
if go -C "$REPO_ROOT" test ./arenas/key-brokering/ -run TestKeyBrokering_HeadToHead -count=1 -v; then
  echo
  echo "PART 2 PASS: App.RunTask stood up a key-free task container and merged a real commit."
else
  rc=$?
  echo
  echo "PART 2 did not pass (exit $rc). PART 1 already proved the container-env fact above." >&2
  exit "$rc"
fi
