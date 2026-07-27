#!/usr/bin/env bash
# resource-guard.sh — pre-flight resource check for heavy lanes (voice tests,
# full go test sweeps, docker-heavy phases). Sibling of prune-guard.sh /
# arena-determinism-guard.sh: a guard that cannot evaluate must never absolve.
#
# Why it exists: with several agent sessions running concurrently, the machine
# saturates (observed 2026-07-23: swap 18.0/19.4 GB, ~630 MB free RAM) and
# macOS starts OOM-killing test workers — the exact cause of the
# internal/guardrails "worker exit: signal: killed" flake. Heavy lanes must
# check BEFORE spending, the same way the runtime rejects before calling the
# LLM (constitutional "reject/degrade before spending" gate, applied to the
# dev machine itself).
#
# Usage:
#   scripts/resource-guard.sh              # check only
#   scripts/resource-guard.sh --explain    # check + print the numbers
#
# Exit codes (prune-guard convention):
#   0  OK        — resources available, run the heavy lane
#   1  DEGRADED  — runnable but tight: run serially, small models, capped threads
#   3  BLOCKED   — saturated (or unmeasurable): do NOT start heavy work now
#
# Thresholds are deliberate defaults (documented against the observed
# saturation incident above, the passQAThreshold pattern). Env-overridable
# for testing and per-machine tuning — the DEFAULTS are the contract:
SWAP_PCT_DEGRADED="${RESOURCE_GUARD_SWAP_DEGRADED:-70}"   # swap % → "tight"
SWAP_PCT_BLOCKED="${RESOURCE_GUARD_SWAP_BLOCKED:-90}"     # swap % → do not start
FREE_MB_DEGRADED="${RESOURCE_GUARD_FREE_MB_DEGRADED:-2048}"  # free RAM MB → DEGRADED
FREE_MB_BLOCKED="${RESOURCE_GUARD_FREE_MB_BLOCKED:-512}"     # free RAM MB → BLOCKED
LOAD_PER_CORE_DEGRADED="${RESOURCE_GUARD_LOAD_DEGRADED:-1.5}"  # loadavg/core → DEGRADED

set -u

explain=false
need_mb=0
while [ $# -gt 0 ]; do
  case "$1" in
    --explain) explain=true; shift ;;
    # --need-mb N: the caller DECLARES how much RAM its lane needs (a docker
    # build, a kind node, a model load). "Not saturated" is not the same as
    # "has 4 GB free": on a small machine a lane can pass a pressure check and
    # still thrash. Declaring the need turns the guard from a mood ring into a
    # capacity check.
    --need-mb) need_mb="${2:-0}"; shift 2 || { echo "resource-guard: --need-mb requires a value" >&2; exit 2; } ;;
    *) echo "resource-guard: unknown argument: $1" >&2; exit 2 ;;
  esac
done

os="$(uname -s)"

swap_pct=-1
free_mb=-1
load_per_core=-1

if [ "$os" = "Darwin" ]; then
  # PRIMARY signal on macOS: the kernel's own memory-pressure verdict.
  # Swap OCCUPANCY is a LAGGING indicator here — swapped pages stay swapped
  # after pressure eases, so a machine can show 90%+ swap while
  # memory_pressure reports 50%+ system-wide free (observed 2026-07-23:
  # swap 94%, real free 56%, pressure level 1/normal — a false BLOCKED).
  # kern.memorystatus_vm_pressure_level: 1=normal, 2=warn, 4=critical.
  pressure_level="$(sysctl -n kern.memorystatus_vm_pressure_level 2>/dev/null || echo -1)"
  free_sys_pct="$(memory_pressure -Q 2>/dev/null | awk -F': ' '/free percentage/{gsub("%","");print $2}' | tr -d ' ')"
  # Free RAM (immediately-allocatable): pages free + speculative, vm_stat.
  page_size="$(vm_stat 2>/dev/null | head -1 | grep -o '[0-9]*' | head -1)"
  pages_free="$(vm_stat 2>/dev/null | awk '/Pages free/{gsub("\\.","");print $3}')"
  pages_spec="$(vm_stat 2>/dev/null | awk '/Pages speculative/{gsub("\\.","");print $3}')"
  if [ -n "${page_size:-}" ] && [ -n "${pages_free:-}" ]; then
    free_mb=$(( (pages_free + ${pages_spec:-0}) * page_size / 1048576 ))
  fi
  # Swap occupancy kept as an INFORMATIVE secondary (printed, never the
  # sole blocker): "vm.swapusage: total = 19456.00M used = 18032.56M ..."
  swap_line="$(sysctl -n vm.swapusage 2>/dev/null || true)"
  if [ -n "$swap_line" ]; then
    total="$(printf '%s' "$swap_line" | awk '{print $3}' | tr -d 'M')"
    used="$(printf '%s' "$swap_line" | awk '{print $6}' | tr -d 'M')"
    if [ -n "$total" ] && [ "${total%.*}" -gt 0 ] 2>/dev/null; then
      swap_pct="$(awk -v u="$used" -v t="$total" 'BEGIN{printf "%d", (u/t)*100}')"
    else
      swap_pct=0  # no swap allocated at all = no pressure signal from swap
    fi
  fi
  ncpu="$(sysctl -n hw.ncpu 2>/dev/null || echo 1)"
