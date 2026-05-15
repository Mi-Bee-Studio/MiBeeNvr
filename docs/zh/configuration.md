# MiBee NVR 配置参考文档

MiBee NVR 使用 YAML 格式的配置文件来控制所有功能模块。以下是所有可用选项的完整参考，包含默认值和使用示例。

## 配置文件结构

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
    name: "摄像头名称"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://..."
    enabled: true
    sub_stream_url: "rtsp://..."
    snapshot_url: "http://..."
    sample_interval: 1
    hls_max_fps: 0
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
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: false
hls:
  write_buffer_size: 40
  segment_max_size_mb: 10
observability:
  log_level: "info"
  log_format: "text"
  enable_pprof: false
version: "1.0"
```

## 服务器配置

### `server.listen`
- **类型**: 字符串
- **默认值**: `":9090"`
- **描述**: Web 服务器监听的地址和端口
- **示例**: `":8080"` 或 `"192.168.1.100:9090"`

## 存储配置

### `storage.root_dir`
- **类型**: 字符串
- **默认值**: `/var/lib/mibee-nvr`
- **描述**: 录像文件的存储根目录
- **示例**: `/var/lib/mibee-nvr`

### `storage.segment_duration`
- **类型**: 字符串
- **默认值**: `"30s"`
- **描述**: 视频分段时长（内存密集型）
- **重要提示**: 每个分段在完成前会保存在内存中
- **内存使用**:
  - 30秒分段: ~15-20MB 每个分段
  - 60秒分段: ~30-40MB 每个分段
  - 120秒分段: ~60-80MB 每个分段
- **建议**: 低内存系统使用 30 秒
- **示例**: `"30s"`, `"1m"`, `"5m"`

## 认证配置

### `auth.username`
- **类型**: 字符串
- **必需**: 是（Web UI 和 FTP 使用）
- **描述**: 认证用户名
- **示例**: `"admin"`

### `auth.password_hash`
- **类型**: 字符串
- **必需**: 是（Web UI 和 FTP 使用）
- **描述**: bcrypt 哈希密码。使用 `mibee-nvr hash-password <password>` 命令生成。
- **优先级**: 如果同时设置了 `password` 和 `password_hash`，`password_hash` 优先
- **注意**: 如果只设置了 `auth.password`（明文），服务器会在启动时自动生成哈希并写回配置文件
- **示例**: `$2a$10$N9qo8uLOickgx2ZMRZoMy...`

### `auth.password`
- **类型**: 字符串
- **可选**: 是
- **描述**: 明文密码（用于便捷的初始配置）。首次运行时，服务器会自动哈希此值并写入 `password_hash`，然后清空 `password` 字段。
- **优先级**: 仅当 `password_hash` 为空时才使用
- **示例**: `"admin123"`

## 摄像头配置

### 摄像头结构
每个摄像头配置需要这些基本字段：

```yaml
cameras:
  - id: "cam1"
    name: "摄像头名称"
    protocol: "rtsp_h264"
    url: "摄像头地址"
    enabled: true
