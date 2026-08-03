#!/usr/bin/env bash
# arenas/live/_shared/preflight.sh
#
# Toolchain preflight for the live arena. Each adapter's run.sh bootstraps its
# OWN project-local deps (a venv, node_modules, go modules, a maven package) but
# assumes the underlying TOOLCHAIN (node / go / python / java+maven) is already
# on the machine. When it is not, the adapter errors on every task and the
# failure only shows up AFTER a billable run — spring-ai silently erroring 70/70
# on a host with no JRE is the case this script exists to catch up front.
#
# Two modes:
#   (default)   CHECK — print a per-adapter toolchain matrix, mutate nothing,
#               and for anything missing print the exact `brew install` command.
#               Exit 0 if every discovered adapter's toolchain is present,
#               exit 3 if any is missing (so a runner/CI step can gate on it).
#   --install   Bootstrap what is safe to bootstrap: install missing SYSTEM
#               toolchains via Homebrew (announced per item) AND the project-local
#               deps each adapter needs (venv+pip, npm ci, go mod download,
#               mvn package). Only touches what is missing; re-running is a no-op.
#
# Style matches start-embed-server.sh: `set -euo pipefail`, all diagnostics to
# stderr as `PROGRESS:`/`WARNING:`/`ERROR:` lines, stdout reserved for the matrix.
#
# Usage:
#   bash arenas/live/_shared/preflight.sh            # check only
#   bash arenas/live/_shared/preflight.sh --install  # check, then install missing
#   bash arenas/live/_shared/preflight.sh --adapters spring-ai,mastra  # subset
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIVE="$(cd "$HERE/.." && pwd)"

INSTALL=0
ONLY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --install) INSTALL=1 ;;
    --adapters) shift; ONLY="${1:-}" ;;
    --adapters=*) ONLY="${1#--adapters=}" ;;
    -h|--help) sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "ERROR: unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

# --- toolchain requirement per adapter (derived from each run.sh) -------------
# Columns: adapter -> space-separated toolchain keys it needs.
# Toolchain keys: node | go | python | java  (java implies maven too).
adapter_toolchains() {
  case "$1" in
    ai-sdk|mastra)               echo "node" ;;
    eino|genkit-go|aisotropy)    echo "go" ;;
    langgraph|llamaindex|pydantic-ai) echo "python" ;;
    spring-ai)                   echo "java" ;;
    *)                           echo "" ;;
  esac
}

# --- how to detect a toolchain, and the brew formula that provides it ---------
# detect_<key> exits 0 iff present. brew_formula_<key> prints the formula name.
detect_node()   { command -v node >/dev/null 2>&1; }
detect_go()     { command -v go   >/dev/null 2>&1; }
detect_python() { command -v python3.13 >/dev/null 2>&1 || command -v python3 >/dev/null 2>&1; }
detect_java()   { command -v java >/dev/null 2>&1 && { command -v mvn >/dev/null 2>&1 || [ -x "$LIVE/spring-ai/mvnw" ]; }; }

brew_formula_node()   { echo "node"; }
brew_formula_go()     { echo "go"; }
brew_formula_python() { echo "python@3.13"; }
brew_formula_java()   { echo "openjdk maven"; }  # two formulae

toolchain_version() {
  case "$1" in
    node)   node --version 2>/dev/null ;;
    go)     go version 2>/dev/null | awk '{print $3}' ;;
    python) { command -v python3.13 >/dev/null 2>&1 && python3.13 --version || python3 --version; } 2>/dev/null ;;
    java)   java -version 2>&1 | head -1 ;;
  esac
}

