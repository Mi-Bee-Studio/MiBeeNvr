# Transcoding API

## Transcoding Self-Check

**Endpoint:** `GET /api/transcoding/check`

Performs a self-check of system transcoding capabilities including hardware validation, FFmpeg availability, and performance estimation.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/check"
```

**Response:**
```json
{
  "supported": true,
  "ffmpeg_status": "available",
  "encoders": {
    "h264": "libx264",
    "h265": "libx265"
  },
  "decoders": {
    "h264": "h264",
    "h265": "hevc"
  },
  "warnings": [],
  "max_concurrent": 2,
  "estimated_fps": 3.5,
  "total_cores": 4,
  "total_memory_mb": 1024,
  "h264_encoder_type": "software",
  "h265_encoder_type": "software",
  "h264_decoder_type": "software",
  "h265_decoder_type": "software",
  "max_encode_width": 1920,
  "max_encode_height": 1080,
  "devices": [
    {
      "type": "cpu",
      "name": "ARMv7 Processor rev 5 (v7l)"
    }
  ]
}
```

**Response Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `supported` | boolean | Whether transcoding is supported |
| `ffmpeg_status` | string | FFmpeg status: "available", "downloading", "not_installed", "failed" |
| `encoders` | object | Available encoder libraries (h264, h265) |
| `decoders` | object | Available decoder libraries (h264, h265) |
| `warnings` | array[] | Human-readable warnings about limitations |
| `max_concurrent` | integer | Estimated maximum concurrent transcoding streams |
| `estimated_fps` | float | Estimated transcoding FPS on this hardware |
| `total_cores` | integer | Total CPU cores available |
| `total_memory_mb` | integer | Total system memory in MB |
| `h264_encoder_type` | string | "software", "hardware", or "unknown" |
| `h265_encoder_type` | string | "software", "hardware", or "unknown" |
| `devices` | array[] | Hardware device information |

## FFmpeg Status

**Endpoint:** `GET /api/transcoding/ffmpeg/status`

Returns the current FFmpeg download/availability status. Does not expose the binary path for security.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/ffmpeg/status"
```

**Response (installed):**
```json
{
  "status": "available",
  "version": "6.0",
  "download_progress": 100
}
```

**Response (not installed):**
```json
{
  "status": "not_installed",
  "version": "",
  "download_progress": 0
}
```

## Download FFmpeg

**Endpoint:** `POST /api/transcoding/ffmpeg/download`

Start a background FFmpeg binary download. Idempotent: returns current status if already downloading or available.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/transcoding/ffmpeg/download"
```

**Response (accepted - download started):**
```json
{
  "status": "downloading",
  "download_progress": 0
}
```

**Response (already available):**
```json
{
  "status": "available",
  "version": "6.0"
}
```

**Response (already downloading):**
```json
{
  "status": "downloading",
  "download_progress": 45
}
```

## Retry FFmpeg Download

**Endpoint:** `POST /api/transcoding/ffmpeg/download/retry`

Retry a failed FFmpeg download. Only works when status is "failed". Returns 409 Conflict if a download is already in progress.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/transcoding/ffmpeg/download/retry"
```

**Response (retry started):**
```json
{
  "status": "downloading",
  "download_progress": 0
}
```

**Response (conflict - already in progress):**
```json
{
  "error": "download already in progress",
  "status": "downloading"
}
```

## Transcoding Manager Status

**Endpoint:** `GET /api/transcoding/status`

Returns the overall transcoding subsystem status including enabled state, hardware info, queue length, and active jobs.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/status"
```

**Response:**
```json
{
  "enabled": true,
  "hardware": {
    "h264_encoder": "libx264",
    "h265_encoder": "libx265"
  },
  "queue_length": 5,
  "active_jobs": 2,
  "recent_results": []
}
```

## List Transcoding Tasks

**Endpoint:** `GET /api/transcoding/tasks`

Returns paginated transcode tasks with optional filters.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `status` | string | No | Filter by task status | `pending` |
| `camera_id` | string | No | Filter by camera ID | `front-door` |
| `limit` | integer | No | Maximum results (default: 50) | `20` |
| `offset` | integer | No | Result offset for pagination | `0` |
| `page` | integer | No | Page number (1-indexed, alternative to offset) | `1` |

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/tasks?status=pending&limit=10"
```

