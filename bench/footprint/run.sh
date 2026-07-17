#!/usr/bin/env bash
# Footprint benchmark: build a minimal "hello agent" image per framework, then
# measure objective, language-agnostic deployment weight — image size,
# cold-start, idle RSS, dependency weight. The model is held constant as an
# offline stub in EVERY subject, so what is measured is the framework + runtime
# footprint, not model behaviour.
#
#   bash bench/footprint/run.sh            # build all, measure, print table, clean up
#   RUNS=9 bash bench/footprint/run.sh     # more cold-start samples (default 5)
#   KEEP=1 bash bench/footprint/run.sh     # do not delete images/containers after
#
# Requires: docker, curl, python3 (millisecond clock — macOS `date` lacks %N).
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BENCH_DIR="$REPO_ROOT/bench/footprint"
RUNS="${RUNS:-5}"
KEEP="${KEEP:-0}"
TAGPREFIX="footprint-bench"
RESULTS="$BENCH_DIR/results.txt"

# Subjects: label | build_context | dockerfile | host_port | lang | packaging
# aisotropy builds from the REPO ROOT Dockerfile (untouched, production image).
SUBJECTS=(
  "aisotropy|$REPO_ROOT|$REPO_ROOT/Dockerfile|18080|Go|distroless static"
  "langgraph|$BENCH_DIR/langgraph|$BENCH_DIR/langgraph/Dockerfile|18081|Python|python:3.12-slim"
  "mastra|$BENCH_DIR/mastra|$BENCH_DIR/mastra/Dockerfile|18082|TypeScript|node:22-slim"
  "adk-go|$BENCH_DIR/adk-go|$BENCH_DIR/adk-go/Dockerfile|18083|Go|distroless static"
)

now_ms() { python3 -c 'import time;print(int(time.time()*1000))'; }

log() { printf '\033[1;36m[bench]\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m[bench]\033[0m %s\n' "$*" >&2; }

# ---- module/dep counters (host-side, no shell needed in image) -------------
go_modules() { awk '{print $1}' "$1/go.sum" 2>/dev/null | sort -u | wc -l | tr -d ' '; }

# ---- per-image measurements ------------------------------------------------
image_size_bytes() { docker image inspect "$1" --format '{{.Size}}' 2>/dev/null; }

image_compressed_bytes() { docker save "$1" 2>/dev/null | gzip -c | wc -c | tr -d ' '; }

# Measure cold-start: time from `docker run` to first HTTP 200 on /healthz.
# Returns "median min max" in ms over $RUNS runs.
cold_start() {
  local tag="$1" port="$2" cname="$3"
  local samples=()
  local i start elapsed code
  for ((i = 1; i <= RUNS; i++)); do
    docker rm -f "$cname" >/dev/null 2>&1
    start="$(now_ms)"
    if ! docker run -d --name "$cname" -p "127.0.0.1:$port:8080" "$tag" >/dev/null 2>&1; then
      echo "NA NA NA"; return 1
    fi
    # Poll until /healthz answers 200 (or timeout ~30s).
    elapsed=NA
    for ((t = 0; t < 1500; t++)); do
      code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/healthz" 2>/dev/null)"
      if [[ "$code" == "200" ]]; then
        elapsed="$(( $(now_ms) - start ))"
        break
      fi
      sleep 0.02
    done
    samples+=("$elapsed")
    docker rm -f "$cname" >/dev/null 2>&1
  done
  # median/min/max
  python3 - "${samples[@]}" <<'PY'
import sys, statistics
xs = [int(x) for x in sys.argv[1:] if x != "NA"]
if not xs:
    print("NA NA NA"); raise SystemExit
xs.sort()
print(f"{int(statistics.median(xs))} {xs[0]} {xs[-1]}")
PY
}

# Idle RSS a few seconds after ready. Returns MiB (float) or NA.
idle_rss() {
  local tag="$1" port="$2" cname="$3"
  docker rm -f "$cname" >/dev/null 2>&1
  docker run -d --name "$cname" -p "127.0.0.1:$port:8080" "$tag" >/dev/null 2>&1 || { echo NA; return 1; }
  local code ok=0 t
  for ((t = 0; t < 1500; t++)); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/healthz" 2>/dev/null)"
    [[ "$code" == "200" ]] && { ok=1; break; }
    sleep 0.02
  done
  if [[ "$ok" != 1 ]]; then docker rm -f "$cname" >/dev/null 2>&1; echo NA; return 1; fi
  sleep 3  # settle
  local mem
  mem="$(docker stats --no-stream --format '{{.MemUsage}}' "$cname" 2>/dev/null | awk '{print $1}')"
  docker rm -f "$cname" >/dev/null 2>&1
  # Normalise "12.3MiB"/"1.2GiB"/"512KiB" -> MiB
  python3 - "$mem" <<'PY'
import sys, re
s = sys.argv[1] if len(sys.argv) > 1 else ""
m = re.match(r'([\d.]+)\s*([KMG]i?B)', s)
if not m:
    print("NA"); raise SystemExit
v, u = float(m.group(1)), m.group(2)
factor = {'KiB': 1/1024, 'MiB': 1, 'GiB': 1024, 'B': 1/1048576}
print(f"{v*factor.get(u,1):.1f}")
PY
}

