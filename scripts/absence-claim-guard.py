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
import subprocess
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

# ABSENCE fires only near one of these names, so a framework missing from this
# list has its absence claims silently unseen. Five arena frameworks were missing
# on 2026-08-04 — adk-go, letta, cloudflare-agents, dify, eve — which is why the
# ledger showed adk-go 3 entries, letta 7 and cloudflare 2 against eino's 62: not
# because less was claimed about them, but because only their UNIQUENESS-shaped
# sentences ("the only framework that...") were ever visible. "ADK Go exposes NO
# pre-spend quota primitive" matched nothing.
#
# Keep this in step with the arena directories. The check is one line:
#   ls -d arenas/*/ arenas/live/*/ | ... | grep -v -f <(this list)
# A new adapter that lands without an entry here is a new blind spot, and it will
# not announce itself — the guard will simply keep reporting clean about it.
COMPETITORS = [
    "langgraph", "llamaindex", "pydantic-ai", "pydantic ai", "mastra", "ai-sdk",
    "ai sdk", "genkit", "eino", "spring-ai", "spring ai", "langchain", "vercel",
    "adk-go", "adk go", "letta", "cloudflare-agents", "cloudflare agents",
    "durable object", "dify", "eve",
]

# Absence verbs. Kept tight on purpose -- see the module docstring.
#
# THE LEADING \b ON EVERY `no` IS LOad-BEARING, and it was missing. "Eino" ends
# in "no", so `no (?:official|native|first-party)` matched inside "Ei|no native|ly
# supports streaming" and `no \w+ primitive` matched inside "the Ei|no streaming
# primitive| is schema.Pipe". Three sentences saying Eino HAS something were
# filed as claims that it LACKS something — a perfect inversion, and 5 of the 62
# eino ledger entries were pure artifact of it. Any competitor whose name ends in
# those two letters would have hit the same thing.
#
# The `ships?|provides?|offers?|exposes?|carries|includes?` alternative was added
# 2026-08-04 for the opposite failure: a FALSE NEGATIVE. arenas/pydantic-ai/
# FEATURES.md said Pydantic AI "ships no memory or multi-tenancy layer" — wrong
# (our own arena-memory-extensions.md marks its cross-session memory an
# OFFICIAL-EXTENSION with three vendor URLs) and invisible, because this pattern
# knew "has no" and "have no" but not "ships no". Measured before landing: 26
# real competitor claims currently unseen, against one or two false positives.
# A claim this guard cannot see is worse than one it reports noisily.
ABSENCE = re.compile(
    r"(\bhas no\b|\bhave no\b|\blacks?\b|\bno \w+ primitive\b|\bcannot\b|\bcan't\b"
    r"|\bdoes not (?:have|offer|ship|support)|\bdo not (?:have|offer|ship|support)"
    r"|\bis not (?:able|capable)|\bno (?:official|native|first-party)\b"
    r"|\b(?:ships?|provides?|offers?|exposes?|carries|includes?)\s+no\b)",
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
    #
    # KNOWN FALSE POSITIVE, MEASURED AND KEPT ON PURPOSE. This alternative also
    # matches "so no framework is advantaged or disadvantaged by a persona"
    # (docs/architecture/arena-safeguard-dimension.md) — a statement about arena
    # FAIRNESS, not about any competitor lacking anything. It could be excluded
    # by requiring a capability verb after the noun, and that was measured and
    # rejected: this alternative is the ONLY thing catching 68 real claims
    # ("Pydantic AI ... a stateless lib with no memory tier", ADK's "no
    # framework-enforced separator", "Genkit Go ships no framework pre-spend
    # gate"), and a verb allow-list narrow enough to drop the fairness sentence
    # would drop some of those too. One false positive per sixty-eight findings
    # is a rate this guard can carry; losing real competitor claims to improve
    # it is optimising the measurement instead of the thing measured.
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
    return sorted(drop_git_ignored(out))


# Sentences this guard MUST see, and sentences it MUST NOT. Every entry is a
# real defect it once had, kept as a fixture so the pattern edits that fixed them
# cannot be undone silently.
#
# WHY THIS EXISTS AT ALL. On 2026-08-04 three separate blind spots were found in
# one afternoon — a missing word boundary, three missing absence verbs, and five
# arena frameworks absent from COMPETITORS. Not one was found by this guard. All
# three surfaced because people read the ecosystem sweeps against the PROSE
# instead of against the ledger, and then noticed the ledger disagreed. That is
# the structural problem these fixtures only partly close: a guard's own output
# can never reveal what its patterns cannot match, so every count it has ever
# printed was a count of what it could see. Fixtures pin the failures we KNOW
# about. They cannot find the fourth blind spot — only reading can.
DETECT_FIXTURES = [
    # (sentence, must_be_detected, why)
    ("Eino has no tenant dimension", True, "plain absence + competitor"),
    ("Pydantic AI ships no memory or multi-tenancy layer", True,
     "'ships no' — the verb gap that hid a wrong claim until 2026-08-04"),
    ("ADK Go exposes NO pre-spend quota primitive", True,
     "'exposes no' + adk-go, which was missing from COMPETITORS"),
    ("Letta has no framework binding between an authenticated caller and a tenant", True,
     "letta was missing from COMPETITORS"),
    ("no competing Go agent framework builds it", True,
     "the front-page uniqueness construction that shipped unseen"),
    ("one rival leaked across tenants when we reproduced it", False,
     "DEFECT needs a competitor name nearby; unnamed prose is our own"),
    ("Eino natively supports streaming via schema.StreamReader", False,
     "'Ei|no native|ly' — the missing word boundary inverted this into an absence claim"),
    ("the Eino streaming primitive is schema.Pipe", False,
     "'Ei|no streaming primitive|' — same inversion"),
    ('{"framework":"ai-sdk","model":"x","reply":"I have no information about your account."}', False,
     "captured run record: the absence language is the MODEL's answer, and a passing one"),
    # Pinned as True on purpose. This sentence is about arena FAIRNESS, not about
    # any competitor lacking anything — a genuine false positive, and one that was
    # measured and ACCEPTED rather than fixed: the `no <modifiers> framework`
    # alternative that catches it is the only thing catching 68 real claims, and
    # every narrowing tight enough to drop it dropped some of those too. Encoding
    # the decision rather than the wish, so that a future narrowing flips this
    # fixture and has to argue with the trade-off instead of silently taking it.
    ("so no framework is advantaged or disadvantaged by a persona", True,
     "accepted false positive: 1 per 68 real findings, see the UNIQUENESS comment"),
]


def self_check() -> list[str]:
    """Fail closed on the two blind-spot classes that CAN be mechanised.

    Returns a list of problems; empty means the guard is fit to run. Callers
    treat a non-empty result as exit 2 (cannot evaluate), never as a pass — a
    guard that cannot vouch for its own coverage must not be read as absolution.
    """
    problems = []

    for sentence, want, why in DETECT_FIXTURES:
        if is_competitor_claim(sentence) != want:
            verb = "missed" if want else "false-positived on"
            problems.append(f"pattern regression: {verb} {sentence!r} ({why})")

    # Every arena adapter directory needs a COMPETITORS entry, or its absence
    # claims are invisible. This is exactly how adk-go, letta, cloudflare-agents,
    # dify and eve went unseen: the ledger read 3, 7 and 2 entries for the first
    # three, not because less was claimed about them but because ABSENCE only
    # fires near a name it knows.
    not_frameworks = {"live", "runner", "_shared", "aisotropy", "key-brokering",
                      "flue", "models", "scripts", "tasks", "docker", "orbit"}
    seen = set()
    for root in (os.path.join(REPO, "arenas"), os.path.join(REPO, "arenas", "live")):
        if not os.path.isdir(root):
            continue
        for d in os.listdir(root):
            if d in not_frameworks or d.startswith(".") or not os.path.isdir(os.path.join(root, d)):
                continue
            seen.add(d)
    known = [c.replace(" ", "-") for c in COMPETITORS]
    for d in sorted(seen):
        if not any(k in d or d in k for k in known):
            problems.append(
                f"coverage gap: arenas/{d}/ is an adapter with no COMPETITORS entry — "
                f"every absence claim about it is invisible to this guard"
            )
    return problems


def drop_git_ignored(paths: list[str]) -> list[str]:
    """Remove paths git is ignoring — build output, virtualenvs, vendored bundles.

    This guard walks the FILESYSTEM, not the index, which is right: a claim
    written five minutes ago and not yet committed is exactly what a pre-push
    gate must see. But the same walk also reads things no clone will ever
    contain. Both have now been paid for: 20 baseline entries once pointed into
    an untracked .venv, and `arenas/eve/.output/server/_libs/eve.mjs` — a
    bundled third-party build artifact, ignored by arenas/eve/.gitignore — was
    contributing "claims" nobody wrote.

    The distinction that matters is IGNORED vs merely UNTRACKED, and only git
    knows it, so ask git rather than growing a hardcoded directory list forever
    (SKIP_DIRS stays as the offline fallback). `git check-ignore` exits 1 when
    nothing matched, which is the normal clean case, not an error.
    """
    if not paths:
        return paths
    try:
        proc = subprocess.run(
            ["git", "-C", REPO, "check-ignore", "--stdin"],
            input="\n".join(paths), capture_output=True, text=True, timeout=30,
        )
    except (OSError, subprocess.SubprocessError):
        return paths  # no git available: fall back to SKIP_DIRS alone
    if proc.returncode not in (0, 1):
        return paths
    ignored = {os.path.join(REPO, p) if not os.path.isabs(p) else p
               for p in proc.stdout.splitlines() if p.strip()}
    return [p for p in paths if os.path.abspath(p) not in ignored]


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

    # Before reporting on anybody else's claims, check our own instrument. Runs
    # on every invocation rather than behind a flag: a self-check nobody
    # remembers to pass is the same shape as the ecosystem sweep this guard
    # exists to enforce — a rule that was right and that nothing ran.
    problems = self_check()
    if problems:
        print("SELF-CHECK FAILED — this guard cannot vouch for its own coverage,", file=sys.stderr)
        print("so its verdict is NOT a pass. Fix these before trusting any count:\n", file=sys.stderr)
        for p in problems:
            print(f"  · {p}", file=sys.stderr)
        return 2

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
