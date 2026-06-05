# AI Detection API

## AI Detection Status

**Endpoint:** `GET /api/ai/status`

Get the AI engine availability, status, and model information.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/ai/status"
```

**Response:**
```json
{
  "available": true,
  "engine_status": "running",
  "model": "/var/lib/mibee-nvr/models/ai/model.onnx",
  "probe": {}
}
```

## Enable AI Detection

**Endpoint:** `POST /api/ai/enable`

Enable AI detection for a specific camera. The camera must be running with an active StreamHub.

**Request Body:**
```json
{
  "camera_id": "front-door"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door"
  }' \
  "http://localhost:9090/api/ai/enable"
```

**Response:**
```json
{
  "status": "enabled",
  "camera_id": "front-door"
}
```

## Disable AI Detection

**Endpoint:** `POST /api/ai/disable`

Disable AI detection for a specific camera. Idempotent — returns success even if AI was not enabled.

**Request Body:**
```json
{
  "camera_id": "front-door"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door"
  }' \
  "http://localhost:9090/api/ai/disable"
```

**Response:**
```json
{
  "status": "disabled",
  "camera_id": "front-door"
}
```

## AI Detection Events (SSE)

**Endpoint:** `GET /api/ai/events`

Server-Sent Events endpoint that streams AI detection results to the frontend in real time.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/ai/events"
```

**Response:** SSE stream (`text/event-stream`). 15-second heartbeat keepalive. Events are JSON-encoded detection results.

```
data: {"camera_id":"front-door","timestamp":"2024-01-01T12:34:56Z","detections":[{"label":"person","confidence":0.95,"bbox":{"x":100,"y":200,"width":50,"height":100}}]}

: ping
```
