#!/usr/bin/env python3
"""Re-render ONLY the full-matrix coverage section of arenas/live/RESULTS.md.

Reads every arenas/live/<fw>/matrix.json and regenerates the
"## Full-matrix coverage" section byte-faithful to what
`writeMatrixSection` (arenas/live/runner/matrix.go) produces, splicing it
in place of the current section. Every other byte of RESULTS.md is left
untouched — so a matrix.json fill can be reflected on the board without
re-running the (billable) runner.

Reproducible evidence (evidencia ejecutable): same inputs -> same output;
stdlib only; verify with `git diff arenas/live/RESULTS.md` (only
matrix-section lines may change).

Usage:
  python3 scripts/render-matrix-section.py [--live DIR] [--results FILE] [--check]

  --check  render and compare against the current section; exit 0 if
           byte-identical, 1 if it differs (nothing is written).
"""
import argparse
import glob
import json
import os
import sys

SECTION_HEADER = "## Full-matrix coverage — `matrix.json` (adapter × dimension × lane × form)"

# Glyphs mirror arenas/live/runner/dimensions.go (sigPass/sigFail/sigUnknown)
SIG_PASS = "✓"
SIG_FAIL = "✗"
SIG_UNKNOWN = "—"
INFEASIBLE_PREFIX = "infeasible:"
# pass:<provenance> — a pass that declares WHERE the capability came from.
# See validMatrixStatus in arenas/live/runner/matrix.go for why it exists.
PASS_PREFIX = "pass:"


def tail(s: str, n: int) -> bytes:
    """Byte-faithful port of main.go's tail(): TrimSpace + newline->space,
    then keep the LAST n BYTES (Go slices bytes, not runes) behind an ellipsis."""
    s = s.replace("\n", " ").strip()
    b = s.encode("utf-8")
    if len(b) <= n:
        return b
    return "…".encode("utf-8") + b[-n:]


def cell_glyph(status: str) -> bytes:
    # Port of matrixCellGlyph (arenas/live/runner/matrix.go).
    if status == "pass":
        return SIG_PASS.encode("utf-8")
    if status == "fail":
        return SIG_FAIL.encode("utf-8")
    if status == "pending":
        return "⧗ pending".encode("utf-8")
    if status.startswith(PASS_PREFIX):
        prov = status[len(PASS_PREFIX):]
        if prov.strip():
            return SIG_PASS.encode("utf-8") + b" " + tail(prov, 50)
    if status.startswith(INFEASIBLE_PREFIX):
        reason = status[len(INFEASIBLE_PREFIX):]
        if reason.strip():
            return "infeasible: ".encode("utf-8") + tail(reason, 50)
    if status == "":
        return SIG_UNKNOWN.encode("utf-8")
    return "invalid:".encode("utf-8") + tail(status, 30)


def render_section(live_dir: str) -> bytes:
    # Port of writeMatrixSection: discovery order = sorted glob, matching
    # loadMatrixDocs' sort.Strings over the manifest paths.
    out = bytearray()
    w = lambda s: out.extend(s.encode("utf-8") if isinstance(s, str) else s)
    w(SECTION_HEADER + "\n\n")
    w("Per `arenas/live/FULL-MATRIX.md`: every (dimension, lane, form) cell is either a\n")
    w("real result or an explicit `infeasible:<reason>` — never a silent blank. Source:\n")
    w("each adapter's own `matrix.json` (schema: `arenas/live/MATRIX-SCHEMA.md`).\n\n")

    for path in sorted(glob.glob(os.path.join(live_dir, "*", "matrix.json"))):
        with open(path, encoding="utf-8") as f:
            doc = json.load(f)
        fw = doc["framework"]
        want = os.path.basename(os.path.dirname(path))
        if fw != want:
            raise SystemExit(f"{path}: framework {fw!r} != directory {want!r}")
        w(f"### {fw}\n\n")
        w("| Dimension | stub/native | stub/docker | stub/wasm | real/native | real/docker | real/wasm |\n")
        w("|---|---|---|---|---|---|---|\n")
        dims = doc.get("dimensions") or {}
        for dim in sorted(dims):  # sortedDimNames: sort.Strings
            ml = dims[dim]
            cells = [ml["stub"]["native"], ml["stub"]["docker"], ml["stub"]["wasm"],
                     ml["real"]["native"], ml["real"]["docker"], ml["real"]["wasm"]]
            w(f"| `{dim}` | ")
            w(b" | ".join(cell_glyph(c) for c in cells))
            w(" |\n")
        if not dims:
            w("_(matrix.json present but declares zero dimensions)_\n")
        w("\n")
    return bytes(out)


def splice(results_path: str, section: bytes, check: bool) -> int:
    with open(results_path, "rb") as f:
        content = f.read()
    header = SECTION_HEADER.encode("utf-8")
    start = content.find(header)
    if start == -1 or (start > 0 and content[start - 1: start] != b"\n"):
        raise SystemExit(f"{results_path}: matrix section header not found at line start")
    nxt = content.find(b"\n## ", start + len(header))
    if nxt == -1:
        raise SystemExit(f"{results_path}: no section after the matrix section — refusing to splice to EOF")
    end = nxt + 1  # keep the newline that precedes the next '## ' heading
    current = content[start:end]
    if current == section:
        print("matrix section already up to date")
        return 0
    if check:
        print("matrix section DIFFERS from matrix.json state")
        return 1
    with open(results_path, "wb") as f:
        f.write(content[:start] + section + content[end:])
    print(f"matrix section re-rendered in {results_path} "
          f"({len(current)} -> {len(section)} bytes)")
    return 0


def main() -> int:
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--live", default=os.path.join(root, "arenas", "live"))
    ap.add_argument("--results", default=os.path.join(root, "arenas", "live", "RESULTS.md"))
    ap.add_argument("--check", action="store_true")
    args = ap.parse_args()
    return splice(args.results, render_section(args.live), args.check)


if __name__ == "__main__":
    sys.exit(main())
