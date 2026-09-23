# Prometheus Metrics

MiBee NVR exposes a comprehensive set of Prometheus metrics at the `/metrics` HTTP endpoint for monitoring, alerting, and dashboarding.

## Accessing Metrics

### Endpoint

```text
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
| `nvr_segment_write_duration_seconds` | Histogram | `camera_id` | Duration of MP4 segment file writes — SD-card degradation early warning |
| `nvr_recording_audit_total` | Counter | `camera_id`, `result` | Recording integrity audit outcomes (mediaprobe on closed segments) |
| `nvr_recording_deepcheck_total` | Counter | `camera_id`, `result` | Decode-level deep check outcomes (`ffmpeg -v error` sampling, ≤1/hour/camera, #489) — absent entirely when no FFmpeg is configured |

**`codec` label values:** `h264`, `h265`, `mjpeg`, `http_jpeg`, `timelapse`, or Xiaomi codec name.

**Buckets** for `nvr_segment_write_duration_seconds`: 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s.

**`result` label values:** `ok`, `zero_duration`, `probe_error` for `nvr_recording_audit_total`; `ok`, `decode_error` for `nvr_recording_deepcheck_total`.

**Usage:** Monitor recording health — a rapidly increasing drop rate indicates the recorder cannot keep up with the stream. Use `rate(nvr_recorder_ring_buffer_drops_total[5m])` to detect frame loss. A rising write-duration P99 means the storage medium is slowing down:

```promql
histogram_quantile(0.99, rate(nvr_segment_write_duration_seconds_bucket[5m])) > 1

