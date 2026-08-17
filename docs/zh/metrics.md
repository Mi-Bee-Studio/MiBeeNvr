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
| `nvr_storage_write_errors_total` | Counter | — | 所有摄像头的存储写入 I/O 错误总数 |

**用途：** 设置 80%/90% 容量告警：

```promql
nvr_storage_used_bytes / nvr_storage_total_bytes > 0.8
```

存储写入错误告警 — 速率持续非零表明存储可能出现故障：

```promql
rate(nvr_storage_write_errors_total[5m]) > 0
```

---

## 3. 清理指标

跟踪保留策略和磁盘阈值清理操作。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_cleanup_deleted_total` | Counter | `reason` | 清理任务删除的录制文件总数 |
| `nvr_cleanup_duration_seconds` | Histogram | — | 清理周期耗时（秒） |

**`reason` 标签值：** `retention`（保留期过期）, `disk_threshold`（磁盘阈值）, `archive_retention`（归档保留）, `orphan`（孤立文件）。

**`nvr_cleanup_duration_seconds` 桶区间：** 1秒, 5秒, 10秒, 30秒, 60秒, 300秒, 600秒。

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
| `nvr_audio_frames_total` | Counter | `camera_id`, `codec` | 广播到 StreamHub 的音频帧总数（按摄像头和编码分区） |
| `nvr_audio_frames_dropped_total` | Counter | `camera_id` | 因缓冲区溢出丢弃的音频帧数（按摄像头分区） |

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
| `nvr_relay_transcoder_temperature_c` | Gauge | — | 转码器热区当前温度（摄氏度） |
| `nvr_relay_transcoder_restarts_total` | Counter | — | 转码器重启总次数 |
| `nvr_relay_transcoder_thermal_throttles_total` | Counter | — | 触发预设降级的热节流事件总次数 |

**`status` 标签值：** `completed`, `failed`, `cancelled`。

**说明：** `nvr_relay_transcoder_*` 指标由实时中继转码器（`internal/livetranscode`）发出，独立于中央 `internal/metrics` 注册表单独注册。

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

## 13. 编码探测指标

观测编码检测链路（recorder 探测 → DB 持久化 → `/protocols` → orchestrator → 播放器）。H.265 链路是本项目最大的复杂度与缺陷来源——#112（H.265 黑屏）就是一次**静默的探测失败**。这些指标让探测结果与延迟可观测，使编码陈旧问题在用户看到黑屏之前就能被发现。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_codec_probe_total` | Counter | `camera_id`, `encoding`, `result` | 按解析出的编码与结果统计的探测次数 |
| `nvr_codec_probe_duration_seconds` | Histogram | `camera_id` | RTSP DESCRIBE 编码探测耗时 |
| `nvr_resolved_encoding` | Gauge | `camera_id`, `encoding` | 最近解析并持久化的编码（值恒为 `1`，`encoding` 标签才是信号） |

**`nvr_codec_probe_total` 的 `result` 标签取值：**
- `ok` — 实时 RTSP DESCRIBE 成功解析编码（权威结果）
- `unsupported` — 探测返回空，回退到声明/默认编码（设备可用，但未从实时流验证）
- `fail` — 探测返回空**且**无声明值，默认为 H264。陈旧编码风险最高（见 #112）。

**`encoding` 标签取值：** `h264`、`h265`、`mjpeg`、`jpeg`（小写，`MJPEG`→`jpeg` 归一化）。

**用法 / 告警：**

```promql
# 陈旧编码嫌疑：单相机 5 分钟内探测失败率 > 50%
sum(rate(nvr_codec_probe_total{result="fail"}[5m]))
  by (camera_id)
  / sum(rate(nvr_codec_probe_total[5m])) by (camera_id) > 0.5

# 慢/不可达设备：探测延迟 p99 偏高
histogram_quantile(0.99, sum(rate(nvr_codec_probe_duration_seconds_bucket[5m])) by (le, camera_id)) > 2.5

# 持久化编码漂移：nvr_resolved_encoding 与其他路径上报的实时编码不符
# —— 应作为持久化 bug 排查。
```

**说明：** 由 `ONVIFRecorder.detectEncoding()`（计数器 + 耗时）和 `ensureEncoding`（resolved gauge）发出。探测耗时仅含 RTSP DESCRIBE 往返，不含 ONVIF profile 回退。直方图桶：`0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10` 秒。

---

## 14. 内置运行时指标

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

## 15. 合并指标