# --- discover adapters (a dir with a run.sh), honoring --adapters -------------
discover_adapters() {
  local a name
  for a in "$LIVE"/*/run.sh; do
    [ -f "$a" ] || continue
    name="$(basename "$(dirname "$a")")"
    if [ -n "$ONLY" ]; then case ",$ONLY," in *",$name,"*) ;; *) continue ;; esac; fi
    echo "$name"
  done | sort
}

# --- brew presence (only needed for --install of a system toolchain) ----------
have_brew() { command -v brew >/dev/null 2>&1; }

install_toolchain() {  # $1 = key
  local key="$1" formula
  formula="$(brew_formula_"$key")"
  if ! have_brew; then
    echo "ERROR: $key is missing and Homebrew is not installed — cannot auto-install." >&2
    echo "       Install $key manually, or install Homebrew from https://brew.sh first." >&2
    return 1
  fi
  echo "PROGRESS: installing $key via: brew install $formula" >&2
  # shellcheck disable=SC2086
  brew install $formula 1>&2 || { echo "ERROR: brew install $formula failed" >&2; return 1; }
}

# --- project-local bootstrap per adapter (only in --install) ------------------
bootstrap_local() {  # $1 = adapter name
  local a="$1" dir="$LIVE/$1"
  case "$a" in
    langgraph|llamaindex|pydantic-ai)
      # Test the INTERPRETER, not the directory. A venv's bin/python is a symlink
      # to the exact minor version that created it (python3.13); a Homebrew
      # upgrade to 3.14 leaves the directory fully intact with a dangling
      # symlink. `[ -d .venv ]` is true for that corpse, so the weak test made
      # this bootstrap a no-op and the adapter then died at exec time — as
      # `python: command not found` (127), a message that names nothing about
      # venvs. Observed 2026-07-27 on pydantic-ai and llamaindex, both venvs
      # created 2026-07-17 against a python3.13 that no longer exists.
      #
      # This is the same defect the embed server's launcher had, and it is worse
      # here: preflight EXISTS to catch this, so the weak test made the check
      # certify a dead venv as healthy. A guard that cannot fail is not a guard.
      if [ ! -x "$dir/.venv/bin/python3" ]; then
        echo "PROGRESS: [$a] creating venv + installing requirements.txt" >&2
        rm -rf "$dir/.venv"
        # Interpreter choice is NOT free: llamaindex pins
        # llama-index-vector-stores-qdrant, which declares Requires-Python
        # >=3.10,<3.14. Falling back to "whatever python3 is" picks the newest
        # Homebrew Python (3.14 today) and the install fails with a wall of
        # "Ignored the following versions" — a failure whose message never
        # mentions the interpreter. Prefer a real 3.13, then uv's managed 3.13
        # (uv downloads it, so this works even when no 3.13 is on PATH), then
        # the ambient python3 as a last resort.
        local py
        if py="$(command -v python3.13)"; then
          "$py" -m venv "$dir/.venv"
        elif command -v uv >/dev/null 2>&1; then
          uv venv --python 3.13 "$dir/.venv" 1>&2
        else
          py="$(command -v python3)"
          echo "WARN: [$a] no python3.13 and no uv; using $("$py" -V 2>&1) — llamaindex requires <3.14" >&2
          "$py" -m venv "$dir/.venv"
        fi
        "$dir/.venv/bin/pip" install -q -r "$dir/requirements.txt" 1>&2 \
          || { echo "ERROR: [$a] venv/pip bootstrap failed" >&2; return 1; }
      fi ;;
    ai-sdk|mastra)
      if [ ! -d "$dir/node_modules" ] && [ -f "$dir/package.json" ]; then
        echo "PROGRESS: [$a] npm install" >&2
        ( cd "$dir" && npm install --silent ) 1>&2 || { echo "ERROR: [$a] npm install failed" >&2; return 1; }
      fi ;;
    eino|genkit-go|aisotropy)
      if [ -f "$dir/go.mod" ]; then
        echo "PROGRESS: [$a] go mod download" >&2
        ( cd "$dir" && go mod download ) 1>&2 || { echo "ERROR: [$a] go mod download failed" >&2; return 1; }
      fi ;;
    spring-ai)
      if [ -x "$dir/mvnw" ]; then
        echo "PROGRESS: [$a] mvnw package (skipTests) — this is slow the first time" >&2
        ( cd "$dir" && ./mvnw -q -DskipTests package ) 1>&2 || { echo "ERROR: [$a] mvnw package failed" >&2; return 1; }
      fi ;;
  esac
}

# ------------------------------------------------------------------------------
echo "PROGRESS: arena preflight — mode: $([ "$INSTALL" = 1 ] && echo INSTALL || echo CHECK)" >&2

adapters="$(discover_adapters)"
[ -n "$adapters" ] || { echo "ERROR: no adapters found under $LIVE/*/run.sh" >&2; exit 2; }

# collect the union of required toolchains and their status
declare -a MISSING_TC=()
printf '%-14s | %-8s | %s\n' "adapter" "toolchain" "status"
printf '%-14s-+-%-8s-+-%s\n' "--------------" "--------" "------------------------------------"

missing_any=0
for a in $adapters; do
  tcs="$(adapter_toolchains "$a")"
  if [ -z "$tcs" ]; then
    printf '%-14s | %-8s | %s\n' "$a" "?" "unknown adapter — no toolchain mapping"
    continue
  fi
  for tc in $tcs; do
    if detect_"$tc"; then
      printf '%-14s | %-8s | present (%s)\n' "$a" "$tc" "$(toolchain_version "$tc")"
    else
      printf '%-14s | %-8s | MISSING -> brew install %s\n' "$a" "$tc" "$(brew_formula_"$tc")"
      missing_any=1
      case " ${MISSING_TC[*]:-} " in *" $tc "*) ;; *) MISSING_TC+=("$tc") ;; esac
    fi
  done
