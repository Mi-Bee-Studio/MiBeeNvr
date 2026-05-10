# Configuration Reference

MiBee NVR uses a YAML configuration file to control all aspects of its operation. Below is a comprehensive reference of all available options, their defaults, and usage examples.

## Configuration File Structure

```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/mnt/data/nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password_hash: ""
cameras:
  - id: "cam1"
    name: "Camera Name"
    protocol: "rtsp_h264"
    url: "rtsp://..."
    enabled: true
    # sub_stream_url: "rtsp://..."   # Sub-stream for live preview
    # snapshot_url: "http://..."      # JPEG snapshot for thumbnails
    # sample_interval: 1              # MJPEG frame sampling
    # hls_max_fps: 0                  # HLS frame rate limit
cleanup:
  retention_days: 30
  check_interval: "1h"
  disk_threshold_percent: 95
merge:
  enabled: false
  check_interval: "1h"
  window_size: "1h"
  batch_limit: 200
  min_segment_age: "10m"
  min_segments_to_merge: 3
ftp:
  enabled: true
  port: 2121
  passive_port_range: "2122-2140"
mqtt:
  enabled: false
  broker: "tcp://localhost:1883"
  topic: "mibeenr/trigger"
  client_id: "mibee-nvr"
webdav:
  enabled: true
  path_prefix: "/dav"
```

## Server Configuration

### `server.listen`
- **Type**: string
- **Default**: `":9090"`
- **Description**: The address and port for the web server to listen on
- **Example**: `":8080"` or `"192.168.1.100:9090"`

## Storage Configuration

### `storage.root_dir`
- **Type**: string
- **Required**: Yes
- **Description**: Root directory for storing recordings and temporary files
- **Example**: `"/mnt/data/nvr"` or `"/var/lib/mibee-nvr"`

### `storage.segment_duration`
- **Type**: string
- **Default**: `"30s"`
- **Description**: Duration of video segments (memory intensive)
- **Important**: Each segment holds all video data in RAM until completion
- **Memory Usage**:
  - 30s segments: ~15-20MB per segment
  - 60s segments: ~30-40MB per segment
  - 120s segments: ~60-80MB per segment
- **Recommendation**: Use 30s for low-memory systems
- **Example**: `"30s"`, `"1m"`, `"5m"`

## Authentication Configuration

### `auth.username`
- **Type**: string
- **Required**: Yes (for web UI and FTP)
- **Description**: Username for authentication
- **Example**: `"admin"`

### `auth.password_hash`
- **Type**: string
- **Required**: Yes (for web UI and FTP)
- **Description**: bcrypt hashed password
- **Description**: bcrypt hashed password. Use `mibee-nvr hash-password <password>` CLI command to generate.
- **Note**: Alternatively, set `auth.password` with a plaintext password and the server will auto-generate the hash on startup.
- **Example**: `$2a$10$N9qo8uLOickgx2ZMRZoMy...`

## Camera Configuration

### Camera Structure
Each camera configuration requires these basic fields:

```yaml
cameras:
  - id: "cam1"
    name: "Camera Name"
    protocol: "rtsp_h264"
    url: "camera_url"
    enabled: true
```

### `cameras[].id`
- **Type**: string
- **Required**: Yes
- **Description**: Unique identifier for the camera (auto-generated if not provided)
- **Format**: 8-character alphanumeric (auto-generated using crypto/rand)
- **Example**: `"front-door"`, `"cam-01"`

### `cameras[].name`
- **Type**: string
- **Required**: Yes
- **Description**: Human-readable camera name
- **Example**: `"Front Door Camera"`, `"Back Yard"`

### `cameras[].protocol`
- **Type**: string
- **Required**: Yes
- **Description**: Camera protocol type
- **Options**: `"rtsp_h264"`, `"rtsp_h265"`, `"rtsp_mjpeg"`, `"http_jpeg"`
- **Note**: H.265/HEVC provides better compression than H.264 but requires more CPU processing

### `cameras[].url`
- **Type**: string
- **Required**: Yes
- **Description**: Camera URL or stream endpoint
- **Examples**:
  - RTSP: `"rtsp://192.168.1.100:554/stream"`
  - HTTP: `"http://192.168.1.101/capture"`

### `cameras[].username`
- **Type**: string
- **Optional**
- **Description**: Username for camera authentication
- **Example**: `"admin"`

### `cameras[].password`
- **Type**: string
- **Optional**
- **Description**: Password for camera authentication
- **Example**: `"camera-password"`

### `cameras[].enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Whether the camera recording is enabled
- **Example**: `true` or `false`

