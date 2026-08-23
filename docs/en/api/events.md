# Events API

## Event Stream (SSE)

**Endpoint:** `GET /api/events`

Server-Sent Events endpoint that streams events from the global EventBus. Supports optional topic filtering.

**Query Parameters:**

| Parameter | Type | Required | Description | Example |
|-----------|------|----------|-------------|---------|
| `filter` | string | No | Filter events by topic prefix | `onvif.` |

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/events?filter=onvif."
```

**Response:** SSE stream (`text/event-stream`) with 15-second heartbeat keepalive.

```text
event: onvif.discovery
data: {"topic":"onvif.discovery","data":{"device":"192.168.1.104"}}

: ping
```

## Camera Event Stream (SSE)

**Endpoint:** `GET /api/cameras/{id}/events`

Server-Sent Events endpoint that streams camera-specific events from the event bus. Filters events by camera ID extracted from the event data.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/events"
```

**Response:** SSE stream (`text/event-stream`) with 15-second heartbeat keepalive.

```text
event: camera.status
data: {"topic":"camera.status","data":{"camera_id":"front-door","status":"recording"}}

: ping
```

## Telemetry

**Endpoint:** `POST /api/telemetry`

Submit playback telemetry events. Rate-limited to 10 requests/second per IP. Requires authentication.

**Request Body:**
```json
{
  "event": "playback_start",
  "camera_id": "front-door",
  "duration_ms": 15000,
  "details": {
    "protocol": "hls",
    "quality": "high"
  }
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "event": "playback_start",
    "camera_id": "front-door",
    "duration_ms": 15000
  }' \
  "http://localhost:9090/api/telemetry"
```

**Response:** `204 No Content` on success.
