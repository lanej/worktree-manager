#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export GOWORK=off
version=$(sed -n 's/^go /go/p' go.mod)
export GOTOOLCHAIN="$version"
exec go run ./internal/checks "$@"