### `cameras[].sub_stream_url`
- **Type**: string
- **Optional**
- **Description**: RTSP URL of a lower-resolution sub-stream for live HLS preview. When configured, the Dashboard uses this stream instead of the main stream, reducing bandwidth usage.
- **Note**: Sub-stream must use the same codec (H.264/H.265) as the main stream
- **Example**: `"rtsp://192.168.1.100:554/stream2"`

### `cameras[].snapshot_url`
- **Type**: string
- **Optional**
- **Description**: HTTP URL returning a JPEG snapshot image. When configured, the Dashboard displays snapshot thumbnails instead of live HLS streams, significantly reducing bandwidth.
- **Behavior**: Snapshots are cached for 10 seconds; stale cache is served when the camera is temporarily unreachable
- **Example**: `"http://192.168.1.100/snapshot"`, `"http://192.168.1.100/cgi-bin/snapshot.cgi"`

### `cameras[].sample_interval`
- **Type**: integer
- **Default**: `1`
- **Description**: Frame sampling interval for MJPEG cameras. Only every Nth frame is saved to disk.
- **Use Case**: Reduce storage and bandwidth for low-priority MJPEG cameras
- **Example**: `1` (every frame), `3` (every 3rd frame), `5` (every 5th frame)

### `cameras[].hls_max_fps`
- **Type**: integer
- **Default**: `0` (unlimited)
- **Description**: Maximum frame rate for HLS live preview. Excess frames are dropped to reduce bandwidth.
- **Important**: Only affects live HLS preview — recording is NOT affected
- **Example**: `10`, `15`, `24`

## Protocol Examples

### RTSP H.264 Camera
```yaml
- id: "cam1"
  name: "Front Door"
  protocol: "rtsp_h264"
  url: "rtsp://192.168.1.100:554/live"
  username: "admin"
  password: "password123"
  enabled: true
```

### RTSP MJPEG Camera
```yaml
- id: "cam2"
  name: "Back Yard"
  protocol: "rtsp_mjpeg"
  url: "rtsp://192.168.1.101:554/stream"
  enabled: true
```

### HTTP JPEG Camera
```yaml
- id: "cam3"
  name: "Garage"
  protocol: "http_jpeg"
  url: "http://192.168.1.102/capture"
  enabled: true
```

### RTSP H.265 Camera

```yaml
- id: "cam4"
  name: "H.265 Security Camera"
  protocol: "rtsp_h265"
  url: "rtsp://192.168.1.103:554/stream"
  username: "admin"
  password: "camera-password"
  enabled: true
```

## Cleanup Configuration

### `cleanup.retention_days`
- **Type**: integer
- **Default**: `30` (when not set or `0`)
- **Description**: Number of days to keep recordings
- **Important**: A value of `0` is treated as "unconfigured" and defaults to 30 days
- **Per-camera retention**: Individual cameras can override this setting via the Web UI or API with their own `retention_days` field
- **Example**: `30`, `90`, `365`

### `cleanup.check_interval`
- **Type**: string
- **Default**: `"1h"`
- **Description**: How often to check for expired recordings
- **Format**: Go duration string
- **Examples**: `"30m"`, `"2h"`, `"24h"`

### `cleanup.disk_threshold_percent`
- **Type**: integer
- **Default**: `95`
- **Description**: Disk usage percentage threshold for cleanup
- **Behavior**: Cleanup runs when disk usage exceeds this threshold
- **Example**: `80`, `90`, `95`

## Merge Configuration

The merge feature automatically combines small video segments into larger files, reducing file count and improving storage efficiency. This is a background task that runs periodically, similar to cleanup.

### `merge.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable or disable the background merge task
- **Note**: When disabled, segments remain as individual files

### `merge.check_interval`
- **Type**: string
- **Default**: `"1h"`
- **Description**: How often the merge task runs
- **Format**: Go duration string
- **Examples**: `"30m"`, `"1h"`, `"2h"`

### `merge.window_size`
- **Type**: string
- **Default**: `"1h"`
- **Description**: Time window for grouping segments. Segments within the same window (same camera, same hour) are merged together.
- **Format**: Go duration string
- **Example**: `"1h"` (merge all segments within each hour)

### `merge.batch_limit`
- **Type**: integer
- **Default**: `200`
- **Description**: Maximum number of segments to process in a single merge run. Prevents excessive resource usage.
- **Example**: `100`, `200`, `500`

### `merge.min_segment_age`
- **Type**: string
- **Default**: `"10m"`
- **Description**: Minimum age of segments before they are considered for merging. Ensures recently created segments are not merged while still being written.
- **Format**: Go duration string
- **Example**: `"5m"`, `"10m"`, `"30m"`

