#!/usr/bin/env bash
# supply-chain-guard.sh — local (pre-push) supply-chain gate for the root module.
#
# WHY THIS EXISTS: the supply-chain gates run LOCALLY, not as extra GitHub Actions.
# The existing .github/workflows/ci.yml already burns Actions minutes on the
# correctness gate (gofmt/vet/build/test -race/coverage); adding govulncheck + SBOM
# + dependency scanning as more Action steps would multiply that paid cost. Instead
# the same checks run on the developer's machine BEFORE a push leaves it (installed
# as a pre-push hook by scripts/install-hooks.sh). This gate FAILS the push on a
# known-exploitable CVE that is actually reachable from the root module's code — the
# ops-hardening posture of ADR-0025 (docs/decisions/0025-security-hardening-authn-ops.md),
# extended to the dependency supply chain.
#
# CHECKS:
#   1. govulncheck ./...            — BLOCKING. Reuses the exact
#      `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` invocation from
#      cmd/x/analysis.go (govulncheckCmd). govulncheck exits non-zero ONLY when a
#      vulnerability is reachable from code this module actually calls — so a plain
#      advisory in an unused transitive dep does not block the push (low false
#      positives by design). A pre-installed `govulncheck` on PATH is used if
#      present; otherwise `go run …@latest` fetches and caches it on first use.
#   2. dependency staleness         — ADVISORY. `go list -m -u all` reports modules
#      with a newer version available. Printed for visibility; never blocks (a push
#      must not fail merely because an upgrade exists). This is the local stand-in
#      for Dependabot, which is GitHub-hosted and intentionally not enabled here.
#   3. build-context secret scan    — ADVISORY. Flags tracked files matching the
#      patterns .dockerignore excludes (.env, *.pem, *.key, *secret*), so a secret
#      is noticed before a COPY . . in any builder stage could bake it into a layer.
#   4. example modules build        — BLOCKING. `go build -tags keybroker ./...` +
#      `go vet` in each examples/* module. Catches public-API rot: a break in the
#      aisotropy/ façade that a copied example would hit — the root `go test ./...`
#      never descends into the separate example modules, so nothing else catches it.
#
# NOTE ON MODULES: only the ROOT module is vuln-scanned (internal/…, aisotropy, cmd/…).
# BUILD gating is wider than vuln gating: examples/* (stage 4) AND arenas/*, bench/*
# (stage 5) are separate modules that ARE build-gated, because `go build ./...` at the
# root does not descend into nested modules — the root can be green while an adapter
# is broken. examples/* are additionally build-gated
# (check 4), so a public-surface break that would hit a copied demo fails the push.
#
# USAGE:   scripts/supply-chain-guard.sh        # run all checks from anywhere in the repo
#          Installed as .git/hooks/pre-push by scripts/install-hooks.sh.
# BYPASS:  SKIP_SUPPLY_CHAIN_GUARD=1 git push …   (emergency escape; or git push --no-verify)
# EXIT:    0 = clean (advisories may still have printed)
#          1 = BLOCKED (reachable known-exploitable CVE, or an example module failed to build)
#          2 = usage / environment error
set -uo pipefail

if [ "${SKIP_SUPPLY_CHAIN_GUARD:-0}" = "1" ]; then
  echo "supply-chain-guard: SKIP_SUPPLY_CHAIN_GUARD=1 set — skipping (unsafe; use sparingly)." >&2
  exit 0
fi

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || { echo "not in a git repo" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "supply-chain-guard: 'go' not found on PATH" >&2; exit 2; }

echo "==================================================================="
echo " supply-chain-guard: local pre-push gate (root module)"
echo "==================================================================="

BLOCKED=0

# --- 1. govulncheck (BLOCKING) ---
echo
echo "1) govulncheck ./...  (known-exploitable CVEs reachable from real code)"
if command -v govulncheck >/dev/null 2>&1; then
  govulncheck ./...
else
  echo "   (govulncheck not on PATH — running via 'go run …@latest', matching cmd/x/analysis.go)"
  go run golang.org/x/vuln/cmd/govulncheck@latest ./...
fi
vuln_rc=$?
if [ "$vuln_rc" -ne 0 ]; then
  echo "   govulncheck FAILED (exit $vuln_rc): a reachable known-exploitable vulnerability was found."
  BLOCKED=1
else
  echo "   govulncheck: no reachable known-exploitable vulnerabilities."
fi

