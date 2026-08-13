# Deploy on 飞牛 fnOS

> Install MiBee NVR on fnOS via a `.fpk` package. Two editions ship per release:
> an **offline** `.fpk` (both arch images docker-saved into the package — installs
> with no registry pull, ideal where ghcr.io is slow) and an **online** `.fpk`
> (tiny — pulls the image at start, auto-selecting ghcr vs the Alibaba Cloud ACR
> mirror by latency). No backend code changes; the package wraps the multi-arch
> Docker image.

## Why host networking

ONVIF WS-Discovery uses UDP multicast (`239.255.255.250:3702`), which Docker's default bridge blocks. The package runs the container with `network_mode: host` so camera auto-discovery works, and sets `manifest: checkport=false` since a host-network multi-port service has no single meaningful `service_port`. If `9090` (Web) or `2121` (FTP) is already in use on the host: for the Web port, prefer changing it via **Web UI → Settings → General → "Web UI port"** after install, or set the `NVR_LISTEN_PORT` environment variable before deploying; for FTP, change `ftp.port` in `mibee-nvr.yaml`.

## Offline vs online edition

| | Offline `.fpk` | Online `.fpk` |
|---|---|---|
| Size | ~150 MB (both arch images bundled) | ~65 KB |
| Install network | none (loads bundled image) | needs internet (pulls at start) |
| Image source | bundled tar | auto: ghcr (overseas) / ACR (China, faster) |
| Use when | ghcr pull is slow/blocked | network is fine, want a tiny package |

Both are on the [release page](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases):
`mibee-nvr-fnos-<ver>.fpk` (offline) and `mibee-nvr-fnos-online-<ver>.fpk` (online).

## How the package works (important)

The package does **not** use fnOS's `docker-project` resource — that would make
fnOS run `docker compose up` during install, *before* the offline image can be
loaded, so the pull fails. Instead `cmd/main` owns the full container lifecycle:

- **start**: (offline) `docker load` the bundled arch tar → `docker run --network host`;
  (online) probe ghcr vs ACR latency → `docker pull` → `docker run`
- **stop**: `docker stop` + `docker rm`
- **status**: `docker inspect`

So `app/docker/docker-compose.yaml` is effectively vestigial (kept for
readability); `cmd/main` is the real entry point. `config/privilege` runs as
**root** (needs the Docker socket); `config/resource` is `{}` (no docker-project).

## Architecture note (x86)

`cmd/main` maps `TRIM_SYS_ARCH` to the bundled image arch. As of 0.10.1 it
recognizes `x86` (fnOS's label for x86_64, alongside `x86_64`/`amd64`/`x64`) and
`armv7l`/`armhf` → armv7 — fixing [#311](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues/311)
where x86 fnOS hosts couldn't start (the arch variable was empty, producing
`mibee-nvr-.tar`).

## Package source

Lives in [`deploy/fnos/`](../../deploy/fnos); the online edition derives from it
(drops `app/images/`, swaps `cmd/main` for the pull-based version).

## Build the `.fpk`

Install [`fnpack`](https://github.com/ckcoding/fnnas-docs/blob/main/docs/cli/fnpack.md), then:

```bash
./deploy/fnos/build.sh 0.10.1    # version MUST be X.Y.Z, must match image tag
```

The version must be `X.Y.Z` (fnOS rejects pre-release suffixes) **and** must equal
the image tag — a mismatch produces a misleading `manifest unknown`. `build.sh`
writes the same version into both `manifest` and the compose `${VERSION}`.

## Install / publish

- **Local test:** fnOS → 应用中心 → 手动安装 → select the generated `.fpk`.
- **Publish to the store:** join a fnOS fan group (fnOS website → 关注飞牛 →
  微信群), contact the community lead, submit the `.fpk` + screenshots per staff
  guidance. See [fnOS publish docs](https://github.com/ckcoding/fnnas-docs/blob/main/docs/quick-started/publish-application.md).

## Update flow

Bump `manifest.version` + image tag, rebuild the `.fpk`; the platform runs
`upgrade_init`/`upgrade_callback` then restarts. Recordings/DB/config under the
app data dir persist across upgrades.

## Icons

`ICON.PNG` / `ICON_256.PNG` are placeholders — replace with the real logo
(PNG only, SVG not supported) before publishing.
