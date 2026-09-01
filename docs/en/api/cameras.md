# Cameras API

## Camera Management

### List Cameras

**Endpoint:** `GET /api/cameras`

Get a list of all configured cameras.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras"
```

**Response:**
```json
[
  {
    "id": "front-door",
    "name": "Front Door",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "enabled": true,
    "status": "recording",
    "last_seen": "2024-01-01T10:15:00Z",
    "retention_days": 30,
    "username": "admin",
    "has_password": true,
    "sub_stream_url": "",
    "snapshot_url": "",
    "sample_interval": 1,
    "hls_max_fps": 30,
    "did": "",
    "vendor": ""
  }
]
```

### Create Camera

**Endpoint:** `POST /api/cameras`

Add a new camera configuration.

**Request Body:**
```json
{
  "name": "Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "username": "admin",
  "password": "secret",
  "enabled": true,
  "retention_days": 30,
  "recording_mode": "adaptive",
  "adaptive": {
    "calm_threshold": "60s",
    "timelapse_interval": "30s",
    "spike_factor": 5.0,
    "ambient_audio": false
  },
  "audio_trigger": {"enabled": true, "min_dbfs": -45, "pre_capture_s": 3},
  "sub_stream_url": "rtsp://192.168.1.100:554/sub_stream",
  "sub_profile_token": "",
  "cascade_enabled": true,
  "cascade_sub_stream": false,
  "snapshot_url": "http://192.168.1.100:8080/snapshot",
  "sample_interval": 1,
  "hls_max_fps": 30,
  "push_targets": [
    {
      "id": "backup-nvr",
      "name": "Backup NVR",
      "protocol": "rtmp",
      "url": "rtmp://backup.example.com:1935/live/front-door",
      "enabled": true
    }
  ]
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Front Door",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "username": "admin",
    "password": "secret",
    "enabled": true
  }' \
  "http://localhost:9090/api/cameras"
```

**Response (201 Created):**
```json
{
  "id": "front-door",
  "name": "Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true
}
```

> `recording_mode` / `adaptive` / `audio_trigger` — see [Adaptive Recording](../adaptive-recording.md); `sub_stream_url` / `sub_profile_token` — see [Sub-streams](../sub-stream.md); `cascade_*` — see the [GB/T 28181 guide](../gb28181-guide.md).

### Get Camera

**Endpoint:** `GET /api/cameras/{id}`

Get a specific camera configuration.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door"
```

**Response:**
```json
{
  "id": "front-door",
  "name": "Front Door", 
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true,
  "status": "recording",
  "last_seen": "2024-01-01T10:15:00Z"
}
```

### Update Camera

**Endpoint:** `PUT /api/cameras/{id}`

Update camera configuration. All fields are optional for partial updates.

**Request Body:**
```json
{
  "name": "Updated Front Door",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false,
  "retention_days": 7
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Updated Front Door",
    "url": "rtsp://192.168.1.100:554/new_stream",
    "enabled": false
  }' \
  "http://localhost:9090/api/cameras/front-door"
```

**Response:**
```json
{
  "id": "front-door",
  "name": "Updated Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false
}
```

### Delete Camera

**Endpoint:** `DELETE /api/cameras/{id}`

Delete a camera configuration.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/backyard"
```

**Response:**
```json
{
  "status": "deleted"
}
```

### Test Connection

**Endpoint:** `POST /api/cameras/test-connection`

Test camera connection with provided configuration.

**Request Body:**
```json
{
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "username": "admin", 
  "password": "secret"
}
```

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "username": "admin",
    "password": "secret"
  }' \
  "http://localhost:9090/api/cameras/test-connection"
```

**Response:**
```json
{
  "success": true,
  "message": "Connection successful",
  "details": {
    "protocol": "rtsp",
    "encoding": "h264",
    "latency_ms": 45,
    "frames_received": 10
  }
}
```

### Start Camera

