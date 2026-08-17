# Deploy on 极空间 ZSpace (ZOS)

> Run MiBee NVR in Docker on a 极空间 (ZSpace) NAS via the built-in **Docker → Compose 项目**. ZOS supports host networking in compose, which ONVIF camera auto-discovery needs. 极空间 has no private package format — "apps" here are just Docker containers.

## Why host networking

ONVIF WS-Discovery uses UDP multicast (`239.255.255.250:3702`), which Docker's default **bridge** blocks. ZOS Docker supports host / macvlan / bridge; the compose pins `network_mode: host` so camera discovery works with no extra setup.

## Image registry / China mirror

The image is on two registries — identical content (same multi-arch manifest list):

| Registry | Address | Use |
|---|---|---|
| GitHub ghcr | `ghcr.io/mi-bee-studio/mibeenvr` | Overseas (default) |
| Alibaba ACR | `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr` | **China recommended** (anonymous pull, no login/PAT) |

**One-line installer** (auto-selects the faster registry — races ghcr vs ACR latency, then pulls and starts the container):

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/install-online.sh | bash
```

> No SSH? Use the Docker → Compose path below. China users can also swap `image:` to the ACR address for a faster pull: `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest`

## Install via Docker → Compose

1. In **文件管理**, pick a folder on your storage pool for persistent data, e.g. an external-disk or large-share path.
2. Open **系统 → Docker → Compose → 新建项目** (or **Docker → 项目** on older firmware).
3. Name the project `mibee-nvr` and paste:
   ```yaml
   services:
     mibee-nvr:
       image: ghcr.io/mi-bee-studio/mibeenvr:latest
       container_name: mibee-nvr
       restart: unless-stopped
       network_mode: host          # required for ONVIF auto-discovery
       volumes:
         - <your-storage-path>/mibee-nvr/data:/data
       environment:
         - NVR_DATA_DIR=/data
       healthcheck:
         test: ["CMD", "mibee-nvr", "health"]
         interval: 30s
         timeout: 5s
         retries: 3
   ```
   Replace `<your-storage-path>` with the path from step 1 (recordings fill disk fast — use a large pool or external drive, not the system volume).
4. Save and start the project. The multi-arch image is pulled (x86 models get `amd64`, ARM models like Z2Pro/T6 get `arm64`).
5. Open `http://<nas-ip>:9090` and complete the setup wizard.

## Port conflicts
## Import the ready-made template

Prefer not to paste compose by hand? A ready-to-import orchestration template
lives in the repo: [`deploy/zspace/docker-compose.yml`](../../deploy/zspace/docker-compose.yml)
(with [`README.md`](../../deploy/zspace/README.md) import steps). It defaults
to the Aliyun mirror (fast in China, anonymous pull), host networking, and a
commented `NVR_LISTEN_PORT` switch.


On host networking the container binds directly to the NAS. MiBee NVR uses `9090` (Web) and `2121` (FTP); if either clashes with another service or container, change the app's own port in `mibee-nvr.yaml` (not a port remap):
```yaml
server:
  listen: ":8080"
ftp:
  port: 2121
  passive_port_range: "2122-2140"
```

## How this differs from 极空间's built-in 监控中心

极空间 ships its own **监控中心** (a lightweight RTSP/ONVIF preview tool). The two can coexist; they serve different needs:

| | 极空间 监控中心 | MiBee NVR |
|---|---|---|
| Live preview | yes | yes |
| Browser-side AI detection (ONNX) | no | **yes** |
| Recording to disk + retention/merge | limited | **full** (pure-Go merge, no FFmpeg needed) |
| FTP / WebDAV / WebRTC streaming | no | **yes** |
| Multi-arch, runs on x86 + ARM models | — | yes |

Think of 监控中心 as quick preview, MiBee NVR as AI analysis + long-term recording.

## Updates & rollback

See [Auto-update guide](deployment-autoupdate.md). `docker compose pull && up -d` from the project, or pin a tag (`mibeenvr:0.10.0`) for rollback. Data under your mapped path persists across recreation.

## Reaching the official app store (optional)

极空间's official store lists a curated set of "Docker apps" and "third-party apps", but there is **no public self-service developer portal** — entering it requires contacting 极空间 business development. Note that 监控中心 overlaps with this NVR, so a store discussion should lead with the differentiation above. For now, the Compose path plus a community template (e.g. submitting to a community compose-template library) is the immediate, zero-friction way to reach 极空间 users.