# --- 2. dependency staleness (ADVISORY) ---
echo
echo "2) dependency staleness  (go list -m -u all — advisory, non-blocking)"
stale="$(go list -m -u all 2>/dev/null | grep -E '\[[^]]+\]' || true)"
if [ -n "$stale" ]; then
  n_stale="$(printf '%s\n' "$stale" | grep -c . || true)"
  echo "   $n_stale module(s) have a newer version available:"
  printf '%s\n' "$stale" | sed 's/^/     /'
  echo "   (advisory only — review and bump deliberately; this does not block the push.)"
else
  echo "   all modules are at their latest known version."
fi

# --- 3. build-context secret scan (ADVISORY) ---
echo
echo "3) build-context secret scan  (patterns .dockerignore excludes — advisory)"
secrets="$(git ls-files -- '.env' '.env.*' '*.pem' '*.key' '*secret*' 2>/dev/null || true)"
if [ -n "$secrets" ]; then
  echo "   WARNING: tracked file(s) match a secret pattern — confirm these are NOT real secrets:"
  printf '%s\n' "$secrets" | sed 's/^/     /'
  echo "   (.dockerignore keeps these out of the build context; still verify they belong in git.)"
else
  echo "   no tracked files match .env / *.pem / *.key / *secret*."
fi

# --- 4. example modules build (BLOCKING) ---
echo
echo "4) example modules build  (examples/* — catch public-surface rot in the demos)"
ex_fail=""
for gm in examples/*/go.mod; do
  [ -f "$gm" ] || continue
  ex_dir="$(dirname "$gm")"
  # -tags keybroker so the RunTask/RunTasks demos (task-agent, task-fanout) compile too.
  if ! ( cd "$ex_dir" && go build -tags keybroker ./... && go vet -tags keybroker ./... ) >/dev/null 2>&1; then
    ex_fail="$ex_fail ${ex_dir}"
  fi
done
if [ -n "$ex_fail" ]; then
  echo "   BUILD FAILED for example module(s):$ex_fail"
  echo "   A public-surface change in aisotropy/ broke a demo an adopter would copy."
  echo "   Fix the example (or 'go mod tidy' in it if a dependency drifted)."
  BLOCKED=1
else
  echo "   all example modules build + vet clean."
fi

echo
echo "5) arena modules build  (arenas/*, bench/* — catch the SAME rot in the benchmark adapters)"
# WHY THIS STAGE EXISTS. Stage 4 has always covered examples/*, and the note at
# the top of this file used to say "arenas/* stay out of scope". That exclusion
# cost a full diagnostic run: an interface change in the root module
# (semantic.Store gaining a method) left arenas/live/aisotropy/go.mod stale.
# `go build ./...` from the repo root does NOT descend into nested modules, so
# the root stayed green, every gate passed, the push went through — and the
# breakage only surfaced when a real board run spent its startup discovering
# that the adapter binary could not compile ("go: updates to go.mod needed").
# Every one of its 58 runs errored on a build failure, measuring nothing.
#
# An arena adapter is not a demo: it is the thing that produces the published
# benchmark. A broken one wastes a multi-hour, billable run and, worse, can
# render as a framework RESULT rather than as our own build error.
arena_fail=""
for gm in arenas/*/go.mod arenas/live/*/go.mod bench/*/go.mod bench/*/*/go.mod; do
  [ -f "$gm" ] || continue
  ar_dir="$(dirname "$gm")"
  # Vendored upstreams (external/) are excluded: they are not ours to fix.
  case "$ar_dir" in external/*) continue ;; esac
  if ! ( cd "$ar_dir" && go build ./... ) >/dev/null 2>&1; then
    arena_fail="$arena_fail ${ar_dir}"
  fi
done
if [ -n "$arena_fail" ]; then
  echo "   BUILD FAILED for arena/bench module(s):$arena_fail"
  echo "   A nested module drifted from the root. This is exactly the failure that"
  echo "   makes a board run burn hours and report nothing — fix it before pushing."
  echo "   Usually: 'go mod tidy' in the module, or update it for a root API change."
  BLOCKED=1
else
  echo "   all arena/bench modules build."
fi

# --- VERDICT ---
echo
echo "==================================================================="
if [ "$BLOCKED" -eq 1 ]; then
  echo " VERDICT: BLOCKED — fix the failing check(s) above before pushing."
  echo " (bump the affected module / fix the example, or bypass once with SKIP_SUPPLY_CHAIN_GUARD=1 / --no-verify.)"
  echo "==================================================================="
  exit 1
fi
echo " VERDICT: clean — no reachable known-exploitable CVEs. Push may proceed."
echo "==================================================================="
exit 0
