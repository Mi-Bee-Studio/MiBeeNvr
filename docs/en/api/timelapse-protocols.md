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
