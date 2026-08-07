# Health Monitoring API

## Camera Health Status Overview

**Endpoint:** `GET /api/health/cameras`

Get aggregated camera health status overview. Public endpoint (rate-limited).

**Request:**
```bash
curl http://localhost:9090/api/health/cameras
```

**Response:**
```json
{
  "total": 4,
  "recording": 3,
  "reconnecting": 0,
  "error": 0,
  "offline": 1,
  "details": [
    {
      "id": "front-door",
      "name": "Front Door",
      "status": "recording",
      "score": 95
    },
    {
      "id": "backyard",
      "name": "Backyard",
      "status": "offline",
      "score": 0
    }
  ]
}
```

## Detailed Health Monitoring Status

**Endpoint:** `GET /api/health/status`

Get detailed health monitoring status for all cameras.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/health/status"
```

**Response:**
```json
{
  "front-door": {
    "status": "recording",
    "last_seen": "2024-01-01T12:34:56Z",
    "error_count": 0,
    "uptime": "2h34m15s"
  }
}
```

## Recent Health Events

**Endpoint:** `GET /api/health/events`

Get paginated health events from the database.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `camera_id` | string | No | Filter by camera ID | `front-door` |
| `event_type` | string | No | Filter by event type | `disconnected` |
| `since` | string | No | Return events after this timestamp | `2024-01-01T00:00:00Z` |
| `limit` | integer | No | Maximum results (default: 50) | `20` |
| `offset` | integer | No | Result offset for pagination | `0` |

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/health/events?camera_id=front-door&limit=10"
```

**Response:**
```json
{
  "events": [
    {
      "id": 1,
      "camera_id": "front-door",
      "event_type": "disconnected",
      "message": "Camera disconnected",
      "timestamp": "2024-01-01T12:34:56Z"
    }
  ],
  "total": 1
}
```

## Camera Stability Scores

**Endpoint:** `GET /api/health/stability`

Get stability quality scores for all cameras.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/health/stability"
```

**Response:**
```json
{
  "cameras": {
    "front-door": {
      "score": 95,
      "uptime_percentage": 99.5,
      "total_events": 2
    },
    "backyard": {
      "score": 70,
      "uptime_percentage": 85.0,
      "total_events": 15
    }
  }
}
```

## Per-Camera Stability

**Endpoint:** `GET /api/health/stability/{id}`

Get stability quality data for a single camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/health/stability/front-door"
```

**Response:**
```json
{
  "score": 95,
  "uptime_percentage": 99.5,
  "total_events": 2
}
```

## Camera-Specific Health Info

**Endpoint:** `GET /api/cameras/{id}/health`

Get health status for a specific camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/health"
```

**Response:**
```json
{
  "status": "recording",
  "last_seen": "2024-01-01T12:34:56Z",
  "error_count": 0,
  "uptime": "2h34m15s"
}
```
