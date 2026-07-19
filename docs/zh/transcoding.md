# 转码指南

## 概述

转码是将录像记录在不同编解码器之间转换，以优化存储空间、提高播放兼容性或减小文件大小。MiBee NVR 支持在后台将 H.264 和 H.265 录像转换为任意一种格式，并支持可配置的质量预设。

在以下情况使用转码功能：
- 通过转换为更高效的编解码器减少存储使用
- 提高在旧设备上的播放兼容性
- 为特定用例转换录像
- 优化文件大小存档录像

## 快速开始

### 步骤 1：检查硬件支持

```bash
# 测试系统是否支持转码
curl -u admin:password http://localhost:9090/api/transcoding/check
```

### 步骤 2：下载 FFmpeg（如果需要）

```bash
# 自动下载适用于您平台的 FFmpeg
curl -u admin:password \
  -X POST http://localhost:9090/api/transcoding/ffmpeg/download
```

### 步骤 3：启用转码

将转码配置添加到您的配置文件：

```yaml
transcoding:
  enabled: true
  max_workers: 1
  replace_original: false

cameras:
  - id: "front-door"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    transcoding:
      enabled: true
      target_codec: "h264"
      preset: "medium"
      bitrate: "2M"
```

重启 MiBee NVR 以应用配置。

## 配置参考

### 全局设置

配置文件中的 `transcoding` 部分控制整体转码行为：

```yaml
transcoding:
  enabled: true                    # 启用/禁用转码系统
  ffmpeg_path: ""                 # 如果为空则自动检测
  max_workers: 1                   # 最大并发转码任务数（1-4）
  replace_original: false          # 成功后删除原始文件
```

**字段详情**：

- **`enabled`**: `true`/`false` - 启用整个转码系统
- **`ffmpeg_path`**: 可选的 FFmpeg 二进制文件路径。留空自动检测
- **`max_workers`**: 并发 FFmpeg 进程数（1-4）。树莓派 3B 上从 1 开始
- **`replace_original`**: `true`/`false` - 成功后删除原始文件。**请谨慎使用**

### 单个摄像头设置

单个摄像头可以有自己的转码配置：

```yaml
cameras:
  - id: "front-door"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    transcoding:
      enabled: true                 # 为此摄像头启用转码
      target_codec: "h264"         # h264 或 h265
      preset: "medium"             # ultrafast, faster, medium
      bitrate: "2M"                # 例如 "1M", "2M", "5M"
```

**字段详情**：

- **`enabled`**: `true`/`false` - 为此摄像头覆盖全局设置
- **`target_codec`**: `"h264"` 或 `"h265"` - 目标转换编解码器
- **`preset`**: 速度/质量权衡：
  - `"ultrafast"`: 最快，文件最大
  - `"faster"`: 良好平衡（推荐）
  - `"medium"`: 较慢，文件较小
- **`bitrate`**: 目标比特率，格式如 `"2M"`（2 Mbps）或 `"500k"`（500 kbps）

### 性能预设

| 预设 | 速度 | 质量 | 文件大小 | CPU 使用 | 最佳用途 |
|------|------|------|----------|----------|----------|
| `ultrafast` | 非常快 | 良好 | 大 | 高 | 实时播放，临时存储 |
| `faster` | 快 | 良好 | 中等 | 中等 | 通用，平衡 |
| `medium` | 中等 | 良好 | 小 | 低 | 存档，长期保存 |

## 自检说明

转码自检验证您的硬件功能和 FFmpeg 可用性：

### 检查内容

- **FFmpeg 可用性**：在系统 PATH 中查找 FFmpeg 二进制文件
- **编码器支持**：验证 H.264/H.265 编码器支持
- **硬件功能**：估计最大并发流数和 FPS
- **内存需求**：如果系统内存不足则发出警告
- **CPU 性能**：估计转码速度和质量

### 失败场景

**FFmpeg 不可用**：
- 首次使用时会自动下载 FFmpeg
- 通过 `/api/transcoding/ffmpeg/status` 监控下载进度

