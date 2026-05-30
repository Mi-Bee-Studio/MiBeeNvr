# MiBee NVR

[![GitHub Release](https://img.shields.io/github/v/release/Mi-Bee-Studio/MiBeeNvr?style=flat&label=Release)](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/Mi-Bee-Studio/MiBeeNvr/ci.yml?style=flat&label=CI)](https://github.com/Mi-Bee-Studio/MiBeeNvr/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![Svelte](https://img.shields.io/badge/Svelte-FF3E00?style=flat&logo=svelte&logoColor=white)](https://svelte.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-003B57?style=flat&logo=sqlite&logoColor=white)](https://www.sqlite.org/)
[![Docker](https://img.shields.io/badge/Docker-2496ED?style=flat&logo=docker&logoColor=white)](https://www.docker.com/)
[![Raspberry Pi](https://img.shields.io/badge/Raspberry_Pi-A22846?style=flat&logo=raspberrypi&logoColor=white)](https://www.raspberrypi.com/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow?style=flat)](LICENSE)

A lightweight, easy-to-use Network Video Recorder designed for simplicity. Single binary, zero config hassle — just run and go.

Built for Raspberry Pi and low-power devices. Supports mainstream protocols: **RTSP** (H.264/H.265/MJPEG), **HTTP JPEG**, **HLS** streaming, **ONVIF** discovery, **WebRTC** (WHEP), **HTTP-FLV**, **RTMP** ingest, and **SRT** receiver.

[**中文**](README.zh.md)

## Screenshots

![Login](docs/images/login-light.png)
![Dashboard](docs/images/dashboard-light.png)
![Settings](docs/images/settings-light.png)

## Core Features

- **Camera Protocols**: RTSP (H.264/H.265/MJPEG), HTTP JPEG, ONVIF discovery & management
- **Recording**: Automatic MP4 segments, multi-camera concurrent, per-camera retention, audio capture (AAC + G.711)
- **Live View**: Multi-protocol streaming — HLS, WebRTC (WHEP), HTTP-FLV, RTMP ingest, SRT receiver
- **Segment Merge**: Auto or manual merge, global + per-camera policies
- **Web UI**: Dark/light theme, responsive, i18n (EN/ZH), Chart.js dashboards
- **Smart Home**: MQTT trigger-based recording, WebDAV/FTP file access
- **Single Binary**: Zero dependencies, embedded SPA, `CGO_ENABLED=0`
- **Xiaomi Support**: CS2 P2P protocol, cloud auth (community-driven, not core focus)
- **Health Monitoring**: Multi-layer camera health detection, auto-remediation, quality scoring

## Roadmap

|| Status | Protocol / Feature | Notes |
|--------|-------------------|-------|
| ✅ Done | RTSP (H.264/H.265/MJPEG) | Core streaming protocol |
| ✅ Done | HTTP JPEG | IP camera snapshot streaming |
| ✅ Done | HLS | On-demand live streaming |
| ✅ Done | ONVIF | Discovery, PTZ, stream URI |
| ✅ Done | Xiaomi (CS2 P2P) | Cloud auth, H.264/H.265 — community support |
| ✅ Done | RTMP | Push/pull streaming |
| ✅ Done | SRT | Low-latency transport |
| ✅ Done | HTTP-FLV | Browser-friendly live streaming |
| ✅ Done | WebRTC | Sub-second latency live view |
| ✅ Done | Audio Recording | AAC + G.711, per-camera toggle |
| ✅ Done | Health Monitoring | Multi-layer detection, auto-remediation |

### Option 1: Pre-built Binary (Recommended)

Download the latest binary from [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases):

```bash
# AMD64 (most PCs/servers)
wget https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/latest/download/mibee-nvr-amd64
chmod +x mibee-nvr-amd64

# ARM64 (Raspberry Pi, etc.)
wget https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/latest/download/mibee-nvr-arm64
chmod +x mibee-nvr-arm64

# ARMv7 (Raspberry Pi 2/3, etc.)
wget https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/latest/download/mibee-nvr-armv7
chmod +x mibee-nvr-armv7
```

Initialize config and start:

```bash
./mibee-nvr-amd64 init --password yourpassword
./mibee-nvr-amd64 -config mibee-nvr.yaml
```

Open `http://localhost:9090` to access the Web UI.

### Option 2: Docker

```bash
docker compose up -d
```

Open `http://localhost:9090` to access the Web UI.

To store recordings on an external drive, edit the volume mount in `docker-compose.yml`:

```yaml
    volumes:
      - /mnt/external/nvr:/data    # ← change to your host path
    environment:
      - NVR_DATA_DIR=/data          # must match the volume mount
```

See [`docker-compose.yml`](docker-compose.yml) for full details.

### Option 3: One-click Install

```bash
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash
```

This downloads the binary, creates a system user (`nvr`), generates config, installs a systemd service, and starts it. Data directory: `/var/lib/mibee-nvr`.

### Option 4: Build from Source

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git
cd MiBeeNvr
make build
./mibee-nvr init --password yourpassword
./mibee-nvr -config mibee-nvr.yaml
```

For detailed setup, see [Getting Started](docs/en/getting-started.md).

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/en/getting-started.md) | Installation, first camera setup |
| [Configuration](docs/en/configuration.md) | Full config reference |
| [API Reference](docs/en/api-reference.md) | REST API documentation |
| [MediaMTX Guide](docs/en/mediamtx-guide.md) | MediaMTX integration for CSI cameras |
| [Deployment](docs/en/deployment.md) | systemd, reverse proxy, cross-compile |
| [Xiaomi Setup](docs/en/xiaomi-setup.md) | Xiaomi cloud camera integration |
#BZ|| [ONVIF Guide](docs/en/onvif-guide.md) | ONVIF camera setup, PTZ control, troubleshooting |

```bash
make build              # Local build (current architecture)
make cross              # Cross-compile ARM64 binary
make test               # Run tests
make lint               # Run linter
```

## Docker Container Images

For quick deployment, see [`docker-compose.yml`](docker-compose.yml):

```bash
docker compose up -d
```

Two build methods are available:

- **Multi-stage build** (`Dockerfile`): Compiles frontend + backend inside the container. Requires network to pull base images.
- **Cross-compile build** (`Dockerfile.arm64`): Cross-compiles on the host, packages with `scratch` base image. No QEMU needed.

```bash
# Build amd64 image (multi-stage)
make docker-build

# Build arm64 image (host cross-compile + scratch packaging)
make docker-build-arm64

# Build all architectures
make docker-build-all

# Push to registry (requires docker/podman login first)
make docker-push              # Push amd64
make docker-push-arm64        # Push arm64
make docker-push-all          # Push all

# Build and push in one shot
make docker-release
```

Images are published to GitHub Container Registry on version tags:

| Image | Architectures |
|-------|--------------|
| `ghcr.io/mi-bee-studio/mibeenvr:<tag>` | amd64, arm64 |

Available tags: `latest`, `v1.2.3` (semver), `sha-abc1234`

## Project Structure

```
cmd/mibee-nvr/       # Entry point
internal/            # Core packages
  api/               # REST API handlers
  camera/            # Camera manager
  recorder/          # H.264/H.265/MJPEG recording engines
  hls/               # HLS streaming manager
  storage/           # SQLite DB + file manager
  config/            # YAML config
  middleware/        # Auth middleware
  muxer/             # MP4 muxer
  ftp/               # FTP server
  webdav/            # WebDAV server (configurable read-only/read-write)
  mqtt/              # MQTT client
  ui/                # Embedded web UI
  xiaomi/            # Xiaomi camera support (built-in, CS2 P2P, cloud auth)
web/                 # Svelte 5 frontend
deploy/              # systemd services
docs/                # Documentation (EN/ZH)
```

## Contributing

1. Run `make lint` before submitting
2. Add tests for new features
3. Write clear commit messages

## License

[MIT License](LICENSE) © Mi&Bee Studio
