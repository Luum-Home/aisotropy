#!/usr/bin/env python3
"""Every claim that a COMPETITOR lacks a capability must carry a DATED sweep.

WHY THIS EXISTS
---------------
`arenas/live/aisotropy/main.go` said, for months, that executing a tool inside a
WASM sandbox was "the capability no other framework here has". The 2026-07-28
ecosystem sweep -- the first one ever run against that sentence -- found it false
twice over: pydantic/mcp-run-python and langchain-ai/langchain-sandbox had both
shipped WASM-sandboxed tool execution wired into their agent loops, and six of the
eight competitors sandbox tool execution today by non-WASM means.

`.claude/rules/capability-absence-claims.md` already REQUIRED a five-point
ecosystem sweep before such a sentence ships. The rule was right and nothing
enforced it, so the sentence shipped anyway.

THE DECAY HALF, WHICH IS THE POINT
----------------------------------
Both archivals happened in January 2026 -- AFTER the claim was written. So the
sentence may well have been defensible the day it was typed and then rotted,
exactly like the `core/x/session` citation that scripts/arena-citation-guard.py
now catches. Two different symptoms, one disease: EVIDENCE DECAYS AND NOTHING
ANNOUNCES IT. A competitor ships, archives, or pivots, and a true sentence in our
repo silently becomes a false one about someone else's product.

So this guard checks two things, and the second is the one that would have caught
the wasm_tool claim:
  1. the claim carries a sweep marker at all;
  2. that sweep is not older than MAX_SWEEP_AGE_DAYS.

THE CONVENTION
--------------
Annotate the claim, on the line or within PROXIMITY_LINES of it:

    SWEPT: 2026-07-28 docs/architecture/arena-wasm-tool-dimension.md

The date is when the ecosystem was actually checked. The path points at where the
per-point findings, maturity labels and last-release dates live.

DELIBERATELY NARROW. It fires only where absence language sits near a COMPETITOR
NAME or an explicit "no other framework"/"only we" construction. Statements about
our own internals ("the only storage path that exists") are not competitor claims
and are not flagged -- a guard that reports 95 lines teaches the reader to skim
past the 3 that matter, which is how the wasm_tool sentence survived in plain
sight.

EXIT CODES (executable-evidence convention)
  0  every competitor-absence claim carries a current sweep
  1  unswept or stale claim(s) found
  2  error (cannot evaluate -- never reported as a pass)
"""
from __future__ import annotations

import datetime as dt
import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Where comparative claims are made. Not the whole repo: a claim in a scratch
# note is not a published claim.
SCAN = [
    "arenas/live",
    "docs/architecture",
    "README.md",
]
SKIP_DIRS = {"node_modules", ".git", "vendor", "__pycache__", "dist", "build", "target"}
SCAN_EXT = (".go", ".md", ".py", ".ts", ".mjs", ".java", ".json")

# GENERATED artifacts are not authored claims. RESULTS.md is rendered from the
# matrix manifests and the run records: every absence sentence in it traces back
# to a matrix.json reason or an adapter comment, which ARE scanned. Flagging the
# render too would trade one real finding for a hundred duplicates of it -- the
# precision failure that let the wasm_tool claim hide in plain sight.
SKIP_FILES = {"RESULTS.md", "RESULTS.records.jsonl"}

COMPETITORS = [
    "langgraph", "llamaindex", "pydantic-ai", "pydantic ai", "mastra", "ai-sdk",
    "ai sdk", "genkit", "eino", "spring-ai", "spring ai", "langchain", "vercel",
]

# Absence verbs. Kept tight on purpose -- see the module docstring.
ABSENCE = re.compile(
    r"(has no\b|have no\b|lacks?\b|no \w+ primitive|cannot\b|can't\b|does not (?:have|offer|ship|support)"
    r"|do not (?:have|offer|ship|support)|is not (?:able|capable)|no (?:official|native|first-party))",
    re.I,
)
# Explicit uniqueness constructions naming no competitor but claiming all of them.
UNIQUENESS = re.compile(
    r"(no other framework|nobody else|none of the (?:other|competing)|only (?:we|aisotropy)\b"
    r"|the only framework|unique to (?:us|aisotropy))",
    re.I,
)
SWEEP = re.compile(r"SWEPT:\s*(\d{4}-\d{2}-\d{2})")

PROXIMITY_LINES = 12
MAX_SWEEP_AGE_DAYS = 180

# The repo already contained ~177 competitor-absence assertions when this guard
# was written, none carrying a sweep. A gate that starts at 177 failures is a
# gate nobody runs, so the pre-existing set is recorded in a BASELINE file and
# the guard fails only on claims that are NEW or STALE.
#
# This is a ledger, not a silencer, and the distinction is load-bearing: the
# baseline file lists every grandfathered claim by file:line and excerpt, so the
# debt is countable and reviewable instead of invisible. Working it down means
# running the sweep and deleting lines from it. A claim that MOVES re-appears as
# new, which is correct -- editing the sentence is the moment to re-check it.
BASELINE = os.path.join(REPO, "arenas", "live", "absence-claims-baseline.txt")


def today() -> dt.date:
    # Injectable so the guard itself is testable and deterministic in CI.
    override = os.environ.get("ABSENCE_GUARD_TODAY")
    if override:
        return dt.date.fromisoformat(override)
    return dt.date.today()


