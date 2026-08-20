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
#   4. arena-integration-gate — OPT-IN, needs the local stack (ARENA_INTEGRATION_GATE=1)
#   5. doc-citation gate      — docs citing commits that no longer exist
#                               (SKIP_CITATION_GATE=1)
#   6. absence-claim guard    — competitor claims without a current ecosystem
#                               sweep (SKIP_ABSENCE_GUARD=1). Stdlib python3,
#                               no network, sub-second.
#   7. nested-module lane     — the 20+ separate go.mod under arenas/, examples/
#                               and bench/, which the root `go test ./...` does
#                               not descend into (SKIP_NESTED_MODULES=1). This
#                               is the SLOW stage — it runs scripts/test-all.sh
#                               --quick, minutes not seconds. Skip it for a
#                               docs-only push; do not skip it after touching
#                               aisotropy/'s public surface, which is precisely
#                               what silently rots the nested modules.
#   8. capability-claims guard — re-derives the volatile claims of
#                               capabilities-and-gaps.md from the code and fails
#                               on drift (SKIP_GAP_CLAIMS=1). Registry-driven
#                               (manifests/capability-claims.yaml, tracked), so
#                               unlike the rule files it works in a clone.
#
# Reference every stage's script as "$REPO_ROOT/scripts/…", never
# "$(dirname "$0")/…": git invokes this through the .git/hooks/pre-push symlink,
# so $0 is the HOOK path and dirname resolves to .git/hooks. Stage 5 shipped with
# that bug and its first push printed "missing or not executable — skipping",
# then "all stages clean" — a gate that skips itself while reporting success is
# worse than no gate. Stage 4 carried the same latent bug, unnoticed because it
# is opt-in and had never run.
#
# Enforcement here (local hooks) rather than a CI runner is deliberate, and is
# recorded as ADR-0043: Actions minutes are billed, so NEW gates go here rather
# than growing the workflow. (This comment used to say "the GitHub workflow is
# .disabled". It is not, and no .disabled file exists — `.github/workflows/ci.yml`
# is active and runs the correctness gate. Corrected 2026-08-04. What is true is
# the narrower thing ADR-0043 actually decided: ci.yml keeps gofmt/vet/build/
# test -race/coverage, and everything added since lives in this hook.)
# The readiness audit (docs/architecture/ai-product-readiness.md) flagged that
# the OWASP suite existed and passed but was NOT enforced on any push — a
# suite nobody runs rots silently. This closes that gap.
#
# EVERY STAGE HERE DECIDES, SO EVERY STAGE MUST SAY WHY. A stage that blocks a
# push while discarding the sentence its check printed is the failure class in
# .claude/rules/absence-has-many-causes.md: `>/dev/null 2>&1` keeps the exit code
# and throws away the explanation, so "a module does not compile", "the flag was
# wrong" and "the toolchain is missing" all render as the same red. Stage 7 was
# the worst of it — it told the reader to "re-run to see which module", a
# minutes-long run whose answer the gate had already computed and dropped.
# Blocking stages therefore capture combined stdout+stderr ONCE, from the run
# that actually decided, and print it through print_cause below. Same fix, same
# shape as scripts/supply-chain-guard.sh (0806c51).
#
# Bypass everything once: git push --no-verify
set -eu

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"
cd "$REPO_ROOT" || { echo "pre-push-gate: not in a git repo" >&2; exit 2; }

rc=0

# print_cause <label> <captured-output>
# Prints on stderr, where every other diagnostic in this hook goes and where git
# shows hook output. The empty-output branch matters: "the check failed and said
# nothing" is itself a finding, and printing an empty block would read as though
# the cause had been shown.
print_cause() {
  cause_label="$1"; cause_out="$2"
  echo "   --- ${cause_label}: cause ---" >&2
  if [ -z "$cause_out" ]; then
    echo "     (the command failed and printed nothing — no diagnostic available)" >&2
  else
    printf '%s\n' "$cause_out" | sed 's/^/     /' >&2
  fi
  echo "   --- end ${cause_label} ---" >&2
}