### `merge.min_segments_to_merge`
- **Type**: integer
- **Default**: `3`
- **Description**: Minimum number of segments required in a group before merging. Groups with fewer segments are skipped.
- **Example**: `2`, `3`, `5`

### Merge Behavior
- **H.264/H.265**: Segments are concatenated without re-encoding (fast, zero quality loss). Only segments with identical codec parameters (SPS/PPS) are merged.
- **MJPEG**: JPEG files are moved into a single directory (no re-encoding).
- **Disk space**: Merging is skipped if available disk space is less than 110% of the estimated merged file size.
- **Atomic**: Merged files use atomic rename (temp file → final) to prevent corruption.
#HM|- **Originals**: Source segments are deleted from disk and database after successful merge.
#NX|
#VY|### Per-Camera Merge Configuration
#RK|
#NK|Individual cameras can override the global merge settings using the API or Web UI. This allows different cameras to have different merge strategies based on their recording patterns and storage requirements.
#JY|
#PX|**API Endpoints**:
#HK|- `GET /api/cameras/:id/merge-config` - Get per-camera merge overrides
#HK|- `PUT /api/cameras/:id/merge-config` - Set per-camera merge overrides
#HK|- `DELETE /api/cameras/:id/merge-config` - Reset to global defaults
#NX|
#VY|**Per-Camera Parameters**:
#WK|When configuring per-camera merge settings, all 6 global parameters can be overridden:
#NJ|
#RK|- `enabled` - Enable/disable merging for this specific camera
#VK|- `check_interval` - How often to check for mergeable segments
#BY|- `window_size` - Time window for grouping segments
#NP|- `batch_limit` - Maximum segments per merge run
#JR|- `min_segment_age` - Minimum age before segments can be merged
#PV|- `min_segments_to_merge` - Minimum segments required to trigger merge
#XN|
#VY|**Example Override**:
#YP|```yaml
cameras:
  - id: "cam1"
    name: "Front Door"
    protocol: "rtsp_h264"
    url: "rtsp://192.168.1.100:554/live"
    # Per-camera merge settings
    merge_config:
      enabled: true
      check_interval: "30m"
      batch_limit: 100  # Lower than global 200
      min_segments_to_merge: 2  # Lower than global 3
```
#NX|
#YJ|## FTP Configuration
#RM|

## FTP Configuration

### `ftp.enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Whether FTP server is enabled

### `ftp.port`
- **Type**: integer
- **Default**: `2121`
- **Description**: FTP server port
- **Note**: FTP cannot be reverse-proxied

### `ftp.passive_port_range`
- **Type**: string
- **Default**: `"2122-2140"`
- **Description**: Range of ports for passive FTP connections
- **Format**: `"start-end"`
- **Example**: `"30000-30100"`

## MQTT Configuration

### `mqtt.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Whether MQTT integration is enabled

### `mqtt.broker`
- **Type**: string
- **Required**: When enabled
- **Description**: MQTT broker URL
- **Example**: `"tcp://localhost:1883"` or `"mqtt://192.168.1.100:1883"`

### `mqtt.topic`
- **Type**: string
- **Required**: When enabled
- **Description**: Topic to subscribe to for trigger events
- **Example**: `"mibeenr/trigger"`

### `mqtt.client_id`
- **Type**: string
- **Required**: When enabled
- **Description**: MQTT client ID
- **Example**: `"mibee-nvr"`

## WebDAV Configuration

### `webdav.enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Whether WebDAV server is enabled

### `webdav.path_prefix`

- **Type**: string
- **Default**: `"/dav"`
- **Description**: URL path prefix for WebDAV access
- **Example**: `"/dav"`, `"/recordings"`

### `webdav.read_write`

- **Type**: boolean
- **Default**: `false`
- **Description**: Whether WebDAV server allows write operations
- **Important**: When enabled, new cameras can be auto-registered via WebDAV PUT requests
- **Security**: Consider security implications before enabling write access
- **Example**: `false`, `true`

## Important Notes

### Security Considerations
- FTP credentials use the same username/password as the web interface
- WebDAV supports optional read-only/read-write mode (read-only by default for security)
- Authentication is required for all web UI and FTP access

### Memory Management
- Segment duration directly affects memory usage
- Longer segments = more RAM usage
- Monitor system memory and adjust segment duration accordingly

### Disk Space
- Recordings are stored in MP4 segments
- Cleanup runs on schedule and when disk thresholds are reached
- `retention_days: 0` defaults to 30 days (not "keep forever")

### File Storage
- Segments are written to temporary files first
- Final segments use atomic file operations to prevent corruption
- Database stores recording metadata and timestamps in UTC