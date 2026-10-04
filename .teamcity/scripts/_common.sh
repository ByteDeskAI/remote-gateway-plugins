#!/usr/bin/env bash
# Shared helpers for .teamcity/scripts/*. Sourced, never run directly.
# Do not add `set -x`: Release and Publish builds hold credentials.
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# Build output must be outside the checkout (tools/build refuses otherwise);
# it is copied into $ROOT/dist (gitignored) for TeamCity artifacts at the end.
OUT="$WORK/dist"
mkdir -p "$OUT"
rm -rf "$ROOT/dist"

info() { printf 'tc-plugins: %s\n' "$*"; }
die() { printf 'tc-plugins: error: %s\n' "$*" >&2; exit 1; }

for c in git go jq python3; do command -v "$c" >/dev/null || die "missing command: $c"; done

# The release CLI, built outside the checkout so the tree stays clean.
(cd tools/release && GOWORK=off go build -o "$WORK/release" .)
release() { "$WORK/release" "$@"; }

# TeamCity fetches without tags; versions come from <id>/vX.Y.Z tags. The
# repository is public, so this needs no credential.
fetch_tags() { git fetch --quiet --force --tags origin; }

# build_plan PLAN_JSON: build every planned plugin at HEAD with tools/build.
# Credentials that the hooks must not see are removed from their environment.
build_plan() {
  local rev id dir ver
  rev="$(git rev-parse HEAD)"
  while IFS=$'\t' read -r id dir ver; do
    [[ -n "$id" ]] || continue
    info "build $id $ver ($dir @ $rev)"
    env -u RELEASE_GIT_TOKEN -u STORE_ADMIN_TOKEN \
      bash tools/build/plugin-build.sh --plugin-dir "$dir" --revision "$rev" \
        --id "$id" --version "$ver" --platform linux-amd64 --out "$OUT/$id"
  done < <(jq -r '.[] | [.id, .dir, .version] | @tsv' "$1")
}

# publish_dist: hand the built packages to TeamCity (artifact rule dist/**).
publish_dist() {
  mkdir -p "$ROOT/dist"
  cp -R "$OUT/." "$ROOT/dist/"
  info "artifacts: $(find "$ROOT/dist" -name '*.tar.gz' | wc -l) package(s)"
}
