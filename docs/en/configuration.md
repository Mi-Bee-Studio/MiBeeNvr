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
    encoding: "h264"
    url: "rtsp://..."
    enabled: true
    onvif_endpoint: ""           # ONVIF specific
    profile_token: ""            # ONVIF specific  
    stream_encoding: ""          # ONVIF auto-detect (H264/H265)
    sub_stream_url: "rtsp://..."  # Sub-stream for live preview
    snapshot_url: "http://..."    # JPEG snapshot for thumbnails
    sample_interval: 1            # MJPEG frame sampling
    hls_max_fps: 0               # HLS frame rate limit
    vendor: ""                   # Xiaomi transport vendor
    merge:                       # Per-camera merge overrides
      enabled: false
      check_interval: "1h"
      window_size: "1h"
      batch_limit: 150
      min_segment_age: "5m"
      min_segments_to_merge: 2
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
  username: ""
  password: ""
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: false
hls:
  write_buffer_size: 100         # Async frame buffer per stream
  segment_max_size_mb: 10        # HLS segment max size in MB
  segment_count: 7               # Segments per stream (range: 3-10)
  max_streams: 4                 # Max concurrent streams (range: 1-20, RPi constraint: 4)
xiaomi:
  user_id: ""                    # Xiaomi account user ID (from auth response)
  token: ""                      # Xiaomi passToken for API access
  region: "cn"                   # Region code: cn, sg, de, etc.
observability:
  log_level: "info"              # Log level: debug, info, warn, error
  log_format: "text"             # Log format: json or text
  enable_pprof: false            # Enable pprof debug endpoints
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
- **RPi Constraint**: Maximum 30 seconds on Raspberry Pi 3B
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
    protocol: "rtsp"
    encoding: "h264"
    url: "camera_url"
    enabled: true
```

### `cameras[].id`
- **Type**: string
- **Required**: Yes
- **Description**: Unique identifier for the camera (auto-generated if not provided)
- **Format**: Alphanumeric, recommended kebab-case (e.g., "front-door")
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
- **Options**: `"rtsp"`, `"http"`, `"onvif"`, `"xiaomi"`
- **Legacy Format**: Also supports `"rtsp_h264"`, `"rtsp_h265"`, `"rtsp_mjpeg"`, `"http_jpeg"` (automatically parsed to new format)
- **Note**: Legacy format is automatically parsed to the new protocol+encoding format for backward compatibility
- **Compatibility**: Both formats are supported

### `cameras[].encoding`
- **Type**: string
- **Optional**: Yes (auto-detected from legacy protocol or defaults based on protocol)
- **Description**: Video encoding format
- **Options**: `"h264"`, `"h265"`, `"mjpeg"`, `"jpeg"`
- **Valid Combinations**:
  - `protocol: "rtsp"` → `encoding: "h264"`, `"h265"`, or `"mjpeg"`
  - `protocol: "http"` → `encoding: "jpeg"`
  - `protocol: "onvif"` → `encoding: "h264"` or `"h265"` (auto-detect if not specified)
  - `protocol: "xiaomi"` → `encoding: "h264"` or `"h265"` (auto-detect)

### `cameras[].url`
- **Type**: string
- **Required**: Yes (except for ONVIF and Xiaomi cameras)
- **Description**: Camera URL or stream endpoint
- **Examples**:
  - RTSP: `"rtsp://192.168.1.100:554/stream"`
  - HTTP: `"http://192.168.1.101/capture"`
  - ONVIF: `"http://192.168.1.102:80/onvif/device_service"` (or use `onvif_endpoint`)
- **Validation**: Must have valid scheme (http/rtsp) and host

### `cameras[].username`
- **Type**: string
- **Optional**: Yes
- **Description**: Username for camera authentication
- **Example**: `"admin"`

### `cameras[].password`
- **Type**: string
- **Optional**: Yes
- **Description**: Password for camera authentication
- **Example**: `"camera-password"`

### `cameras[].enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Whether the camera recording is enabled
- **Example**: `true` or `false`

### `cameras[].onvif_endpoint`
- **Type**: string
- **Optional**: Yes (required for ONVIF cameras if no URL provided)
- **Description**: ONVIF device service endpoint URL
- **Example**: `"http://192.168.1.100:80/onvif/device_service"`
- **Note**: If URL is set for ONVIF camera, it's automatically copied to onvif_endpoint

