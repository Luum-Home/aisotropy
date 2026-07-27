#!/usr/bin/env python3
"""Report where a matrix manifest and the board that just ran DISAGREE.

WHY A REPORT AND NOT AN AUTO-SYNC

The obvious version of this script rewrites each matrix.json cell from the board
result. That would be wrong in a way worth stating: the manifest exists to
declare what coverage the arena claims, and the completeness gate checks the
board against it. A manifest that rewrites itself from the results it is meant to
check always agrees with them, and a gate that always passes is not a gate — the
same defect as a guard that green-lights what it could not analyse.

So the disagreement IS the deliverable. A human decides which side is wrong,
because both directions happen and they mean opposite things:

  declared `fail`, observed pass    a real fix landed and the ledger is stale
                                    (spring-ai/rag after the seeding fix)
  declared `pass`, observed fail    a regression, or a board run under degraded
                                    conditions — never silently accept either
  declared `pending`, observed *    the cell finally ran; declare what it did

Usage: arena-manifest-drift.py RECORDS_JSONL LIVE_DIR
Exit codes: 0 no drift · 1 drift reported · 2 error.
"""

from __future__ import annotations

import collections
import json
import os
import sys


def observed_verdict(counts: dict) -> str:
    """Collapse a mode's cells into one verdict.

    `error-only` is deliberately distinct from `fail`: a mode whose every cell
    errored produced no measurement at all, and calling that a failure would
    attribute an infrastructure problem to the framework — the single mistake
    this whole arena has spent the most effort not making.
    """
    if counts["pass"] == 0 and counts["fail"] == 0:
        return "error-only"
    return "pass" if counts["pass"] > counts["fail"] else "fail"


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__, file=sys.stderr)
        return 2
    records, live = sys.argv[1], sys.argv[2]
    if not os.path.exists(records):
        print(f"ERROR: no record stream at {records}", file=sys.stderr)
        return 2

    scored: dict = collections.defaultdict(lambda: {"pass": 0, "fail": 0, "err": 0})
    for line in open(records, encoding="utf-8"):
        line = line.strip()
        if not line:
            continue
        try:
            env = json.loads(line)
        except json.JSONDecodeError:
            continue
        if env.get("kind") != "correctness":
            continue
        r = env.get("record") or {}
        key = (r.get("Framework"), r.get("Mode"))
        if r.get("RunErr") or (r.get("Out") or {}).get("error"):
            scored[key]["err"] += 1
        elif (r.get("Det") or {}).get("Pass"):
            scored[key]["pass"] += 1
        else:
            scored[key]["fail"] += 1

    drift = 0
    for name in sorted(os.listdir(live)):
        path = os.path.join(live, name, "matrix.json")
        if not os.path.exists(path):
            continue
        try:
            doc = json.load(open(path, encoding="utf-8"))
        except json.JSONDecodeError as exc:
            print(f"   ERROR    {name}: unparseable matrix.json ({exc})")
            drift += 1
            continue

        fw = doc.get("framework")
        for dim, lanes in (doc.get("dimensions") or {}).items():
            declared = ((lanes or {}).get("real") or {}).get("native")
            # An infeasible cell is a declaration ABOUT THE FRAMEWORK, not a
            # result, and the runner never builds work for it — so there is
            # nothing to compare and silence is the correct output.
            if not declared or str(declared).startswith("infeasible:"):
                continue
            counts = scored.get((fw, dim))
            if not counts:
                continue  # the board did not run this dimension
            obs = observed_verdict(counts)
            tally = f"pass={counts['pass']} fail={counts['fail']} err={counts['err']}"

            if declared == "pending":
                print(f"   PENDING -> {obs:11s} {fw:13s} {dim:22s} {tally}  — the cell ran; declare it")
                drift += 1
            elif declared == "fail" and obs == "error-only":
                # Not drift: both mean "this produced nothing usable". Distinguishing
                # them further would be noise.
                continue
            elif declared != obs:
                print(f"   {declared:7s} vs {obs:11s} {fw:13s} {dim:22s} {tally}")
                drift += 1

    if drift == 0:
        print("   ok       every real/native declaration matches what the board observed")
        return 0
    print(f"   {drift} disagreement(s) — resolve them deliberately; this script will not sync them.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
