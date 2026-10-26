# MiBee NVR — fnOS native package (non-Docker)

A **native** fnOS (.fpk) package variant that installs alongside the Docker
package (`deploy/fnos/`): same app, different appname (`mibee-nvr-native`),
different port (:9091) and its own data directory, so both can coexist on one
box — e.g. running stable on Docker while smoke-testing a fresh build natively.

## Why native

- No Docker dependency at all: one static binary (`CGO_ENABLED=0`, embedded
  web UI) + SQLite — the fnOS app center manages the lifecycle directly.
- ~30 MB RAM idle footprint vs a full container stack.
- Installs/upgrades in seconds (fpk is ~⅓ the size of the binary).

**Limitation**: the Docker image bundles FFmpeg; this native build does not.
Transcoding-dependent features (transcode targets, pixel-gate analysis) are
unavailable — recording / live view / web UI / API / notifications all work.

## Layout (fnpack native template)

```
manifest               appname=mibee-nvr-native, platform=arm (aarch64)
ICON.PNG ICON_256.PNG  copied from ../fnos/ by build.sh
config/privilege       run-as: package (dedicated system user)
config/resource        {}
cmd/main               start/stop/status + first-boot config generation
app/bin/mibee-nvr      the linux/arm64 static binary (built by build.sh)
app/ui/                desktop launcher config + icons (URL mode, port 9091)
```

First boot generates `${TRIM_PKGVAR}/mibee-nvr.yaml` (TRIM_PKGVAR =
`/vol1/@appdata/mibee-nvr-native`) with port-clash-free defaults: HTTP :9091,
RTSP/FTP/LAN-discovery off, recordings under the package data dir, admin
password `mibee-native` (change it). Edit the yaml + restart to customize —
the full option set is documented in the top-level `docs/`.

## Build

`bash deploy/fnos-native/build.sh <version>` from the repo root: builds the
SPA, embeds it, cross-compiles linux/arm64, assembles the fnpack project and
copies the icons. The final `fnpack build` step must run where `fnpack` is
installed (on the fnOS box itself or a build VM) — it just packs the assembled
directory:

```
fnpack build -d deploy/fnos-native/out/mibee-nvr-native
sudo appcenter-cli install-fpk <out>.fpk        # then: appcenter-cli start mibee-nvr-native
```
