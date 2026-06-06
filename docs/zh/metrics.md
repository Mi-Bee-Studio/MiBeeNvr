# Prometheus 指标

MiBee NVR 在 `/metrics` HTTP 端点暴露了一整套 Prometheus 指标，可用于监控、告警和仪表盘展示。

## 访问指标

### 端点

```
GET /metrics
```

**默认情况：** `/metrics` 是公开的（无需身份验证）。

**可选身份验证：** 在配置中设置 `metrics_auth`，为 metrics 端点配置独立的 BasicAuth 认证：

```yaml
metrics_auth:
  username: "metrics"
  password: "your_metrics_password"
```

### Prometheus 抓取配置

添加到你的 `prometheus.yml`：

```yaml
scrape_configs:
  - job_name: 'mibee-nvr'
    static_configs:
      - targets: ['localhost:9090']
    metrics_path: '/metrics'
    # 如果配置了 metrics_auth：
    basic_auth:
      username: 'metrics'
      password: 'your_metrics_password'
```

### 验证

```bash
# 公开端点
curl http://localhost:9090/metrics

# 需要认证的端点
curl -u metrics:password http://localhost:9090/metrics
```

## 指标概览

所有自定义指标均使用 `nvr_` 前缀。以下是按子系统分类的完整指标列表。

---

## 1. 录制指标

跟踪录制操作 — 片段创建、字节计数和活跃会话。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_active_cameras` | Gauge | — | 当前活跃摄像头数量（已连接且正在推流） |
| `nvr_active_recordings` | Gauge | — | 当前活跃的录制会话数 |
| `nvr_recording_bytes_total` | Counter | `camera_id`, `codec` | 写入录制片段的总字节数 |
| `nvr_segments_created_total` | Counter | `camera_id`, `codec` | 已创建的 MP4 片段总数 |
| `nvr_recording_count` | Gauge | — | 数据库中当前的录制条目数 |
| `nvr_recorder_ring_buffer_drops_total` | Counter | `camera_id` | 因录制器环形缓冲区溢出而丢弃的帧数 |

**`codec` 标签值：** `h264`, `h265`, `mjpeg`, `http_jpeg`, `timelapse`，或小米摄像头编码名称。

**用途：** 监控录制健康状态 — 丢帧率快速增长表明录制器无法跟上视频流。使用 `rate(nvr_recorder_ring_buffer_drops_total[5m])` 检测帧丢失。

---

## 2. 存储指标

跟踪磁盘使用情况和容量。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_storage_used_bytes` | Gauge | — | 录制文件占用的存储空间 |
| `nvr_storage_total_bytes` | Gauge | — | 可用总存储容量 |

**用途：** 设置 80%/90% 容量告警：

```promql
nvr_storage_used_bytes / nvr_storage_total_bytes > 0.8
```

---

## 3. 清理指标

跟踪保留策略和磁盘阈值清理操作。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_cleanup_deleted_total` | Counter | `reason` | 清理任务删除的录制文件总数 |

**`reason` 标签值：** `retention`（保留期过期）, `disk_threshold`（磁盘阈值）, `archive_retention`（归档保留）, `orphan`（孤立文件）。

**用途：** 监控清理活动：

```promql
rate(nvr_cleanup_deleted_total[1h])
```

---

## 4. HLS 流指标

跟踪 HLS 按需流媒体性能。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_hls_active_streams` | Gauge | `camera_id` | 当前活跃的 HLS 流数 |
| `nvr_hls_frames_dropped_total` | Counter | `camera_id` | 因缓冲区满而丢弃的 HLS 帧数 |
| `nvr_hls_write_errors_total` | Counter | `camera_id` | HLS muxer 写入错误次数 |
| `nvr_hls_muxer_restarts_total` | Counter | `camera_id` | HLS muxer 重启次数（写入错误后） |
| `nvr_hls_segment_size_bytes` | Histogram | `camera_id` | HLS 片段文件大小分布 |
| `nvr_hls_idle_evictions_total` | Counter | `camera_id` | 因空闲超时被驱逐的 HLS 流数 |

