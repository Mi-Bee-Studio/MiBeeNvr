# AI Detection API

## Architecture

> **AI inference runs entirely in the browser** via [ONNX Runtime Web](https://onnxruntime.ai/docs/tutorials/web/) (WebGPU preferred, WASM SIMD fallback). The Go backend performs **no AI inference**.

This is intentional: the NVR ships as a static `CGO_ENABLED=0` binary that cross-compiles to ARM64/ARMv7. Bundling a Go ONNX runtime (`libonnxruntime`) would drag in a C dependency, bloat the binary, and complicate ARM cross-compilation — FFmpeg is already the heaviest dependency. Detection is also per-viewer over an already-decoded live stream, so running it in the browser avoids re-decoding on the server and scales with viewers rather than cameras.

The backend's role is limited to:

- **(a) Persisting AI config + ROI zones** — the endpoints below (all under `/api/ai/*`).
- **(b) Serving the `.onnx` model file** to the browser — the public [`GET /models/{filename}`](#serve-model-file) route.

The browser-side inference pipeline lives in `web/src/lib/ai-detection/` (`runtime.ts` = ONNX Runtime Web session, `inference.ts` = YOLOv11-nano preprocessing / NMS / EMA smoothing). There is **no** `POST /api/ai/enable`, `POST /api/ai/disable`, or `GET /api/ai/events` SSE endpoint — detection never flows through the backend.

> All `/api/ai/*` endpoints require HTTP Basic Auth. Only [`GET /models/{filename}`](#serve-model-file) is public.

---

## Get AI Status

**Endpoint:** `GET /api/ai/status`

Returns the global AI configuration. This is config state only — there is no per-camera inference status, because inference runs in the browser.

**Request:**
```bash
curl -u admin:password \
  "http://localhost:9090/api/ai/status"
```

**Response:** `200 OK`
```json
{
  "enabled": true,
  "model_url": "/models/yolo11n.onnx",
  "confidence_threshold": 0.5,
  "frame_skip_rate": 10
}
```

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | bool | Whether AI is enabled (consumed by the browser UI) |
| `model_url` | string | Model path the browser loads (relative or whitelisted HTTPS) |
| `confidence_threshold` | float | Detection confidence threshold `[0, 1]` |
| `frame_skip_rate` | int | Run inference every N frames |

---

## Update AI Config

**Endpoint:** `PUT /api/ai/config`

Updates global AI config at runtime. All fields are optional — only provided fields are changed (partial update). Changes are persisted to the YAML config atomically.

**Request Body:**
```json
{
  "enabled": true,
  "confidence_threshold": 0.6,
  "frame_skip_rate": 5,
  "model_url": "/models/yolo11n.onnx"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `enabled` | bool | no | Enable/disable AI |
| `confidence_threshold` | float | no | Must be in `[0, 1]` |
| `frame_skip_rate` | int | no | Must be `> 0` |
| `model_url` | string | no | Model path (relative or whitelisted HTTPS) |

**Request:**
```bash
curl -u admin:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "confidence_threshold": 0.6,
    "frame_skip_rate": 5
  }' \
  "http://localhost:9090/api/ai/config"
```

**Response:** `200 OK`
```json
{ "status": "updated" }
```

**Errors:** `400 Bad Request` — `{"error":"invalid request body"}` if the JSON body cannot be decoded.

---

## List ROI Zones

**Endpoint:** `GET /api/ai/zones`

Lists all ROI (region of interest) zones across all cameras. Always returns an array (empty if none).

**Request:**
```bash
curl -u admin:password \
  "http://localhost:9090/api/ai/zones"
```

**Response:** `200 OK`
```json
{
  "zones": [
    {
      "camera_id": "front-door",
      "zone": {
        "name": "driveway",
        "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
      },
      "enabled": true
    }
  ]
}
```

---

## Create ROI Zone

**Endpoint:** `POST /api/ai/zones`

Creates a new ROI zone. Zone **names are globally unique** across all cameras.

**Request Body:**
```json
{
  "camera_id": "front-door",
  "zone": {
    "name": "driveway",
    "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
  },
  "enabled": true
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `camera_id` | string | yes | Target camera ID |
| `zone.name` | string | yes | Globally-unique zone name (used as the `{id}` in update/delete) |
| `zone.points` | `[[x,y],...]` | yes | Polygon vertices, ≥ 3 points, normalized `[0, 1]` |
| `enabled` | bool | no | Whether the zone filter is active |

**Request:**
```bash
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "zone": {
      "name": "driveway",
      "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
    },
    "enabled": true
  }' \
  "http://localhost:9090/api/ai/zones"
```

**Response:** `201 Created`
```json
{
  "camera_id": "front-door",
  "zone": {
    "name": "driveway",
    "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
  },
  "enabled": true
}
```

**Errors:**
- `400 Bad Request` — `{"error":"camera_id and zone.name are required"}`
- `409 Conflict` — `{"error":"zone with this name already exists"}`

---

## Update ROI Zone

**Endpoint:** `PUT /api/ai/zones/{id}`

Updates an existing zone. The `{id}` path parameter is the zone's **name** (zone names are globally unique). Supports renaming and/or replacing the polygon points.

**Path Parameter:**

| Param | Description |
|-------|-------------|
| `id` | The zone **name** to update |

**Request Body:**
```json
{
  "camera_id": "front-door",
  "zone": {
    "name": "front-lawn",
    "points": [[0.0, 0.0], [1.0, 0.0], [1.0, 1.0], [0.0, 1.0]]
  },
  "enabled": true
}
```

**Request:**
```bash
curl -u admin:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "zone": { "points": [[0.0, 0.0], [1.0, 0.0], [1.0, 1.0]] }
  }' \
  "http://localhost:9090/api/ai/zones/driveway"
```

**Response:** `200 OK`
```json
{ "status": "updated" }
```

**Errors:**
- `400 Bad Request` — missing zone id or invalid body
- `404 Not Found` — `{"error":"zone not found"}`
- `409 Conflict` — `{"error":"zone with new name already exists"}` (when renaming to an existing name)

---

## Delete ROI Zone

**Endpoint:** `DELETE /api/ai/zones/{id}`

Deletes a zone by name. The `{id}` is the zone **name**.

**Request:**
```bash
curl -u admin:password \
  -X DELETE \
  "http://localhost:9090/api/ai/zones/driveway"
```

**Response:** `200 OK`
```json
{ "status": "deleted" }
```

**Errors:**
- `400 Bad Request` — missing zone id
- `404 Not Found` — `{"error":"zone not found"}`

---

## Serve Model File

**Endpoint:** `GET /models/{filename}`

Serves an AI model file from the `{storage_root}/models/` directory using `http.ServeFile` (with HTTP range-request support for partial downloads).

> **Public — no authentication required.** This route is intentionally unauthenticated so the browser can fetch the ONNX model before/without depending on session-authenticated streaming. It is the **only** public AI-related route; all `/api/ai/*` endpoints still require Basic Auth.

**Path Parameter:**

| Param | Description |
|-------|-------------|
| `filename` | Model filename, e.g. `yolo11n.onnx` |

**Request:**
```bash
curl "http://localhost:9090/models/yolo11n.onnx" -o yolo11n.onnx
```

**Response:** `200 OK` with the file bytes (`Content-Type` auto-detected, `Accept-Ranges: bytes`).

**Errors:** `400 Bad Request` if `filename` is empty; standard `404` if the file does not exist.

### Putting a model on disk

Model files are placed into `{storage_root}/models/` by the operator — there is **no** HTTP upload/download endpoint for models. The bundled CLI subcommand downloads the default YOLOv11-nano model:

```bash
mibee-nvr download-model -config mibee-nvr.yaml
# or: make download-model RPi_HOST=user@host  (deploys + downloads on a remote host)
```

---

## Data Model

### ROI Zone

```json
{
  "camera_id": "front-door",
  "zone": {
    "name": "driveway",
    "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
  },
  "enabled": true
}
```

- **`points`** — polygon vertices as `[x, y]` pairs in **normalized `[0, 1]`** coordinates relative to the frame (resolution-independent). Minimum 3 points. The polygon is implicitly closed (last vertex connects back to the first).
- **`zone.name`** — globally unique across all cameras; also used as the `{id}` URL parameter for update/delete.

### Error Responses

All errors use the standard helper shape:

```json
{ "error": "human-readable message" }
```

| Status | Meaning |
|--------|---------|
| `200` / `201` | Success |
| `400` | Bad request (invalid body, missing required field) |
| `404` | Zone not found |
| `409` | Conflict (duplicate zone name) |
