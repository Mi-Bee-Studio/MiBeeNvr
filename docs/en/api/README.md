# MiBee NVR API Reference

This is the REST API reference for MiBee NVR. All API endpoints are documented with their request formats, response schemas, and example `curl` commands.

Most endpoints require authentication via HTTP Basic Auth. See [Authentication](authentication.md) for details.

## API Sections

| Section | File | Description |
|---------|------|-------------|
| Authentication | [authentication.md](authentication.md) | Login, setup, capabilities, basic auth usage |
| Health & System | [system.md](system.md) | Health check, readiness check, system stats |
| Health Monitoring | [health-monitoring.md](health-monitoring.md) | Camera health status, stability scores, health events |
| Cameras | [cameras.md](cameras.md) | Camera CRUD, test connection, start/stop, snapshots |
| Streaming | [streaming.md](streaming.md) | HLS, WebRTC, HTTP-FLV, WebSocket streaming, camera protocols |
| Camera Stats & Events | [camera-details.md](camera-details.md) | Recording stats, camera event stream (SSE) |
| ONVIF | [onvif.md](onvif.md) | PTZ control, presets, imaging, network, users, discovery |
| Recordings | [recordings.md](recordings.md) | List, get, delete, download recordings, timelapse frames |
| Archives | [archives.md](archives.md) | Archive groups, retention management |
| Settings | [settings.md](settings.md) | System stats, settings CRUD, merge/streaming/transcoding settings, MiBeeVision integration status |
| GB28181 | [../gb28181-guide.md](../gb28181-guide.md) | GB/T 28181 platform: devices/channels, catalog refresh, alarms, PTZ, device-side recording search & playback |
| Xiaomi | [xiaomi.md](xiaomi.md) | Cloud auth, captcha, device management |
| Merge & Timelapse Config | [merge.md](merge.md) | Merge status, pending counts, camera merge/timelapse config |
| Transcoding | [transcoding.md](transcoding.md) | FFmpeg management, transcode tasks, backfill, camera configs |
| Relay | [relay-guide.md](../relay-guide.md) | Push-out relay presets, camera relay status |
| AI Detection | [ai-detection.md](ai-detection.md) | AI config, ROI zones, model serving, AI event API (browser-side inference) |
| Events | [events.md](events.md) | Event stream (SSE), camera events, telemetry |
| Timelapse & Protocols | [timelapse-protocols.md](timelapse-protocols.md) | Timelapse status, supported protocols, feature flags |
| Backup | [backup.md](backup.md) | Create/list database backups |
| Error Responses | [errors.md](errors.md) | Error codes, HTTP status codes, common examples |
| Prometheus Metrics | [../metrics.md](../metrics.md) | All Prometheus metric definitions, types, labels, and usage |

## Quick Start

### Basic Authentication Test

```bash
# Test health endpoint (no auth required)
curl http://localhost:9090/api/health

# Test authentication
curl -u admin:password http://localhost:9090/api/cameras
```

### Common Operations

```bash
# List all recordings
curl -u admin:password "http://localhost:9090/api/recordings"

# Add a new camera
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Living Room Cam",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.50:554/stream",
    "enabled": true
  }' \
  "http://localhost:9090/api/cameras"

# Download a recording
curl -u admin:password \
  -o recording.mp4 \
  "http://localhost:9090/api/recordings/1704123456789012345/download"

# Update settings to clean up recordings older than 7 days
curl -u admin:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "cleanup": {
      "retention_days": 7
    }
  }' \
  "http://localhost:9090/api/settings"

# Test camera connection
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "username": "admin",
    "password": "secret"
  }' \
  "http://localhost:9090/api/cameras/test-connection"
```

### HLS Streaming Example

```bash
# Get HLS playlist
curl -u admin:password \
  "http://localhost:9090/api/cameras/living-room/stream/stream.m3u8"

# Get HLS segment  
curl -u admin:password \
  "http://localhost:9090/api/cameras/living-room/stream/segment_001.ts"
```

### Xiaomi Camera Setup

```bash
# Authenticate with Xiaomi cloud
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/auth"

# List Xiaomi devices
curl -u admin:password \
  "http://localhost:9090/api/xiaomi/devices"
```

## Error Responses

All error responses follow this format:

```json
{
  "error": "Error message",
  "code": "ERROR_CODE"
}
```

See [Error Responses](errors.md) for the full error code reference and HTTP status codes.