**Endpoint:** `POST /api/cameras/{id}/start`

Start recording for a camera.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/front-door/start"
```

**Response:**
```json
{
  "status": "started"
}
```

### Stop Camera

**Endpoint:** `POST /api/cameras/{id}/stop`

Stop recording for a camera.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/front-door/stop"
```

**Response:**
```json
{
  "status": "stopped"
}
```

### Adaptive-Recording External Trigger

**Endpoint:** `POST /api/cameras/{id}/adaptive/trigger`

Pull an **adaptive-recording** camera back to full rate immediately (the entry point for MQTT / scripts / AI backends). Non-adaptive cameras return an error.

**Request Body:**
```json
{
  "source": "automation",
  "hold": "30s",
  "dbfs": -30.2
}
```

| Field | Type | Description |
|-------|------|-------------|
| `source` | string | Free-form trigger-source label (logged, feeds health stats) |
| `hold` | string | How long to stay full-rate (0–10m; default hold when omitted) |
| `dbfs` | number | Optional loudness reference at trigger time (logged only) |

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"source": "mqtt", "hold": "30s"}' \
  "http://localhost:9090/api/cameras/front-door/adaptive/trigger"
```

**Response (200 OK):**
```json
{
  "status": "triggered"
}
```

> Non-adaptive cameras return 400 (`camera does not support adaptive triggers`).

### Per-Camera Storage Root

**Endpoint:** `PUT /api/cameras/{id}/storage-root` / `GET /api/cameras/{id}/storage-root`

Set / query this camera's recording storage root (hot — the next segment lands in the new location).

**Request Body (PUT):**
```json
{
  "root": "/mnt/bigdisk/recordings",
  "migrate": true,
  "delete_source": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| `root` | string | Target root (default root or a candidate volume; empty string = clear the override) |
| `migrate` | bool | Also enqueue this camera's history into the background migration queue (default true) |
| `delete_source` | bool | Delete source files after verified migration (default false) |

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{"root": "/mnt/bigdisk/recordings", "migrate": true, "delete_source": true}' \
  "http://localhost:9090/api/cameras/backyard/storage-root"
```

**Response (200 OK):**
```json
{
  "status": "updated",
  "camera_id": "backyard",
  "storage_root": "/mnt/bigdisk/recordings",
  "migration": {"camera_id": "backyard", "state": "queued", "...": "..."}
}
```

**GET response:**
```json
{
  "camera_id": "backyard",
  "override_root": "/mnt/bigdisk/recordings",
  "effective_root": "/mnt/bigdisk/recordings",
  "default_root": "/var/lib/mibee-nvr",
  "migration": null
}
```

> Insufficient target space (20% safety-margin check) rejects the switch with 400; candidate management and batch migration are covered in [Storage Management](../storage-management.md).

## Camera Snapshot

**Endpoint:** `GET /api/cameras/{id}/snapshot`

Get a JPEG snapshot image from a camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/snapshot" \
  -o snapshot.jpg
```

**Response:** JPEG image with `Content-Type: image/jpeg` and `Cache-Control: max-age=5`

## Camera Push-Out Status

**Endpoint:** `GET /api/cameras/{id}/push-status`

Get the live runtime status of a camera's push-out relay targets (RTMP/RTSP). Returns per-target connection state, outbound bitrate, uptime, and errors.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/push-status"
```

**Response:**
```json
{
  "camera_id": "front-door",
  "targets": [
    {
      "id": "backup-nvr",
      "name": "Backup NVR",
      "protocol": "rtmp",
      "url": "rtmp://backup.example.com:1935/live/front-door",
      "status": "streaming",
      "kbps": 270.8,
      "enabled": true,
      "uptime": "1m16s",
      "error": "",
      "updated_at": "2026-06-17T06:15:37+08:00"
    }
  ]
}
```

**Target `status` values:** `idle` (disabled), `connecting`, `streaming`, `reconnecting`, `error`.