```

### `cameras[].id`
- **类型**: 字符串
- **必需**: 是
- **描述**: 摄像头的唯一标识符
- **格式**: 8 字符字母数字（使用 crypto/rand 自动生成）
- **示例**: `"front-door"`, `"cam-01"`

### `cameras[].name`
- **类型**: 字符串
- **必需**: 是
- **描述**: 人类可读的摄像头名称
- **示例**: `"前门摄像头"`, `"后院"`

### `cameras[].protocol`

- **类型**: 字符串
- **必需**: 是
- **描述**: 摄像头传输协议（v0.2.0 新增独立协议字段）
- **选项**: `"rtsp"`, `"http"`, `"onvif"`（新的传输层协议值）

### `cameras[].encoding`

- **类型**: 字符串
- **可选**: 从旧协议自动检测或根据协议默认
- **描述**: 视频编码格式（v0.2.0 新增独立编码字段）
- **选项**: `"h264"`, `"h265"`, `"mjpeg"`, `"jpeg"`

**有效组合**:
  - `rtsp` 协议支持: `h264`, `h265`, `mjpeg`
  - `http` 协议支持: `jpeg`
  - `onvif` 协议支持: `h264`, `h265`

**注意事项**:
  - 不提供时自动从协议推断（如 `rtsp` 默认为 `h264`）
  - 某些编码格式仅适用于特定协议（如 `http` 只支持 `jpeg`）
**向后兼容支持**: 旧格式如 `"rtsp_h264"`, `"rtsp_h265"`, `"rtsp_mjpeg"`, `"http_jpeg"` 仍然可用（自动解析为对应的协议和编码）

### `cameras[].url`
- **类型**: 字符串
- **必需**: 是
- **描述**: 摄像头 URL 或流端点
- **示例**:
  - RTSP: `"rtsp://192.168.1.100:554/stream"`
  - HTTP: `"http://192.168.1.101/capture"`

### `cameras[].username`
- **类型**: 字符串
- **可选**
- **描述**: 摄像头认证用户名
- **示例**: `"admin"`

### `cameras[].password`
- **类型**: 字符串
- **可选**
- **描述**: 摄像头认证密码
- **示例**: `"摄像头密码"`

### `cameras[].onvif_endpoint`
- **类型**: 字符串
- **可选**
- **描述**: ONVIF 设备端点地址（仅当 protocol="onvif" 时使用）
- **示例**: `"http://192.168.1.100/onvif"`

### `cameras[].enabled`
- **类型**: 布尔值
- **默认值**: `true`
- **描述**: 是否启用摄像头录制
- **示例**: `true` 或 `false`

### `cameras[].sub_stream_url`
- **类型**: 字符串
- **可选**
- **描述**: 低分辨率子码流的 RTSP 地址。配置后，Dashboard 直播预览使用子码流而非主码流，降低带宽占用。
- **注意**: 子码流必须与主码流使用相同的编码格式（H.264/H.265）
- **示例**: `"rtsp://192.168.1.100:554/stream2"`

### `cameras[].snapshot_url`
- **类型**: 字符串
- **可选**
- **描述**: 返回 JPEG 快照图像的 HTTP 地址。配置后，Dashboard 显示快照缩略图而非 HLS 直播流，大幅降低带宽。
- **行为**: 快照缓存 10 秒；摄像头暂时不可达时返回过期缓存
- **示例**: `"http://192.168.1.100/snapshot"`, `"http://192.168.1.100/cgi-bin/snapshot.cgi"`

### `cameras[].sample_interval`
- **类型**: 整数
- **默认值**: `1`
- **描述**: MJPEG 摄像头的帧采样间隔。仅保存每第 N 帧。
- **用途**: 降低低优先级 MJPEG 摄像头的存储和带宽占用
- **示例**: `1`（每帧），`3`（每 3 帧），`5`（每 5 帧）

### `cameras[].hls_max_fps`
- **类型**: 整数
- **默认值**: `0`（不限制）
- **描述**: HLS 直播预览的最大帧率。超出帧率的帧会被丢弃以降低带宽。
- **重要**: 仅影响 HLS 直播预览，不影响录像
- **示例**: `10`, `15`, `24`

## 协议示例

### RTSP H.264 摄像头（新格式）

```yaml
- id: "front-door"
  name: "前门"
  protocol: "rtsp"
  encoding: "h264"
  url: "rtsp://192.168.1.100:554/live"
  username: "admin"
  password: "password123"
  enabled: true
```

**旧格式仍然支持**:
```yaml
- id: "front-door"
  name: "前门"
  protocol: "rtsp_h264"
  url: "rtsp://192.168.1.100:554/live"
  username: "admin"
  password: "password123"
  enabled: true
```

### RTSP H.265 摄像头

**新格式**:
```yaml
- id: "h265-camera"
  name: "H.265 摄像头"
  protocol: "rtsp"
  encoding: "h265"
  url: "rtsp://192.168.1.100:554/live"
  username: "admin"
  password: "password123"
  enabled: true
```

**旧格式仍然支持**:
```yaml
- id: "h265-camera"
  name: "H.265 摄像头"
  protocol: "rtsp_h265"
  url: "rtsp://192.168.1.100:554/live"
  username: "admin"
  password: "password123"
  enabled: true
```

### RTSP MJPEG 摄像头

**新格式**:
```yaml
- id: "back-yard"
  name: "后院"
  protocol: "rtsp"
  encoding: "mjpeg"
  url: "rtsp://192.168.1.101:554/stream"
  enabled: true
```

**旧格式仍然支持**:
```yaml
- id: "back-yard"
  name: "后院"
  protocol: "rtsp_mjpeg"
  url: "rtsp://192.168.1.101:554/stream"
  enabled: true
```

### HTTP JPEG 摄像头（新格式）

```yaml
- id: "garage"
  name: "车库"
  protocol: "http"
  encoding: "jpeg"
  url: "http://192.168.1.102/capture"
  enabled: true
```

**旧格式仍然支持**:
```yaml
- id: "garage"
  name: "车库"
  protocol: "http_jpeg"
  url: "http://192.168.1.102/capture"
  enabled: true