elif [ "$os" = "Linux" ]; then
  # Swap + available memory from /proc/meminfo (kB)
  swap_total="$(awk '/SwapTotal/{print $2}' /proc/meminfo)"
  swap_free="$(awk '/SwapFree/{print $2}' /proc/meminfo)"
  mem_avail="$(awk '/MemAvailable/{print $2}' /proc/meminfo)"
  if [ -n "$swap_total" ] && [ "$swap_total" -gt 0 ]; then
    swap_pct=$(( (swap_total - swap_free) * 100 / swap_total ))
  else
    swap_pct=0
  fi
  [ -n "$mem_avail" ] && free_mb=$(( mem_avail / 1024 ))
  ncpu="$(nproc 2>/dev/null || echo 1)"
else
  echo "resource-guard: BLOCKED — unsupported OS '$os' (cannot evaluate; a guard that cannot evaluate must never absolve)" >&2
  exit 3
fi

# 1-min load average per core (portable via uptime)
load1="$(uptime | sed 's/.*load average[s]*: *//' | cut -d',' -f1 | tr -d ' ')"
if [ -n "$load1" ]; then
  load_per_core="$(awk -v l="$load1" -v c="$ncpu" 'BEGIN{printf "%.2f", l/c}')"
fi

# Fail closed if we could not measure the two primary signals.
if [ "$swap_pct" -lt 0 ] || [ "$free_mb" -lt 0 ]; then
  echo "resource-guard: BLOCKED — could not measure swap/RAM (swap_pct=$swap_pct free_mb=$free_mb)" >&2
  exit 3
fi

verdict=0
reason="ok"
pl="${pressure_level:--1}"
fsp="${free_sys_pct:--1}"
if [ "$os" = "Darwin" ] && [ "$pl" -gt 0 ] && [ -n "$fsp" ] && [ "$fsp" -ge 0 ] 2>/dev/null; then
  # Darwin with kernel pressure available: level 4 (critical) or single-digit
  # system-free blocks; level 2 (warn), low system-free, or high load
  # degrades. BOTH vm_stat free pages (pessimistic by design — reclaimable
  # cache is not "free") AND swap occupancy (lagging after pressure eases)
  # are informative only here: memory_pressure's system-wide free percentage
  # already accounts for free+purgeable+reclaimable, which is what a new
  # heavy process can actually get.
  if [ "$pl" -ge 4 ] || [ "$fsp" -le "${RESOURCE_GUARD_SYSFREE_PCT_BLOCKED:-8}" ]; then
    verdict=3; reason="saturated"
  elif [ "$pl" -ge 2 ] || [ "$fsp" -le "${RESOURCE_GUARD_SYSFREE_PCT_DEGRADED:-20}" ] \
    || awk -v l="$load_per_core" -v t="$LOAD_PER_CORE_DEGRADED" 'BEGIN{exit !(l>=t)}'; then
    verdict=1; reason="tight"
  fi
  extra="pressure L${pl}, sys-free ${fsp}% (blocked<=${RESOURCE_GUARD_SYSFREE_PCT_BLOCKED:-8}, degraded<=${RESOURCE_GUARD_SYSFREE_PCT_DEGRADED:-20}); swap ${swap_pct}%, vm_stat-free ${free_mb}MB (informative)"
else
  # Linux, or Darwin without a pressure reading: original swap+RAM thresholds.
  if [ "$swap_pct" -ge "$SWAP_PCT_BLOCKED" ] || [ "$free_mb" -le "$FREE_MB_BLOCKED" ]; then
    verdict=3; reason="saturated"
  elif [ "$swap_pct" -ge "$SWAP_PCT_DEGRADED" ] || [ "$free_mb" -le "$FREE_MB_DEGRADED" ] \
    || awk -v l="$load_per_core" -v t="$LOAD_PER_CORE_DEGRADED" 'BEGIN{exit !(l>=t)}'; then
    verdict=1; reason="tight"
  fi
  extra="swap ${swap_pct}% (degraded>=${SWAP_PCT_DEGRADED}, blocked>=${SWAP_PCT_BLOCKED})"
fi

# Declared-need check (independent of the pressure verdict, and stricter).
# available_mb: what a new process can realistically get. On Darwin the kernel's
# system-free percentage already folds in reclaimable memory, so use it against
# physical RAM; elsewhere fall back to the measured free figure.
if [ "$need_mb" -gt 0 ] 2>/dev/null; then
  avail_mb="$free_mb"
  if [ "$os" = "Darwin" ] && [ "${fsp:--1}" -ge 0 ] 2>/dev/null; then
    phys_mb=$(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1048576 ))
    [ "$phys_mb" -gt 0 ] && avail_mb=$(( phys_mb * fsp / 100 ))
  fi
  if [ "$avail_mb" -lt "$need_mb" ]; then
    echo "resource-guard: BLOCKED (insufficient) — lane declared it needs ${need_mb}MB, ~${avail_mb}MB available" >&2
    exit 3
  fi
  extra="$extra; declared need ${need_mb}MB <= ~${avail_mb}MB available"
fi

if $explain || [ "$verdict" -ne 0 ]; then
  label=OK; [ "$verdict" -eq 1 ] && label=DEGRADED; [ "$verdict" -eq 3 ] && label=BLOCKED
  echo "resource-guard: $label ($reason) — ${extra}; free RAM ${free_mb}MB (degraded<=${FREE_MB_DEGRADED}, blocked<=${FREE_MB_BLOCKED}); load/core ${load_per_core} (degraded>=${LOAD_PER_CORE_DEGRADED}, cores ${ncpu})"
fi
exit "$verdict"