**`nvr_hls_segment_size_bytes` 桶区间：** 64KB, 128KB, 256KB, 512KB, 1MB, 2MB, 4MB, 8MB, 16MB。

**用途：**

```promql
# 各摄像头 HLS 帧丢弃率
rate(nvr_hls_frames_dropped_total[5m])

# 各摄像头 HLS 观看者数
nvr_hls_active_streams

# 片段大小 P90
histogram_quantile(0.9, rate(nvr_hls_segment_size_bytes_bucket[5m]))
```

---

## 5. WebRTC 流指标

跟踪 WebRTC WHEP 亚秒级延迟流媒体。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_webrtc_active_peers` | Gauge | `camera_id` | 活跃的 WebRTC PeerConnections 数 |
| `nvr_webrtc_frames_sent_total` | Counter | `camera_id` | 通过 WebRTC 成功发送的帧数 |
| `nvr_webrtc_frames_dropped_total` | Counter | `camera_id` | 因缓冲区满丢弃的 WebRTC 帧数 |
| `nvr_webrtc_connection_state_changes_total` | Counter | `camera_id`, `state` | WebRTC 连接状态转换次数 |

**`state` 标签值：** `new`, `connecting`, `connected`, `disconnected`, `failed`, `closed`。

**用途：**

```promql
# WebRTC 帧丢失率
rate(nvr_webrtc_frames_dropped_total[5m]) / (rate(nvr_webrtc_frames_sent_total[5m]) + rate(nvr_webrtc_frames_dropped_total[5m]))

# 连接失败次数
rate(nvr_webrtc_connection_state_changes_total{state="failed"}[5m])
```

---

## 6. HTTP-FLV 流指标

跟踪 HTTP-FLV 浏览器流媒体。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_flv_active_streams` | Gauge | `camera_id` | 活跃的 FLV 观看者数 |
| `nvr_flv_frames_sent_total` | Counter | `camera_id` | 发送给观看者的 FLV 帧数 |
| `nvr_flv_frames_dropped_total` | Counter | `camera_id` | 因缓冲区满丢弃的 FLV 帧数 |
| `nvr_flv_gop_cache_hits_total` | Counter | `camera_id` | FLV GOP 缓存命中（新观看者快速加入） |
| `nvr_flv_gop_cache_misses_total` | Counter | `camera_id` | FLV GOP 缓存未命中（新观看者，无缓存 GOP） |

**用途：**

```promql
# FLV 缓存命中率 — 高比率意味着观看者快速加入
rate(nvr_flv_gop_cache_hits_total[5m]) / (rate(nvr_flv_gop_cache_hits_total[5m]) + rate(nvr_flv_gop_cache_misses_total[5m]))
```

---

## 7. 小米摄像头指标

跟踪小米 CS2 P2P 摄像头连接稳定性。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_xiaomi_disconnects_total` | Counter | `camera_id`, `reason` | 小米摄像头断开连接次数 |
| `nvr_xiaomi_reconnects_total` | Counter | `camera_id` | 小米摄像头重新连接次数 |

**`reason` 标签值：** `network`（网络）, `eof`（流结束）, `idle_timeout`（空闲超时）。

**用途：**

```promql
# 不稳定的小米摄像头（断开率高）
rate(nvr_xiaomi_disconnects_total[15m]) > 0.1
```

---

## 8. 摄像头连接指标

跟踪通用摄像头连接健康状态和重连行为。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_camera_errors_total` | Counter | `camera_id`, `error_type` | 录制过程中的摄像头错误数 |
| `nvr_camera_connection_errors_total` | Counter | `camera_id`, `error_type` | 摄像头连接错误数（超时、认证、网络、未知） |
| `nvr_camera_reconnect_attempts_total` | Counter | `camera_id` | 摄像头重连尝试次数 |
| `nvr_camera_reconnect_backoff_seconds` | Gauge | `camera_id` | 当前重连退避时长 |

**`nvr_camera_connection_errors_total` 的 `error_type` 标签值：** `timeout`, `auth`, `network`, `unknown`。

