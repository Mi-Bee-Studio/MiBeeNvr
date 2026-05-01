# Getting Started with MiBee NVR

## What is MiBee NVR

MiBee NVR is a lightweight Network Video Recorder (NVR) written in Go that records video feeds from IP cameras to MP4 segments on disk. It provides a simple web interface for viewing recordings, managing cameras, and accessing recorded footage through various protocols.

**Key Features:**
- Records RTSP (H.264 and MJPEG) and HTTP JPEG cameras to MP4 segments
- Web UI for camera management and recording playback
- WebDAV (read-only) access to recordings
- FTP server access for recording downloads
- MQTT integration for triggering recording events
- Single static binary with embedded web interface
- Configurable retention and cleanup policies

## Prerequisites

Before using MiBee NVR, ensure you have:

- **Go 1.22+** for building from source
- **Linux** (AMD64 or ARM64 architecture)
- **Storage device** with sufficient disk space for recordings
- **IP cameras** with RTSP or HTTP JPEG streaming capabilities

## Quick Start

### Option 1: Download Pre-built Binary

Download the latest release binary for your architecture from the project releases page.

### Option 2: Build from Source

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr
make build
```

### Create Configuration

Create a configuration file named `mibee-nvr.yaml`:

```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/mnt/data/nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password_hash: ""  # Generate hash using Go code
cameras:
  - id: "front-door"
    name: "Front Door Camera"
    protocol: "rtsp_h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
cleanup:
  retention_days: 30
  check_interval: "1h"
  disk_threshold_percent: 95
```

### Run MiBee NVR

```bash
./mibee-nvr -config mibee-nvr.yaml
```

After starting, the web interface will be available at `http://localhost:9090`.

## Adding Your First Camera

MiBee NVR supports three camera protocols. Here are examples for each:

### RTSP H.264 Camera

```yaml
cameras:
  - id: "cam1"
    name: "Front Door"
    protocol: "rtsp_h264"
    url: "rtsp://192.168.1.100:554/live"
    enabled: true
```

### RTSP MJPEG Camera

```yaml
cameras:
  - id: "cam2"
    name: "Back Yard"
    protocol: "rtsp_mjpeg"
    url: "rtsp://192.168.1.101:554/stream"
    enabled: true
```

### HTTP JPEG Camera

```yaml
cameras:
  - id: "cam3"
    name: "Garage"
    protocol: "http_jpeg"
    url: "http://192.168.1.102/capture"
    enabled: true
```

## Accessing MiBee NVR

### Web Interface

Access the web interface at `http://your-server:9090`. Use the credentials configured in your `auth` section.

### WebDAV Access

WebDAV provides read-only access to recordings. The path depends on your configuration:

```bash
curl -u admin:password http://your-server:9090/dav/
```

### FTP Access

FTP is available on port 2121 (default) for accessing recordings:

```bash
ftp your-server
# Username: admin
# Password: password
```

## Next Steps

Once you have MiBee NVR running and your first camera configured, explore these next steps:

- [Configuration Reference](configuration.md) - Learn about all configuration options
- [Deployment Guide](deployment.md) - Set up production deployment and services
- Adjust segment duration based on your available memory (30s recommended for low-memory systems)
- Configure retention policies to manage disk space
- Set up reverse proxy for secure external access