# MiBee NVR API Reference

## Table of Contents

- [Authentication](#authentication)
- [Health & System API](#health--system-api)
- [Cameras API](#cameras-api)
  - [Camera Management](#camera-management)
  - [Camera Snapshot](#camera-snapshot)
  - [HLS Streaming](#hls-streaming)
  - [ONVIF Camera Control](#onvif-camera-control)
  - [Camera Merge Configuration](#camera-merge-configuration)
- [Recordings API](#recordings-api)
- [Archive API](#archive-api)
- [Stats & Settings API](#stats--settings-api)
- [ONVIF API](#onvif-api)
- [Xiaomi API](#xiaomi-api)
- [Merge API](#merge-api)
- [Features API](#features-api)
- [Error Responses](#error-responses)
- [HTTP Status Codes](#http-status-codes)
- [Quick Start](#quick-start)

## Authentication

MiBee NVR uses HTTP Basic Authentication for protected endpoints. The authentication credentials are configured in the application settings.

### How to Use Basic Auth

```bash
curl -u username:password http://localhost:9090/api/cameras
```

### Authentication Behavior

- If `password_hash` is configured in the settings: All protected endpoints require valid Basic Auth credentials
- If `password_hash` is empty in settings: Authentication is bypassed (no protection)
- Failed authentication returns `401 Unauthorized` with empty body

## Health & System API

### Health Check

**Endpoint:** `GET /api/health`

Get overall system health status including database and storage disk space.

**Request:**
```bash
curl http://localhost:9090/api/health
```

**Response:**
```json
{
  "status": "ok",
  "checks": {
    "database": {
      "status": "ok",
      "message": ""
    },
    "storage": {
      "status": "ok", 
      "message": ""
    }
  },
  "uptime": "2h34m15s"
}
```

### Readiness Check

**Endpoint:** `GET /api/readyz`

Check if the system is ready to accept requests (same as health check).

**Request:**
```bash
curl http://localhost:9090/api/readyz
```

**Response:**
```json
{
  "status": "ok"
}
```

### System Stats

**Endpoint:** `GET /api/stats/system`

Get detailed system statistics including CPU, memory, and network usage.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats/system"
```

**Response:**
```json
{
  "cpu": {
    "total": 1234567,
    "idle": 987654
  },
  "memory": {
    "total": 1073741824,
    "available": 536870912,
    "process_rss": 10485760
  },
  "network": {
    "bytes_sent": 1048576,
    "bytes_recv": 2097152
  },
  "uptime": "2h34m15s",
  "timestamp": 1716789012
}
```

## Cameras API

### Camera Management

#### List Cameras

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
    "id": "front-door",
    "name": "Front Door",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "enabled": true,
    "status": "recording",
    "last_seen": "2024-01-01T10:15:00Z",
    "retention_days": 30,
    "username": "admin",
    "has_password": true,
    "sub_stream_url": "",
    "snapshot_url": "",
    "sample_interval": 1,
    "hls_max_fps": 30,
    "did": "",
    "vendor": ""
  }
]
```

#### Create Camera

**Endpoint:** `POST /api/cameras`

Add a new camera configuration.

**Request Body:**
```json
{
  "name": "Front Door",
  "protocol": "rtsp",
  "encoding": "h264", 
  "url": "rtsp://192.168.1.100:554/stream",
  "username": "admin",
  "password": "secret",
  "enabled": true,
  "retention_days": 30,
  "sub_stream_url": "rtsp://192.168.1.100:554/sub_stream",
  "snapshot_url": "http://192.168.1.100:8080/snapshot",
  "sample_interval": 1,
  "hls_max_fps": 30
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Front Door",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "username": "admin",
    "password": "secret",
    "enabled": true
  }' \
  "http://localhost:9090/api/cameras"
```

**Response (201 Created):**
```json
{
  "id": "front-door",
  "name": "Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true
}
```

#### Get Camera

**Endpoint:** `GET /api/cameras/:id`

Get a specific camera configuration.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door"
```

**Response:**
```json
{
  "id": "front-door",
  "name": "Front Door", 
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true,
  "status": "recording",
  "last_seen": "2024-01-01T10:15:00Z"
}
```

#### Update Camera

**Endpoint:** `PUT /api/cameras/:id`

Update camera configuration. All fields are optional for partial updates.

**Request Body:**
```json
{
  "name": "Updated Front Door",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false,
  "retention_days": 7
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Updated Front Door",
    "url": "rtsp://192.168.1.100:554/new_stream",
    "enabled": false
  }' \
  "http://localhost:9090/api/cameras/front-door"
```

**Response:**
```json
{
  "id": "front-door",
  "name": "Updated Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false
}
```

#### Delete Camera

**Endpoint:** `DELETE /api/cameras/:id`

Delete a camera configuration.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/backyard"
```

**Response:**
```json
{
  "status": "deleted"
}
```

#### Test Connection

**Endpoint:** `POST /api/cameras/test-connection`

Test camera connection with provided configuration.

**Request Body:**
```json
{
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "username": "admin", 
  "password": "secret"
}
```

**Request:**
```bash
curl -u username:password \
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

**Response:**
```json
{
  "success": true,
  "message": "Connection successful",
  "details": {
    "protocol": "rtsp",
    "encoding": "h264",
    "latency_ms": 45,
    "frames_received": 10
  }
}
```

#### Start Camera

**Endpoint:** `POST /api/cameras/:id/start`

Start recording for a camera.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/front-door/start"
```

**Response:**
```json
{
  "status": "started"
}
```

#### Stop Camera

**Endpoint:** `POST /api/cameras/:id/stop`

Stop recording for a camera.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/front-door/stop"
```

**Response:**
```json
{
  "status": "stopped"
}
```

### Camera Snapshot

**Endpoint:** `GET /api/cameras/{id}/snapshot`

Get a JPEG snapshot image from a camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/snapshot" \
  -o snapshot.jpg
```

**Response:** JPEG image with `Content-Type: image/jpeg` and `Cache-Control: max-age=5`

### HLS Streaming

**Endpoint:** `GET /api/cameras/:id/stream/*path`

Provide on-demand HLS live streaming.

**Request (HLS playlist):**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream/stream.m3u8"
```

**Request (HLS segment):**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream/segment_001.ts"
```

**Response:** HLS playlist or segment file content

### ONVIF Camera Control

#### Get ONVIF Profiles

**Endpoint:** `GET /api/cameras/:id/onvif/profiles`

Get available media profiles for an ONVIF camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/onvif/profiles"
```

**Response:**
```json
{
  "profiles": [
    {
      "token": "profile_1",
      "name": "Profile 1",
      "video_encoder": "H264",
      "audio_encoder": "G.711"
    }
  ]
}
```

#### PTZ Control

**Endpoint:** `POST /api/cameras/:id/ptz/move`

Move PTZ camera.

**Request Body:**
```json
{
  "x": 0.5,
  "y": 0.5,
  "zoom": 1.0,
  "speed": 1.0
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "x": 0.5,
    "y": 0.5,
    "zoom": 1.0
  }' \
  "http://localhost:9090/api/cameras/lobby/ptz/move"
```

**Response:**
```json
{
  "status": "moving"
}
```

#### Stop PTZ

**Endpoint:** `POST /api/cameras/:id/ptz/stop`

Stop PTZ movement.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/lobby/ptz/stop"
```

**Response:**
```json
{
  "status": "stopped"
}
```

#### Get PTZ Status

**Endpoint:** `GET /api/cameras/:id/ptz/status`

Get current PTZ position.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/lobby/ptz/status"
```

**Response:**
```json
{
  "x": 0.5,
  "y": 0.5,
  "zoom": 1.0
}
```

### Camera Merge Configuration

#### Update Camera Merge Configuration

**Endpoint:** `PUT /api/cameras/:id/merge-config`

Set merge configuration overrides for a specific camera.

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
  "http://localhost:9090/api/cameras/front-door/merge-config"
```

**Response:**
```json
{
  "status": "updated"
}
```

#### Delete Camera Merge Configuration

**Endpoint:** `DELETE /api/cameras/:id/merge-config`

Remove per-camera merge overrides.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/front-door/merge-config"
```

**Response:**
```json
{
  "status": "reset"
}
```

## Recordings API

### List Recordings

**Endpoint:** `GET /api/recordings`

Retrieve a paginated list of recordings with optional filtering.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | No | Filter by camera ID | `front-door` |
| `format` | string | No | Filter by format (h264, h265, mjpeg) | `h264` |
| `merged` | boolean | No | Filter by merge status | `true` |
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
      "camera_id": "front-door",
      "file_path": "/data/recordings/h264/front-door_1704123456789012345.mp4",
      "format": "h264",
      "started_at": "2024-01-01T12:34:56.789Z",
      "ended_at": "2024-01-01T12:35:06.789Z",
      "duration": 10.0,
      "file_size": 1048576,
      "frame_count": 300,
      "merged": false
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
  "camera_id": "front-door",
  "file_path": "/data/recordings/h264/front-door_1704123456789012345.mp4",
  "format": "h264",
  "started_at": "2024-01-01T12:34:56.789Z",
  "ended_at": "2024-01-01T12:35:06.789Z",
  "duration": 10.0,
  "file_size": 1048576,
  "frame_count": 300,
  "merged": false
}
```

### Delete Recording

**Endpoint:** `DELETE /api/recordings/:id`

Delete a recording by ID.

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

Download a recording file.

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
      "filename": "front-door_1704123456789012345_0000.jpg",
      "size": 54321
    },
    {
      "index": 1,
      "filename": "front-door_1704123456789012345_0001.jpg",
      "size": 52345
    }
  ]
}
```

### Batch Delete Recordings

**Endpoint:** `POST /api/recordings/batch-delete`

Delete multiple recordings by ID.

**Request Body:**
```json
{
  "recording_ids": ["id1", "id2", "id3"]
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "recording_ids": ["1704123456789012345", "1704123456789012346"]
  }' \
  "http://localhost:9090/api/recordings/batch-delete"
```

**Response:**
```json
{
  "deleted": 2,
  "failed": 0
}
```

## Archive API

### List Archives

**Endpoint:** `GET /api/archives`

List all archive groups by camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/archives"
```

**Response:**
```json
{
  "archives": [
    {
      "camera_id": "front-door",
      "retention_days": 30,
      "recordings_count": 150,
      "total_size_mb": 1024
    }
  ]
}
```

### List Archive Recordings

**Endpoint:** `GET /api/archives/{cameraID}/recordings`

List recordings for a specific archive.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/archives/front-door/recordings"
```

**Response:**
```json
{
  "recordings": [
    {
      "id": "1704123456789012345",
      "started_at": "2024-01-01T12:34:56.789Z",
      "duration": 10.0,
      "file_size": 1048576
    }
  ],
  "total": 1
}
```

### Delete Archive Group

**Endpoint:** `DELETE /api/archives/{cameraID}`

Delete an entire archive group for a camera.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/archives/front-door"
```

**Response:**
```json
{
  "status": "deleted"
}
```

### Delete Archive Recording

**Endpoint:** `DELETE /api/archives/{cameraID}/recordings/{recordingID}`

Delete a specific recording from an archive.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/archives/front-door/recordings/1704123456789012345"
```

**Response:**
```json
{
  "status": "deleted"
}
```

### Set Archive Retention

**Endpoint:** `PUT /api/archives/{cameraID}/retention`

Set retention period for an archive group.

**Request Body:**
```json
{
  "retention_days": 60
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "retention_days": 60
  }' \
  "http://localhost:9090/api/archives/front-door/retention"
```

**Response:**
```json
{
  "status": "updated"
}
```

## Stats & Settings API

### System Statistics

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

### Stats Trends

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

### Update Settings

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

### Get Merge Settings

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

### Update Merge Settings

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

## ONVIF API

### Discover ONVIF Devices

**Endpoint:** `POST /api/onvif/discover`

Discover ONVIF devices on the network.

**Request Body:**
```json
{
  "timeout": 5,
  "target": "192.168.1.0/24"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "timeout": 5
  }' \
  "http://localhost:9090/api/onvif/discover"
```

**Response:**
```json
{
  "devices": [
    {
      "url": "http://192.168.1.104:80/onvif/device_service",
      "name": "Camera 1",
      "model": "ABC-123",
      "location": "Office"
    }
  ]
}
```

### Get ONVIF Device Detail

**Endpoint:** `GET /api/onvif/discover/{ip}`

Get detailed information about a specific ONVIF device.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/onvif/discover/192.168.1.104"
```

**Response:**
```json
{
  "device": {
    "url": "http://192.168.1.104:80/onvif/device_service",
    "name": "Camera 1",
    "model": "ABC-123",
    "location": "Office",
    "manufacturer": "CameraCo",
    "firmware_version": "1.2.3",
    "serial_number": "CAM123456"
  }
}
```

## Xiaomi API

### Xiaomi Cloud Authentication

**Endpoint:** `POST /api/xiaomi/auth`

Authenticate with Xiaomi cloud services.

**Request Body:**
```json
{
  "username": "xiaomi@example.com",
  "password": "password123"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/auth"
```

**Response:**
```json
{
  "user_id": "1234567890",
  "token": "xiaomi_token_123",
  "region": "cn"
}
```

### Get Xiaomi Captcha

**Endpoint:** `POST /api/xiaomi/captcha`

Get captcha for Xiaomi authentication.

**Request Body:**
```json
{
  "username": "xiaomi@example.com"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com"
  }' \
  "http://localhost:9090/api/xiaomi/captcha"
```

**Response:**
```json
{
  "captcha_id": "captcha_123",
  "captcha_image": "base64_encoded_image"
}
```

### Verify Xiaomi Captcha

**Endpoint:** `POST /api/xiaomi/verify`

Verify captcha and complete authentication.

**Request Body:**
```json
{
  "captcha_id": "captcha_123",
  "captcha_code": "ABC123",
  "username": "xiaomi@example.com",
  "password": "password123"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "captcha_id": "captcha_123",
    "captcha_code": "ABC123",
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/verify"
```

**Response:**
```json
{
  "user_id": "1234567890",
  "token": "xiaomi_token_123",
  "region": "cn"
}
```

### List Xiaomi Devices

**Endpoint:** `GET /api/xiaomi/devices`

Get list of Xiaomi devices from cloud.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/xiaomi/devices"
```

**Response:**
```json
{
  "devices": [
    {
      "did": "camera_did_123",
      "name": "Front Door Camera",
      "model": "xiaomi.camera.v2",
      "online": true
    }
  ]
}
```

### Sync Xiaomi Devices

**Endpoint:** `POST /api/xiaomi/sync`

Sync Xiaomi devices with local configuration.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/xiaomi/sync"
```

**Response:**
```json
{
  "synced": 2,
  "added": 1,
  "removed": 0
}
```

## Merge API

### Get Merge Status

**Endpoint:** `GET /api/merge/status`

Get current merge manager status and statistics.

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
    "front-door": 99,
    "backyard": 145
  }
}
```

## Features API

### Get Features

**Endpoint:** `GET /api/features`

Get enabled/disabled feature flags.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/features"
```

**Response:**
```json
{
  "features": {
    "experimental_hls": false,
    "auto_delete_old": true,
    "webdav_upload": true
  }
}
```

### Update Features

**Endpoint:** `PUT /api/features`

Update feature flags.

**Request Body:**
```json
{
  "experimental_hls": true,
  "auto_delete_old": false
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "experimental_hls": true,
    "auto_delete_old": false
  }' \
  "http://localhost:9090/api/features"
```

**Response:**
```json
{
  "status": "updated"
}
```

## Backup API

### Create Backup

**Endpoint:** `POST /api/backup`

Create a backup of the database and configuration.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/backup"
```

**Response:**
```json
{
  "backup_id": "backup_1704123456",
  "path": "/var/lib/mibee-nvr/backups/backup_1704123456.tar.gz",
  "size_bytes": 1048576,
  "created_at": "2024-01-01T12:34:56Z"
}
```

### List Backups

**Endpoint:** `GET /api/backups`

List available backups.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/backups"
```

**Response:**
```json
{
  "backups": [
    {
      "backup_id": "backup_1704123456",
      "path": "/var/lib/mibee-nvr/backups/backup_1704123456.tar.gz",
      "size_bytes": 1048576,
      "created_at": "2024-01-01T12:34:56Z"
    }
  ]
}
```

## Error Responses

All error responses follow this format:

```json
{
  "error": "Error message",
  "code": "ERROR_CODE"
}
```

### Error Code Reference

| Code | Description | HTTP Status |
|------|-------------|-------------|
| `CAMERA_NOT_FOUND` | Camera with specified ID does not exist | 404 |
| `CAMERA_ALREADY_RUNNING` | Camera recorder is already active | 400 |
| `CAMERA_DISABLED` | Camera is disabled and cannot be started | 400 |
| `CAMERA_ALREADY_EXISTS` | Camera with specified ID already exists | 409 |
| `RECORDING_NOT_FOUND` | Recording with specified ID does not exist | 404 |
| `STORAGE_FULL` | Disk space is critically low | 507 |
| `AUTH_REQUIRED` | Authentication is required | 401 |
| `AUTH_FAILED` | Authentication credentials were rejected | 401 |
| `INVALID_INPUT` | Request contains invalid parameters | 400 |
| `PATH_TRAVERSAL` | Path traversal attempt detected | 400 |
| `HLS_MAX_STREAMS` | Maximum concurrent HLS stream limit reached | 503 |
| `HLS_UNSUPPORTED_CODEC` | Camera codec is not supported for HLS streaming | 400 |
| `ONVIF_NOT_CAMERA` | Device is not an ONVIF camera | 400 |
| `ONVIF_CONNECTION_FAILED` | Failed to connect to ONVIF device | 500 |
| `ONVIF_NO_PROFILES` | No media profiles found for ONVIF camera | 400 |
| `INTERNAL` | Internal server error | 500 |

### Common Error Examples

#### Authentication Failed
```json
{
  "error": "authentication failed: invalid username or password",
  "code": "AUTH_FAILED"
}
```

#### Camera Not Found
```json
{
  "error": "camera not found: non-existent-camera",
  "code": "CAMERA_NOT_FOUND"
}
```

#### Invalid Input
```json
{
  "error": "invalid input: camera URL must be valid",
  "code": "INVALID_INPUT"
}
```

#### Storage Full
```json
{
  "error": "storage full: disk space critically low",
  "code": "STORAGE_FULL"
}
```

## HTTP Status Codes

| Status Code | Description |
|-------------|-------------|
| 200 | OK - Request successful |
| 201 | Created - Resource successfully created |
| 400 | Bad Request - Invalid request parameters |
| 401 | Unauthorized - Authentication failed or required |
| 403 | Forbidden - Resource access not allowed |
| 404 | Not Found - Resource does not exist |
| 409 | Conflict - Resource conflict (e.g., duplicate camera ID) |
| 500 | Internal Server Error - Server-side error |
| 503 | Service Unavailable - Service temporarily unavailable (e.g., max streams reached) |
| 507 | Insufficient Storage - Disk space insufficient |

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