# Recording audit anomalies (zero-duration segments / probe failures)
rate(nvr_recording_audit_total{result!="ok"}[1h]) > 0
```

---

## 2. Storage & I/O Budget Metrics

Track disk usage, capacity, and shared I/O budget billing (see [Performance Tuning](performance.md) for the budget mechanism and its tuning).

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_storage_used_bytes` | Gauge | — | Storage space consumed by recordings |
| `nvr_storage_total_bytes` | Gauge | — | Total storage capacity available |
| `nvr_storage_write_errors_total` | Counter | — | Total number of storage write I/O errors across all cameras |
| `nvr_iobudget_bytes_charged_total` | Counter | `consumer` | Bytes billed to the shared I/O budget, per consumer |
| `nvr_iobudget_unlinks_charged_total` | Counter | `consumer` | Files billed to the recursive-deletion unlink guardrail, per consumer (#755) |
| `nvr_iobudget_wait_seconds_total` | Counter | `consumer` | Cumulative seconds background work spent waiting on the shared I/O budget token bucket |

**`consumer` label values:** `merge`, `cleanup`, `repair`, `timelapse`, `transcode`, `offload` (S3 cold-archive uploads), plus `recording` / `playback` when the gray-release switches (`io.recording_writes_budgeted` / `io.playback_reads_budgeted`, off by default) are enabled.

**Usage:** Set alerts at 80%/90% thresholds:

```promql
nvr_storage_used_bytes / nvr_storage_total_bytes > 0.8
```

Alert on storage write errors — a sustained non-zero rate indicates failing storage:

```promql
rate(nvr_storage_write_errors_total[5m]) > 0
```

---

## 3. Cleanup Metrics

Track retention and disk threshold cleanup operations.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_cleanup_deleted_total` | Counter | `reason` | Total recordings deleted by cleanup jobs |
| `nvr_cleanup_duration_seconds` | Histogram | — | Cleanup cycle duration in seconds |

**`reason` label values:** `retention`, `disk_threshold`, `archive_retention`, `orphan`.

**Buckets** for `nvr_cleanup_duration_seconds`: 1s, 5s, 10s, 30s, 60s, 300s, 600s.

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

## 7. WebSocket Streaming Metrics

Track WebSocket low-latency live streaming (the WASM decode pipeline).

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_ws_active_streams` | Gauge | `camera_id` | Number of currently active WebSocket streams |
| `nvr_ws_frames_sent_total` | Counter | `camera_id` | Frames successfully sent over WebSocket |
| `nvr_ws_frames_dropped_total` | Counter | `camera_id` | WebSocket frames dropped due to buffer full |

**Usage:**

```promql
# WebSocket frame loss rate
rate(nvr_ws_frames_dropped_total[5m]) / (rate(nvr_ws_frames_sent_total[5m]) + rate(nvr_ws_frames_dropped_total[5m]))
```

---

## 8. Xiaomi Camera Metrics

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

## 9. Camera Connection Metrics

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

## 10. StreamHub / Pipeline Metrics

Track the internal frame distribution pipeline. These metrics help diagnose bottlenecks and frame loss in the StreamHub fan-out system.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_streamhub_frames_in_total` | Counter | `camera_id` | Total frames broadcast into StreamHub |
| `nvr_streamhub_frames_dropped_total` | Counter | `camera_id`, `consumer`, `is_idr` | Frames dropped by StreamHub (buffer full) |
| `nvr_streamhub_consumer_buffer_depth` | Gauge | `camera_id`, `consumer` | Current buffer depth for each consumer |
| `nvr_streamhub_frames_sent_total` | Counter | `camera_id`, `consumer` | Total frames delivered to each hub consumer (periodic flush from hub atomics) |
| `nvr_streamhub_bytes_in_total` | Counter | `camera_id` | Total video bytes broadcast into StreamHub (periodic flush) |
| `nvr_streamhub_drop_rate_exceeded_total` | Counter | `camera_id`, `consumer` | Times a consumer's drop rate crossed the warn threshold |
| `nvr_streamhub_hop_dwell_ms_avg` | Gauge | `camera_id`, `consumer` | Average enqueue→drain dwell in a consumer's queue (ms) |
| `nvr_streamhub_hop_dwell_ms_max` | Gauge | `camera_id`, `consumer` | Maximum enqueue→drain dwell in a consumer's queue (ms) |
| `nvr_jitter_buffer_depth` | Gauge | `camera_id` | Current jitter buffer frame count |
| `nvr_jitter_buffer_reorders_total` | Counter | `camera_id` | Out-of-order frames detected |
| `nvr_jitter_buffer_flushes_total` | Counter | `camera_id` | Jitter buffer flushes (capacity or timeout reached) |
| `nvr_audio_frames_total` | Counter | `camera_id`, `codec` | Total audio frames broadcast into StreamHub |
| `nvr_audio_frames_dropped_total` | Counter | `camera_id` | Audio frames dropped due to buffer overflow |

**`consumer` label values:** `hls`, `webrtc`, `flv`, `wsstream`, `recorder`, `ai`, etc.

**`is_idr` label values:** `true` (IDR/key frame), `false`.

**Usage:**

```promql
# StreamHub frame drop rate per consumer
rate(nvr_streamhub_frames_dropped_total[5m])

# High consumer buffer depth (potential bottleneck)
nvr_streamhub_consumer_buffer_depth > 100

# Delivered vs dropped frames per consumer
rate(nvr_streamhub_frames_sent_total[5m])

# Per-hop dwell — a high max means that consumer's queue is backing up
nvr_streamhub_hop_dwell_ms_max

# Jitter buffer activity — non-zero = out-of-order frames
nvr_jitter_buffer_depth
```

---

## 11. Playback Quality Metrics

Track player-reported end-to-end live latency and stalls (telemetry relayed over the WS channel; covers all live protocols).

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_playback_live_latency_ms` | Gauge | `camera_id`, `protocol` | Player-reported end-to-end live latency (ms): hub ingest wallclock relayed via WS vs. browser clock |
| `nvr_playback_stalls_total` | Counter | `camera_id`, `protocol` | Player-reported playback stalls (buffering/freezes) |

**Usage:**

```promql
# Live latency by protocol
nvr_playback_live_latency_ms

# Stall rate — sustained non-zero means bandwidth or pipeline trouble
rate(nvr_playback_stalls_total[5m]) > 0
```

---

## 12. Health → Prometheus Bridge Metrics

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

## 13. Transcoding Metrics

Track FFmpeg transcoding jobs.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_transcoding_jobs_total` | Counter | `codec_from`, `codec_to`, `status` | Total transcoding jobs by codec conversion and result |
| `nvr_transcoding_active_jobs` | Gauge | — | Currently running transcoding jobs |
| `nvr_transcoding_duration_seconds` | Histogram | `codec_from`, `codec_to` | Duration of completed transcoding jobs |
| `nvr_transcoding_bytes_processed` | Counter | — | Total bytes processed by transcoding |
| `nvr_transcoding_ffmpeg_status` | Gauge | — | FFmpeg availability: 0=not_installed, 1=downloading, 2=available |
| `nvr_relay_transcoder_temperature_c` | Gauge | — | Current transcoder thermal zone temperature in Celsius |
| `nvr_relay_transcoder_restarts_total` | Counter | — | Total number of transcoder restarts |
| `nvr_relay_transcoder_thermal_throttles_total` | Counter | — | Total number of thermal throttle events that caused preset downgrade |

**`status` label values:** `completed`, `failed`, `cancelled`.

**Note:** The `nvr_relay_transcoder_*` metrics are emitted by the live relay transcoder (`internal/livetranscode`) and registered separately from the central `internal/metrics` registry.

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

## 14. Remote Log Metrics

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

## 15. Codec Probe Metrics

Observe the codec-detection pipeline (recorder probe → DB persist → `/protocols` → orchestrator → player). The H.265 chain is the project's largest complexity and defect source — issue #112 (H.265 black screen) was a **silent probe failure**. These metrics make probe outcome and latency visible so stale-encoding problems surface before users see black video.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_codec_probe_total` | Counter | `camera_id`, `encoding`, `result` | Codec probes by resolved encoding and outcome |
| `nvr_codec_probe_duration_seconds` | Histogram | `camera_id` | Latency of the RTSP DESCRIBE codec probe |
| `nvr_resolved_encoding` | Gauge | `camera_id`, `encoding` | Last codec resolved and persisted (value always `1`; the `encoding` label is the signal) |

**`result` label values for `nvr_codec_probe_total`:**
- `ok` — the live RTSP DESCRIBE resolved the codec (authoritative)
- `unsupported` — the probe returned empty; fell back to a claimed/default encoding (the device works but its codec wasn't verified from the live stream)
- `fail` — probe empty **and** no claim; defaulted to H264. Highest stale-encoding risk (see #112).

**`encoding` label values:** `h264`, `h265`, `mjpeg`, `jpeg` (lowercase, normalized — `MJPEG`→`jpeg`).

**Usage / alerts:**

```promql
# Stale-encoding suspect: a single camera's probe fails >50% over 5 min
sum(rate(nvr_codec_probe_total{result="fail"}[5m]))
  by (camera_id)
  / sum(rate(nvr_codec_probe_total[5m])) by (camera_id) > 0.5

# Slow/unreachable devices: high probe latency p99
histogram_quantile(0.99, sum(rate(nvr_codec_probe_duration_seconds_bucket[5m])) by (le, camera_id)) > 2.5

# Persisted encoding drift: nvr_resolved_encoding disagrees with the live codec
# reported by other labels/paths — investigate as a persistence bug.
```

**Notes:** Emitted from `ONVIFRecorder.detectEncoding()` (counter + duration) and `ensureEncoding` (resolved gauge). Probe duration is the RTSP DESCRIBE round-trip only — it excludes ONVIF profile fallback. Histogram buckets: `0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10` seconds.

---

## 16. Built-in Runtime Metrics

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

## 17. Merge Metrics

Track recording segment merge operations — both batch merges and the quasi-real-time rolling merge.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_merge_attempts_total` | Counter | — | Total number of merge attempts |
| `nvr_merge_successes_total` | Counter | — | Total number of successful merges |
| `nvr_merge_failures_total` | Counter | `reason` | Total number of failed merges, partitioned by reason |
| `nvr_merge_duration_seconds` | Histogram | — | Duration of merge operations in seconds |
| `nvr_merge_size_bytes` | Histogram | — | Size of merged output in bytes |
| `nvr_merge_pending_segments` | Gauge | `camera_id` | Number of segments pending merge, partitioned by camera |
| `nvr_rolling_merge_latency_seconds` | Histogram | `camera_id` | Time from segment close to rolling merge completion |
| `nvr_rolling_merge_bucket_segments` | Gauge | `camera_id` | Segments accumulated in the current rolling merge window bucket |
| `nvr_rolling_merge_bucket_finalized_total` | Counter | `reason` | Rolling buckets leaving the retained set (#764). `reason`: `idle_ttl` / `capacity_lru` / `size_limit` (mdat cap roll) / `batch_reset` (batch merge paths drop the whole set) |
| `nvr_rolling_merge_bucket_lifetime_seconds` | Histogram | `reason` | Wall time from bucket creation to finalize (seconds) — should lengthen markedly once a quality-oscillating camera stops micro-merging |

**Buckets** for `nvr_merge_duration_seconds`: 0.5s, 1s, 5s, 10s, 30s, 60s, 300s, 600s. For `nvr_merge_size_bytes`: 10MB, 50MB, 100MB, 500MB, 1GB, 3GB. For `nvr_rolling_merge_latency_seconds`: 0.1s, 0.5s, 1s, 2s, 5s, 10s, 30s. For `nvr_rolling_merge_bucket_lifetime_seconds`: 1s, 5s, 15s, 1m, 5m, 15m, 1h, 6h.

**Usage:**

```promql
# Merge failure rate
rate(nvr_merge_failures_total[5m]) / rate(nvr_merge_attempts_total[5m])

# Rolling merge latency P99 (segment close → merged)
histogram_quantile(0.99, rate(nvr_rolling_merge_latency_seconds_bucket[5m]))

# Backlog of unmerged segments per camera
nvr_merge_pending_segments

# Bucket eviction rate by reason (reconnect-storm cameras should fall from high finalize rates)
sum by (reason) (rate(nvr_rolling_merge_bucket_finalized_total[1h]))
```

---

## 18. SQLite Database Metrics

Health metrics for the SQLite metadata database — writer pool, read-only pool, and file-level health.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_sqlite_open_connections` | Gauge | — | SQLite open connections from the writer connection pool |
| `nvr_sqlite_in_use_connections` | Gauge | — | SQLite in-use connections from the writer connection pool |
| `nvr_sqlite_read_open_connections` | Gauge | — | SQLite open connections from the read-only pool (query_only, concurrent with the writer under WAL) |
| `nvr_sqlite_read_in_use_connections` | Gauge | — | SQLite in-use connections from the read-only pool |
| `nvr_sqlite_read_wait_count_total` | Counter | — | Total number of times the read pool had no connection available and the caller waited (sustained growth means `SetReadPoolSize` should be raised) |
| `nvr_sqlite_read_wait_duration_seconds` | Gauge | — | Total seconds callers waited for a read-pool connection (cumulative since start) |
| `nvr_sqlite_wal_size_bytes` | Gauge | — | SQLite WAL file size in bytes |
| `nvr_sqlite_db_size_bytes` | Gauge | — | SQLite database file size in bytes |
| `nvr_sqlite_fragmentation_ratio` | Gauge | — | SQLite fragmentation ratio (freelist_count / page_count) |
| `nvr_sqlite_query_duration_seconds` | Histogram | `query_name` | SQLite query duration in seconds, partitioned by query name |
| `nvr_sqlite_txns_total` | Counter | `source` | Write transactions counted by source (`recording_insert` / `recording_close` / `merge_status` / `ai_event` / `health` / `api_write` / `cleanup` / `repair`) — the write-hotspot ranking (#759) |
| `nvr_sqlite_txn_duration_seconds` | Histogram | `source` | Write-transaction latency in seconds by source (#759) |
| `nvr_sqlite_busy_errors_total` | Counter | — | Total SQLITE_BUSY errors retried across all database operations |
| `nvr_memlimit_bytes` | Gauge | — | Go runtime soft memory limit (GOMEMLIMIT) installed at startup; 0 = not set (env var won / disabled by config / host memory unknown) (#756) |

**Buckets** for `nvr_sqlite_query_duration_seconds`: 1ms, 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s.

**Buckets** for `nvr_sqlite_txn_duration_seconds`: 0.5ms, 1ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s, 5s.

**Usage:**

```promql
# Slow queries by name, P99
histogram_quantile(0.99, rate(nvr_sqlite_query_duration_seconds_bucket[5m]))

# Read pool starved — raise SetReadPoolSize
rate(nvr_sqlite_read_wait_count_total[5m]) > 0

# Lock contention
rate(nvr_sqlite_busy_errors_total[5m]) > 0
```

---

## 19. Authentication Metrics

Track login attempts for security monitoring.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_auth_attempts_total` | Counter | `result` | Total authentication attempts, partitioned by result |
| `nvr_auth_rate_limited_total` | Counter | — | Total requests blocked by auth rate limiter |

**`result` label values:** `success`, `failure`, `no_password`.

**Usage:**

```promql
# Brute-force detection — high failure rate
rate(nvr_auth_attempts_total{result="failure"}[5m]) > 0.1

# Rate limiter activity
rate(nvr_auth_rate_limited_total[5m])
```

---

## 20. AI Event Metrics

Track AI events received from the external MiBeeVision backend.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_ai_events_received_total` | Counter | `camera_id`, `event_type` | Total AI events received from MiBeeVision, partitioned by camera and event type |
| `nvr_ai_events_errors_total` | Counter | — | Total errors when receiving or processing AI events |

**Usage:**

```promql
# AI pipeline health — events flowing per camera
rate(nvr_ai_events_received_total[5m])

# Ingest errors
rate(nvr_ai_events_errors_total[5m]) > 0
```

---

## 21. Timeline Metrics

Track DVR-style timeline seek operations during recording browsing.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_timeline_seeks_total` | Counter | `camera_id`, `type` | Total timeline seek operations, partitioned by camera and seek type |

**`type` label values:** `segment`, `intra`.

**Usage:**

```promql
# Seek hot spots per camera
topk(5, sum(rate(nvr_timeline_seeks_total[1h])) by (camera_id))
```

---

## 22. Pixel Activity Gate Metrics

Track the adaptive-recording pixel-activity gate (pixgate) sampler telemetry (#699). journald rotates logs away under disk pressure, so these metrics are the durable observation surface for sampler health.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_pixgate_samples_total` | Counter | `camera_id`, `source` | Samples processed. `source`: `hub` (main-stream mirror) / `rtsp` (dedicated sub-stream pull) |
| `nvr_pixgate_triggers_total` | Counter | `camera_id` | Confirmed-activity full-rate exit invocations (every active sample re-arms the hold — 1:1 with Trigger calls) |
| `nvr_pixgate_last_area_pct` | Gauge | `camera_id` | Largest foreground blob area of the most recent sample (% of grid) |
| `nvr_pixgate_last_sample_timestamp_seconds` | Gauge | `camera_id` | Unix timestamp of the most recent sample — the sampler heartbeat |

**Usage:** a stale heartbeat means the sampler is dead; a fresh heartbeat with low `area_pct` means the scene is quiet. This resolves the post-hoc "stuck in timelapse" diagnosis blind spot where the two were indistinguishable in logs.

```promql
# Sampler heartbeat staleness alert (no sample for 5 minutes)
time() - nvr_pixgate_last_sample_timestamp_seconds > 300

# Trigger frequency per camera
topk(5, sum(rate(nvr_pixgate_triggers_total[1h])) by (camera_id))
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

# WebSocket viewers per camera
nvr_ws_active_streams
```

### Quality Panel

```promql
# Top cameras by frame drop rate
topk(5, rate(nvr_streamhub_frames_dropped_total[5m]))

# Slow cameras by per-hop dwell (pipeline bottleneck)
topk(5, nvr_streamhub_hop_dwell_ms_max)
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
