#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
cd "$root"
stage=$(mktemp -d "${TMPDIR:-/tmp}/worktree-checks.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
CGO_ENABLED=0 go build -trimpath -o "$stage/worktree" .
export WORKTREE_BINARY="$stage/worktree"
export WORKTREE_CONFIG="$stage/config.json"
export XDG_STATE_HOME="$stage/state"
printf '%s\n' '{"recovery_allowed_github_owners":["easypost-sandbox"]}' > "$WORKTREE_CONFIG"
python3 scripts/test-lifecycle.py "$WORKTREE_BINARY"
python3 scripts/test-git-worktree-policy.py
python3 scripts/test-git-worktree-cleanup.py
python3 scripts/test-git-worktree-size-cache.py
python3 scripts/test-git-worktree-wip-push.py
if [ -n "${WORKTREE_HOOKS_GIT:-}" ]; then
    python3 scripts/test-hooks.py "$WORKTREE_BINARY" "$WORKTREE_HOOKS_GIT"
fi
