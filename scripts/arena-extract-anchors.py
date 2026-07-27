#!/usr/bin/env python3
"""Extract the anchor-set results from a board's durable record stream.

WHY THIS EXISTS, AND WHY IT COSTS NOTHING

Two boards run on different models cannot be compared: change the model and every
absolute number moves. The fix (arenas/live/tasks/anchors.md) is common-item
equating — run a FIXED subset of items under both models and let the observed
shift on those items estimate the model effect.

The subset is deliberately drawn from the suite the board already runs, so a
completed board ALREADY CONTAINS every anchor cell. Re-running them would be
paying twice for the same measurement. This reads them out of
RESULTS.records.jsonl instead and appends them to ANCHORS.jsonl, keyed by model.

Feed two models' entries to scoring.Equate and it reports the shift, its spread,
the per-framework shifts, and whether the estimate is usable at all — it refuses
on thin evidence, on framework interaction, and on non-overlapping anchor sets.

Usage:
    arena-extract-anchors.py [--records PATH] [--out PATH] [--dry-run]

Exit codes: 0 wrote (or would write) anchors · 1 no anchor cells found · 2 error.
"""

from __future__ import annotations

import argparse
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
LIVE = os.path.join(HERE, "..", "arenas", "live")

# THE FROZEN ANCHOR SET — must match arenas/live/tasks/anchors.md exactly.
#
# Frozen means frozen: scoring.Equate pairs results on (framework, task_id) and
# reports zero paired items when the sets diverge, which is the honest outcome —
# a drifted anchor set measures nothing. Adding items means defining a NEW named
# set, never editing this one, or every prior equating silently becomes garbage.
ANCHORS = [
    # QA — easy and hard tiers, so the estimate is not read off saturated items
    "qa-01-capital", "qa-03-continents", "qa-05-lightspeed",
    "qa-h01-bones", "qa-h03-iron", "qa-h05-boiling",
    # multi-turn memory
    "mem-01-color", "mem-03-deadline", "mem-h01-overwrite",
    "mem-h02-interference", "mem-h04-multifact",
    # tool calling
    "tool-01-weather", "tool-02-calc", "tool-03-search",
    # safeguards — decline + no-leak
    "sg-offtopic-crypto", "sg-jailbreak-persona",
    "sg-identity-model", "sg-identity-vendor",
    # memory extensions
    "xsession-01-empid", "semantic-01-sparekey", "consolidation-01-codename",
    # RAG. h2 and h4 are in ON PURPOSE: every framework failed them on the
    # published agnes board, so they carry information exactly where the easy
    # items saturate and stop discriminating.
    "q1", "q3", "h2", "h4",
]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--records", default=os.path.join(LIVE, "RESULTS.records.jsonl"))
    ap.add_argument("--out", default=os.path.join(LIVE, "ANCHORS.jsonl"))
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    if not os.path.exists(args.records):
        print(f"ERROR: no record stream at {args.records} — run a board first.", file=sys.stderr)
        return 2

    wanted = set(ANCHORS)
    rows, models, missing_by_fw = [], set(), {}

    for line in open(args.records, encoding="utf-8"):
        line = line.strip()
        if not line:
            continue
        try:
            env = json.loads(line)
        except json.JSONDecodeError:
            continue
        if env.get("kind") != "correctness":
            continue
        rec = env.get("record") or {}
        task_id = rec.get("ID")
        if task_id not in wanted:
            continue

        # An errored cell carries no measurement. Recording it as 0.0 would let
        # an infrastructure failure masquerade as a model effect — the exact
        # confusion the quota abort exists to prevent, arriving through the
        # equating instead of through the board.
        if rec.get("RunErr") or (rec.get("Out") or {}).get("error"):
            missing_by_fw.setdefault(rec.get("Framework", "?"), []).append(task_id)
            continue

        det = rec.get("Det") or {}
        if "Value" not in det:
            missing_by_fw.setdefault(rec.get("Framework", "?"), []).append(task_id)
            continue

        model = (rec.get("Out") or {}).get("model") or ""
        models.add(model)
        rows.append({
            "model": model,
            "framework": rec.get("Framework"),
            # Mode is part of the cell identity: the same task id runs in several
            # modes (qa-01-capital in qa, structured AND stream). Dropping it
            # collapses independent measurements and throws away evidence.
            "mode": rec.get("Mode"),
            "task_id": task_id,
            "value": float(det.get("Value") or 0.0),
            "pass": bool(det.get("Pass")),
        })

    if not rows:
        print("ERROR: no anchor cells in the record stream. Either the board ran a "
              "task subset that excludes them, or the anchor ids drifted from the suite.",
              file=sys.stderr)
        return 1

    by_fw: dict[str, int] = {}
    for r in rows:
        by_fw[r["framework"]] = by_fw.get(r["framework"], 0) + 1

    print(f"  anchor cells extracted : {len(rows)}")
    print(f"  models present         : {sorted(models)}")
    print(f"  per framework          : {dict(sorted(by_fw.items()))}")
    print(f"  anchor set size        : {len(ANCHORS)}")
    if missing_by_fw:
        # Loud, never silent: a framework short of anchors contributes fewer
        # paired items, and below scoring.Equate's floor the whole correction is
        # refused. Better to see it here than to wonder later why it refused.
        print("  UNUSABLE cells (errored or unscored), by framework:")
        for fw, ids in sorted(missing_by_fw.items()):
            print(f"    {fw:13s} {len(ids)}: {', '.join(sorted(ids)[:6])}")

    if args.dry_run:
        print("  --dry-run: nothing written")
        return 0

    # APPEND, never overwrite: ANCHORS.jsonl accumulates one baseline per model
    # over time, and that history IS the equating's input. Truncating it would
    # discard the very thing that makes a future provider switch comparable.
    with open(args.out, "a", encoding="utf-8") as fh:
        for r in rows:
            fh.write(json.dumps(r, ensure_ascii=False) + "\n")
    print(f"  appended {len(rows)} row(s) -> {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
