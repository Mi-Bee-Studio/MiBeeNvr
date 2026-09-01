# 可观测性手册

MiBee NVR 内置一整套排障与监控能力：链路（Flow）页、端到端延迟显示、健康稳定性数据、按相机帧追踪采样、Prometheus 指标与 Grafana 面板。本文是这些能力的总入口；Prometheus 指标明细另见 [metrics.md](metrics.md)。

## 链路（Flow）页

仪表盘逐相机展开的链路树展示一帧视频的完整旅程：**采集源 → 分发中枢（StreamHub）→ 录像落盘 / 各直播协议消费者 / 健康检测 / 转发**。相机配置了[子码流](sub-stream.md)时另有一条**虚线子流分支**（独立拉流器、独立 hub，差分 fps/kbps + 消费者类型）——有消费者才出现，回收后自动消失。

### 各列含义

| 列 | 含义 | 排障提示 |
|----|------|----------|
| 已发（sends） | 该消费者累计收到的帧数 | 停止增长 = 该支路断了 |
| 丢弃（drops） | 消费者缓冲满被丢的帧数 | 增长快 = 消费端处理不过来 |
| IDR 丢弃 | 关键帧被丢的数量 | >0 意味着观众可能长时间黑屏等关键帧 |
| 丢帧率（drop_rate） | drops / sends | >1% 橙色、>5% 红色 |
| 缓冲（buffer） | 当前缓冲水位 / 容量 | 贴近容量 = 即将开始丢帧 |
| 驻留（dwell avg/max） | 帧在 hub 排队到发出的耗时 | max 持续增大 = 消费者拖慢了分发 |

节点上的 **fps / kbps** 由前端两次轮询（约 2 秒）对累计计数器做差分计算——后端热路径不算速率，所以刷新有几秒延迟属正常。

### 录制支路

录像节点额外显示（H.264/H.265 相机）：

- **分片进度**：当前 MP4 分片已写时长 / 目标时长（`segment_duration`）与帧数——接近目标说明马上会切片
- **环形缓冲水位**：录制器内部帧环形缓冲的当前占用 / 容量。>50% 变黄说明磁盘写入跟不上帧率，持续打满会开始丢帧（对应 `nvr_recorder_ring_buffer_drops_total`）
- **待合并段数**：等待 rolling merge 合并的分段数（对应 `nvr_merge_pending_segments`）。持续增长 = 合并速度落后于生成速度

### 最近帧（last_frame）

hub 最近一次收到视频帧的时间距今多久。这是最直接的"流是否活着"信号——相机挂掉后累计帧数不会归零（那是历史值），只有这个字段会持续增大。

## 端到端延迟角标

支持直播的播放器左上角显示延迟角标（鼠标悬停可见"端到端延迟"）：

- **< 1 秒** 绿色、**< 3 秒** 黄色、**≥ 3 秒** 红色
- WebCodecs（WS）与 HTTP-FLV 为**精确值**：帧在进入 NVR 分发中枢时打上墙钟时间戳，随帧带到浏览器
- 带有 **≈** 前缀的是近似值：HLS 按缓冲分片估算（受分片粒度限制）、WebRTC 按相机入口时间估算（浏览器不暴露 RTP 时间戳）
- 数值每 10 秒上报一次遥测（`live_latency`，带 `protocol` 标签），可与 `nvr_playback_live_latency_ms` 指标对照

## 按相机帧追踪（frame-trace）

回答"这台相机拉流为什么卡"的最快路径：开启 30 秒采样，该相机的每个关键帧面包屑日志升到 Info 级并集中输出，直接看到帧在哪个环节被丢/延迟：

```bash
# 开启（默认 30 秒，最长 5 分钟）
curl -X POST 'http://nvr:9090/api/cameras/front-door/trace?duration=30s'

# 查看剩余窗口
curl http://nvr:9090/api/cameras/front-door/trace

# 提前停止
curl -X DELETE http://nvr:9090/api/cameras/front-door/trace
```

日志过滤 `component=frame-trace`（或 `journalctl -u mibee-nvr | grep frame-trace`），链路为：

```text
ingest（录制器收到帧）
  → streamhub_in / streamhub_drop（hub 入口/消费者丢帧）
  → ws_recv / flv_recv / hls_* / webrtc_recv|drop（各协议支路）
```

`trace_id` 为 `相机ID-PTS`（仅关键帧），同一帧在各环节用同一 ID 串联。窗口到期自动回落——严禁常开全帧全相机（日志量 = 帧率 × 环节数 × ~250B/行）。

## 健康稳定性（Health 页）

健康页除实时评分外还提供稳定性统计：

- **在线率（uptime %）**：统计窗口内相机处于可录制状态的时间占比
- **MTBF**：平均无故障时间——两次掉线之间的平均间隔
- **趋势图**：评分随时间变化，用于识别间歇性劣化（Wi-Fi 弱信号、DHCP 换 IP 前兆等）

扣分因素以中文人话列出（如"5 分钟内丢帧率 3.2%"），不需要反查规则表。

## Prometheus + Grafana

### 抓取

`/metrics` 端点默认公开（可用 `metrics_auth` 加 BasicAuth），Prometheus 抓取配置见 [metrics.md](metrics.md#访问指标)。

### 导入面板

`deploy/grafana/` 内置 5 块面板 + 告警规则，Grafana 导入即可用：

| 文件 | 内容 |
|------|------|
| `mibee-nvr-flow.json` | 链路视图指标版：各相机入站 fps/码率、消费者丢帧率、hub 驻留 |
| `camera-health-dashboard.json` | 相机健康评分、在线率、黑名单/自动重连 |
| `streaming-quality-dashboard.json` | 各直播协议观看数、帧发送/丢弃、端到端延迟 |
| `video-playback-dashboard.json` | 录制帧率、分片时长、merge 队列深度 |
| `system-overview-dashboard.json` | CPU/内存/goroutine/磁盘水位 |
| `alerts.yaml` | 常用告警规则（相机离线、丢帧率超阈、磁盘将满） |

> 面板与告警建议部署在**旁路监控机**上，不要跑在 NVR 本机——树莓派级别的硬件资源应留给录制。

### 端到端延迟指标

`nvr_playback_live_latency_ms`（标签 `protocol` = ws/flv/hls/webrtc）来自浏览器遥测上报，反映**真实用户看到的延迟**，与 hub 侧指标互补。

## pprof 性能分析

配置开启（默认关闭）：

```yaml
observability:
  enable_pprof: true
```

开启后 `/debug/pprof/*` 可用（CPU/内存/goroutine profile）。端点位于认证中间件之后——需要登录或 API Key 才能访问，但仍建议仅在排障期间开启，分析完关闭。

## 相关文档

- [metrics.md](metrics.md) — 全部 Prometheus 指标明细
- [configuration.md](configuration.md) — `observability` 配置项
- [troubleshooting.md](troubleshooting.md) — 常见问题排查
