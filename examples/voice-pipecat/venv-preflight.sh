#!/usr/bin/env bash
# venv-preflight.sh — validate this example's virtualenv BEFORE anything tries
# to use it, and say exactly what is wrong when it is not usable.
#
# Why this exists: on 2026-07-27 the lane failed with a bare
#   (eval):1: no such file or directory: .venv/bin/python
# because Homebrew had removed `python@3.13`, leaving `.venv/bin/python3` a
# DANGLING SYMLINK. `ls` still showed the file, so the venv looked present. That
# error message names neither the cause nor the fix, and a venv is exactly the
# kind of machine-local artifact that rots silently between sessions while the
# repo stays green.
#
# A missing interpreter is not a test failure and must not look like one: this
# exits non-zero with a remediation before pytest is ever invoked.
#
# All four branches were exercised on 2026-07-27, each by reconstructing the
# state rather than by reading the code:
#   missing        — venv moved aside              -> exit 1, recreate commands
#   dangling       — today's exact bug, rebuilt     -> exit 1, names the target
#   healthy        — the real venv                  -> exit 0
#   partial        — a venv with ONLY pytest        -> exit 0 + warning, AND the
#                    premise behind that exit code was verified rather than
#                    assumed: in that state `pytest tests/` really does run
#                    (8 passed, 8 skipped). Calling it broken would be a lie in
#                    the noisier direction, and a guard that shouts too often is
#                    a guard people learn to ignore.
#
# Usage:
#   examples/voice-pipecat/venv-preflight.sh            # check only
#   examples/voice-pipecat/venv-preflight.sh --quiet    # exit code only
#
# Exit codes (repo guard convention):
#   0  usable   — interpreter resolves and the test-lane imports are present
#   1  findings — venv missing, dangling, or incomplete (message says which)
#   2  error    — could not evaluate

set -u

quiet=false
[ "${1:-}" = "--quiet" ] && quiet=true
say() { $quiet || echo "$@"; }

here="$(cd "$(dirname "$0")" && pwd)" || { echo "venv-preflight: cannot resolve own directory" >&2; exit 2; }
venv="$here/.venv"
py="$venv/bin/python"

# The pinned interpreter range is a real constraint, not a preference. The
# floor is pipecat 1.x's own `Requires-Python: >=3.11`; the ceiling is numba,
# a base dependency of pipecat, which does not build on 3.14.
WANT_RANGE=">=3.11,<3.14"

if [ ! -d "$venv" ]; then
  say "venv-preflight: ❌ no virtualenv at $venv"
  say "   create one with an interpreter in $WANT_RANGE, e.g.:"
  say "     python3.11 -m venv examples/voice-pipecat/.venv"
  say "     examples/voice-pipecat/.venv/bin/pip install -r examples/voice-pipecat/requirements.txt -r examples/voice-pipecat/requirements-test.txt"
  exit 1
fi

# -e follows symlinks, so a DANGLING link fails here even though `ls` shows it.
if [ ! -e "$py" ]; then
  target="$(readlink "$py" 2>/dev/null || true)"
  say "venv-preflight: ❌ the virtualenv's interpreter does not resolve"
  say "   $py${target:+  ->  $target}"
  say "   This is the classic rotted venv: the interpreter it was built against"
  say "   was upgraded or removed (Homebrew retires python@3.N), leaving a"
  say "   dangling symlink. The venv cannot be repaired in place — recreate it:"
  say "     rm -rf examples/voice-pipecat/.venv"
  say "     python3.11 -m venv examples/voice-pipecat/.venv"
  say "     examples/voice-pipecat/.venv/bin/pip install -r examples/voice-pipecat/requirements.txt -r examples/voice-pipecat/requirements-test.txt"
  exit 1
fi

if ! ver="$("$py" -c 'import sys;print("%d.%d.%d"%sys.version_info[:3])' 2>/dev/null)"; then
  say "venv-preflight: ❌ $py exists but cannot execute — recreate the venv (see README)."
  exit 1
fi

if ! "$py" -c 'import sys;sys.exit(0 if (3,11) <= sys.version_info < (3,14) else 1)' 2>/dev/null; then
  say "venv-preflight: ❌ interpreter is $ver, outside the required $WANT_RANGE"
  say "   pipecat 1.x declares Requires-Python >=3.11, and depends on numba,"
  say "   which does not build on 3.14."
  exit 1
fi

# The lane's own imports. pytest alone is the skip-lane floor; the model-backed
# packages are what the real lane needs, so they are reported separately rather
# than lumped into one pass/fail.
missing_core=""
for m in pytest; do
  "$py" -c "import $m" 2>/dev/null || missing_core="$missing_core $m"
done
# Probe what each BACKEND actually needs, not a flat package list. The flat
# list was a real hole, found 2026-08-03: it probed `faster_whisper`, which
# imports fine, while `pipecat.services.whisper.stt` — the class the
# kokoro-fastapi fallback actually constructs — raised ImportError because
# pipecat 1.x hard-requires mlx_whisper on Darwin/arm64 (ADR-0078). The venv
# reported ✅ with the fallback backend broken. A package importing is not the
# same as the service that uses it importing, and only the second one matters.
probe_backend() { # $1 = label, $2... = importable module paths
  local label="$1"; shift
  local miss=""
  for m in "$@"; do "$py" -c "import $m" 2>/dev/null || miss="$miss $m"; done
  printf '%s' "$miss"
}
missing_common="$(probe_backend common pipecat openai httpx)"
missing_voicebox="$(probe_backend voicebox pipecat.services.openai.stt)"
missing_kokoro="$(probe_backend kokoro faster_whisper pipecat.services.whisper.stt)"
missing_lane="$missing_common$missing_voicebox"   # the DEFAULT backend gates the lane

if [ -n "$missing_core" ]; then
  say "venv-preflight: ❌ interpreter $ver OK, but the test floor is missing:$missing_core"
  say "   $venv/bin/pip install -r $here/requirements-test.txt"
  exit 1
fi

if [ -n "$missing_lane" ]; then
  say "venv-preflight: ⚠️  interpreter $ver OK, skip-lane usable, but VOICE_LANE=real needs:$missing_lane"
  say "   $venv/bin/pip install -r $here/requirements.txt"
  say "   (exit 0: the offline WER unit tests still run — this is a warning, not a failure)"
  exit 0
fi

if [ -n "$missing_kokoro" ]; then
  say "venv-preflight: ✅ python $ver — DEFAULT backend (voicebox) ready"
  say "   ⚠️  the VOICE_AUDIO_BACKEND=kokoro-fastapi fallback is NOT usable here:$missing_kokoro"
  say "   That switch is what makes depending on a v0.2.x project safe (ADR-0077),"
  say "   so a broken fallback is worth knowing about even though the lane passes."
  say "   On Darwin/arm64 pipecat 1.x hard-requires mlx_whisper (ADR-0078); it is"
  say "   declared in requirements.txt but pulls mlx + torch, so it is often absent:"
  say "     $venv/bin/pip install -r $here/requirements.txt"
  say "   (exit 0: the default backend and the whole test lane work without it)"
  exit 0
fi

say "venv-preflight: ✅ python $ver — test floor, default backend (voicebox) and the"
say "   kokoro-fastapi fallback are all importable"
exit 0
