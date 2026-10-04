#!/usr/bin/env bash
# Release main: version, changelog, commit, tag, build, push, back-merge.
#
# Order matters. tools/build only builds a clean checkout at an exact commit,
# so the release commit and tags are made locally first, every plugin is built
# from that commit, and nothing is pushed unless every build passed:
#   1. plan --mode release     2. apply each      3. finalize (commit + tags)
#   4. build each at HEAD      5. push main + tags atomically
#   6. merge main into develop and push
# A failure before step 5 leaves GitHub untouched.
#
# Env: RELEASE_GIT_TOKEN (GitHub token of the release bot; secret),
#      RELEASE_GIT_NAME, RELEASE_GIT_EMAIL, BUILD_VCS_NUMBER.
# shellcheck source=SCRIPTDIR/_common.sh
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

[[ -n "${RELEASE_GIT_TOKEN:-}" ]] || die "RELEASE_GIT_TOKEN is not set"
export GIT_AUTHOR_NAME="${RELEASE_GIT_NAME:?}" GIT_COMMITTER_NAME="${RELEASE_GIT_NAME:?}"
export GIT_AUTHOR_EMAIL="${RELEASE_GIT_EMAIL:?}" GIT_COMMITTER_EMAIL="${RELEASE_GIT_EMAIL:?}"

# TeamCity may leave a detached HEAD; release from the exact built revision.
git checkout --quiet -B main "${BUILD_VCS_NUMBER:-HEAD}"
fetch_tags

plan="$WORK/plan.json"
release plan --mode release >"$plan"
if [[ "$(jq 'length' "$plan")" -eq 0 ]]; then
  info "nothing to release"
  publish_dist
  exit 0
fi
info "plan: $(jq -r 'map(.id + " " + .version) | join(", ")' "$plan")"

while IFS=$'\t' read -r id ver; do
  release apply --id "$id" --version "$ver"
done < <(jq -r '.[] | [.id, .version] | @tsv' "$plan")
release finalize --ids "$(jq -r 'map(.id) | join(",")' "$plan")"
mapfile -t tags < <(jq -r '.[] | "refs/tags/\(.id)/v\(.version)"' "$plan")

build_plan "$plan"

# Credentials only from here on, and only for git: an HTTP header injected
# through GIT_CONFIG_* so the token is never in argv, a URL or ~/.gitconfig.
GIT_AUTH="$(printf 'x-access-token:%s' "$RELEASE_GIT_TOKEN" | base64 -w0)"
export GIT_TERMINAL_PROMPT=0 GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="http.https://github.com/.extraheader"
export GIT_CONFIG_VALUE_0="AUTHORIZATION: basic $GIT_AUTH"
unset GIT_AUTH RELEASE_GIT_TOKEN

info "push main and ${#tags[@]} tag(s)"
git push --atomic origin HEAD:refs/heads/main "${tags[@]}"

info "merge main into develop"
git fetch --quiet --no-tags origin +refs/heads/develop:refs/remotes/origin/develop
git checkout --quiet -B develop origin/develop
if ! git merge --no-ff --no-edit -m "chore(release): merge main into develop" main; then
  git merge --abort || true
  die "main released and pushed, but merging it into develop conflicted. A maintainer must merge main into develop by hand."
fi
git push origin HEAD:refs/heads/develop
git checkout --quiet main
publish_dist
