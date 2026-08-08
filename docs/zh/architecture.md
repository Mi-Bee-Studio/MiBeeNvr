# 架构:推流接入 (Push-In Ingest) 与推流转发 (Push-Out Relay)

> MiBee NVR 如何跨网络连接摄像头 —— 原生 Go 实现,无需 FFmpeg。

本文档介绍 v0.8.0 引入的两个流媒体子系统,使 NVR 能够跨越网络边界运行:

1. **Push-In(接入)** —— 远端推流者将流推送 INTO NVR(SRT/RTMP)。NVR 像对待任何摄像头一样录制并提供该流的直播。
2. **Push-Out(转发)** —— NVR 将摄像头的直播流转发 OUT 到远端目标(RTMP/RTSP)。纯 Go 实现,无外部进程。

二者均复用现有的 `StreamHub` 帧总线,因此无需改动消费者即可插入到录制 + 直播(HLS/WebRTC/FLV/WS)流水线中。

---

## 1. 共享基础:StreamHub

每个摄像头都持有一个 `*model.StreamHub` —— 一个帧扇出总线。生产者调用 `hub.Broadcast(pts, au, isIDR)`;消费者调用 `hub.Subscribe(id, callback)`。hub 在一个独立的 goroutine 中运行每个消费者的回调(非阻塞;缓冲区满时丢弃)。

```
                     ┌─────────────────────────────────────────┐
                     │              StreamHub                   │
   RTSP recorder ──▶ │  Broadcast(pts, au, isIDR)              │ ──▶ HLS muxer
   ONVIF recorder ──▶ │                                         │ ──▶ WebRTC
   IngestRecorder ──▶ │  Subscribe("hls", cb)                   │ ──▶ FLV
                     │  Subscribe("webrtc-<id>", cb)            │ ──▶ WebSocket
                     │  Subscribe("relay-rtmp-<id>", cb)  ◀── NEW (push-out)
                     └─────────────────────────────────────────┘
```

**中央 hub 注册表**(`CameraManager.hubRegistry`,`GetHub(id)` / `GetOrCreateHub(id)`)是唯一的真相来源:拉流录像器、接入服务器和转发目标对于同一摄像头都引用同一个 hub 对象。

---

## 2. Push-In(接入) —— `internal/recorder/ingest.go`

远端推流者(ffmpeg、OBS、手机、另一台 NVR)将流推送到 NVR 的 SRT 监听器或 RTMP 服务器。该流即成为一个完整的摄像头。

### 组件

| 组件 | 文件 | 职责 |
|-----------|------|------|
| SRT 监听器 | `internal/srt/listener.go` | 接受 SRT 推流;将 `streamid` 映射为摄像头 ID |
| RTMP 服务器 | `internal/rtmp/server.go` | 接受 RTMP 推流;将流密钥映射为摄像头 ID |
| IngestRecorder | `internal/recorder/ingest.go` | 推流摄像头的录像器:录制滚动 MP4 + 为 hub 提供数据 |

### 数据流(RTMP 示例)

```
Publisher ──RTMP──▶ RTMP Server (handlePublisher)
                       │  OnDataH264(au, pts)
                       ├─▶ NALUProvider ──▶ IngestRecorder.WriteNALU(au, pts, isIDR)
                       │                       ├─▶ hub.Broadcast (live consumers)
                       │                       ├─▶ SPS/PPS capture + IDR-gated MP4 segment
                       │                       └─▶ SegmentStore → RecordingDB → SegmentCompleted event
                       └─▶ hub.Broadcast (legacy path)
```

### 关键设计要点

