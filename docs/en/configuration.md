# Configuration Reference

MiBee NVR uses a YAML configuration file to control all aspects of its operation. Below is a comprehensive reference of all available options, their defaults, and usage examples.

## Configuration File Structure

```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/var/lib/mibee-nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password_hash: ""
  password: ""
cameras:
  - id: "cam1"
    name: "Camera Name"
    protocol: "rtsp"
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
  read_write: false
hls:
  write_buffer_size: 40
  segment_max_size_mb: 10
observability:
  log_level: "info"
  log_format: "text"
  enable_pprof: false
version: "1.0"
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
- **Default**: `/var/lib/mibee-nvr`
- **Description**: Root directory for storing recordings and temporary files
- **Example**: `/var/lib/mibee-nvr`

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
- **Description**: bcrypt hashed password. Use `mibee-nvr hash-password <password>` to generate.
- **Priority**: `password_hash` takes precedence if both `password` and `password_hash` are set
- **Note**: If only `auth.password` (plaintext) is provided, the server auto-generates the hash on startup and persists it back to the config file
- **Example**: `$2a$10$N9qo8uLOickgx2ZMRZoMy...`

### `auth.password`
- **Type**: string
- **Optional**: Yes
- **Description**: Plaintext password for convenient initial setup. On first run, the server auto-hashes this value and writes it to `password_hash`, then clears the `password` field.
- **Priority**: Only used when `password_hash` is empty
- **Example**: `"admin123"`

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
- **Description**: Camera transport protocol
- **Options**: `"rtsp"`, `"http"`, `"onvif"` (new format) or `"rtsp_h264"`, `"rtsp_h265"`, `"rtsp_mjpeg"`, `"http_jpeg"` (legacy format)
- **Note**: Legacy format is automatically parsed to the new protocol+encoding format
- **Compatibility**: Both formats are supported for backward compatibility
### `cameras[].encoding`

- **Type**: string
- **Optional**: Yes (auto-detected from legacy protocol or defaults based on protocol)
- **Description**: Video encoding format
- **Options**: `"h264"`, `"h265"`, `"mjpeg"`, `"jpeg"`
- **Valid Combinations**:
  - `protocol: "rtsp"` → `encoding: "h264"`, `"h265"`, or `"mjpeg"`
  - `protocol: "http"` → `encoding: "jpeg"`
  - `protocol: "onvif"` → `encoding: "h264"` or `"h265"` (auto-detect if not specified)

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

**New Format (Protocol + Encoding)**:

**Legacy Format (Protocol only)**:


### RTSP H.264 Camera

**New Format**:
```yaml
- id: "cam1"
  name: "Front Door"
  protocol: "rtsp"
  encoding: "h264"
  url: "rtsp://192.168.1.100:554/live"
  username: "admin"
  password: "password123"
  enabled: true
```

**Legacy Format**:
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

**New Format**:
```yaml
- id: "cam2"
  name: "Back Yard"
  protocol: "rtsp"
  encoding: "mjpeg"
  url: "rtsp://192.168.1.101:554/stream"
  enabled: true
```

**Legacy Format**:
```yaml
- id: "cam2"
  name: "Back Yard"
  protocol: "rtsp_mjpeg"
  url: "rtsp://192.168.1.101:554/stream"
  enabled: true
```

### HTTP JPEG Camera

**New Format**:
```yaml
- id: "cam3"
  name: "Garage"
  protocol: "http"
  encoding: "jpeg"
  url: "http://192.168.1.102/capture"
  enabled: true
```

**Legacy Format**:
```yaml
- id: "cam3"
  name: "Garage"
  protocol: "http_jpeg"
  url: "http://192.168.1.102/capture"
  enabled: true
```

### RTSP H.265 Camera

**New Format**:
```yaml
- id: "cam4"
  name: "H.265 Security Camera"
  protocol: "rtsp"
  encoding: "h265"
  url: "rtsp://192.168.1.103:554/stream"
  username: "admin"
  password: "camera-password"
  enabled: true
