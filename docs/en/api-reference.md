# MiBee NVR API Reference

## Table of Contents

- [Authentication](#authentication)
- [Recordings API](#recordings-api)
- [Cameras API](#cameras-api)
  - [Camera Snapshot](#camera-snapshot)
- [Stats & Settings API](#stats--settings-api)
- [Upload API](#upload-api)
- [Plugins API](#plugins-api)
- [Error Responses](#error-responses)
- [HTTP Status Codes](#http-status-codes)
- [Quick Start](#quick-start)

## Authentication

MiBee NVR uses HTTP Basic Authentication for protected endpoints. The authentication credentials are configured in the application settings.

### How to Use Basic Auth

```bash
curl -u username:password http://localhost:9090/api/recordings
```

### Authentication Behavior

- If `password_hash` is configured in the settings: All protected endpoints require valid Basic Auth credentials
- If `password_hash` is empty in settings: Authentication is bypassed (no protection)
- Failed authentication returns `401 Unauthorized` with empty body

## Recordings API

### List Recordings

**Endpoint:** `GET /api/recordings`

Retrieve a paginated list of recordings with optional filtering.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | No | Filter by camera ID | `cam1` |
|| `format` | string | No | Filter by format (h264, h265, or mjpeg) | `h264`
|| `merged` | boolean | No | Filter by merge status | `true` |
| `start` | string | No | Start time (RFC3339 format) | `2024-01-01T00:00:00Z` |
| `end` | string | No | End time (RFC3339 format) | `2024-01-02T00:00:00Z` |
| `limit` | integer | No | Maximum results (default: 50) | `20` |
| `offset` | integer | No | Result offset for pagination | `0` |

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings?format=h264&limit=10&offset=0"
```

**Response:**
```json
{
  "recordings": [
    {
      "id": "1704123456789012345",
      "camera_id": "cam1",
      "file_path": "/data/recordings/h264/cam1_1704123456789012345.mp4",
      "format": "h264",
      "started_at": "2024-01-01T12:34:56.789Z",
      "ended_at": "2024-01-01T12:35:06.789Z",
      "duration": 10.0,
      "file_size": 1048576,
      "frame_count": 300,
      "merged": 0,
    }
  ],
  "total": 1
}
```

### Get Recording

**Endpoint:** `GET /api/recordings/:id`

Retrieve a specific recording by ID.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345"
```

**Response:**
```json
{
  "id": "1704123456789012345",
  "camera_id": "cam1",
  "file_path": "/data/recordings/h264/cam1_1704123456789012345.mp4",
  "format": "h264",
  "started_at": "2024-01-01T12:34:56.789Z",
  "ended_at": "2024-01-01T12:35:06.789Z",
  "duration": 10.0,
  "file_size": 1048576,
  "frame_count": 300,
      "merged": 0,
}
```

### Delete Recording

**Endpoint:** `DELETE /api/recordings/:id`

Delete a recording by ID. Deletes from database first, then removes the file. File deletion is non-fatal - if the file cannot be deleted, the database record is still removed.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/recordings/1704123456789012345"
```

**Response:**
```json
{
  "status": "deleted"
}
```


### Download Recording

**Endpoint:** `GET /api/recordings/:id/download`

Download a recording file. Supports streaming download for large files.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `frame` | integer | No | For MJPEG format, download specific frame | `150` |

**Request (H264):**
```bash
curl -u username:password \
  -o recording.mp4 \
  "http://localhost:9090/api/recordings/1704123456789012345/download"
```

**Request (MJPEG with specific frame):**
```bash
curl -u username:password \
  -o frame_150.jpg \
  "http://localhost:9090/api/recordings/1704123456789012345/download?frame=150"
```

**Response:** Binary file content (MP4 or JPEG)

### List Recording Frames (MJPEG only)

**Endpoint:** `GET /api/recordings/:id/frames`

List all frames for an MJPEG recording.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/frames"
```

**Response:**
```json
{
  "frames": [
    {
      "index": 0,
      "filename": "cam1_1704123456789012345_0000.jpg",
      "size": 54321
    },
    {
      "index": 1,
      "filename": "cam1_1704123456789012345_0001.jpg",
      "size": 52345
    }
  ]
}
```

### Merge Status API

Get current merge manager status and statistics.

**Endpoint:** `GET /api/merge/status`

Retrieve merge manager operational status including error count and performance metrics.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/status"
```

**Response:**
```json
{
  "enabled": true,
  "error_count": 0,
  "files_created": 9,
  "last_run_time": "2026-05-11T06:37:41Z",
  "segments_merged": 235
}
```

### Get Pending Merge Counts

**Endpoint:** `GET /api/merge/pending`

Get count of segments pending merge for each camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/pending"
```

**Response:**
```json
{
  "pending": {
    "cam-xxx": 99,
    "cam-yyy": 145
  }
}
```

### Get Merge Configuration

**Endpoint:** `GET /api/settings/merge`

Get global merge settings configuration.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/settings/merge"
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

### Update Merge Configuration

**Endpoint:** `PUT /api/settings/merge`

Update global merge settings. All fields are optional.

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

### Set Per-Camera Merge Configuration

**Endpoint:** `PUT /api/cameras/:id/merge-config`

Set merge configuration overrides for a specific camera. All 6 parameters are required.

**Request Body:**
```json
{
  "enabled": true,
  "check_interval": "30m",
  "window_size": "1h",
  "batch_limit": 150,
  "min_segment_age": "5m",
  "min_segments_to_merge": 2
}

  # Note: When set, these override the global merge settings for this camera
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": false,
    "batch_limit": 50
  }' \
  "http://localhost:9090/api/cameras/cam1/merge-config"
```

**Response:**
```json
{
  "status": "updated"
}
```

### Reset Per-Camera Merge Configuration

**Endpoint:** `DELETE /api/cameras/:id/merge-config`

Remove per-camera merge overrides, camera will inherit global merge settings.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/cam1/merge-config"
```

**Response:**
```json
{
  "status": "reset"
}
```

## Cameras API

### List Cameras

**Endpoint:** `GET /api/cameras`

Get a list of all configured cameras.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras"
```

**Response:**
```json

[

  {

    "id": "cam1",

    "name": "Front Door Camera",

    "protocol": "rtsp_h264",
  "encoding": "h264",

    "url": "rtsp://192.168.1.100:554/stream",

    "enabled": true,

    "status": "recording",

    "last_seen": "2024-01-01T10:15:00Z",

    "retention_days": 30,
    "username": "admin",
    "has_password": true

  },

  {

    "id": "cam2",

    "name": "Backyard Camera",

    "protocol": "http_jpeg",
  "encoding": "jpeg",

    "url": "http://192.168.1.101:8080/cam.jpg",

    "enabled": false,

    "status": "stopped",

    "last_seen": "2024-01-01T09:30:00Z",

    "retention_days": 7,
    "username": "",
    "has_password": false

  }

]
```

### Create Camera

**Endpoint:** `POST /api/cameras`

Add a new camera configuration.

**Request Body:**
```json
{
  "name": "Garage Camera",
  "protocol": "rtsp_mjpeg",
  "encoding": "mjpeg",
  "url": "rtsp://192.168.1.102:554/mjpeg_stream",
  "username": "admin",
  "password": "secret",
  "enabled": true,
  "retention_days": 15
  
  # Note: retention_days is optional, defaults to global setting
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Garage Camera",
    "protocol": "rtsp_mjpeg",
  "encoding": "mjpeg",
    "url": "rtsp://192.168.1.102:554/mjpeg_stream",
    "username": "admin",
    "password": "secret",
    "enabled": true
  }' \
  "http://localhost:9090/api/cameras"
```

**Response (201 Created):**
```json
{
  "id": "cam3",
  "name": "Garage Camera",
  "protocol": "rtsp_mjpeg",
  "encoding": "mjpeg",
  "url": "rtsp://192.168.1.102:554/mjpeg_stream",
  "enabled": true
}
```

### Get Camera

**Endpoint:** `GET /api/cameras/:id`

Get a specific camera configuration.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/cam1"
```

**Response:**
```json
{

  "id": "cam1",

  "name": "Front Door Camera",

  "protocol": "rtsp_h264",
  "encoding": "h264",

  "url": "rtsp://192.168.1.100:554/stream",

  "enabled": true,

  "status": "recording",

  "last_seen": "2024-01-01T10:15:00Z",

  "retention_days": 30,
  "username": "admin",
  "has_password": true

}
```

### Update Camera

**Endpoint:** `PUT /api/cameras/:id`

Update camera configuration. All fields are optional for partial updates.

**Request Body:**
```json
{
  "name": "Updated Front Door Camera",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false,
  "retention_days": 7
  
  # Note: retention_days is optional, updates per-camera retention
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Updated Front Door Camera",
    "url": "rtsp://192.168.1.100:554/new_stream",
    "enabled": false
  }' \
  "http://localhost:9090/api/cameras/cam1"
```

**Response:**
```json
{
  "id": "cam1",
  "name": "Updated Front Door Camera",
  "protocol": "rtsp_h264",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false
}
```

### Delete Camera

**Endpoint:** `DELETE /api/cameras/:id`

Delete a camera configuration. Any active recordings from this camera are not deleted but will be marked accordingly.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/cam2"
```

**Response:**
```json
{
  "status": "deleted"
}
```

### Camera Snapshot

**Endpoint:** `GET /api/cameras/{id}/snapshot`

Get a JPEG snapshot image from a camera. Requires `snapshot_url` to be configured for the camera.

**Path Parameters:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Camera ID |

**Response:**
- `200 OK` — JPEG image with `Content-Type: image/jpeg` and `Cache-Control: max-age=5`
- `404 Not Found` — Camera not found or no snapshot URL configured
- `502 Bad Gateway` — Snapshot URL unreachable and no cached image available

**Cache Behavior:**
- Snapshots are cached for 10 seconds (server-side)
- When the camera is temporarily unreachable, stale cached snapshots are served with `X-Cache: stale` header
- Client-side cache: 5 seconds (`Cache-Control: max-age=5`)

**Request:**
```bash
curl -u admin:admin http://localhost:9090/api/cameras/cam1/snapshot -o snapshot.jpg
```
## Stats & Settings API

### Live Stream (HLS)

**Endpoint:** `GET /api/cameras/:id/stream/*path`

Provide on-demand HLS live streaming for H.264 and H.265 cameras. Click "实时" button in Web UI to start streaming. Auto-stops after 60s idle. Max 4 concurrent streams.

**Path Parameters:**
- `:id` - Camera ID
- `*path` - HLS segment path (m3u8, ts, m4s)

**Query Parameters:**
- `format` - Video format: `h264`, `h265`, `mjpeg`

**Status Codes:**
- `200` - HLS segment delivered
- `404` - Camera or segment not found
- `503` - Maximum concurrent streams reached

**Request (HLS playlist):**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/cam1/stream/stream.m3u8"
```

**Request (HLS segment):**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/cam1/stream/segment_001.ts"
```

**Response:** HLS playlist or segment file content

### Get System Stats

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

### Get Settings

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
  }
}
```

### Update Settings

**Endpoint:** `PUT /api/settings`

Update cleanup settings. All fields are optional.

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

## Upload API

### Upload File

**Endpoint:** `POST /api/upload`

Upload a file via multipart form. Maximum file size is 100MB.

**Request:**
```bash
curl -u username:password \
  -X POST \
  -F "file=@/path/to/local/file.mp4" \
  "http://localhost:9090/api/upload"
```

**Response (200 OK):**
```json
{
  "status": "uploaded",
  "filename": "file.mp4",
  "size": 1048576
}
```

**Response (400 Bad Request):**
```json
{
  "error": "file too large (max 100MB)"
}
```

## Plugins API

### List Plugins

**Endpoint:** `GET /api/plugins`

Retrieve a list of all loaded plugins with their status and capabilities. Returns deduplicated list where gRPC managed plugins take priority.

**Request:**

```bash
curl -u username:password \
  "http://localhost:9090/api/plugins"
```

**Response:**

```json
{
  "plugins": [
    {
      "name": "xiaomi",
      "version": "1.0.0",
      "status": "running",
      "protocols": ["xiaomi"],
      "capabilities": {
        "hls": false,
        "ptz": false,
        "snapshot": false,
        "discovery": true,
        "auth": false
      },
      "supported_encodings": ["h264", "h265"],
      "uptime_seconds": 3600,
      "restart_count": 0
    }
  ]
}
```

### Get Plugin

**Endpoint:** `GET /api/plugins/{name}`

Retrieve details for a specific plugin by name.

**Request:**

```bash
curl -u username:password \
  "http://localhost:9090/api/plugins/xiaomi"
```

**Response:**

```json
{
  "name": "xiaomi",
  "version": "1.0.0",
  "status": "running",
  "protocols": ["xiaomi"],
  "capabilities": {
    "hls": false,
    "ptz": false,
    "snapshot": false,
    "discovery": true,
    "auth": false
  },
  "supported_encodings": ["h264", "h265"],
  "uptime_seconds": 3600,
  "restart_count": 0
}
```

### Restart Plugin

**Endpoint:** `POST /api/plugins/{name}/restart`

Restart a plugin process. This will gracefully shutdown the plugin and start a new instance.

**Request:**

```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/plugins/xiaomi/restart"
```

**Response:**

```json
{
  "status": "restarting"
}
```

### Get Plugin Capabilities

**Endpoint:** `GET /api/plugins/{name}/capabilities`

Retrieve plugin capabilities without detailed status information.

**Request:**

```bash
curl -u username:password \
  "http://localhost:9090/api/plugins/xiaomi/capabilities"
```

**Response:**

```json
{
  "name": "xiaomi",
  "version": "1.0.0",
  "protocols": ["xiaomi"],
  "supported_encodings": ["h264", "h265"],
  "capabilities": {
    "hls": false,
    "ptz": false,
    "snapshot": false,
    "discovery": true,
    "auth": false
  }
}
```

### List Protocols

**Endpoint:** `GET /api/protocols`

Retrieve a merged list of all available protocols from both built-in recorders and plugins. Built-in protocols take priority in case of conflicts.

**Request:**

```bash
curl -u username:password \
  "http://localhost:9090/api/protocols"
```

**Response:**

```json
{
  "protocols": [
    {
      "id": "rtsp",
      "label": "RTSP",
      "encodings": ["h264", "h265", "mjpeg"],
      "built_in": true,
      "capabilities": {
        "hls": true,
        "ptz": false
      }
    },
    {
      "id": "xiaomi",
      "label": "xiaomi",
      "encodings": ["h264", "h265"],
      "built_in": false,
      "capabilities": {
        "discovery": true,
        "hls": false,
        "ptz": false,
        "snapshot": false
      }
    }
  ]
}
```

| Code | Description |
|------|-------------|
| 200 | OK - Request successful |
| 201 | Created - Resource successfully created |
| 400 | Bad Request - Invalid request parameters |
| 401 | Unauthorized - Authentication failed or required |
| 403 | Forbidden - Resource access not allowed |
| 404 | Not Found - Resource does not exist |
| 500 | Internal Server Error - Server-side error |

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
    "protocol": "rtsp_h264",
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
```

### List Protocols

**Endpoint:** `GET /api/protocols`

Retrieve a merged list of all available protocols from both built-in recorders and plugins. Built-in protocols take priority in case of conflicts.

**Request:**

```bash
curl -u username:password \
  "http://localhost:9090/api/protocols"
```

**Response:**

```json
{
  "protocols": [
    {
      "id": "rtsp",
      "label": "RTSP",
      "encodings": ["h264", "h265", "mjpeg"],
      "built_in": true,
      "capabilities": {
        "hls": true,
        "ptz": false
      }
    },
    {
      "id": "xiaomi",
      "label": "xiaomi",
      "encodings": ["h264", "h265"],
      "built_in": false,
      "capabilities": {
        "discovery": true,
        "hls": false,
        "ptz": false,
        "snapshot": false
      }
    }
  ]
}
```

### Get Plugin Capabilities

**Endpoint:** `GET /api/plugins/{name}/capabilities`

Retrieve plugin capabilities without detailed status information.

**Request:**

```bash
curl -u username:password \
  "http://localhost:9090/api/plugins/xiaomi/capabilities"
```

**Response:**

```json
{
  "name": "xiaomi",
  "version": "1.0.0",
  "protocols": ["xiaomi"],
  "supported_encodings": ["h264", "h265"],
  "capabilities": {
    "hls": false,
    "ptz": false,
    "snapshot": false,
    "discovery": true,
    "auth": false
  }
}
```

### Restart Plugin

**Endpoint:** `POST /api/plugins/{name}/restart`

Restart a plugin process. This will gracefully shutdown the plugin and start a new instance.

**Request:**

```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/plugins/xiaomi/restart"
```

**Response:**

```json
{
  "status": "restarting"
}
```

### Get Plugin

**Endpoint:** `GET /api/plugins/{name}`

Retrieve details for a specific plugin by name.

**Request:**

```bash
curl -u username:password \
  "http://localhost:9090/api/plugins/xiaomi"
```

**Response:**

```json
{
  "name": "xiaomi",
  "version": "1.0.0",
  "status": "running",
  "protocols": ["xiaomi"],
  "capabilities": {
    "hls": false,
    "ptz": false,
    "snapshot": false,
    "discovery": true,
    "auth": false
  },
  "supported_encodings": ["h264", "h265"],
  "uptime_seconds": 3600,
  "restart_count": 0
}
```

## Plugins API

### List Plugins

**Endpoint:** `GET /api/plugins`

Retrieve a list of all loaded plugins with their status and capabilities. Returns deduplicated list where gRPC managed plugins take priority.

**Request:**

```bash
curl -u username:password \
  "http://localhost:9090/api/plugins"
```

**Response:**

```json
{
  "plugins": [
    {
      "name": "xiaomi",
      "version": "1.0.0",
      "status": "running",
      "protocols": ["xiaomi"],
      "capabilities": {
        "hls": false,
        "ptz": false,
        "snapshot": false,
        "discovery": true,
        "auth": false
      },
      "supported_encodings": ["h264", "h265"],
      "uptime_seconds": 3600,
      "restart_count": 0
    }
  ]
}
```