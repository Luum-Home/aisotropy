#!/usr/bin/env bash
# scripts/license-audit.sh — reproducible evidence for the repo's license posture.
#
# WHY THIS EXISTS: the 2026-07-23 license audit (docs/decisions/0067-grafana-agpl-exception-
# observability-stack.md) was a one-off manual pass. Per the "evidencia ejecutable" norm, every
# audited claim with a number needs the command that produced it — this script IS that command,
# reproducible any time the dependency graph or the compose stack changes.
#
# WHAT IT CHECKS:
#   (a) Go dependency licenses — for every REAL go.mod in the repo (nested modules included:
#       arenas/*, bench/*, examples/*, external/gollem, arenas/live/*), extract DIRECT
#       (non-indirect) requires, resolve each to $(go env GOMODCACHE), and:
#         - look for a LICENSE/COPYING file (case-insensitive, common name variants)
#         - grep its text for prohibited copyleft/source-available license strings
#         - report MISSING_FROM_CACHE (module not downloaded — never auto-downloaded by this
#           script) or UNKNOWN (module present, no LICENSE-like file found)
#       A `replace` directive pointing at a local filesystem path (first-party code, e.g.
#       `replace github.com/Luum-Home/aisotropy => ../..`) is treated as first-party and skipped.
#   (b) Repo sweep — informative `git grep` count of AGPL/SSPL/BUSL/Elastic/Commons-Clause/
#       copyleft mentions (context, not itself a finding — a doc CITING a license is expected),
#       plus a check that every `image:` in every docker-compose*.yml is on the approved allowlist
#       (an unlisted image is a finding: a new service license was never reviewed).
#
# KNOWN, ACCEPTED EXCEPTIONS (do not re-litigate — see the cited ADR before changing these):
#   - github.com/fugue-labs/gollem-langsmith@v0.0.2 has no LICENSE file upstream (verified
#     2026-07-23). It is NOT an aisotropy dependency (absent from the root go.mod/go.sum; pulled
#     only by external/gollem's own CLI tooling) — low risk, upstream's responsibility. Whitelisted
#     below so it does not fail the gate. See docs/decisions/0067-grafana-agpl-exception-
#     observability-stack.md "Adjacent 2026-07-23 license-audit verdicts".
#   - grafana/grafana image (AGPL-3.0) is an approved exception under
#     docs/decisions/0067-grafana-agpl-exception-observability-stack.md — allowlisted, not blocked.
#
# USAGE:  scripts/license-audit.sh [--root-only]
#         --root-only: audit only the ROOT go.mod (the shipped runtime) + the full
#         compose-image sweep. This is the COMMIT-GATE / CI scope (matches ci.yml's
#         "only the root module is gated" philosophy); the no-flag full sweep over
#         all 24 nested modules stays the manual audit-cadence tool.
#         SKIP_LICENSE_AUDIT=1 skips entirely (loud bypass — a skip is NOT a pass).
# EXIT:   0 = clean, no findings
#         1 = findings (review the FINDINGS list printed at the end)
#         2 = environment error (not a git repo, `go` missing, GOMODCACHE unreadable)
#
# Read-only. Deterministic given the current go.sum + module cache + compose files. Does NOT run
# `go mod download` — a module missing from the cache is reported (MISSING_FROM_CACHE), not fetched.
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || { echo "license-audit: not in a git repo" >&2; exit 2; }

if [ "${SKIP_LICENSE_AUDIT:-0}" = "1" ]; then
  echo "license-audit: SKIPPED (SKIP_LICENSE_AUDIT=1) — audit NOT performed; a skip is a bypass, not a pass."
  exit 0
fi

ROOT_ONLY=0
case "${1:-}" in
  --root-only) ROOT_ONLY=1 ;;
  "") : ;;
  *) echo "license-audit: unknown argument '$1' (only --root-only is supported)" >&2; exit 2 ;;
esac

command -v go >/dev/null 2>&1 || { echo "license-audit: 'go' not found on PATH" >&2; exit 2; }

GOMODCACHE="$(go env GOMODCACHE 2>/dev/null)"
if [ -z "$GOMODCACHE" ] || [ ! -d "$GOMODCACHE" ]; then
  echo "license-audit: GOMODCACHE ('$GOMODCACHE') not readable" >&2
  exit 2
