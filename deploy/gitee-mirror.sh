#!/usr/bin/env bash
# Upload GitHub release assets (≤100MB) to the Gitee mirror repo's releases.
#
# GitHub Actions CANNOT do this: Gitee stalls large POST bodies from
# GitHub-hosted runner IPs (verified 2026-09-18 — a 65MB binary made zero byte
# progress across 4×15min curl retries, while a git push seconds earlier
# succeeded). CI's mirror-to-gitee job therefore only creates the tag +
# release shell; run this from a mainland-side machine after each release to
# fill it. Fully idempotent: re-runs skip attachments that already match by
# name+size and replace mismatched ones.
#
# Usage:
#   GITEE_TOKEN=<pat> deploy/gitee-mirror.sh                # sweep all releases
#   GITEE_TOKEN=<pat> deploy/gitee-mirror.sh v0.12.0        # one tag
#
# Token: Gitee personal access token (projects scope) of the mirror owner
# (same secret as the repo Variable ENABLE_GITEE / Secret GITEE_TOKEN setup).
set -uo pipefail

GH_REPO=Mi-Bee-Studio/MiBeeNvr
GITEE_SLUG=Mi-Bee-Studio/MiBeeNvr
API="https://gitee.com/api/v5/repos/${GITEE_SLUG}"
AUTH="Authorization: Bearer ${GITEE_TOKEN:?GITEE_TOKEN env required}"
MAX_BYTES=104857600            # Gitee's ~100MB single-attachment cap
CACHE="${TMPDIR:-/tmp}/gitee-mirror-assets"
declare -a FAILURES=()

mkdir -p "$CACHE"

# Attach one asset file to a Gitee release, replacing any same-named
# attachment. Usage: attach <release_id> <file>
attach() {
  local rel_id=$1 f=$2 name aid
  name=$(basename "$f")
  for aid in $(curl -sf --retry 3 -H "$AUTH" "${API}/releases/${rel_id}/attach_files" \
    | jq -r --arg n "$name" '(. // [])[] | select(.name == $n) | .id'); do
    echo "  replace existing ${name} (attachment ${aid})"
    curl -sf --retry 3 -X DELETE -H "$AUTH" \
      "${API}/releases/${rel_id}/attach_files/${aid}" || return 1
  done
  # Gitee quirks (verified against the live API): multipart field is `file`
  # (NOT the `files[]` some community docs claim) and bursts are throttled.
  curl -sf --retry 3 --retry-delay 5 --connect-timeout 30 --max-time 1800 \
    -X POST -H "$AUTH" -F "file=@${f}" \
    "${API}/releases/${rel_id}/attach_files" >/dev/null
}

mirror_tag() {
  local tag=$1 meta pre rel_id name size f want have
  echo "=== ${tag} ==="
  meta=$(gh api "repos/${GH_REPO}/releases/tags/${tag}") || { FAILURES+=("${tag}: not on GitHub"); return; }

  # Gitee tag must exist (CI's mirror-to-gitee job pushes it; the repo's own
  # git mirror sync is a slower fallback)
  if ! curl -sf --retry 3 -H "$AUTH" "${API}/tags?per_page=100" \
    | jq -re --arg t "$tag" '[.[].name] | index($t)' >/dev/null; then
    echo "  tag missing on Gitee — skipping (wait for CI/sync, then re-run)"
    FAILURES+=("${tag}: tag missing on Gitee")
    return
  fi

  # Find-or-create the release shell. Quirk: GET releases/tags/{tag} answers
  # 200 with body `null` when missing — test the parsed id, not the code;
  # POST /releases demands target_commitish (empty string OK, tag exists).
  rel_id=$(curl -sf -H "$AUTH" "${API}/releases/tags/${tag}" | jq -r '.id // empty')
  if [ -z "$rel_id" ]; then
    pre=$(jq -r '.prerelease' <<<"$meta")
    rel_id=$(curl -sf --retry 3 -X POST -H "$AUTH" -H "Content-Type: application/json" \
      -d "$(jq -n --arg t "$tag" --argjson p "$pre" \
            '{tag_name:$t,name:$t,target_commitish:"",prerelease:$p,
              body:("MiBee NVR "+$t+" — 完整发布说明（含更新日志）见 GitHub：https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/tag/"+$t)}')" \
      "${API}/releases" | jq -r '.id // empty') || true
    [ -n "$rel_id" ] || { FAILURES+=("${tag}: create release"); return; }
    echo "  created release (id ${rel_id})"
  fi

  while IFS=$'\t' read -r name size; do
    [ -n "$name" ] || continue
    # Already attached with the exact size → converged, skip
    have=$(curl -sf --retry 3 -H "$AUTH" "${API}/releases/${rel_id}/attach_files" \
      | jq -r --arg n "$name" --argjson s "$size" \
        '(. // [])[] | select(.name == $n) | .size' | head -1)
    if [ "$have" = "$size" ]; then
      echo "  ok ${name} (already attached)"
      continue
    fi
    # Fetch into the local cache (resumable: reuse files with matching size)
    f="${CACHE}/${tag}/${name}"; mkdir -p "${CACHE}/${tag}"
    if [ ! -f "$f" ] || [ "$(stat -c%s "$f")" != "$size" ]; then
      echo "  download ${name} ($((size / 1048576))MB)"
      gh release download "$tag" -R "$GH_REPO" --pattern "$name" \
        --dir "${CACHE}/${tag}" --clobber || { FAILURES+=("${tag}: download ${name}"); continue; }
    fi
    echo "  upload ${name} ($((size / 1048576))MB)"
    attach "$rel_id" "$f" || { FAILURES+=("${tag}: upload ${name}"); continue; }
    sleep 1
  done < <(jq -r --argjson m "$MAX_BYTES" \
    '.assets[] | select(.size <= $m) | "\(.name)\t\(.size)"' <<<"$meta")
}

if [ $# -gt 0 ]; then
  for tag in "$@"; do mirror_tag "$tag"; done
else
  for tag in $(gh api "repos/${GH_REPO}/releases?per_page=100" --paginate --jq '.[].tag_name'); do
    mirror_tag "$tag"
  done
fi

echo ""
if [ "${#FAILURES[@]}" -gt 0 ]; then
  echo "=== failures ==="
  printf '%s\n' "${FAILURES[@]}"
  exit 1
fi
echo "=== mirror converged ==="
