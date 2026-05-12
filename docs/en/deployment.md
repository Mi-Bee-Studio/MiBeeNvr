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
sudo ./install.sh --version v0.2.0
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

```bash
# 1. Prepare data directory and config
mkdir -p data
cp config.example.yaml data/mibee-nvr.yaml

# 2. Edit config
#    IMPORTANT: Change storage.root_dir to "/data" (the in-container path)
#    Set admin password (see Configuration Notes below)
nano data/mibee-nvr.yaml

# 3. Start the service
docker compose up -d

# 4. Open http://localhost:9090
```

#### Configuration Notes

Docker deployment has some key differences from non-Docker setups:

- **Data directory**: `storage.root_dir` MUST be set to `/data` inside the container (differs from the non-Docker default of `/var/lib/mibee-nvr`). Update the copied config file accordingly.
- **Port mapping**:
  - `9090`: Web UI / REST API
  - `2121`: FTP server
  - `2122-2140`: FTP passive mode port range
  - To change ports, modify the left-side (host) value in `docker-compose.yml`
- **Data persistence**: The `./data:/data` volume mount stores:
  - Recording files (MP4 segments)
  - SQLite database (`mibee-nvr.db`)
  - Config file (`mibee-nvr.yaml`)
- **Environment variable**: `NVR_DATA_DIR=/data` tells the container where to find data
- **Timezone**: Set via `TZ=Asia/Shanghai` (or your timezone) to ensure correct timestamps
- **Password setup**: The `mibee-nvr init` subcommand is not available inside Docker. Use one of these methods:
  1. Set `auth.password` in plaintext (auto-converted to hash on first start)
  2. Generate a hash with `mibee-nvr hash-password <pw>` and paste into `auth.password_hash`

#### docker-compose.yml Reference

Full configuration with annotated fields:

```yaml
services:
  mibee-nvr:
    # Docker image — official pre-built image
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest

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

- Image: `ghcr.io/mi-bee-studio/mibee-nvr:latest`
- Architecture tags: `latest` (amd64), `latest-arm64` (arm64)
- Alternative registry (China): `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr`

No extra steps needed — the `docker-compose.yml` uses the pre-built image by default.

**Option B: Build locally**

If you need custom builds or want the latest source code:

```bash
# Multi-stage build (compiles frontend + backend inside container, requires network)
docker build -t mibee-nvr .

# Cross-compile ARM64 (on host, no QEMU needed)
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

#### Running on Raspberry Pi

Raspberry Pi requires the ARM64 image:

```yaml
# docker-compose.yml — Raspberry Pi configuration
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest-arm64
    # Alternative (China):
    # image: registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:latest-arm64
    deploy:
      resources:
        limits:
          memory: 512m      # Prevent OOM on RPi 3B
```

Important notes:

- Segment duration must stay at 30s (`segment_duration: "30s"`)
- Use an external USB disk (ext4) for recording storage
- Limit concurrent recording to 2-3 cameras depending on resolution and bitrate

#### Docker Troubleshooting

**Permission errors**

The container runs as nonroot (UID 65534). Fix mount permission issues:

```bash
chown -R 65534:65534 ./data
```

**Port conflicts**

Change the left-side (host) port in `docker-compose.yml`:

```yaml
ports:
  - "8090:9090"   # Change host port to 8090
```

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

## RPi 3B Notes

The Raspberry Pi 3B has 905MB RAM. For stable operation:

- **Segment duration**: Use 30s (`segment_duration: "30s"`). Longer durations hold more frames in RAM (e.g., 120s = 60-80MB per segment).
- **Memory limit**: Uncomment `MemoryMax=512M` in `deploy/mibee-nvr.service` to prevent OOM kills.
- **Storage**: Use an external USB disk (ext4) for recordings. The SD card will wear out quickly with continuous writes.
- **Cameras**: Limit to 2-3 concurrent H.264/H.265 streams depending on resolution and bitrate.

## Updating

### Using install.sh (Recommended)

```bash
sudo ./install.sh --version v0.2.0
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
