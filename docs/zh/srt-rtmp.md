# SRT / RTMP 推流接入

> 适用于 MiBeeNvr v0.11.0

MiBee NVR 支持 SRT 和 RTMP 两种推流协议接入摄像头，适用于跨网络、跨平台的视频录制场景。

## 协议对比

| 特性 | SRT | RTMP |
|------|-----|------|
| 传输协议 | UDP | TCP |
| 延迟 | 低（<1s） | 中（1-3s） |
| 穿透 NAT | ✅ 好 | ⚠️ 一般 |
| 加密 | ✅ AES-128/256 | ❌ 无 |
| 错误恢复 | ✅ ARQ 重传 | ❌ 无 |
| 适用场景 | 跨网络、户外 | 局域网、直播平台 |

## SRT 推流接入

![设置中的直播（SRT/RTMP）开关](images/settings-streaming.webp)

### 启用 SRT 服务器

```yaml
srt:
  enabled: true
  port: 9000
  passphrase: "your-secret-key"    # 可选，启用加密
  latency: "200ms"                  # 可选，默认 200ms
```

### 摄像头端推流

摄像头或编码器需要配置推流到 NVR：

```text
SRT URL: srt://你的NVR地址:9000?streamid=#!:r=摄像头ID,m=publish
```

例如：

```bash
# FFmpeg 推流测试
ffmpeg -re -i input.mp4 -c:v libx264 -f mpegts \
  "srt://192.168.1.50:9000?streamid=#!:r=test-cam,m=publish"
```

### Web UI 添加 SRT 摄像头

1. 进入「设置」→「摄像头管理」→「添加摄像头」
2. 选择协议为 **SRT**
3. 输入推流地址
4. 点击「保存」

### 推流参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `port` | `9000` | SRT 监听端口 |
| `passphrase` | — | 加密密钥（空=不加密） |
| `latency` | `200ms` | 延迟设置 |
| `maxbw` | `0` | 最大带宽（0=自动） |

### 加密配置

启用加密后，推流端必须提供相同的密钥：

```yaml
srt:
  enabled: true
  port: 9000
  passphrase: "my-secret-key-123"   # 最少 10 个字符
```

推流端：

```bash
ffmpeg -re -i input.mp4 -c:v libx264 -f mpegts \
  "srt://192.168.1.50:9000?streamid=#!:r=test-cam,m=publish&passphrase=my-secret-key-123"
```

## RTMP 推流接入

### 启用 RTMP 服务器

```yaml
rtmp:
  enabled: true
  port: 1935
```

### 摄像头端推流

摄像头或编码器配置 RTMP 推流：

```text
RTMP URL: rtmp://你的NVR地址:1935/live/摄像头ID
```

例如：

```bash
# FFmpeg 推流测试
ffmpeg -re -i input.mp4 -c:v libx264 -c:a aac -f flv \
  "rtmp://192.168.1.50:1935/live/test-cam"
```

### Web UI 添加 RTMP 摄像头

1. 进入「设置」→「摄像头管理」→「添加摄像头」
2. 选择协议为 **RTMP**
3. 输入推流地址
4. 点击「保存」

### 推流参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `port` | `1935` | RTMP 监听端口 |
| `chunk_size` | `4096` | 分块大小 |

## 推流转发（Push-Out）

MiBee NVR 还支持将摄像头直播流转发到远端目标：

### 转发到直播平台

```yaml
cameras:
  - id: "outdoor-cam"
    name: "户外摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    push_out:
      - protocol: "rtmp"
        url: "rtmp://live.example.com/live/stream-key"
      - protocol: "srt"
        url: "srt://remote-server:9000?streamid=#!:r=live-stream,m=publish"
```

### 转发参数

| 参数 | 说明 |
|------|------|
| `protocol` | 转发协议（`rtmp` 或 `srt`） |
| `url` | 转发目标地址 |
| `passphrase` | SRT 加密密钥（SRT 转发时） |

## 常见问题

### SRT 连接超时

1. **防火墙**：确保 UDP 端口 9000 已开放
2. **NAT 穿透**：SRT 通常能穿透 NAT，但极端网络环境可能失败
3. **延迟设置**：增加 `latency` 值（如 `500ms`）

### RTMP 推流中断

- RTMP 基于 TCP，网络不稳定时可能中断
- 推流端通常会自动重连
- 如果频繁中断，考虑切换到 SRT

### 视频质量问题

- 推流质量取决于摄像头编码设置和网络带宽
- 建议 1080p 摄像头推流码率 4-8 Mbps
- 4K 摄像头推流码率 10-20 Mbps

## 下一步

- [树莓派摄像头接入](raspberrypi.md) — libcamera 配置
- [录制与回放](recording-playback.md) — 录像管理
- [延时摄影](timelapse.md) — 延时摄影功能
