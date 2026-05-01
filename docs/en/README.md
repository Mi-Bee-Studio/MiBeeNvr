# MiBee NVR

A lightweight Network Video Recorder (NVR) written in Go. Supports RTSP (H.264/MJPEG) and HTTP JPEG cameras, with a built-in Web UI, WebDAV, FTP, and MQTT integration. Compiles to a single static binary with an embedded SPA frontend.

<!-- TODO: add screenshots -->

## Features

- RTSP (H.264/MJPEG) and HTTP JPEG camera support
- Automatic MP4 segment recording
- Web UI for camera management and recording playback
- WebDAV (read-only) and FTP file access
- MQTT trigger-based recording for smart home integration
- Multi-camera concurrent recording
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
| [Getting Started](getting-started.md) | Installation, first camera setup |
| [Configuration](configuration.md) | Full config reference |
| [API Reference](api-reference.md) | REST API documentation |
| [MediaMTX Guide](mediamtx-guide.md) | MediaMTX integration for CSI cameras |
| [Deployment](deployment.md) | systemd, reverse proxy, cross-compile |

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
  recorder/          # H.264/MJPEG recording engines
  storage/           # SQLite DB + file manager
  config/            # YAML config
  middleware/        # Auth middleware
  muxer/             # MP4 muxer
  ftp/               # FTP server
  webdav/            # WebDAV server (read-only)
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

[MIT License](../../LICENSE) © Mi&Bee Studio
