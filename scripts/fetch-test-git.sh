#!/bin/sh
set -eu
output=${1:?Usage: fetch-test-git.sh OUTPUT_DIRECTORY}
case $(uname -s) in Darwin) system=darwin ;; Linux) system=linux ;; *) exit 1 ;; esac
case $(uname -m) in
    arm64|aarch64) if [ "$system" = linux ]; then architecture=aarch64; else architecture=arm64; fi ;;
    x86_64|amd64) architecture=x86_64 ;;
    *) exit 1 ;;
esac
name="git-2.56.0-worktree-hooks-$system-$architecture.tar.gz"
release=https://github.com/lanej/dotfiles/releases/download/git-worktree-hooks-2.56.0.1
mkdir -p "$output"
output=$(CDPATH='' cd -- "$output" && pwd)
curl -fLsS "$release/$name" -o "$output/$name"
curl -fLsS "$release/$name.sha256" -o "$output/$name.sha256"
cd "$output"
if command -v sha256sum >/dev/null 2>&1; then sha256sum -c "$name.sha256"
else shasum -a 256 -c "$name.sha256"; fi
mkdir runtime
tar -xzf "$name" -C runtime
printf '%s\n' "$output/runtime/bin/git"
