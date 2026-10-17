#!/usr/bin/env python3
"""Run serial verification, stopping if the shared Git config changes.

Run on macOS from the checkout, with Go 1.26.9 already installed. Exit status:
0 = command passed; 1 = command failed; 2 = unsafe state / execution error.
Verification writes evidence only. Explicit mutation flags support reviewed
pinned go get / git commit operations with the same config monitor.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import signal
import subprocess
import sys
import time


ROOT = Path(__file__).resolve().parents[1]
FIX = "99e9ab15efa3b3d192312e65237b7a206a662efb"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def clean_env():
    # Git hooks export repository selectors. -C alone cannot isolate a fixture.
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    bypasses = [k for k, v in env.items() if k.startswith("SKIP_") and v == "1"]
    if bypasses:
        raise RuntimeError("Refusing inherited bypasses: " + ", ".join(bypasses))
    env.update(GOENV_VERSION="1.26.9", GOTOOLCHAIN="local", GOMAXPROCS="2")
    return env


def git(env, *args):
    return subprocess.run(
        ["git", "-C", str(ROOT), *args], env=env, check=True,
        text=True, capture_output=True, timeout=30,
    ).stdout.strip()


def source_fingerprint(env, manifests=True):
    patterns = ["*.go", "go.mod", "go.sum"] if manifests else ["*.go"]
    files = git(env, "ls-files", "--cached", "--others", "--exclude-standard", "--", *patterns).splitlines()
    entries = [(name, digest(ROOT / name)) for name in sorted(set(files)) if (ROOT / name).is_file()]
    return hashlib.sha256(json.dumps(entries, separators=(",", ":")).encode()).hexdigest()


def module_fingerprint(env, cwd):
    pending, visited, entries = [cwd], set(), []
    while pending:
        module = pending.pop().resolve()
        if module in visited:
            continue
        visited.add(module)
        for name in ("go.mod", "go.sum"):
            path = module / name
            if path.is_file():
                entries.append((str(path.relative_to(ROOT)), digest(path)))
        for target in re.findall(r"=>\s+(\.\.?/[^\s]+)", (module / "go.mod").read_text()):
            dependency = (module / target).resolve()
            if not dependency.is_relative_to(ROOT):
                raise RuntimeError("Local replacement outside checkout needs an explicit input inventory")
            pending.append(dependency)
    inputs = dict(go_source=source_fingerprint(env, manifests=False), manifests=sorted(entries))
    return hashlib.sha256(json.dumps(inputs, sort_keys=True).encode()).hexdigest()


def stop(proc):
    try:
        os.killpg(proc.pid, signal.SIGTERM)
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        proc.wait(timeout=5)
    except ProcessLookupError:
        pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config-sha256", required=True)
    parser.add_argument("--phase", required=True, choices=[
        "inspect", "graph", "download", "prepare", "build", "vet", "test", "vuln", "supply", "suite", "modules", "command",
    ])
    parser.add_argument("--module", default=".", help="Module directory relative to checkout")
    parser.add_argument("--tags", help="Go build tags; no manual tags are selected by default")
    parser.add_argument("--resume", action="store_true", help="Reuse identical existing module results and continue collecting ordinary failures")
    parser.add_argument("--allow-new-commit", action="store_true", help="For a reviewed git commit: allow exactly one new direct child of HEAD")
    parser.add_argument("--allow-module-update", action="store_true", help="For a reviewed pinned go get: allow only this module's manifest changes")
    parser.add_argument("--label", help="Required for a custom command; safe filename")
    parser.add_argument("--timeout", type=int, default=1800)
    parser.add_argument("--output", type=Path, default=ROOT / "docs/security/evidence/go-vulns-2026-10-17")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.timeout < 1 or args.timeout > 7200:
        parser.error("timeout must be between 1 and 7200 seconds")
    env = clean_env()
    common = Path(git(env, "rev-parse", "--git-common-dir"))
    if not common.is_absolute():
        common = ROOT / common
    config = common.resolve() / "config"
    before = digest(config)
    if before != args.config_sha256:
        raise RuntimeError("Shared Git config differs from the reviewed SHA256")
    git(env, "merge-base", "--is-ancestor", FIX, "HEAD")
    head = git(env, "rev-parse", "HEAD")
    branch = git(env, "branch", "--show-current")
    if branch != "chore/go-vulns-x-net-grpc-go1269":
        raise RuntimeError("Unexpected branch: " + branch)
    cwd = (ROOT / args.module).resolve()
    if not cwd.is_relative_to(ROOT) or not (cwd / "go.mod").is_file():
        raise RuntimeError("Module must be within this checkout and contain go.mod")
    if args.phase == "modules":
        manifests = git(env, "diff", "--name-only", FIX + "^", FIX, "--", "**/go.mod").splitlines()
        if not manifests:
            raise RuntimeError("No changed nested modules found")
        args.output.mkdir(parents=True, exist_ok=True)
        records = []
        failed = False
        pristine_manifests = not git(env, "diff", "--name-only", FIX, "--", "go.mod", "go.sum", "**/go.mod", "**/go.sum")
        for manifest in manifests:
            module = str(Path(manifest).parent)
            phases = (["prepare"] if (ROOT / module / "build.sh").exists() else []) + ["build", "test", "vuln"]
            for phase in phases:
                label = module.replace("/", "-") + "-" + phase
                command = [sys.executable, str(Path(__file__).resolve()), "--config-sha256", before,
                           "--phase", phase, "--module", module, "--label", label,
                           "--timeout", str(args.timeout), "--output", str(args.output)]
                if module.startswith("examples/"):
                    command.extend(["--tags", "keybroker"])
                record_path = args.output / (label + ".json")
                if args.resume and record_path.exists():
                    if digest(config) != before:
                        raise RuntimeError("Shared Git config changed during module inventory")
                    previous = json.loads(record_path.read_text())
                    if (not previous.get("error")
                            and (previous.get("module_input_sha256_after") == module_fingerprint(env, ROOT / module)
                                 or (not previous.get("module_input_sha256_after") and pristine_manifests
                                     and previous.get("source_sha256_after") == source_fingerprint(env)))
                            and previous.get("config_sha256_after") == before):
                        records.append(previous)
                        failed |= previous.get("exit") != 0 or bool(previous.get("error"))
                        print("Reused unchanged result: " + label, flush=True)
                        continue
                record_path.unlink(missing_ok=True)
                result = subprocess.run(command, check=False)
                records.append(json.loads(record_path.read_text()) if record_path.exists()
                               else dict(module=module, phase=phase, exit=result.returncode, error="Runner failed before process start"))
                (args.output / "modules.json").write_text(json.dumps(records, indent=2) + "\n")
                failed |= result.returncode != 0
                if result.returncode == 2 or (result.returncode and not args.resume):
                    return result.returncode
        for record in records:
            if (not record.get("module_input_sha256_after") and pristine_manifests
                    and record.get("source_sha256_after") == source_fingerprint(env)):
                fingerprint = module_fingerprint(env, ROOT / record["cwd"])
                record.update(module_input_sha256_before=fingerprint, module_input_sha256_after=fingerprint,
                              fingerprint_reconstructed_from_matching_global_hash=True)
                (args.output / (record["cwd"].replace("/", "-") + "-" + record["phase"] + ".json")).write_text(json.dumps(record, indent=2) + "\n")
        if digest(config) != before:
            raise RuntimeError("Shared Git config changed during module inventory")
        (args.output / "modules.json").write_text(json.dumps(records, indent=2) + "\n")
        return 1 if failed else 0
    flags = shlex.split(env.get("GOFLAGS", ""))
    if args.phase in ("test", "suite", "command") and "-trimpath" in flags:
        raise RuntimeError("Tests resolve fixtures with runtime.Caller; remove inherited -trimpath")
    if args.phase not in ("test", "suite", "command"):
        flags.append("-trimpath")
    flags.append("-p=2")
    if not args.allow_module_update:
        flags.append("-mod=readonly")
    env["GOFLAGS"] = shlex.join(flags)
    lane = ["bash", str(ROOT / "scripts/with-heavy-lane.sh")]
    tags = ["-tags", args.tags] if args.tags else []
    commands = {
        "inspect": ["python3", "-c", (
            "import subprocess; "
            "cmds=[['go','version'],['go','env','GOFLAGS','GOMODCACHE','GOCACHE'],"
            "['govulncheck','-version'],['sysctl','-n','vm.loadavg'],"
            "['sysctl','vm.swapusage'],['vm_stat'],['df','-h','.']]; "
            "[(print('$ '+ ' '.join(c),flush=True),subprocess.run(c,check=True)) for c in cmds]"
        )],
        "graph": ["go", "mod", "graph"],
        "download": ["go", "mod", "download"],
        "prepare": lane + ["bash", "build.sh", "--prepare-only"],
        "build": lane + ["go", "build", *tags, "./..."],
        "vet": lane + ["go", "vet", *tags, "./..."],
        "test": lane + ["go", "test", *tags, "./...", "-count=1"],
        "vuln": lane + ["govulncheck", "-show", "verbose", *tags, "./..."],
        "supply": lane + ["bash", "scripts/supply-chain-guard.sh"],
        "suite": lane + ["bash", "scripts/test-all.sh", "--quick"],
    }
    label = args.label or args.phase
    if not label or any(c not in "abcdefghijklmnopqrstuvwxyz0123456789-_" for c in label):
        parser.error("label must contain only lowercase letters, digits, hyphens and underscores")
    if args.phase == "command":
        command = args.command[1:] if args.command[:1] == ["--"] else args.command
        if not args.label or not command:
            parser.error("custom command requires --label and -- command")
    else:
        command = commands[args.phase]
    if args.allow_new_commit and (args.phase != "command" or command[:2] != ["git", "commit"]):
        parser.error("--allow-new-commit requires --phase command -- git commit ...")
    if args.allow_module_update and (args.phase != "command" or command[:2] != ["go", "get"]
                                    or not command[2:] or any(not re.fullmatch(r"[\w./-]+@v[\w.+-]+", arg) for arg in command[2:])):
        parser.error("--allow-module-update requires a pinned go get without update flags")
    args.output.mkdir(parents=True, exist_ok=True)
    log = args.output / (label + ".log")
    manifests = {str(p.relative_to(ROOT)): digest(p) for p in ROOT.rglob("go.*")
                 if p.name in ("go.mod", "go.sum") and ".git" not in p.parts}
    source_before = source_fingerprint(env)
    go_before = source_fingerprint(env, manifests=False)
    module_before = module_fingerprint(env, cwd)
    reason = None
    started = time.monotonic()
    with log.open("w") as output:
        proc = subprocess.Popen(command, cwd=cwd, env=env, stdout=output,
                                stderr=subprocess.STDOUT, start_new_session=True)
        while proc.poll() is None:
            try:
                current = digest(config)
            except OSError:
                current = None
            if current != before:
                reason = "Shared Git config changed; process group terminated"
            elif time.monotonic() - started > args.timeout:
                reason = "Command timeout; process group terminated"
            elif log.stat().st_size > 10 * 1024 * 1024:
                reason = "Log exceeded 10 MiB; process group terminated"
            if reason:
                stop(proc)
                break
            time.sleep(0.25)
    try:
        after = digest(config)
    except OSError:
        after = None
    if after != before:
        reason = "Shared Git config changed; verification stopped"
    head_after = git(env, "rev-parse", "HEAD") if after == before else None
    branch_after = git(env, "branch", "--show-current") if after == before else None
    expected_commit = (after == before and args.allow_new_commit and head_after != head
                       and git(env, "rev-list", "--parents", "-n", "1", head_after).split() == [head_after, head])
    if after == before and (branch_after != branch or (head_after != head and not expected_commit)):
        reason = "HEAD or branch changed during verification"
    source_after = source_fingerprint(env) if after == before else None
    if after == before and source_after != source_before and not args.allow_module_update:
        reason = "Go source or manifests changed during verification"
    if args.allow_module_update and source_fingerprint(env, manifests=False) != go_before:
        reason = "Go source changed during manifest update"
    changed = [p for p, sha in manifests.items() if not (ROOT / p).exists() or digest(ROOT / p) != sha]
    allowed = {str((cwd / name).relative_to(ROOT)) for name in ("go.mod", "go.sum")}
    if changed and (not args.allow_module_update or set(changed) - allowed):
        reason = "Manifest changed during verification: " + ", ".join(changed)
    # Keep evidence portable without altering diagnostics or exits. Full noisy
    # output is regenerable process data; compact results remain in the repo.
    log.write_text(log.read_text(errors="replace").replace(str(ROOT), "$REPO_ROOT")
                   .replace(str(Path.home()), "$HOME"))
    log_name = log.name
    if log.stat().st_size > 64 * 1024:
        archive = common.resolve() / "recovery/go-vulns-2026-10-17"
        archive.mkdir(parents=True, exist_ok=True)
        shutil.move(str(log), str(archive / log.name))
        log_name = "$GIT_COMMON_DIR/recovery/go-vulns-2026-10-17/" + log.name
    record = dict(phase=args.phase, head=head, branch=branch, head_after=head_after,
                  branch_after=branch_after, cwd=str(cwd.relative_to(ROOT)),
                  allow_new_commit=args.allow_new_commit,
                  allow_module_update=args.allow_module_update,
                  command=[str(arg).replace(str(ROOT), "$REPO_ROOT") for arg in command],
                  environment={k: env[k] for k in ["GOFLAGS", "GOENV_VERSION", "GOTOOLCHAIN", "GOMAXPROCS"]},
                  config_sha256_before=before, config_sha256_after=after,
                  source_sha256_before=source_before, source_sha256_after=source_after,
                  module_manifests={p.name: digest(p) for p in [cwd / "go.mod", cwd / "go.sum"] if p.is_file()},
                  module_input_sha256_before=module_before,
                  module_input_sha256_after=module_fingerprint(env, cwd) if after == before else None,
                  exit=proc.returncode, error=reason, log=log_name)
    (args.output / (label + ".json")).write_text(json.dumps(record, indent=2) + "\n")
    if cwd != ROOT:
        summary = args.output / "modules.json"
        records = json.loads(summary.read_text()) if summary.exists() else []
        records = [r for r in records if (r.get("cwd"), r.get("phase")) != (record["cwd"], record["phase"])]
        records.append(record)
        summary.write_text(json.dumps(records, indent=2) + "\n")
    print(json.dumps(record, indent=2))
    return 2 if reason else (0 if proc.returncode == 0 else 1)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
