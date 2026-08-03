---
type: Runbook
title: "Purging a rewritten history from GitHub — before publication, not after"
description: A force-push does not delete anything on GitHub. Unreachable objects from a history rewrite stay in the remote's object store and are served by SHA to anyone who can read the repo — measured, not assumed. While the repo is private that is the owner; on publication it becomes everyone, and ADR-0080 records the old tip SHA, so the transparency record doubles as the retrieval key. Hence the ordering rule this runbook exists to enforce: purge first, publish second. Two routes — ask GitHub Support to run gc (keeps everything, third-party latency) or delete and recreate under the SAME owner/name (self-service, verifiable, keeps the URL and therefore the Go import path, costs only metadata that never migrates). Includes the preflight that enumerates exactly what would be lost and refuses to answer when it cannot see the repo, and the one-command verification that proves the purge worked.
tags: [operations, runbook, git, history-rewrite, github, publication, privacy]
timestamp: 2026-08-03T00:00:00Z
---

# Purging a rewritten history from GitHub

## The fact this runbook is built on

A `git push --force` replaces a ref. It deletes nothing. The old commits stay in
GitHub's object store, unreachable but **served by SHA to anyone who can read the
repository**. Verified on this repo, 2026-08-03, twice, hours apart:

```bash
git fetch origin 3ec36fb449d98175ca3971fa7429c8ac03a8d3b1   # the pre-rewrite tip
# -> succeeds, and re-imports the old history into the local clone
```

Local cleanup is necessary and not sufficient. `git gc --prune=now` empties your
clone; the remote is untouched.

## The ordering rule

**Purge before publishing. Never after.**

While the repository is private, "anyone who can read it" is the owner. Publication
turns that into everyone, in one click, retroactively covering objects that were
already there. And [ADR-0080](../decisions/0080-project-license-apache-2.md) records
the old tip SHA — deliberately, because a repo that gates deletions on executable
evidence cannot make its own history unverifiable and say nothing. That record is
also the retrieval key while the objects live.

The tension is structural, not sloppy. It resolves in one direction only: **destroy
the referent, and the reference becomes harmless.** A SHA that names nothing
documents without enabling. So the order is purge, then publish, then the ADR can
stay complete.

## Step 0 — sweep the local copies first

A rewrite done safely *creates copies of the thing it is erasing*: a backup bundle,
often a second one, a verification clone, loose files, and filter-repo's own residue
under `.git/filter-repo/`. Nothing tracks them. On this repo there were four, plus
three older bundles from unrelated rewrites sitting in the parent directory.

```bash
find ~/Projects -name '*.bundle' -o -name '*pre-rewrite*' -o -name '*history-backup*'
git -C <repo> fsck --unreachable --connectivity-only | grep -c 'unreachable commit'
git -C <repo> reflog expire --expire=now --all && git -C <repo> gc --prune=now
```

Deleting the remote's copy while a bundle sits on your disk is theatre.

## Step 1 — preflight: what would recreating cost?

```bash
scripts/repo-recreate-preflight.sh Luum-Home/aisotropy --as <account-with-access>
```

Exit 0 nothing would be lost · 1 something would · **2 cannot evaluate — never a pass**.

`--as` switches the gh account for the run and switches back on exit, via trap, on
every path including the refusal and Ctrl-C. It exists because `gh auth switch`
writes a **global** config, not a per-terminal one: a bare switch leaves every other
shell and every concurrent agent session on the wrong account, and an `&&` chain that
dies in the middle never switches back.

It enumerates issues, PRs, forks, stars, watchers, releases, wiki, webhooks, deploy
keys, collaborators, Actions secrets/variables, environments and branch protection —
everything GitHub stores *about* a repo, none of which is in your clone.

Three of its rules were paid for by mistakes made while writing it:

- **GitHub answers 404, not 403, for a private repo you cannot see.** A wrong-account
  run would report zeros across the board and read as a clean bill of health. The
  script probes access first and refuses to report at all.
- **`gh` prints its error JSON to stdout**, so `|| echo 0` converts a permission
  error into a reported zero — which it did, on the "Actions secrets" row, the one
  annotated *CI breaks silently if not re-added*. Those rows now read `UNKNOWN` and
  force exit 2.
- **GitHub's `/issues` endpoint returns pull requests too.** The first real run
  reported "2 issues, 2 pull requests" for what was 2 PRs and no issues. Counting
  issues now filters `.pull_request == null`. An inflated loss estimate is a bad
  input to an irreversible decision in exactly the same way a deflated one is.

## Step 2 — choose a route

| | Delete + recreate | GitHub Support |
|---|---|---|
| Under your control | yes | no — their queue |
| Verifiable by you | yes, immediately | yes, after they act |
| Keeps the URL | **yes, if same owner/name** | yes |
| Keeps issues/PRs/stars/releases | no | yes |
| When it is cheap | while private and empty | always |

**Same owner/name keeps the URL identical**, which matters more here than in most
repos: the Go import path *is* the URL (`github.com/Luum-Home/aisotropy/aisotropy`).
Recreating is not "moving the project".

Prefer delete-and-recreate when the preflight is all zeros. That cheapness expires at
publication, along with the reason to hurry.

## Step 3 — delete and recreate

Do not start until Step 1 exits 0 and you hold a current clone.

```bash
# 1. confirm the clone is complete and current — this is the only copy that will survive
git -C <repo> fetch --all --tags && git -C <repo> log --oneline -1
git -C <repo> status --porcelain          # commit or stash anything of value first

# 2. record what must be recreated (from the preflight output): secrets, variables,
#    branch protection, collaborators, webhooks, environments

# 3. delete, then recreate under the SAME owner/name and the SAME visibility
gh repo delete Luum-Home/aisotropy --yes
gh repo create Luum-Home/aisotropy --private

# 4. push history and tags back
git -C <repo> push origin main
git -C <repo> push origin --tags

# 5. re-add the settings from step 2 (secrets are NOT recoverable — you need the values)
```

Under concurrency, make sure no other session is mid-push. A commit that exists only
in someone else's local clone survives; one that exists only in the remote does not.

## Step 4 — verify, do not assume

The same probe that found the problem proves it is gone:

```bash
git fetch origin <old-tip-sha>
# expected: fatal — the object no longer exists on the server
```

If it still succeeds, the purge did not happen. Clean up afterwards, because a
successful probe re-imports what you just deleted:

```bash
rm -f .git/FETCH_HEAD && git reflog expire --expire=now --all && git gc --prune=now
```

## What none of this achieves

- **It does not revoke anything.** If the old history was ever public, or ever cloned
  or forked, it is out. Purging removes a retrieval path, not a copy someone took.
  For this repo that risk is nil — it was never public and has zero forks — which is
  the only reason the purge is meaningful at all rather than performative.
- **Forks keep objects alive.** A repository with forks cannot be purged this way;
  the fork network shares storage. Check the preflight's fork row before believing
  any of this.
- **It is not a substitute for rotating a leaked secret.** If the rewrite removed a
  credential, rotate it. Assume it was read.
