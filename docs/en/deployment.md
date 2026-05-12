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

Use the provided `docker-compose.yml`:

```bash
# 1. Prepare data directory and config
mkdir -p data
cp config.example.yaml data/mibee-nvr.yaml
# Edit config: set password, add cameras
nano data/mibee-nvr.yaml

# 2. Start the service
docker compose up -d

# 3. Open http://localhost:9090
```

Ports:

| Port | Purpose |
|------|---------|
| 9090 | Web UI / REST API |
| 2121 | FTP |
| 2122-2140 | FTP passive mode |

The Docker image includes a health check (`mibee-nvr health`) that runs every 30 seconds. Data is persisted in the `./data` volume mount.

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
