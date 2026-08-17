#!/usr/bin/env bash
# Build the fnOS .fpk package for MiBee NVR.
#
# This produces an OFFLINE .fpk: both arch images are docker-saved into the
# package, and cmd/install_init loads the matching one at install time, so the
# NAS never has to pull from ghcr.io (the single biggest source of slow fnOS
# installs in mainland China).
#
# Requirements:
#   - fnpack (https://github.com/ckcoding/fnnas-docs → cli/fnpack) on PATH,
#     or set FNPACK_BIN (Windows: point at fnpack.exe).
#   - Docker, with the two arch images already built locally and tagged
#     mibee-nvr:<version>-amd64 and mibee-nvr:<version>-arm64. See the
#     ARCHES loop below; to build them use:
#       docker buildx build --platform linux/amd64 --build-arg VERSION=<ver> \
#         -t mibee-nvr:<ver>-amd64 --load -f deploy/docker/Dockerfile .
#       (same for linux/arm64 → -arm64)
#
# Usage:
#   ./deploy/fnos/build.sh <version>      # e.g. ./deploy/fnos/build.sh 0.10.1
#   VERSION=0.10.1 ./deploy/fnos/build.sh
#
# The <version> MUST be X.Y.Z (fnOS requirement) and is written into both
# manifest.version and the docker image tag, keeping them in lockstep. A
# mismatch causes a misleading "manifest unknown" pull error on the NAS.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# ONLINE=1 builds the online .fpk: no bundled image tars — cmd/main probes
# ghcr vs the ACR mirror at start and pulls the matching arch. The online
# package is tiny (~KB) but requires the NAS to reach a registry on install.
# ONLINE uses EMPTY/1 (not 0/1): the canonical-name suffix below relies on
# ${ONLINE:+-online}, which tests NON-EMPTY — a literal "0" would wrongly add
# the suffix to offline builds too (v0.11.0: both jobs emitted -online- names
# and the offline upload clobbered the small online package).
ONLINE=""
if [ "${1:-}" = "--online" ]; then ONLINE=1; shift; fi
VERSION="${1:-${VERSION:-}}"
# fnpack binary; on Windows set FNPACK_BIN to the path of fnpack.exe.
FNPACK="${FNPACK_BIN:-fnpack}"
# Arches to bundle. Override with FNOS_ARCHES="amd64" for a single-arch fpk.
ARCHES="${FNOS_ARCHES:-amd64 arm64}"

if [ -z "$VERSION" ]; then
  echo "ERROR: version required (X.Y.Z). Usage: $0 <version>" >&2
  exit 1
fi

if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "ERROR: version must be X.Y.Z (got '$VERSION'); strip pre-release suffixes." >&2
  exit 1
fi

if ! command -v "$FNPACK" >/dev/null 2>&1; then
  echo "ERROR: fnpack not found on PATH. Install it or set FNPACK_BIN." >&2
  echo "       https://github.com/ckcoding/fnnas-docs/blob/main/docs/cli/fnpack.md" >&2
  exit 1
fi

if [ "$ONLINE" -eq 0 ] && ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker not found on PATH (needed to save the bundled images)." >&2
  exit 1
fi

# ---- 1. Save the per-arch images into app/images/ (offline only) -------------
# Each tarball is saved with the UNIFIED tag mibee-nvr:<version> (the tag the
# compose references), so install_init only needs `docker load` — no re-tagging
# at install time. We can't keep both arches tagged identically in the local
# daemon simultaneously (a tag names one image), so we tag→save→drop per arch.
IMAGES_DIR="$SCRIPT_DIR/app/images"
mkdir -p "$IMAGES_DIR"
# Clean stale tars so a half-built arch can't leak through (offline), and so
# the online package never accidentally bundles an old tar.
rm -f "$IMAGES_DIR"/mibee-nvr-*.tar

UNIFIED_TAG="mibee-nvr:${VERSION}"
if [ "$ONLINE" -eq 1 ]; then
  echo "Online mode: skipping bundled images (cmd/main pulls at install time)."
