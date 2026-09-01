# Deploy on QNAP QTS / QuTShero

> Run MiBee NVR in Docker on QNAP via **Container Station → Application** (docker-compose). Container Station 3.x honors `network_mode: host` in a compose file, which is what ONVIF camera auto-discovery needs.

## Why host networking

ONVIF WS-Discovery uses UDP multicast (`239.255.255.250:3702`), which Docker's default **bridge** blocks. The compose file therefore pins `network_mode: host`. Container Station 2's GUI historically did **not** expose host mode, but CS 3.x's compose/Application path accepts it and runs the container on the host network. Prefer the **Application (compose)** path below over the single-container GUI.

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

> No SSH? Use the Container Station → Application path below. China users can also swap `image:` to the ACR address for a faster pull: `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest`

## Install via Container Station → Application

1. Install/enable **Container Station** (3.x recommended) from the App Center.
2. In **Container Station → Applications → Create**, paste a compose file:
   ```yaml
   services:
     mibee-nvr:
       image: ghcr.io/mi-bee-studio/mibeenvr:latest
       container_name: mibee-nvr
       restart: unless-stopped
       network_mode: host          # required for ONVIF auto-discovery
       volumes:
         - /share/Container/mibee-nvr/data:/data
       environment:
         - NVR_DATA_DIR=/data
       healthcheck:
         test: ["CMD", "mibee-nvr", "health"]
         interval: 30s
         timeout: 5s
         retries: 3
   ```
   Container Station creates a shared folder named `Container` on install; map data there (e.g. `/share/Container/mibee-nvr/data`).
3. **Validate → Deploy**. The multi-arch image is pulled and the container starts.
4. Open `http://<nas-ip>:9090` and complete the setup wizard.

If the Application path blocks host mode on your firmware, the fallback is SSH:
```bash
ssh admin@<nas-ip>
mkdir -p /share/Container/mibee-nvr && cd /share/Container/mibee-nvr
# place the compose file above as docker-compose.yml, then:
docker compose up -d
```

## Import the ready-made template

Prefer not to paste compose by hand? A ready-to-use application template
lives in the repo: [`deploy/qnap/docker-compose.yml`](../../deploy/qnap/docker-compose.yml)
(with [`README.md`](../../deploy/qnap/README.md) import steps). It is the
same host-network compose with the typical QTS pool path pre-filled, plus
commented `NVR_LISTEN_PORT` and China-mirror switches.

## Port conflicts

QNAP QTS uses `8080` (HTTP management) / `443` (HTTPS) / `80` (Web Server, if enabled) for itself. These do not clash with MiBee NVR's `9090` (Web) or `2121` (FTP). If a QNAP service occupies a port you need, change that service's port in the QTS **Control Panel → Network** rather than remapping the NVR.

To change the NVR's own port (if `9090`/`2121` is taken by another container), edit `mibee-nvr.yaml`:
```yaml
server:
  listen: ":8080"
ftp:
  port: 2121
  passive_port_range: "2122-2140"
```

## Updates & rollback

See [Auto-update guide](deployment-autoupdate.md). Redeploy from the Application after `docker compose pull`, or pin a tag (`mibeenvr:0.12.0`) for reproducible rollbacks. Data under `/share/Container/mibee-nvr/data` persists across recreation.

## Native `.qpkg` — when it's worth it (optional)

QNAP's **App Center** is comparatively open to self-publishing via [QDK](https://github.com/qnap-dev/QDK), and you can even self-host a third-party App source so users subscribe and install. Wrapping the Docker container as a QPKG is documented in the community. But for a working install you do not need a QPKG — the Compose/Application path above is sufficient. Invest in QPKG packaging only if App Center discoverability matters to you, and reuse the same multi-arch image (QPKG can wrap a container rather than ship per-arch binaries).
