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
  "server": {
    "listen": ":9090"
  },
  "storage": {
    "root_dir": "/var/lib/mibee-nvr",
    "segment_duration": "30s"
  },
  "cleanup": {
    "retention_days": 30,
    "check_interval": "1h",
    "disk_threshold_percent": 95
  },
  "auth": {
    "username": "admin"
  }
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