fi

echo "==================================================================="
echo " license-audit: Go deps + repo sweep (read-only, deterministic)"
echo " GOMODCACHE=$GOMODCACHE"
echo "==================================================================="

FINDINGS=0
declare -a FINDING_LINES=()

record_finding() {
  FINDINGS=$((FINDINGS + 1))
  FINDING_LINES+=("$1")
}

# --- known-accepted exceptions (module@version -> reason) ---
# A module here is reported as WHITELISTED, not counted as a finding, no matter its cache status.
# Each entry cites how it was verified — re-verify (not silently trust) if the version bumps.
is_whitelisted_dep() {
  case "$1" in
    "github.com/fugue-labs/gollem-langsmith@v0.0.2")
      echo "no LICENSE upstream (verified 2026-07-23); not an aisotropy dep — see ADR-0067"
      return 0
      ;;
    "github.com/cloudwego/eino-ext/components/model/openai@v0.1.13")
      echo "monorepo nested-module packaging gap: the eino-ext repo root IS Apache-2.0 (verified 2026-07-23 via 'gh api repos/cloudwego/eino-ext/license' -> spdx_id=Apache-2.0), but Go's module zip for this per-directory nested module does not include the repo-root LICENSE file — a scan artifact, not a real license gap. Re-verify if the version bumps: gh api repos/cloudwego/eino-ext/license"
      return 0
      ;;
  esac
  return 1
}

# Prohibited license text (case-insensitive). Matches the repo's blocked list:
# GNU AFFERO (AGPL) | GNU GENERAL PUBLIC (GPL — deliberately NOT LGPL: "LESSER" sits between the
# two words in the LGPL header, so this literal substring does not match it) | Server Side Public
# (SSPL) | Business Source (BUSL) | Elastic License | Commons Clause | Functional Source (FSL).
PROHIBITED_RE='GNU AFFERO|GNU GENERAL PUBLIC|Server Side Public|Business Source|Elastic License|Commons Clause|Functional Source'