### `cameras[].profile_token`
- **Type**: string
- **Optional**: Yes
- **Description**: ONVIF media profile token for specific stream selection
- **Example**: `"profile_1"`
- **Note**: Optional, uses default profile if not specified

### `cameras[].stream_encoding`
- **Type**: string
- **Optional**: Yes
- **Description**: Stream encoding for ONVIF cameras (H264 or H265)
- **Options**: `"H264"`, `"H265"`
- **Note**: Empty = auto-detect from ONVIF device capabilities

### `cameras[].sub_stream_url`
- **Type**: string
- **Optional**: Yes
- **Description**: RTSP URL of a lower-resolution sub-stream for live HLS preview. When configured, the Dashboard uses this stream instead of the main stream, reducing bandwidth usage.
- **Note**: Sub-stream must use the same codec (H.264/H.265) as the main stream
- **Example**: `"rtsp://192.168.1.100:554/stream2"`

### `cameras[].snapshot_url`
- **Type**: string
- **Optional**: Yes
- **Description**: HTTP URL returning a JPEG snapshot image. When configured, the Dashboard displays snapshot thumbnails instead of live HLS streams, significantly reducing bandwidth.
- **Behavior**: Snapshots are cached for 10 seconds; stale cache is served when the camera is temporarily unreachable
- **Example**: `"http://192.168.1.100/snapshot"`, `"http://192.168.1.100/cgi-bin/snapshot.cgi"`

### `cameras[].sample_interval`
- **Type**: integer
- **Optional**: Yes
- **Default**: 1 (for MJPEG cameras only)
- **Description**: Interval for sampling MJPEG frames (seconds). Higher values reduce CPU usage but decrease frame rate.
- **Example**: `1`, `2`, `5`

### `cameras[].hls_max_fps`
- **Type**: integer
- **Optional**: Yes
- **Default**: 0 (no limit)
- **Description**: Maximum frame rate for HLS streaming. 0 = no limit.
- **Example**: `30`, `15`, `25`

### `cameras[].vendor`
- **Type**: string
- **Optional**: Yes
- **Description**: Transport vendor for Xiaomi cameras
- **Options**: `"cs2"` (default)
- **Example**: `"cs2"`

### `cameras[].audio_enabled`

#SS|- **Type**: boolean
#TT|- **Default**: `false`
#BY|- **Description**: Enable audio recording for this camera. When enabled, the recorder captures audio from the RTSP/ONVIF/Xiaomi stream and muxes it into the MP4 recording.
#MV|- **Supported Formats**: AAC (RTSP cameras), G.711 μ-law/A-law (ONVIF/Xiaomi cameras)
#YR|- **Note**: Not supported for MJPEG or HTTP-JPEG cameras
#TM|- **Example**: `true`, `false


- **Type**: string
- **Optional**: Yes (required for Xiaomi cameras)
- **Description**: Xiaomi Device ID from cloud service
- **Example**: `"camera_did_123"`

