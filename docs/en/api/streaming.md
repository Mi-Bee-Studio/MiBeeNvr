# Streaming API

## HLS Streaming

**Endpoint:** `GET /api/cameras/{id}/stream/*path`

Provide on-demand HLS live streaming.

When the playlist is requested with a session token, the response also sets a
scoped `mbs_session` cookie — for players that cannot set per-request headers
(iOS AVPlayer, some native players) to fetch media segments without 401s
(new in 0.11.0).

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

G.711 (PCMU/PCMA) and Opus audio are muxed directly into the WebRTC track with
zero transcoding (since 0.11.0); AAC live audio still uses the separate audio
WebSocket endpoint.

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

WebSocket live stream. Upgrades to a WebSocket connection for real-time binary frame streaming with support for both video and audio.

### Request

```bash
# Use a WebSocket client
wscat -c "ws://localhost:9090/api/cameras/front-door/stream/ws" \
  -H "Authorization: Basic $(echo -n 'username:password' | base64)"
```

### Query Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `audio_only` | integer | Set to `1` to receive audio-only frames (no video). Used by HLS/FLV/WebRTC players to get audio alongside their video protocol. |
| `token` | string | Base64-encoded authentication token as alternative to `Authorization` header (required for browser WebSocket API which can't set headers). |

### Audio-Only Mode

When `audio_only=1` is set, the WebSocket sends only audio frames. This is used by players that receive video via another protocol (HLS, FLV, or WebRTC) but need a separate audio stream.

**Request (audio-only):**
```bash
wscat -c "ws://localhost:9090/api/cameras/front-door/stream/ws?audio_only=1&token=$(echo -n 'username:password' | base64)"
```

### Binary Wire Format

All messages are binary WebSocket frames with the following structure:

#### Message Types

| Type | Value | Description |
|------|-------|-------------|
| `CodecInfo` | 0x01 | Video codec configuration (sent first on connect) |
| `VideoFrame` | 0x02 | Video frame data (not sent in audio-only mode) |
| `AudioFrame` | 0x03 | Audio frame data (sent when audio is configured) |
| `AudioCodecInfo` | 0x05 | Audio codec configuration (sent once when audio is available) |
| `EOS` | 0xFF | End of stream (camera went offline) |

#### CodecInfo (0x01)

Video codec configuration sent as the first message on connect (skipped in audio-only mode).

**Wire format:** `{type:1}{codec:1}{profile:1}{level:1}{sps_len:2}{sps}{pps_len:2}{pps}[vps_len:2][vps]`

- `codec` byte: 4 = H.264, 5 = H.265
- `profile` and `level`: values from the SPS
- `sps`, `pps`, `vps`: raw NAL unit data (VPS only for H.265)
- All multi-byte integers are big-endian

#### AudioCodecInfo (0x05)

Audio codec configuration sent once on connect when audio is configured.

**Wire format:** `{type:1}{audio_codec:1}{sample_rate:4_BE}{channels:1}`

- `audio_codec` byte: 0x01 = G.711 μ-law, 0x02 = G.711 A-law, 0x03 = Opus, 0x04 = AAC
- `sample_rate`: sample rate in Hz (e.g., 8000, 44100, 48000)
- `channels`: number of channels (1 = mono, 2 = stereo)

#### VideoFrame (0x02)

Video frame data with presentation timestamp and NAL unit payloads.

**Wire format:** `{type:1}{pts:8_BE}{is_keyframe:1}{nalu_count:2}{nalu1_len:4}{nalu1}...`

- `pts`: presentation timestamp in 90kHz clock (big-endian)
- `is_keyframe`: 1 = IDR frame, 0 = non-IDR
- `nalu_count`: number of NAL units in this frame (big-endian)
- Each NAL unit has a 4-byte big-endian length prefix followed by raw NAL unit data (no start codes)

#### AudioFrame (0x03)

Audio frame data with codec identifier and raw encoded audio samples.

**Wire format:** `{type:1}{pts:8_BE}{codec:1}{data_len:4_BE}{data}`

- `pts`: presentation timestamp in 90kHz clock (big-endian)
- `codec`: audio codec byte (same as in AudioCodecInfo)
- `data_len`: length of audio data in bytes (big-endian)
- `data`: raw encoded audio data (G.711 samples, Opus packets, or AAC frames)

#### EOS (0xFF)

Single byte indicating the camera went offline. Sent when the stream is unregistered or when the idle timeout expires.

**Wire format:** `{type:1}`

### Supported Audio Codecs

| Codec | Byte | Description |
|-------|------|-------------|
| G.711 μ-law | 0x01 | Telephony codec (8kHz mono, 64 kbps). Decoded via Web Audio API with G.711 μ-law lookup table. |
| G.711 A-law | 0x02 | Telephony codec (8kHz mono, 64 kbps). Decoded via Web Audio API with G.711 A-law lookup table. |
| Opus | 0x03 | Low-latency codec (8-48kHz, mono/stereo). Decoded via WebCodecs API. |
| AAC | 0x04 | High-quality codec (8-48kHz, mono/stereo). Decoded via WebCodecs API. |

### JavaScript Example

The following example connects to the audio-only WebSocket endpoint and logs incoming audio frames:

```javascript
const ws = new WebSocket('ws://localhost:9090/api/cameras/front-door/stream/ws?audio_only=1&token=' + btoa('username:password'));

ws.binaryType = 'arraybuffer';

ws.onmessage = (event) => {
  const view = new DataView(event.data);
  const msgType = view.getUint8(0);

  switch (msgType) {
    case 0x05: // AudioCodecInfo
      const audioCodec = view.getUint8(1);
      const sampleRate = view.getUint32(2, false); // big-endian
      const channels = view.getUint8(6);
      console.log(`Audio codec: ${audioCodec}, sample rate: ${sampleRate}Hz, channels: ${channels}`);
      break;

    case 0x03: // AudioFrame
      const pts = view.getBigUint64(1, false); // big-endian
      const codec = view.getUint8(9);
      const dataLen = view.getUint32(10, false); // big-endian
      const audioData = new Uint8Array(event.data, 14, dataLen);
      console.log(`Audio frame: pts=${pts}, codec=${codec}, size=${dataLen}`);
      break;

    case 0xFF: // EOS
      console.log('Stream ended');
      ws.close();
      break;
  }
};
```

## Sub-Stream Selection (quality=sub)

All four live endpoints support **on-demand sub-streams**: adding `quality=sub` requests the camera's low-resolution sub-stream (the NVR pulls it just for that request and recycles it when the last consumer leaves):

| Endpoint | Sub-stream request |
|----------|--------------------|
| WebSocket | `GET /api/cameras/{id}/stream/ws?quality=sub` |
| HTTP-FLV | `GET /api/cameras/{id}/stream.flv?quality=sub` |
| HLS | path form `GET /api/cameras/{id}/stream/sub/index.m3u8` (segments use relative addressing, no query possible) |
| WebRTC (WHEP) | `POST /api/cameras/{id}/stream/webrtc?quality=sub` |

```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/stream.flv?quality=sub" \
  -o sub.flv
```

- The `X-Stream-Quality: main|sub` response header reports what was actually served — cameras without a sub-stream, or H.265 sub-streams on H.264-only WebRTC, silently **fall back to main**
- Sub-stream availability is reported by the `sub_stream` entry of `GET /api/cameras/{id}/protocols` (`available` / `source` / `reason`)
- See the [Sub-streams](../sub-stream.md) manual page

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
      "available": true,
      "reason": ""
    },
    {
      "protocol": "flv",
      "available": true,
      "reason": ""
    },
    {
      "protocol": "hls",
      "available": true,
      "reason": ""
    },
    {
      "protocol": "ws",
      "available": true,
      "reason": ""
    }
  ],
  "encoding": "h264",
  "default": "webrtc"
}
```

## Flow Snapshot

**Endpoint:** `GET /api/streams`

Returns a real-time snapshot of every camera's video pipeline (source → StreamHub → consumers). Powers the flow-diagnosis tree in the dashboard's camera-status list. Counters are cumulative — callers diff two polls to derive fps / bitrate / per-consumer send rates (the backend hot path never computes rates).

**Request:**
```bash
curl -u username:password http://localhost:9090/api/streams
```

**Response:**
```json
{
  "streams": [
    {
      "camera_id": "front-door",
      "source": "rtsp",
      "frames_in": 10240,
      "bytes_in": 955630592,
      "last_frame_at": "2026-08-25T06:00:00Z",
      "jitter_active": false,
      "name": "Front Door",
      "status": "recording",
      "encoding": "h264",
      "width": 1920,
      "height": 1080,
      "viewers": { "ws": 1 },
      "last_frame_age_s": 0.04,
      "consumers": [
        {
          "id": "ws-front-door",
          "sends": 5120,
          "drops": 0,
          "idr_drops": 0,
          "drop_rate": 0,
          "buffer_depth": 0,
          "buffer_capacity": 150,
          "dwell_avg_ms": 0.4,
          "dwell_max_ms": 3
        }
      ]
    }
  ]
}
```

- `last_frame_age_s`: seconds since the last frame (`null` when no frame has ever arrived) — a more direct "just broke" signal than cumulative counters.
- `consumers[].id` prefixes identify the consumer type: `ws-`/`webrtc-`/`flv-`/`hls` (live viewing), `health-stats-`/`health-freeze-` (health monitoring), `keyframe-extractor-` (timelapse), `relay-*` (relays), `cascade-` (cascades).

**Per-camera endpoint:** `GET /api/cameras/{id}/flow` returns the single entry for one camera.

## Camera Recording Stats

**Endpoint:** `GET /api/cameras/{id}/stats`

Returns one camera's recording count and total on-disk size (non-archived). The "storage usage" line in the expanded flow tree uses this endpoint.

**Response:**
```json
{ "recording_count": 95, "total_size": 553675776 }
```
