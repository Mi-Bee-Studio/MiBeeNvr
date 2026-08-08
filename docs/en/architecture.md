# Architecture: Push-In Ingest & Push-Out Relay

> How MiBee NVR connects cameras across networks — natively in Go, without FFmpeg.

This document covers two streaming subsystems added in v0.8.0 that let the NVR operate across network boundaries:

1. **Push-In (Ingest)** — a remote publisher pushes a stream INTO the NVR (SRT/RTMP). The NVR records and serves it live like any camera.
2. **Push-Out (Relay)** — the NVR forwards a camera's live stream OUT to remote destinations (RTMP/RTSP). Pure Go, no external processes.

Both reuse the existing `StreamHub` frame bus so they slot into the recording + live (HLS/WebRTC/FLV/WS) pipeline without changes to consumers.

---

## 1. Shared Foundation: the StreamHub

Every camera owns a `*model.StreamHub` — a frame fan-out bus. Producers call `hub.Broadcast(pts, au, isIDR)`; consumers call `hub.Subscribe(id, callback)`. The hub runs each consumer's callback in a dedicated goroutine (non-blocking; drops on full buffer).

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

The **central hub registry** (the `hubs` map inside `CameraManager`'s copy-on-write snapshot, exposed via the lock-free `GetHub(id)` / `GetOrCreateHub(id)`) is the single source of truth: pull recorders, ingest servers, and relay targets all reference the SAME hub object for a given camera.

---

## 2. Push-In (Ingest) — `internal/recorder/ingest.go`

A remote publisher (ffmpeg, OBS, a phone, another NVR) pushes a stream to the NVR's SRT listener or RTMP server. The stream becomes a full camera.

### Components

| Component | File | Role |
|-----------|------|------|
| SRT Listener | `internal/srt/listener.go` | Accepts SRT pushes; maps `streamid` → camera ID |
| RTMP Server | `internal/rtmp/server.go` | Accepts RTMP pushes; maps stream key → camera ID |
| IngestRecorder | `internal/recorder/ingest.go` | The recorder for push cameras: records rolling MP4 + feeds the hub |

### Data flow (RTMP example)

```
Publisher ──RTMP──▶ RTMP Server (handlePublisher)
                       │  OnDataH264(au, pts)
                       ├─▶ NALUProvider ──▶ IngestRecorder.WriteNALU(au, pts, isIDR)
                       │                       ├─▶ hub.Broadcast (live consumers)
                       │                       ├─▶ SPS/PPS capture + IDR-gated MP4 segment
                       │                       └─▶ SegmentStore → RecordingDB → SegmentCompleted event
                       └─▶ hub.Broadcast (legacy path)
```

### Key design points

- **IngestRecorder implements `model.Recorder`** (Start/Stop/Status) + **`HLSProvider`** (CodecParams) so it slots into the existing CameraManager and HLS/FLV/WS handlers with zero changes.
- **Lifecycle**: `Idle (awaiting publisher)` → `Recording (publisher connected)` → `Idle (publisher disconnected)`. Modeled as `StatusReconnecting` for Idle to avoid a new status constant.
- **SPS/PPS injection**: RTMP carries SPS/PPS out-of-band (AVCDecoderConfigurationRecord). The server feeds them to IngestRecorder before VCL frames, and IngestRecorder prepends them to IDR broadcasts so downstream muxers (gohlslib DTS extractor) see them in-band — matching the RTSP path.
- **Segment rolling**: mirrors H264Recorder — SPS change detection, IDR-gated segment start, duration-based rollover, atomic temp→rename.

### Push-in save policy

Per-camera `push_retention_days`: `nil` = follow global, `0` = live-only (no recording), `N` = keep N days.

---

## 3. Push-Out (Relay) — `internal/relay/`

The NVR forwards a camera's live stream to remote RTMP/RTSP targets. RTSP targets use `gortsplib.Client` (pure Go, zero-copy remux); RTMP targets use a **custom handshake + publish layer** (`rtmp_client.go`) that solves the six FMS-compat root causes (HMAC digest, chunk size, Type 0 headers, full `onMetaData`, big-endian streamID) strict receivers (Douyu/Huya/Bilibili) require — no FFmpeg needed for remux. H.265 sources transcode to H.264 via `livetranscode` (FFmpeg subprocess) when `TranscodePolicy` ≠ `off`.

### Components

| Component | File | Role |
|-----------|------|------|
| PushTarget | `internal/relay/engine.go` | One per destination; subscribes to camera hub, writes frames to target, reconnect loop |
| Manager | `internal/relay/manager.go` | Owns all targets; config-diff reconcile, status aggregation, lifecycle |
| Status | `internal/relay/status.go` | `RelayStatus` (distinct from `RecorderStatus`) + `TargetStatus` (JSON) |

### Data flow

```
Camera StreamHub ──▶ Subscribe("relay-rtmp-<id>", cb)
                      │  cb(pts, au)
                      ▼
                   PushTarget.connectRTMP/connectRTSP
                      │
           ┌──────────┴──────────┐
           ▼                     ▼
     rtmpPublishConn       gortsplib.Client
     (custom handshake,    .WritePacketRTP(media, pkt)
      Type 0 publish)        (rtpEnc.Encode(au))
           │                     │
           ▼                     ▼
     RTMP target            RTSP target
     (remote NVR /          (remote NVR /
      live platform)         backup)
```

### Key design points

- **Source = zero-copy**: PushTarget subscribes to the camera's existing hub. No re-pull, no decode. Same frame bus as HLS/WebRTC/recording — adding a relay target adds one goroutine + one outbound socket, ~5-10MB on RPi 3B.
- **H.264 remux or H.265 transcode**: H.264 sources remux zero-copy. H.265 sources live-transcode to H.264 via `livetranscode.LiveTranscoder` (FFmpeg subprocess) when `TranscodePolicy` ≠ `off`; if `off`, H.265 is rejected with `errPermanent`. Thermal monitoring protects ARM SBCs. See [Relay Guide](relay-guide.md#h265-transcoding).
- **Per-target independence**: each target is a separate goroutine + connection + reconnect loop (`TieredBackoffWithJitter`). Failure of one target never affects another, recording, or live.
- **Dedicated `RelayStatus`**: NOT `RecorderStatus`. "Streaming to a target" ≠ "recording to disk" — the camera health UI must not conflate them.
- **Reconcile is async**: `SetCameraTargets` runs in a goroutine so it doesn't block the AddCamera/UpdateCamera API response on relay-engine teardown. (GetHub is a lock-free snapshot read, so there's no lock-reentrancy concern.)
- **SPS/PPS from source**: `camMgr.GetSPS(cameraID)` returns the source's SPS/PPS for target track initialization.

### Why not FFmpeg?

| | FFmpeg | Native Go Relay |
|---|---|---|
| Binary | External process (~50MB) | Embedded, single static binary |
| RPi 3B RAM | ~30-50MB per process | ~5-10MB (one goroutine + socket) |
| Cross-compile | Separate ARM ffmpeg build | `CGO_ENABLED=0`, no change |
| Reliability | Crashes need restart scripts | NVR reconnect/backoff built-in |
| Transcode | ✅ (H.265→H.264) | ❌ (remux only) |

The relay covers the common case (H.264 cameras across networks) at 10× lower cost. Transcoding stays behind its FFmpeg feature flag for the rare H.265→RTMP case.

---

## 4. Configuration

### Push-in (camera protocol `srt` or `rtmp`)

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

### Push-out (any camera)

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

### Enabling the ingest servers

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

- `POST /api/cameras` / `PUT /api/cameras/{id}` — accept `push_targets[]` and `push_retention_days`.
- `GET /api/cameras/{id}/push-status` — returns per-target live status:
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

## 6. Network Topology Examples

```
A) Push-in: remote camera → NVR
   [Remote Cam/ffmpeg] ──push RTMP/SRT──▶ [NVR (public IP / port-forwarded)]

B) Push-out: NVR → remote destination
   [NVR + camera] ──relay RTMP/RTSP──▶ [Remote NVR ingest / live platform]

C) Chained (NVR-to-NVR, the cross-network scenario):
   [Camera] ──RTSP──▶ [NVR-A] ──push-out relay──▶ [NVR-B (push-in ingest)] ──▶ records + live
```

---

## 7. Audio Pipeline

### 7.1 Overview

Audio flows from camera capture through dual paths: recording (MP4 segments stored on disk) and live preview (real-time WebSocket streaming to the browser). The pipeline supports multiple audio codecs: G.711 μ-law and A-law (8-bit logarithmic PCM), AAC (MPEG-4 Audio), and Opus. Raw encoded bytes from the camera are written simultaneously to both paths without transcoding, preserving audio quality while minimizing server CPU load.

### 7.2 Codec Detection

Each camera type detects audio tracks differently during stream initialization:


| Camera Type | Detection Method | Codec Payload Mapping | Decoder Backend |
|-------------|-----------------|----------------------|-----------------|
| RTSP cameras | gortsplib parses SDP (Session Description Protocol) | PayloadType 0 = PCMU (μ-law), PT 8 = PCMA (A-law), PT 96+ = AAC (MPEG-4 Audio) | `rtplpcm.Decoder` for G.711 (returns raw 8-bit bytes, NO decompression), `rtpmpeg4audio.Decoder` for AAC |
| Xiaomi cameras | MISS protocol codec IDs | 1026 = PCMU, 1027 = PCMA, 1032 = Opus | All PCMA/PCMU map to `model.AudioG711`; Opus passes raw bytes |

The detection phase sets three critical fields on the recorder:
- `g711MULaw` flag (true for μ-law, false for A-law)
- `g711SampleRate` (typically 8000 Hz for G.711)
- `audioMuxerConfig` bytes (codec-specific sample entry data for MP4)

### 7.3 Dual-Path Architecture

Raw encoded bytes from the camera RTP packets are written to TWO consumers simultaneously via the StreamHub frame bus:

1. **Recording path**: `muxer.WriteAudioSample(pts, codec, bytes)` → raw bytes stored in MP4 segment. MP4 box types vary by codec: `ulaw`/`alaw` (G.711, 8-bit sample size), `mp4a`+`esds` (AAC with AudioSpecificConfig), `Opus`+`dOps` (Opus with OpusHead, 48kHz timescale). NO transcoding occurs — raw codec data passes through unchanged.

2. **Live preview path**: `hub.BroadcastAudio(pts, codec, sampleRate, bytes)` → wsstream → WebSocket binary frames → frontend JS decode.

The critical insight: the SAME raw bytes go to both paths. The difference in audio quality between recording playback and live preview comes from the DECODER, not the data. Recording playback uses the browser's native MP4 audio decoder (highly optimized native code), while live preview uses JavaScript lookup tables + Web Audio API (requires correct tables and sample rate matching).

### 7.4 Recording Playback (Clean Audio)

When playing back a recording, the browser's native `<video>`/`<audio>` element or HLS player decodes the MP4 audio track. For G.711, the browser reads the `ulaw` (μ-law) or `alaw` (A-law) box descriptor and uses its built-in G.711 decoder (typically hardware-accelerated or highly optimized native code). This is a well-tested code path in Chrome/Firefox/Safari that produces clean audio without artifacts. The same applies to AAC (native decoder) and Opus (native decoder in modern browsers).

### 7.5 Live Preview (WebSocket Audio)

Live preview audio uses a custom binary WebSocket protocol defined in `internal/wsstream/`. The wire format consists of two frame types:

1. **AudioCodecInfo** (type 0x05, sent once when audio stream starts):
```
{type:1}{codec:1}{sample_rate:4_BE}{channels:1}
```
- Total: 7 bytes
- `type`: 0x05 (codec info)
- `codec`: 0x01 = μ-law, 0x02 = A-law, 0x03 = Opus, 0x04 = AAC
- `sample_rate`: 32-bit big-endian integer (e.g., 8000 for G.711, 48000 for Opus)
- `channels`: 1 = mono, 2 = stereo

2. **AudioFrame** (type 0x03, per RTP packet):
```
{type:1}{pts:8_BE}{codec:1}{data_len:4_BE}{data}
```
- Total: 14 + data_len bytes
- `type`: 0x03 (audio frame)
- `pts`: 64-bit big-endian presentation timestamp (microseconds since epoch)
- `codec`: same codec byte as AudioCodecInfo
- `data_len`: 32-bit big-endian length of audio payload
- `data`: raw codec bytes (no transformation from StreamHub — identical to recording path)

The WebSocket endpoint is `GET /api/cameras/{id}/stream/ws?audio_only=1`. The `audio_only=1` flag tells the server to skip video frames entirely, reducing bandwidth for audio-only monitoring scenarios.

### 7.6 Frontend G.711 Decode

The frontend uses standard ITU-T G.711 256-entry lookup tables to convert 8-bit compressed samples to 16-bit linear PCM:

- **μ-law decoder** (`decodeMuLaw` in `web/src/lib/g711-decoder.ts`): Table indexed directly by raw byte. The bitwise NOT (bit-flip) required by ITU-T G.711 specification is already baked into the table values. Max output: ±32124 (full 16-bit range).

- **A-law decoder** (`decodeALaw`): Table indexed directly by raw byte. The XOR 0x55 required by ITU-T G.711 specification is already baked into the table values. Max output: ±32256.

- **PCM to Float32 conversion**: After decoding, PCM samples are normalized to Float32 range [-1.0, +1.0] via `pcm[i] / 32768` for Web Audio API consumption.

**CRITICAL**: The lookup tables are sourced from well-tested reference implementations (NAudio, Wireshark, janus-gateway). The bit-flip transformation (NOT for μ-law, XOR 0x55 for A-law) MUST NOT be applied by the caller — it is already incorporated into the table values. Direct indexing by raw byte is correct.

Key files: `web/src/lib/g711-decoder.ts` (tables + decode functions), `web/src/lib/audio-player.ts` (Web Audio API playback).

### 7.7 Web Audio API Playback

Live preview audio playback uses the Web Audio API for gapless, low-latency audio:

- **AudioContext created at stream sample rate**: `new AudioContext({ sampleRate: 8000 })` for G.711, NOT the browser default (48000 Hz). This prevents per-buffer resampling artifacts at buffer boundaries caused by mismatched sample rates.

- **Buffer creation**: Each audio frame creates an `AudioBuffer` at the stream sample rate (8000 Hz), filled with decoded Float32 samples from the G.711 lookup tables. Buffer duration = `frameCount / sampleRate` (typically 20ms for 160-byte G.711 frames at 8000 Hz).

- **Gapless scheduling**: `AudioBufferSourceNode` instances are chained via `_nextTime` tracking. Each buffer is scheduled to start at `_nextTime`, then `_nextTime += buffer.duration`. If scheduling drifts >1 second ahead of current time (indicating accumulated timing errors or client buffering), `_nextTime` resets to `ctx.currentTime + 0.1` to prevent memory buildup and audible gaps.

- **Autoplay policy**: AudioContext creation requires user gesture (browser autoplay policy). `CameraAudioButton.svelte` handles this by creating the AudioContext on the first click.

- **Codec support**: All three codecs (G.711, AAC, Opus) are now decoded for live preview (#131). G.711 via ITU-T lookup tables; AAC via WebCodecs `AudioDecoder` (HTTPS/localhost) or FAAD2 WASM (plain HTTP); Opus via WebCodecs `OpusDecoder` — see `web/src/lib/audio-player.ts` + `web/src/lib/decoders/`. (Previously only G.711 was decoded; AAC/Opus live preview was added in #131.)

### 7.8 Components


| Component | Location | Role |
|-----------|----------|------|
| Audio detection | Recorder implementations (`internal/recorder/`) | Detect audio tracks from RTSP SDP (G.711 μ-law/A-law/AAC) or Xiaomi MISS protocol (G.711/Opus) |
| Audio muxing | `internal/muxer/` | MP4 segments include audio tracks with codec-specific sample entries (`ulaw`/`alaw`/`mp4a`/`Opus` boxes) |
| Audio merge | `internal/merge/` | Preserves audio tracks during segment merge, detects and copies `ulaw`/`alaw`/`Opus` boxes |
| Audio streaming | `internal/wsstream/` | WebSocket audio streaming via `?audio_only=1` endpoint, sends AudioCodecInfo (0x05) + AudioFrame (0x03) binary frames |
| Audio playback | Browser | Decodes G.711 via standard ITU-T 256-entry lookup tables + Web Audio API at native sample rate (8kHz) |

### 7.9 Data Flow

```
Camera (RTSP SDP / Xiaomi MISS)
    │  Detect audio track (G.711 μ-law, A-law, AAC, Opus)
    │  Set g711MULaw flag, g711SampleRate, audioMuxerConfig
    ▼
Recorder (audio_enabled flag)
    │  BroadcastAudio(pts, codec, sampleRate, raw_bytes) via StreamHub
    ▼
StreamHub
    │  Fan-out to all consumers (recording muxer, live wsstream, merge)
    ▼
┌─────────────────┴─────────────────┐
│                                     │
▼                                     ▼
MP4 Muxer (recording)          WebSocket Manager (live preview)
│  WriteAudioSample()                 │  Send AudioCodecInfo (0x05)
│  Raw bytes → MP4 track             │  Send AudioFrame (0x03)
│  ulaw/alaw/mp4a/Opus box            │  Binary WebSocket frames
│                                     │
▼                                     ▼
Segment Merge                    Browser
│  Detect audio boxes                   │  Parse AudioCodecInfo (0x05)
│  Preserve audio in merged MP4        │  Parse AudioFrame (0x03)
│                                     │  G.711 decoder (ITU-T standard 256-entry lookup tables)
▼                                     │  AudioContext at stream sample rate (8kHz)
Recording Playback (HLS/MP4)           │  Web Audio API gapless playback
│  Browser native MP4 audio decoder    │
│  Clean, well-tested audio            │
```

### 7.10 Key Design Points

- **Standard ITU-T tables**: G.711 lookup tables must use standard ITU-T G.711 values sourced from reference implementations (NAudio, Wireshark, janus-gateway). The bit-flip transformation (NOT for μ-law, XOR 0x55 for A-law) is baked into the table values — callers index by raw byte directly without applying transformations.

- **AudioContext sample rate matching**: AudioContext must be created at the stream's native sample rate (8000 Hz for G.711). Using the browser default (48000 Hz) causes per-buffer resampling artifacts at buffer boundaries, resulting in clicks or pops.

- **No server-side decode**: All G.711 decoding happens in the browser via JavaScript lookup tables. The backend passes raw codec bytes through without transformation or decompression, minimizing server CPU load and simplifying the pipeline.

- **Recording vs live difference**: Recording playback uses the browser's native MP4 audio decoder (well-tested, clean, hardware-accelerated in some browsers). Live preview uses JS lookup tables + Web Audio API (requires correct tables, sample rate matching, and gapless scheduling). The audio quality difference is decoder implementation, not data.

- **AAC/Opus live preview decode (supported as of #131)**: All three codecs are now decoded for live preview. AAC via WebCodecs `AudioDecoder` (HTTPS/localhost) or FAAD2 WASM (~200KB, plain HTTP); Opus via WebCodecs `OpusDecoder`; G.711 via lookup tables. See `web/src/lib/decoders/`. (Previously only G.711 was decoded live; AAC/Opus were recordings-only.)

- **Dual-path preservation**: The same raw codec bytes flow to both recording and live paths. Any change to the encoding side (e.g., sample rate, codec selection) affects both paths equally. The decoder is the only variable between recording and live quality.

