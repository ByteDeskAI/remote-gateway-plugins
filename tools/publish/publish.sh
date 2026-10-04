#!/usr/bin/env bash
# Publish every built plugin under --dist to one ByteDesk Store.
#
#   publish.sh --store https://store.dev.bytedesk.ai --dist dist
#
# Input: the tools/build output. Each directory holding a <id>-<version>.tar.gz
# must also hold SHA256SUMS and provenance.json for that archive (the runner
# writes one plugin per directory, e.g. dist/<id>/).
#
# For each archive, in order:
#   1. verify   sha256 == SHA256SUMS line == provenance.json .sha256, and
#               provenance id/version/archive and the archive's own
#               <id>/plugin.json agree with the file name
#   2. POST     /v1/admin/packages                         catalog row (upsert)
#   3. PUT      /v1/admin/packages/<id>/versions/<v>/artifact?latest=1
#               200 = same bytes already there (success), 201 = new,
#               409 = different bytes under that version (hard failure)
#   4. GET      /v1/admin/packages/<id>                   sha256 must match
#
# Auth: STORE_ADMIN_TOKEN from the environment only, sent as a Bearer header
# through a mode-600 curl config file. It never appears in argv or output.
# Do not add `set -x` to this file.
set -euo pipefail

info() { printf 'publish: %s\n' "$*"; }
die() { printf 'publish: error: %s\n' "$*" >&2; exit 1; }

STORE_URL=""
DIST=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --store) STORE_URL="${2:-}"; shift 2 ;;
    --dist) DIST="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,23p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ -n "$STORE_URL" ]] || die "--store is required"
[[ -d "$DIST" ]] || die "--dist must be a directory"
[[ -n "${STORE_ADMIN_TOKEN:-}" ]] || die "STORE_ADMIN_TOKEN is not set"
for c in curl jq sha256sum tar; do command -v "$c" >/dev/null || die "missing command: $c"; done
STORE_URL="${STORE_URL%/}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
CFG="$WORK/curl.cfg"
( umask 077; printf 'header = "Authorization: Bearer %s"\n' "$STORE_ADMIN_TOKEN" >"$CFG" )
unset STORE_ADMIN_TOKEN

CODE=""
BODY="$WORK/body"
# request METHOD URL [curl args...]; sets CODE, body in $BODY.
request() {
  local m="$1" u="$2"; shift 2
  CODE="$(curl -sS -K "$CFG" -X "$m" -o "$BODY" -w '%{http_code}' "$@" "$u")" || die "$m $u: request failed"
}

mapfile -t ARCHIVES < <(find "$DIST" -type f -name '*.tar.gz' | sort)
[[ ${#ARCHIVES[@]} -gt 0 ]] || { info "no archives under $DIST; nothing to publish"; exit 0; }

# Verify everything before uploading anything.
declare -a IDS VERS SHAS ROWS
for a in "${ARCHIVES[@]}"; do
  dir="$(dirname "$a")"; file="$(basename "$a")"
  [[ -f "$dir/SHA256SUMS" && -f "$dir/provenance.json" ]] || die "$a: SHA256SUMS or provenance.json missing beside it"
  sha="$(sha256sum "$a" | cut -d' ' -f1)"
  listed="$(awk -v f="$file" '$2 == f { print $1 }' "$dir/SHA256SUMS")"
  [[ "$listed" == "$sha" ]] || die "$file: sha256 $sha does not match SHA256SUMS (${listed:-absent})"

  top="$(tar -tzf "$a" | head -n 1 | cut -d/ -f1)" || true
  man="$(tar -xzOf "$a" "$top/plugin.json" 2>/dev/null | head -c 1048576)" || true
  jq -e . >/dev/null 2>&1 <<<"$man" || die "$file: no readable <id>/plugin.json inside"
  id="$(jq -r '.id // ""' <<<"$man")"
  ver="$(jq -r '.version // ""' <<<"$man")"
  [[ "$top" == "$id" && "$file" == "$id-$ver.tar.gz" ]] || die "$file: archive manifest says $id $ver (top folder $top)"

  jq -e --arg id "$id" --arg v "$ver" --arg f "$file" --arg s "$sha" \
    '.id == $id and .version == $v and .archive == $f and .sha256 == $s' \
    "$dir/provenance.json" >/dev/null || die "$file: provenance.json does not match the archive"

  # Catalog row from the contract-2 manifest, mirroring bytedesk-capture's
  # publish-to-store.sh. kind "process" (or absent) is a Store process_plugin.
  row="$(jq -c '{
      id,
      name: (.identity.displayName // .name // .id),
      description: (.identity.description // .description // ""),
      publisher: (.publisher.id // .publisher // "bytedesk"),
      kind: (if (.kind // "process") == "process" then "process_plugin" else .kind end),
      category: (.category // "plugins"),
      pricing: (.pricing.model // .pricing // "free"),
      sku: (.pricing.sku // ""),
      targets: (.targets // ["gateway"]),
      min_core_version: (.minCoreVersion // .min_core_version // "")
    } | with_entries(select(.value != ""))' <<<"$man")"

  IDS+=("$id"); VERS+=("$ver"); SHAS+=("$sha"); ROWS+=("$row")
  info "verified $file sha256=$sha"
done

for i in "${!ARCHIVES[@]}"; do
  a="${ARCHIVES[$i]}" id="${IDS[$i]}" ver="${VERS[$i]}" sha="${SHAS[$i]}"
  info "$id $ver -> $STORE_URL"

  printf '%s' "${ROWS[$i]}" >"$WORK/row.json"
  request POST "$STORE_URL/v1/admin/packages" -H 'Content-Type: application/json' --data-binary @"$WORK/row.json"
  [[ "$CODE" == 200 || "$CODE" == 201 ]] || die "POST /v1/admin/packages ($id): HTTP $CODE $(head -c 500 "$BODY")"

  request PUT "$STORE_URL/v1/admin/packages/$id/versions/$ver/artifact?latest=1" \
    -H 'Content-Type: application/gzip' --data-binary @"$a"
  case "$CODE" in
    201) info "uploaded $id $ver" ;;
    200) info "$id $ver already published with the same bytes" ;;
    409) die "$id $ver already exists on $STORE_URL with DIFFERENT bytes. Store versions are immutable; never use ?force=1 here." ;;
    *) die "PUT artifact ($id $ver): HTTP $CODE $(head -c 500 "$BODY")" ;;
  esac

  request GET "$STORE_URL/v1/admin/packages/$id"
  [[ "$CODE" == 200 ]] || die "GET /v1/admin/packages/$id: HTTP $CODE"
  remote="$(jq -r --arg v "$ver" '[.versions[]? | select(.version == $v) | .sha256][0] // ""' "$BODY")"
  [[ "$remote" == "$sha" ]] || die "$id $ver: Store serves sha256 ${remote:-none}, expected $sha"
  info "PASS $id $ver sha256 matches"
done
info "published ${#ARCHIVES[@]} package(s) to $STORE_URL"
