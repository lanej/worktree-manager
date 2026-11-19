#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
package_binary=${WORKTREE_PACKAGE_BINARY:-}
if [ -n "$package_binary" ]; then
    package_binary=$(CDPATH='' cd -- "$(dirname "$package_binary")" && pwd)/$(basename "$package_binary")
fi
cd "$root"
export GOWORK=off
go_version=$(sed -n 's/^go /go/p' go.mod)
export GOTOOLCHAIN="$go_version"
stage=$(mktemp -d "${TMPDIR:-/tmp}/worktree-checks.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
if [ -n "$package_binary" ]; then
    export WORKTREE_BINARY="$package_binary"
else
    version=$(cat VERSION)
    CGO_ENABLED=0 go build -trimpath -ldflags="-X main.version=$version" -o "$stage/worktree" .
    export WORKTREE_BINARY="$stage/worktree"
fi
export WORKTREE_CONFIG="$stage/config.json"
export XDG_STATE_HOME="$stage/state"
printf '%s\n' '{"recovery_allowed_github_owners":["easypost-sandbox"]}' > "$WORKTREE_CONFIG"
if command -v gotestsum >/dev/null; then
    gotestsum -- -tags integration -count=1 -timeout 20m ./internal/acceptance "$@"
else
    go test -tags integration -count=1 -timeout 20m ./internal/acceptance "$@"
fi