# ---- stage 1: supply chain (govulncheck) ------------------------------------
if [ "${SKIP_SUPPLY_CHAIN_GUARD:-0}" = "1" ]; then
  echo "pre-push-gate: stage 1/9 supply-chain-guard SKIPPED (SKIP_SUPPLY_CHAIN_GUARD=1)"
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
  echo "pre-push-gate: stage 2/9 owasp-llm-scan SKIPPED (SKIP_OWASP_SCAN=1)"
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
  echo "pre-push-gate: stage 3/9 golden-gate SKIPPED (SKIP_GOLDEN_GATE=1)"
else
  if command -v go >/dev/null 2>&1; then
    echo "pre-push-gate: stage 3/9 golden-set quality baseline ..."
    go test ./internal/eval/ -run 'GoldenGate' -count=1 || rc=$?
    [ "$rc" -eq 0 ] || { echo "pre-push-gate: BLOCKED by the golden-set gate (exit $rc) — a frozen quality baseline drifted." >&2; exit "$rc"; }
  else
    echo "pre-push-gate: 'go' not on PATH — skipping stage 3" >&2
  fi
fi

# ---- stage 4: arena integration gate (opt-in, needs the local stack) --------
# Real embed server + Qdrant + rqlite, MOCKED model: proves the arena lane works
# for all nine adapters at zero model cost. Catches the four classes that reached
# a board unnoticed — lane leaks, vendor-default model ids, store-schema
# mismatches that silently return zero documents, and stdout pollution that
# breaks the one-JSON-object contract.
#
# OPT-IN because it needs services running, and a gate that fails for "the stack
# is down" would train everyone to bypass the whole pre-push hook. It exits 2
# when it cannot reach the stores, and 2 is treated here as "not applicable" —
# never as a pass, and never as a block. Set ARENA_INTEGRATION_GATE=1 to require
# it (the right setting before touching arenas/live/).
if [ "${ARENA_INTEGRATION_GATE:-0}" = "1" ]; then
  echo "pre-push-gate: stage 4/9 arena integration gate ..."
  # `|| rc=$?`, not `; rc=$?` — the same `set -e` trap already documented for
  # stages 5 and 6: a bare failing command terminates this script BEFORE the
  # assignment, so both branches below were unreachable. Measured with a stub
  # exiting 2: the push stopped with the right code and "could not run (stores
  # unreachable) — NOT a pass" never printed. This stage's output is deliberately
  # NOT captured — it streams a long integration run the operator watches live,
  # and it was never being discarded in the first place.
  rc=0
  bash "$REPO_ROOT/scripts/arena-integration-gate.sh" || rc=$?
  if [ "$rc" -eq 2 ]; then
    echo "pre-push-gate: arena integration gate could not run (stores unreachable) — NOT a pass." >&2
    exit 2
  fi
  [ "$rc" -eq 0 ] || { echo "pre-push-gate: BLOCKED by the arena integration gate (exit $rc) — an infrastructure defect would have been reported as a framework result." >&2; exit "$rc"; }
else
  echo "pre-push-gate: stage 4/9 arena integration gate SKIPPED (set ARENA_INTEGRATION_GATE=1 with the local stack up)"
fi

# ---- stage 5: doc commit-citation rot ---------------------------------------
# The docs cite commits by short hash as evidence ("landed in `abc1234`"). A
# history rewrite invalidates all of them at once and silently: the prose still
# reads like proof. Cheap (no build, no network), and it belongs on push rather
# than commit because that is when a rewrite reaches everyone else.
if [ "${SKIP_CITATION_GATE:-0}" = "1" ]; then
  echo "pre-push-gate: stage 5/9 doc-citation gate SKIPPED (SKIP_CITATION_GATE=1)"
else
  if [ -x "$REPO_ROOT/scripts/check-doc-commit-citations.sh" ]; then
    echo "pre-push-gate: stage 5/9 doc commit-citation check ..."
    # Same `set -eu` fix as stages 6 and 7 (2026-08-04): with `; rc=$?` this
    # stage blocked the push correctly but printed NOTHING about why — the
    # diagnostic below was unreachable because the script had already exited.
    rc=0
    # Captured once, from the run that decided. The previous shape re-ran the
    # script to show the finding it had just computed: double work, and the text
    # shown was a SECOND run's output, not the evidence the block was based on.
    out="$(bash "$REPO_ROOT/scripts/check-doc-commit-citations.sh" 2>&1)" || rc=$?
    if [ "$rc" -eq 2 ]; then
      echo "pre-push-gate: doc-citation gate could not evaluate — NOT a pass." >&2
      print_cause "doc-citation gate (could not evaluate)" "$out"
      exit 2
    fi
    [ "$rc" -eq 0 ] || {
      echo "pre-push-gate: BLOCKED by the doc-citation gate (exit $rc) — a doc cites a commit that no longer exists." >&2
      print_cause "doc-citation gate" "$out"
      exit "$rc"
    }
  else
    echo "pre-push-gate: scripts/check-doc-commit-citations.sh missing or not executable — skipping stage 5" >&2
  fi
