#!/usr/bin/env bash
# plugin-build-v1 hook (docs/CONTRACT.md): test, build, stage plugin.json + binary.
set -euo pipefail
fail() { printf '%s\n' "plugin-build-v1: $*" >&2; exit 1; }
[[ $# == 1 ]] || fail 'expected one absolute staging directory'
stage=$1
[[ "$stage" == /* && -d "$stage" && ! -L "$stage" ]] || fail 'stage must be an existing absolute directory, not a symlink'
stage=$(cd -- "$stage" && pwd -P)
[[ "$stage" != / ]] || fail 'stage cannot be root'
plugin=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
repo=$(cd -- "$plugin/../.." && pwd -P)
[[ "$stage" != "$repo" && "$stage" != "$repo/"* ]] || fail 'stage must be outside the repository'
[[ -z "$(find "$stage" -mindepth 1 -maxdepth 1 -print -quit)" ]] || fail 'stage must be empty'

case "${PLUGIN_BUILD_PLATFORM:-$(go env GOHOSTOS)-$(go env GOHOSTARCH)}" in
  linux-amd64) goos=linux goarch=amd64 ;;
  linux-arm64) goos=linux goarch=arm64 ;;
  *) fail "unsupported PLUGIN_BUILD_PLATFORM ${PLUGIN_BUILD_PLATFORM:-}" ;;
esac

cd -- "$plugin"
# go.mod's replace points kit at ../../kit, so the module builds without go.work.
export GOWORK=off GOFLAGS=-mod=readonly CGO_ENABLED=0
go test -count=1 ./...
GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false -o "$stage/firewalla" ./cmd/firewalla
cp -- plugin.json "$stage/plugin.json"
