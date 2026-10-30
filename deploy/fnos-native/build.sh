#!/usr/bin/env bash
# Assemble the fnOS native fpk project for MiBee NVR. Run from repo root:
#   bash deploy/fnos-native/build.sh [version]
# Produces deploy/fnos-native/out/mibee-nvr-native/ ready for `fnpack build`.
set -euo pipefail

VERSION="${1:-0.0.0}"
# Target architecture: "arm64" (fnOS platform=arm, the default - aarch64 NAS)
# or "amd64" (fnOS platform=x86 - fnpack accepts platform values
# x86 / arm / loongarch / risc-v / all).
ARCH="${FNOS_ARCH:-arm64}"
case "${ARCH}" in
  arm64) FN_PLATFORM="arm" ;;
  amd64) FN_PLATFORM="x86" ;;
  *) echo "unsupported ARCH (want arm64|amd64)" >&2; exit 2 ;;
esac
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERE="${ROOT}/deploy/fnos-native"
OUT="${HERE}/out/mibee-nvr-native-${ARCH}"

cd "${ROOT}"

echo "==> building web UI"
( cd web && npm ci --no-audit --no-fund && npm run build )

echo "==> embedding SPA into internal/ui/static"
rm -rf internal/ui/static/assets internal/ui/static/ort internal/ui/static/ort.min.js \
       internal/ui/static/index.html internal/ui/static/manifest.json \
       internal/ui/static/favicon.svg internal/ui/static/icons.svg internal/ui/static/sw.js
mkdir -p internal/ui/static
cp -r web/dist/* internal/ui/static/

echo "==> cross-compiling linux/${ARCH} static binary (fnOS platform=${FN_PLATFORM})"
CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} \
  go build -trimpath -ldflags "-s -w -X main.appVersion=${VERSION}" \
  -o "${OUT}/app/bin/mibee-nvr" ./cmd/mibee-nvr/

echo "==> assembling fnpack project"
mkdir -p "${OUT}/app/ui/images" "${OUT}/config" "${OUT}/cmd"
cp "${HERE}/manifest" "${OUT}/manifest"
sed -i "s/^platform .*/platform               = ${FN_PLATFORM}/" "${OUT}/manifest"
sed -i "s/^version               = .*/version               = ${VERSION}/" "${OUT}/manifest"
cp "${HERE}/cmd/main" "${OUT}/cmd/main"
chmod +x "${OUT}/cmd/main"
for hook in install_init install_callback uninstall_init uninstall_callback \
            upgrade_init upgrade_callback config_init config_callback; do
  printf '#!/bin/bash\nexit 0\n' > "${OUT}/cmd/${hook}"
  chmod +x "${OUT}/cmd/${hook}"
done
cp "${HERE}/config/privilege" "${HERE}/config/resource" "${OUT}/config/"
cp "${HERE}/app/ui/config" "${OUT}/app/ui/config"
cp "${ROOT}/deploy/fnos/ICON.PNG" "${ROOT}/deploy/fnos/ICON_256.PNG" "${OUT}/"
cp "${ROOT}/deploy/fnos/app/ui/images/icon_64.png" \
   "${ROOT}/deploy/fnos/app/ui/images/icon_256.png" "${OUT}/app/ui/images/"

echo "==> project assembled at ${OUT}"
echo "    next (where fnpack exists): fnpack build -d ${OUT}"
