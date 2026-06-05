# Streaming API

## HLS Streaming

**Endpoint:** `GET /api/cameras/{id}/stream/*path`

Provide on-demand HLS live streaming.

**Request (HLS playlist):**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream/stream.m3u8"
```

**Request (HLS segment):**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream/segment_001.ts"
```

**Response:** HLS playlist or segment file content

### Stop HLS Stream

**Endpoint:** `DELETE /api/cameras/{id}/stream`

Stop all HLS streams for a camera.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/front-door/stream"
```

**Response:**
```json
{
  "status": "stopped"
}
```

## WebRTC Streaming

### Create WebRTC Session (WHEP)

**Endpoint:** `POST /api/cameras/{id}/stream/webrtc`

Create a new WebRTC WHEP (WebRTC-HTTP Egress Protocol) session. Accepts an SDP offer and returns an SDP answer with a session URL in the Location header.

**Request:**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/sdp" \
  -d "$SDP_OFFER" \
  "http://localhost:9090/api/cameras/front-door/stream/webrtc"
```

**Response (201 Created):** SDP answer with `Content-Type: application/sdp` and `Location: /api/cameras/{id}/stream/webrtc/{session}`

### Close WebRTC Session

**Endpoint:** `DELETE /api/cameras/{id}/stream/webrtc/{session}`

Tear down an active WHEP session.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/front-door/stream/webrtc/session_123"
```

**Response:**
```json
{
  "status": "deleted"
}
```

## HTTP-FLV Streaming

**Endpoint:** `GET /api/cameras/{id}/stream.flv`

HTTP-FLV live stream. Provides browser-compatible FLV streaming over HTTP.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream.flv"
```

**Response:** FLV binary stream with `Content-Type: video/x-flv`

## WebSocket Streaming

**Endpoint:** `GET /api/cameras/{id}/stream/ws`

WebSocket live stream. Upgrades to a WebSocket connection for real-time binary frame streaming.

**Request:**
```bash
# Use a WebSocket client
wscat -c "ws://localhost:9090/api/cameras/front-door/stream/ws" \
  -H "Authorization: Basic $(echo -n 'username:password' | base64)"
```

**Response:** WebSocket upgrade. Binary frames containing video data.

## Camera Protocols

**Endpoint:** `GET /api/cameras/{id}/protocols`

Get the available streaming protocols for a specific camera, based on its encoding and registered stream handlers. Returns protocols, encoding, and the default protocol.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/protocols"
```

**Response:**
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