### `cameras[].merge`
- **Type**: object
- **Optional**: Yes
- **Description**: Per-camera merge configuration overrides
- **Note**: Only non-zero fields override the global merge config
- **Example**: See [Merge Configuration](#merge-configuration)

## Cleanup Configuration

### `cleanup.retention_days`
- **Type**: integer
- **Default**: 30
- **Range**: 1-3650
- **Description**: Delete recordings older than N days
- **Example**: `7`, `30`, `90`

### `cleanup.check_interval`
- **Type**: string
- **Default**: `"1h"`
- **Description**: How often to check for expired recordings
- **Example**: `"30m"`, `"1h"`, `"2h"`

### `cleanup.disk_threshold_percent`
- **Type**: integer
- **Default**: 95
- **Range**: 50-99
- **Description**: Start cleanup when disk usage exceeds N%
- **Example**: `90`, `95`, `98`

## Merge Configuration

### `merge.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable segment merging functionality

### `merge.check_interval`
- **Type**: string
- **Default**: `"1h"`
- **Description**: How often to check for merge candidates
- **Example**: `"30m"`, `"1h"`, `"2h"`

### `merge.window_size`
- **Type**: string
- **Default**: `"1h"`
- **Description**: Time window for merging segments (segments within this window can be merged)
- **Example**: `"30m"`, `"1h"`, `"2h"`

### `merge.batch_limit`
- **Type**: integer
- **Default**: 200
- **Description**: Maximum number of segments to merge in one batch
- **Example**: `100`, `200`, `500`

### `merge.min_segment_age`
- **Type**: string
- **Default**: `"10m"`
- **Description**: Minimum age before a segment can be merged
- **Example**: `"5m"`, `"10m"`, `"30m"`

### `merge.min_segments_to_merge`
- **Type**: integer
- **Default**: 3
- **Description**: Minimum number of segments required to trigger a merge
- **Example**: `2`, `3`, `5`

## FTP Configuration

### `ftp.enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Enable FTP server

### `ftp.port`
- **Type**: integer
- **Default**: 2121
- **Range**: 1-65535
- **Description**: FTP control port
- **Example**: `2121`, `990`

### `ftp.passive_port_range`
- **Type**: string
- **Default**: `"2122-2140"`
- **Description**: Passive mode port range (start-end)
- **Example**: `"2122-2140"`, `"40000-40100"`

## MQTT Configuration

### `mqtt.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable MQTT client for trigger-based recording

### `mqtt.broker`
- **Type**: string
- **Required**: Yes (if enabled)
- **Description**: MQTT broker URL
- **Example**: `"tcp://localhost:1883"`, `"mqtt://192.168.1.100:1883"`

### `mqtt.topic`
- **Type**: string
- **Required**: Yes (if enabled)
- **Description**: MQTT topic to subscribe to for recording triggers
- **Example**: `"mibeenr/trigger"`, `"cameras/front-door/record"`

### `mqtt.client_id`
- **Type**: string
- **Default**: `"mibee-nvr"`
- **Description**: MQTT client identifier
- **Example**: `"mibee-nvr"`, `"nvr-client-01"`

### `mqtt.username`
- **Type**: string
- **Optional**: Yes
- **Description**: MQTT broker authentication username
- **Example**: `"mqtt-user"`, `"admin"`

### `mqtt.password`
- **Type**: string
- **Optional**: Yes
- **Description**: MQTT broker authentication password
- **Example**: `"mqtt-password"`

## WebDAV Configuration

### `webdav.enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Enable WebDAV server

### `webdav.path_prefix`
- **Type**: string
- **Default**: `"/dav"`
- **Description**: URL path prefix for WebDAV access
- **Example**: `"/dav"`, `"/ recordings"`

### `webdav.read_write`
- **Type**: boolean
- **Default**: `false`
- **Description**: Allow write operations (PUT, MKCOL, DELETE, etc.)
- **Example**: `true`, `false`

## HLS Configuration

### `hls.write_buffer_size`
- **Type**: integer
- **Default**: 100
- **Description**: Async frame buffer size per stream (units of frames)
- **Example**: `40`, `100`, `200`

### `hls.segment_max_size_mb`
- **Type**: integer
- **Default**: 10
- **Description**: Maximum HLS segment size in megabytes
- **Example**: `5`, `10`, `20`

### `hls.segment_count`
- **Type**: integer
- **Default**: 7
- **Range**: 3-10
- **Description**: Number of HLS segments per stream
- **Example**: `5`, `7`, `10`

### `hls.max_streams`
- **Type**: integer
- **Default**: 4
- **Range**: 1-20
- **RPi Constraint**: Maximum 4 on Raspberry Pi 3B
- **Description**: Maximum number of concurrent HLS streams
- **Example**: `4`, `8`, `16`

## Xiaomi Configuration

### `xiaomi.user_id`
- **Type**: string
- **Required**: Yes (if Xiaomi cameras configured)
- **Description**: Xiaomi cloud account user ID (obtained after authentication)
- **Example**: `"1234567890"`

### `xiaomi.token`
- **Type**: string
- **Required**: Yes (if Xiaomi cameras configured)
- **Description**: Xiaomi passToken for API access (obtained via `/api/xiaomi/auth`)
- **Example**: `"xiaomi_token_123"`

### `xiaomi.region`
- **Type**: string
- **Default**: `"cn"`
- **Description**: Xiaomi cloud region code
- **Options**: `"cn"`, `"sg"`, `"de"`, etc.
- **Example**: `"cn"`, `"sg"`

## Observability Configuration

### `observability.log_level`
- **Type**: string
- **Default**: `"info"`
- **Options**: `"debug"`, `"info"`, `"warn"`, `"error"`
- **Description**: Log level verbosity
- **Example**: `"debug"`, `"info"`, `"error"`

### `observability.log_format`
- **Type**: string
- **Default**: `"text"`
- **Options**: `"json"`, `"text"`
- **Description**: Log output format
- **Example**: `"json"`, `"text"`

### `observability.enable_pprof`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable pprof debug endpoints for performance profiling
- **Note**: Use with caution in production

## Camera Protocol Examples

### RTSP Camera
```yaml
cameras:
  - id: "front-door"
    name: "Front Door Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    username: "admin"
    password: "camera-password"
    enabled: true
    sub_stream_url: "rtsp://192.168.1.100:554/stream2"
    snapshot_url: "http://192.168.1.100:8080/snapshot"
```

### HTTP JPEG Camera
```yaml
cameras:
  - id: "backyard"
    name: "Back Yard Camera"
    protocol: "http"
    encoding: "jpeg"
    url: "http://192.168.1.101/capture"
    sample_interval: 1
    enabled: true
```

### ONVIF Camera
```yaml
cameras:
  - id: "lobby"
    name: "Lobby Camera"
    protocol: "onvif"
    url: "http://192.168.1.102:80/onvif/device_service"
    enabled: true
    # Optional: specify encoding
    encoding: "h264"
    # Optional: specify stream encoding
    stream_encoding: "H264"
```

### Xiaomi Camera
```yaml
xiaomi:
  user_id: "1234567890"
  token: "xiaomi_token_123"
  region: "cn"

cameras:
  - id: "xiaomi-cam"
    name: "Xiaomi Camera"
    protocol: "xiaomi"
    encoding: "h264"
    did: "xiaomi_device_id"
    vendor: "cs2"
    enabled: true
```

## Migration from Legacy Format

Legacy protocol formats like `"rtsp_h264"` are automatically converted to the new separate `protocol` and `encoding` fields:

```yaml
# Old format (still supported)
cameras:
  - id: "cam1"
    protocol: "rtsp_h264"
    url: "rtsp://..."

# Automatically converted to new format:
# protocol: "rtsp"
# encoding: "h264"
```

## Validation Rules

The configuration is validated on startup with these constraints:

- **Camera IDs**: Must be unique across all cameras
- **Camera URLs**: Must have valid scheme (http/rtsp) and host
- **ONVIF Cameras**: Must have either URL or onvif_endpoint
- **Xiaomi Cameras**: Must have xiaomi.token configured
- **Port Numbers**: Must be in range 1-65535
- **Segment Duration**: Maximum 30 seconds on RPi 3B
- **Retention Days**: Must be between 1 and 3650
- **Disk Threshold**: Must be between 50% and 99%
- **Merge Configuration**: All duration fields must be valid
- **HLS Configuration**: 
  - Segment count: 3-10
  - Max streams: 1-20 (4 on RPi 3B)

## File Paths and Locations

- **Default config path**: `./mibee-nvr.yaml`
- **Default storage**: `/var/lib/mibee-nvr`
- **Recordings**: `{root_dir}/recordings/{encoding}/{camera_id}/`
- **Segments**: `{root_dir}/recordings/{encoding}/{camera_id}/`
- **Snapshots**: `{root_dir}/snapshots/{camera_id}/`
- **WebDAV**: `{root_dav}{root_dir}/` (where root_dav is reverse proxy path)

## Quick Configuration

### Basic Setup
```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/var/lib/mibee-nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password: "your-password-here"
cameras:
  - id: "cam1"
    name: "Camera 1"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
cleanup:
  retention_days: 30
  disk_threshold_percent: 95
```

### Complete Setup with All Features
```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/mnt/data/nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password_hash: "$2a$10$N9qo8uLOickgx2ZMRZoMy..."
cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    sub_stream_url: "rtsp://192.168.1.100:554/sub"
  - id: "xiaomi-cam"
    name: "Xiaomi Camera"
    protocol: "xiaomi"
    encoding: "h264"
    did: "xiaomi_device_id"
    vendor: "cs2"
    enabled: true
xiaomi:
  user_id: "1234567890"
  token: "xiaomi_token_123"
  region: "cn"
cleanup:
  retention_days: 30
  disk_threshold_percent: 90
merge:
  enabled: true
  check_interval: "1h"
  batch_limit: 200
ftp:
  enabled: true
  port: 2121
mqtt:
  enabled: true
  broker: "tcp://192.168.1.100:1883"
  topic: "mibeenr/trigger"
webdav:
  enabled: true
  read_write: false
hls:
  max_streams: 4
observability:
  log_level: "info"
```