**`nvr_camera_errors_total` 的 `error_type` 标签值：** 各个录制器报告的具体错误类型。

**用途：**

```promql
# 重连过于频繁的摄像头
rate(nvr_camera_reconnect_attempts_total[5m]) > 0.1

# 按类型的连接错误
rate(nvr_camera_connection_errors_total[5m])
```

---

## 9. StreamHub / 管道指标

跟踪内部帧分发管道。这些指标有助于诊断 StreamHub 分发系统中的瓶颈和帧丢失。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_streamhub_frames_in_total` | Counter | `camera_id` | 广播到 StreamHub 的帧总数 |
| `nvr_streamhub_frames_dropped_total` | Counter | `camera_id`, `consumer`, `is_idr` | StreamHub 丢弃的帧数（缓冲区满） |
| `nvr_streamhub_consumer_buffer_depth` | Gauge | `camera_id`, `consumer` | 每个消费者的当前缓冲区深度 |
| `nvr_frame_processing_duration_seconds` | Histogram | `camera_id`, `protocol` | 帧通过管道的处理时间（1:100 采样） |
| `nvr_jitter_buffer_depth` | Gauge | `camera_id` | 当前抖动缓冲区中的帧数 |
| `nvr_jitter_buffer_reorders_total` | Counter | `camera_id` | 检测到的乱序帧数 |

**`consumer` 标签值：** `hls`, `webrtc`, `flv`, `wsstream`, `recorder`, `ai` 等。

**`is_idr` 标签值：** `true`（IDR/关键帧）, `false`。

**`nvr_frame_processing_duration_seconds` 桶区间：** 1ms, 2ms, 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s。

**用途：**

```promql
# 各消费者的 StreamHub 帧丢弃率
rate(nvr_streamhub_frames_dropped_total[5m])

# 高消费者缓冲区深度（潜在瓶颈）
nvr_streamhub_consumer_buffer_depth > 100

# 帧处理延迟 P99
histogram_quantile(0.99, rate(nvr_frame_processing_duration_seconds_bucket[5m]))

# 抖动缓冲区活动 — 非零 = 存在乱序帧
nvr_jitter_buffer_depth
```

---

## 10. 健康 → Prometheus 桥接指标

从健康监控系统桥接的实时摄像头流质量指标。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_stream_fps` | Gauge | `camera_id` | 当前每秒帧数 |
| `nvr_stream_bitrate_kbps` | Gauge | `camera_id` | 当前流码率（kbps） |
| `nvr_stream_idr_interval_seconds` | Gauge | `camera_id` | 距离上一个 IDR（关键帧）的秒数 |

**用途：**

```promql
# 低 FPS 告警
nvr_stream_fps < 10

# IDR 过期 — 长时间未收到关键帧（可能流已冻结）
nvr_stream_idr_interval_seconds > 30

# 码率降至零（流已断开）
nvr_stream_bitrate_kbps == 0
```

---

## 11. 转码指标

跟踪 FFmpeg 转码任务。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_transcoding_jobs_total` | Counter | `codec_from`, `codec_to`, `status` | 按编码转换和结果分类的转码任务总数 |
| `nvr_transcoding_active_jobs` | Gauge | — | 当前正在运行的转码任务数 |
| `nvr_transcoding_duration_seconds` | Histogram | `codec_from`, `codec_to` | 完成的转码任务持续时间 |
| `nvr_transcoding_bytes_processed` | Counter | — | 转码处理的总字节数 |
| `nvr_transcoding_ffmpeg_status` | Gauge | — | FFmpeg 可用性：0=未安装, 1=下载中, 2=可用 |

**`status` 标签值：** `completed`, `failed`, `cancelled`。

**用途：**

```promql
# 转码失败率
rate(nvr_transcoding_jobs_total{status="failed"}[5m]) / rate(nvr_transcoding_jobs_total[5m])

# 活跃任务 vs 容量
nvr_transcoding_active_jobs