done

if [ "$INSTALL" != 1 ]; then
  if [ "$missing_any" = 1 ]; then
    echo "" >&2
    echo "WARNING: some toolchains are missing — the adapters needing them will error on every task." >&2
    echo "         Re-run with --install to install them (and bootstrap project-local deps), or run the" >&2
    echo "         printed 'brew install' commands yourself. This CHECK run mutated nothing." >&2
    exit 3
  fi
  echo "PROGRESS: all discovered adapters have their toolchain present." >&2
  # State the SCOPE of this green, or it reads as "ready to run".
  #
  # On 2026-07-28 this script reported all nine adapters green and a full board
  # was launched against Qdrant, rqlite and the embed server that were not
  # reachable. Forty minutes later the run was killed. The check was correct and
  # the conclusion drawn from it was not: it verifies TOOLCHAINS, and a board
  # needs toolchains AND stores.
  #
  # The stores are deliberately NOT probed here. The runner gates them in-process
  # before the first billable call (gateServices, arenas/live/runner/main.go) and
  # distinguishes DOWN from MISCONFIGURED from INCONCLUSIVE from NO COLLECTION —
  # a bash reimplementation would drift from that vocabulary and be worse than
  # the pointer. What this script owes the reader is not a second probe, it is
  # honesty about which half it checked.
  echo "PROGRESS: SCOPE — toolchains only. Stores (Qdrant, rqlite, embed server) are NOT checked here;" >&2
  echo "PROGRESS:   the runner gates them in-process before spending. A green here means the adapters" >&2
  echo "PROGRESS:   CAN run, not that a board WILL. Start the stores with 'docker compose up -d'." >&2
  exit 0
fi

# --- INSTALL mode -------------------------------------------------------------
rc=0
for tc in "${MISSING_TC[@]:-}"; do
  [ -n "$tc" ] || continue
  install_toolchain "$tc" || rc=1
done
for a in $adapters; do
  bootstrap_local "$a" || rc=1
done
if [ "$rc" = 0 ]; then
  echo "PROGRESS: preflight install complete — toolchains present, project-local deps bootstrapped." >&2
else
  echo "ERROR: preflight install finished with failures — see the ERROR lines above." >&2
fi
exit "$rc"
