# Prometheus Metrics

MiBee NVR exposes a comprehensive set of Prometheus metrics at the `/metrics` HTTP endpoint for monitoring, alerting, and dashboarding.

## Accessing Metrics

### Endpoint

```
GET /metrics
```

**Default:** `/metrics` is public (no authentication required).

**Optional Authentication:** Set `metrics_auth` in the config to protect the metrics endpoint with separate BasicAuth credentials:

```yaml
metrics_auth:
  username: "metrics"
  password: "your_metrics_password"
```

### Scrape Configuration

Add to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'mibee-nvr'
    static_configs:
      - targets: ['localhost:9090']
    metrics_path: '/metrics'
    # If metrics_auth is configured:
    basic_auth:
      username: 'metrics'
      password: 'your_metrics_password'
```

### Verify

```bash
# Public endpoint
curl http://localhost:9090/metrics

# Authenticated endpoint
curl -u metrics:password http://localhost:9090/metrics
```

## Metrics Overview

All custom metrics use the `nvr_` prefix. Below is the complete catalog organized by subsystem.

---

## 1. Recording Metrics

Track recording operations — segment creation, byte counts, and active sessions.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_active_cameras` | Gauge | — | Number of currently active cameras (connected and streaming) |
| `nvr_active_recordings` | Gauge | — | Number of currently active recording sessions |
| `nvr_recording_bytes_total` | Counter | `camera_id`, `codec` | Total bytes written to recording segments |
| `nvr_segments_created_total` | Counter | `camera_id`, `codec` | Total MP4 segments created |
| `nvr_recording_count` | Gauge | — | Current number of recording entries in the database |
| `nvr_recorder_ring_buffer_drops_total` | Counter | `camera_id` | Frames dropped due to recorder ring buffer overflow |

**`codec` label values:** `h264`, `h265`, `mjpeg`, `http_jpeg`, `timelapse`, or Xiaomi codec name.

**Usage:** Monitor recording health — a rapidly increasing drop rate indicates the recorder cannot keep up with the stream. Use `rate(nvr_recorder_ring_buffer_drops_total[5m])` to detect frame loss.

---

## 2. Storage Metrics

Track disk usage and capacity.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_storage_used_bytes` | Gauge | — | Storage space consumed by recordings |
| `nvr_storage_total_bytes` | Gauge | — | Total storage capacity available |

**Usage:** Set alerts at 80%/90% thresholds:

```promql
nvr_storage_used_bytes / nvr_storage_total_bytes > 0.8
```

---

## 3. Cleanup Metrics

Track retention and disk threshold cleanup operations.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_cleanup_deleted_total` | Counter | `reason` | Total recordings deleted by cleanup jobs |

**`reason` label values:** `retention`, `disk_threshold`, `archive_retention`, `orphan`.

**Usage:** Monitor cleanup activity:

```promql
rate(nvr_cleanup_deleted_total[1h])
```

---

## 4. HLS Streaming Metrics

Track HLS on-demand streaming performance.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_hls_active_streams` | Gauge | `camera_id` | Number of currently active HLS streams |
| `nvr_hls_frames_dropped_total` | Counter | `camera_id` | HLS frames dropped due to buffer full |
| `nvr_hls_write_errors_total` | Counter | `camera_id` | HLS muxer write errors |
| `nvr_hls_muxer_restarts_total` | Counter | `camera_id` | HLS muxer restarts after write errors |
| `nvr_hls_segment_size_bytes` | Histogram | `camera_id` | Distribution of HLS segment file sizes |
| `nvr_hls_idle_evictions_total` | Counter | `camera_id` | HLS streams evicted due to idle timeout |

**Buckets** for `nvr_hls_segment_size_bytes`: 64KB, 128KB, 256KB, 512KB, 1MB, 2MB, 4MB, 8MB, 16MB.

**Usage:**

```promql
# HLS frame drop rate per camera
rate(nvr_hls_frames_dropped_total[5m])

# Active HLS viewers per camera
nvr_hls_active_streams

# Segment size P90
histogram_quantile(0.9, rate(nvr_hls_segment_size_bytes_bucket[5m]))
```

---

## 5. WebRTC Streaming Metrics

Track WebRTC WHEP sub-second latency streaming.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_webrtc_active_peers` | Gauge | `camera_id` | Active WebRTC PeerConnections |
| `nvr_webrtc_frames_sent_total` | Counter | `camera_id` | Frames successfully sent via WebRTC |
| `nvr_webrtc_frames_dropped_total` | Counter | `camera_id` | Frames dropped due to buffer full |
| `nvr_webrtc_connection_state_changes_total` | Counter | `camera_id`, `state` | WebRTC connection state transitions |

**`state` label values:** `new`, `connecting`, `connected`, `disconnected`, `failed`, `closed`.

**Usage:**

```promql
# WebRTC frame loss rate
rate(nvr_webrtc_frames_dropped_total[5m]) / (rate(nvr_webrtc_frames_sent_total[5m]) + rate(nvr_webrtc_frames_dropped_total[5m]))

# Connection failures
rate(nvr_webrtc_connection_state_changes_total{state="failed"}[5m])
```

