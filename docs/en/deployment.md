# Deployment Guide

This guide covers installing, configuring, and maintaining MiBee NVR in production.

## Installation Methods

### One-Click Install Script (Recommended)

The install script downloads the latest release binary, creates the `nvr` system user, initializes the config, and installs the systemd service — all in one step.

```bash
# Install latest version
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash
```

Install a specific version:

```bash
sudo ./install.sh --version v0.9.0
```

Uninstall (preserves recordings in `/var/lib/mibee-nvr`):

```bash
sudo ./install.sh --uninstall
```

The installer will prompt for an admin password if no config file exists. After installation, the Web UI is available at `http://<host-ip>:9090`.

### Docker

#### Prerequisites

- Docker Engine 20.10+ and Docker Compose v2 (or Podman equivalent)
- Check versions:
  ```bash
  docker --version
  docker compose version
  ```

#### Quick Start

# Option A: Just run — auto-initialization (recommended)
docker run -d \
  --name mibee-nvr \
  --restart unless-stopped \
  -p 9090:9090 \
  -v ./data:/data \
  ghcr.io/mi-bee-studio/mibeenvr:latest

# Option B: With initial password
docker run -d \
  --name mibee-nvr \
  --restart unless-stopped \
  -p 9090:9090 \
  -e NVR_PASSWORD=yourpassword \
  -v ./data:/data \
  ghcr.io/mi-bee-studio/mibeenvr:latest

# Option C: With docker-compose.yml
mkdir -p data
docker compose --project-directory . -f deploy/docker/docker-compose.yml up -d

> **First-time setup**: When started without a config file, MiBee NVR auto-generates a default configuration and runs in **setup mode** — all API endpoints are accessible without authentication. Set a password via the Web UI Settings page or the `NVR_PASSWORD` environment variable. Once a password is set, authentication is enforced.

#### Configuration Notes

- **Auto-initialization**: If no config file exists at `/data/mibee-nvr.yaml`, one is generated automatically with sensible defaults. No manual setup required.
- **Initial password**: Set via `NVR_PASSWORD` environment variable. If not set, the app starts in setup mode (no auth) — set a password through the Web UI Settings page.
- **Data directory**: `storage.root_dir` is automatically set to `/data` inside Docker containers via the `NVR_DATA_DIR` environment variable.
#### docker-compose.yml Reference

Full configuration with annotated fields:

```yaml
services:
  mibee-nvr:
    # Docker image — official pre-built image
    image: ghcr.io/mi-bee-studio/mibeenvr:latest

    # Container name (for easier management and log viewing)
    container_name: mibee-nvr

    # Auto-restart policy: always restart unless manually stopped
    restart: unless-stopped

    # Port mapping: host_port:container_port
    ports:
      - "9090:9090"               # Web UI and REST API
      - "2121:2121"               # FTP server
      - "2122-2140:2122-2140"     # FTP passive mode ports

    # Volume mount: map host ./data to container /data
    # Persists config, recordings, and database
    volumes:
      - ./data:/data

    # Environment variables
    environment:
      - NVR_DATA_DIR=/data         # Data directory path
      - TZ=Asia/Shanghai            # Timezone

    # Health check: verifies service status every 30 seconds
    healthcheck:
      test: ["CMD", "mibee-nvr", "health"]  # Health check command
      interval: 30s                           # Check interval
      timeout: 5s                             # Timeout
      start_period: 10s                       # Grace period after start
      retries: 3                              # Retry count
```

#### Pre-built Images vs Local Build

**Option A: Use pre-built image (recommended)**

- Image: `ghcr.io/mi-bee-studio/mibeenvr:latest`
- Architecture tags: `latest (multi-arch: amd64 + arm64)`

No extra steps needed — the `docker-compose.yml` uses the pre-built image by default.

#### Deploying on a NAS OS

The same multi-arch Docker image installs on most NAS operating systems. All NAS deployments share one design choice: **`network_mode: host` is required** — ONVIF camera auto-discovery uses UDP multicast (`239.255.255.250:3702`), which Docker's default bridge blocks. Per-platform guides (with the exact UI path, volume paths, and port-conflict notes):

| Platform | Guide |
|----------|-------|
| unRAID | [deployment-unraid.md](deployment-unraid.md) — Community Applications template |
| 飞牛 fnOS | [deployment-fnos.md](deployment-fnos.md) — `.fpk` package |
| iStoreOS / OpenWrt | [deployment-istoreos.md](deployment-istoreos.md) — Compose (default host networking) |
| Synology DSM | [deployment-synology.md](deployment-synology.md) — Container Manager → Project |
| QNAP QTS | [deployment-qnap.md](deployment-qnap.md) — Container Station → Application |
| 极空间 ZSpace | [deployment-zspace.md](deployment-zspace.md) — Docker → Compose |

