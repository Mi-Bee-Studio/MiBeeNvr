# Deployment Guide

This guide covers deploying MiBee NVR in production, including building from source, setting up system services, and configuring reverse proxies.

## Building MiBee NVR

### Local Build

Build MiBee NVR for your current architecture:

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr
make build
```

This creates `./mibee-nvr` binary for your system.

### Cross-Compilation

Build for ARM64 (common for home servers and embedded devices):

```bash
make cross
```

This creates `./mibee-nvr-arm64` binary for Linux ARM64.

### Docker Container Images

Build container images for deployment:

```bash
# Build amd64 image (multi-stage, compiles everything inside container)
make docker-build

# Build arm64 image (host cross-compile + scratch, no QEMU needed)
make docker-build-arm64

# Build all architectures
make docker-build-all

# Push to registry (login first: docker login <registry>)
make docker-push-all

# One-shot: build + push
make docker-release
```

Images are tagged with git short SHA. For example, on commit `0c7e0eb`:

| Image | Arch | Base |
|-------|------|------|
| `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:0c7e0eb` | amd64 | distroless |
| `registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:0c7e0eb-arm64` | arm64 | scratch |

#### Running with Docker/Podman

```bash
docker run -d \
  --name mibee-nvr \
  -p 9090:9090 \
  -v /mnt/data/nvr:/data \
  registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr:0c7e0eb-arm64
```

The config file is expected at `/data/mibee-nvr.yaml` (mount it via volume).

### Build and Test

Run the test suite to ensure everything works:

```bash
make test
```

Check for any issues:

```bash
make lint
```

## Systemd Service Setup

Create a systemd service file for MiBee NVR:

```bash
sudo tee /etc/systemd/system/mibee-nvr.service > /dev/null <<EOF
[Unit]
Description=MiBee NVR - Network Video Recorder
After=network.target
Requires=network.target

[Service]
Type=simple
User=nvr
Group=nvr
ExecStart=/mnt/data/nvr/bin/mibee-nvr -config /mnt/data/nvr/mibee-nvr.yaml
Restart=on-failure
RestartSec=5s
WorkingDirectory=/mnt/data/nvr
LimitNOFILE=65536

# Security hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/mnt/data/nvr
MemoryMax=512M

[Install]
WantedBy=multi-user.target
EOF
```

### Set up the NVR user and directories

```bash
# Create nvr user
sudo useradd -r -s /bin/false -d /mnt/data/nvr nvr

# Create necessary directories
sudo mkdir -p /mnt/data/nvr/bin /mnt/data/nvr/config /mnt/data/nvr/recordings
sudo chown -R nvr:nvr /mnt/data/nvr

# Copy binary and configuration
sudo cp mibee-nvr-arm64 /mnt/data/nvr/bin/mibee-nvr
sudo chmod +x /mnt/data/nvr/bin/mibee-nvr

# Create configuration file
sudo cp mibee-nvr.yaml /mnt/data/nvr/mibee-nvr.yaml
sudo chown nvr:nvr /mnt/data/nvr/mibee-nvr.yaml
```

### Start and enable the service

```bash
sudo systemctl daemon-reload
sudo systemctl enable mibee-nvr
sudo systemctl start mibee-nvr
```

### Check service status

```bash
sudo systemctl status mibee-nvr
sudo journalctl -u mibee-nvr -f  # View logs
```

## Reverse Proxy Configuration

### Caddy Example

Caddy is recommended for its automatic HTTPS support:

```bash
sudo tee /etc/caddy/Caddyfile > /dev/null <<EOF
{
    email admin@example.com
}

# Main web interface
https://nvr.example.com {
    reverse_proxy localhost:9090
    
    # Authentication (basic auth)
    basicauth {
        admin password123
    }
    
    # Logging
    log {
        output file /var/log/caddy/nvr_access.log
    }
}

# WebDAV access
https://nvr.example.com/dav/ {
    reverse_proxy localhost:9090
    basicauth {
        admin password123
    }
    # WebDAV specific headers
    @webdav method {GET HEAD POST PUT DELETE PROPFIND PROPPATCH COPY MOVE LOCK UNLOCK}
    reverse_proxy @webdav localhost:9090
}
EOF

