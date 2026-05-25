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
    onvif_endpoint: ""           # ONVIF 特定
    profile_token: ""            # ONVIF 特定
    stream_encoding: ""          # ONVIF 自动检测 (H264/H265)
    sub_stream_url: "rtsp://..."  # 实时预览子流
    snapshot_url: "http://..."    # JPEG 快照缩略图
    sample_interval: 1            # MJPEG 帧采样间隔
    hls_max_fps: 0               # HLS 帧率限制
    vendor: ""                   # 小米传输供应商
    merge:                       # 每个摄像头合并配置覆盖
      enabled: false
      check_interval: "1h"
      window_size: "1h"
      batch_limit: 150
      min_segment_age: "5m"
      min_segments_to_merge: 2
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
  username: ""
  password: ""
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: false
hls:
  write_buffer_size: 100         # 每个流的异步帧缓冲区
  segment_max_size_mb: 10        # HLS 片段最大大小 (MB)
  segment_count: 7               # 每个流的片段数 (范围: 3-10)
  max_streams: 4                 # 最大并发流数 (范围: 1-20，RPi 限制: 4)
xiaomi:
  user_id: ""                    # 小米账户用户 ID (来自认证响应)
  token: ""                      #小米 passToken API 访问令牌
  region: "cn"                   # 区域代码: cn, sg, de 等
observability:
  log_level: "info"              # 日志级别: debug, info, warn, error
  log_format: "text"             # 日志格式: json 或 text
  enable_pprof: false            # 启用 pprof 调试端点
version: "1.0"
```

## 服务器配置

### `server.listen`
- **类型**: string
- **默认**: `":9090"`
- **描述**: Web 服务器监听地址和端口
- **示例**: `":8080"` 或 `"192.168.1.100:9090"`

## 存储配置

### `storage.root_dir`
- **类型**: string
- **默认**: `/var/lib/mibee-nvr`
- **描述**: 存储录像和临时文件的根目录
- **示例**: `/var/lib/mibee-nvr`

### `storage.segment_duration`
- **类型**: string
- **默认**: `"30s"`
- **描述**: 视频片段时长（内存密集型）
- **重要**: 每个片段在完成前会将所有视频数据保存在 RAM 中
- **内存使用**:
  - 30s 片段: ~15-20MB 每片段
  - 60s 片段: ~30-40MB 每片段
  - 120s 片段: ~60-80MB 每片段
- **RPi 限制**: 树莓派 3B 上最大 30 秒
- **示例**: `"30s"`, `"1m"`, `"5m"`

## 身份验证配置

### `auth.username`
- **类型**: string
- **必需**: 是（Web UI 和 FTP 需要）
- **描述**: 身份验证用户名
- **示例**: `"admin"`

### `auth.password_hash`
- **类型**: string
- **必需**: 是（Web UI 和 FTP 需要）
- **描述**: bcrypt 哈希密码。使用 `mibee-nvr hash-password <password>` 生成
- **优先级**: 如果同时设置了 `password` 和 `password_hash`，`password_hash` 优先
- **注意**: 如果只提供了 `auth.password`（明文），服务器会在首次启动时自动生成哈希值并写回到配置文件的 `password_hash`，然后清除 `password` 字段
- **示例**: `$2a$10$N9qo8uLOickgx2ZMRZoMy...`

### `auth.password`
- **类型**: string
- **可选**: 是
- **描述**: 明文密码，方便初始设置。首次运行时，服务器会自动哈希此值并写入到 `password_hash`，然后清除 `password` 字段
- **优先级**: 仅在 `password_hash` 为空时使用
- **示例**: `"admin123"`

## 摄像头配置

### 摄像头结构
每个摄像头配置需要这些基本字段：

```yaml
cameras:
  - id: "cam1"
    name: "摄像头名称"
    protocol: "rtsp"
    encoding: "h264"
    url: "摄像头地址"
    enabled: true