For keeping the image current (manual pull, or optional Watchtower), see the [auto-update guide](deployment-autoupdate.md).


**Option B: Build locally**

If you need custom builds or want the latest source code:

```bash
# Multi-stage build (compiles frontend + backend inside container, requires network)
docker build -t mibee-nvr -f deploy/docker/Dockerfile .

# Cross-compile ARM64 (inside Docker, no QEMU needed)
make docker-build-arm64

# Build both architectures
make docker-build-all
```

After building locally, replace the `image:` field in `docker-compose.yml` with your local tag.

#### Common Docker Operations

```bash
# View logs (follow mode)
docker compose logs -f mibee-nvr

# View recent logs (last 100 lines)
docker compose logs --tail 100 mibee-nvr

# Restart container
docker compose restart mibee-nvr

# Stop container (preserves data)
docker compose down

# Stop and remove volumes (WARNING: deletes all data!)
docker compose down -v

# Update to latest image
docker compose pull
docker compose up -d

# Container status
docker compose ps

# Resource usage
docker stats mibee-nvr

# Health check status
docker inspect --format='{{.State.Health.Status}}' mibee-nvr
```

> **Note**: The container uses a distroless/scratch base image, so `docker exec` shell access is not available. Use `docker compose logs` for debugging.

#### Using Docker CLI

If you prefer not to use Docker Compose, you can run the container directly:

```bash
# 1. Login to GHCR (required for private images)
echo YOUR_GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin

# 2. Pull the image
docker pull ghcr.io/mi-bee-studio/mibeenvr:latest

# 3. Run the container
docker run -d \
  --name mibee-nvr \
  --restart unless-stopped \
  -p 9090:9090 \
  -p 2121:2121 \
  -p 2122-2140:2122-2140 \
  -v ./data:/data \
  -e NVR_DATA_DIR=/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/mi-bee-studio/mibeenvr:latest

# 4. Check status
docker ps
docker logs -f mibee-nvr
docker inspect --format='{{.State.Health.Status}}' mibee-nvr
```

**Run a specific version:**

```bash
docker pull ghcr.io/mi-bee-studio/mibeenvr:v0.9.0
docker run -d --name mibee-nvr ... ghcr.io/mi-bee-studio/mibeenvr:v0.9.0
```

**Stop and remove:**

```bash
docker stop mibee-nvr
docker rm mibee-nvr
```

**Update to latest:**

```bash
docker stop mibee-nvr
docker rm mibee-nvr
docker pull ghcr.io/mi-bee-studio/mibeenvr:latest
docker run -d ... ghcr.io/mi-bee-studio/mibeenvr:latest
```
#### Data Backup and Restore

**Backup:**

```bash
# 1. Stop container
docker compose stop

# 2. Backup data directory
tar czf nvr-backup-$(date +%Y%m%d).tar.gz data/

# 3. Restart
docker compose start
```

**Restore:**

```bash
# 1. Stop and remove container
docker compose down

# 2. Extract backup
tar xzf nvr-backup-20240101.tar.gz

# 3. Start with restored data
docker compose up -d
```

#### Running on ARM SBCs (e.g. Raspberry Pi)

Raspberry Pi requires the ARM64 image:

```yaml
# docker-compose.yml — Raspberry Pi configuration
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    deploy:
      resources:
        limits:
          memory: 512m      # Prevent OOM on RPi 3B
```

Important notes:

- Segment duration: 30s on low-memory devices (≤2 GB available RAM, e.g. RPi 3B); up to 2m on hosts with >2 GB RAM (x86, Banana Pi M5). The configured value is auto-clamped to the platform cap at startup.
- Use an external USB disk (ext4) for recording storage
- Limit concurrent recording to 2-3 cameras depending on resolution and bitrate

#### Docker Troubleshooting

**Permission errors**

The container runs as nonroot (UID 65534). Fix mount permission issues:

```bash
chown -R 65534:65534 ./data
```

**Port conflicts**

MiBee NVR offers three ways to configure the listen port, in order of preference:

1. **Web UI (recommended)**: Settings → General → "Web UI port". Saving applies it immediately (the service restarts automatically). This is the preferred way to change the port after installation — no config file editing or container restart needed.
2. **At install time**: `./install.sh --port 9091` (set during first install, bare-metal).
3. **Environment variable (NAS host networking / Docker)**: `NVR_LISTEN_PORT=9091` (for `network_mode: host` NAS deployments, where Docker port mapping has no effect).

> The legacy `server.listen: ":9191"` in `mibee-nvr.yaml` still works, but the three options above are more straightforward and require no manual config editing.

**Container keeps restarting**

Usually a config file error. Check logs:

```bash
docker compose logs mibee-nvr
```

**FTP won't connect**

Ensure passive port range (2122-2140) is mapped and not blocked by firewall.

**Wrong timezone**

