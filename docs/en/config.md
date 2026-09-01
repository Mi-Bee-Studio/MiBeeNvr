# Configuration Reference

> For MiBeeNvr v0.12.0 · default config file `mibee-nvr.yaml` (override with `-config`)

A single YAML file drives all of MiBee NVR. This page is a **top-level key cheat sheet**; for every field see the [full configuration reference](https://github.com/Mi-Bee-Studio/MiBeeNvr/blob/main/docs/en/configuration.md) in the repository.

## Ways to Change Settings

There are **two entry points**; the web UI is the recommended one (persists immediately, a few fields need a restart):

| Entry point | Good for | Notes |
|------------|----------|-------|
| Web UI → Settings | Most runtime options | Storage, streaming, GB28181, AI detection, recording & processing pages |
| Edit the YAML | Bulk edits / bootstrapping | Camera list, deploy scripts; restart after editing |

> `mibee-nvr encrypt-config` encrypts plaintext secrets in place (see the [CLI reference](cli.md#encrypt-config-encrypt-sensitive-fields)).

## Top-Level Key Cheat Sheet

```yaml
server:
  listen: ":9090"            # HTTP listen address (web/API/streaming)

storage:
  root_dir: "/var/lib/mibee-nvr"  # storage root: recordings + database
  segment_duration: "30s"          # MP4 segment duration (≤30s recommended on RPi)

auth:
  username: "admin"
  password_hash: ""          # generate with: mibee-nvr hash-password <password>
  # local_bypass: false      # skip login for browsers on the host (localhost); default off; loopback + localhost Host only; NEVER enable behind a proxy/Docker

cameras: []                  # camera list (prefer maintaining via web UI, see below)

cleanup:
  retention_days: 30         # retention in days (1–3650), auto-cleaned when expired

merge:
  enabled: false             # periodic segment merging (8h/24h/7d/30d outputs)

ftp:
  enabled: true              # FTP access to recordings (default port 2121)

mqtt:
  enabled: false             # MQTT event integration; see the MQTT guide

webdav:
  enabled: true              # WebDAV access (/dav)

hls:
  write_buffer_size: 40      # async write buffer frames per HLS stream

observability:
  log_level: "info"          # debug / info / warn / error

security:
  frame_ancestors: ""        # CSP frame-ancestors (set when embedding the web UI cross-origin)
```

## Camera Entry Structure

One entry per camera — **protocol and encoding are separate fields**:

```yaml
cameras:
  - id: "front-door"             # stable unique ID (used for directories and the API)
    name: "Front Door"           # display name
    protocol: "rtsp"             # rtsp | http | onvif | xiaomi | srt | rtmp | gb28181 | timelapse
    encoding: "h264"             # h264 | h265 | mjpeg | jpeg (ONVIF auto-detects; may omit)
    url: "rtsp://192.168.1.100:554/stream"
    username: "admin"            # camera credentials (protocol-dependent)
    password: "camera123"
    enabled: true
    recording_enabled: true      # false = live-only, no recording
```

Full recipes per protocol: [ONVIF](onvif-discovery.md) · [Xiaomi](xiaomi-setup.md) · [SRT/RTMP](srt-rtmp.md) · [Raspberry Pi](raspberrypi.md) · [GB28181](gb28181-guide.md).

## Common Recipes

### External Drive Storage

```yaml
storage:
  root_dir: "/mnt/external/nvr"
  segment_duration: "30s"
```

### Docker Environment Overrides

Config values can be overridden via environment variables (common in containers):

```yaml
# docker-compose.yml
environment:
  - NVR_PASSWORD=initial-password   # admin password on first start
  - NVR_LISTEN_PORT=9090
  - NVR_DATA_DIR=/data
  - NVR_UID=1000
  - NVR_GID=1000
```

## Environment / Port Cheat Sheet

| Item | Default | Where |
|------|---------|-------|
| HTTP (web/API/HLS) | 9090 | `server.listen` / `NVR_LISTEN_PORT` |
| FTP | 2121 (passive 2122–2140) | `ftp.port` |
| SRT ingest | 9000/udp | `srt.port` |
| RTMP ingest | 1935 | `rtmp.port` |
| GB28181 SIP | 5060/udp | `gb28181.sip_listen` |

## Next Steps

- [CLI Reference](cli.md) — init / hash-password / encrypt-config and friends
- [Docker Deployment](install-docker.md) — container deployment and volume mounts
- [Full configuration reference (GitHub)](https://github.com/Mi-Bee-Studio/MiBeeNvr/blob/main/docs/en/configuration.md)
