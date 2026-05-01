# MiBee NVR API Reference

## Table of Contents

- [Authentication](#authentication)
- [Recordings API](#recordings-api)
- [Cameras API](#cameras-api)
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
| `format` | string | No | Filter by format (h264 or mjpeg) | `h264` |
| `pinned` | boolean | No | Filter by pin status | `true` |
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
      "pinned": false
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
  "pinned": false
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

### Pin Recording

**Endpoint:** `POST /api/recordings/:id/pin`

Pin a recording to prevent automatic cleanup.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/recordings/1704123456789012345/pin"
```

**Response:**
```json
{
  "status": "pinned"
}
```

### Unpin Recording

**Endpoint:** `POST /api/recordings/:id/unpin`

Unpin a recording to allow automatic cleanup.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/recordings/1704123456789012345/unpin"
```

**Response:**
```json
{
  "status": "unpinned"
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
    "url": "rtsp://192.168.1.100:554/stream",
    "enabled": true
  },
  {
    "id": "cam2",
    "name": "Backyard Camera",
    "protocol": "http_jpeg",
    "url": "http://192.168.1.101:8080/cam.jpg",
    "enabled": false
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
  "url": "rtsp://192.168.1.102:554/mjpeg_stream",
  "username": "admin",
  "password": "secret",
  "enabled": true
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
  "id": "cam1",
  "name": "Front Door Camera",
  "protocol": "rtsp_h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true
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
  "enabled": false
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

## Stats & Settings API

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