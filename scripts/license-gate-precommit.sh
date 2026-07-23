#!/usr/bin/env bash
# license-gate-precommit.sh — conditional pre-commit gate over scripts/license-audit.sh.
#
# WHY: the constitutional license gate (ADR-0067 companion,
# .claude/rules/cos/license-policy.md) should fail a NEW dependency or compose
# image AT COMMIT TIME, not at the next manual audit — but the full audit costs
# ~20s of wall clock, which is hostile on every commit. So this hook is
# CONDITIONAL: it runs only when the STAGED changes touch a dependency/image
# manifest; every other commit pays ~0ms.
#
# SCOPE: `license-audit.sh --root-only` (shipped-runtime deps + ALL compose
# images), matching ci.yml's "only the root module is gated" philosophy. Nested
# demo modules (arenas/*, examples/*, bench/*, external/*) are covered by the
# manual full sweep (`scripts/license-audit.sh`), not by the commit gate.
#
# BYPASS (loud, deliberate — a skip is not a pass):
#   SKIP_LICENSE_AUDIT=1 git commit …     or     git commit --no-verify
# INSTALL: scripts/install-hooks.sh  (symlinks .git/hooks/pre-commit here)
# EXIT: 0 = pass or not triggered; 1 = audit findings (commit blocked); 2 = env error
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "license-gate: not in a git repo" >&2; exit 2; }

if [ "${SKIP_LICENSE_AUDIT:-0}" = "1" ]; then
  echo "license-gate: SKIPPED (SKIP_LICENSE_AUDIT=1) — commit not audited."
  exit 0
fi

# Manifest surfaces whose staged change means "a dependency or service image may
# have changed". Anything else commits without paying for the audit.
TRIGGER='(^|/)go\.(mod|sum)$|(^|/)docker-compose[^/]*\.ya?ml$|(^|/)Dockerfile[^/]*$|(^|/)Cargo\.(toml|lock)$|(^|/)requirements[^/]*\.txt$|(^|/)package\.json$|(^|/)pyproject\.toml$'

staged="$(git diff --cached --name-only --diff-filter=ACMR)"
hits="$(printf '%s\n' "$staged" | grep -E "$TRIGGER" || true)"

if [ -z "$hits" ]; then
  exit 0 # nothing dependency-shaped staged — free pass, zero cost
fi

echo "license-gate: staged manifest change(s) detected:"
printf '%s\n' "$hits" | sed 's/^/  - /'
echo "license-gate: running scripts/license-audit.sh --root-only ..."
bash scripts/license-audit.sh --root-only
rc=$?
if [ "$rc" -eq 0 ]; then
  exit 0
fi
echo
echo "license-gate: BLOCKED — the license audit reported findings (see above)."
echo "  Fix the dependency/image (or extend the reviewed allowlist WITH evidence),"
echo "  or bypass this one commit with: SKIP_LICENSE_AUDIT=1 git commit …"
exit "$rc"
