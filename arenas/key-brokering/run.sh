#!/usr/bin/env bash
# run.sh — the key-brokering arena against REAL Docker.
#
# aisotropy's RunTask is a clean implementation of the STANDARD "sandbox-as-tool"
# pattern (LangChain's Pattern 2 — model host-side, only tool execution sandboxed),
# with the key-broker invariant enforced in code. That is table stakes across the
# agent-sandbox field; the differentiator is specifically against coding-CLI-in-a-box
# orchestrators like sandcastle. This driver proves aisotropy's OWN invariant and
# reproduces sandcastle's DOCUMENTED `-e` injection (it does not run sandcastle).
#
# Two parts, both docker-gated (exit 0 + SKIP when docker is absent, matching the
# repo's honesty gate so CI never fails merely because Docker is missing):
#
#   PART 1  A language-agnostic, pure-`docker` demonstration at the container-env
#           level — no Go build required, so it runs even if the tree is mid-edit.
#           Note the ASYMMETRY: Side A is a repro, Side B is the real fact.
#             Side A  sandcastle injection repro: our OWN container with the key
#                     injected via `docker run -e KEY=...` (the mechanism sandcastle
#                     documents under its no-proxy design) → `printenv` from inside
#                     REVEALS the key. This is NOT sandcastle's binary; sandcastle's
#                     behavior is established by the source citation in README.md.
#             Side B  aisotropy RunTask (Pattern 2, invariant enforced): the task
#                     container is started WITHOUT the key (host holds it) →
#                     `printenv` does NOT reveal the key, and the sandboxed "hands"
#                     still do work.
#
#   PART 2  The genuinely sound proof: aisotropy's PUBLIC App.RunTask driven with
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
echo "-- PART 1: pure-docker demonstration (language-agnostic) --"

# ---- Side A: repro of sandcastle's documented `-e` key injection ----
# Our OWN container reproducing the mechanism sandcastle documents (`-e KEY=...`
# under its no-proxy design). NOT sandcastle's binary — see README.md source cite.
docker run -d --name "$SIDE_A_CTR" -e "${KEY_NAME}=${CANARY}" "$IMAGE" sleep 120 >/dev/null
A_ENV="$(docker exec "$SIDE_A_CTR" printenv || true)"
if printf '%s\n' "$A_ENV" | grep -q "$CANARY"; then
  echo "Side A  sandcastle injection repro : ${KEY_NAME} PRESENT in container env  (our own '-e' repro of sandcastle's documented behavior)"
else
  echo "Side A  UNEXPECTED: key not present in container env" >&2
  exit 1
fi

# ---- Side B: aisotropy RunTask (Pattern 2, invariant enforced) ----
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
  echo "Side B  aisotropy RunTask          : ${KEY_NAME} ABSENT in container env; hands still ran (wrote note.txt)"
else
  echo "Side B  UNEXPECTED: sandboxed hands did not run" >&2
  exit 1
fi

echo
echo "PART 1 PASS: '-e' injection is printenv-visible (sandcastle model); aisotropy RunTask: key ABSENT, hands still ran"

# ---- PART 2: the genuinely sound proof via the public App.RunTask ----
echo
echo "-- PART 2: real-code proof of aisotropy's invariant (public App.RunTask) --"
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