fi

# ---- stage 6: competitor-absence claims -------------------------------------
# Every claim that somebody else's product LACKS something, or that only we do
# something, or that a named competitor leaked, needs a dated ecosystem sweep.
# The guard predates this stage by weeks and was wired to nothing: on 2026-08-04
# the repo's own front page carried both a blanket "no competing Go agent
# framework builds it" and a claim that a named competitor "leaked across
# tenants", and the guard reported clean — its patterns did not match either
# sentence, and nothing ran it anyway. Patterns widened, ledger re-derived, and
# now it runs on every push. Stdlib python3, no network, sub-second.
if [ "${SKIP_ABSENCE_GUARD:-0}" = "1" ]; then
  echo "pre-push-gate: stage 6/9 absence-claim guard SKIPPED (SKIP_ABSENCE_GUARD=1)"
else
  if [ -f "$REPO_ROOT/scripts/absence-claim-guard.py" ]; then
    echo "pre-push-gate: stage 6/9 competitor-absence claim guard ..."
    # `|| rc=$?`, not `; rc=$?`: this script runs under `set -eu`, so a bare
    # failing command terminates it BEFORE the assignment — the push is blocked
    # with the correct exit code but WITHOUT the diagnostic below ever printing.
    # Measured, not theorised: the first version of this stage blocked silently.
    rc=0
    out="$(python3 "$REPO_ROOT/scripts/absence-claim-guard.py" 2>&1)" || rc=$?
    if [ "$rc" -eq 2 ]; then
      echo "pre-push-gate: absence-claim guard could not evaluate — NOT a pass." >&2
      print_cause "absence-claim guard (could not evaluate)" "$out"
      exit 2
    fi
    [ "$rc" -eq 0 ] || {
      echo "pre-push-gate: BLOCKED by the absence-claim guard (exit $rc)." >&2
      print_cause "absence-claim guard" "$out"
      exit "$rc"
    }
  else
    echo "pre-push-gate: scripts/absence-claim-guard.py missing — skipping stage 6" >&2
  fi
fi

# ---- stage 7: nested modules (the slow one) ---------------------------------
# `go test ./...` does not descend into a nested go.mod, so the root suite can be
# green while an arenas/ or examples/ module does not compile. Twice paid for
# here: a stale go.mod in arenas/live/aisotropy silently invalidated a 58-run
# diagnostic (the runs had been measuring a build that no longer compiled), and a
# grpc CVE bump broke 13 nested modules against a green root suite.
#
# Calls the maintained script instead of re-implementing module discovery, so the
# logic stays in one place AND the script itself gets exercised rather than
# rotting — the failure mode that produced every other gap in this file.
# test-all.sh reports SKIPPED separately from PASS and names the skips in its
# verdict, so a layer that could not run never reads as green.
if [ "${SKIP_NESTED_MODULES:-0}" = "1" ]; then
  echo "pre-push-gate: stage 7/9 nested-module lane SKIPPED (SKIP_NESTED_MODULES=1)"
else
  if [ -x "$REPO_ROOT/scripts/test-all.sh" ]; then
    echo "pre-push-gate: stage 7/9 nested modules (root + arenas/ + examples/ + bench/) — slow ..."
    rc=0
    # THE STAGE THIS FILE'S CAUSE-PRINTING CONTRACT WAS WRITTEN FOR. test-all.sh
    # names the failing module and its verdict; `>/dev/null 2>&1` deleted both,
    # and the gate then asked the developer to spend MINUTES reproducing an
    # answer it had already had. Captured (not streamed) so a passing run stays
    # silent, and printed only when this lane blocks.
    #
    # exit 2 is test-all.sh's usage error, not a failing module — no special
    # branch decides that here, because the cause block now says which it is
    # instead of leaving the reader to infer it from a bare number.
    out="$(bash "$REPO_ROOT/scripts/test-all.sh" --quick 2>&1)" || rc=$?
    [ "$rc" -eq 0 ] || {
      echo "pre-push-gate: BLOCKED by the nested-module lane (exit $rc)." >&2
      print_cause "test-all.sh --quick" "$out"
      exit "$rc"
    }
  else
    echo "pre-push-gate: scripts/test-all.sh missing or not executable — skipping stage 7" >&2
  fi
