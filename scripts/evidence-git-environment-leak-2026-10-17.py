#!/usr/bin/env python3
"""Reproduce Git environment isolation in temporary repositories only.

macOS/Linux, Python 3. Default: light shell/dispatcher checks and vulnerable
negative control. --go-tests also runs affected Go packages with a poisoned
GIT_DIR and compares victim config, HEAD, index, tree and working files.
Exit: 0 expected isolation/control behavior, 1 finding, 2 execution error.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
BASE_ENV = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
CONFIG = None
BASELINE = None


def guard():
    if CONFIG is not None and hashlib.sha256(CONFIG.read_bytes()).hexdigest() != BASELINE:
        raise AssertionError("STOP: shared .git/config changed")


def run(argv, *, cwd=ROOT, env=None, timeout=30, expected=0):
    guard()
    try:
        result = subprocess.run(argv, cwd=cwd, env=BASE_ENV if env is None else env,
                                capture_output=True, text=True, timeout=timeout)
    finally:
        guard()
    if result.returncode != expected:
        raise RuntimeError(f"{argv!r}: exit {result.returncode}\n{result.stdout[-6000:]}\n{result.stderr[-6000:]}")
    return result.stdout


def git(repo, *args, env=None):
    return run(["git", "-C", str(repo), *args], env=env)


def init_repo(path):
    path.mkdir()
    git(path, "init", "-q")
    (path / "sentinel").write_text("untouched\n")
    git(path, "add", "sentinel")
    git(path, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid",
        "commit", "-qm", "initial")
    return path


def snapshot(repo):
    gitdir = Path(git(repo, "rev-parse", "--absolute-git-dir").strip())
    common = Path(git(repo, "rev-parse", "--path-format=absolute", "--git-common-dir").strip())
    return {
        "config": hashlib.sha256((common / "config").read_bytes()).hexdigest(),
        "index": hashlib.sha256((gitdir / "index").read_bytes()).hexdigest(),
        "HEAD": git(repo, "rev-parse", "HEAD").strip(),
        "tree": git(repo, "rev-parse", "HEAD^{tree}").strip(),
        "files": {str(p.relative_to(repo)): hashlib.sha256(p.read_bytes()).hexdigest()
                  for p in sorted(repo.rglob("*")) if p.is_file() and ".git" not in p.relative_to(repo).parts},
    }


def main():
    global CONFIG, BASELINE
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-tests", action="store_true")
    parser.add_argument("--incident-evidence", type=Path, help="optional directory containing the saved pre-push-root-tests.log")
    parser.add_argument("--negative-control", action="store_true", help="run the same Go isolation test against an unfiltered helper in a temporary minimal module")
    args = parser.parse_args()
    common = Path(git(ROOT, "rev-parse", "--path-format=absolute", "--git-common-dir").strip())
    CONFIG = common / "config"
    BASELINE = hashlib.sha256(CONFIG.read_bytes()).hexdigest()
    result = {"shared_config_sha256": BASELINE}
    if args.incident_evidence:
        saved = args.incident_evidence / "pre-push-root-tests.log"
        text = saved.read_text()
        assert 'committed \\"msg\\" at ffd2635b58f9b901ca09be341a580afdff6af41d' in text, "missing direct msg-to-test correlation"
        assert "2696683 add note\\n2c0267a initial\\nffd2635 msg" in text, "missing host-side executor history correlation"
        result["incident_commits"] = git(ROOT, "log", "--reverse", "--format=%h %aI %an <%ae> %s",
                                        "99e9ab1..refs/archive/fixture-leak-2026-10-16").splitlines()
        result["incident_correlation"] = "saved taskagent failures contain exact fixture commit SHAs"

    with tempfile.TemporaryDirectory(prefix="aisotropy-git-env-") as temporary:
        temp = Path(temporary)
        victim = init_repo(temp / "victim")
        target = temp / "target"
        target.mkdir()
        poisoned = dict(BASE_ENV, GIT_DIR=str(victim / ".git"), GIT_COMMON_DIR=str(victim / ".git"),
                        GIT_WORK_TREE=str(victim), GIT_INDEX_FILE=str(victim / ".git/index"),
                        GIT_CONFIG_COUNT="1", GIT_CONFIG_KEY_0="user.email",
                        GIT_CONFIG_VALUE_0="poison@example.invalid")
        before = snapshot(victim)
        # The exact vulnerable operation, in fixtures only: -C loses to GIT_DIR.
        git(target, "config", "user.name", "leaked", env=poisoned)
        assert snapshot(victim) != before, "negative control failed to catch victim write"
        result["negative_control"] = "detected victim configuration write from git -C target"
        git(victim, "config", "--unset", "user.name")
        assert snapshot(victim) == before, "negative control fixture did not restore"

        # This narrow init case does not identify the historical bare writer.
        probe = init_repo(temp / "init-probe")
        git(target, "init", "-q", env=dict(BASE_ENV, GIT_DIR=str(probe / ".git")))
        assert git(probe, "config", "--get", "core.bare").strip() == "false"
        result["init_probe"] = "GIT_DIR-only reinitialization kept core.bare=false in this fixture; historical writer unidentified"

        # Linked worktree gitdirs differ from ordinary .git directories: without
        # GIT_WORK_TREE, raw init writes bare=true to their common configuration.
        shared = init_repo(temp / "linked-victim")
        linked = temp / "linked-worktree"
        git(shared, "worktree", "add", "-q", "-b", "linked-fixture", str(linked))
        linked_gitdir = Path(git(linked, "rev-parse", "--absolute-git-dir").strip())
        assert linked_gitdir.resolve().is_relative_to(temp.resolve()), "linked victim escaped temporary fixture"
        linked_env = dict(BASE_ENV, GIT_DIR=str(linked_gitdir))
        linked_before = snapshot(linked)
        linked_target = temp / "linked-target"
        linked_target.mkdir()
        git(linked_target, "init", "-q", env=linked_env)
        shared_config = shared / ".git/config"
        assert run(["git", "config", "--file", str(shared_config), "--get", "core.bare"]).strip() == "true"
        linked_after = snapshot(linked)
        assert linked_before["config"] != linked_after["config"], "linked negative control missed config write"
        assert {k: v for k, v in linked_before.items() if k != "config"} == {
            k: v for k, v in linked_after.items() if k != "config"}, "linked init changed more than common config"
        result["linked_worktree_negative_control"] = "raw git -C target init with linked GIT_DIR wrote core.bare=true to fixture common config"
        run(["git", "config", "--file", str(shared_config), "core.bare", "false"])
        assert snapshot(linked) == linked_before, "linked negative-control fixture did not restore"
        run(["sh", "-c", '. "$1"; exec git -C "$2" init -q', "sh",
             str(ROOT / "scripts/clear-git-env.sh"), str(linked_target)], env=linked_env)
        assert snapshot(linked) == linked_before, "sanitized init changed linked victim common config/HEAD/index/tree/files"
        assert (linked_target / ".git/config").is_file(), "sanitized init did not initialize target"
        result["linked_worktree_positive_control"] = "same init after shell sanitization initialized target; linked victim common config, HEAD, index, tree and files unchanged"

        # Prove shell sanitization removes configuration injection as well as selectors.
        output = run(["sh", "-c", '. "$1"; env', "sh", str(ROOT / "scripts/clear-git-env.sh")], env=poisoned)
        assert not any(line.startswith("GIT_") for line in output.splitlines()), "shell retained GIT_*"
        result["shell_sanitization"] = "all inherited GIT_* removed"

        # A fake active worktree gate records dispatch and retained hook arguments.
        dispatch_repo = init_repo(temp / "dispatch")
        (dispatch_repo / "scripts").mkdir()
        (dispatch_repo / "scripts/pre-push-gate.sh").write_text(
            '#!/bin/sh\nprintf "active-worktree:%s:%s\\n" "$1" "$2"\n')
        installed_hook = common / "hooks/pre-push"
        assert installed_hook.read_bytes() == (ROOT / "scripts/pre-push-hook.sh").read_bytes(), "installed hook differs from tracked dispatcher"
        output = run(["sh", str(installed_hook), "origin", "fixture-remote"], cwd=dispatch_repo)
        assert output == "active-worktree:origin:fixture-remote\n", output
        result["installed_dispatcher"] = "selected active fixture worktree and retained arguments"

        if args.go_tests:
            env = dict(poisoned, GOMAXPROCS="1", GOTOOLCHAIN="local", HEAVY_LANE_WAIT_S="0",
                       GOFLAGS="-buildvcs=false -trimpath -p=1")
            commands = [
                ["go", "test", "./internal/gitenv", "./internal/sandbox", "./internal/taskagent", "-count=1", "-timeout=180s"],
                ["go", "test", "-tags", "keybroker", "./aisotropy", "-run",
                 "TestApp_RunTask_(CustomProviderIsUsed|MaxDurationTearsDownRunaway)$", "-count=1", "-timeout=60s"],
            ]
            for command in commands:
                before = snapshot(victim)
                output = run([str(ROOT / "scripts/with-heavy-lane.sh"), "--need-mb", "256", *command], env=env, timeout=420)
                assert snapshot(victim) == before, f"victim config/HEAD/index/tree/files changed: {command}"
                print(output, end="", file=sys.stderr)
            result["go_regression"] = "affected packages and keybroker subset passed; victim snapshot unchanged"
        if args.go_tests or args.negative_control:
            # Copy only the self-contained helper and its actual regression test.
            # The checkout remains unchanged; mutate the helper in a temp module.
            mutant = temp / "mutant"
            mutant.mkdir()
            source = (ROOT / "internal/gitenv/command.go").read_text()
            needle = "cmd.Env = clean(os.Environ())"
            assert source.count(needle) == 1, "negative-control mutation target changed"
            (mutant / "command.go").write_text(source.replace(needle, "cmd.Env = os.Environ()"))
            (mutant / "command_test.go").write_bytes((ROOT / "internal/gitenv/command_test.go").read_bytes())
            (mutant / "go.mod").write_text("module fixture-negative-control\ngo 1.26.9\n")
            env = dict(BASE_ENV, GOMAXPROCS="1", GOTOOLCHAIN="local", HEAVY_LANE_WAIT_S="0",
                       GOFLAGS="-buildvcs=false -trimpath -p=1")
            output = run([str(ROOT / "scripts/with-heavy-lane.sh"), "--need-mb", "128",
                          "go", "test", "-run", "^TestCommandIgnoresInheritedGitEnvironment$",
                          "-count=1", "-timeout=45s", "./..."], cwd=mutant, env=env,
                         timeout=150, expected=1)
            assert "inherited Git environment changed victim config, HEAD, index or tree" in output, output
            print("EXPECTED NEGATIVE CONTROL (exit 1):\n" + output, end="", file=sys.stderr)
            result["same_test_mutation_control"] = "same regression failed with exit 1 on unfiltered helper in a temporary minimal module"
        assert snapshot(victim) == before, "positive checks changed victim"
        result["victim_after_positive_checks"] = "config, HEAD, index, tree and working files unchanged"
    guard()
    print(json.dumps(result, indent=2, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except AssertionError as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(1)
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(2)