**内存过低（<512MB）**：
- 警告：系统可能变得不稳定
- 解决方案：将 `max_workers` 减少到 1，避免 `replace_original`

**估计 FPS 过低（<5.0）**：
- 警告：实时转码可能太慢
- 解决方案：使用较低分辨率或减少 `max_workers`

**编码器不支持**：
- 错误：FFmpeg 编译时没有 H.264/H.265 支持
- 解决方案：重新编译 FFmpeg 并添加编码器支持

## 硬件要求

| 平台 | 最大工作进程 | 推荐 FPS | 内存使用 | 备注 |
|------|-------------|---------|----------|------|
| 树莓派 3B | 1 | 1-2 FPS | 512MB+ | 仅使用 1 个工作进程 |
| 树莓派 4 | 2 | 2-4 FPS | 1GB+ | 适合轻度转码 |
| 树莓派 5 | 2-3 | 3-6 FPS | 1GB+ | 更好的性能 |
| x86 服务器 | 4 | 5+ FPS | 2GB+ | 完整性能潜力 |
| Docker | 1-2 | 2-4 FPS | 1GB+ | 取决于主机资源 |

## 性能预期

### 树莓派 3B
- **1 个工作进程**: 720p ~1-2 FPS，1080p ~1 FPS
- **内存**: 转码期间 ~300-500MB
- **存储**: ~15-30% 更快的录制速度
- **推荐**: 对实时播放使用 `ultrafast` 预设

### 树莓派 4/5  
- **2 个工作进程**: 720p ~3-4 FPS，1080p ~2-3 FPS
- **内存**: 每个工作进程 ~500MB-1GB
- **存储**: ~25-40% 更快的录制速度
- **推荐**: 通用使用使用 `faster` 预设

### x86 系统
- **4 个工作进程**: 多个流的实时性能
- **内存**: 每个工作进程 ~200-500MB
- **存储**: 接近实时性能
- **推荐**: 文件大小优化使用 `medium` 预设

## 存储文件转码

### 通过 API 手动触发

手动转码现有录像：

```bash
# 为特定录像排队转码任务
curl -u admin:password \
  -X POST http://localhost:9090/api/transcoding/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "recording_id": "1704123456789012345",
    "target_codec": "h264",
    "replace_original": false
  }'
```

### 自动转码

启用单摄像头转码来自动处理新录像：

```yaml
cameras:
  - id: "front-door"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    transcoding:
      enabled: true
      target_codec: "h264"
      preset: "faster"
      bitrate: "2M"
```

### 监控进度

检查转码状态：

```bash
# 查看系统状态
curl -u admin:password http://localhost:9090/api/transcoding/status

# 列出所有转码任务
curl -u admin:password http://localhost:9090/api/transcoding/tasks

# 查看 FFmpeg 下载进度
curl -u admin:password http://localhost:9090/api/transcoding/ffmpeg/status
```

## 替换模式

### 工作原理

当 `replace_original: true` 时，系统会：

1. 将原始文件转码为新文件
2. 验证转码后的文件有效
3. 删除原始文件
4. 更新数据库引用

### 权衡利弊

**优势**：
- **存储节省**: 文件大小减少 30-50%
- **单一来源**: 每个录像只有一个版本
- **更干净的存储**: 没有混合质量的文件

**风险**：
- **数据丢失**: 原始录像永久删除
- **需要验证**: 必须验证转码文件有效
- **无法恢复**: 如果转码失败无法恢复原始文件

### 安全建议

1. **始终备份原始录像** 在启用替换模式之前
2. **先测试转码** 使用单个摄像头
3. **监控任务成功率** 通过 API
4. **保持 `replace_original: false`** 用于生产系统
5. **仅在临时存储优化时使用 `replace_original: true`**

```yaml
# 安全配置（推荐用于生产环境）
transcoding:
  enabled: true
  max_workers: 1
  replace_original: false  # 保留原始文件以确保安全

cameras:
  - id: "front-door"
    transcoding:
      enabled: true
      target_codec: "h264"
      preset: "medium"
      bitrate: "2M"
```

