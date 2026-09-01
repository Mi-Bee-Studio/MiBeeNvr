# 录制与回放

> 适用于 MiBeeNvr v0.12.0

MiBee NVR 将摄像头视频流录制为 MP4 片段并保存到磁盘，提供 Web 界面用于回放、搜索和下载录像。

## 录制原理

1. **帧采集**：从摄像头获取视频帧（RTSP / ONVIF / SRT / RTMP / libcamera）
2. **封装**：使用 MP4 容器封装视频帧（H.264 / H.265）
3. **分段**：按配置的时间间隔切分为 MP4 片段文件
4. **索引**：SQLite 数据库记录每个片段的元数据（时间、摄像头、文件路径等）
5. **清理**：根据保留策略自动删除过期片段

## 录制密度策略

每路相机可选两种**录像模式**（`recording_mode`）：

| 模式 | 行为 | 适用 |
|------|------|------|
| `continuous`（默认） | 全帧率连续录制 | 常规监控 |
| `adaptive` | 动静感知——安静时段自动降为延时级稀疏写入，活动 / 音频 / 外部触发立即恢复全帧率 | 看家、楼道、仓库等长时间无人的场景，磁盘占用直降 75%+ |

自适应模式的调参、音频触发、环境声氛围层与活动检索见 **[自适应录制](adaptive-recording.md)** 专页。

## 默认行为

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| 录像目录 | `/var/lib/mibee-nvr`（`storage.root_dir`） | Docker 部署为数据卷（默认 `/data`） |
| 片段时长 | 30 秒 | `storage.segment_duration` 可改（10s ~ 5m） |
| 保留天数 | 30 天 | `cleanup.retention_days`，超过自动删除 |
| 磁盘占用 | ~1GB/天/摄像头 | 1080p H.264 预估；自适应模式可再降 75%+ |

## 配置录制

### 录制设置

```yaml
storage:
  root_dir: "/var/lib/mibee-nvr"  # 录像存储根路径
  segment_duration: "30s"          # 片段时长（10s ~ 5m）

cleanup:
  retention_days: 30               # 保留天数
```

### 片段时长选择

| 时长 | 优点 | 缺点 |
|------|------|------|
| 10s-30s | 快速加载、节省内存 | 文件数量多、I/O 频繁 |
| 1m（推荐） | 平衡性能和资源 | — |
| 2m-5m | 文件数量少 | 内存占用高、加载慢 |

### 保留策略

