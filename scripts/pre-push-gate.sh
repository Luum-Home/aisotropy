#!/usr/bin/env sh
# Composite pre-push gate: the single hook target that chains every local
# pre-push check. `.git/hooks/pre-push` symlinks HERE (see install-hooks.sh);
# adding a new gate means adding a stage below, not re-pointing the hook.
#
# Why a composite: git allows exactly ONE script per hook name. The repo's
# pre-push previously pointed straight at the supply-chain guard, so a second
# gate (the OWASP security suite) had nowhere to live. This preserves the
# existing guard's behavior byte-for-byte and runs the new one after it.
#
# Stages (each blocks the push on failure, each independently skippable):
#   1. supply-chain-guard.sh  — govulncheck reachability (SKIP_SUPPLY_CHAIN_GUARD=1)
#   2. owasp-llm-scan.sh      — deterministic OWASP LLM+Agentic suite, fully
#                               offline, zero model calls (SKIP_OWASP_SCAN=1)
#   3. golden-set gate        — frozen quality baseline, deterministic CodeJudge,
#                               zero model calls (SKIP_GOLDEN_GATE=1)
#
# Enforcement here (local hooks) rather than a CI runner is deliberate: this
# repo treats local gates as authoritative (the GitHub workflow is .disabled).
# The readiness audit (docs/architecture/ai-product-readiness.md) flagged that
# the OWASP suite existed and passed but was NOT enforced on any push — a
# suite nobody runs rots silently. This closes that gap.
#
# Bypass everything once: git push --no-verify
set -eu

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"
cd "$REPO_ROOT" || { echo "pre-push-gate: not in a git repo" >&2; exit 2; }

rc=0

# ---- stage 1: supply chain (govulncheck) ------------------------------------
if [ "${SKIP_SUPPLY_CHAIN_GUARD:-0}" = "1" ]; then
  echo "pre-push-gate: stage 1/2 supply-chain-guard SKIPPED (SKIP_SUPPLY_CHAIN_GUARD=1)"
else
  if [ -x "$REPO_ROOT/scripts/supply-chain-guard.sh" ]; then
    sh "$REPO_ROOT/scripts/supply-chain-guard.sh" "$@" || rc=$?
    [ "$rc" -eq 0 ] || { echo "pre-push-gate: BLOCKED by supply-chain-guard (exit $rc)" >&2; exit "$rc"; }
  else
    echo "pre-push-gate: scripts/supply-chain-guard.sh missing or not executable — skipping stage 1" >&2
  fi
fi

# ---- stage 2: OWASP LLM + Agentic deterministic suite -----------------------
if [ "${SKIP_OWASP_SCAN:-0}" = "1" ]; then
  echo "pre-push-gate: stage 2/2 owasp-llm-scan SKIPPED (SKIP_OWASP_SCAN=1)"
else
  if [ -x "$REPO_ROOT/scripts/owasp-llm-scan.sh" ]; then
    sh "$REPO_ROOT/scripts/owasp-llm-scan.sh" || rc=$?
    [ "$rc" -eq 0 ] || { echo "pre-push-gate: BLOCKED by owasp-llm-scan (exit $rc)" >&2; exit "$rc"; }
  else
    echo "pre-push-gate: scripts/owasp-llm-scan.sh missing or not executable — skipping stage 2" >&2
  fi
fi

# ---- stage 3: frozen golden-set quality baseline ----------------------------
# Deterministic CodeJudge scoring of a frozen fixture — zero model calls. Catches
# a silent quality regression (a shifted threshold, a changed normalization)
# that unit tests on individual primitives would not surface.
if [ "${SKIP_GOLDEN_GATE:-0}" = "1" ]; then
  echo "pre-push-gate: stage 3/3 golden-gate SKIPPED (SKIP_GOLDEN_GATE=1)"
else
  if command -v go >/dev/null 2>&1; then
    echo "pre-push-gate: stage 3/3 golden-set quality baseline ..."
    go test ./internal/eval/ -run 'GoldenGate' -count=1 || rc=$?
    [ "$rc" -eq 0 ] || { echo "pre-push-gate: BLOCKED by the golden-set gate (exit $rc) — a frozen quality baseline drifted." >&2; exit "$rc"; }
  else
    echo "pre-push-gate: 'go' not on PATH — skipping stage 3" >&2
  fi
fi

echo "pre-push-gate: all stages clean — push may proceed."
exit 0
