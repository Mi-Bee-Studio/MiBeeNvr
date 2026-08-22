# Camera Stats & Events

## Camera Recording Stats

**Endpoint:** `GET /api/cameras/{id}/stats`

Get recording statistics for a specific camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stats"
```

**Response:**
```json
{
  "recording_count": 150,
  "total_size": 1073741824
}
```

## Camera Event Stream (SSE)

**Endpoint:** `GET /api/cameras/{id}/events`

Server-Sent Events endpoint that streams camera-specific events from the event bus. Filters events by camera ID.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/events"
```

**Response:** SSE stream (`text/event-stream`). 15-second heartbeat keepalive.

```text
event: camera.status
data: {"topic":"camera.status","data":{"camera_id":"front-door","status":"recording"}}

: ping
```
