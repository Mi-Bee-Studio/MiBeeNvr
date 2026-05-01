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
cleanup:
  retention_days: 30
  check_interval: "1h"
  disk_threshold_percent: 95
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
- **Important**: The `mibee-nvr hash-password` CLI command is NOT yet implemented
- **Temporary Solution**: Use Go code to generate the hash
- **Example**: `"hashed_password_here"`

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
- **Options**: `"rtsp_h264"`, `"rtsp_mjpeg"`, `"http_jpeg"`
- **Example**: `"rtsp_h264"`

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

## Cleanup Configuration

### `cleanup.retention_days`
- **Type**: integer
- **Default**: `30` (when not set or `0`)
- **Description**: Number of days to keep recordings
- **Important**: A value of `0` is treated as "unconfigured" and defaults to 30 days
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
- **Important**: WebDAV is read-only - all write operations return 403
- **Example**: `"/dav"`, `"/recordings"`

## Important Notes

### Security Considerations
- FTP credentials use the same username/password as the web interface
- WebDAV is intentionally read-only for security
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