- **IngestRecorder 实现了 `model.Recorder`**(Start/Stop/Status)+ **`HLSProvider`**(CodecParams),因此它可零改动地插入现有的 CameraManager 和 HLS/FLV/WS 处理器。
- **生命周期**:`Idle(等待推流者)` → `Recording(推流者已连接)` → `Idle(推流者已断开)`。Idle 被建模为 `StatusReconnecting`,以避免引入新的状态常量。
- **SPS/PPS 注入**:RTMP 以带外方式(AVCDecoderConfigurationRecord)承载 SPS/PPS。服务器在 VCL 帧之前将其喂给 IngestRecorder,IngestRecorder 在 IDR 广播前预置它们,以便下游 muxer(gohlslib DTS 提取器)能在带内看到它们 —— 与 RTSP 路径保持一致。
- **分片滚动**:镜像 H264Recorder —— SPS 变化检测、IDR 门控的分片起始、基于时长的滚动、原子的 temp→rename。

### Push-in 保存策略

按摄像头的 `push_retention_days`:`nil` = 跟随全局设置,`0` = 仅直播(不录制),`N` = 保留 N 天。

---

## 3. Push-Out(转发) —— `internal/relay/`

NVR 将摄像头的直播流转发到远端 RTMP/RTSP 目标。RTSP 目标使用 `gortsplib.Client`（纯 Go，零拷贝转封装）；RTMP 目标使用**自定义握手 + 发布层**（`rtmp_client.go`），解决了严格接收端（斗鱼/虎牙/哗哩哗啦）所需的六大 FMS 兼容根因（HMAC digest、chunk size、Type 0 header、完整 `onMetaData`、大端序 streamID）——转封装无需 FFmpeg。H.265 源在 `TranscodePolicy` ≠ `off` 时经 `livetranscode`（FFmpeg 子进程）转码为 H.264。

### 组件

| 组件 | 文件 | 职责 |
|-----------|------|------|
| PushTarget | `internal/relay/engine.go` | 每个目标一个;订阅摄像头 hub,将帧写入目标,重连循环 |
| Manager | `internal/relay/manager.go` | 拥有所有目标;配置差异调和、状态聚合、生命周期 |
| Status | `internal/relay/status.go` | `RelayStatus`(区别于 `RecorderStatus`)+ `TargetStatus`(JSON) |

### 数据流

```
Camera StreamHub ──▶ Subscribe("relay-rtmp-<id>", cb)
                      │  cb(pts, au)
                      ▼
                   PushTarget.connectRTMP/connectRTSP
                      │
           ┌──────────┴──────────┐
           ▼                     ▼
     rtmpPublishConn       gortsplib.Client
     (自定义握手,         .WritePacketRTP(media, pkt)
      Type 0 发布)          (rtpEnc.Encode(au))
           │                     │
           ▼                     ▼
     RTMP target            RTSP target
     (remote NVR /          (remote NVR /
      live platform)         backup)
```

### 关键设计要点

