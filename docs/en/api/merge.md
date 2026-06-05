# Merge & Timelapse Configuration API

## Merge API

### Get Merge Status

**Endpoint:** `GET /api/merge/status`

Get current merge manager status and statistics.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/status"
```

**Response:**
```json
{
  "enabled": true,
  "error_count": 0,
  "files_created": 9,
  "last_run_time": "2026-05-11T06:37:41Z",
  "segments_merged": 235
}
```

### Get Pending Merge Counts

**Endpoint:** `GET /api/merge/pending`

Get count of segments pending merge for each camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/merge/pending"
```

**Response:**
```json
{
  "pending": {
    "front-door": 99,
    "backyard": 145
  }
}
```

## Camera Merge Configuration

### Update Camera Merge Configuration

**Endpoint:** `PUT /api/cameras/{id}/merge-config`

Set merge configuration overrides for a specific camera.

**Request Body:**
```json
{
  "enabled": true,
  "check_interval": "30m",
  "window_size": "1h", 
  "batch_limit": 150,
  "min_segment_age": "5m",
  "min_segments_to_merge": 2
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": false,
    "batch_limit": 50
  }' \
  "http://localhost:9090/api/cameras/front-door/merge-config"
```

**Response:**
```json
{
  "status": "updated"
}
```

### Delete Camera Merge Configuration

**Endpoint:** `DELETE /api/cameras/{id}/merge-config`

Remove per-camera merge overrides.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/front-door/merge-config"
```

**Response:**
```json
{
  "status": "reset"
}
```

## Camera Timelapse Configuration

### Get Camera Timelapse Config

**Endpoint:** `GET /api/cameras/{id}/timelapse`

Get the timelapse configuration for a camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/timelapse"
```

**Response:**
```json
{
  "enabled": false,
  "interval": "30s",
  "output_fps": 30,
  "video_codec": "h264",
  "delete_original": false
}
```

### Update Camera Timelapse Config

**Endpoint:** `PUT /api/cameras/{id}/timelapse`

Update the timelapse configuration for a camera.

**Request Body:**
```json
{
  "enabled": true,
  "interval": "10s",
  "output_fps": 15,
  "video_codec": "h264",
  "delete_original": false,
  "merge_mode": "auto",
  "merge_output_fps": 30
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "interval": "10s",
    "output_fps": 15
  }' \
  "http://localhost:9090/api/cameras/front-door/timelapse"
```

**Response:** Returns the full camera configuration array after update.
