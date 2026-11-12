#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
output=${1:?Usage: build-release.sh OUTPUT_DIRECTORY}
mkdir -p "$output"
output=$(CDPATH='' cd -- "$output" && pwd)
cd "$root"
version=$(cat VERSION)
system=${GOOS:-$(go env GOOS)}
architecture=${GOARCH:-$(go env GOARCH)}
name="worktree-$system-$architecture"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$output/$name" .
if command -v sha256sum >/dev/null 2>&1; then digest=$(sha256sum "$output/$name" | awk '{print $1}')
else digest=$(shasum -a 256 "$output/$name" | awk '{print $1}'); fi
printf '%s  %s\n' "$digest" "$name" > "$output/$name.sha256"
printf '%s\n' "$output/$name"
cp scripts/install-worktree "$output/install-worktree"
if command -v sha256sum >/dev/null 2>&1; then installer_digest=$(sha256sum "$output/install-worktree" | awk '{print $1}')
else installer_digest=$(shasum -a 256 "$output/install-worktree" | awk '{print $1}'); fi
printf '%s  install-worktree\n' "$installer_digest" > "$output/install-worktree.sha256"
