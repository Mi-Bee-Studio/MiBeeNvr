# Recordings API

## List Recordings

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

## Get Recording

**Endpoint:** `GET /api/recordings/{id}`

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

## Delete Recording

**Endpoint:** `DELETE /api/recordings/{id}`

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

## Download Recording

**Endpoint:** `GET /api/recordings/{id}/download`

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

## List Recording Frames (MJPEG only)

**Endpoint:** `GET /api/recordings/{id}/frames`

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

## Batch Delete Recordings

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

## Create Recording

**Endpoint:** `POST /api/recordings`

Register a new recording in the database. Used by MiBeeVision to create recording entries for externally-processed footage.

**Auth:** API Key (Bearer token with `mbv_` prefix) or BasicAuth

**Request Body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | No | Recording ID (auto-generated if omitted) |
| `camera_id` | string | Yes | Camera identifier |
| `file_path` | string | Yes | Path to the recording file |
| `format` | string | Yes | Recording format (`h264`, `h265`, `mjpeg`, etc.) |
| `started_at` | string | No | Start time (RFC3339 format, defaults to now) |
| `ended_at` | string | No | End time (RFC3339 format) |
| `duration` | number | No | Duration in seconds |
| `file_size` | integer | No | File size in bytes |
| `frame_count` | integer | No | Number of frames |

**Request:**
```bash
curl -H "Authorization: Bearer mbv_your_api_key_here" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "file_path": "/data/recordings/h264/front-door_1704123456789012345.mp4",
    "format": "h264",
    "started_at": "2024-01-01T12:34:56.789Z",
    "ended_at": "2024-01-01T12:35:06.789Z",
    "duration": 10.0,
    "file_size": 1048576,
    "frame_count": 300
  }' \
  "http://localhost:9090/api/recordings"
```

**Response:** `201 Created`
```json
{
  "id": "1704123456789012345",
  "status": "created"
}
```

## Update Recording

**Endpoint:** `PATCH /api/recordings/{id}`

Update recording metadata fields. Used by MiBeeVision to update recording details after processing.

**Auth:** API Key (Bearer token with `mbv_` prefix) or BasicAuth

**Request Body:** All fields are optional — only provided fields will be updated.

| Field | Type | Description |
|-------|------|-------------|
| `file_path` | string | Updated file path |
| `format` | string | Updated format |
| `ended_at` | string | Updated end time (RFC3339 format) |
| `duration` | number | Updated duration in seconds |
| `file_size` | integer | Updated file size in bytes |
| `frame_count` | integer | Updated frame count |

**Request:**
```bash
curl -H "Authorization: Bearer mbv_your_api_key_here" \
  -X PATCH \
  -H "Content-Type: application/json" \
  -d '{
    "duration": 15.5,
    "file_size": 2097152
  }' \
  "http://localhost:9090/api/recordings/1704123456789012345"
```

**Response:** `200 OK`
```json
{
  "id": "1704123456789012345",
  "status": "updated"
}
```

## Update Recording AI Status

**Endpoint:** `PATCH /api/recordings/{id}/ai-status`

Update the AI processing status of a recording. Used by MiBeeVision to report processing progress and prevent duplicate processing.

**Auth:** API Key (Bearer token with `mbv_` prefix)

**Request Body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `status` | string | Yes | AI status: `pending`, `processing`, `done`, `failed`, or `skipped` |
| `error` | string | No | Error message if status is `failed` |

**Request:**
```bash
curl -H "Authorization: Bearer mbv_your_api_key_here" \
  -X PATCH \
  -H "Content-Type: application/json" \
  -d '{
    "status": "done"
  }' \
  "http://localhost:9090/api/recordings/1704123456789012345/ai-status"
```

**Response:** `200 OK`
```json
{
  "recording_id": "1704123456789012345",
  "ai_status": "done"
}
```

## Download Merged Recording

**Endpoint:** `GET /api/recordings/{id}/merged`

Download the merged MP4 file for a timelapse recording. Serves the file via `http.ServeFile()` with range support. Before serving, the handler verifies the merged file exists on disk and is non-empty; a missing/empty file returns 404 even when the DB row reports `merge_status=merged` (this lets the frontend fall back to the JPEG frame viewer instead of a dead-end error). Stale DB entries are also proactively reset at startup by a merge-integrity scan.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/merged"
```

**Response:** Binary MP4 file content. Returns 404 if the recording has no merge result, or if the merged file is missing/empty on disk.

## List Timelapse Frames

**Endpoint:** `GET /api/recordings/{id}/timelapse-frames`

List JPEG frames for a timelapse recording.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/timelapse-frames"
```

**Response:**
```json
[
  {
    "filename": "frame_0001.jpg",
    "url": "/api/recordings/1704123456789012345/timelapse-frames/frame_0001.jpg",
    "size": 54321,
    "timestamp": "2024-01-01T12:34:56Z"
  },
  {
    "filename": "frame_0002.jpg",
    "url": "/api/recordings/1704123456789012345/timelapse-frames/frame_0002.jpg",
    "size": 52345,
    "timestamp": "2024-01-01T12:35:06Z"
  }
]
```

## Get Timelapse Frame

**Endpoint:** `GET /api/recordings/{id}/timelapse-frames/{filename}`

Get a specific JPEG frame from a timelapse recording.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/timelapse-frames/frame_0001.jpg" \
  -o frame_0001.jpg
```

**Response:** JPEG image binary. Returns 400 for invalid filenames (path traversal protection).