Add the `TZ` environment variable to `docker-compose.yml`:

```yaml
environment:
  - TZ=America/New_York
```

**Docker Compose v1 vs v2**

- Use `docker compose` (with space, v2)
- Not `docker-compose` (with hyphen, v1, deprecated)

**ONVIF device discovery doesn't work in Docker**

ONVIF auto-discovery uses WS-Discovery (UDP multicast to `239.255.255.250:3702`). Docker's default bridge network blocks multicast traffic, so auto-discovery won't find devices.

Solutions:

1. **Host networking** (recommended for discovery): Uncomment `network_mode: host` in `docker-compose.yml` and remove the `ports` section. The container shares the host's network stack, enabling multicast. If the host's port 9090 is already in use (e.g. Synology DSM), use a different port: change it via **Web UI → Settings → General → "Web UI port"** after install (recommended), or set the `NVR_LISTEN_PORT=9191` environment variable before deploying — the container binds that host port directly. The health check (`mibee-nvr health`) auto-detects the configured listen port, so no extra setup is needed.

2. **Manual probe** (works in any network mode): In the Web UI camera page, use the "Manual Probe" section to enter a device IP address directly. This bypasses multicast and works in any Docker configuration.

3. **Manual camera addition**: Add cameras by specifying the ONVIF endpoint URL directly (e.g., `http://192.168.1.100/onvif/device_service`) in the camera form. ONVIF connection, PTZ control, and streaming work normally in bridge mode — only auto-discovery is affected.

### Manual Installation

If you prefer full control or the install script doesn't cover your use case:

```bash
# 1. Download binary from GitHub Releases
#    https://github.com/Mi-Bee-Studio/MiBeeNvr/releases
sudo cp mibee-nvr /usr/local/bin/mibee-nvr
sudo chmod +x /usr/local/bin/mibee-nvr

# 2. Create system user and data directory
sudo useradd -r -s /bin/false -d /var/lib/mibee-nvr nvr
sudo mkdir -p /var/lib/mibee-nvr
sudo chown -R nvr:nvr /var/lib/mibee-nvr

# 3. Initialize config (prompts for admin password)
sudo -u nvr /usr/local/bin/mibee-nvr init \
    --password <your-password> \
    --data-dir /var/lib/mibee-nvr \
    --config /var/lib/mibee-nvr/mibee-nvr.yaml \
    --listen ":9090"

# 4. Install systemd service
sudo cp deploy/mibee-nvr.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now mibee-nvr
```

### Building from Source

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr

# Build for current architecture
make build

# Cross-compile for ARM64 (e.g., Raspberry Pi)
make cross

# Run tests
make test

# Lint
make lint
```

To deploy a cross-compiled binary directly to a Raspberry Pi:

```bash
make deploy RPi_HOST=user@your-rpi-host
make deploy-check RPi_HOST=user@your-rpi-host
make rollback RPi_HOST=user@your-rpi-host
```

## Systemd Service

The service file is maintained in [`deploy/mibee-nvr.service`](../../deploy/mibee-nvr.service). Key details:

- **Binary**: `/usr/local/bin/mibee-nvr`
- **Config**: `/var/lib/mibee-nvr/mibee-nvr.yaml`
- **Working directory**: `/var/lib/mibee-nvr`
- **Runs as**: `nvr` user
- **Security**: `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`, `ProtectHome`
- **Memory limit**: `MemoryMax=512M` (commented out by default; uncomment for RPi 3B)

Common commands:

```bash
sudo systemctl start mibee-nvr
sudo systemctl stop mibee-nvr
sudo systemctl restart mibee-nvr
sudo systemctl status mibee-nvr
sudo journalctl -u mibee-nvr -f   # follow logs
```

## Reverse Proxy

### Caddy

Caddy provides automatic HTTPS with minimal configuration:

```caddyfile
nvr.example.com {
    reverse_proxy localhost:9090
}
```

For TLS with explicit email:

```caddyfile
{
    email admin@example.com
}