- **源端 = 零拷贝**:PushTarget 订阅摄像头现有的 hub。无需重新拉流,无需解码。与 HLS/WebRTC/录制使用相同的帧总线 —— 新增一个转发目标仅增加一个 goroutine + 一个出站 socket,在 RPi 3B 上约 5-10MB。
- **H.264 转封装或 H.265 转码**:H.264 源零拷贝转封装。H.265 源在 `TranscodePolicy` ≠ `off` 时经 `livetranscode.LiveTranscoder`(FFmpeg 子进程)实时转码为 H.264;若为 `off`,H.265 以 `errPermanent` 拒绝。热监控保护 ARM SBC。参见[转发指南](relay-guide.md#h265-transcoding)。
- **每个目标相互独立**:每个目标是独立的 goroutine + 连接 + 重连循环(`TieredBackoffWithJitter`)。某一个目标的失败绝不会影响其他目标、录制或直播。
- **专用的 `RelayStatus`**:不是 `RecorderStatus`。"向目标推流" ≠ "录制到磁盘" —— 摄像头健康界面绝不能将两者混为一谈。
- **调和是异步的**:`SetCameraTargets` 在 goroutine 中运行,这样 AddCamera/UpdateCamera 的 API 响应不会被 relay 引擎的拆卸阻塞。(GetHub 现在是无锁的快照读取,不再有锁重入问题。)
- **从源端获取 SPS/PPS**:`camMgr.GetSPS(cameraID)` 返回源端的 SPS/PPS,用于目标轨道初始化。

### 为什么不用 FFmpeg?

| | FFmpeg | 原生 Go 转发 |
|---|---|---|
| 二进制 | 外部进程(约 50MB) | 内嵌于单一静态二进制 |
| RPi 3B 内存 | 每进程约 30-50MB | 约 5-10MB(一个 goroutine + socket) |
| 交叉编译 | 需单独构建 ARM ffmpeg | `CGO_ENABLED=0`,无变化 |
| 可靠性 | 崩溃需重启脚本 | NVR 内置重连/退避 |
| 转码 | ✅(H.265→H.264) | ❌(仅重封装) |

转发以 10 倍更低的成本覆盖了常见场景(跨网络的 H.264 摄像头)。转码则继续保留在其 FFmpeg feature flag 之后,以应对罕见的 H.265→RTMP 场景。

---

## 4. 配置

### Push-in(摄像头协议 `srt` 或 `rtmp`)

```yaml
cameras:
  - id: "remote-shop"
    name: "Remote Shop"
    protocol: "rtmp"
    encoding: "h264"
    stream_key: "remote-shop"       # maps rtmp://NVR:1935/live/remote-shop
    push_retention_days: 7          # nil=global, 0=live-only, N=days
    enabled: true
```

### Push-out(任意摄像头)

```yaml
cameras:
  - id: "front-door"
    protocol: "rtsp"
    url: "rtsp://192.168.1.50/stream"
    push_targets:
      - id: "backup-nvr"
        name: "Backup NVR"
        protocol: "rtmp"
        url: "rtmp://backup.example.com:1935/live/front-door"
        enabled: true
      - id: "live-platform"
        name: "Live Platform"
        protocol: "rtsp"
        url: "rtsp://live.example.com:8554/front-door"
        enabled: false
```

### 启用接入服务器

```yaml
srt:
  enabled: true
  port: 9000
rtmp:
  enabled: true
  port: 1935
```

---

## 5. API

- `POST /api/cameras` / `PUT /api/cameras/{id}` —— 接受 `push_targets[]` 和 `push_retention_days`。
- `GET /api/cameras/{id}/push-status` —— 返回各目标的实时状态:
  ```json
  {
    "camera_id": "front-door",
    "targets": [{
      "id": "backup-nvr",
      "name": "Backup NVR",
      "protocol": "rtmp",
      "status": "streaming",
      "kbps": 270.8,
      "enabled": true,
      "uptime": "1m16s"
    }]
  }
  ```

---

## 6. 网络拓扑示例

```
A) Push-in: remote camera → NVR
   [Remote Cam/ffmpeg] ──push RTMP/SRT──▶ [NVR (public IP / port-forwarded)]

B) Push-out: NVR → remote destination
   [NVR + camera] ──relay RTMP/RTSP──▶ [Remote NVR ingest / live platform]

C) Chained (NVR-to-NVR, the cross-network scenario):
   [Camera] ──RTSP──▶ [NVR-A] ──push-out relay──▶ [NVR-B (push-in ingest)] ──▶ records + live
```

---

## 7. 音频流水线

### 7.1 概述

音频从摄像头采集开始，经过两条并行路径（录制 + 实时预览）。支持 G.711 μ-law/A-law、AAC、Opus 编解码器。原始编码字节同时写入两条路径，不做转码，既保留了音质又最小化服务器 CPU 开销。

### 7.2 编解码器检测

每种摄像头类型的音频检测方式：

| 摄像头类型 | 检测方式 | 编解码器映射 |
|-----------|---------|------------|
| RTSP 摄像头 | gortsplib 解析 SDP | PayloadType 0 = PCMU (μ-law)，PT 8 = PCMA (A-law)，PT 96+ = AAC |
| Xiaomi 摄像头 | MISS 协议 codec ID | 1026=PCMU，1027=PCMA，1032=Opus |

**检测后设置**：
- `g711MULaw` 标志（区分 μ-law 和 A-law）
- `g711SampleRate`（通常 8000 Hz）
- `audioMuxerConfig` 字节（MP4 box 配置）

G.711 使用 `rtplpcm.Decoder`（返回原始 8-bit 字节，不做解压），AAC 使用 `rtpmpeg4audio.Decoder`。PCMA/PCMU 统一映射到 `model.AudioG711`。

### 7.3 双路径架构

原始编码字节通过 StreamHub 同时写入两个消费者：

1. **录制路径**：`muxer.WriteAudioSample()` → 原始字节存入 MP4 分片
   - MP4 box 类型：`ulaw`/`alaw`（G.711，8-bit sample size）、`mp4a`+`esds`（AAC）、`Opus`+`dOps`（Opus，48kHz timescale）
   - 无转码 — 原始编解码器数据直接透传

2. **实时预览路径**：`hub.BroadcastAudio()` → wsstream → WebSocket 二进制帧 → 前端 JS 解码

**关键点**：相同的原始字节流向两条路径。音质差异来自解码器，而非数据本身。

### 7.4 录制回放（清晰音频）

回放录制文件时，浏览器原生 `<video>`/`<audio>` 元素或 HLS 播放器解码 MP4 音频轨道。对于 G.711，浏览器读取 `ulaw`/`alaw` box 描述符并使用内置 G.711 解码器。这是经过充分测试的原生代码路径 → 音频清晰。

### 7.5 实时预览（WebSocket 音频）

**Wire protocol**（二进制 WebSocket 帧）：

- **AudioCodecInfo**（type 0x05，发送一次）：`{type:1}{codec:1}{sample_rate:4_BE}{channels:1}` — 共 7 字节
  - Codec 字节：0x01=μ-law，0x02=A-law，0x03=Opus，0x04=AAC

- **AudioFrame**（type 0x03，每个 RTP 包一帧）：`{type:1}{pts:8_BE}{codec:1}{data_len:4_BE}{data}` — 14 + data_len 字节
  - Payload 为原始编解码器字节（从 StreamHub 无变换）

**端点**：`GET /api/cameras/{id}/stream/ws?audio_only=1` — 纯音频模式完全跳过视频帧。

### 7.6 前端 G.711 解码

前端使用标准 ITU-T G.711 256 项查找表将 8-bit 压缩样本转换为 16-bit 线性 PCM：

- **μ-law**（`decodeMuLaw`）：表直接用原始字节索引。ITU-T G.711 要求的按位 NOT（位翻转）已内置在表值中。最大输出：±32124（完整 16-bit 范围）。
- **A-law**（`decodeALaw`）：表直接用原始字节索引。ITU-T G.711 要求的 XOR 0x55 已内置在表值中。最大输出：±32256。
- PCM 样本通过 `pcm[i] / 32768` 归一化为 Float32 [-1.0, +1.0]。
- **关键**：查找表来源于经过充分测试的参考实现（NAudio、Wireshark、janus-gateway）。位翻转变换绝不能由调用方额外执行 — 它已包含在表值中。

**文件**：
- `web/src/lib/g711-decoder.ts`（表 + 解码函数）
- `web/src/lib/audio-player.ts`（Web Audio API 播放）

### 7.7 Web Audio API 播放

- **AudioContext 以流采样率创建**（`new AudioContext({ sampleRate: 8000 })`），而非浏览器默认值（48000）。这避免了缓冲边界的逐缓冲重采样伪影。
- 每个音频帧创建一个 `AudioBuffer`，填充解码后的 Float32 样本。
- **无缝调度**：通过 `_nextTime` 跟踪串联 `AudioBufferSourceNode` 实例。每个缓冲持续时间 = `frameCount / sampleRate`（G.711 通常 20ms）。如果调度超前 >1s，`_nextTime` 重置以防内存堆积。
- AudioContext 在用户点击时创建（浏览器自动播放策略）。由 `CameraAudioButton.svelte` 处理。
- **实时预览三种编码均已支持解码**（#131）：G.711 经 ITU-T 查找表；AAC 经 WebCodecs `AudioDecoder`（HTTPS/localhost）或 FAAD2 WASM（明文 HTTP）；Opus 经 WebCodecs `OpusDecoder`。见 `web/src/lib/audio-player.ts` 与 `web/src/lib/decoders/`。（早期仅支持 G.711，AAC/Opus 实时预览在 #131 加入。）

### 7.8 组件表格

| 组件 | 位置 | 职责 |
|-----------|----------|------|
| 音频检测 | Recorder 实现 | 从 RTSP SDP (G.711 μ-law/A-law) 或 Xiaomi MISS 协议 (G.711/Opus) 检测音频轨道 |
| 音频复用 | `internal/muxer/` | MP4 分片包含音频轨道 (AAC、G.711、Opus sample entry) |
| 音频合并 | `internal/merge/` | 分片合并期间保留音频轨道 (检测 `ulaw`/`alaw`/`Opus` box 类型) |
| 音频流式传输 | `internal/wsstream/` | 通过 `?audio_only=1` 端点进行 WebSocket 音频流式传输 |
| 音频回放 | 浏览器 | 通过标准 ITU-T 查找表 + Web Audio API 以原始采样率解码 G.711 |

### 7.9 数据流图

```
Camera (RTSP SDP / Xiaomi MISS)
    │  检测音频轨道 (G.711 μ-law, A-law, Opus)
    ▼
Recorder (audio_enabled 标志)
    │  通过 StreamHub 广播音频帧
    ▼
StreamHub
    │  扇出到所有消费者 (录制、直播、合并)
    ▼
MP4 Muxer (录制)
    │  写入音频轨道 (AAC/G.711/Opus sample entry)
    ▼
Segment Merge
    │  检测音频 box (ulaw/alaw/Opus)
    │  在合并的 MP4 中保留音频
    ▼
WebSocket Manager (实时预览)
    │  发送 AudioCodecInfo (0x05) + AudioFrame (0x03)
    ▼
Browser
    │  G.711 解码器 (ITU-T 标准 256 项查找表)
    │  AudioContext 以流采样率运行 (8kHz)
    │  Web Audio API 无缝播放
```

### 7.10 关键设计要点

- **标准 ITU-T 查找表**：G.711 查找表必须使用标准值（NAudio/Wireshark 参考）。位翻转（μ-law 的 NOT、A-law 的 XOR 0x55）已内置在表中 — 调用方直接用原始字节索引。
- **AudioContext 采样率匹配**：AudioContext 必须以流的原生采样率（G.711 为 8000 Hz）创建。使用浏览器默认值（48000 Hz）会导致逐缓冲重采样伪影。
- **无服务端解码**：所有 G.711 解码在浏览器中进行。后端直接透传原始编解码器字节，不做变换。
- **录制与实时预览的差异**：录制回放使用浏览器原生 MP4 音频解码器（经过充分测试，清晰）。实时预览使用 JS 查找表 + Web Audio API（需要正确的表和采样率匹配）。
- **AAC/Opus 实时预览解码（#131 已支持）**：实时预览现支持全部三种编码——G.711 经查找表；AAC 经 WebCodecs `AudioDecoder`（HTTPS/localhost）或 FAAD2 WASM（明文 HTTP）；Opus 经 WebCodecs `OpusDecoder`（见 `web/src/lib/decoders/`）。录制回放使用浏览器原生 MP4 解码器。
- **按摄像头控制**：每个摄像头都有 `audio_enabled` 标志 (默认: false) 用于录制。
- **格式保留**：合并流水线保留音频轨道，并使用特定于编解码器的 sample entry (`writeMergeG711SampleEntry`, `writeMergeOpusSampleEntry`)。
- **协议支持**：所有四种流媒体协议 (WebSocket、FLV、HLS、WebRTC) 都通过共享的音频 WebSocket 端点支持音频。
