#!/usr/bin/env bash
# Build the fnOS .fpk package for MiBee NVR.
#
# Requirements: fnpack (https://github.com/ckcoding/fnnas-docs → cli/fnpack)
# installed and on PATH (or set FNPACK_BIN).
#
# Usage:
#   ./deploy/fnos/build.sh <version>      # e.g. ./deploy/fnos/build.sh 0.10.0
#   VERSION=0.10.0 ./deploy/fnos/build.sh
#
# The <version> MUST be X.Y.Z (fnOS requirement) and is written into both
# manifest.version and the docker image tag, keeping them in lockstep. A
# mismatch causes a misleading "manifest unknown" pull error on the NAS.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERSION="${1:-${VERSION:-}}"
# fnpack binary; on Windows set FNPACK_BIN to the path of fnpack.exe.
FNPACK="${FNPACK_BIN:-fnpack}"

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

# Inject the version into the manifest (idempotent).
tmp_manifest="$(mktemp)"
# Replace the version= line, preserving the rest.
sed -E "s/^version=.*/version=${VERSION}/" "$SCRIPT_DIR/manifest" > "$tmp_manifest"
mv "$tmp_manifest" "$SCRIPT_DIR/manifest"

# Bake the version into docker-compose.yaml. fnpack does NOT substitute
# ${VERSION} (unlike docker compose at runtime, fnOS does not inject a VERSION
# env at install time), so the placeholder must be resolved here — otherwise
# manifest.version and the image tag drift apart and the pull fails with a
# misleading "manifest unknown". The source keeps ${VERSION:-latest} so it
# stays usable for manual `docker compose up`; we restore it after packing.
compose_file="$SCRIPT_DIR/app/docker/docker-compose.yaml"
cp "$compose_file" "$compose_file.bak"
sed -E "s#\\\$\\{VERSION:-latest\\}#${VERSION}#g" "$compose_file" > "$compose_file.tmp"
mv "$compose_file.tmp" "$compose_file"
restore_compose() { mv "$compose_file.bak" "$compose_file"; }
trap restore_compose EXIT

# Build.
echo "Building .fpk with version ${VERSION}..."
( cd "$SCRIPT_DIR" && "$FNPACK" build )

echo "Done. Install the generated .fpk via fnOS 应用中心 → 手动安装,"
echo "or submit it through the fnOS developer channel."
