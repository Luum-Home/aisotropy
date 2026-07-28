#!/usr/bin/env bash
# arena-post-board.sh — everything worth doing once a board finishes, at zero cost.
#
# Three steps, none of which spends a model call:
#
#   1. Extract the anchor set from the durable record stream into ANCHORS.jsonl.
#      The anchors are a SUBSET of the suite the board just ran, so they are
#      already measured — re-running them would pay twice.
#
#   2. Report where the matrix manifests DISAGREE with what the board observed.
#      Deliberately a report and not an auto-sync: a manifest that rewrites itself
#      from the results it is meant to check stops being a check. The disagreement
#      is the signal; a human decides which side is wrong.
#
#   3. Re-render RESULTS.md from the records (-render-only), so any manifest edit
#      made in step 2 is reflected without touching the provider.
#
# Usage: arena-post-board.sh [--render] ; --render performs step 3.
# Exit codes: 0 clean · 1 disagreements found · 2 error.

set -uo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIVE="${REPO_ROOT}/arenas/live"
RECORDS="${LIVE}/RESULTS.records.jsonl"
DO_RENDER=0
[[ "${1:-}" == "--render" ]] && DO_RENDER=1

if [[ ! -s "${RECORDS}" ]]; then
  echo "ERROR: no record stream at ${RECORDS}. Run a board first." >&2
  exit 2
fi
if pgrep -f 'aisotropy-live-runner' >/dev/null 2>&1; then
  echo "ERROR: a board is still running — its record stream is incomplete." >&2
  echo "Acting on a half-written stream would produce an anchor baseline and a" >&2
  echo "coverage report that describe a run that never happened." >&2
  exit 2
fi

echo "==================================================================="
echo " arena-post-board: anchors + manifest drift + render (0 model calls)"
echo "==================================================================="
echo

# STEP ZERO: ARCHIVE THE STREAM BEFORE ANYTHING ELSE TOUCHES IT.
#
# RESULTS.records.jsonl is the single source from which a board can be
# re-rendered, drift-checked and re-analysed at cell level — and it is
# gitignored, so git cannot bring it back. Worse, the runner appends to it on
# EVERY invocation, so a two-cell debugging run silently mixes into a
# thousand-cell board.
#
# On 2026-07-28 an Agnes board's 780 records were destroyed by a one-line
# mistake: `head -n -2` (a GNU-ism BSD head rejects) failed, its redirect left an
# empty file, and the "backup" was taken AFTER the truncation rather than before.
# The rendered RESULTS.md survived only because it was committed. Nothing about
# that sequence was exotic; it just had no archive standing in front of it.
ARCHIVE_DIR="${LIVE}/.records-archive"
mkdir -p "${ARCHIVE_DIR}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
ARCHIVE="${ARCHIVE_DIR}/records-${STAMP}.jsonl"
cp "${RECORDS}" "${ARCHIVE}"
chmod 444 "${ARCHIVE}"   # read-only: an archive that can be overwritten is not one
echo "0) archived $(wc -l < "${ARCHIVE}" | tr -d ' ') record(s) -> ${ARCHIVE#${LIVE}/} (read-only)"
echo
echo "1) anchor baseline"
python3 "${REPO_ROOT}/scripts/arena-extract-anchors.py" --records "${RECORDS}" || exit 2
echo

echo "2) manifest vs observed"
DRIFT_OUT="$(python3 "${REPO_ROOT}/scripts/arena-manifest-drift.py" "${RECORDS}" "${LIVE}")"
rc=$?
printf '%s\n' "${DRIFT_OUT}"
DRIFT=0
[[ ${rc} -eq 1 ]] && DRIFT=1
echo

if [[ "${DO_RENDER}" -eq 1 ]]; then
  echo "3) re-render from records (no model calls, no adapter spawns)"
  ( cd "${LIVE}/runner" && go run . -render-only ) || exit 2
else
  echo "3) render SKIPPED — pass --render after resolving any drift above."
  echo "   (-render-only regenerates RESULTS.md from the record stream alone.)"
fi

echo
echo "==================================================================="
if [[ "${DRIFT}" -eq 1 ]]; then
  echo " done — anchors appended, nothing spent. DRIFT REPORTED above:"
  echo " a manifest declaration and the board disagree. Fix whichever is wrong;"
  echo " the script will not sync them for you, because a manifest that rewrites"
  echo " itself from the results it exists to check is no longer a check."
  echo "==================================================================="
  exit 1
fi
echo " done — anchors appended, manifests agree with the board, nothing spent."
echo "==================================================================="
