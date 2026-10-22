#!/usr/bin/env sh
# Installed as a copy: shared hooks must dispatch to the active worktree, not
# to the checkout from which install-hooks.sh happened to run.
set -eu
repo_root="$(git rev-parse --show-toplevel)"
exec sh "$repo_root/scripts/pre-push-gate.sh" "$@"
