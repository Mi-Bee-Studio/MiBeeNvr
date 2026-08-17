# Stats & Settings API

## System Statistics

**Endpoint:** `GET /api/stats`

Get system statistics including storage usage and recording counts.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats"
```

**Response:**
```json
{
  "total_bytes": 1073741824,
  "used_bytes": 536870912,
  "recording_count": 1000,
  "camera_count": 4
}
```

## Stats Trends

**Endpoint:** `GET /api/stats/trends`

Get storage usage trends over time.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats/trends"
```

**Response:**
```json
{
  "trends": [
    {
      "date": "2024-01-01",
      "total_bytes": 1000000000,
      "used_bytes": 500000000,
      "recording_count": 950
    }
  ]
}
```

## Get Settings

**Endpoint:** `GET /api/settings`

Get current configuration settings.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings"
```

**Response:**
```json
{
  "cleanup": {
    "retention_days": 30,
    "check_interval": "1h",
    "disk_threshold_percent": 85
  },
  "webdav": {
    "enabled": true,
    "path_prefix": "/dav",
    "read_write": false
  },
  "auth": {
    "username": "admin",
    "auth_configured": true
  },
  "mibeevision": {
    "api_keys": [
      {
        "name": "vision",
        "prefix": "mbv_1a2b…",
        "revoked": false,
        "last_used": "2026-08-17T02:00:00Z"
      }
    ]
  },
  "timezone": "Local",
  "timezone_display": "CST (UTC+8)",
  "server": {
    "listen": ":9090"
  },
  "gb28181": {
    "enabled": false,
    "sip_listen": ":5060",
    "server_id": "34020000002000000001",
    "realm": "3402000000",
    "password_configured": true,
    "port_range": "30000-30050",
    "allowed_device_ids": [],
    "heartbeat_interval": "60s",
    "catalog_interval": "30m",
    "tcp_mode": false,
    "tcp_framing": "auto",
    "media_transport": "udp",
    "sip_transport": "udp",
    "subscribe_catalog": true,
    "subscribe_alarm": false,
    "subscribe_mobile_position": false,
    "subscribe_expires": "3600s"
  }
}
```

`mibeevision.api_keys` never returns full secrets (prefix only); `last_used` is the
UTC time that key was last used (minute granularity), omitted when never used.

## MiBeeVision Integration Status

### Query Vision consumer health

**Endpoint:** `GET /api/vision/status`

Report the health of the external AI processing consumer (MiBeeVision), as shown in
the web UI. Returns `{"enabled": false}` when the integration is not enabled.

**Request:**
```bash
curl -u username:password   "http://localhost:9090/api/vision/status"
```

**Response:**
```json
{
  "enabled": true,
  "healthy": true,
  "device": "jetson-orin",
  "queue_depth": 0,
  "processed": 12841,
  "last_seen": "2026-08-17T12:00:00Z"
}
```

`last_seen` is omitted when no heartbeat has ever been received. The NVR only pushes
video segments to Vision while `healthy` is `true`; after a heartbeat returns, missed
segments are automatically re-pushed as compensation.

### Vision heartbeat

**Endpoint:** `POST /api/vision/heartbeat` (public, no auth)

The Vision service reports every 30 seconds. Request body:

```json
{
  "status": "ok",
  "device": "jetson-orin",
  "queue_depth": 0,
  "processed_count": 12841
}
```

**Response:**
```json
{
  "ok": true,
  "push_enabled": true
}
```

## Update Settings

**Endpoint:** `PUT /api/settings`

Update configuration settings.

**Request Body:**
```json
{
  "cleanup": {
    "retention_days": 60,
    "disk_threshold_percent": 90,
    "check_interval": "30m"
  }
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "cleanup": {
      "retention_days": 60,
      "disk_threshold_percent": 90,
      "check_interval": "30m"
    }
  }' \
  "http://localhost:9090/api/settings"
```

**Response:**
```json
{
  "status": "updated"
}
```

## Get Merge Settings

**Endpoint:** `GET /api/settings/merge`

Get global merge settings configuration.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings/merge"
```

**Response:**
```json
{
  "enabled": true,
  "check_interval": "1h",
  "window_size": "1h",
  "batch_limit": 200,
  "min_segment_age": "10m",
  "min_segments_to_merge": 3
}
```

## Update Merge Settings

**Endpoint:** `PUT /api/settings/merge`

Update global merge settings.

**Request Body:**
```json
{
  "enabled": true,
  "check_interval": "30m",
  "window_size": "2h",
  "batch_limit": 100,
  "min_segment_age": "15m",
  "min_segments_to_merge": 5
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "check_interval": "30m",
    "batch_limit": 100
  }' \
  "http://localhost:9090/api/settings/merge"
```

**Response:**
```json
{
  "status": "updated"
}
```

## Streaming & Transcoding Settings API

## Get Streaming Settings

**Endpoint:** `GET /api/settings/streaming`

Get the current streaming configuration including default protocol, WebRTC, FLV, and HLS low-latency settings.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings/streaming"
```

**Response:**
```json
{
  "default_protocol": "webrtc",
  "webrtc": {
    "enabled": true,
    "max_viewers": 10,
    "idle_timeout": "5m"
  },
  "flv": {
    "enabled": true,
    "max_viewers": 10,
    "idle_timeout": "5m",
    "gop_cache_size": 25
  },
  "hls": {
    "low_latency": true
  }
}
```

## Update Streaming Settings

**Endpoint:** `PUT /api/settings/streaming`

Update the streaming configuration. All fields are optional for partial updates.

**Request Body:**
```json
{
  "default_protocol": "flv",
  "webrtc": {
    "enabled": true,
    "max_viewers": 5,
    "idle_timeout": "10m"
  },
  "flv": {
    "enabled": true,
    "max_viewers": 5,
    "idle_timeout": "10m",
    "gop_cache_size": 50
  },
  "hls": {
    "low_latency": false
  }
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "default_protocol": "flv",
    "webrtc": {
      "max_viewers": 5
    }
  }' \
  "http://localhost:9090/api/settings/streaming"
```

**Response:**
```json
{
  "status": "updated"
}
```

## Get Transcoding Settings

**Endpoint:** `GET /api/settings/transcoding`

Get the global transcoding configuration.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/settings/transcoding"
```

**Response:**
```json
{
  "enabled": true,
  "max_workers": 2
}
```

## Update Transcoding Settings

**Endpoint:** `PUT /api/settings/transcoding`

Update the global transcoding configuration.

**Request Body:**
```json
{
  "enabled": true,
  "max_workers": 2
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "max_workers": 2
  }' \
  "http://localhost:9090/api/settings/transcoding"
```

**Response:**
```json
{
  "status": "updated"
}
```