**Response:**
```json
{
  "tasks": [
    {
      "id": 1,
      "camera_id": "front-door",
      "recording_id": "1704123456789012345",
      "status": "pending",
      "input_format": "h265",
      "output_format": "h264",
      "created_at": "2024-01-01T12:34:56.789Z"
    }
  ],
  "total": 1,
  "limit": 10,
  "offset": 0,
  "page": 1
}
```

## Create Transcoding Task

**Endpoint:** `POST /api/transcoding/tasks`

Manually enqueue a transcode task. Validates the recording exists and the camera has transcoding enabled.

**Request Body:**
```json
{
  "camera_id": "front-door",
  "recording_id": "1704123456789012345",
  "target_codec": "h264"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "recording_id": "1704123456789012345",
    "target_codec": "h264"
  }' \
  "http://localhost:9090/api/transcoding/tasks"
```

**Response (201 Created):**
```json
{
  "id": 1,
  "camera_id": "front-door",
  "recording_id": "1704123456789012345",
  "input_path": "/data/recordings/h265/front-door_1704123456789012345.mp4",
  "input_format": "h265",
  "output_path": "/data/recordings/h265/front-door_1704123456789012345.mp4.transcoded.mp4",
  "output_format": "h264",
  "created_at": "2024-01-01T12:34:56.789999999"
}
```

## Delete Transcoding Task

**Endpoint:** `DELETE /api/transcoding/tasks/{id}`

Cancel a pending or running task. Returns 409 for completed, failed, or already cancelled tasks.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/transcoding/tasks/1"
```

**Response:**
```json
{
  "id": 1,
  "status": "cancelled"
}
```

## Retry Transcoding Task

**Endpoint:** `POST /api/transcoding/tasks/{id}/retry`

Create a new pending transcoding task from a failed task.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/transcoding/tasks/1/retry"
```

**Response (201 Created):**
```json
{
  "id": 2,
  "camera_id": "front-door",
  "recording_id": "1704123456789012345",
  "input_path": "/data/recordings/h265/front-door_1704123456789012345.mp4",
  "input_format": "h265",
  "output_format": "h264",
  "created_at": "2024-01-01T12:35:00.000000000"
}
```

## Create Backfill Tasks

**Endpoint:** `POST /api/transcoding/backfill`

Enqueue all untranscoded recordings for a camera into the transcode queue.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | Yes | Camera ID to backfill | `front-door` |

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/transcoding/backfill?camera_id=front-door"
```

**Response:**
```json
{
  "enqueued": 45,
  "skipped": 5,
  "total": 50
}
```

## Camera Transcoding Configs

**Endpoint:** `GET /api/transcoding/cameras`

Returns the resolved transcoding configuration for each camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/cameras"
```

**Response:**
```json
{
  "global_enabled": true,
  "cameras": [
    {
      "camera_id": "front-door",
      "camera_name": "Front Door",
      "enabled": true,
      "target_codec": "h264",
      "preset": "fast",
      "bitrate": 1000000
    },
    {
      "camera_id": "backyard",
      "camera_name": "Backyard",
      "enabled": false,
      "target_codec": "h264",
      "preset": "",
      "bitrate": 0
    }
  ]
}
```

## Recordings Without Transcode

**Endpoint:** `GET /api/transcoding/recordings-without-transcode`

Returns the count of recordings that have not been transcoded for a camera.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | Yes | Camera ID to check | `front-door` |

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/transcoding/recordings-without-transcode?camera_id=front-door"
```

**Response:**
```json
{
  "count": 45
}
```
