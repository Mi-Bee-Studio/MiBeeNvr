# Deploy on QNAP QTS / QuTShero

> Run MiBee NVR in Docker on QNAP via **Container Station → Application** (docker-compose). Container Station 3.x honors `network_mode: host` in a compose file, which is what ONVIF camera auto-discovery needs.

## Why host networking

ONVIF WS-Discovery uses UDP multicast (`239.255.255.250:3702`), which Docker's default **bridge** blocks. The compose file therefore pins `network_mode: host`. Container Station 2's GUI historically did **not** expose host mode, but CS 3.x's compose/Application path accepts it and runs the container on the host network. Prefer the **Application (compose)** path below over the single-container GUI.

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

See [Auto-update guide](deployment-autoupdate.md). Redeploy from the Application after `docker compose pull`, or pin a tag (`mibeenvr:0.10.0`) for reproducible rollbacks. Data under `/share/Container/mibee-nvr/data` persists across recreation.

## Native `.qpkg` — when it's worth it (optional)

QNAP's **App Center** is comparatively open to self-publishing via [QDK](https://github.com/qnap-dev/QDK), and you can even self-host a third-party App source so users subscribe and install. Wrapping the Docker container as a QPKG is documented in the community. But for a working install you do not need a QPKG — the Compose/Application path above is sufficient. Invest in QPKG packaging only if App Center discoverability matters to you, and reuse the same multi-arch image (QPKG can wrap a container rather than ship per-arch binaries).