sudo systemctl restart caddy
```

### Nginx Example

```nginx
server {
    listen 80;
    server_name nvr.example.com;
    
    # Basic authentication
    auth_basic "MiBee NVR";
    auth_basic_user_file /etc/nginx/htpasswd;
    
    location / {
        proxy_pass http://localhost:9090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
    
    # WebDAV location
    location /dav/ {
        proxy_pass http://localhost:9090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        # WebDAV specific headers
        proxy_request_buffering off;
        proxy_buffering off;
    }
}
```

## Access Protocol Notes

### Web Interface
- **URL**: `https://your-domain.com` or `http://your-ip:9090`
- **Authentication**: Required (basic auth)
- **Features**: Camera management, recording playback, live view

### FTP Access
- **Port**: 2121 (cannot be reverse-proxied)
- **Protocol**: FTP (not SFTP)
- **Authentication**: Required (same as web interface)
- **Access**: Direct FTP connection to server

### WebDAV Access
- **URL**: `https://your-domain.com/dav/` or `http://your-ip:9090/dav/`
- **Authentication**: Required (basic auth)
- **Access**: Read-only to recordings
- **Protocol**: WebDAV (RFC 4918)

## Storage Considerations

### Memory Requirements
- **Minimum**: 512MB RAM (for 30s segments)
- **Recommended**: 1GB+ RAM for multiple cameras
- **Segment Memory Usage**:
  - 30s segments: ~15-20MB per segment
  - 60s segments: ~30-40MB per segment
  - 120s segments: ~60-80MB per segment

### Disk Space Planning
- **Single Camera**: ~1-5GB per day depending on resolution and frame rate
- **Multiple Cameras**: Scale accordingly
- **Retention**: Plan for retention_days + buffer for cleanup delay

### Filesystem Considerations
- Use ext4 for best performance on Linux
- Consider SSD for better write performance
- Monitor disk I/O for multiple concurrent recordings

### Performance Tips
1. **Segment Duration**: Use 30s for low-memory systems
2. **Disk Monitoring**: Set appropriate disk_threshold_percent (80-95)
3. **Retention**: Regular cleanup prevents disk space issues
4. **Network**: Ensure cameras and server have stable network connections

## Updating MiBee NVR

### Backup Configuration
```bash
sudo cp /mnt/data/nvr/mibee-nvr.yaml /mnt/data/nvr/mibee-nvr.yaml.backup
```

### Stop Service
```bash
sudo systemctl stop mibee-nvr
```

### Update Binary
```bash
# Download new binary or build from source
sudo cp mibee-nvr-arm64 /mnt/data/nvr/bin/mibee-nvr
sudo chmod +x /mnt/data/nvr/bin/mibee-nvr
```

### Start Service
```bash
sudo systemctl start mibee-nvr
```

### Check Status
```bash
sudo systemctl status mibee-nvr
```

## Monitoring and Maintenance

### Log Management
```bash
# View logs
sudo journalctl -u mibee-nvr -n 100

# Follow logs
sudo journalctl -u mibee-nvr -f

# Rotate logs (add to /etc/logrotate.d/mibee-nvr)
sudo tee /etc/logrotate.d/mibee-nvr > /dev/null <<EOF
/var/log/caddy/nvr_access.log {
    daily
    missingok
    rotate 7
    compress
    delaycompress
    notifempty
    create 644 nvr nvr
}
EOF
```

### Health Checks
```bash
# Check if service is running
sudo systemctl is-active mibee-nvr

# Check web interface
curl -f http://localhost:9090/api/health

# Check disk usage
df -h /mnt/data/nvr
```

### Regular Maintenance Tasks
1. **Review Logs**: Check for errors or warnings
2. **Monitor Disk Usage**: Ensure retention policies are working
3. **Update Software**: Periodically update to latest version
4. **Backup Configuration**: Regular config backups

## Troubleshooting

### Common Issues
1. **Service won't start**: Check config file syntax and permissions
2. **Port conflicts**: Ensure ports aren't used by other services
3. **Permission errors**: Check file ownership and permissions
4. **Memory issues**: Reduce segment duration or add more RAM

### Debug Mode
Add `-v` flag for verbose logging:
```bash
sudo /mnt/data/nvr/bin/mibee-nvr -config /mnt/data/nvr/mibee-nvr.yaml -v
```

### Resource Monitoring
```bash
# Monitor memory usage
ps aux | grep mibee-nvr

# Monitor disk I/O
iostat -x 1

# Monitor network
netstat -an | grep :9090
```