---

## 6. HTTP-FLV Streaming Metrics

Track HTTP-FLV browser streaming.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_flv_active_streams` | Gauge | `camera_id` | Active FLV viewers |
| `nvr_flv_frames_sent_total` | Counter | `camera_id` | FLV frames sent to viewers |
| `nvr_flv_frames_dropped_total` | Counter | `camera_id` | FLV frames dropped due to buffer full |
| `nvr_flv_gop_cache_hits_total` | Counter | `camera_id` | FLV GOP cache hits (rapid join for new viewers) |
| `nvr_flv_gop_cache_misses_total` | Counter | `camera_id` | FLV GOP cache misses (new viewer, no cached GOP) |

**Usage:**

```promql
# FLV cache hit rate — high ratio means fast viewer joins
rate(nvr_flv_gop_cache_hits_total[5m]) / (rate(nvr_flv_gop_cache_hits_total[5m]) + rate(nvr_flv_gop_cache_misses_total[5m]))
```

---

## 7. Xiaomi Camera Metrics

Track Xiaomi CS2 P2P camera connection stability.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_xiaomi_disconnects_total` | Counter | `camera_id`, `reason` | Xiaomi camera disconnections |
| `nvr_xiaomi_reconnects_total` | Counter | `camera_id` | Xiaomi camera reconnections |

**`reason` label values:** `network`, `eof`, `idle_timeout`.

**Usage:**

```promql
# Unstable Xiaomi cameras (high disconnect rate)
rate(nvr_xiaomi_disconnects_total[15m]) > 0.1
```

---

## 8. Camera Connection Metrics

Track general camera connection health and reconnection behavior.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_camera_errors_total` | Counter | `camera_id`, `error_type` | Camera errors during recording |
| `nvr_camera_connection_errors_total` | Counter | `camera_id`, `error_type` | Camera connection errors (timeout, auth, network, unknown) |
| `nvr_camera_reconnect_attempts_total` | Counter | `camera_id` | Camera reconnection attempts |
| `nvr_camera_reconnect_backoff_seconds` | Gauge | `camera_id` | Current reconnect backoff duration |

**`error_type` label values for `nvr_camera_connection_errors_total`:** `timeout`, `auth`, `network`, `unknown`.

**`error_type` label values for `nvr_camera_errors_total`:** Protocol-specific error types reported by individual recorders.

**Usage:**

```promql
# Cameras with excessive reconnect attempts
rate(nvr_camera_reconnect_attempts_total[5m]) > 0.1

# Connection errors by type
rate(nvr_camera_connection_errors_total[5m])
```

---

## 9. StreamHub / Pipeline Metrics

Track the internal frame distribution pipeline. These metrics help diagnose bottlenecks and frame loss in the StreamHub fan-out system.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_streamhub_frames_in_total` | Counter | `camera_id` | Total frames broadcast into StreamHub |
| `nvr_streamhub_frames_dropped_total` | Counter | `camera_id`, `consumer`, `is_idr` | Frames dropped by StreamHub (buffer full) |
| `nvr_streamhub_consumer_buffer_depth` | Gauge | `camera_id`, `consumer` | Current buffer depth for each consumer |
| `nvr_frame_processing_duration_seconds` | Histogram | `camera_id`, `protocol` | Frame processing time through the pipeline (1:100 sampling) |
| `nvr_jitter_buffer_depth` | Gauge | `camera_id` | Current jitter buffer frame count |
| `nvr_jitter_buffer_reorders_total` | Counter | `camera_id` | Out-of-order frames detected |

**`consumer` label values:** `hls`, `webrtc`, `flv`, `wsstream`, `recorder`, `ai`, etc.

**`is_idr` label values:** `true` (IDR/key frame), `false`.

**Buckets** for `nvr_frame_processing_duration_seconds`: 1ms, 2ms, 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s.

**Usage:**

```promql
# StreamHub frame drop rate per consumer
rate(nvr_streamhub_frames_dropped_total[5m])

# High consumer buffer depth (potential bottleneck)
nvr_streamhub_consumer_buffer_depth > 100

# Frame processing latency P99
histogram_quantile(0.99, rate(nvr_frame_processing_duration_seconds_bucket[5m]))

# Jitter buffer activity — non-zero = out-of-order frames
nvr_jitter_buffer_depth
```

---

## 10. Health → Prometheus Bridge Metrics

Real-time camera stream quality metrics bridged from the health monitoring system.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_stream_fps` | Gauge | `camera_id` | Current frames per second |
| `nvr_stream_bitrate_kbps` | Gauge | `camera_id` | Current stream bitrate in kbps |
| `nvr_stream_idr_interval_seconds` | Gauge | `camera_id` | Seconds since last IDR (key) frame |

**Usage:**

```promql
# Low FPS alert
nvr_stream_fps < 10

# Stale IDR — no keyframe received recently (potential stream freeze)
nvr_stream_idr_interval_seconds > 30