跟踪录像片段合并操作 — 批量合并与准实时滚动合并。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_merge_attempts_total` | Counter | — | 合并尝试总次数 |
| `nvr_merge_successes_total` | Counter | — | 成功合并的总次数 |
| `nvr_merge_failures_total` | Counter | `reason` | 失败合并的总次数（按原因分区） |
| `nvr_merge_duration_seconds` | Histogram | — | 合并操作耗时（秒） |
| `nvr_merge_size_bytes` | Histogram | — | 合并输出大小（字节） |
| `nvr_merge_pending_segments` | Gauge | `camera_id` | 待合并的片段数量（按摄像头分区） |
| `nvr_rolling_merge_latency_seconds` | Histogram | `camera_id` | 从片段关闭到滚动合并完成的耗时 |
| `nvr_rolling_merge_bucket_segments` | Gauge | `camera_id` | 当前滚动合并窗口桶中累计的片段数 |

**`nvr_merge_duration_seconds` 桶区间：** 0.5秒, 1秒, 5秒, 10秒, 30秒, 60秒, 300秒, 600秒。**`nvr_merge_size_bytes` 桶区间：** 10MB, 50MB, 100MB, 500MB, 1GB, 3GB。**`nvr_rolling_merge_latency_seconds` 桶区间：** 0.1秒, 0.5秒, 1秒, 2秒, 5秒, 10秒, 30秒。

**用途：**

```promql
# 合并失败率
rate(nvr_merge_failures_total[5m]) / rate(nvr_merge_attempts_total[5m])

# 滚动合并延迟 P99（片段关闭 → 合并完成）
histogram_quantile(0.99, rate(nvr_rolling_merge_latency_seconds_bucket[5m]))

# 各摄像头未合并片段积压
nvr_merge_pending_segments
```

---

## 16. SQLite 数据库指标

SQLite 元数据库的健康指标 — 写连接池、只读连接池与文件级健康度。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_sqlite_open_connections` | Gauge | — | SQLite 写连接池的打开连接数 |
| `nvr_sqlite_in_use_connections` | Gauge | — | SQLite 写连接池的使用中连接数 |
| `nvr_sqlite_read_open_connections` | Gauge | — | SQLite 只读连接池的打开连接数（query_only，WAL 模式下与写并发） |
| `nvr_sqlite_read_in_use_connections` | Gauge | — | SQLite 只读连接池的使用中连接数 |
| `nvr_sqlite_read_wait_count_total` | Counter | — | 只读连接池无可用连接、调用方需要等待的总次数（持续增长说明应调大 `SetReadPoolSize`） |
| `nvr_sqlite_read_wait_duration_seconds` | Gauge | — | 调用方等待只读连接池连接的累计秒数（自启动起累计） |
| `nvr_sqlite_wal_size_bytes` | Gauge | — | SQLite WAL 文件大小（字节） |
| `nvr_sqlite_db_size_bytes` | Gauge | — | SQLite 数据库文件大小（字节） |
| `nvr_sqlite_fragmentation_ratio` | Gauge | — | SQLite 碎片率（freelist_count / page_count） |
| `nvr_sqlite_query_duration_seconds` | Histogram | `query_name` | SQLite 查询耗时（秒），按查询名称分区 |
| `nvr_sqlite_busy_errors_total` | Counter | — | 所有数据库操作中重试的 SQLITE_BUSY 错误总数 |

**`nvr_sqlite_query_duration_seconds` 桶区间：** 1ms, 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s。

**用途：**

```promql
# 按查询名称的慢查询 P99
histogram_quantile(0.99, rate(nvr_sqlite_query_duration_seconds_bucket[5m]))

# 只读连接池不足 — 应调大 SetReadPoolSize
rate(nvr_sqlite_read_wait_count_total[5m]) > 0

# 锁竞争
rate(nvr_sqlite_busy_errors_total[5m]) > 0
```

---

## 17. 认证指标

跟踪登录尝试，用于安全监控。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_auth_attempts_total` | Counter | `result` | 认证尝试总次数（按结果分区） |
| `nvr_auth_rate_limited_total` | Counter | — | 被认证速率限制器拦截的请求总数 |

**`result` 标签值：** `success`（成功）, `failure`（失败）, `no_password`（无密码）。

**用途：**

```promql
# 暴力破解检测 — 高失败率
rate(nvr_auth_attempts_total{result="failure"}[5m]) > 0.1

# 速率限制器活动情况
rate(nvr_auth_rate_limited_total[5m])
```

---

## 18. AI 事件指标

跟踪从外部 MiBeeVision 后端接收的 AI 事件。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_ai_events_received_total` | Counter | `camera_id`, `event_type` | 从 MiBeeVision 接收的 AI 事件总数（按摄像头和事件类型分区） |
| `nvr_ai_events_errors_total` | Counter | — | 接收或处理 AI 事件时的错误总数 |

**用途：**

```promql
# AI 管道健康 — 各摄像头事件流入速率
rate(nvr_ai_events_received_total[5m])

# 摄入错误
rate(nvr_ai_events_errors_total[5m]) > 0
```

---

## 19. 时间轴指标

跟踪 DVR 式录像浏览过程中的时间轴跳转操作。

| 指标 | 类型 | 标签 | 说明 |
|--------|------|--------|-------------|
| `nvr_timeline_seeks_total` | Counter | `camera_id`, `type` | 时间轴跳转操作总次数（按摄像头和跳转类型分区） |

**`type` 标签值：** `segment`, `intra`。

**用途：**

```promql
# 各摄像头跳转热点
topk(5, sum(rate(nvr_timeline_seeks_total[1h])) by (camera_id))
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