else
for arch in $ARCHES; do
  src="mibee-nvr:${VERSION}-${arch}"
  out="$IMAGES_DIR/mibee-nvr-${arch}.tar"
  if ! docker image inspect "$src" >/dev/null 2>&1; then
    echo "ERROR: image '$src' not found locally." >&2
    echo "       Build it first, e.g.:" >&2
    echo "         docker buildx build --platform linux/${arch} \\" >&2
    echo "           --build-arg VERSION=${VERSION} -t ${src} --load \\" >&2
    echo "           -f deploy/docker/Dockerfile ." >&2
    exit 1
  fi
  echo "Saving ${src} → ${out} (as ${UNIFIED_TAG})..."
  # Re-tag to the unified name, save, then remove the unified tag so the next
  # arch can claim it. (Removing the tag does NOT remove the image layers.)
  docker tag "$src" "$UNIFIED_TAG"
  docker save -o "$out" "$UNIFIED_TAG"
  docker rmi "$UNIFIED_TAG" >/dev/null 2>&1 || true
done
fi

# ---- 2. Bake the version into manifest + compose ---------------------------
# Inject the version into the manifest (idempotent).
tmp_manifest="$(mktemp)"
sed -E "s/^version=.*/version=${VERSION}/" "$SCRIPT_DIR/manifest" > "$tmp_manifest"
mv "$tmp_manifest" "$SCRIPT_DIR/manifest"

# Resolve ${VERSION} in docker-compose.yaml. fnpack does NOT substitute it
# (fnOS does not inject a VERSION env at install time), so the placeholder must
# be baked here — otherwise manifest.version and the image tag drift apart and
# the pull/load fails with a misleading "manifest unknown". The source keeps
# ${VERSION} so it stays readable; we restore it after packing.
compose_file="$SCRIPT_DIR/app/docker/docker-compose.yaml"
# Back up to a temp file OUTSIDE the app/ tree — fnpack packs everything under
# app/, so a sibling .bak would leak into the .fpk. Restore from temp on exit.
compose_bak="$(mktemp)"
cp "$compose_file" "$compose_bak"
sed -E "s#\\\$\\{VERSION\\}#${VERSION}#g" "$compose_file" > "$compose_file.tmp"
mv "$compose_file.tmp" "$compose_file"
restore_compose() { cp "$compose_bak" "$compose_file" && rm -f "$compose_bak"; }
trap restore_compose EXIT

# Ensure lifecycle scripts are executable before packing. Files created on
# Windows lack the Unix +x bit; without it fnOS silently skips the cmd/ hooks
# (the script never starts — no log, no effect), which presents as a bare
# "No such image" because install_callback's docker load never ran. fnpack
# preserves the on-disk mode into the .fpk, so this must happen before build.
chmod +x "$SCRIPT_DIR"/cmd/*
# Windows checkouts carry CRLF line endings; a "#!/bin/bash\r" shebang fails on
# the NAS with a confusing "bad interpreter". Strip CR bytes in-place — no-op
# when already LF. (fnpack preserves on-disk bytes/mode into the .fpk.)
for f in "$SCRIPT_DIR"/cmd/*; do
  if grep -q $'\r' "$f"; then
    echo "normalizing CRLF -> LF: $f"
    tr -d '\r' < "$f" > "$f.tmp" && mv "$f.tmp" "$f"
  fi
done

# ---- 3. Pack ---------------------------------------------------------------
echo "Building .fpk with version ${VERSION}..."
( cd "$SCRIPT_DIR" && "$FNPACK" build )

# ---- 4. Canonical output name ------------------------------------------------
# fnpack derives its own filename from the manifest; rename to a deterministic
# asset name so release.yml can attach both editions side by side:
#   offline → mibee-nvr-fnos-<ver>.fpk      online → mibee-nvr-fnos-online-<ver>.fpk
OUT="$SCRIPT_DIR/mibee-nvr-fnos${ONLINE:+-online}-${VERSION}.fpk"
PRODUCED="$(ls -1t "$SCRIPT_DIR"/*.fpk 2>/dev/null | head -1 || true)"
if [ -n "$PRODUCED" ] && [ "$(basename "$PRODUCED")" != "$(basename "$OUT")" ]; then
  mv -f "$PRODUCED" "$OUT"
fi
[ -f "$OUT" ] || { echo "ERROR: no .fpk produced." >&2; exit 1; }

echo ""
echo "Done. Output: $OUT"
echo "Install via fnOS 应用中心 → 手动安装."
