# Deploy on Synology DSM

> Run MiBee NVR in Docker on Synology DSM 7.2+ via **Container Manager → Project** (docker-compose). The compose path supports `network_mode: host`, which is what ONVIF camera auto-discovery needs. No `.spk` package is required for a working install.

## Why host networking

ONVIF WS-Discovery uses UDP multicast (`239.255.255.250:3702`), which Docker's default **bridge** blocks. The compose file therefore pins `network_mode: host`; Container Manager honors it in a Project. Do **not** also declare `ports:` in a host-network service — the two conflict and DSM will reject the compose.

## Install via Container Manager → Project

1. In **File Station**, create a folder for the project, e.g. `/volume1/docker/mibee-nvr/`.
2. Open **Container Manager → Container → Project → Add**.
3. Set **Project name** = `mibee-nvr`, **Path** = the folder from step 1.
4. Under **Source**, choose **Create docker-compose.yml** and paste the host-network compose:
   ```yaml
   services:
     mibee-nvr:
       image: ghcr.io/mi-bee-studio/mibeenvr:latest
       container_name: mibee-nvr
       restart: unless-stopped
       network_mode: host          # required for ONVIF auto-discovery
       volumes:
         - /volume1/docker/mibee-nvr/data:/data
       environment:
         - NVR_DATA_DIR=/data
       healthcheck:
         test: ["CMD", "mibee-nvr", "health"]
         interval: 30s
         timeout: 5s
         retries: 3
   ```
   (This is the same as [`deploy/compose/docker-compose.host.yml`](../../deploy/compose/docker-compose.host.yml) with the host path filled in.)
5. **Next → Done**. Container Manager pulls the multi-arch image and starts the container.
6. Open `http://<nas-ip>:9090` and complete the setup wizard.

## Port conflicts

On host networking the container binds directly to the NAS. MiBee NVR uses `9090` (Web/API) and `2121` (FTP) — these do **not** clash with DSM's own ports. DSM reserves `5000` (HTTP) / `5001` (HTTPS) for its UI, and `20/21` for its optional built-in FTP. If you also enabled DSM's FTP, note it is a separate service from MiBee NVR's FTP (2121) — do not confuse them.

If `9090` or `2121` is taken by another container, change the app's own port in `mibee-nvr.yaml` (not a port remap):
```yaml
server:
  listen: ":8080"
ftp:
  port: 2121
  passive_port_range: "2122-2140"
```

## Updates & rollback

See [Auto-update guide](deployment-autoupdate.md). In short: `docker compose pull && up -d` from the project folder, or pin a tag (`mibeenvr:0.10.0`) for reproducible rollbacks. Data under `/volume1/docker/mibee-nvr/data` is never touched by recreation.

## Native `.spk` package — when it's worth it (optional)

A `.spk` package in Synology's **Package Center** gets you store discoverability, but the cost is high: DSM 7 requires a publisher signing certificate + functional review, and you must build per-architecture packages. Two pragmatic options:

- **Compose-only (this guide):** zero packaging cost, works today, no signing. Recommended for most users.
- **Wrap the Docker image as a `.spk`:** Synology's developer guide documents a [Compile Docker Package](https://help.synology.com/developer-guide/examples/compile_docker_package.html) flow that ships a container as a Package Center entry. This is the lowest-effort path to store presence if you decide you need it — it reuses the same image, no per-arch rebuild.

For an independent NVR project, start with Compose-only and only invest in `.spk` packaging if Package Center discoverability is worth the signing/review overhead.
