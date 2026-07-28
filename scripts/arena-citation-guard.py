#!/usr/bin/env python3
"""Verify that every dependency source path an arena adapter CITES actually
exists in the version that adapter pins.

WHY THIS EXISTS (the miss it generalizes)
-----------------------------------------
genkit-go's adapter and README justified a competitive claim -- "Genkit Go has
no user-scoped memory primitive" -- by citing `core/x/session`. That path does
not exist in genkit go@v1.10.0, the version the adapter pins. The conclusion was
still true (the real surface, ai/exp/session.go, is session-keyed too), but the
evidence for it had rotted: a dependency bump moved the code and the citation
kept pointing at a path that had ceased to exist.

That is how a once-true claim quietly becomes an unfounded one. Nobody edited
the sentence; the world moved underneath it. And because the sentence still
READS like sourced evidence, it is more dangerous than an unsourced claim -- a
reviewer checks that a citation is present, not that it still resolves.

`.claude/rules/capability-absence-claims.md` requires a URL or file:line per
finding for any "framework X lacks Y" claim. This script enforces the half a
human reviewer cannot hold in their head: that the file:line still resolves in
the pinned version.

WHAT IT CHECKS
--------------
For each arena adapter with a go.mod, every `pkg/path/file.go`-shaped citation
appearing in its .go/.md files is resolved against:
  * the adapter's own tree,
  * this repo, including repo-relative shorthand roots (`runner/tasks.go` means
    `arenas/live/runner/tasks.go`),
  * the module-cache dir of every module pinned by ANY arena adapter -- the
    union, not just the citing adapter's own go.mod, because the citations most
    worth verifying are cross-framework ones (our adapter citing a genkit path
    while pinning no genkit dependency).
A citation that resolves in NONE of those is reported. Citations into
deliberately unpinned third-party repos are listed separately with the repo
they belong to (UNPINNED_THIRD_PARTY), never silently dropped.

Deliberately NOT checked: Python/Node/Java adapters. Their dependency layouts
(site-packages, node_modules, jars) are resolvable too, but the Go module cache
is the only one with a stable, version-pinned on-disk path this script can
trust without a network call. Extending it is worthwhile; claiming it already
covers them would be the exact defect this script exists to catch.

EXIT CODES (executable-evidence convention)
  0  no rotted citations
  1  rotted citation(s) found
  2  error (cannot evaluate -- never reported as a pass)
"""
from __future__ import annotations

import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LIVE = os.path.join(REPO, "arenas", "live")

# A citation looks like `some/dir/file.go`. Two precision rules, both learned
# from this guard's own first run, which reported 14 hits of which most were its
# own bugs:
#
#   (?<![\w./-])  the match may not START INSIDE a longer path. Without it,
#                 "github.com/cloudwego/eino/callbacks/interface.go" matched
#                 from "com/..." (the regex cannot begin at "github." because of
#                 the dot) and was then reported as a nonexistent path. A guard
#                 whose findings are mostly its own parsing errors is worse than
#                 no guard: it teaches the reader to skim past real ones.
#
#   REPO_SHORTHAND  a doc citing "runner/tasks.go" means
#                   "arenas/live/runner/tasks.go". Resolving those roots before
#                   declaring a miss is the difference between "this path is
#                   gone" and "I did not look where it lives".
CITATION = re.compile(r"(?<![\w./-])([a-z0-9_]+(?:/[a-z0-9_.-]+)+\.go)\b")

# Roots a repo-relative shorthand citation may be written against.
REPO_SHORTHAND = ("", "arenas/live", "arenas")

# Paths that are documentation shorthand rather than a claim about a dependency.
IGNORE_PREFIXES = ("cmd/", "crates/", "docs/", "scripts/", "test/", "internal/", "aisotropy/")

# Citations into repos that are deliberately NOT pinned -- typically a
# third-party alternative the adapter names in order to explain why it was NOT
# adopted. The guard cannot resolve these (they are not in go.mod, by design),
# and reporting them forever would be the crying-wolf failure this guard exists
# to avoid. Each entry carries the repo it belongs to, so an allowlist entry is
# a statement a reviewer can check rather than a silencer.
UNPINNED_THIRD_PARTY = {
    # genkit-go/qdrant_client.go explains why the third-party Qdrant plugin is
    # not used (maintenance risk + it requires an ai.Embedder). The path is real
    # in qdrant/qdrant-genkit; it is simply not a dependency of this adapter.
    "go/qdrant/qdrant.go": "qdrant/qdrant-genkit (third-party, deliberately not adopted)",
}


