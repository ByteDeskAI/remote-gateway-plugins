#!/usr/bin/env bash
# Build hook (docs/CONTRACT.md): test, build, and stage plugin.json plus the
# binary into the empty staging directory given as $1. Called from the plugin
# folder by tools/build/plugin-build.sh.
set -euo pipefail
fail() { printf '%s\n' "plugin-build-v1: $*" >&2; exit 1; }
[[ $# == 1 ]] || fail 'expected one absolute staging directory'
stage=$1
[[ "$stage" == /* && -d "$stage" && ! -L "$stage" ]] || fail 'stage must be an existing absolute directory, not a symlink'
plugin=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
[[ "$stage" != "$plugin" && "$stage" != "$plugin/"* ]] || fail 'stage must be outside the plugin folder'
shopt -s nullglob dotglob
entries=("$stage"/*)
[[ ${#entries[@]} == 0 ]] || fail 'stage must be empty'
cd -- "$plugin"
case "${PLUGIN_BUILD_PLATFORM:-linux-$(go env GOHOSTARCH)}" in
  linux-amd64) export GOOS=linux GOARCH=amd64 ;;
  linux-arm64) export GOOS=linux GOARCH=arm64 ;;
  *) fail "unsupported PLUGIN_BUILD_PLATFORM=${PLUGIN_BUILD_PLATFORM}" ;;
esac
export CGO_ENABLED=0
# Tests run natively; only the build targets the platform.
GOOS= GOARCH= go test ./...
go build -trimpath -o "$stage/example" .
cp -- plugin.json "$stage/plugin.json"
