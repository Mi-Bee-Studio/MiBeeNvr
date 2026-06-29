# 流媒体 API

## HLS 流媒体

**端点：** `GET /api/cameras/{id}/stream/*path`

提供按需 HLS 实时流媒体。

**请求（HLS 播放列表）：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream/stream.m3u8"
```

**请求（HLS 切片）：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream/segment_001.ts"
```

**响应：** HLS 播放列表或切片文件内容

### 停止 HLS 流

**端点：** `DELETE /api/cameras/{id}/stream`

停止摄像头的所有 HLS 流。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/front-door/stream"
```

**响应：**
```json
{
  "status": "stopped"
}
```

## WebRTC 流媒体

### 创建 WebRTC 会话（WHEP）

**端点：** `POST /api/cameras/{id}/stream/webrtc`

创建新的 WebRTC WHEP（WebRTC-HTTP Egress Protocol）会话。接受 SDP offer 并返回 SDP answer，同时在 Location 头中返回会话 URL。

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/sdp" \
  -d "$SDP_OFFER" \
  "http://localhost:9090/api/cameras/front-door/stream/webrtc"
```

**响应（201 Created）：** SDP answer，`Content-Type: application/sdp`，`Location: /api/cameras/{id}/stream/webrtc/{session}`

### 关闭 WebRTC 会话

**端点：** `DELETE /api/cameras/{id}/stream/webrtc/{session}`

拆除活跃的 WHEP 会话。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/front-door/stream/webrtc/session_123"
```

**响应：**
```json
{
  "status": "deleted"
}
```

## HTTP-FLV 流媒体

**端点：** `GET /api/cameras/{id}/stream.flv`

HTTP-FLV 实时流。通过 HTTP 提供浏览器兼容的 FLV 流媒体。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream.flv"
```

**响应：** FLV 二进制流，`Content-Type: video/x-flv`

## WebSocket 流媒体

**端点：** `GET /api/cameras/{id}/stream/ws`

WebSocket 实时流。升级为 WebSocket 连接，用于实时二进制帧流传输（支持视频和音频）。

### 音频模式

使用 `?audio_only=1` 查询参数可获取纯音频流，适用于需要单独音频数据的应用场景（例如配合 HLS/FLV/WebRTC 视频播放器同时获取音频）。

**音频模式请求：**
```bash
wscat -c "ws://localhost:9090/api/cameras/front-door/stream/ws?audio_only=1" \
  -H "Authorization: Basic $(echo -n 'username:password' | base64)"
```

### 音频线缆格式

WebSocket 音频流使用自定义二进制协议，包含以下帧类型：

**1. AudioCodecInfo (0x05)**

在连接建立且摄像头配置了音频时发送一次。

格式：`{type:1}{audio_codec:1}{sample_rate:4_BE}{channels:1}`

- `type`: 帧类型，固定为 `0x05`
- `audio_codec`: 音频编解码器类型
  - `0x01` = G.711 μ-law
  - `0x02` = G.711 A-law
  - `0x03` = Opus
  - `0x04` = AAC
- `sample_rate`: 采样率（大端序 4 字节）
- `channels`: 声道数

**2. AudioFrame (0x03)**

包含实际的音频数据帧。

格式：`{type:1}{pts:8_BE}{codec:1}{data_len:4_BE}{data}`

- `type`: 帧类型，固定为 `0x03`
- `pts`: 显示时间戳（大端序 8 字节）
- `codec`: 编解码器类型（同上）
- `data_len`: 数据长度（大端序 4 字节）
- `data`: 原始编解码器数据（G.711 采样、Opus 数据包或 AAC 帧）

**3. EOS (0xFF)**

流结束标记，表示音频流已终止。

### 认证

WebSocket 支持通过 `?token=<base64>` 查询参数进行身份验证，作为 HTTP Basic Auth 头的替代方案。

**使用 token 认证：**
```bash
wscat -c "ws://localhost:9090/api/cameras/front-door/stream/ws?token=$(echo -n 'username:password' | base64)"
```

### 支持的音频编解码器

- **G.711 μ-law**: 传统电话音频，采样率 8kHz，单声道。浏览器端通过 Web Audio API 的 G.711 查找表解码。
- **G.711 A-law**: 欧洲/亚洲电话标准，采样率 8kHz，单声道。浏览器端通过 Web Audio API 的 G.711 查找表解码。
- **Opus**: 低延迟高质量音频编解码器，支持各种采样率和声道配置。浏览器端通过 WebCodecs API 或 OpusDecoder 解码。
- **AAC**: 高效压缩音频，常用于 MP4 容器。浏览器端通过 WebCodecs AudioDecoder 或原生 Audio 元素解码。

### 使用示例

**JavaScript 客户端示例：**

```javascript
const cameraId = 'front-door';
const username = 'admin';
const password = 'password';
const authToken = btoa(`${username}:${password}`);

const ws = new WebSocket(`ws://localhost:9090/api/cameras/${cameraId}/stream/ws?token=${authToken}`);

ws.binaryType = 'arraybuffer';
ws.onopen = () => {
  console.log('WebSocket 连接已建立');
};

ws.onmessage = (event) => {
  const data = new Uint8Array(event.data);
  const frameType = data[0];
  
  if (frameType === 0x05) {
    // AudioCodecInfo 帧解析
    const codec = data[1];
    const sampleRate = new DataView(data.buffer).getUint32(2, false); // 大端序
    const channels = data[6];
    console.log(`音频编解码器: ${getCodecName(codec)}, 采样率: ${sampleRate}Hz, 声道数: ${channels}`);
  } else if (frameType === 0x03) {
    // AudioFrame 帧解析
    const pts = new DataView(data.buffer).getBigUint64(1, false); // 大端序
    const codec = data[9];
    const dataLen = new DataView(data.buffer).getUint32(10, false); // 大端序
    const audioData = data.slice(14, 14 + dataLen);
    console.log(`音频帧: PTS=${pts}, 编解码器=${getCodecName(codec)}, 数据长度=${dataLen}`);
    // 在此处处理音频数据（解码、播放等）
  } else if (frameType === 0xFF) {
    // EOS 流结束
    console.log('音频流已结束');
    ws.close();
  }
};

function getCodecName(codec) {
  switch (codec) {
    case 0x01: return 'G.711 μ-law';
    case 0x02: return 'G.711 A-law';
    case 0x03: return 'Opus';
    case 0x04: return 'AAC';
    default: return '未知';
  }
}
```

**音频模式专用客户端示例：**

```javascript
const ws = new WebSocket(`ws://localhost:9090/api/cameras/${cameraId}/stream/ws?audio_only=1&token=${authToken}`);
// 其余处理逻辑与上述相同，但只会收到音频帧
```

**响应：** WebSocket 升级。根据请求模式返回视频数据或音频数据的二进制帧。

## 摄像头协议

**端点：** `GET /api/cameras/{id}/protocols`

获取指定摄像头的可用流媒体协议，基于其编码格式和已注册的流处理器。返回协议列表、编码格式和默认协议。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/protocols"
```

**响应：**
```json
{
  "protocols": [
    {
      "protocol": "webrtc",
      "label": "WebRTC (WHEP)",
      "available": true
    },
    {
      "protocol": "flv",
      "label": "HTTP-FLV",
      "available": true
    },
    {
      "protocol": "hls",
      "label": "HLS",
      "available": true
    },
    {
      "protocol": "ws",
      "label": "WebSocket",
      "available": true
    }
  ],
  "encoding": "h264",
  "default": "webrtc"
}
```
