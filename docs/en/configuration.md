# Configuration Reference

MiBee NVR uses a YAML configuration file to control all aspects of its operation. Below is a comprehensive reference of all available options, their defaults, and usage examples.

![General settings page](images/settings-general.webp)

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
  local_bypass: false             # Skip login for browsers on the NVR host machine (localhost) (default off)
cameras:
  - id: "cam1"
    name: "Camera Name"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://..."
    enabled: true
    audio_enabled: false
    onvif_endpoint: ""           # ONVIF specific
    profile_token: ""            # ONVIF specific  
    stream_encoding: ""          # ONVIF auto-detect (H264/H265)
    sub_stream_url: "rtsp://..."  # Sub-stream for live preview
    snapshot_url: "http://..."    # JPEG snapshot for thumbnails
    sample_interval: 1            # MJPEG frame sampling
    hls_max_fps: 0               # HLS frame rate limit
    vendor: ""                   # Xiaomi transport vendor
    did: ""                      # Xiaomi device ID
    frame_watchdog_timeout: "30s" # Per-camera frame watchdog
    merge:                       # Per-camera merge overrides
      enabled: false
      check_interval: "1h"
      window_size: "1h"
      batch_limit: 150
      min_segment_age: "5m"
      min_segments_to_merge: 2
    transcoding:                 # Per-camera transcoding overrides
      enabled: false
      target_codec: "h264"
      preset: "ultrafast"
      bitrate: "2M"
    timelapse:                   # Per-camera timelapse recording
      enabled: false
      interval: "30s"
      delete_original: false
    health_overrides:            # Per-camera health threshold overrides
      min_fps: 10
      offline_threshold: "15s"
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
  topic: "mibee"
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
  low_latency: false             # Enable Low-Latency HLS
  part_min_duration: "200ms"     # LL-HLS partial segment duration
xiaomi:
  user_id: ""                    # Xiaomi account user ID (from auth response)
  token: ""                      # Xiaomi passToken for API access
  region: "cn"                   # Region code: cn, sg, de, etc.
observability:
  log_level: "info"              # Log level: debug, info, warn, error
  log_format: "text"             # Log format: json or text
  enable_pprof: false            # Enable pprof debug endpoints
streaming:
  webrtc:
    enabled: true
    max_viewers: 2               # Range: 1-10
    idle_timeout: "60s"
  flv:
    enabled: true
    max_viewers: 10              # Range: 1-50
    idle_timeout: "60s"
    gop_cache_size: 1
websocket:
  max_viewers: 10
  write_buf_size: 100
  idle_timeout: 60s
health:
  enabled: false
  events_retention: "720h"       # 30 days
  alerts:
    cooldown: "5m"
    mqtt: false
  layer1:
    offline_threshold: "30s"
  layer2:
    bitrate_change_threshold: 0.5
    min_fps: 5
    max_idr_interval: "60s"
  layer2_5:
    freeze_timeout: "10s"
  auto_remediation:
    enabled: false
    max_restarts_per_hour: 3
    cooldown_minutes: 5
    blacklist_hours: 1
    global_max_per_min: 10
remote_log:
  enabled: false
  endpoint: ""                   # VictoriaLogs URL when enabled
  format: "jsonline"             # jsonline or loki
transcoding:
  enabled: false
  max_workers: 1                 # Range: 1-4
  job_timeout: "30m"
  history_retention: "168h"      # 7 days
ai:
  inference_timeout_ms: 0
  frame_skip_rate: 0
  confidence_threshold: 0.0
  model_path: ""
rtmp:
  enabled: false
  port: 1935
  # stream_keys:                  # Map camera_id to stream key
  #   cam1: "my-stream-key"
srt:
  enabled: false
  port: 9000
  # streams:                      # SRT stream mappings
  #   - camera_id: "cam1"
  #     mode: "listener"
metrics_auth:
  username: ""
  password: ""