nvr.example.com {
    reverse_proxy localhost:9090
}
```

### Nginx

```nginx
server {
    listen 80;
    server_name nvr.example.com;

    location / {
        proxy_pass http://localhost:9090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /dav/ {
        proxy_pass http://localhost:9090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;
        proxy_buffering off;
    }
}
```

## Low-Memory Device Notes (e.g. RPi 3B)

The Raspberry Pi 3B has 905MB RAM. For stable operation:

- **Segment duration**: 30s on low-memory devices (≤2 GB available RAM, e.g. RPi 3B); up to 2m on hosts with >2 GB RAM (x86, Banana Pi M5), which halves the fragment count rolling merge must process. Longer durations hold more frames in RAM (e.g., 120s = 60-80MB per segment), so the configured value is auto-clamped to the platform cap (≤2 GB → 30s, >2 GB → up to 2m) at startup with a warning if it was reduced.
- **Memory limit**: Uncomment `MemoryMax=512M` in `deploy/mibee-nvr.service` to prevent OOM kills.
- **Storage**: Use an external USB disk (ext4) for recordings. The SD card will wear out quickly with continuous writes.
- **Cameras**: Limit to 2-3 concurrent H.264/H.265 streams depending on resolution and bitrate.

## NAS / Large-Memory Optimization (tmpfs buffer)

On hosts with ample RAM (NAS boxes, x86 mini-PCs with 8 GB+), the frequent small-segment writes keep mechanical drives from spinning down and accelerate SD/eMMC wear. A **zero-code-change** mitigation is to use a Linux `tmpfs` as the NVR's temporary recording directory: small segments are written to RAM first, then the background merge task flushes the large merged file to the mechanical disk in one sequential write.

```bash
# 1. Mount a tmpfs (tune size to available RAM; stay under 25-30% of total)
sudo mount -t tmpfs -o size=2G tmpfs /mnt/nvr-tmp

# 2. Point storage.root_dir at the tmpfs mount in mibee-nvr.yaml
#    storage:
#      root_dir: /mnt/nvr-tmp
#
# Then route final archive output (the mechanical disk) via the merge output
# path or a scheduled mv to persistent storage.
```

Caveats:

- **Power loss loses tmpfs data.** tmpfs is only suitable as a rolling-segment buffer; the merged large files must be moved to persistent storage promptly. Mains power is generally reliable, but add a small UPS if you are cautious.
- **Size within reason.** tmpfs consumes real RAM — an oversized mount starves the NVR process, HLS streaming, and browser rendering. 2 GB is a sensible starting point on most 8 GB hosts.
- **v0.9.0+ adaptive segment duration** already reduces write frequency: low-memory devices (≤2 GB) use 30s, high-memory hosts (>2 GB) auto-use up to 2m, halving fragment count. tmpfs further eliminates the remaining small-write churn on top of this.
- **Approaches NOT adopted** (they conflict with the project's design constraints): a resident in-memory ring buffer needs a large constant RAM allocation, incompatible with the RPi 3B's 1 GB target; enlarging the muxer write buffer risks losing hundreds of MB on power loss and corrupts the MP4 moov atom, violating the atomic-write (temp → rename) crash-safety principle. The project therefore sticks with the memory-friendly "small segments + async merge + optional tmpfs (deployment layer)" approach.

## Updating

For Docker deployments — manual `docker compose pull && up -d`, optional Watchtower auto-updates, and rollback — see the [auto-update guide](deployment-autoupdate.md). For breaking-change migrations between specific versions, see the [Upgrade Guide](upgrade-guide.md).

### Using install.sh (Recommended)

```bash
sudo ./install.sh --version v0.9.0
```

The script stops the service, replaces the binary, and restarts automatically. Config and recordings are preserved.

### Manual Update

```bash
sudo systemctl stop mibee-nvr
sudo cp mibee-nvr /usr/local/bin/mibee-nvr
sudo chmod +x /usr/local/bin/mibee-nvr
sudo systemctl start mibee-nvr
```

Always back up your config before updating:

```bash
sudo cp /var/lib/mibee-nvr/mibee-nvr.yaml /var/lib/mibee-nvr/mibee-nvr.yaml.backup
```

## Monitoring

### Logs

```bash
sudo journalctl -u mibee-nvr -n 100    # last 100 lines
sudo journalctl -u mibee-nvr -f        # follow
sudo journalctl -u mibee-nvr --since "1 hour ago"
```

### Health Check

```bash
sudo systemctl is-active mibee-nvr
curl -f http://localhost:9090/api/health
```

### Disk Usage

```bash
df -h /var/lib/mibee-nvr
du -sh /var/lib/mibee-nvr/recordings
```

### Prometheus Metrics

Metrics are available at `/metrics` (public, no auth required):

```bash
curl http://localhost:9090/metrics
```

## Troubleshooting

### Service won't start

```bash
sudo journalctl -u mibee-nvr -n 50
# Verify config syntax
sudo -u nvr /usr/local/bin/mibee-nvr -config /var/lib/mibee-nvr/mibee-nvr.yaml
```

### Camera connection failures

```bash
# Test RTSP connection
ffmpeg -rtsp_transport tcp -i "rtsp://admin:pass@192.168.1.100:554/stream" -t 5 -f null -

# Check network
ping 192.168.1.100
```

### Port conflicts

```bash
sudo lsof -i :9090
sudo lsof -i :2121
```

### Permission errors

```bash
ls -la /var/lib/mibee-nvr/
sudo -u nvr ls /var/lib/mibee-nvr/
```

### High memory usage

Reduce `segment_duration` to 30s. On RPi 3B, uncomment `MemoryMax=512M` in the service file.
