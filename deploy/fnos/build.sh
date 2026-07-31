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
FNPACK_BIN="${FNPACK_BIN:-fnpack}"

if [ -z "$VERSION" ]; then
  echo "ERROR: version required (X.Y.Z). Usage: $0 <version>" >&2
  exit 1
fi

if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "ERROR: version must be X.Y.Z (got '$VERSION'); strip pre-release suffixes." >&2
  exit 1
fi

if ! command -v "$FNPACK_BIN" >/dev/null 2>&1; then
  echo "ERROR: fnpack not found on PATH. Install it or set FNPACK_BIN." >&2
  echo "       https://github.com/ckcoding/fnnas-docs/blob/main/docs/cli/fnpack.md" >&2
  exit 1
fi

# Inject the version into the manifest (idempotent).
tmp_manifest="$(mktemp)"
# Replace the version= line, preserving the rest.
sed -E "s/^version=.*/version=${VERSION}/" "$SCRIPT_DIR/manifest" > "$tmp_manifest"
mv "$tmp_manifest" "$SCRIPT_DIR/manifest"

# Build. fnpack picks up the image tag from compose via the VERSION env var
# (docker-compose.yaml uses ${VERSION:-latest}).
echo "Building .fpk with version ${VERSION}..."
( cd "$SCRIPT_DIR" && VERSION="$VERSION" fnpack build )

echo "Done. Install the generated .fpk via fnOS 应用中心 → 手动安装,"
echo "or submit it through the fnOS developer channel."