version: "1.0"
```

## Server Configuration

### `server.listen`
- **Type**: string
- **Default**: `":9090"`
- **Description**: The address and port for the web server to listen on
- **Example**: `":8080"` or `"192.168.1.100:9090"`
- **Env override**: The `NVR_LISTEN_PORT` environment variable overrides the port portion at startup (e.g. `NVR_LISTEN_PORT=8080`). Useful for NAS host-networking deployments where you can't edit the config file. You can also set it via `install.sh --port <port>` or the Web UI Settings page.

### `server.device_id` / `server.device_name`
- **Type**: string
- **Default**: `device_id` is generated automatically on first start (UUIDv4, persisted); `device_name` defaults to the system hostname
- **Description**: Stable LAN identity exposed by `GET /api/health` so clients (e.g. mobile apps) can anchor on an ID instead of a changeable IP. Both are optional; an explicit `device_name` overrides the hostname.
- **Example**: `device_name: "garage-nvr"`

### `server.discovery.mdns.enabled` / `server.discovery.udp.*`
- **Type**: bool / (bool, int)
- **Default**: both `true`; UDP port `49090`
- **Description**: LAN self-announcement. mDNS/DNS-SD registers the `_mibee-nvr._tcp` service; the UDP responder answers `MIBEE-NVR-DISCv1?` broadcast probes with a JSON identity payload (covers multicast-restricted Wi-Fi). Bind failures are logged, never fatal.

### `server.rtsp.enabled` / `server.rtsp.port`
- **Type**: bool / int
- **Default**: `true` / `8554`
- **Description**: Built-in RTSP output server — every camera gets a stable pull URL `rtsp://<NVR-IP>:8554/<camera_id>` that third-party platforms (e.g. Synology Surveillance Station) can use directly as a camera source. H.264/H.265 served natively (no transcoding, video only; MJPEG/JPEG cameras are not served). Credentials optional (`username`/`password`; empty = open on the LAN; setting both enables Basic/Digest auth — URL becomes `rtsp://user:pass@<NVR-IP>:8554/<camera_id>`). Docker deployments must publish the port (`-p 8554:8554`); a bind failure only logs an error — the rest of the NVR keeps working. See [Home Assistant Integration](./home-assistant.md).
- **See**: [Sub-streams · RTSP output](sub-stream.md#rtsp-output-third-party-platforms)

### `server.substream.idle_timeout_s` / `server.substream.ready_timeout_s`
- **Type**: int / int
- **Default**: `30` / `8`
- **Description**: On-demand sub-stream pull parameters — `idle_timeout_s` is how long an idle pull stays warm before recycling; `ready_timeout_s` is how long a `quality=sub` request waits for first video. See [Sub-streams](sub-stream.md).

## Storage Configuration

### `storage.root_dir`
- **Type**: string
- **Default**: `/var/lib/mibee-nvr` (binary) or `/data` (Docker)
- **Description**: Root directory for storing recordings, database, and temporary files. All camera recordings are stored under `{root_dir}/{camera_id}/`.
- **Docker**: When running in Docker, this is set via the `NVR_DATA_DIR` environment variable. The volume mount and `NVR_DATA_DIR` must match.
- **Binary**: Can be set via `--data-dir` flag with `mibee-nvr init`, or directly in the YAML config.
- **Example**: `/var/lib/mibee-nvr`, `/mnt/external/nvr`, `/data`

### `storage.segment_duration`
- **Type**: string
- **Default**: `"30s"`
- **Description**: Duration of video segments (memory intensive)
- **Important**: Each segment holds all video data in RAM until completion
- **Memory Usage**:
  - 30s segments: ~15-20MB per segment
  - 60s segments: ~30-40MB per segment
  - 120s segments: ~60-80MB per segment
- **Platform-aware cap (auto-applied at startup)**: to keep the MP4 muxer's RAM usage safe, the configured value is clamped based on available RAM:
  - **≤2 GB available RAM** (e.g. Raspberry Pi 3B): capped at **30s**
  - **>2 GB available RAM** (e.g. Banana Pi M5, x86): up to **2m** (120s), which halves the fragment count rolling merge must process
  - Values above the platform cap are **silently clamped** with a warning in the logs (they do not fail startup). On non-Linux hosts or if `/proc/meminfo` cannot be read, the conservative 30s cap applies.

### `storage.db_path`
- **Type**: string
- **Optional**: yes
- **Default**: `mibee-nvr.db` in the data directory
- **Description**: SQLite database location. **Decoupled from the recording root** — switching `root_dir` / migrating recordings never moves the database (auto-pinned on bare-metal first boot, preventing an empty-DB foot-gun after a root switch). Docker deployments pin it to `NVR_DATA_DIR`.
- **Generally leave unset**.

### `storage.camera_roots`
- **Type**: map (cameraID → path)
- **Optional**: yes
- **Description**: Per-camera recording-root overrides — mapped cameras record to the given directory, the rest to the default root. **Hot for new segments**; history moves via the background migrator. Also manageable at runtime via API / Web UI (see [Storage Management](storage-management.md)).
- **Example**: `backyard: "/mnt/bigdisk/recordings"`

### `storage.migration_rate_mb` / `storage.migration_window`
- **Type**: int / string
- **Default**: `15` / empty (always)
- **Description**: The background migrator's copy rate cap (MB/s — never competes with recording IO) and its local-time window (e.g. `"22:00-06:00"`; empty = always, rate-limited).
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

### `auth.local_bypass`
- **Type**: boolean
- **Default**: `false`
- **Description**: Allows browsers running on the NVR host machine itself (loopback connections `127.0.0.1` / `::1` with no proxy/gateway headers) to skip the login page. The frontend learns this via the `local_access` field of `/api/health`.
- **Security warning**: For **bare-metal (systemd / native binary) deployments only**. **NEVER enable behind a reverse proxy (Caddy/nginx) or Docker published ports** — in those topologies every request arrives from `127.0.0.1`, so enabling this would let ALL remote clients bypass authentication.
- **Required conditions** (all three): `local_bypass: true`, the request originates from loopback (RemoteAddr 127.0.0.1/::1), **the Host header is `localhost` / `127.0.0.1` / `[::1]` after stripping the port**, and no `X-Forwarded-For` / `X-Real-IP` / `Forwarded` proxy header is present. Access via the host LAN IP or hostname does NOT bypass (intentionally conservative; also blocks malicious web pages and DNS rebinding).
- **Example**: `local_bypass: true`

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
- **Options**: `"rtsp"`, `"http"`, `"onvif"`, `"xiaomi"`, `"timelapse"`, `"srt"`, `"rtmp"`
- **Push protocols**: `"srt"` and `"rtmp"` are push/ingest protocols — a remote publisher pushes a stream INTO the NVR. The `url` field is not used; instead configure `stream_key` (RTMP) or `srt_stream_id` / `srt_passphrase` (SRT). See [Push Cameras](./camera-guide.md#push-cameras-srt--rtmp--cross-network-ingest).
- **Legacy Format**: Not supported — combined strings like `"rtsp_h264"`, `"rtsp_h265"`, `"rtsp_mjpeg"`, `"http_jpeg"` are rejected with a validation error since 0.10.0
- **Note**: Use the separate `protocol` + `encoding` fields instead (see `cameras[].encoding` below)
- **Compatibility**: Only the separate `protocol` + `encoding` format is supported

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
  - `protocol: "srt"` → `encoding: "h264"` or `"h265"` (H.264 only in current SRT demux)
  - `protocol: "rtmp"` → `encoding: "h264"` (RTMP carries H.264 only)

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
- **Description**: Manual RTSP address of the camera's low-resolution sub-stream — pulled on demand for explicit `quality=sub` viewing (grid "Smooth", live quality switcher, sub-stream cascade), see [Sub-streams](sub-stream.md). ONVIF cameras usually don't need this — the sub-stream profile is auto-discovered (`sub_profile_token`)
- **Note**: Sub-stream must use the same codec (H.264/H.265) as the main stream
- **Example**: `"rtsp://192.168.1.100:554/stream2"`

### `cameras[].sub_profile_token`
- **Type**: string
- **Optional**: Yes
- **Description**: ONVIF sub-stream profile token. **Blank = auto-discover** (picks the second-highest-resolution profile besides the main one; backfilled once — clear and save to re-discover). Manual values override the discovery result.
- **Example**: `"SubStreamToken"`, `"Profile2"`

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

- **Type**: boolean
- **Default**: `false`
- **Description**: Enable audio recording for this camera. When enabled, the recorder captures audio from the RTSP/ONVIF/Xiaomi stream and muxes it into the MP4 recording. Audio is also available for live preview playback via the WebSocket audio endpoint.
- **Supported Formats**: AAC (RTSP cameras), G.711 μ-law/A-law (ONVIF/Xiaomi cameras), Opus (Xiaomi cameras)
- **Note**: Not supported for MJPEG or HTTP-JPEG cameras
- **Example**: `true`, `false`

### `cameras[].audio_in_recordings`

- **Type**: boolean
- **Default**: `false`
- **Description**: Whether recorded segments keep the camera's real audio track (event spans in merged products). Default off — recordings are video-only; live monitoring and the [audio trigger](adaptive-recording.md#audio-trigger) are unaffected by this flag.
- **Example**: `true`, `false`

### `cameras[].recording_mode`

- **Type**: string
- **Default**: `"continuous"`
- **Values**: `"continuous"` (full-rate recording) / `"adaptive"` (motion-aware adaptive recording)
- **Description**: Write-density strategy. In `adaptive` mode the camera writes one keyframe per `adaptive.timelapse_interval` while calm and instantly returns to full rate on activity / audio / external triggers (see [Adaptive Recording](adaptive-recording.md)). H.264/H.265 cameras only (MJPEG has no compressed-domain differential signal).
- **Example**: `"adaptive"`

### `cameras[].adaptive`

- **Type**: object
- **Optional**: yes (only meaningful with `recording_mode: "adaptive"`)
- **Description**: Adaptive-recording tuning (all optional; defaults below — see [Adaptive Recording](adaptive-recording.md#tuning))
- **Fields**:
  - `calm_threshold` (string, default `"60s"`, range 10s–30m) — how long calm must last before going sparse
  - `timelapse_interval` (string, default `"30s"`, range 5s–10m) — keyframe cadence while sparse
  - `spike_factor` (float, default `5.0`, range 1.5–20) — activity sensitivity threshold
  - `gop_buffer_bytes` (int, default `33554432`, range 1–64MB) — GOP pre-buffer cap
  - `ambient_audio` (boolean, default `false`) — record ambient audio while sparse (merged into an atmosphere bed; G.711 only)
  - `timelapse_frame_ms` (int, 100/300/500, default 100) — merged-product timelapse cadence
  - `ambient_archive` (boolean, default `false`) — keep raw ambient audio as a `<segment>.g711` sidecar

### `cameras[].audio_trigger`

- **Type**: object
- **Optional**: yes (only effective for `recording_mode: "adaptive"` cameras with G.711 audio)
- **Description**: Loudness-triggered recording — abnormal sounds exit sparse mode with pre-trigger audio back-fill (see [Adaptive Recording · Audio Trigger](adaptive-recording.md#audio-trigger))
- **Fields**:
  - `enabled` (boolean, required) — arm the loudness input
  - `min_dbfs` (float, default `-45`, range -90–0) — loudness threshold over a 1s window
  - `pre_capture_s` (int, default `3`, range 0–30) — seconds of pre-trigger audio

### `cameras[].recording_schedule`

- **Type**: object
- **Optional**: yes
- **Description**: Recording time window (24/7 by default). Outside the window nothing is written to disk (live view unaffected).
- **Fields**:
  - `time_ranges` (array) — list of `{start: "09:00", end: "18:00"}`; overlaps auto-merge
  - `days_of_week` (array of int) — 0=Sunday … 6=Saturday; empty = every day

### `cameras[].cascade_enabled`

- **Type**: boolean
- **Default**: `true` (nil treated as true)
- **Description**: GB28181 cascade catalog-convergence switch — `false` hides this camera from the upper platform's aggregated catalog and its channel's INVITEs answer 404. The channel-code allocation is kept, so re-enabling restores the same code (upper-platform bindings don't drift).
- **Example**: `false`

### `cameras[].cascade_sub_stream`

- **Type**: boolean
- **Default**: `false`
- **Description**: Cascade forwarding rides the camera's **sub-stream** instead of main — the low-resolution tier keeps uplink bandwidth bounded for upper platforms that only need a preview. INVITE falls back to main when the camera has no sub-stream or its pull fails; cameras without one are unaffected.
- **Example**: `true`


### `cameras[].did`
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

### `cameras[].transcoding`
- **Type**: object
- **Optional**: Yes
- **Description**: Per-camera transcoding configuration overrides. See [Transcoding Configuration](#transcoding-configuration) for field details.
- **Example**:
  ```yaml
  cameras:
    - id: "cam1"
      transcoding:
        enabled: true
        target_codec: "h264"
        preset: "ultrafast"
        bitrate: "2M"
  ```

### `cameras[].timelapse`
- **Type**: object
- **Optional**: Yes
- **Description**: Per-camera timelapse recording configuration
- **Fields**:
  - **`enabled`** (boolean, default: `false`) — Enable timelapse recording
  - **`interval`** (string, default: `"30s"`, min: 1s) — Snapshot capture interval
  - **`output_fps`** (integer, default: 30, range: 1-60) — Output framerate
  - **`video_codec`** (string, default: `"h264"`, options: h264/h265) — Video codec (deprecated)
  - **`delete_original`** (boolean, default: `false`) — Remove original segments after timelapse
  - **`merge_enabled`** (boolean, default: auto-detect) — Enable auto-merging
  - **`merge_mode`** (string, default: `"auto"`, options: auto/mp4/jpeg) — Merge output format
  - **`daily_merge`** (boolean, default: `true`) — Merge segments into daily files
  - **`merge_output_fps`** (integer, default: 30, range: 1-60) — Merge output framerate
- **Example**:
  ```yaml
  cameras:
    - id: "cam1"
      timelapse:
        enabled: true
        interval: "60s"
        delete_original: true
  ```

### `cameras[].health_overrides`
- **Type**: object
- **Optional**: Yes
- **Description**: Per-camera health monitoring threshold overrides. Non-zero values take precedence over global [Health Configuration](#health-configuration).
- **Fields**:
  - **`max_idr_interval`** (string) — Max IDR interval override (e.g. `"30s"`)
  - **`bitrate_change_threshold`** (float, range: 0-1) — Bitrate change threshold override
  - **`min_fps`** (integer) — Minimum FPS override
  - **`offline_threshold`** (string) — Offline detection threshold override (e.g. `"15s"`)
  - **`freeze_timeout`** (string) — Freeze detection timeout override (e.g. `"5s"`)
- **Example**:
  ```yaml
  cameras:
    - id: "cam1"
      health_overrides:
        min_fps: 10
        offline_threshold: "15s"
  ```

### `cameras[].frame_watchdog_timeout`
- **Type**: string
- **Optional**: Yes
- **Default**: `"30s"`
- **Description**: Timeout before declaring a camera unhealthy when no frames are received. Per-camera override of the frame watchdog.
- **Example**: `"30s"`, `"60s"`, `"120s"`

### `cameras[].ring_buf_cap`
- **Type**: int
- **Optional**: Yes
- **Default**: `0` (built-in default 300)
- **Description**: Overrides the recorder's frame ring-buffer (frameCh) capacity (#521). The buffer absorbs write-loop stalls (segment-finalize fsync, merge IO, lock contention); when it fills, frames are dropped (`nvr_recorder_ring_buffer_drops_total` metric + the overflow counter on the flow page's recording branch). Raise it for cameras showing occasional drops to trade ~1KB of memory per slot for stall tolerance. H.264/H.265 recorders only. Range 0–10000.
- **Example**: `600`, `1000`

### `cameras[].stream_key`
- **Type**: string
- **Optional**: Yes (push-in only)
- **Description**: RTMP stream key for push-in cameras (`protocol: "rtmp"`). Maps the incoming `rtmp://host:1935/live/{key}` to this camera. The publisher pushes TO this address.

### `cameras[].srt_passphrase`
- **Type**: string
- **Optional**: Yes (push-in only)
- **Description**: AES passphrase for encrypted SRT push-in cameras (`protocol: "srt"`).

### `cameras[].srt_stream_id`
- **Type**: string
- **Optional**: Yes (push-in only)
- **Description**: SRT stream ID for push-in cameras (`protocol: "srt"`). Maps the incoming SRT `streamid` to this camera.

### `cameras[].push_retention_days`
- **Type**: integer (nullable)
- **Optional**: Yes (push-in only: `protocol: "srt"` or `"rtmp"`)
- **Default**: `null` (follow global cleanup retention)
- **Description**: Recording retention override for push-in cameras. `null` = follow global `cleanup.retention_days`, `0` = live-only (no recording), `N` = keep N days.

### `cameras[].push_targets`
- **Type**: array of objects
- **Optional**: Yes (any camera protocol)
- **Description**: Push-out relay targets — forward this camera's live stream to remote destinations (another NVR's ingest, a live platform, a backup). Native Go by default, FFmpeg optional for compatibility. Each target is an independent connection. See [Push-Out Relay](./camera-guide.md#push-out-relay--forward-a-camera-to-remote-destinations).
- **Fields per target**:
  - `id` (string, required) — stable identifier within the camera
  - `name` (string, optional) — display name
  - `protocol` (string, required) — `"rtmp"` or `"rtsp"`
  - `url` (string, required) — target URL (`rtmp://host:1935/app/key` or `rtsp://host:8554/path`)
  - `enabled` (boolean, required) — whether the target is active
  - `platform` (string, optional) — platform preset: `"bilibili"`, `"douyin"`, `"youtube"`, `"kuaishou"`, `"generic"`, or empty for custom
  - `transcode_policy` (string, optional, default: `"off"`) — `"auto"` (probe hardware, fallback software), `"force_sw"` (always libx264), `"off"` (reject H.265 sources)
  - `video_preset_override` (object, optional) — override preset params: `{ resolution, framerate, video_bitrate_kbps, gop_seconds, profile, bframes }`
- **Note**: H.264 sources are remuxed zero-copy. H.265 sources are live-transcoded to H.264 when `transcode_policy` is set (requires FFmpeg). Thermal monitoring protects ARM SBCs from overheating during transcode. See [Relay Guide](./relay-guide.md) for details.

## API Keys Configuration

### `api_keys`
- **Type**: array of objects
- **Optional**: Yes
- **Description**: MiBeeVision API keys for external AI processing integration. Keys use the `mbv_` prefix and are authenticated via Bearer token (checked before BasicAuth). See [Authentication](./api/authentication.md) for details.
- **Fields per key**:
  - `name` (string, required) — display name for the key
  - `key` (string, required) — the API key value (must start with `mbv_`)
- **Example**:
  ```yaml
  api_keys:
    - name: "MiBeeVision Production"
      key: "mbv_a1b2c3d4e5f6..."
  ```
- **Note**: Keys are auto-encrypted on save if `NVR_ENCRYPTION_KEY` is set. Generate new keys via `POST /api/settings/api-keys`.

## Vision Push Integration Configuration

External AI post-processing consumer (MiBeeVision) integration. The consumer
authenticates with the API keys above and reports heartbeats; a healthy consumer
receives video-segment pushes and writes AI events back to the NVR.

### `vision.enabled`
- **Type**: bool
- **Default**: `false`
- **Description**: Enable the push integration. Pushing pauses when the heartbeat times out; when it returns, missed segments are re-pushed automatically.

### `vision.url`
- **Type**: string
- **Example**: `"http://192.168.1.20:9091"`
- **Description**: Base address of the consumer service.

### `vision.heartbeat_timeout_secs`
- **Type**: int
- **Default**: `60`
- **Description**: Consider the consumer offline after this many seconds without a heartbeat.

### `vision.push_mode`
- **Type**: string
- **Default**: `"notify"`
- **Values**: `"notify"` (tell the consumer to fetch) / `"upload"` (the NVR pushes video bytes)

### `vision.skip_cameras`
- **Type**: array of string
- **Default**: `[]`
- **Description**: Camera IDs never pushed (e.g. MJPEG/JPEG encodings — pushing them is pointless). Skipped segments don't count toward the offline re-push window. Skip lists reported by consumer heartbeats merge (union) with this static list.

### `vision.sub_layer_cameras`
- **Type**: array of string
- **Default**: `[]`
- **Description**: Sub-stream analysis-layer cameras — listed cameras get an on-demand **sub-stream** pull recorded as independent low-res analysis segments (not in the recording library, not merged; deleted once consumed); pushes ride the sub-stream segments and main-stream segments are no longer pushed. Low-res segments decode at 1/4–1/16 the cost of main. Requires a sub-stream on the camera (`sub_profile_token` or `sub_stream_url`). Companion knobs: `sub_layer_segment_secs` (segment duration, default 60), `sub_layer_retention_secs` (on-disk bound, default 7200), `sub_layer_push_interval_secs` (push sweep cadence, default 20).

## GB28181 Configuration

GB/T 28181 platform access (default off). See the [GB28181 guide](gb28181-guide.md)
for the full key reference (`gb28181:` platform role and `gb28181_cascade:`
lower-level cascade role) and `config.example.yaml` in the repo root for examples.

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
- **Description**: Topic **prefix** (not a full topic). Trigger subscription is `{topic}/trigger/+`; status publishing uses `{topic}/health/{camera_id}` and `{topic}/event/{topic}`
- **Example**: `"mibee"`, `"home/security"`

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

### `mqtt.status_events`
- **Type**: boolean
- **Default**: `false`
- **Description**: Forward whitelisted events (segment completed, camera added/quality, storage health) to `{topic}/event/<event-topic>` so smart-home platforms can consume NVR state. See [MQTT Integration — Status Publishing](./mqtt-integration.md#status-publishing)

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

### `hls.low_latency`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable Low-Latency HLS (LL-HLS) using partial segments for reduced latency
- **Note**: Requires `segment_count` >= 7 when enabled
- **Example**: `true`, `false`

### `hls.part_min_duration`
- **Type**: string
- **Default**: `"200ms"`
- **Range**: 100ms-1s
- **Description**: Minimum duration for LL-HLS partial segments
- **Example**: `"200ms"`, `"500ms"`, `"1s"`

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

## Streaming Configuration

> **Note**: `streaming.default_protocol` was removed in 0.11.0 (stale values are silently ignored) — the per-camera orchestrator auto-selects the protocol; use the switcher in the player UI to pin one manually.

### `streaming.webrtc.enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Enable WebRTC WHEP streaming for low-latency live view
- **Example**: `true`, `false`

### `streaming.webrtc.max_viewers`
- **Type**: integer
- **Default**: 2
- **Range**: 1-10
- **Description**: Maximum number of concurrent WebRTC viewers per stream
- **Example**: `2`, `5`, `10`

### `streaming.webrtc.idle_timeout`
- **Type**: string
- **Default**: `"60s"`
- **Description**: Idle timeout before closing an inactive WebRTC connection
- **Example**: `"30s"`, `"60s"`, `"120s"`

### `streaming.flv.enabled`
- **Type**: boolean
- **Default**: `true`
- **Description**: Enable HTTP-FLV streaming for browser-compatible live view
- **Example**: `true`, `false`

### `streaming.flv.max_viewers`
- **Type**: integer
- **Default**: 10
- **Range**: 1-50
- **Description**: Maximum number of concurrent FLV viewers per stream
- **Example**: `10`, `25`, `50`

### `streaming.flv.idle_timeout`
- **Type**: string
- **Default**: `"60s"`
- **Description**: Idle timeout before closing an inactive FLV connection
- **Example**: `"30s"`, `"60s"`, `"120s"`

### `streaming.flv.gop_cache_size`
- **Type**: integer
- **Default**: 1
- **Description**: Number of GOPs to cache for instant FLV playback on viewer connect
- **Example**: `1`, `2`, `5`

## WebSocket Configuration

### `websocket.max_viewers`
- **Type**: integer
- **Default**: 10
- **Description**: Maximum number of concurrent WebSocket viewers per stream
- **Example**: `10`, `20`, `50`

### `websocket.write_buf_size`
- **Type**: integer
- **Default**: 100
- **Description**: Write buffer size for WebSocket frames (units of frames)
- **Example**: `100`, `200`, `500`

### `websocket.idle_timeout`
- **Type**: duration
- **Default**: `60s`
- **Description**: Idle timeout before closing an inactive WebSocket connection
- **Example**: `30s`, `60s`, `120s`

## Health Configuration

### `health.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable camera health monitoring system
- **Example**: `true`, `false`

### `health.events_retention`
- **Type**: string
- **Default**: `"720h"` (30 days)
- **Description**: How long to retain health monitoring events
- **Example**: `"720h"`, `"168h"`, `"720h"`

### `health.alerts.cooldown`
- **Type**: string
- **Default**: `"5m"`
- **Description**: Cooldown period between consecutive health alerts
- **Example**: `"1m"`, `"5m"`, `"10m"`

### `health.alerts.mqtt`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable MQTT publishing for health alerts
- **Example**: `true`, `false`

### `health.layer1.offline_threshold`
- **Type**: string
- **Default**: `"30s"`
- **Description**: Time without any data before a camera is considered offline
- **Example**: `"15s"`, `"30s"`, `"60s"`

### `health.layer2.bitrate_change_threshold`
- **Type**: float
- **Default**: 0.5
- **Range**: 0-1
- **Description**: Normalized bitrate change threshold for quality anomaly detection. A value of 0.5 means a 50% change triggers a quality event.
- **Example**: `0.3`, `0.5`, `0.8`

### `health.layer2.min_fps`
- **Type**: integer
- **Default**: 5
- **Description**: Minimum acceptable FPS for a healthy camera
- **Example**: `5`, `10`, `15`

### `health.layer2.max_idr_interval`
- **Type**: string
- **Default**: `"60s"`
- **Description**: Maximum allowed interval between IDR frames before triggering a health event
- **Example**: `"30s"`, `"60s"`, `"120s"`

### `health.layer2_5.freeze_timeout`
- **Type**: string
- **Default**: `"10s"`
- **Description**: Timeout for detecting a frozen video stream (streaming data but no motion)
- **Example**: `"5s"`, `"10s"`, `"30s"`

### `health.auto_remediation.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable automatic camera restart when health issues are detected
- **Example**: `true`, `false`

### `health.auto_remediation.max_restarts_per_hour`
- **Type**: integer
- **Default**: 3
- **Description**: Maximum number of automatic camera restarts per hour
- **Example**: `3`, `5`, `10`

### `health.auto_remediation.cooldown_minutes`
- **Type**: integer
- **Default**: 5
- **Description**: Cooldown in minutes between automatic remediation actions
- **Example**: `5`, `10`, `30`

### `health.auto_remediation.blacklist_hours`
- **Type**: integer
- **Default**: 1
- **Description**: Hours to blacklist a camera from auto-remediation after exceeding the restart limit
- **Example**: `1`, `2`, `24`

### `health.auto_remediation.global_max_per_min`
- **Type**: integer
- **Default**: 10
- **Description**: Global maximum number of remediation actions per minute across all cameras
- **Example**: `10`, `20`, `50`

### `health.auto_remediation.rediscovery_rescan_minutes`
- **Type**: integer
- **Default**: 5
- **Description**: While a camera is blacklisted, re-attempt IP rediscovery every N minutes. Without this, a camera that comes back online mid-blacklist (e.g. power restored) is not recovered until the full `blacklist_hours` elapses — rediscovery only scanned once at the blacklist moment. Each rescan is a bounded network sweep (≤30s, ≤16 parallel probes). Set to 0 to disable (legacy single-scan behavior).
- **Example**: `5`, `10`, `0` (disabled)

## Auto-Discover Configuration

When enabled, the NVR discovers ONVIF cameras joining the LAN in the background and enrolls them automatically — no manual "scan" button needed (Hikvision-NVR-style plug-and-play). **Off by default**; opt in explicitly.

### Operating modes

Two modes run in parallel:
- **Passive Hello listener** (`listen_for_hello`): a resident UDP 3702 multicast listener that reacts the instant a device announces itself via WS-Discovery Hello (zero latency).
- **Active periodic Probe** (`scan_interval`): a multicast Probe sweep every N seconds, as a fallback.

### Credential handling (activation_state)

After discovering a device, the NVR connects and classifies it:
- **Unauthenticated devices** (e.g. ESP32 MiBeeCam): activated immediately, recording starts right away.
- **Authenticated devices**: if `default_username`/`default_password` are configured and valid, activated immediately; otherwise marked **pending activation** (`activation_state: pending_activation`) — enrolled but **not recording**. The UI shows a "Pending Activation" badge; the user supplies credentials and clicks "Activate" to start recording.

### `auto_discover.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable background auto-discovery and auto-enrollment. Off by default to avoid enrolling devices on unfamiliar networks.
- **Example**: `true`

### `auto_discover.scan_interval`
- **Type**: integer (seconds)
- **Default**: `60`
- **Minimum**: `30` (values below 30 are clamped, to respect RPi-3B resources)
- **Description**: Active Probe sweep period. The passive Hello listener is unaffected by this value (it responds instantly).
- **Example**: `60`, `120`

### `auto_discover.listen_for_hello`
- **Type**: boolean
- **Default**: `true` (when auto_discover is enabled)
- **Description**: Enable the passive Hello listener (zero-latency discovery). Disable for active-sweep-only mode (lower resource, higher latency).
- **Example**: `true`, `false`

### `auto_discover.network_interface`
- **Type**: string
- **Default**: `""` (kernel default multicast interface)
- **Description**: Bind the discovery sockets to a specific NIC (e.g. `eth0`, `end0`). Set when the NVR is multi-homed and cameras live on a non-default interface, otherwise multicast may go out the wrong NIC.
- **Example**: `"eth0"`, `""`

### `auto_discover.default_username` / `default_password`
- **Type**: string
- **Default**: `""`
- **Description**: Default credentials tried against authenticated ONVIF devices during discovery. On success the device is activated immediately; on failure (or if blank) the device is added as pending activation.
- **Example**: `username: "admin"`, `password: "admin123"`

### `auto_discover.ignore_scopes`
- **Type**: list of strings
- **Default**: `[]`
- **Description**: Deny-list of ONVIF scope substrings. A device whose scopes contain any entry is skipped (never auto-added). Useful to exclude a specific hardware line.
- **Example**: `["hardware/LegacyCam"]`

### Full example

```yaml
auto_discover:
  enabled: true
  scan_interval: 60
  listen_for_hello: true
  network_interface: ""
  default_username: "admin"
  default_password: "admin123"
  ignore_scopes:
    - "hardware/LegacyCam"
```

> You can also configure this via **Settings → Features → Auto-Discover Cameras** in the Web UI (changes persist to YAML automatically). The password is never returned over the API (the UI only shows whether one is set).

## Remote Log Configuration

### `remote_log.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable remote log shipping (e.g. to VictoriaLogs)
- **Example**: `true`, `false`

### `remote_log.endpoint`
- **Type**: string
- **Required**: Yes (if enabled)
- **Description**: Remote log endpoint URL (e.g. VictoriaLogs insert URL)
- **Example**: `"http://localhost:9428/insert/jsonline"`

### `remote_log.format`
- **Type**: string
- **Default**: `"jsonline"`
- **Options**: `"jsonline"`, `"loki"`
- **Description**: Log shipping format
- **Example**: `"jsonline"`, `"loki"`

## AI Configuration

### `ai.inference_timeout_ms`
- **Type**: integer
- **Default**: 0 (no timeout)
- **Description**: Inference timeout in milliseconds for AI model execution
- **Example**: `5000`, `10000`, `30000`

### `ai.frame_skip_rate`
- **Type**: integer
- **Default**: 0 (process all frames)
- **Description**: Number of frames to skip between AI inference runs
- **Example**: `0`, `3`, `5`

### `ai.confidence_threshold`
- **Type**: float
- **Default**: 0.0
- **Range**: 0.0-1.0
- **Description**: Minimum confidence threshold for AI detection results
- **Example**: `0.5`, `0.7`, `0.9`

### `ai.model_path`
- **Type**: string
- **Optional**: Yes
- **Description**: Path to the ONNX model file for AI inference
- **Example**: `"/models/yolo.onnx"`

## RTMP Configuration

### `rtmp.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable RTMP ingest server for receiving push streams
- **Example**: `true`, `false`

### `rtmp.port`
- **Type**: integer
- **Default**: 1935
- **Range**: 1-65535
- **Description**: RTMP server listen port
- **Example**: `1935`, `1936`

### `rtmp.stream_keys`
- **Type**: map (string → string)
- **Optional**: Yes
- **Description**: Mapping of camera IDs to stream keys for RTMP authentication
- **Example**:
  ```yaml
  rtmp:
    stream_keys:
      cam1: "my-stream-key-1"
      cam2: "my-stream-key-2"
  ```

## SRT Configuration

### `srt.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable SRT listener for receiving MPEG-TS streams
- **Example**: `true`, `false`

### `srt.port`
- **Type**: integer
- **Default**: 9000
- **Range**: 1-65535
- **Description**: SRT listener listen port
- **Example**: `9000`, `9001`

### `srt.streams`
- **Type**: array of objects
- **Optional**: Yes
- **Description**: SRT stream mappings defining how incoming SRT streams are routed to cameras
- **Fields**:
  - **`camera_id`** (string, required) — Target camera ID
  - **`mode`** (string, required, options: `"listener"`/`"caller"`) — SRT mode
  - **`address`** (string, required for caller mode) — Remote SRT address
  - **`passphrase`** (string, optional) — AES encryption passphrase
  - **`stream_id`** (string, optional) — SRT stream ID for caller mode
- **Example**:
  ```yaml
  srt:
    streams:
      - camera_id: "cam1"
        mode: "listener"
      - camera_id: "cam2"
        mode: "caller"
        address: "192.168.1.100:9000"
  ```

## Metrics Auth Configuration

### `metrics_auth.username`
- **Type**: string
- **Optional**: Yes
- **Description**: Username for /metrics endpoint BasicAuth. When set (with password), the /metrics endpoint becomes authenticated; otherwise it stays public.
- **Example**: `"admin"`

### `metrics_auth.password`
- **Type**: string
- **Optional**: Yes
- **Description**: Password (plaintext) for /metrics endpoint BasicAuth. Mutually exclusive with password_hash.
- **Example**: `"metrics-password"`

### `metrics_auth.password_hash`
- **Type**: string
- **Optional**: Yes
- **Description**: bcrypt-hashed password for /metrics endpoint BasicAuth. Takes precedence over password.
- **Example**: `"$2a$10$..."`

## Transcoding Configuration

### `transcoding.enabled`
- **Type**: boolean
- **Default**: `false`
- **Description**: Enable FFmpeg-based transcoding globally
- **Example**: `true`, `false`

### `transcoding.ffmpeg_path`
- **Type**: string
- **Optional**: Yes
- **Description**: Path to FFmpeg binary. Auto-detected if not specified.
- **Example**: `"/usr/bin/ffmpeg"`

### `transcoding.max_workers`
- **Type**: integer
- **Default**: 1
- **Range**: 1-4
- **Description**: Maximum number of concurrent transcoding jobs
- **Example**: `1`, `2`, `4`

### `transcoding.download_url`
- **Type**: string
- **Optional**: Yes
- **Description**: URL to download FFmpeg binary from (auto-populated per platform)
- **Example**: `"https://github.com/.../ffmpeg"`

### `transcoding.job_timeout`
- **Type**: string
- **Default**: `"30m"`
- **Range**: 1s-4h
- **Description**: Per-job timeout for transcoding operations
- **Example**: `"10m"`, `"30m"`, `"1h"`

### `transcoding.history_retention`
- **Type**: string
- **Default**: `"168h"` (7 days)
- **Description**: How long to retain transcoding job history. Empty string means never delete.
- **Minimum**: 24h
- **Example**: `"168h"`, `"720h"`, `""`

## Extensions Configuration

`extensions` is a generic key-value map for external module configuration passthrough.
MiBeeNvr core does NOT read or validate these values.

### `extensions`

```yaml
extensions:
  # example_key: example_value
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `extensions` | `map[string]any` | `nil` | Generic passthrough for external module config. Core NVR does not read or validate. |

---

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

Combined protocol strings like `"rtsp_h264"` are rejected with a validation error since 0.10.0. Migrate to the separate `protocol` and `encoding` fields:

```yaml
# Old combined format (rejected since 0.10.0)
cameras:
  - id: "cam1"
    protocol: "rtsp_h264"
    url: "rtsp://..."

# Migrate to the new format:
# protocol: "rtsp"
# encoding: "h264"
```

## Validation Rules

The configuration is validated on startup with these constraints:

- **Camera IDs**: Must be unique across all cameras
- **Camera URLs**: Must have valid scheme (http/rtsp) and host
- **Camera Protocols**: Protocol/encoding combinations must be valid (rtsp+h264/h265/mjpeg, http+jpeg, onvif+h264/h265, xiaomi+h264/h265, timelapse)
- **ONVIF Cameras**: Must have either URL or onvif_endpoint
- **Xiaomi Cameras**: Must have xiaomi.token configured
- **Port Numbers**: Must be in range 1-65535
- **Segment Duration**: Maximum 30 seconds on RPi 3B
- **Retention Days**: Must be between 1 and 3650
- **Disk Threshold**: Must be between 50% and 99%
- **Merge Configuration**: All duration fields must be valid; min_segments_to_merge >= 2
- **HLS Configuration**:
  - Segment count: 3-10
  - Max streams: 1-20 (4 on RPi 3B)
  - Low-latency requires segment_count >= 7
  - Part min duration: 100ms-1s
- **Streaming Configuration**:
  - Default protocol must be one of: hls, ll-hls, webrtc, flv
  - WebRTC max viewers: 1-10
  - FLV max viewers: 1-50
- **WebSocket Configuration**: max_viewers > 0, write_buf_size > 0, idle_timeout > 0
- **Health Configuration**: All duration fields must be valid when health is enabled
- **Remote Log Configuration**: endpoint required when enabled; format must be jsonline or loki
- **Transcoding Configuration**: max_workers 1-4; job_timeout 1s-4h; history_retention >= 24h
- **SRT Configuration**: port 1-65535; stream mode must be listener or caller
- **Camera Health Overrides**: All duration fields must be valid; bitrate_change_threshold 0-1; min_fps >= 0
- **Camera Timelapse**: interval >= 1s; merge_mode must be auto/mp4/jpeg; merge_output_fps 1-60
- **Camera Transcoding**: target_codec must be h264 or h265; preset must be ultrafast/faster/medium

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
    audio_enabled: true
    transcoding:
      enabled: true
      target_codec: "h264"
      preset: "ultrafast"
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
  topic: "mibee"
webdav:
  enabled: true
  read_write: false
hls:
  max_streams: 4
  low_latency: false
streaming:
  webrtc:
    enabled: true
    max_viewers: 2
  flv:
    enabled: true
    max_viewers: 10
websocket:
  max_viewers: 10
health:
  enabled: true
  layer1:
    offline_threshold: "30s"
observability:
  log_level: "info"
```