# Bitrate drops to zero (stream disconnected)
nvr_stream_bitrate_kbps == 0
```

---

## 11. Transcoding Metrics

Track FFmpeg transcoding jobs.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_transcoding_jobs_total` | Counter | `codec_from`, `codec_to`, `status` | Total transcoding jobs by codec conversion and result |
| `nvr_transcoding_active_jobs` | Gauge | — | Currently running transcoding jobs |
| `nvr_transcoding_duration_seconds` | Histogram | `codec_from`, `codec_to` | Duration of completed transcoding jobs |
| `nvr_transcoding_bytes_processed` | Counter | — | Total bytes processed by transcoding |
| `nvr_transcoding_ffmpeg_status` | Gauge | — | FFmpeg availability: 0=not_installed, 1=downloading, 2=available |

**`status` label values:** `completed`, `failed`, `cancelled`.

**Usage:**

```promql
# Transcoding failure rate
rate(nvr_transcoding_jobs_total{status="failed"}[5m]) / rate(nvr_transcoding_jobs_total[5m])

# Active jobs vs capacity
nvr_transcoding_active_jobs

# FFmpeg not installed alert
nvr_transcoding_ffmpeg_status == 0
```

---

## 12. Remote Log Metrics

Track remote log shipping (VictoriaLogs / Loki).

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_remote_log_sent_total` | Counter | — | Successful remote log batch sends |
| `nvr_remote_log_dropped_total` | Counter | — | Remote log batches dropped due to send failure |
| `nvr_remote_log_batch_size` | Histogram | — | Distribution of remote log batch sizes |

**Buckets** for `nvr_remote_log_batch_size`: 1, 2, 4, 8, 16, 32, 64, 128 (exponential).

**Usage:**

```promql
# Remote log drop rate
rate(nvr_remote_log_dropped_total[5m]) > 0
```

---

## 13. Built-in Runtime Metrics

In addition to custom NVR metrics, these standard collectors are registered:

### Go Runtime Memory Stats (limited for RPi 3B)

All standard Go memory metrics prefixed with `go_*`: memory usage, goroutine count, GC statistics, etc.

Only `GoRuntimeMemStatsCollection` is enabled to minimize overhead on resource-constrained devices.

### Process Collector

| Metric | Description |
|--------|-------------|
| `nvr_process_cpu_seconds_total` | Total CPU time consumed |
| `nvr_process_open_fds` | Number of open file descriptors |
| `nvr_process_max_fds` | Maximum file descriptors |
| `nvr_process_resident_memory_bytes` | Resident memory size |
| `nvr_process_virtual_memory_bytes` | Virtual memory size |
| `nvr_process_virtual_memory_max_bytes` | Maximum virtual memory |

**Usage:**

```promql
# Memory usage alert (RPi 3B: 512MB budget)
nvr_process_resident_memory_bytes > 500 * 1024 * 1024

# Goroutine leak detection
go_goroutines > 500
```

---

## Example: Grafana Dashboard Queries

### System Health Panel

```promql
# Uptime — process start time
time() - process_start_time_seconds{job="mibee-nvr"}

# Memory usage
nvr_process_resident_memory_bytes

# CPU usage rate
rate(nvr_process_cpu_seconds_total[1m])
```

### Camera Overview Panel

```promql
# Active cameras
nvr_active_cameras

# Active recordings
nvr_active_recordings

# Total cameras (from recording count)
nvr_recording_count
```

### Streaming Panel

```promql
# HLS viewers per camera
nvr_hls_active_streams

# WebRTC viewers per camera
nvr_webrtc_active_peers

# FLV viewers per camera
nvr_flv_active_streams
```

### Quality Panel

```promql
# Top cameras by frame drop rate
topk(5, rate(nvr_streamhub_frames_dropped_total[5m]))

# Slow cameras by processing time
topk(5, histogram_quantile(0.99, rate(nvr_frame_processing_duration_seconds_bucket[5m])))
```

---

## Legend: Metric Types

| Type | Behavior | Use Case |
|------|----------|----------|
| **Gauge** | Single value that can go up and down | Active streams, buffer depth, storage usage |
| **Counter** | Monotonically increasing (resets on restart) | Total bytes, frames sent, errors |
| **Histogram** | Bucketed observations with count/sum | Duration, size distribution |

---

## Configuration Reference

The `/metrics` endpoint is set up in `cmd/mibee-nvr/main.go` using `promhttp.HandlerFor` with `ContinueOnError` error handling. It uses a custom Prometheus registry (not the global default) to isolate NVR metrics.

Config field for metrics authentication:

```yaml
metrics_auth:
  username: "metrics"         # Required
  password: "secret"          # Mutually exclusive with password_hash
  password_hash: "$2a$10$..." # bcrypt hash (alternative to password)
```

When `metrics_auth` is not configured, the `/metrics` endpoint is accessible without authentication.

To configure, use `mibee-nvr hash-password yourpassword` to generate a bcrypt hash, or set the password as plaintext (auto-converted on first run).