# FFmpeg 未安装告警
nvr_transcoding_ffmpeg_status == 0
```

---

## 12. 远程日志指标

跟踪远程日志发送（VictoriaLogs / Loki）。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_remote_log_sent_total` | Counter | — | 成功的远程日志批次发送次数 |
| `nvr_remote_log_dropped_total` | Counter | — | 因发送失败丢弃的远程日志批次数量 |
| `nvr_remote_log_batch_size` | Histogram | — | 远程日志批次大小分布 |

**`nvr_remote_log_batch_size` 桶区间：** 1, 2, 4, 8, 16, 32, 64, 128（指数分布）。

**用途：**

```promql
# 远程日志丢弃率
rate(nvr_remote_log_dropped_total[5m]) > 0
```

---

## 13. 内置运行时指标

除了自定义的 NVR 指标外，还注册了以下标准收集器：

### Go 运行时内存统计（针对 RPi 3B 精简）

所有标准 Go 内存指标均以 `go_*` 为前缀：内存使用量、goroutine 数量、GC 统计信息等。

仅启用了 `GoRuntimeMemStatsCollection`，以最小化资源受限设备上的开销。

### 进程收集器

| 指标 | 说明 |
|--------|-------------|
| `nvr_process_cpu_seconds_total` | 消耗的总 CPU 时间 |
| `nvr_process_open_fds` | 打开的文件描述符数 |
| `nvr_process_max_fds` | 最大文件描述符数 |
| `nvr_process_resident_memory_bytes` | 常驻内存大小 |
| `nvr_process_virtual_memory_bytes` | 虚拟内存大小 |
| `nvr_process_virtual_memory_max_bytes` | 最大虚拟内存 |

**用途：**

```promql
# 内存使用告警（RPi 3B：512MB 预算）
nvr_process_resident_memory_bytes > 500 * 1024 * 1024

# Goroutine 泄漏检测
go_goroutines > 500
```

---

## 示例：Grafana 面板查询

### 系统健康面板

```promql
# 运行时间 — 进程启动时间
time() - process_start_time_seconds{job="mibee-nvr"}

# 内存使用
nvr_process_resident_memory_bytes

# CPU 使用率
rate(nvr_process_cpu_seconds_total[1m])
```

### 摄像头概览面板

```promql
# 活跃摄像头
nvr_active_cameras

# 活跃录制
nvr_active_recordings

# 摄像头总数（来自录制计数）
nvr_recording_count
```

### 流媒体面板

```promql
# 各摄像头 HLS 观看者
nvr_hls_active_streams

# 各摄像头 WebRTC 观看者
nvr_webrtc_active_peers

# 各摄像头 FLV 观看者
nvr_flv_active_streams
```

### 质量面板

```promql
# 帧丢弃率最高的摄像头
topk(5, rate(nvr_streamhub_frames_dropped_total[5m]))

# 处理时间最慢的摄像头
topk(5, histogram_quantile(0.99, rate(nvr_frame_processing_duration_seconds_bucket[5m])))
```

---

## 指标类型说明

| 类型 | 行为 | 用途 |
|------|----------|----------|
| **Gauge** | 可上下浮动的单一数值 | 活跃流数、缓冲区深度、存储用量 |
| **Counter** | 单调递增（重启后重置） | 总字节数、发送帧数、错误数 |
| **Histogram** | 带计数/总和的分桶观测值 | 持续时间、大小分布 |

---

## 配置参考

`/metrics` 端点在 `cmd/mibee-nvr/main.go` 中使用 `promhttp.HandlerFor` 设置，错误处理方式为 `ContinueOnError`。它使用自定义的 Prometheus 注册表（而非全局默认注册表），以隔离 NVR 指标。

指标认证的配置字段：

```yaml
metrics_auth:
  username: "metrics"         # 必填
  password: "secret"          # 与 password_hash 互斥
  password_hash: "$2a$10$..." # bcrypt 哈希（密码的替代方式）
```

当未配置 `metrics_auth` 时，`/metrics` 端点无需认证即可访问。

要配置密码，使用 `mibee-nvr hash-password yourpassword` 生成 bcrypt 哈希，或将密码设置为明文（首次运行时自动转换）。
