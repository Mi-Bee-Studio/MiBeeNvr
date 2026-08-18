# Setup Wizard

> For MiBeeNvr v0.11.0

When MiBee NVR starts for the first time without an admin password, the web interface automatically opens a **single-page setup wizard** that configures the account, language, and storage location in one step.

![Setup wizard](images/wizard.webp)

## When the Wizard Appears

The "Initial Setup" page appears when either of the following is true:

- MiBee NVR is started via Docker without an existing config file mounted (auto-initialization)
- `mibee-nvr.yaml` was deleted before restart

If a password was already set via `mibee-nvr init --password` or the config file, the wizard is skipped and the login page is shown instead.

## Wizard Contents

### 1. Admin Account

- **Username**: defaults to `admin`, changeable
- **Password**: at least 8 characters
- **Confirm Password**: re-enter the password

> Passwords are stored as hashes and cannot be recovered — keep them safe.

### 2. Browser Capability Detection

The wizard live-detects which of the four playback protocols the current browser supports and shows a **recommended streaming protocol**:

| Check | Description |
|-------|-------------|
| LL-HLS | Low-latency HLS (WebCodecs) |
| WebRTC | Ultra-low-latency real-time playback |
| HTTP-FLV | HTTP-FLV stream playback |
| HLS | Best-compatibility HLS playback |

When everything is supported, LL-HLS is recommended; older browsers without WebCodecs fall back to HLS automatically. This only affects the default player choice, not recording.

### 3. Interface Language

Choose the web UI language (**中文 / English**). The choice is stored in the browser and can be changed at any time via the language dropdown in the top-right corner.

### 4. Storage Path

Specify where recordings are stored:

- **Leave empty** (recommended): keep the server-side setting (`server.data_dir`)
- **Enter a path**: e.g. an external drive mount point like `/mnt/storage/nvr`; make sure the directory exists and is writable

### 5. Complete Setup

Click "Complete Setup". The wizard saves the admin credentials, logs you in automatically, and drops you into the web UI. There are no cameras yet — you can:

- Click "Scan Devices" on the Cameras page to auto-discover ONVIF / Xiaomi cameras
- Or click "+ Add Camera" to add one manually (see [Quick Start](quickstart.md))

## Resetting the Wizard

To re-run the wizard, delete the config file and restart:

```bash
rm mibee-nvr.yaml
# Restart MiBee NVR — the wizard appears automatically
```

> ⚠️ Deleting the config file discards settings such as the camera list (recordings live in the data directory and are not affected).

## Advanced Configuration

After the wizard, configure advanced options via the web UI or by editing the config file:

### Recording Settings

```yaml
recording:
  segment_duration: "1m"        # Recording segment length
  max_days: 30                  # Retention period in days
  storage_path: "/data"         # Recording storage path
```

### Stream Ingest

Enable SRT / RTMP ingest:

```yaml
srt:
  enabled: true
  port: 9000

rtmp:
  enabled: true
  port: 1935
```

### MQTT Integration

```yaml
mqtt:
  enabled: true
  broker: "mqtt://localhost:1883"
  topic: "mibee/nvr"
```

### Timelapse

```yaml
cameras:
  - id: "construction"
    name: "Construction Site"
    protocol: "timelapse"
    enabled: true
    timelapse:
      enabled: true
      merge_duration: "natural-day"
      output_fps: 30
```

## Full Config File Structure

```yaml
# MiBee NVR Configuration
server:
  listen: ":9090"
  data_dir: "/var/lib/mibee-nvr"

auth:
  username: "admin"
  password: "your-password"

recording:
  segment_duration: "1m"
  max_days: 30

srt:
  enabled: false
  port: 9000

rtmp:
  enabled: false
  port: 1935

mqtt:
  enabled: false

webdav:
  enabled: true
  read_write: false

ftp:
  enabled: true
  port: 2121

cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:pass@192.168.1.100:554/stream"
    enabled: true
```

## Next Steps

- [Quick Start](quickstart.md) — add your first camera
- [Docker Deployment](install-docker.md) — production container deployment
- [NAS Deployment](install-nas.md) — NAS system installation
- [ONVIF Auto-Discovery](onvif-discovery.md) — camera auto-discovery