def files() -> list[str]:
    out = []
    for root_rel in SCAN:
        root = os.path.join(REPO, root_rel)
        if os.path.isfile(root):
            out.append(root)
            continue
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
            out.extend(
                os.path.join(dirpath, fn) for fn in filenames
                if fn.endswith(SCAN_EXT) and fn not in SKIP_FILES
                and (not fn.endswith(".json") or fn == "matrix.json")
            )
    return sorted(out)


def is_competitor_claim(line: str) -> bool:
    # A rendered table row is output, not an authored sentence.
    if line.lstrip().startswith("|"):
        return False
    low = line.lower()
    if UNIQUENESS.search(line):
        return True
    if not ABSENCE.search(line):
        return False
    return any(c in low for c in COMPETITORS)


def nearest_sweep(lines: list[str], idx: int) -> str | None:
    lo = max(0, idx - PROXIMITY_LINES)
    hi = min(len(lines), idx + PROXIMITY_LINES + 1)
    for j in range(lo, hi):
        m = SWEEP.search(lines[j])
        if m:
            return m.group(1)
    return None


def load_baseline() -> set[str]:
    if not os.path.exists(BASELINE):
        return set()
    out = set()
    for line in open(BASELINE, encoding="utf-8"):
        line = line.strip()
        if line and not line.startswith("#"):
            out.add(line.split("\t")[0])
    return out


def claim_key(rel: str, line: str) -> str:
    """Identity of a claim: file + normalized text, NOT the line number. A claim
    that merely shifts down when something is inserted above it is the same
    claim; a claim whose WORDS change is a new one that must be re-swept."""
    return f"{rel}::{' '.join(line.split())[:160]}"


def main() -> int:
    write_baseline = "--write-baseline" in sys.argv
    paths = files()
    if not paths:
        print("error: nothing to scan — the guard could not evaluate anything, "
              "which is absence of evidence, not evidence of absence", file=sys.stderr)
        return 2

    baseline = load_baseline()
    unswept: list[str] = []
    stale: list[str] = []
    grandfathered: list[str] = []
    all_keys: list[tuple[str, str]] = []
    ok = 0
    now = today()

    for path in paths:
        try:
            lines = open(path, encoding="utf-8", errors="replace").read().splitlines()
        except OSError:
            continue
        for i, line in enumerate(lines):
            if not is_competitor_claim(line):
                continue
            rel = os.path.relpath(path, REPO)
            swept = nearest_sweep(lines, i)
            excerpt = line.strip()[:110]
            key = claim_key(rel, line)
            all_keys.append((key, f"{rel}:{i+1}"))
            if swept is None:
                if key in baseline and not write_baseline:
                    grandfathered.append(f"  {rel}:{i+1}")
                    continue
                unswept.append(f"  UNSWEPT  {rel}:{i+1}\n             {excerpt}")
                continue
            try:
                age = (now - dt.date.fromisoformat(swept)).days
            except ValueError:
                unswept.append(f"  BAD DATE {rel}:{i+1}  SWEPT: {swept!r} is not YYYY-MM-DD")
                continue
            if age > MAX_SWEEP_AGE_DAYS:
                stale.append(f"  STALE    {rel}:{i+1}  swept {swept} ({age}d ago, max {MAX_SWEEP_AGE_DAYS})\n             {excerpt}")
            else:
                ok += 1

    if write_baseline:
        with open(BASELINE, "w", encoding="utf-8") as f:
            f.write("# Competitor-absence claims present when absence-claim-guard.py was\n")
            f.write("# introduced (%s). Each asserts that somebody else's product lacks\n" % now.isoformat())
            f.write("# something, WITHOUT a recorded ecosystem sweep. This is a DEBT LEDGER:\n")
            f.write("# run the five-point sweep, annotate the claim with SWEPT: YYYY-MM-DD\n")
            f.write("# <findings-doc>, then delete its line here. Editing a claim's wording\n")
            f.write("# makes it NEW again, which is the right moment to re-check it.\n")
            for key, loc in sorted(set(all_keys)):
                f.write(f"{key}\t{loc}\n")
        print(f"baseline written: {len(set(all_keys))} claim(s) -> {os.path.relpath(BASELINE, REPO)}")
        return 0

    for line in unswept:
        print(line)
    for line in stale:
        print(line)

    print()
    total = len(unswept) + len(stale)
    if total:
        print(f"VERDICT: {len(unswept)} unswept + {len(stale)} stale competitor-absence claim(s).")
        print("Each asserts that somebody else's product lacks something. Run the five-point")
        print("sweep in .claude/rules/capability-absence-claims.md (core, official extensions,")
        print("platform, official third-party integrations, vendor docs), record the findings")
        print("with maturity labels and last-release dates, then annotate the claim:")
        print()
        print("    SWEPT: YYYY-MM-DD docs/architecture/<where-the-findings-live>.md")
        print()
        print("A stale sweep is not a technicality: the wasm_tool claim this guard exists for")
        print("was plausibly true when written and was falsified by two archivals that")
        print("happened months later, without a line of our code changing.")
        return 1
    print(f"VERDICT: clean — {ok} swept claim(s) current, "
          f"{len(grandfathered)} grandfathered in {os.path.relpath(BASELINE, REPO)} "
          f"(a countable debt, not a pass: sweep them and delete their lines).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