```

### `cameras[].id`
- **类型**: string
- **必需**: 是
- **描述**: 摄像头的唯一标识符（如果未提供则自动生成）
- **格式**: 字母数字，推荐用 kebab-case（例如 "front-door"）
- **示例**: `"front-door"`, `"cam-01"`

### `cameras[].name`
- **类型**: string
- **必需**: 是
- **描述**: 人类可读的摄像头名称
- **示例**: `"前门摄像头"`, `"后院"`

### `cameras[].protocol`
- **类型**: string
- **必需**: 是
- **描述**: 摄像头传输协议
- **选项**: `"rtsp"`, `"http"`, `"onvif"`, `"xiaomi"`
- **旧格式**: 也支持 `"rtsp_h264"`, `"rtsp_h265"`, `"rtsp_mjpeg"`, `"http_jpeg"`（自动解析为新格式）
- **注意**: 旧格式会自动解析为新协议+编码格式以保持向后兼容性
- **兼容性**: 两种格式都支持

### `cameras[].encoding`
- **类型**: string
- **可选**: 是（从旧协议自动检测或根据协议设置默认值）
- **描述**: 视频编码格式
- **选项**: `"h264"`, `"h265"`, `"mjpeg"`, `"jpeg"`
- **有效组合**:
  - `protocol: "rtsp"` → `encoding: "h264"`, `"h265"`, 或 `"mjpeg"`
  - `protocol: "http"` → `encoding: "jpeg"`
  - `protocol: "onvif"` → `encoding: "h264"` 或 `"h265"`（如果未指定则自动检测）
  - `protocol: "xiaomi"` → `encoding: "h264"` 或 `"h265"`（自动检测）

### `cameras[].url`
- **类型**: string
- **必需**: 是（ONVIF 和 Xiaomi 摄像头除外）
- **描述**: 摄像头地址或流端点
- **示例**:
  - RTSP: `"rtsp://192.168.1.100:554/stream"`
  - HTTP: `"http://192.168.1.101/capture"`
  - ONVIF: `"http://192.168.1.102:80/onvif/device_service"`（或使用 `onvif_endpoint`）
- **验证**: 必须有有效的协议（http/rtsp）和主机

### `cameras[].username`
- **类型**: string
- **可选**: 是
- **描述**: 摄像头身份验证用户名
- **示例**: `"admin"`

### `cameras[].password`
- **类型**: string
- **可选**: 是
- **描述**: 摄像头身份验证密码
- **示例**: `"摄像头密码"`

### `cameras[].enabled`
- **类型**: boolean
- **默认**: `true`
- **描述**: 是否启用摄像头录制
- **示例**: `true` 或 `false`

### `cameras[].onvif_endpoint`
- **类型**: string
- **可选**: 是（ONVIF 摄像头如果未提供 URL 则必需）
- **描述**: ONVIF 设备服务端点地址
- **示例**: `"http://192.168.1.100:80/onvif/device_service"`
- **注意**: 如果为 ONVIF 摄像头设置了 URL，会自动复制到 onvif_endpoint

### `cameras[].profile_token`
- **类型**: string
- **可选**: 是
- **描述**: ONVIF 媒体配置文件令牌，用于特定流选择
- **示例**: `"profile_1"`
- **注意**: 可选，如果未指定则使用默认配置文件

### `cameras[].stream_encoding`
- **类型**: string
- **可选**: 是
- **描述**: ONVIF 摄像头的流编码 (H264 或 H265)
- **选项**: `"H264"`, `"H265"`
- **注意**: 空 = 从 ONVIF 设备功能自动检测

### `cameras[].sub_stream_url`
- **类型**: string
- **可选**: 是
- **描述**: 实时 HLS 预览的低分辨率 RTSP 子流地址。配置后，仪表板将使用此流而不是主流，减少带宽使用
- **注意**: 子流必须使用与主流相同的编解码器（H.264/H.265）
- **示例**: `"rtsp://192.168.1.100:554/stream2"`

### `cameras[].snapshot_url`
- **类型**: string
- **可选**: 是
- **描述**: 返回 JPEG 快照图像的 HTTP 地址。配置后，仪表板将显示快照缩略图而不是实时 HLS 流，显著减少带宽
- **行为**: 快照缓存 10 秒；摄像头暂时无法访问时提供缓存的内容
- **示例**: `"http://192.168.1.100/snapshot"`, `"http://192.168.1.100/cgi-bin/snapshot.cgi"`

### `cameras[].sample_interval`
- **类型**: integer
- **可选**: 是
- **默认**: 1（仅限 MJPEG 摄像头）
- **描述**: MJPEG 帧采样间隔（秒）。较高的值可减少 CPU 使用率但降低帧率
- **示例**: `1`, `2`, `5`

### `cameras[].hls_max_fps`
- **类型**: integer
- **可选**: 是
- **默认**: 0（无限制）
- **描述**: HLS 流媒体的最大帧率。0 = 无限制
- **示例**: `30`, `15`, `25`

### `cameras[].vendor`
- **类型**: string
- **可选**: 是
- **描述**: 小米摄像头的传输供应商
- **选项**: `"cs2"`（默认）
- **示例**: `"cs2"`

### `cameras[].audio_enabled`

#ZV|- **类型**: boolean
#MK|- **默认**: `false`
#VB|- **描述**: 启用此摄像头的音频录制。启用后，录制器会从 RTSP/ONVIF/小米摄像头流中捕获音频并将其混入 MP4 录像
#QM|- **支持格式**: AAC（RTSP 摄像头）、G.711 μ-law/A-law（ONVIF/小米摄像头）
#ZX|- **注意**: MJPEG 和 HTTP-JPEG 摄像头不支持
#RN|- **示例**: `true`, `false


