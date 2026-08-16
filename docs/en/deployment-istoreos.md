# Deploy on iStoreOS / OpenWrt

> Run MiBee NVR in Docker on iStoreOS (or a generic OpenWrt build that ships Docker). iStoreOS is a natural fit: it enables **host networking by default**, so ONVIF camera auto-discovery (UDP multicast) works with zero extra configuration.

## Hardware gate — read first

These are software-router/router-board OSes. A **full** NVR — recording to disk + SQLite + multi-stream demux — is realistic only on:

- **x86 software routers** (Intel N100 / J4125 / N305 class) with **≥ 4 GB RAM**, or
- **ARM SBCs** (Raspberry Pi 4/5 4 GB+, RK3588) with external storage.

> **RK3588 note**: the NVR transcoding backend supports software / V4L2 M2M /
> VAAPI / NVENC only — **not RKMPP (Rockchip NPU encode/decode)**. The RK3588
> NPU cannot be used; transcoding falls back to software encoding (CPU-bound
> with multiple streams). Core functions (recording / live / playback) are pure
> Go and do not depend on transcoding, so they are unaffected.

On 512 MB – 1 GB boxes, run only as a lightweight **ingest gateway / live preview** (disable or strictly limit recording), or you will OOM. Router flash (8–32 GB eMMC) fills in days with video — **always map `/data` to external storage** (USB HDD / SATA / mounted NAS share).

## Why it works so well here

iStoreOS uses `network_mode: host` by default for Docker. Since the router is itself the network device, the NVR process sits directly on the LAN — ONVIF multicast and broadcast reach cameras with no bridge/NAT in the way. No code change is needed; the app already listens on `0.0.0.0`.

## Image registry / China mirror

The image is on two registries — identical content (same multi-arch manifest list):

| Registry | Address | Use |
|---|---|---|
| GitHub ghcr | `ghcr.io/mi-bee-studio/mibeenvr` | Overseas (default) |
| Alibaba ACR | `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr` | **China recommended** (anonymous pull, no login/PAT) |

**One-line installer** (auto-selects the faster registry — races ghcr vs ACR latency, then pulls and starts the container). On iStoreOS without bash, run `opkg install bash` first, or use the Compose path below:

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/install-online.sh | bash
```

> China users can also swap `image:` to the ACR address for a faster pull: `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest`

## Install

### Via the iStoreOS Web UI (recommended)

1. Go to **系统 → Docker → Compose → 新建项目**.
2. Name the project `mibee-nvr` and paste the contents of [`deploy/istoreos/docker-compose.yml`](../../deploy/istoreos/docker-compose.yml).
3. Edit the volume left side to point at your external storage (e.g. `/mnt/sata1/mibee-nvr/data`).
4. Save and start the project.
5. Open `http://<device-ip>:9090` and complete the setup wizard.

### Via SSH

```bash
mkdir -p /mnt/sata1/mibee-nvr && cd /mnt/sata1/mibee-nvr
# download the compose file (or scp your edited copy)
wget -O docker-compose.yml https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/istoreos/docker-compose.yml
docker compose up -d
```

## Optional: add as a third-party Compose app source

iStoreOS/iStore supports third-party Compose app repositories. You can self-host this repo (or a fork) as an app source so users add it once and install with one click: **系统 → 应用商店 → 添加第三方应用商店**, pointing at the repo URL. See [iStoreOS discussion #1777](https://github.com/istoreos/istoreos/discussions/1777) for the source format.

## Port conflicts

With host networking the app binds directly to the host. If `9090` or `2121` clash with another service, change the app's own port in `mibee-nvr.yaml` (do not remap ports):

```yaml
server:
  listen: ":8080"
ftp:
  port: 2121
  passive_port_range: "2122-2140"
```

## Updates & rollback

- **Manual (recommended):** `cd /mnt/sata1/mibee-nvr && docker compose pull && docker compose up -d`.
- **Pinned / rollback:** set the image tag to a specific release (`mibeenvr:0.10.0`), recreate, and keep the old tag for rollback.
- **Automatic:** optional Watchtower profile — see [Auto-update guide](deployment-autoupdate.md).

Data under `/data` (recordings, DB, config, models) is untouched by container recreation.