```

### ONVIF Camera

**New Format (using url field)**:
```yaml
- id: "cam5"
  name: "ONVIF Security Camera"
  protocol: "onvif"
  encoding: "h264"
  url: "rtsp://192.168.1.104:554/stream"
  enabled: true
```

**Alternative Format (using onvif_endpoint field)**:
```yaml
- id: "cam5"
  name: "ONVIF Security Camera"
  protocol: "onvif"
  encoding: "h265"
  onvif_endpoint: "http://192.168.1.104/onvif"
  profile_token: "profile_1"
  enabled: true
```

**Legacy Format**:
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
- **Originals**: Source segments are deleted from disk and database after successful merge.

### Per-Camera Merge Configuration

Individual cameras can override the global merge settings using the API or Web UI. This allows different cameras to have different merge strategies based on their recording patterns and storage requirements.

**API Endpoints**:
- `GET /api/cameras/:id/merge-config` - Get per-camera merge overrides
- `PUT /api/cameras/:id/merge-config` - Set per-camera merge overrides
- `DELETE /api/cameras/:id/merge-config` - Reset to global defaults

**Per-Camera Parameters**:
When configuring per-camera merge settings, all 6 global parameters can be overridden:

- `enabled` - Enable/disable merging for this specific camera
- `check_interval` - How often to check for mergeable segments
- `window_size` - Time window for grouping segments
- `batch_limit` - Maximum segments per merge run
- `min_segment_age` - Minimum age before segments can be merged
- `min_segments_to_merge` - Minimum segments required to trigger merge

**Example Override**:
```yaml
cameras:
  - id: "cam1"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/live"
    # Per-camera merge settings
    merge_config:
      enabled: true
      check_interval: "30m"
      batch_limit: 100  # Lower than global 200
      min_segments_to_merge: 2  # Lower than global 3
```

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
## Observability Configuration

### `observability.log_level`
- **Type**: string
- **Default**: `"info"`
- **Description**: Log level
- **Options**: `"debug"`, `"info"`, `"warn"`, `"error"`
- **Example**: `"debug"`, `"info"`

### `observability.log_format`
- **Type**: string
- **Default**: `"text"`
- **Description**: Log output format
- **Options**: `"json"`, `"text"`
- **Example**: `"json"`, `"text"`

### `observability.enable_pprof`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable Go pprof performance profiling endpoints at `/debug/pprof`
- **Example**: `false`, `true`

### `version`
- **Type**: string
- **Default**: `"1.0"`
- **Description**: Configuration file schema version

## HLS Configuration

### `hls.write_buffer_size`
- **Type**: integer
- **Default**: `40`
- **Description**: Async frame buffer size per HLS stream. Controls how many frames are buffered before writing.
- **Example**: `40`, `80`, `120`

### `hls.segment_max_size_mb`
- **Type**: integer
- **Default**: `10`
- **Description**: Maximum HLS segment size in megabytes
- **Example**: `10`, `20`


## CLI Subcommands

MiBee NVR supports several subcommands in addition to the main server mode:

### `mibee-nvr init`
Interactive first-time setup wizard. Creates a config file with essential settings.

```bash
mibee-nvr init [flags]
```

**Flags**:
- `--password <pw>` — Set admin password (prompts interactively if not provided)
- `--username <name>` — Set admin username (default: `admin`)
- `--data-dir <path>` — Set storage directory (default: `/var/lib/mibee-nvr`)
- `--listen <addr>` — Set listen address (default: `:9090`)
- `--config <path>` — Config file path (default: `mibee-nvr.yaml`)
- `--force` — Overwrite existing config file

### `mibee-nvr health`
Health check for container/Docker orchestration. Exits 0 if the server is healthy.

```bash
mibee-nvr health [--addr :9090] [--config <path>]
```

### `mibee-nvr hash-password <password>`
Generate a bcrypt password hash for use in `auth.password_hash`.

```bash
mibee-nvr hash-password my-secret-password
# Output: $2a$10$N9qo8uLOickgx2ZMRZoMy...
```

### `mibee-nvr -version`
Print the binary version and exit.

```bash
mibee-nvr -version
# Output: MiBee NVR version 0.1.0-dev
```

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