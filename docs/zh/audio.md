# 音频

> 适用于 MiBeeNvr v0.11.0

MiBee NVR 支持音频录制、实时试听和双向对讲功能。

## 支持的音频编码

| 编码 | 说明 | 录制 | 直播 | 对讲 |
|------|------|------|------|------|
| G.711 μ-law | 电话品质 | ✅ | ✅ | ✅ |
| G.711 A-law | 电话品质 | ✅ | ✅ | ✅ |
| AAC | 高品质 | ✅ | ✅ | — |
| Opus | 高品质低延迟 | ✅ | ✅ | ✅ |

## 音频配置

### 启用音频录制

摄像头配置中添加音频参数：

```yaml
cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    audio:
      enabled: true
      codec: "g711"               # g711 / aac / opus
      sample_rate: 8000           # 采样率（Hz）
      channels: 1                 # 声道数
    enabled: true
```

### 音频参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `enabled` | `false` | 是否启用音频 |
| `codec` | `g711` | 音频编码 |
| `sample_rate` | `8000` | 采样率（Hz） |
| `channels` | `1` | 声道数（1=单声道，2=立体声） |

## 实时试听

![直播页的试听与对讲控制](images/live-ptz.webp)

### Web UI 试听

1. 在 Web 界面打开摄像头
2. 点击「扬声器」图标
3. 音频会通过浏览器播放

> **注意**：音频延迟可能比视频高 100-300ms。

### API 试听

```bash
# 获取音频流
curl -u admin:password \
  "http://192.168.1.50:9090/api/v1/cameras/front-door/audio" \
  -o audio-stream.aac
```

## 双向对讲

### 启用对讲

```yaml
cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    audio:
      enabled: true
      codec: "g711"
      two_way: true               # 启用双向对讲
    enabled: true
```

### 对讲功能

- 在 Web UI 中点击「麦克风」图标开始对讲
- 支持浏览器麦克风输入
- 支持 G.711 和 Opus 编码

### 对讲参数

| 参数 | 说明 |
|------|------|
| 编码 | G.711（兼容性好）或 Opus（音质好） |
| 延迟 | 通常 200-500ms |
| 带宽 | G.711: 64kbps，Opus: 32-128kbps |

## 音频存储

音频与视频同步录制，存储在相同的 MP4 片段中：

```
recordings/
├── front-door/
│   ├── 2026-08-18/
│   │   ├── 00-00-00.mp4     # 包含视频和音频
│   │   ├── 00-01-00.mp4
│   │   └── ...
```

## 音频质量优化

### 带宽控制

```yaml
cameras:
  - id: "front-door"
    audio:
      enabled: true
      codec: "aac"
      bitrate: "64k"              # 音频码率
      sample_rate: 44100          # 高采样率
```

### 降噪

如果摄像头支持降噪，在摄像头端启用：

- ONVIF 摄像头：在摄像头 Web 管理界面启用降噪
- RTSP 摄像头：检查摄像头配置手册

### 音频同步

如果音频和视频不同步：

1. 检查摄像头的时间戳同步
2. 尝试重新连接摄像头
3. 在 NVR 配置中启用时间戳校正

```yaml
audio:
  enabled: true
  sync_correction: true          # 启用时间戳校正
```

## 常见问题

### 音频不录制

1. 确认摄像头支持音频（不是所有摄像头都有麦克风）
2. 确认 `audio.enabled: true`
3. 检查摄像头的音频编码格式是否在支持列表中
4. 查看日志是否有音频错误

### 对讲无声

1. 确认浏览器已授权麦克风
2. 确认摄像头支持音频输出（扬声器或音频输出端口）
3. 检查网络延迟（高延迟会影响对讲体验）

### 音频延迟

- 音频延迟通常比视频高
- 建议在需要严格同步的场景中使用 Opus 编码
- 降低音频采样率可以减少延迟

## 下一步

- [延时摄影](timelapse.md) — 延时摄影功能
- [转码](transcoding.md) — 视频转码
- [WebDAV / FTP 存储](webdav-ftp.md) — 访问录像文件