# --- escape a Go module path per the module-cache casing convention (each uppercase letter -> "!" + lowercase) ---
escape_modpath() {
  local s="$1" out="" c i len
  len=${#s}
  for (( i = 0; i < len; i++ )); do
    c="${s:i:1}"
    if [[ "$c" =~ [A-Z] ]]; then
      out+="!${c,,}"
    else
      out+="$c"
    fi
  done
  printf '%s' "$out"
}

# --- extract DIRECT (non-"// indirect") requires from a go.mod, one "path version" per line ---
extract_direct_deps() {
  awk '
    /^require[[:space:]]*\(/ { inblock = 1; next }
    inblock && /^\)/ { inblock = 0; next }
    inblock {
      if ($0 ~ /\/\/[[:space:]]*indirect/) next
      line = $0; sub(/\/\/.*$/, "", line); gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
      if (line != "") print line
      next
    }
    /^require[[:space:]]+[^\(]/ {
      if ($0 ~ /\/\/[[:space:]]*indirect/) next
      line = $0; sub(/^require[[:space:]]+/, "", line); sub(/\/\/.*$/, "", line)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
      if (line != "") print line
    }
  ' "$1"
}

# --- extract local-filesystem "replace" targets from a go.mod: "modpath" -> 1 if replaced locally ---
extract_local_replaces() {
  awk '
    /^replace[[:space:]]/ {
      line = $0; sub(/^replace[[:space:]]+/, "", line)
      split(line, parts, "=>")
      if (length(parts) < 2) next
      src = parts[1]; dst = parts[2]
      gsub(/[[:space:]]+$/, "", src); gsub(/^[[:space:]]+/, "", dst)
      # strip a version pin off the source ("path v1.2.3" -> "path")
      split(src, sp, " "); modpath = sp[1]
      gsub(/[[:space:]].*$/, "", dst)
      if (dst ~ /^(\.|\/)/) print modpath
    }
  ' "$1"
}

echo
echo "--- (a) Go direct-dependency license sweep ---"

if [ "$ROOT_ONLY" = "1" ]; then
  # Commit-gate / CI scope: the shipped runtime only. Nested demo modules
  # (arenas/*, examples/*, bench/*, external/*) are audited by the manual
  # full sweep, mirroring ci.yml's "only the root module is gated" note.
  GO_MOD_DIRS="go.mod"
else
  GO_MOD_DIRS="$(find . -name go.mod \
    -not -path '*/node_modules/*' \
    -not -path '*/.claude/worktrees/*' \
    -not -path '*/.agent-tooling/checkpoints/*' \
    -not -path '*/.venv/*' \
    | sed 's#^\./##' | sort)"
fi

n_modules=0
n_deps_total=0
n_safe=0
n_local=0
n_whitelisted=0

while IFS= read -r gomod; do
  [ -n "$gomod" ] || continue
  n_modules=$((n_modules + 1))
  modroot="$(dirname "$gomod")"

  local_replaces="$(extract_local_replaces "$gomod")"

  while IFS= read -r reqline; do
    [ -n "$reqline" ] || continue
    n_deps_total=$((n_deps_total + 1))
    modpath="$(printf '%s' "$reqline" | awk '{print $1}')"
    modver="$(printf '%s' "$reqline" | awk '{print $2}')"
    key="${modpath}@${modver}"

    # first-party: replaced by a local filesystem path in THIS go.mod
    if printf '%s\n' "$local_replaces" | grep -qxF -- "$modpath"; then
      n_local=$((n_local + 1))
      continue
    fi

    if reason="$(is_whitelisted_dep "$key")"; then
      n_whitelisted=$((n_whitelisted + 1))
      echo "  WHITELISTED  $key  ($reason)  [$modroot]"
      continue
    fi

    escaped="$(escape_modpath "$modpath")"
    cachedir="$GOMODCACHE/${escaped}@${modver}"

    if [ ! -d "$cachedir" ]; then
      record_finding "MISSING_FROM_CACHE  $key  [$modroot]  (not downloaded — run 'go mod download' in $modroot to resolve, then re-run this audit; never auto-downloaded here)"
      continue
    fi

    license_file="$(find "$cachedir" -maxdepth 1 -type f \( \
        -iname 'LICENSE*' -o -iname 'COPYING*' -o -iname 'LICENCE*' -o -iname 'License*' \
      \) 2>/dev/null | head -1)"

    if [ -z "$license_file" ]; then
      record_finding "UNKNOWN  $key  [$modroot]  (module present in cache, no LICENSE/COPYING file found)"
      continue
    fi

    # grep's own exit code tells "read fine, no prohibited text" (1 -> safe) apart from
    # "could not even read the file" (2+ -> permissions, encoding, a directory that
    # slipped past the -type f filter). The old `-q ... 2>/dev/null` boolean collapsed
    # both into "not blocked", so an unreadable license counted as a permissive one —
    # see .claude/rules/absence-has-many-causes.md ("absence has many causes; presence
    # has one"). A single grep call now supplies both the verdict and the evidence.
    grep_out="$(grep -iE "$PROHIBITED_RE" "$license_file" 2>&1)"
    grep_rc=$?
    case "$grep_rc" in
      0)
        matched="$(printf '%s\n' "$grep_out" | head -1)"
        record_finding "BLOCKER  $key  [$modroot]  prohibited license text in $license_file: \"$matched\""
        continue
        ;;
      1)
        n_safe=$((n_safe + 1))
        ;;
      *)
        record_finding "COULD_NOT_EVALUATE  $key  [$modroot]  grep could not read $license_file (exit $grep_rc: ${grep_out:-no diagnostic output}) — an unreadable license file is NOT a permissive one, fail-closed"
        continue
        ;;
    esac
  done <<< "$(extract_direct_deps "$gomod")"
done <<< "$GO_MOD_DIRS"

echo
echo "  go.mod files scanned:        $n_modules"
echo "  direct deps enumerated:      $n_deps_total"
echo "  first-party (local replace): $n_local"
echo "  whitelisted (known, docs'd): $n_whitelisted"
echo "  SAFE (permissive license):   $n_safe"
echo "  findings from this pass:     $FINDINGS"

