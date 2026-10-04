#!/usr/bin/env bash
# Check (pull requests): contract, commit scopes, and a build of every changed
# plugin. Runs untrusted code: TeamCity gives this build no credentials.
# Env: PR_TARGET_BRANCH (teamcity.pullRequest.target.branch), BUILD_NUMBER.
# shellcheck source=SCRIPTDIR/_common.sh
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

target="${PR_TARGET_BRANCH:-develop}"
target="${target#refs/heads/}"
[[ "$target" =~ ^[A-Za-z0-9._/-]+$ ]] || die "unexpected target branch name"
git fetch --quiet --no-tags origin "+refs/heads/$target:refs/remotes/origin/$target"
base="$(git merge-base HEAD "origin/$target")"
info "base $target @ $base"

release check
release check --commits "$base..HEAD"

# Changed plugins: folders under plugins/ touched since the base, minus "_" ones
# and folders deleted by this change.
plan="$WORK/plan.json"
git diff --name-only "$base" HEAD -- plugins/ | cut -d/ -f2 | sort -u | { grep -v '^_' || true; } \
  | while read -r id; do
      [[ -f "plugins/$id/VERSION" ]] || continue
      jq -n --arg id "$id" --arg v "$(tr -d '[:space:]' <"plugins/$id/VERSION")-check.${BUILD_NUMBER:-0}" \
        '{id: $id, dir: ("plugins/" + $id), version: $v}'
    done | jq -s . >"$plan"
info "changed plugins: $(jq -r 'map(.id) | join(", ")' "$plan")"
build_plan "$plan"
publish_dist