```

## 清理配置

### `cleanup.retention_days`
- **类型**: 整数
- **默认值**: `30`（未设置或 `0` 时）
- **描述**: 保留录像的天数
- **重要提示**: 值为 `0` 会被视为"未配置"，默认为 30 天
- **按摄像头保留**: 单个摄像头可以通过 Web UI 或 API 设置自己的 `retention_days` 来覆盖全局设置
- **示例**: `30`, `90`, `365`

### `cleanup.check_interval`
- **类型**: 字符串
- **默认值**: `"1h"`
- **描述**: 检查过期录像的频率
- **格式**: Go 时间格式
- **示例**: `"30m"`, `"2h"`, `"24h"`

### `cleanup.disk_threshold_percent`
- **类型**: 整数
- **默认值**: `95`
- **描述**: 清理的磁盘使用率阈值
- **行为**: 当磁盘使用率超过此阈值时运行清理
- **示例**: `80`, `90`, `95`

## 合并配置

合并功能自动将小视频段合并为更大的文件，减少文件数量并提高存储效率。这是一个类似清理功能的周期性后台任务。

### `merge.enabled`
- **类型**: 布尔值
- **默认值**: `false`
- **描述**: 启用或禁用后台合并任务
- **注意**: 禁用时，录像段保持为独立文件

### `merge.check_interval`
- **类型**: 字符串
- **默认值**: `"1h"`
- **描述**: 合并任务的运行间隔
- **格式**: Go 时间格式
- **示例**: `"30m"`, `"1h"`, `"2h"`

### `merge.window_size`
- **类型**: 字符串
- **默认值**: `"1h"`
- **描述**: 分段时间窗口。同一窗口内（同一摄像头、同一小时）的段会被合并。
- **格式**: Go 时间格式
- **示例**: `"1h"`（每小时合并所有段）

### `merge.batch_limit`
- **类型**: 整数
- **默认值**: `200`
- **描述**: 单次合并运行中处理的最大段数。防止资源过度占用。
- **示例**: `100`, `200`, `500`

### `merge.min_segment_age`
- **类型**: 字符串
- **默认值**: `"10m"`
- **描述**: 段被纳入合并的最小年龄。确保正在写入的段不会被合并。
- **格式**: Go 时间格式
- **示例**: `"5m"`, `"10m"`, `"30m"`

### `merge.min_segments_to_merge`
- **类型**: 整数
- **默认值**: `3`
- **描述**: 触发合并所需的最小段数。段数不足的组会被跳过。
- **示例**: `2`, `3`, `5`

### 合并行为
- **H.264/H.265**: 段以原始编码直接拼接（快速、无损）。仅编码参数相同（SPS/PPS）的段会被合并。
- **MJPEG**: JPEG 文件移动到同一目录（无重编码）。
- **磁盘空间**: 如果可用磁盘空间不足合并文件大小的 110%，合并会被跳过。
- **原子操作**: 合并文件使用原子重命名（临时文件 → 最终文件）防止数据损坏。
**原始文件**: 合并成功后，源段会从磁盘和数据库中删除。

### 每摄像头合并配置

单个摄像头可以通过 API 或 Web UI 覆盖全局合并设置。这允许不同摄像头根据其录制模式和存储需求采用不同的合并策略。

**API 接口**:
- `GET /api/cameras/:id/merge-config` - 获取摄像头合并覆盖设置
- `PUT /api/cameras/:id/merge-config` - 设置摄像头合并覆盖设置
- `DELETE /api/cameras/:id/merge-config` - 重置为全局默认值

**摄像头合并参数**:
配置摄像头合并设置时，可以覆盖所有 6 个全局参数：

- `enabled` - 启用/禁用此摄像头的合并功能
- `check_interval` - 检查可合并段的频率
- `window_size` - 分段组合的时间窗口
- `batch_limit` - 单次合并运行的最大段数
- `min_segment_age` - 段可合并的最小年龄
- `min_segments_to_merge` - 触发合并所需的最小段数

**覆盖示例**:
```yaml
cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/live"
    # 摄像头合并设置
    merge_config:
      enabled: true
      check_interval: "30m"
      batch_limit: 100  # 低于全局值 200
      min_segments_to_merge: 2  # 低于全局值 3