# Dependency weight. For slim (Python/Node) images we can du inside the image.
# For distroless Go we report go.sum module count (host-side) + note the binary
# is statically linked so deps do not ship as files.
dep_weight() {
  local label="$1" tag="$2" ctx="$3"
  case "$label" in
    langgraph)
      docker run --rm --entrypoint sh "$tag" -c \
        'du -sm /usr/local/lib/python3.12/site-packages 2>/dev/null | cut -f1 | xargs -I{} echo "{} MiB site-packages"; echo "$(ls /usr/local/lib/python3.12/site-packages | wc -l | tr -d " ") top-level entries"' \
        2>/dev/null | paste -sd' / ' -
      ;;
    mastra)
      docker run --rm --entrypoint sh "$tag" -c \
        'du -sm /app/node_modules 2>/dev/null | cut -f1 | xargs -I{} echo "{} MiB node_modules"; echo "$(find /app/node_modules -maxdepth 2 -name package.json | wc -l | tr -d " ") packages"' \
        2>/dev/null | paste -sd' / ' -
      ;;
    aisotropy|adk-go)
      echo "$(go_modules "$ctx") modules (go.sum) / static-linked (no dep files ship)"
      ;;
    *) echo "NA" ;;
  esac
}

# ---------------------------------------------------------------------------
: > "$RESULTS"
declare -A BUILT SIZE CSIZE COLD RSS DEPS LANG PACK

log "Building ${#SUBJECTS[@]} images (RUNS=$RUNS cold-start samples each)..."
for spec in "${SUBJECTS[@]}"; do
  IFS='|' read -r label ctx dockerfile port lang pack <<<"$spec"
  tag="$TAGPREFIX-$label:latest"
  LANG[$label]="$lang"; PACK[$label]="$pack"
  log "build: $label ($lang, $pack)"
  if docker build -q -t "$tag" -f "$dockerfile" "$ctx" >/dev/null 2>"$BENCH_DIR/.build-$label.log"; then
    BUILT[$label]=1
    log "  ok: $label"
  else
    BUILT[$label]=0
    warn "  BUILD FAILED: $label — see bench/footprint/.build-$label.log"
    warn "  $(tail -3 "$BENCH_DIR/.build-$label.log" | tr '\n' ' ')"
  fi
done

log "Measuring..."
for spec in "${SUBJECTS[@]}"; do
  IFS='|' read -r label ctx dockerfile port lang pack <<<"$spec"
  tag="$TAGPREFIX-$label:latest"
  cname="$TAGPREFIX-$label-run"
  [[ "${BUILT[$label]}" == 1 ]] || { SIZE[$label]=NA; CSIZE[$label]=NA; COLD[$label]="NA NA NA"; RSS[$label]=NA; DEPS[$label]=NA; continue; }
  log "  measure: $label"
  SIZE[$label]="$(image_size_bytes "$tag")"
  CSIZE[$label]="$(image_compressed_bytes "$tag")"
  COLD[$label]="$(cold_start "$tag" "$port" "$cname")"
  RSS[$label]="$(idle_rss "$tag" "$port" "$cname")"
  DEPS[$label]="$(dep_weight "$label" "$tag" "$ctx")"
done

# ---- render table ----------------------------------------------------------
mib() { [[ "$1" =~ ^[0-9]+$ ]] && python3 -c "print(f'{$1/1048576:.1f}')" || echo NA; }

{
  echo "# Footprint benchmark — raw results"
  echo "# generated: $(date -u +%Y-%m-%dT%H:%M:%SZ) | host: $(uname -sm) | docker: $(docker version --format '{{.Server.Version}}' 2>/dev/null)"
  echo "# cold-start samples per subject: RUNS=$RUNS"
  echo
  printf '%-11s | %-10s | %-18s | %11s | %11s | %-22s | %8s | %s\n' \
    "framework" "language" "packaging" "img (MiB)" "gz (MiB)" "cold-start ms med/mn/mx" "RSS MiB" "dep weight"
  printf -- '------------|------------|--------------------|-------------|-------------|------------------------|----------|--------------------\n'
  for spec in "${SUBJECTS[@]}"; do
    IFS='|' read -r label ctx dockerfile port lang pack <<<"$spec"
    read -r cmed cmin cmax <<<"${COLD[$label]}"
    printf '%-11s | %-10s | %-18s | %11s | %11s | %8s/%s/%-8s | %8s | %s\n' \
      "$label" "${LANG[$label]}" "${PACK[$label]}" \
      "$(mib "${SIZE[$label]}")" "$(mib "${CSIZE[$label]}")" \
      "$cmed" "$cmin" "$cmax" "${RSS[$label]}" "${DEPS[$label]}"
  done
} | tee "$RESULTS"

# ---- cleanup ---------------------------------------------------------------
if [[ "$KEEP" != 1 ]]; then
  log "Cleaning up images/containers (KEEP=1 to retain)..."
  for spec in "${SUBJECTS[@]}"; do
    IFS='|' read -r label _ _ _ _ _ <<<"$spec"
    docker rm -f "$TAGPREFIX-$label-run" >/dev/null 2>&1
    docker rmi -f "$TAGPREFIX-$label:latest" >/dev/null 2>&1
  done
  rm -f "$BENCH_DIR"/.build-*.log
else
  log "KEEP=1 — images retained ($TAGPREFIX-*:latest)."
fi

log "Done. Raw table saved to bench/footprint/results.txt"
