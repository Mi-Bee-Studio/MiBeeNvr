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
  "sub_stream_url": "rtsp://192.168.1.100:554/sub_stream",
  "snapshot_url": "http://192.168.1.100:8080/snapshot",
  "sample_interval": 1,
  "hls_max_fps": 30
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