echo
echo "--- (b) Repo-wide copyleft mention sweep (informative — a doc CITING a license is expected) ---"
mention_count="$(git grep -niE 'AGPL|SSPL|BUSL|Elastic Licen|Commons Clause|copyleft' -- . ':!scripts/license-audit.sh' 2>/dev/null | wc -l | tr -d ' ')"
echo "  git grep -niE 'AGPL|SSPL|BUSL|Elastic Licen|Commons Clause|copyleft' -> $mention_count line(s) (informative, not a finding by itself)"

echo
echo "--- (b) docker-compose image allowlist ---"
# Allowlist derived from the actual compose files in this repo (re-derive with:
#   git grep -h '^\s*image:' -- '*docker-compose*.yml' | sort -u
# ). grafana/grafana carries the AGPL-3.0 ADR-0067 exception; every other entry is
# MIT/Apache/BSD-family per the license-policy gate.
ALLOWLIST_IMAGE_PREFIXES=(
  "qdrant/qdrant:"                              # Apache-2.0
  "rqlite/rqlite:"                               # MIT
  "otel/opentelemetry-collector-contrib:"        # Apache-2.0
  "prom/prometheus:"                             # Apache-2.0
  "prom/alertmanager:"                           # Apache-2.0
  "prom/blackbox-exporter:"                      # Apache-2.0
  "quay.io/prometheuscommunity/json-exporter:"   # Apache-2.0
  "grafana/grafana:"                             # AGPL-3.0 -- ADR-0067 exception, allowlisted deliberately
  "caddy:"                                       # Apache-2.0
  "nginx:"                                       # BSD-2-Clause
  "mcr.microsoft.com/dotnet/aspire-dashboard:"   # MIT -- ADR-0065 (local OTel viewer)
  # voicebox: MIT source, but its TTS engines pull GPL-3.0 (phonemizer-fork via
  # kokoro-onnx at module import, piper-tts on the other engine, espeak-ng on
  # both). Allowlisted DELIBERATELY under license-policy §Exceptions as a
  # separate, unmodified, HTTP-only service in examples/ — never vendored, never
  # shipped inside an aisotropy artifact. Conditions answered one by one, and
  # the four actions that VOID the exception, in ADR-0077. Built from source
  # (upstream tag v0.2.12 / 539325c) because the published image is not
  # publicly pullable — so this is a source pin, not a digest pin.
  "voicebox:"                                    # GPL-3.0 engine deps -- ADR-0077 exception, allowlisted deliberately
)

is_allowlisted_image() {
  local img="$1" prefix
  for prefix in "${ALLOWLIST_IMAGE_PREFIXES[@]}"; do
    case "$img" in
      "$prefix"*) return 0 ;;
    esac
  done
  return 1
}

compose_files="$(find . -maxdepth 4 -iname 'docker-compose*.yml' \
  -not -path '*/node_modules/*' -not -path '*/.claude/worktrees/*' \
  -not -path '*/.agent-tooling/checkpoints/*' -not -path '*/.venv/*' | sort)"

n_images=0
n_images_ok=0
while IFS= read -r cf; do
  [ -n "$cf" ] || continue
  while IFS= read -r img; do
    [ -n "$img" ] || continue
    n_images=$((n_images + 1))
    if is_allowlisted_image "$img"; then
      n_images_ok=$((n_images_ok + 1))
      echo "  OK           $img  [$cf]"
    else
      record_finding "UNLISTED_IMAGE  $img  [$cf]  (new image, license never reviewed — add to the allowlist above only after checking its license)"
    fi
  done <<< "$(grep -oE '^\s*image:\s*\S+' "$cf" 2>/dev/null | awk '{print $2}')"
done <<< "$compose_files"

echo
echo "  compose files scanned: $(printf '%s\n' "$compose_files" | grep -c .)"
echo "  images found:          $n_images"
echo "  images allowlisted:    $n_images_ok"

echo
echo "==================================================================="
if [ "$FINDINGS" -gt 0 ]; then
  echo " VERDICT: $FINDINGS finding(s) — review before proceeding:"
  for line in "${FINDING_LINES[@]}"; do
    echo "   - $line"
  done
  echo "==================================================================="
  exit 1
fi
echo " VERDICT: clean — 0 findings across $n_deps_total direct Go deps and $n_images compose images."
echo "==================================================================="
exit 0
