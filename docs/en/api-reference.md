# MiBee NVR API Reference

## Table of Contents

- [Authentication](#authentication)
- [Recordings API](#recordings-api)
- [Cameras API](#cameras-api)
  - [Camera Snapshot](#camera-snapshot)
- [Stats & Settings API](#stats--settings-api)
- [Upload API](#upload-api)
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
#PR|```

#VY|### Merge Status API
#WK|
#RK|Get current merge manager status and statistics.
#JQ|
#JQ|**Endpoint:** `GET /api/merge/status`
#NX|
#RV|Retrieve merge manager operational status including error count and performance metrics.
#KR|
#JQ|**Request:**
#BV|```bash
#JS|curl -u username:password \
#XN|  "http://localhost:9090/api/merge/status"
#PW|```
#WY|
#QV|**Response:**
#YP|```json
#SV|{
#MQ|  "enabled": true,
#KM|  "error_count": 0,
#JK|  "files_created": 9,
#ZM|  "last_run_time": "2026-05-11T06:37:41Z",
#BJ|  "segments_merged": 235
#QR|}
#YV|```
#VS|
#VY|### Get Pending Merge Counts
#NT|
#HP|**Endpoint:** `GET /api/merge/pending`
#PN|
#SZ|Get count of segments pending merge for each camera.
#NJ|
#JQ|**Request:**
#BV|```bash
#JS|curl -u username:password \
#XN|  "http://localhost:9090/api/merge/pending"
#PW|```
#WY|
#QV|**Response:**
#YP|```json
#SV|{
#MQ|  "pending": {
#KM|    "cam-xxx": 99,
#JK|    "cam-yyy": 145
#ZM|  }
#QR|}
#YV|```
#VS|
#VY|### Get Merge Configuration
#HY|
#HP|**Endpoint:** `GET /api/settings/merge`
#PN|
#HV|Get global merge settings configuration.
#WP|
#JQ|**Request:**
#BV|```bash
#JS|curl -u username:password \
#XN|  "http://localhost:9090/api/merge/settings/merge"
#PW|```
#WY|
#QV|**Response:**
#YP|```json
#SV|{
#MQ|  "enabled": true,
#KM|  "check_interval": "1h",
#JK|  "window_size": "1h",
#ZM|  "batch_limit": 200,
#BJ|  "min_segment_age": "10m",
#RZ|  "min_segments_to_merge": 3
#QR|}
#YV|```
#VS|
#VY|### Update Merge Configuration
#MM|
#WY|**Endpoint:** `PUT /api/settings/merge`
#MK|
#VP|Update global merge settings. All fields are optional.
#RY|
#RX|**Request Body:**
#YP|```json
#KR|{
#WY|  "enabled": true,
#HZ|  "check_interval": "30m",
#QH|  "window_size": "2h",
#RT|  "batch_limit": 100,
#HV|  "min_segment_age": "15m",
#VX|  "min_segments_to_merge": 5
#XB|}
#BK|```
#KB|
#JQ|**Request:**
#BV|```bash
#JS|curl -u username:password \
#KX|  -X PUT \
#VQ|  -H "Content-Type: application/json" \
#BW|  -d '{
#RV|    "enabled": true,
#NH|    "check_interval": "30m",
#SS|    "batch_limit": 100
#BQ|  }' \
#JR|  "http://localhost:9090/api/settings/merge"
#QV|```
#HR|
#QV|**Response:**
#YP|```json
#VM|{
#QJ|  "status": "updated"
#SY|}
#YX|```
#SK|
#VY|### Set Per-Camera Merge Configuration
#BR|
#ZH|**Endpoint:** `PUT /api/cameras/:id/merge-config`
#WS|
#NK|Set merge configuration overrides for a specific camera. All 6 parameters are required.
#JY|
#RX|**Request Body:**
#YP|```json
#RJ|{
#QQ|  "enabled": true,
#NN|  "check_interval": "30m",
#XW|  "window_size": "1h",
#QR|  "batch_limit": 150,
#ZK|  "min_segment_age": "5m",
#QJ|  "min_segments_to_merge": 2
#NW|}
#SV|
#PX|  # Note: When set, these override the global merge settings for this camera
#RJ|}```
#KJ|
#JQ|**Request:**
#BV|```bash
#JS|curl -u username:password \
#KX|  -X PUT \
#VQ|  -H "Content-Type: application/json" \
#BW|  -d '{
#RV|    "enabled": false,
#NH|    "batch_limit": 50
#SS|  }' \
#JR|  "http://localhost:9090/api/cameras/cam1/merge-config"
#QV|```
#HR|
#QV|**Response:**
#YP|```json
#VM|{
#QJ|  "status": "updated"
#SY|}
#YX|```
#SK|
#VY|### Reset Per-Camera Merge Configuration
#XH|
#WY|**Endpoint:** `DELETE /api/cameras/:id/merge-config`
#MK|
#VP|Remove per-camera merge overrides, camera will inherit global merge settings.
#RV|
#JQ|**Request:**
#BV|```bash
#JS|curl -u username:password \
#YY|  -X DELETE \
#HT|  "http://localhost:9090/api/cameras/cam1/merge-config"
#PV|```
#RN|
#QV|**Response:**
#YP|```json
#ST|{
#KR|  "status": "reset"
#YS|}
#BB|```
#YV|
#VY|### Download Recording
#TS|

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
#QB|
[
#KT|
  {
#PW|
    "id": "cam1",
#WM|
    "name": "Front Door Camera",
#WY|
    "protocol": "rtsp_h264",
#HV|
    "url": "rtsp://192.168.1.100:554/stream",
#QP|
    "enabled": true,
#VJ|
    "status": "recording",
#QV|
    "last_seen": "2024-01-01T10:15:00Z",
#VV|
    "retention_days": 30,
#KZ|    "username": "admin",
#XJ|    "has_password": true
#WM|
  },
#YY|
  {
#KV|
    "id": "cam2",
#RP|
    "name": "Backyard Camera",
#KR|
    "protocol": "http_jpeg",
#RV|
    "url": "http://192.168.1.101:8080/cam.jpg",
#HN|
    "enabled": false,
#RT|
    "status": "stopped",
#MM|
    "last_seen": "2024-01-01T09:30:00Z",
#TY|
    "retention_days": 7,
#YK|    "username": "",
#XW|    "has_password": false
#QR|
  }
#WK|
]```
#WX|
#NS|### Create Camera
#NT|
#ZK|**Endpoint:** `POST /api/cameras`
#JH|

### Create Camera

**Endpoint:** `POST /api/cameras`

Add a new camera configuration.

**Request Body:**
```json
{
  "name": "Garage Camera",
  "protocol": "rtsp_mjpeg",
  # Supported protocols: rtsp_h264, rtsp_h265, rtsp_mjpeg, http_jpeg
  "url": "rtsp://192.168.1.102:554/mjpeg_stream",
  "username": "admin",
  "password": "secret",
  "enabled": true
```

**Request Body:**
```json
{
  "name": "Garage Camera",
  "protocol": "rtsp_mjpeg",
  "url": "rtsp://192.168.1.102:554/mjpeg_stream",
  "username": "admin",
  "password": "secret",
  "enabled": true,
  "retention_days": 15
  
  # Note: retention_days is optional, defaults to global setting
}```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Garage Camera",
    "protocol": "rtsp_mjpeg",
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
#KN|
  "id": "cam1",
#WV|
  "name": "Front Door Camera",
#ZR|
  "protocol": "rtsp_h264",
#RB|
  "url": "rtsp://192.168.1.100:554/stream",
#BK|
  "enabled": true,
#XS|
  "status": "recording",
#HH|
  "last_seen": "2024-01-01T10:15:00Z",
#RP|
  "retention_days": 30,
#VX|  "username": "admin",
#WP|  "has_password": true
#VP|
}```

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
}```

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

## Error Responses

All error responses use the following format:

```json
{
  "error": "descriptive error message"
}
```

Common error scenarios:

- Authentication failures (401)
- Invalid request parameters (400)
- Missing required fields (400)
- File upload too large (400)
- Resource not found (404)
- Internal server errors (500)

## HTTP Status Codes

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