```

## FTP 配置

### `ftp.enabled`
- **类型**: 布尔值
- **默认值**: `true`
- **描述**: 是否启用 FTP 服务器

### `ftp.port`
- **类型**: 整数
- **默认值**: `2121`
- **描述**: FTP 服务器端口
- **注意**: FTP 无法反向代理

### `ftp.passive_port_range`
- **类型**: 字符串
- **默认值**: `"2122-2140"`
- **描述**: 被动模式 FTP 连接的端口范围
- **格式**: `"起始-结束"`
- **示例**: `"30000-30100"`

**匿名访问**: FTP 服务器拒绝所有匿名访问，必须使用配置文件中的认证凭据。

## MQTT 配置

### `mqtt.enabled`
- **类型**: 布尔值
- **默认值**: `false`
- **描述**: 是否启用 MQTT 集成

### `mqtt.broker`
- **类型**: 字符串
- **必需**: 启用时必需
- **描述**: MQTT 代理服务器地址
- **示例**: `"tcp://localhost:1883"` 或 `"mqtt://192.168.1.100:1883"`

### `mqtt.topic`
- **类型**: 字符串
- **必需**: 启用时必需
- **描述**: 用于触发事件的订阅主题
- **示例**: `"mibeenr/trigger"`

### `mqtt.client_id`
- **类型**: 字符串
- **必需**: 启用时必需
- **描述**: MQTT 客户端 ID
- **示例**: `"mibee-nvr"`

## WebDAV 配置

### `webdav.enabled`
- **类型**: 布尔值
- **默认值**: `true`
- **描述**: 是否启用 WebDAV 服务器

### `webdav.path_prefix`
- **类型**: 字符串
- **默认值**: `"/dav"`
- **描述**: WebDAV 访问的 URL 路径前缀
- **示例**: `"/dav"`, `"/recordings"`

### `webdav.read_write`

- **类型**: 布尔值
- **默认值**: `false`
- **描述**: WebDAV 服务器是否允许写入操作（v0.2.0 可配置）
- **重要**: 启用后，可以通过 WebDAV PUT 请求自动注册新摄像头
- **安全考虑**: 启用写入访问前请考虑安全影响
- **示例**: `false`（只读），`true`（可读写）

## HLS 配置

### `hls.write_buffer_size`
- **类型**: 整数
- **默认值**: `40`
- **描述**: 每个 HLS 流的异步帧缓冲区大小。控制写入前缓冲的帧数。
- **示例**: `40`, `80`, `120`

### `hls.segment_max_size_mb`
- **类型**: 整数
- **默认值**: `10`
- **描述**: HLS 分段的最大大小（MB）
- **示例**: `10`, `20`

## 可观测性配置

### `observability.log_level`
- **类型**: 字符串
- **默认值**: `"info"`
- **描述**: 日志级别
- **选项**: `"debug"`, `"info"`, `"warn"`, `"error"`
- **示例**: `"debug"`, `"info"`

### `observability.log_format`
- **类型**: 字符串
- **默认值**: `"text"`
- **描述**: 日志格式
- **选项**: `"json"`, `"text"`
- **示例**: `"json"`, `"text"`

### `observability.enable_pprof`
- **类型**: 布尔值
- **默认值**: `false`
- **描述**: 是否启用性能分析（pprof）
- **示例**: `false`, `true`

### `version`
- **类型**: 字符串
- **默认值**: `"1.0"`
- **描述**: 配置文件版本

## CLI 子命令

MiBee NVR 除了主服务模式外，还支持以下子命令：

### `mibee-nvr init`
交互式首次配置向导。创建包含基本设置的配置文件。

```bash
mibee-nvr init [flags]
```

**参数**:
- `--password <密码>` — 设置管理员密码（如未提供则交互式输入）
- `--username <名称>` — 设置管理员用户名（默认：`admin`）
- `--data-dir <路径>` — 设置存储目录（默认：`/var/lib/mibee-nvr`）
- `--listen <地址>` — 设置监听地址（默认：`:9090`）
- `--config <路径>` — 配置文件路径（默认：`mibee-nvr.yaml`）
- `--force` — 覆盖已有配置文件

### `mibee-nvr health`
健康检查，适用于容器/Docker 编排。服务器健康时退出码为 0。

```bash
mibee-nvr health [--addr :9090] [--config <path>]
```

### `mibee-nvr hash-password <密码>`
生成 bcrypt 密码哈希，用于 `auth.password_hash` 配置。

```bash
mibee-nvr hash-password my-secret-password
# 输出: $2a$10$N9qo8uLOickgx2ZMRZoMy...
```

### `mibee-nvr -version`
打印二进制版本并退出。

```bash
mibee-nvr -version
# 输出: MiBee NVR version 0.1.0-dev
```

## 重要提示

### 安全考虑
- FTP 凭据使用与 Web 界面相同的用户名/密码
- WebDAV 支持可选的只读/读写模式（默认只读，出于安全考虑）
- 所有 Web UI 和 FTP 访问都需要认证

### 内存管理
- 分段时长直接影响内存使用
- 较长的分段 = 更多 RAM 使用
- 监控系统内存并相应调整分段时长

### 磁盘空间
- 录像以 MP4 分段形式存储
- 清理按计划运行，并在达到磁盘阈值时运行
- `retention_days: 0` 默认为 30 天（不是"永久保留"）

### 文件存储
- 分段首先写入临时文件
- 最终分段使用原子文件操作防止损坏
- 数据库以 UTC 格式存储录像元数据和时间戳

### 密码哈希生成
使用以下命令生成 bcrypt 密码哈希：

```bash
mibee-nvr hash-password your-password
```

### 文件权限
确保配置文件权限适当：

```bash
chmod 600 config.yaml  # 仅所有者可读写
chown mibee:nvr config.yaml  # 设置合适的所有权
```