#!/usr/bin/env python3
"""Read-only checks for the October 2026 Git fixture recovery.

Exit 0: recovery checks pass; 1: a check fails; 2: execution error.
All Git subprocesses discard inherited GIT_* variables and watch config bytes.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


class Finding(Exception):
    pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--expect-config-sha256", required=True)
    parser.add_argument("--target", default="99e9ab15efa3b3d192312e65237b7a206a662efb")
    parser.add_argument("--archive-ref", default="refs/archive/fixture-leak-2026-10-16")
    parser.add_argument("--archive-tip", default="0a9d95f9eced3d41bad95c3ee9d3f4054153d22f")
    args = parser.parse_args()
    repo = args.repo.resolve()
    dotgit = repo / ".git"
    gitdir = ((repo / dotgit.read_text().split(":", 1)[1].strip()).resolve()
              if dotgit.is_file() else dotgit)
    common = ((gitdir / (gitdir / "commondir").read_text().strip()).resolve()
              if (gitdir / "commondir").exists() else gitdir)
    config = common / "config"
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env["GIT_OPTIONAL_LOCKS"] = "0"
    results = []

    def watch():
        actual = hashlib.sha256(config.read_bytes()).hexdigest()
        if actual != args.expect_config_sha256:
            raise Finding("STOP: repository config SHA256 changed")

    def git(*command, allowed=(0,)):
        watch()
        proc = subprocess.run(["git", "-C", str(repo), *command], env=env,
                              capture_output=True, text=True, timeout=60)
        watch()
        results.append({"git": list(command), "exit": proc.returncode})
        if proc.returncode not in allowed:
            raise RuntimeError(f"git {command!r}: exit {proc.returncode}: {proc.stderr.strip()}")
        return proc

    def expect(condition, message):
        if not condition:
            raise Finding(message)

    watch()
    expect(git("config", "--file", str(config), "--get", "core.bare").stdout.strip() == "false",
           "core.bare must be false")
    for key in ("user.name", "user.email"):
        value = git("config", "--file", str(config), "--get", key, allowed=(0, 1))
        expect(value.returncode == 1, f"unexpected local {key}; review legitimate identity separately")
    archived = git("rev-parse", "--verify", "--quiet", args.archive_ref, allowed=(0, 1))
    expect(archived.returncode == 0 and archived.stdout.strip() == args.archive_tip,
           "fixture archive ref is missing or points at a different commit")
    ancestor = git("merge-base", "--is-ancestor", args.target, "HEAD", allowed=(0, 1))
    expect(ancestor.returncode == 0, "recovered commit is not an ancestor of current HEAD")
    fixture_commits = git("rev-list", args.archive_ref, "^" + args.target).stdout.splitlines()
    expect(bool(fixture_commits), "fixture archive contains no commits beyond recovered target")
    for fixture in fixture_commits:
        included = git("merge-base", "--is-ancestor", fixture, "HEAD", allowed=(0, 1))
        expect(included.returncode == 1, "a fixture commit is an ancestor of current HEAD")
    recovery = common / "recovery" / "fixture-leak-2026-10-16"
    for name in ("config.before", "index.before", "HEAD.before", "branch-before.bundle",
                 "worktree-vs-99e9ab1.json", "recovery-transitions.json", "config.sha256"):
        expect((recovery / name).is_file(), f"missing local recovery artifact: {name}")
    expect((recovery / "config.sha256").read_text().strip() == args.expect_config_sha256,
           "saved config baseline differs from the expected baseline")
    expect(json.loads((recovery / "worktree-vs-99e9ab1.json").read_text()) == [],
           "pre-recovery worktree differed from target; review preserved differences")
    git("bundle", "verify", str(recovery / "branch-before.bundle"))
    heads = git("bundle", "list-heads", str(recovery / "branch-before.bundle")).stdout
    expect(f"{args.archive_tip} refs/heads/chore/go-vulns-x-net-grpc-go1269\n" in heads,
           "bundle does not preserve the original branch tip")
    watch()
    print(json.dumps({"status": "pass", "config_sha256": args.expect_config_sha256,
                      "recovered_target": args.target, "archive_tip": args.archive_tip,
                      "checks": results}, indent=2))


if __name__ == "__main__":
    try:
        main()
    except Finding as exc:
        print(json.dumps({"status": "finding", "message": str(exc)}), file=sys.stderr)
        sys.exit(1)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        print(json.dumps({"status": "error", "message": str(exc)}), file=sys.stderr)
        sys.exit(2)