def module_dirs(adapter: str) -> dict[str, str]:
    """Map module path -> on-disk dir for every dependency the adapter pins."""
    try:
        out = subprocess.run(
            ["go", "list", "-m", "-f", "{{.Path}}\t{{.Dir}}", "all"],
            cwd=adapter, capture_output=True, text=True, timeout=180,
        )
    except (OSError, subprocess.TimeoutExpired) as e:
        raise RuntimeError(f"go list failed in {adapter}: {e}") from e
    if out.returncode != 0:
        raise RuntimeError(f"go list exit {out.returncode} in {adapter}: {out.stderr.strip()[:200]}")
    dirs = {}
    for line in out.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) == 2 and parts[1].strip():
            dirs[parts[0]] = parts[1]
    return dirs


def resolves(cit: str, adapter: str, deps: dict[str, str]) -> bool:
    """deps is the UNION of every arena adapter's pinned modules, not just the
    citing adapter's. That union is deliberate: the citations most worth
    verifying are CROSS-FRAMEWORK ones -- our own adapter explaining how
    Genkit-Go's tracing differs from ours cites a genkit path while pinning no
    genkit dependency. Resolving only against the citing adapter's own go.mod
    would report every such comparison as rot, which would push the guard toward
    the one thing it must never do: make the reader stop believing it."""
    # The adapter's own tree, then repo-relative shorthand roots, then every
    # pinned dependency's module-cache dir.
    if os.path.exists(os.path.join(adapter, cit)):
        return True
    for root in REPO_SHORTHAND:
        if os.path.exists(os.path.join(REPO, root, cit)):
            return True
    if any(os.path.exists(os.path.join(d, cit)) for d in deps.values()):
        return True
    # A citation may name a module-qualified path ("github.com/x/y/pkg/f.go"):
    # strip the module prefix that matches a pinned dependency and retry inside
    # that module's dir, which is where the file actually lives on disk.
    for mod, d in deps.items():
        if cit.startswith(mod + "/") and os.path.exists(os.path.join(d, cit[len(mod) + 1:])):
            return True
    return False


def main() -> int:
    if not os.path.isdir(LIVE):
        print(f"error: {LIVE} not found", file=sys.stderr)
        return 2

    adapters = sorted(
        os.path.join(LIVE, d) for d in os.listdir(LIVE)
        if os.path.isfile(os.path.join(LIVE, d, "go.mod"))
    )
    if not adapters:
        print("error: no Go adapters found — the guard could not evaluate anything, "
              "which is absence of evidence, not evidence of absence", file=sys.stderr)
        return 2

    # Union of every adapter's pinned modules — see resolves() for why.
    all_deps: dict[str, str] = {}
    for adapter in adapters:
        try:
            all_deps.update(module_dirs(adapter))
        except RuntimeError as e:
            print(f"error: {os.path.basename(adapter)}: {e}", file=sys.stderr)
            return 2

    rotted = 0
    for adapter in adapters:
        name = os.path.basename(adapter)
        deps = all_deps

        seen: dict[str, set[str]] = {}
        for root, dirnames, filenames in os.walk(adapter):
            dirnames[:] = [d for d in dirnames if d not in (".git", "node_modules", "vendor")]
            for fn in filenames:
                if not fn.endswith((".go", ".md")):
                    continue
                path = os.path.join(root, fn)
                try:
                    text = open(path, encoding="utf-8", errors="replace").read()
                except OSError:
                    continue
                for cit in CITATION.findall(text):
                    if cit.startswith(IGNORE_PREFIXES):
                        continue
                    seen.setdefault(cit, set()).add(os.path.relpath(path, REPO))

        for cit in sorted(seen):
            if cit in UNPINNED_THIRD_PARTY:
                print(f"  unpinned {name:12s} {cit}  — {UNPINNED_THIRD_PARTY[cit]}")
                continue
            if not resolves(cit, adapter, deps):
                rotted += 1
                print(f"  ROTTED  {name:12s} {cit}")
                for src in sorted(seen[cit]):
                    print(f"            cited by {src}")

    print()
    if rotted:
        print(f"VERDICT: {rotted} rotted citation(s) — each names a path that does not exist")
        print("in the version its adapter pins. Re-verify the claim against the real")
        print("surface, then update the citation. A claim whose evidence no longer")
        print("resolves is unfounded even when its conclusion happens to still hold.")
        return 1
    print(f"VERDICT: clean — every cited dependency path resolves in the pinned version "
          f"({len(adapters)} Go adapter(s) checked).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
