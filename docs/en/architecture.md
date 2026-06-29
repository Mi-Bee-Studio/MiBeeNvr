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

The **central hub registry** (`CameraManager.hubRegistry`, `GetHub(id)` / `GetOrCreateHub(id)`) is the single source of truth: pull recorders, ingest servers, and relay targets all reference the SAME hub object for a given camera.

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

The NVR forwards a camera's live stream to remote RTMP/RTSP targets. **No FFmpeg** — uses the existing `gortsplib`/`gortmplib` client+publish APIs already in go.mod.

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
     gortmplib.Writer       gortsplib.Client
     .WriteH264(track,      .WritePacketRTP(media, pkt)
       pts, dts, au)          (rtpEnc.Encode(au))
           │                     │
           ▼                     ▼
     RTMP target            RTSP target
     (remote NVR /          (remote NVR /
      live platform)         backup)
```

### Key design points

- **Source = zero-copy**: PushTarget subscribes to the camera's existing hub. No re-pull, no decode. Same frame bus as HLS/WebRTC/recording — adding a relay target adds one goroutine + one outbound socket, ~5-10MB on RPi 3B.
- **H.264 remux only**: no transcode (no viable pure-Go H.265 encoder). RTMP targets reject H.265 sources (`errPermanent`). This is by design — transcoding remains the one FFmpeg exception.
- **Per-target independence**: each target is a separate goroutine + connection + reconnect loop (`TieredBackoffWithJitter`). Failure of one target never affects another, recording, or live.
- **Dedicated `RelayStatus`**: NOT `RecorderStatus`. "Streaming to a target" ≠ "recording to disk" — the camera health UI must not conflate them.
- **Reconcile is async**: `SetCameraTargets` runs in a goroutine (not under `cm.mu`) because it calls `GetHub` which re-locks the camera manager's mutex.
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

Audio flows through the NVR from camera capture to browser playback. The pipeline handles multiple audio formats and integrates with all streaming protocols.

### Components

| Component | Location | Role |
|-----------|----------|------|
| Audio detection | Recorder implementations | Detect audio tracks from RTSP SDP (G.711 μ-law/A-law) or Xiaomi MISS protocol (G.711/Opus) |
| Audio muxing | `internal/muxer/` | MP4 segments include audio tracks (AAC, G.711, Opus sample entries) |
| Audio merge | `internal/merge/` | Preserves audio tracks during segment merge (detects `ulaw`/`alaw`/`Opus` boxes) |
| Audio streaming | `internal/wsstream/` | WebSocket audio streaming via `?audio_only=1` endpoint |
| Audio playback | Browser | Decodes G.711 via Web Audio API with JS lookup tables |

### Data flow

```
Camera (RTSP SDP / Xiaomi MISS)
    │  Detect audio track (G.711 μ-law, A-law, Opus)
    ▼
Recorder (audio_enabled flag)
    │  Broadcast audio frames via StreamHub
    ▼
StreamHub
    │  Fan-out to all consumers (recording, live, merge)
    ▼
MP4 Muxer (recording)
    │  Write audio track (AAC/G.711/Opus sample entry)
    ▼
Segment Merge
    │  Detect audio boxes (ulaw/alaw/Opus)
    │  Preserve audio in merged MP4
    ▼
WebSocket Manager (live preview)
    │  Send AudioCodecInfo (0x05) + AudioFrame (0x03)
    ▼
Browser
    │  G.711 decoder (JS lookup tables)
    │  Web Audio API playback
```

### Frontend integration

The `CameraAudioButton.svelte` component provides audio toggle functionality embedded in:
- VideoPlayer (HLS)
- FlvPlayer (FLV)
- WebRTCPlayer

WasmPlayer (WebSocket video) has built-in audio support.

### Key design points

- **Per-camera control**: Each camera has an `audio_enabled` flag (default: false) for recording
- **Format preservation**: Merge pipeline preserves audio tracks with codec-specific sample entries (`writeMergeG711SampleEntry`, `writeMergeOpusSampleEntry`)
- **Client-side decoding**: G.711 decode happens in browser via Web Audio API, not server
- **Protocol support**: All four streaming protocols (WebSocket, FLV, HLS, WebRTC) support audio via the shared audio WebSocket endpoint
