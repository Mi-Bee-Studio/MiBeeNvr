# Deploy on 飞牛 fnOS

> Install MiBee NVR on fnOS via a `.fpk` package. The package wraps the existing multi-arch Docker image; no backend code changes are involved. fnOS has the most complete official developer toolchain (`fnpack`) of the target platforms.

## Why host networking

ONVIF WS-Discovery uses UDP multicast (`239.255.255.250:3702`), which Docker's default bridge blocks. The package runs the container with `network_mode: host` so camera auto-discovery works, and sets `manifest: checkport=false` since a host-network multi-port service has no single meaningful `service_port`. If `9090` (Web) or `2121` (FTP) is already in use on the host: for the Web port, prefer changing it via **Web UI → Settings → General → "Web UI port"** after install, or set the `NVR_LISTEN_PORT` environment variable before deploying; for FTP, change `ftp.port` in `mibee-nvr.yaml`.

## Package contents

The `.fpk` source lives in [`deploy/fnos/`](../../deploy/fnos):

```
deploy/fnos/
├── manifest                     # app metadata (INI), version injected at build
├── ICON.PNG  ICON_256.PNG       # 64/256 icons (replace placeholders with real logo)
├── config/{privilege,resource}  # run-as package user; declare docker-project
├── app/docker/docker-compose.yaml   # host-network, reuses ghcr image
├── app/ui/config                # desktop entry → opens http://host:9090
├── cmd/main                     # status check via `docker inspect`
├── wizard/install               # install-time tips
└── build.sh                     # injects X.Y.Z version, runs fnpack build
```

## Build the `.fpk`

Install [`fnpack`](https://github.com/ckcoding/fnnas-docs/blob/main/docs/cli/fnpack.md), then:

```bash
./deploy/fnos/build.sh 0.10.0    # version MUST be X.Y.Z, must match image tag
```

The version must be `X.Y.Z` (fnOS rejects pre-release suffixes) **and** must equal the image tag — a mismatch produces a misleading `manifest unknown` error when the NAS pulls the image. `build.sh` writes the same version into both `manifest` and the compose `${VERSION}`.

## Install / publish

- **Local test:** fnOS → 应用中心 → 手动安装 → select the generated `.fpk`, or `appcenter-cli install-fpk mibee-nvr.fpk`.
- **Publish to the store:** join a fnOS fan group (via the fnOS website → 关注飞牛 → 微信群), contact the community lead to join the "应用中心开发者先锋交流群", and submit the `.fpk` + screenshots per staff guidance. See [fnOS publish docs](https://github.com/ckcoding/fnnas-docs/blob/main/docs/quick-started/publish-application.md).

## Update flow

fnOS drives updates via the app lifecycle: bump `manifest.version` + image tag, rebuild the `.fpk`, and the platform runs `upgrade_init`/`upgrade_callback` (data/config migration hooks) then stops and restarts the app. Recordings/DB/config under the app data dir persist across upgrades. See the [fnOS app framework](https://github.com/ckcoding/fnnas-docs/blob/main/docs/core-concepts/framework.md).

## Icons

`ICON.PNG` / `ICON_256.PNG` / `app/ui/images/icon_{64,256}.png` are solid-color placeholders. Replace them with the real logo (PNG only, SVG not supported) before publishing.
