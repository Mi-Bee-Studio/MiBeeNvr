# Docker Deployment

> For MiBeeNvr v0.11.0

Deploy MiBee NVR quickly using Docker Compose.

## Prerequisites

- Docker 20.10+
- Docker Compose v2+
- Available disk space (depends on camera count and recording retention policy)

## Quick Start

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr
docker compose --project-directory . \
  -f deploy/docker/docker-compose.yml up -d
```

No config file is needed on first launch — MiBee NVR auto-initializes. Open `http://localhost:9090` in your browser.

## External Hard Drive Storage

By default, recordings are stored in `./data` on the host. To use an external drive:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest
    ports:
      - "9090:9090"
      - "1935:1935"
      - "9000:9000/udp"
      - "2121:2121"
    volumes:
      - /mnt/external/nvr:/data    # ← replace with your host path
    environment:
      - NVR_DATA_DIR=/data          # must match the right side of the volume mount
      # - NVR_UID=1000               # match the host directory owner's UID
      # - NVR_GID=1000               # match the host directory owner's GID
```

> **Important**: The right side of the volume mount (`:data`) and `NVR_DATA_DIR` must always match. If the container fails to start, verify the host directory exists and that the configured UID/GID has write permission.

## Persistent Configuration

### Option 1: Environment Variables

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest
    ports:
      - "9090:9090"
    environment:
      - NVR_LISTEN_PORT=9090
      - NVR_DATA_DIR=/data
      - NVR_PASSWORD=your-password
    volumes:
      - ./data:/data
```

### Option 2: Mount a Config File

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibee-nvr:latest
    ports:
      - "9090:9090"
    volumes:
      - ./mibee-nvr.yaml:/app/mibee-nvr.yaml
      - ./data:/data
```

First generate a default config file:

```bash
./mibee-nvr-amd64 init --password your-password
```

Then edit `mibee-nvr.yaml` to add camera configurations.

## Port Reference

| Port | Protocol | Purpose |
|------|----------|---------|
| 9090 | TCP | Web interface and API |
| 1935 | TCP | RTMP stream ingest |
| 9000 | UDP | SRT stream ingest |
| 2121 | TCP | FTP access to recordings |

## Read-Only Mode Mount

When read-only mode is enabled (the default), WebDAV and FTP allow browsing and downloading recordings but not deletion.

To manage recordings through the Web UI, WebDAV and FTP must be disabled:

```yaml
environment:
  - NVR_WEBDAV_ENABLED=false
  - NVR_FTP_ENABLED=false
```

## Health Checks

```bash
# Check container status
docker compose ps

# View logs
docker compose logs -f mibee-nvr

# Health check
curl http://localhost:9090/api/v1/system/status
```

## Updating

```bash
# Pull latest image
docker compose pull

# Recreate the container (data is preserved)
docker compose up -d
```

## Uninstalling

```bash
# Stop and remove containers (data preserved)
docker compose down

# Remove everything including data
docker compose down -v
rm -rf ./data
```

## Troubleshooting

### Container Fails to Start

```bash
# Check detailed errors
docker compose logs mibee-nvr

# Check for port conflicts
netstat -tlnp | grep 9090
```

### Permission Issues

```bash
# Check host directory permissions
ls -la /mnt/external/nvr/

# Adjust UID/GID
environment:
  - NVR_UID=1000
  - NVR_GID=1000
```

### Network Issues

If cameras are outside the Docker default bridge, use `host` networking:

```yaml
services:
  mibee-nvr:
    network_mode: host
```

## Next Steps

- [NAS Deployment](install-nas.md) — Synology / QNAP / unRAID / FlyBull
- [Recording & Playback](recording-playback.md) — managing recordings
- [SRT / RTMP Ingest](srt-rtmp.md) — configuring push-stream cameras