- **类型**: string
- **可选**: 是（小米摄像头必需）
- **描述**: 来自云服务的小米设备 ID
- **示例**: `"xiaomi_device_id_123"`

### `cameras[].merge`
- **类型**: object
- **可选**: 是
- **描述**: 每个摄像头合并配置覆盖
- **注意**: 只有非零字段会覆盖全局合并配置
- **示例**: 参见 [合并配置](#合并配置)

## 清理配置

### `cleanup.retention_days`
- **类型**: integer
- **默认**: 30
- **范围**: 1-3650
- **描述**: 删除超过 N 天的录像
- **示例**: `7`, `30`, `90`

### `cleanup.check_interval`
- **类型**: string
- **默认**: `"1h"`
- **描述**: 检查过期录像的频率
- **示例**: `"30m"`, `"1h"`, `"2h"`

### `cleanup.disk_threshold_percent`
- **类型**: integer
- **默认**: 95
- **范围**: 50-99
- **描述**: 当磁盘使用率超过 N% 时开始清理
- **示例**: `90`, `95`, `98`

## 合并配置

### `merge.enabled`
- **类型**: boolean
- **默认**: `false`
- **描述**: 启用片段合并功能

### `merge.check_interval`
- **类型**: string
- **默认**: `"1h"`
- **描述**: 检查合并候选者的频率
- **示例**: `"30m"`, `"1h"`, `"2h"`

### `merge.window_size`
- **类型**: string
- **默认**: `"1h"`
- **描述**: 片段合并的时间窗口（此窗口内的片段可以合并）
- **示例**: `"30m"`, `"1h"`, `"2h"`

### `merge.batch_limit`
- **类型**: integer
- **默认**: 200
- **描述**: 一次合并的最大片段数
- **示例**: `100`, `200`, `500`

### `merge.min_segment_age`
- **类型**: string
- **默认**: `"10m"`
- **描述**: 片段可以合并的最小时间
- **示例**: `"5m"`, `"10m"`, `"30m"`

### `merge.min_segments_to_merge`
- **类型**: integer
- **默认**: 3
- **描述**: 触发合并所需的最小片段数
- **示例**: `2`, `3`, `5`

## FTP 配置

### `ftp.enabled`
- **类型**: boolean
- **默认**: `true`
- **描述**: 启用 FTP 服务器

### `ftp.port`
- **类型**: integer
- **默认**: 2121
- **范围**: 1-65535
- **描述**: FTP 控制端口
- **示例**: `2121`, `990`

### `ftp.passive_port_range`
- **类型**: string
- **默认**: `"2122-2140"`
- **描述**: 被动模式端口范围（开始-结束）
- **示例**: `"2122-2140"`, `"40000-40100"`

## MQTT 配置

### `mqtt.enabled`
- **类型**: boolean
- **默认**: `false`
- **描述**: 启用 MQTT 客户端进行基于触发器的录制

### `mqtt.broker`
- **类型**: string
- **必需**: 是（如果启用）
- **描述**: MQTT 代理地址
- **示例**: `"tcp://localhost:1883"`, `"mqtt://192.168.1.100:1883"`

### `mqtt.topic`
- **类型**: string
- **必需**: 是（如果启用）
- **描述**: 订阅的 MQTT 主题（用于录制触发器）
- **示例**: `"mibeenr/trigger"`, `"cameras/front-door/record"`

### `mqtt.client_id`
- **类型**: string
- **默认**: `"mibee-nvr"`
- **描述**: MQTT 客户端标识符
- **示例**: `"mibee-nvr"`, `"nvr-client-01"`

### `mqtt.username`
- **类型**: string
- **可选**: 是
- **描述**: MQTT 代理身份验证用户名
- **示例**: `"mqtt-user"`, `"admin"`

### `mqtt.password`
- **类型**: string
- **可选**: 是
- **描述**: MQTT 代理身份验证密码
- **示例**: `"mqtt-password"`

## WebDAV 配置

### `webdav.enabled`
- **类型**: boolean
- **默认**: `true`
- **描述**: 启用 WebDAV 服务器

### `webdav.path_prefix`
- **类型**: string
- **默认**: `"/dav"`
- **描述**: WebDAV 访问的 URL 路径前缀
- **示例**: `"/dav"`, `"/recordings"`

### `webdav.read_write`
- **类型**: boolean
- **默认**: `false`
- **描述**: 允许写入操作（PUT、MKCOL、DELETE 等）
- **示例**: `true`, `false`

## HLS 配置

### `hls.write_buffer_size`
- **类型**: integer
- **默认**: 100
- **描述**: 每个流的异步帧缓冲区大小（帧数单位）
- **示例**: `40`, `100`, `200`

### `hls.segment_max_size_mb`
- **类型**: integer
- **默认**: 10
- **描述**: HLS 片段最大大小（兆字节）
- **示例**: `5`, `10`, `20`

### `hls.segment_count`
- **类型**: integer
- **默认**: 7
- **范围**: 3-10
- **描述**: 每个流的 HLS 片段数
- **示例**: `5`, `7`, `10`

### `hls.max_streams`
- **类型**: integer
- **默认**: 4
- **范围**: 1-20
- **RPi 限制**: 树莓派 3B 上最大 4
- **描述**: 最大并发 HLS 流数
- **示例**: `4`, `8`, `16`

## 小米配置

### `xiaomi.user_id`
- **类型**: string
- **必需**: 是（如果配置了小米摄像头）
- **描述**: 小米云账户用户 ID（认证后获得）
- **示例**: `"1234567890"`

### `xiaomi.token`
- **类型**: string
- **必需**: 是（如果配置了小米摄像头）
- **描述**: 小米 passToken API 访问令牌（通过 `/api/xiaomi/auth` 获得）
- **示例**: `"xiaomi_token_123"`

### `xiaomi.region`
- **类型**: string
- **默认**: `"cn"`
- **描述**: 小米云区域代码
- **选项**: `"cn"`, `"sg"`, `"de"` 等
- **示例**: `"cn"`, `"sg"`

## 可观察性配置

### `observability.log_level`
- **类型**: string
- **默认**: `"info"`
- **选项**: `"debug"`, `"info"`, `"warn"`, `"error"`
- **描述**: 日志详细程度
- **示例**: `"debug"`, `"info"`, `"error"`

### `observability.log_format`
- **类型**: string
- **默认**: `"text"`
- **选项**: `"json"`, `"text"`
- **描述**: 日志输出格式
- **示例**: `"json"`, `"text"`

### `observability.enable_pprof`
- **类型**: boolean
- **默认**: `false`
- **描述**: 启用 pprof 调试端点进行性能分析
- **注意**: 生产环境中请谨慎使用

## 摄像头协议示例

### RTSP 摄像头
```yaml
cameras:
  - id: "front-door"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    username: "admin"
    password: "摄像头密码"
    enabled: true
    sub_stream_url: "rtsp://192.168.1.100:554/stream2"
    snapshot_url: "http://192.168.1.100:8080/snapshot"
```

### HTTP JPEG 摄像头
```yaml
cameras:
  - id: "backyard"
    name: "后院摄像头"
    protocol: "http"
    encoding: "jpeg"
    url: "http://192.168.1.101/capture"
    sample_interval: 1
    enabled: true
```

### ONVIF 摄像头
```yaml
cameras:
  - id: "lobby"
    name: "大厅摄像头"
    protocol: "onvif"
    url: "http://192.168.1.102:80/onvif/device_service"
    enabled: true
    # 可选：指定编码
    encoding: "h264"
    # 可选：指定流编码
    stream_encoding: "H264"
```

### 小米摄像头
```yaml
xiaomi:
  user_id: "1234567890"
  token: "xiaomi_token_123"
  region: "cn"

cameras:
  - id: "xiaomi-cam"
    name: "小米摄像头"
    protocol: "xiaomi"
    encoding: "h264"
    did: "xiaomi_device_id"
    vendor: "cs2"
    enabled: true
```

## 从旧格式迁移

旧协议格式如 `"rtsp_h264"` 会自动转换为新的独立 `protocol` 和 `encoding` 字段：

```yaml
# 旧格式（仍然支持）
cameras:
  - id: "cam1"
    protocol: "rtsp_h264"
    url: "rtsp://..."

# 自动转换为新格式：
# protocol: "rtsp"
# encoding: "h264"
```

## 验证规则

配置在启动时会根据以下约束进行验证：

- **摄像头 ID**: 在所有摄像头中必须唯一
- **摄像头地址**: 必须有有效的协议（http/rtsp）和主机
- **ONVIF 摄像头**: 必须有 URL 或 onvif_endpoint
- **小米摄像头**: 必须配置 xiaomi.token
- **端口号**: 必须在 1-65535 范围内
- **片段时长**: RPi 3B 上最大 30 秒
- **保留天数**: 必须在 1-3650 之间
- **磁盘阈值**: 必须在 50% 到 99% 之间
- **合并配置**: 所有时间段字段必须有效
- **HLS 配置**:
  - 片段数: 3-10
  - 最大流数: 1-20（RPi 3B 上为 4）

## 文件路径和位置

- **默认配置路径**: `./mibee-nvr.yaml`
- **默认存储**: `/var/lib/mibee-nvr`
- **录像**: `{root_dir}/recordings/{encoding}/{camera_id}/`
- **片段**: `{root_dir}/recordings/{encoding}/{camera_id}/`
- **快照**: `{root_dir}/snapshots/{camera_id}/`
- **WebDAV**: `{root_dav}{root_dir}/`（其中 root_dav 是反向代理路径）

## 快速配置

### 基本设置
```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/var/lib/mibee-nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password: "你的密码"
cameras:
  - id: "cam1"
    name: "摄像头 1"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
cleanup:
  retention_days: 30
  disk_threshold_percent: 95
```

### 包含所有功能的完整设置
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
    name: "前门"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    sub_stream_url: "rtsp://192.168.1.100:554/sub"
  - id: "xiaomi-cam"
    name: "小米摄像头"
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
  topic: "mibeenr/trigger"
webdav:
  enabled: true
  read_write: false
hls:
  max_streams: 4
observability:
  log_level: "info"
```