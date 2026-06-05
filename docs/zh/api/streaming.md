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

WebSocket 实时流。升级为 WebSocket 连接，用于实时二进制帧流传输。

**请求：**
```bash
# 使用 WebSocket 客户端
wscat -c "ws://localhost:9090/api/cameras/front-door/stream/ws" \
  -H "Authorization: Basic $(echo -n 'username:password' | base64)"
```

**响应：** WebSocket 升级。包含视频数据的二进制帧。

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
