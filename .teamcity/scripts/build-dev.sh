#!/usr/bin/env bash
# Build dev (develop): every plugin with releasable commits since its last tag,
# as <next>-dev.<build>. No commits, no tags, no pushes. Env: BUILD_NUMBER.
# shellcheck source=SCRIPTDIR/_common.sh
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

[[ "${BUILD_NUMBER:-}" =~ ^[0-9]+$ ]] || die "BUILD_NUMBER must be the numeric TeamCity build counter"
fetch_tags
plan="$WORK/plan.json"
release plan --mode dev --build "$BUILD_NUMBER" >"$plan"
info "plan: $(jq -r 'map(.id + " " + .version) | join(", ")' "$plan")"
build_plan "$plan"
publish_dist
