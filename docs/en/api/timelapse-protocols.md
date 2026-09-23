# Timelapse & Protocols API

## Timelapse API

### Timelapse Manager Status

**Endpoint:** `GET /api/timelapse/status`

Get the global timelapse manager status and default merge settings.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/timelapse/status"
```

**Response:**
```json
{
  "merge_enabled": false,
  "merge_mode": "auto",
  "daily_merge": true,
  "merge_output_fps": 30
}
```

### Batch-Fetch Merge Output Frames

**Endpoint:** `GET /api/timelapse/merges/{id}/frames`

Slice a batch of JPEG frames out of an MJPEG (`mjpa`) periodic-merge output and return them as a `multipart/mixed` response (one part per frame) — an MJPEG player fetches N frames in one request instead of N requests. Frame positions come from the pure-Go MP4 sample table (`stsz`/`stco`) and are read on demand; the file is never loaded whole. Only for `codec=mjpeg` merges — H.264/H.265 outputs play natively via `<video>`.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `offset` | integer | No | First frame index (default 0; negative / non-numeric returns 400) | `120` |
| `limit` | integer | No | Frames in this batch (default 120, max 240 — clamped; 0 / negative / non-numeric returns 400) | `120` |

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/timelapse/merges/42/frames?offset=0&limit=120" \
  -o batch.txt
```

**Response:** `Content-Type: multipart/mixed; boundary=…`, each part an `image/jpeg` whose part headers carry `X-Frame-Index`. Response headers:

| Header | Description |
|--------|-------------|
| `X-Frame-Total` | Total frames in the merge output |
| `X-Frame-Offset` | Index this batch starts at |
| `X-Frame-Count` | Frames actually in this batch |
| `X-Frame-Fps` | Merge output frame rate (present when an fps is configured) |
| `Cache-Control` | `no-store` |

> A non-positive-integer `id` returns 400; a missing merge, one that is not `completed`, a non-MJPEG codec, or a missing output file all return 404. An out-of-range `offset` yields an empty but valid multipart body.

## Protocols API

### Get Supported Protocols

**Endpoint:** `GET /api/protocols`

Get the list of all supported camera protocols with their encodings and capabilities.

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
        "ptz": false,
        "snapshot": false,
        "discovery": false,
        "auth": true
      }
    },
    {
      "id": "http",
      "label": "HTTP JPEG",
      "encodings": ["jpeg"],
      "built_in": true,
      "capabilities": {
        "hls": false,
        "ptz": false,
        "snapshot": true,
        "discovery": false,
        "auth": true
      }
    },
    {
      "id": "onvif",
      "label": "ONVIF",
      "encodings": ["h264", "h265", "mjpeg"],
      "built_in": true,
      "capabilities": {
        "hls": true,
        "ptz": true,
        "snapshot": false,
        "discovery": true,
        "auth": true
      }
    },
    {
      "id": "xiaomi",
      "label": "Xiaomi",
      "encodings": ["h264", "h265"],
      "built_in": true,
      "capabilities": {
        "hls": true,
        "ptz": false,
        "snapshot": false,
        "discovery": true,
        "auth": true
      }
    }
  ]
}
```

## Features API

### Get Features

**Endpoint:** `GET /api/features`

Get enabled/disabled protocol feature flags.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/features"
```

**Response:**
```json
{
  "protocols": {
    "rtsp": true,
    "onvif": true,
    "xiaomi": false
  }
}
```

### Update Features

**Endpoint:** `PUT /api/features`

Update protocol feature flags.

**Request Body:**
```json
{
  "protocols": {
    "rtsp": true,
    "xiaomi": false
  }
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "protocols": {
      "rtsp": true,
      "xiaomi": false
    }
  }' \
  "http://localhost:9090/api/features"
```

**Response:**
```json
{
  "protocols": {
    "rtsp": true,
    "onvif": true,
    "xiaomi": false
  }
}
```
