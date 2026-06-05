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

## Download Merged Recording

**Endpoint:** `GET /api/recordings/{id}/merged`

Download the merged MP4 file for a timelapse recording. Serves the file via `http.ServeFile()` with range support.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/recordings/1704123456789012345/merged"
```

**Response:** Binary MP4 file content. Returns 404 if no merged recording is available.

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
