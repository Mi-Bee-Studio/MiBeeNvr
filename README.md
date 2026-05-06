# MiBee NVR

A lightweight Network Video Recorder (NVR) written in Go. Supports RTSP (H.264/H.265/MJPEG) and HTTP JPEG cameras, with a built-in Web UI, WebDAV, FTP, and MQTT integration. Compiles to a single static binary with an embedded SPA frontend.

[**中文**](README.zh.md)

## Screenshots

![Recordings - Dark Theme](docs/images/recordings-dark.png)
![Recordings - Light Theme](docs/images/recordings-light.png)
![Cameras Management - Dark Theme](docs/images/cameras-dark.png)
![Statistics - Dark Theme](docs/images/stats-dark.png)
![Statistics - Light Theme](docs/images/stats-light.png)
![Settings - Dark Theme](docs/images/settings-dark.png)

## Features

- RTSP (H.264/H.265/MJPEG) and HTTP JPEG camera support
- Automatic MP4 segment recording
- Web UI with **dark/light theme** (auto-detects system preference)
- **Chart.js-powered** storage trends and per-camera statistics
- **Live view (HLS streaming)** - On-demand H.264/H.265 live streaming via Web UI
- **lucide-svelte** icons throughout the interface
- **i18n** support: English/Chinese language switching
- **Responsive design** for mobile and desktop
- WebDAV (configurable read-only/read-write) and FTP file access
- MQTT trigger-based recording for smart home integration
- Multi-camera concurrent recording
- **Per-camera retention_days** - Each camera can have its own retention policy
- Automatic cleanup with retention and disk threshold policies
- SQLite metadata storage
- Single static binary, no external dependencies (`CGO_ENABLED=0`)
## Quick Start

```bash
# Build
make build

# Create config
cp config.example.yaml mibee-nvr.yaml

# Run
./mibee-nvr -config mibee-nvr.yaml
```

Open `http://localhost:9090` to access the Web UI.

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/en/getting-started.md) | Installation, first camera setup |
| [Configuration](docs/en/configuration.md) | Full config reference |
| [API Reference](docs/en/api-reference.md) | REST API documentation |
| [MediaMTX Guide](docs/en/mediamtx-guide.md) | MediaMTX integration for CSI cameras |
| [Deployment](docs/en/deployment.md) | systemd, reverse proxy, cross-compile |

## Build

```bash
make build        # Local build
make cross        # Cross-compile for ARM64
make test         # Run tests
make lint         # Run linter
```

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
