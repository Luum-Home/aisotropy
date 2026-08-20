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
#   5. arena/bench modules build     — BLOCKING. Same rot check for arenas/*, bench/*.
#      A module that ships a build.sh is first asked to regenerate its generated
#      artifacts (`build.sh --prepare-only`), because some are .gitignored and a
#      fresh clone therefore cannot compile the module at all — see the stage.
#   6. license audit (--root-only)   — BLOCKING. Runs scripts/license-audit.sh over the
#      root deps + compose-image allowlist: the local twin of the CI license step, so a
#      non-permissive dep/unreviewed image can't reach origin even if CI is never watched.
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

# print_cause <label> <captured-output>
# WHY THIS EXISTS. Every stage below DECIDES — it can block a push. A decision
# whose explanation was thrown away is the failure mode named in
# .claude/rules/absence-has-many-causes.md: `>/dev/null 2>&1` keeps the exit code
# and discards the sentence, so "the code is broken", "a generated file is
# missing" and "the toolchain is unavailable" all render as the same red. They
# are three different facts and need three different outputs. Blocking stages
# therefore capture combined stdout+stderr and print it through here.
print_cause() {
  cause_label="$1"; cause_out="$2"
  echo "   --- ${cause_label}: cause ---"
  if [ -z "$cause_out" ]; then
    echo "     (the command failed and printed nothing — no diagnostic available)"
  else
    printf '%s\n' "$cause_out" | sed 's/^/     /'
  fi
  echo "   --- end ${cause_label} ---"
}

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
# stderr is captured, not discarded: if `go list -m -u all` cannot run (no
# network, broken module graph) its output is empty, and reporting that as "all
# modules are at their latest version" would be a false claim built out of a
# silenced error. Could-not-check and nothing-to-report are different answers.
stale_raw="$(go list -m -u all 2>&1)"
list_rc=$?
stale=""
[ "$list_rc" -eq 0 ] && stale="$(printf '%s\n' "$stale_raw" | grep -E '\[[^]]+\]' || true)"
if [ "$list_rc" -ne 0 ]; then
  echo "   could NOT evaluate staleness ('go list -m -u all' exited $list_rc)."
  echo "   This is not 'everything is up to date' — the check did not run:"
  printf '%s\n' "$stale_raw" | sed 's/^/     /'
elif [ -n "$stale" ]; then
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
  if ! ex_out="$( ( cd "$ex_dir" && go build -tags keybroker ./... && go vet -tags keybroker ./... ) 2>&1 )"; then
    ex_fail="$ex_fail ${ex_dir}"
    print_cause "${ex_dir} (go build + go vet)" "$ex_out"
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

  # GENERATED ARTIFACTS COME FIRST. arenas/live/aisotropy embeds calc.wasm via
  # go:embed, and calc.wasm is .gitignored (it is a 3.4 MB compiled artifact —
  # generated code does not belong in the tree). A fresh clone therefore has no
  # calc.wasm and `go build ./...` fails on the embed pattern before it can
  # check a single thing this stage cares about. The gate used to pass only on
  # machines that happened to have the artifact lying around from an earlier
  # run — it was green by accident of local state, not by evidence.
  #
  # The module's own build.sh already knows how to produce those artifacts AND
  # when to skip the work (it compares testdata/wasmguest/*.go against
  # calc.wasm's mtime), so the gate reuses it via --prepare-only rather than
  # re-implementing the freshness rule in a second place that could drift.
  # --prepare-only stops after the generated artifacts: no adapter binary is
  # built, since the `go build ./...` below is the actual check.
  if [ -f "$ar_dir/build.sh" ]; then
    if ! prep_out="$( bash "$ar_dir/build.sh" --prepare-only 2>&1 )"; then
      arena_fail="$arena_fail ${ar_dir}(generated-artifact prepare)"
      print_cause "${ar_dir} (build.sh --prepare-only)" "$prep_out"
      continue
    fi
  fi

  if ! ar_out="$( ( cd "$ar_dir" && go build ./... ) 2>&1 )"; then
    arena_fail="$arena_fail ${ar_dir}"
    print_cause "${ar_dir} (go build ./...)" "$ar_out"
  fi
done
if [ -n "$arena_fail" ]; then
  echo "   BUILD FAILED for arena/bench module(s):$arena_fail"
  echo "   A broken adapter is exactly the failure that makes a board run burn hours"
  echo "   and report nothing — fix it before pushing. The cause block above says which"
  echo "   kind this is; do not guess at the remedy from the module name alone:"
  echo "     - '(go build ./...)'        the module's own code or go.mod drifted from the"
  echo "                                 root — usually 'go mod tidy', or update it for a"
  echo "                                 root API change."
  echo "     - '(build.sh --prepare-only)'  the module's GENERATED artifacts could not be"
  echo "                                 produced; the module's source may be fine. Fix the"
  echo "                                 generator input the cause block names."
  BLOCKED=1
else
  echo "   all arena/bench modules build."
fi

# --- 6. license audit (BLOCKING) ---
# WHY AT PRE-PUSH. The license gate lives in three places: the pre-commit hook
# (conditional, only when a manifest is staged), the GitHub Actions CI step, and
# here. This stage is the one that does NOT depend on observing a remote runner:
# a commit made with --no-verify, by a concurrent session without the hook
# installed, or before the gate existed can still carry a non-permissive dep or
# an unreviewed compose image to the push. Running license-audit --root-only here
# (~1s, same scope as the CI step) makes the guarantee local: an unreviewed
# license cannot leave this machine even if CI is never looked at.
echo
echo "6) license audit  (root-module deps + compose-image allowlist — the CI step, run locally)"
lic_out="$(bash "$(git rev-parse --show-toplevel)/scripts/license-audit.sh" --root-only 2>&1)"
lic_rc=$?
if [ "$lic_rc" -eq 0 ]; then
  echo "   license audit clean (permissive deps + allowlisted images)."
elif [ "$lic_rc" -eq 2 ]; then
  echo "   license audit could NOT run (env error, exit 2) — not blocking on tooling failure,"
  echo "   but note this stage did not evaluate anything:"
  print_cause "license-audit (env error)" "$lic_out"
else
  echo "   LICENSE AUDIT FAILED (exit $lic_rc): a root dep resolves to a non-permissive/absent"
  echo "   license, or a compose image is not on the reviewed allowlist. The finding itself:"
  print_cause "license-audit" "$lic_out"
  BLOCKED=1
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
echo " VERDICT: clean — no reachable CVEs, all modules build, licenses permissive. Push may proceed."
echo "==================================================================="
exit 0
