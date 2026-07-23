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
# ALSO GATES (2026-07-23): oversized artifacts. A compiled binary slipped into
# history as examples/graphbot/graphbot (51MB of permanent pack bloat on every
# clone; purged from history the same day). Any staged blob >10MB now blocks the
# commit — a source file that big is almost certainly a build artifact; if it is
# genuinely intentional, bypass once with SKIP_SIZE_GATE=1.
#
# BYPASS (loud, deliberate — a skip is not a pass):
#   SKIP_LICENSE_AUDIT=1 git commit …  (license)   SKIP_SIZE_GATE=1 git commit …  (size)
#   git commit --no-verify skips both.
# INSTALL: scripts/install-hooks.sh  (symlinks .git/hooks/pre-commit here)
# EXIT: 0 = pass or not triggered; 1 = findings (commit blocked); 2 = env error
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "license-gate: not in a git repo" >&2; exit 2; }

staged="$(git diff --cached --name-only --diff-filter=ACMR)"

# --- oversized-artifact gate: any staged blob >10MB blocks (build-artifact tell) ---
MAX_BLOB_MB=10
if [ "${SKIP_SIZE_GATE:-0}" != "1" ] && [ -n "$staged" ]; then
  oversize=""
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    sz="$(git cat-file -s ":$f" 2>/dev/null || echo 0)"
    if [ "$sz" -gt $((MAX_BLOB_MB * 1024 * 1024)) ]; then
      oversize="${oversize}  - $f ($((sz / 1024 / 1024))MB)
"
    fi
  done <<STAGED_EOF
$staged
STAGED_EOF
  if [ -n "$oversize" ]; then
    echo "license-gate: BLOCKED — staged blob(s) exceed ${MAX_BLOB_MB}MB (build artifact?):"
    printf '%s' "$oversize"
    echo "  If intentional (it almost never is), bypass once: SKIP_SIZE_GATE=1 git commit …"
    exit 1
  fi
fi

if [ "${SKIP_LICENSE_AUDIT:-0}" = "1" ]; then
  echo "license-gate: license audit SKIPPED (SKIP_LICENSE_AUDIT=1) — commit not audited (size gate still ran)."
  exit 0
fi

# Manifest surfaces whose staged change means "a dependency or service image may
# have changed". Anything else commits without paying for the audit.
TRIGGER='(^|/)go\.(mod|sum)$|(^|/)docker-compose[^/]*\.ya?ml$|(^|/)Dockerfile[^/]*$|(^|/)Cargo\.(toml|lock)$|(^|/)requirements[^/]*\.txt$|(^|/)package\.json$|(^|/)pyproject\.toml$'

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