- **按天数**：`max_days: 30` 保留最近 30 天
- **按磁盘**：监控磁盘使用量，自动清理最旧的片段
- **手动**：在 Web UI 中手动删除片段，或用 [`mibee-nvr cleanup`](cli.md#cleanup-录像清理) 按日期 / 孤儿文件批量清理

## Web UI 回放

### 访问录像

1. 打开 Web 界面，进入「**录像**」页
2. 顶部工具栏可按类型筛选（**全部 / 视频 / 延时摄影 / MJPEG**）、按摄像头和 **AI 检测**（含人 / 含车）过滤、按**活动**过滤（有活动 / 安静 / 场景切换，可设最低活动分），或用搜索框检索
3. 通过月份切换器和日历选择日期，定位到有录像的一天
4. 「**时间轴**」视图下点击或拖拽时间轴定位播放；「**列表**」视图下点击片段的「查看」进入回放

### 时间轴视图

「录像」页默认以时间轴展示每个摄像头当天的录像覆盖情况，AI 检测事件以标记点的形式叠放在轨道上：

![录像时间轴](images/recordings.webp)

- **轨道填充**：该时间段有录像
- **AI 标记**：悬停可查看事件摘要（类别、目标、出现次数），点击可跳转到对应时刻
- **now 指示**：当前时间位置

### 列表视图

点击「列表」切换为分页列表，每行显示摄像头、格式（MP4 (HEVC) / JPEG 等）、时长、大小、开始时间与合并状态，支持多选批量删除：

![录像列表](images/recordings-list.webp)

> 长时间段的连续片段会被自动**合并**（标记为「已合并」），回放时按一个整体加载。

### 回放详情

在列表中点击「查看」进入回放页：播放器支持拖动进度条定位、倍速、快照，页面还提供下载与元数据信息：

![录像回放](images/playback.webp)

### 搜索录像

支持按时间范围搜索：

1. 选择摄像头
2. 设置开始和结束时间
3. 点击「搜索」
4. 从结果中选择片段播放

## 下载录像

### 单个片段下载

在 Web UI 中：
1. 找到目标片段
2. 点击「下载」按钮
3. 保存为 MP4 文件

### 批量下载

```bash
# 使用 WebDAV 下载
rclone copy nvr:/recordings/camera-id/2026-08-18/ ./local-folder/

# 使用 FTP 下载
wget -r ftp://admin:password@192.168.1.50:2121/recordings/camera-id/
```

### 按时间范围导出

没有一次导出整段区间的端点；按时间定位片段后逐段下载即可（合并后的长片段通常一段就是一整天）：

```bash
# 列出时间范围内的录像，逐段下载（支持 Range 断点续传）
curl -u admin:password \
  "http://192.168.1.50:9090/api/recordings?camera_id=front-door&start=2026-08-18T00:00:00Z&end=2026-08-18T12:00:00Z"

curl -u admin:password \
  -o segment.mp4 \
  "http://192.168.1.50:9090/api/recordings/{id}/download"
```

## 存储统计

两处查看：

- 「**仪表盘**」→「存储趋势」：每摄像头占用 / 段数 / 占比，每日写入量堆叠趋势图（图例可点选隔离）
- 「**设置**」→「存储」：存储路径、[候选卷与录像迁移](storage-management.md)

![存储设置页](images/settings-storage.webp)

| 指标 | 说明 |
|------|------|
| 总占用 | 所有录像的磁盘占用 |
| 今日新增 | 今天录制的数据量 |
| 摄像头分布 | 每个摄像头的磁盘占用 |
| 趋势图 | 过去 7/30 天的存储趋势 |

## 录制状态

### 健康录制

在 Web UI 中，摄像头状态应显示为绿色（录制中）。

### 录制错误

常见错误及解决方法：

| 错误 | 原因 | 解决方法 |
|------|------|----------|
| 摄像头离线 | 网络问题或摄像头关机 | 检查网络连接 |
| 权限被拒 | 存储目录不可写 | 检查目录权限 |
| 磁盘已满 | 存储空间不足 | 增加磁盘或调整保留策略 |
| 编码不支持 | 摄像头编码格式不在支持列表 | 检查 encoding 字段配置 |

### 录制日志

NVR 日志输出到标准输出（systemd 部署进 journald）：

```bash
# systemd 部署：查看录制相关日志
journalctl -u mibee-nvr -f | grep -i record

# Docker 部署
docker compose logs -f mibee-nvr | grep -i record
```

## 高级功能

### 自适应录制

安静时段自动降为稀疏关键帧写入、有活动立即恢复全帧率——详见 **[自适应录制](adaptive-recording.md)**。

```yaml
cameras:
  - id: "front-door"
    recording_mode: "adaptive"   # 默认 "continuous"
```

### 录制时间窗

限制录像到特定时间段（如仅白天），时间窗外不落盘：

```yaml
cameras:
  - id: "office-cam"
    recording_schedule:
      time_ranges:
        - start: "09:00"
          end: "18:00"
      days_of_week: [1, 2, 3, 4, 5]   # 0=周日 … 6=周六；空 = 每天
```

### 外部事件触发录制

MQTT 消息、脚本或 AI 后端可把相机拉回全帧率（配合自适应模式 = 事件驱动录制）：

```bash
curl -u admin:password -X POST \
  http://192.168.1.50:9090/api/cameras/front-door/adaptive/trigger \
  -H "Content-Type: application/json" \
  -d '{"source": "automation", "hold": "30s"}'
```

MQTT 集成见 [MQTT 集成](mqtt-integration.md)。

## 常见问题

### 录像文件无法播放

- 确认播放器支持 H.265 编码（推荐 VLC）
- 尝试转换编码格式（`ffmpeg -i input.mp4 -c:v libx264 output.mp4`）

### 录像文件损坏

- NVR 的分段写入使用「临时文件 + 原子重命名」，录制中异常终止不会损坏已落盘的片段
- 若个别文件仍无法播放，通常是**源流本身**断连瞬间的参考帧缺失——用 NVR 的 Web 回放验证（前端有容错解码）；确需修复可尝试 `ffmpeg -i input.mp4 -c copy output.mp4`

## 下一步

- [音频](audio.md) — 音频录制和对讲
- [延时摄影](timelapse.md) — 延时摄影功能
- [转码](transcoding.md) — 视频转码
