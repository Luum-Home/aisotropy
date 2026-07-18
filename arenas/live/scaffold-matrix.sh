#!/usr/bin/env bash
#
# scaffold-matrix.sh — emit a matrix.json SKELETON for one live-arena adapter.
#
# Usage:
#   arenas/live/scaffold-matrix.sh <adapter-dir> [--dimensions dim1,dim2,...] [--force]
#
# Writes <adapter-dir>/matrix.json: every dimension the adapter implements,
# with every (lane, form) cell set to "pending" — the starting point for the
# per-adapter fill pass (arenas/live/MATRIX-SCHEMA.md). This script makes NO
# feasibility or correctness claim; it only enumerates dimensions and lays out
# the six-cell grid each one needs.
#
# Dimension discovery:
#   --dimensions dim1,dim2,...   explicit list, used as-is (sorted+deduped)
#   (default)                    auto-discovered from <adapter-dir>/README.md's
#                                 "## Modes" section: table rows shaped
#                                 "| `dim_name` | ... |" between that heading
#                                 and the next "## " heading. See the aisotropy
#                                 adapter's README for the worked convention.
#
# Refuses to overwrite an existing matrix.json unless --force is passed — a
# filled-in matrix is real, evidence-backed work; scaffolding must never
# silently clobber it.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: scaffold-matrix.sh <adapter-dir> [--dimensions dim1,dim2,...] [--force]

Emits <adapter-dir>/matrix.json: every dimension the adapter implements, with
every (lane, form) cell set to "pending". Dimensions come from --dimensions
when given; otherwise they are auto-discovered from the adapter's README.md
"## Modes" table (see arenas/live/MATRIX-SCHEMA.md). Refuses to overwrite an
existing matrix.json unless --force is passed.
EOF
  exit 1
}

[[ $# -ge 1 ]] || usage

adapter_dir=""
dims_arg=""
force=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dimensions)
      [[ $# -ge 2 ]] || usage
      dims_arg="$2"
      shift 2
      ;;
    --force)
      force=1
      shift
      ;;
    -h|--help)
      usage
      ;;
    -*)
      echo "ERROR: unknown flag: $1" >&2
      usage
      ;;
    *)
      if [[ -z "$adapter_dir" ]]; then
        adapter_dir="$1"
        shift
      else
        echo "ERROR: unexpected extra argument: $1" >&2
        usage
      fi
      ;;
  esac
done

[[ -n "$adapter_dir" ]] || usage
[[ -d "$adapter_dir" ]] || { echo "ERROR: not a directory: ${adapter_dir}" >&2; exit 1; }

adapter_dir="$(cd "$adapter_dir" && pwd)"
fw_name="$(basename "$adapter_dir")"
out_file="${adapter_dir}/matrix.json"
readme="${adapter_dir}/README.md"

if [[ -f "$out_file" && "$force" -ne 1 ]]; then
  echo "ERROR: ${out_file} already exists — pass --force to overwrite (this would discard any filled-in statuses)" >&2
  exit 1
fi

dims=()
if [[ -n "$dims_arg" ]]; then
  IFS=',' read -ra dims <<< "$dims_arg"
else
  [[ -f "$readme" ]] || {
    echo "ERROR: no README.md at ${readme} and no --dimensions given — cannot auto-discover dimensions" >&2
    exit 1
  }
  # Extract dimension names from the "## Modes" section's table rows: lines
  # shaped "| `dim_name` | ...". Scoped to stop at the next "## " heading so
  # backtick-quoted names elsewhere in the README (env vars, etc.) are not
  # picked up.
  mapfile -t dims < <(
    awk '/^## Modes/{flag=1; next} /^## /{flag=0} flag' "$readme" \
      | grep -oE '^\| `[a-z0-9_]+`' \
      | sed -E 's/^\| `([a-z0-9_]+)`/\1/' \
      | grep -vxE 'mode'
  )
  [[ ${#dims[@]} -gt 0 ]] || {
    echo "ERROR: no '## Modes' table with \`dim\` rows found in ${readme} — pass --dimensions explicitly" >&2
    exit 1
  }
fi

# sort + dedupe for a deterministic skeleton
mapfile -t dims < <(printf '%s\n' "${dims[@]}" | awk '{gsub(/^[ \t]+|[ \t]+$/, ""); if (NF) print}' | sort -u)

[[ ${#dims[@]} -gt 0 ]] || { echo "ERROR: dimension list is empty after normalization" >&2; exit 1; }

cell='{"native":"pending","docker":"pending","wasm":"pending"}'

{
  echo "{"
  printf '  "framework": "%s",\n' "$fw_name"
  echo '  "dimensions": {'
  n=${#dims[@]}
  for i in "${!dims[@]}"; do
    d="${dims[$i]}"
    comma=","
    [[ $i -eq $((n - 1)) ]] && comma=""
    printf '    "%s": {\n' "$d"
    printf '      "stub": %s,\n' "$cell"
    printf '      "real": %s\n' "$cell"
    printf '    }%s\n' "$comma"
  done
  echo '  }'
  echo "}"
} > "$out_file"

echo "wrote ${out_file} (${#dims[@]} dimension(s): ${dims[*]})" >&2
