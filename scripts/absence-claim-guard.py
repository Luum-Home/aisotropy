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
    # `arenas`, not `arenas/live`: the per-framework directories (arenas/eino,
    # arenas/genkit-go, arenas/adk-go, ...) hold FEATURES.md / DX-NOTES.md /
    # README.md, which is where the densest competitor claims actually live. The
    # original scope missed 13 such directories — the guard was reporting a debt
    # of 176 against a real population closer to 280, so its own ledger
    # understated the thing it exists to count.
    "arenas",
    "docs/architecture",
    "README.md",
]
# `.venv` / `site-packages` matter here for a reason worth stating: this guard
# walks the FILESYSTEM, not the git index, so an untracked virtualenv sitting
# inside a scanned directory gets read like authored prose. That already
# happened — 20 baseline entries pointed into a .venv that no clone will ever
# contain. Vendored/installed third-party text is not an authored claim.
SKIP_DIRS = {
    "node_modules", ".git", "vendor", "__pycache__", "dist", "build", "target",
    ".venv", "venv", "site-packages", ".pytest_cache", ".mypy_cache", "testdata",
}
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
#
# The `no <modifiers> <noun>` alternative was added on 2026-08-04 after the
# repo's own front page slipped past every pattern here. README.md said "no
# competing Go agent framework builds it" — a claim about EVERY competitor, and
# the exact sentence this guard exists to stop — and matched nothing: the list
# below had "no other framework" but not "no <adjective> framework". A guard
# that only recognises one word order is matching a phrasing, not a claim.
UNIQUENESS = re.compile(
    r"(no other framework|nobody else|none of the (?:other|competing)|only (?:we|aisotropy)\b"
    r"|the only framework|unique to (?:us|aisotropy)"
    # Two deliberate narrowings, both paid for by false positives on the first
    # draft of this line (measured 2026-08-04, before/after counts in the commit):
    #   · the filler words may not be articles/prepositions. "no LLM credential
    #     THE runtime falls back to a keyless stub" is a sentence about OUR OWN
    #     runtime, and it matched when any 4 words were allowed through.
    #   · the head noun list excludes "runtime"/"engine". Both are words this
    #     repo uses constantly about itself ("no arena runtimes needed"), so
    #     they cost more precision than the recall they buy. "framework" and
    #     "library" are what a competitor claim actually reaches for.
    r"|\bno (?:(?!the\b|a\b|an\b|of\b|for\b|to\b|in\b|on\b|with\b|and\b|or\b)\w+[-\s]){0,3}"
    r"(?:framework|library|sdk|competitor|rival|implementation)s?\b)",
    re.I,
)

# Attribution of a DEFECT to somebody else's product. This is a third class, and
# it is the one that actually shipped: "one rival leaked across tenants" is not
# an absence claim ("X lacks Y") and not a uniqueness claim ("only we do Y"), so
# neither pattern above looked at it — while being the most exposed sentence in
# the repository, on the front page, naming a competitor, asserting a data-safety
# failure. Saying somebody else's product LEAKS is a heavier claim than saying it
# lacks a feature, and it was the one class with no guard at all.
#
# Gated on competitor proximity like ABSENCE: a defect word with nobody attached
# is our own prose about our own tests (the isolation_leak dimension is named
# after this exact vocabulary), not a claim about anyone.
DEFECT = re.compile(
    r"\b(leaks?|leaked|leaking|leaky|vulnerab\w+|insecure|unsafe|exploitable"
    r"|silently (?:drops|loses|returns|ignores))\b",
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
    # Neither is a serialized RUN RECORD. The adapter READMEs embed captured
    # arena output as evidence — {"framework":…,"mode":…,"model":…,"reply":…} in
    # a ```json block — and the absence language inside is the MODEL's answer,
    # not a sentence anybody wrote about a competitor. On the isolation_leak
    # records it is the DESIRED answer: "I have no information about your
    # account" is the model correctly refusing to leak, i.e. evidence the
    # framework passed. Flagging it as "you claim a rival lacks something"
    # inverts what the line is.
    #
    # Discriminated on the `"reply"` key, deliberately, because it is the
    # narrowest thing that separates a captured record from the OTHER JSON this
    # guard must keep reading: matrix.json's `"native": "infeasible:<reason>"`
    # entries ARE authored competitor claims (the no-blank-cells law requires a
    # written reason) and carry no reply field. Measured when this was added:
    # exactly 9 entries left the ledger, all of them run records.
    if '"reply"' in line and line.lstrip().startswith(("{", "[", '"')):
        return False
    low = line.lower()
    if UNIQUENESS.search(line):
        return True
    if not (ABSENCE.search(line) or DEFECT.search(line)):
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
            # Only UNSWEPT claims belong in the ledger. Appending here rather
            # than before the `swept is None` test is a fix, not a style change:
            # --write-baseline used to record every claim it saw, so a claim that
            # already carried a current SWEPT: marker was written into a file
            # whose own header calls it "claims ... WITHOUT a recorded ecosystem
            # sweep". That overstated the debt, and it broke the one documented
            # way to work the debt down — "annotate the claim, then delete its
            # line here" — because for those entries the line was never load-
            # bearing. Caught on 2026-08-04 when a sentence swept minutes earlier
            # showed up in the regenerated ledger.
            if swept is None:
                all_keys.append((key, f"{rel}:{i+1}"))
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