## 监控

### Prometheus 指标

转码指标在 `/api/metrics` 暴露（带标签和类型的完整列表见 [metrics.md](metrics.md#11-transcoding-metrics)）：
- `nvr_transcoding_active_jobs`: 当前运行的转码任务（gauge）
- `nvr_transcoding_jobs_total`: 转码任务总数，带 `codec_from`、`codec_to`、`encoder`、`crf` 和 `status`（`completed`/`failed`/`cancelled`）标签（counter）
- `nvr_transcoding_duration_seconds`: 已完成转码任务的耗时，按编码转换分组（histogram）
- `nvr_transcoding_bytes_processed`: 转码任务处理的总字节数（counter）
- `nvr_transcoding_ffmpeg_status`: FFmpeg 可用性 —— 0=未安装、1=下载中、2=可用（gauge）

### Web UI 状态

在 Web UI 中检查转码状态：
- **设置 → 转码**: 系统状态和 FFmpeg 下载进度
- **录像 → 操作**: 单个文件的转码按钮
- **队列状态**: 活跃和等待任务计数

### API 端点

- `GET /api/transcoding/check` - 硬件自检
- `GET /api/transcoding/status` - 系统状态
- `GET /api/transcoding/ffmpeg/status` - FFmpeg 进度
- `POST /api/transcoding/ffmpeg/download` - 开始下载
- `POST /api/transcoding/ffmpeg/download/retry` - 重试下载
- `GET /api/transcoding/tasks` - 列出任务（分页）
- `POST /api/transcoding/tasks` - 创建手动任务
- `DELETE /api/transcoding/tasks/{id}` - 取消任务
- `GET /api/transcoding/cameras` - 单摄像头配置

## 常见问题

### 常见问题

**Q: 转码会影响录制性能吗？**
A: 是的，转码会使用额外的 CPU 和内存。从 1 个工作进程开始并监控系统负载。

**Q: 摄像头录制时可以转码吗？**
A: 是的，转码在后台进程中运行，不会中断录制。

**Q: 转码失败会发生什么？**
A: 失败的任务保留在队列中。检查日志获取 FFmpeg 错误详情。手动重试或修复问题。

### 技术问题

**Q: 为什么需要下载 FFmpeg？**
A: FFmpeg 提供视频处理功能。MiBee NVR 包含下载器以获取适合您平台的版本。

**Q: 转码和合并有什么区别？**
A: 转码改变视频编解码器/格式，而合并将多个片段组合成更少的文件。

**Q: 可以在树莓派上将 H.265 转码为 H.264 吗？**
A: 是的，但性能会受限。在树莓派系统上使用 `ultrafast` 预设和 1 个工作进程。

### 性能问题

**Q: 转码能节省多少存储空间？**
A: H.265→H.264 转码通常节省 30-50%，取决于源质量和比特率设置。

**Q: 最佳工作进程数是多少？**
A: 从 1 个工作进程开始，仅在转码期间 CPU 使用率保持在 80% 以下时增加。

**Q: 转码能在树莓派 3B 上工作吗？**
A: 是的，但预期性能会降低。使用 1 个工作进程和简单预设以获得最佳结果。

### 故障排除

**FFmpeg 问题**：
- 检查下载进度：`GET /api/transcoding/ffmpeg/status`
- 重试失败的下载：`POST /api/transcoding/ffmpeg/download/retry`
- 验证 FFmpeg 路径：检查配置中的 `ffmpeg_path`

**性能问题**：
- 如果 CPU 使用率 >80%，减少 `max_workers`
- 树莓派系统使用 `ultrafast` 预设
- 如果磁盘空间关键，禁用 `replace_original`

**队列问题**：
- 检查任务状态：`GET /api/transcoding/tasks`
- 取消卡住的任务：`DELETE /api/transcoding/tasks/{id}`
- 验证摄像头已启用转码

更多详细的故障排除，请查看 [配置](configuration.md) 和 [API 参考](./api/README.md) 文档。