fi

# ---- stage 8: capability-claims drift ---------------------------------------
# Re-derives the volatile, mechanically checkable claims of
# docs/architecture/capabilities-and-gaps.md FROM THE CODE and fails when the
# document disagrees. The registry (manifests/capability-claims.yaml) is tracked,
# so this works in any clone — which matters, because the rule files under
# .claude/rules/ and .agent-tooling/rules/ are NOT tracked and do not travel.
#
# Like stages 6 and 7 before today, this guard already existed and nothing ran
# it. It was written after two build dispatches were burned on work that already
# existed (the runtime config file and the Helm chart, both shipped BEFORE the
# doc that called them gaps), and a re-verification found 5 of 12 dispatched
# claims wrong or materially overstated. A doc that dispatches work and rots
# silently is the failure; a guard nobody runs is the same failure one level up.
#
# Cheap: no build, no network, no model. Exit 2 = could not evaluate, which is
# not a pass.
if [ "${SKIP_GAP_CLAIMS:-0}" = "1" ]; then
  echo "pre-push-gate: stage 8/9 capability-claims guard SKIPPED (SKIP_GAP_CLAIMS=1)"
else
  if [ -f "$REPO_ROOT/scripts/gap-claims-guard.sh" ]; then
    echo "pre-push-gate: stage 8/9 capability-claims drift ..."
    rc=0
    out="$(bash "$REPO_ROOT/scripts/gap-claims-guard.sh" 2>&1)" || rc=$?
    if [ "$rc" -eq 2 ]; then
      echo "pre-push-gate: capability-claims guard could not evaluate — NOT a pass." >&2
      print_cause "capability-claims guard (could not evaluate)" "$out"
      exit 2
    fi
    [ "$rc" -eq 0 ] || {
      echo "pre-push-gate: BLOCKED by the capability-claims guard (exit $rc) — a doc claim no longer matches the code." >&2
      print_cause "capability-claims guard" "$out"
      exit "$rc"
    }
  else
    echo "pre-push-gate: scripts/gap-claims-guard.sh missing — skipping stage 8" >&2
  fi
fi

# ---- stage 9: co-authorship trailers ----------------------------------------
# Both CLAUDE.md files say "NEVER add Co-Authored-By trailer to commit messages".
# Until 2026-08-19 that rule was enforced by nothing but prose in a file an agent
# may or may not have loaded, so a single slip would have sat in the history
# unnoticed — and history is the one thing a later commit cannot fix.
#
# Cheap: it reads commit objects only, no build, no network, sub-second on ~600
# commits. Cheap enough that there is no reason to run a push without it.
if [ "${SKIP_COAUTHOR_GUARD:-0}" = "1" ]; then
  echo "pre-push-gate: stage 9/9 co-authorship guard SKIPPED (SKIP_COAUTHOR_GUARD=1)"
else
  if [ -x "$REPO_ROOT/scripts/coauthor-trailer-guard.sh" ]; then
    echo "pre-push-gate: stage 9/9 co-authorship trailers ..."
    rc=0
    out="$(bash "$REPO_ROOT/scripts/coauthor-trailer-guard.sh" 2>&1)" || rc=$?
    if [ "$rc" -eq 2 ]; then
      echo "pre-push-gate: co-authorship guard could not evaluate — NOT a pass." >&2
      print_cause "co-authorship guard (could not evaluate)" "$out"
      exit 2
    fi
    [ "$rc" -eq 0 ] || {
      echo "pre-push-gate: BLOCKED — a commit carries a co-authorship trailer." >&2
      print_cause "co-authorship guard" "$out"
      exit "$rc"
    }
  else
    echo "pre-push-gate: scripts/coauthor-trailer-guard.sh missing — skipping stage 9" >&2
  fi
fi

echo "pre-push-gate: all stages clean — push may proceed."
exit 0
