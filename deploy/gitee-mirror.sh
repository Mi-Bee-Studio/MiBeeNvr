#!/usr/bin/env bash
# Keep the Gitee mirror holding EXACTLY ONE version: the latest stable GitHub
# release. Gitee caps total attachment storage, so the mirror intentionally
# does not accumulate history — old releases (pages + attachments) are pruned;
# git tags stay (tiny, and needed by CI's tag-push flow).
#
# GitHub Actions cannot do the upload (Gitee stalls large POST bodies from
# GHA runner IPs — verified 2026-09-18), so run this from a mainland-side
# machine after each release. CI's mirror-to-gitee job already pushes the tag,
# creates the release shell and prunes old versions; this script fills the
# assets of the latest stable and re-prunes anything that slipped through.
#
# Usage:
#   GITEE_TOKEN=<pat> deploy/gitee-mirror.sh                # sync latest stable + prune others
#   GITEE_TOKEN=<pat> deploy/gitee-mirror.sh v0.12.0        # fill one tag's assets only (no pruning)
#
# Idempotent: attached assets with matching name+size are skipped, mismatched
# ones replaced; pruning skips already-absent releases. Assets >100MB (the
# ~160MB .fpk bundles) stay GitHub-only — Gitee's single-attachment cap.
set -uo pipefail

GH_REPO=Mi-Bee-Studio/MiBeeNvr
GITEE_SLUG=Mi-Bee-Studio/MiBeeNvr
API="https://gitee.com/api/v5/repos/${GITEE_SLUG}"
AUTH="Authorization: Bearer ${GITEE_TOKEN:?GITEE_TOKEN env required}"
MAX_BYTES=104857600
CACHE="${TMPDIR:-/tmp}/gitee-mirror-assets"
declare -a FAILURES=()

mkdir -p "$CACHE"

gitee_rel_id() {
  # Quirk: GET releases/tags/{tag} answers 200 with body `null` when missing —
  # test the parsed id, never the HTTP code.
  curl -sf --retry 3 -H "$AUTH" "${API}/releases/tags/$1" | jq -r '.id // empty'
}

# Attach one asset file, replacing any same-named attachment. Usage: attach <release_id> <file>
attach() {
  local rel_id=$1 f=$2 name aid
  name=$(basename "$f")
  for aid in $(curl -sf --retry 3 -H "$AUTH" "${API}/releases/${rel_id}/attach_files" \
    | jq -r --arg n "$name" '(. // [])[] | select(.name == $n) | .id'); do
    echo "  replace existing ${name} (attachment ${aid})"
    curl -sf --retry 3 -X DELETE -H "$AUTH" \
      "${API}/releases/${rel_id}/attach_files/${aid}" || return 1
  done
  # Quirk: multipart field is `file` (NOT the `files[]` some community docs
  # claim); Gitee throttles API bursts, hence the sleeps at call sites.
  curl -sf --retry 3 --retry-delay 5 --connect-timeout 30 --max-time 1800 \
    -X POST -H "$AUTH" -F "file=@${f}" \
    "${API}/releases/${rel_id}/attach_files" >/dev/null
}

mirror_tag() {
  local tag=$1 meta pre rel_id name size f have
  echo "=== ${tag} ==="
  meta=$(gh api "repos/${GH_REPO}/releases/tags/${tag}") || { FAILURES+=("${tag}: not on GitHub"); return; }

  # The Gitee tag must exist — CI's mirror-to-gitee job pushes it on release;
  # the repo's own git mirror sync is the slower fallback.
  if ! curl -sf --retry 3 -H "$AUTH" "${API}/tags?per_page=100" \
    | jq -e --arg t "$tag" '[.[].name] | index($t)' >/dev/null; then
    echo "  tag missing on Gitee — skipping (wait for CI/sync, then re-run)"
    FAILURES+=("${tag}: tag missing on Gitee")
    return
  fi

  rel_id=$(gitee_rel_id "$tag")
  if [ -z "$rel_id" ]; then
    pre=$(jq -r '.prerelease' <<<"$meta")
    # Quirk: POST /releases demands target_commitish (empty string is fine —
    # the tag already exists).
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
    have=$(curl -sf --retry 3 -H "$AUTH" "${API}/releases/${rel_id}/attach_files" \
      | jq -r --arg n "$name" --argjson s "$size" \
        '(. // [])[] | select(.name == $n and .size == $s) | .id' | head -1)
    if [ -n "$have" ]; then
      echo "  ok ${name} (already attached)"
      continue
    fi
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

prune_others() {
  local keep=$1 tag id
  # Enumerate candidates from the GitHub side: every Gitee release we ever
  # create corresponds to a GitHub release, and the Gitee releases LIST
  # endpoint proved unreliable (drops entries under load).
  for tag in $(gh api "repos/${GH_REPO}/releases?per_page=100" --paginate --jq '.[].tag_name'); do
    [ "$tag" = "$keep" ] && continue
    id=$(gitee_rel_id "$tag")
    if [ -n "$id" ]; then
      echo "prune ${tag} (release ${id})"
      curl -sf --retry 3 -X DELETE -H "$AUTH" "${API}/releases/${id}" \
        || FAILURES+=("prune ${tag}")
      sleep 1
    fi
  done
}

if [ $# -gt 0 ]; then
  for tag in "$@"; do mirror_tag "$tag"; done
else
  # Latest stable only — releases list is reverse-chronological, pick the
  # first non-prerelease.
  LATEST=$(gh api "repos/${GH_REPO}/releases?per_page=100" --paginate \
    --jq '[.[] | select(.prerelease == false)][0].tag_name') \
    || { echo "cannot resolve latest stable tag" >&2; exit 1; }
  mirror_tag "$LATEST"
  prune_others "$LATEST"
fi

echo ""
if [ "${#FAILURES[@]}" -gt 0 ]; then
  echo "=== failures ==="
  printf '%s\n' "${FAILURES[@]}"
  exit 1
fi
echo "=== mirror converged (latest stable only) ==="
