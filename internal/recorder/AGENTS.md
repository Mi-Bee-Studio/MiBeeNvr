# MiBee NVR — Recorder Package

## OVERVIEW

Seven recorder implementations of `model.Recorder` interface. Each manages RTSP/HTTP/P2P connection, frame processing, and MP4/MJPEG segment lifecycle with auto-reconnect. Shared logic extracted to `baseRecorder` + `codecDriver` interface.

## STRUCTURE

```
base.go           # baseRecorder + codecDriver interface — shared segment lifecycle, reconnect loop, metrics, frame dispatch. H264/H265 migrated to this pattern.
h264.go          # H264Recorder — RTSP→RTP→ring buffer→MP4, SPS change detection (uses baseRecorder)
h265.go          # H265Recorder — RTSP HEVC, VPS/SPS/PPS tracking, IRAP sync (uses baseRecorder)
mjpeg.go         # MJPEGRecorder — RTSP MJPEG→JPEG frames to directory segments
http_jpeg.go     # HTTPJPEGRecorder — HTTP multipart MJPEG stream→JPEG frames, LatestFrame() cache
onvif.go         # ONVIFRecorder — delegate recorder via ONVIF GetStreamUri, MJPEG probe, port 81 fallback
timelapse.go     # TimelapseRecorder — periodic JPEG capture, configurable interval
ingest.go        # IngestRecorder — SRT/RTMP push-in: passive recorder, hub created on publisher connect
stub.go          # StubRecorder — minimal no-op for testing/placeholder
pts_check.go     # Shared PTS monotonicity check (warn only, never drop)
storage_health.go # Storage health check helper
backoff.go       # Shared exponential backoff with jitter
*_test.go        # Per-recorder tests with in-process RTSP/HTTP test servers
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add new protocol | New file, implement `model.Recorder` | Add case in `camera/manager.go:createRecorder()` |
| Fix H.264 NALU handling | `h264.go` `writeFrames()` | NAL type switch: SPS=7, PPS=8, IDR=5, non-IDR=1 |
| Fix H.265 NALU handling | `h265.go` `writeFrames()` | HEVC NAL types: VPS=32, SPS=33, PPS=34, IDR=19/20 |
| Change segment rotation | `closeCurrentSegment()` in each | Triggered by `SegmentDur` timeout or SPS/PPS change |
| Fix reconnection | `run()` in each | Exponential backoff with jitter, capped at `MaxBackoff` |
| MJPEG frame sampling | `mjpeg.go` `writeFrames()` | `SampleInterval` controls frame skip (1=every frame) |
| ONVIF stream setup | `onvif.go` | Calls ONVIF GetStreamUri, creates delegate recorder |
| HLS frame callback | `OnHLSFrame` field on H264/H265 | Non-blocking, sends to HLS manager channel |
| Fix ingest recorder | `ingest.go` | Passive recorder — NVR does NOT dial out, waits for SRT/RTMP publisher |
| Fix baseRecorder pattern | `base.go` | Shared segment lifecycle, codecDriver interface — H264/H265 use this |
| Fix audio capture | `h264.go`/`h265.go` audio RTP callbacks | G.711 via `rtplpcm.Decoder` (raw passthrough), AAC via `rtpmpeg4audio.Decoder`. Dual-write: `BroadcastAudio()` + `WriteAudioSample()` |

## CONVENTIONS

- **Shared architecture**: All recorders follow same pattern: `New*Recorder()` → `Start(ctx)` → `run()` loop → `connectAndRecord()` → `writeFrames()` goroutine
- **Ring buffer pattern** (H264/H265): RTP decode → `frameCh` channel (cap=100) → `writeFrames()` goroutine. Non-blocking send drops frames when full
- **Auto-reconnect**: `run()` wraps `connectAndRecord()` with exponential backoff + jitter. Backoff starts at `InitBackoff`, doubles + jitter, caps at `MaxBackoff`
- **Panic recovery**: `writeFrames()` and `run()` have `defer recover()` with stack logging — never crash the goroutine
- **Segment lifecycle**: `CreateSegment(temp)` → write frames → `muxer.Close()` → `CloseSegment(temp, final)` atomic rename → `DB.InsertRecording()`
- **IDR sync**: H264 waits for NAL type 5, H265 waits for NAL type 19/20 before creating new segment muxer
- **Metrics**: Optional `*metrics.Metrics` — all recorders have `incActive/decActive/recordSegmentCreated/recordBytes/recordError` helpers
- **Thread safety**: `sync.Mutex` protects `status` field. `atomic.Int64` for `dropped` frame counter
- **Audio capture**: H264/H265 recorders detect audio from RTSP SDP (PT 0=PCMU, PT 8=PCMA, PT 96+=AAC). `rtplpcm.Decoder` returns raw 8-bit G.711 bytes (NO decompression). Each audio frame is dual-written: `Hub.BroadcastAudio()` for live preview + `muxer.WriteAudioSample()` for recording. No transcoding — raw codec bytes pass through both paths identically.
- **Audio capture (H264/H265/MJPEG-over-RTSP)**: H264/H265 detect audio from RTSP SDP (PT 0=PCMU, PT 8=PCMA, PT 96+=AAC). `MJPEGRecorder` (RTSP) detects G.711 (`format.G711`) when `AudioEnabled` and writes MJPEG video + G.711 audio into **AVI** segments (`internal/avi/`), played back via the WebSocket binary protocol + `AviPlayback.svelte` (browsers can't play MJPEG-in-MP4 via `<video>`, so AVI is used instead). `HTTPJPEGRecorder` is still video-only (HTTP multipart has no audio). No transcoding — raw codec bytes pass through. `getCodecParams` (`internal/api/handlers_stream.go`) MUST have a case for every recorder type so the live recorder probe returns a non-empty codec — MJPEG/HTTPJPEG return `FormatMJPEG`/`EncJPEG`; missing a case here breaks live preview + Surveillance grid rendering for that camera type.
- **ONVIF JPEG → RTSP MJPEG routing**: An ONVIF camera whose profile reports `Encoding="JPEG"` is NOT automatically video-only. `ONVIFRecorder.detectEncoding` → `resolveJPEGEncoding` asks `GetStreamURIWithProtocol("RTSP")` and probes it; if the device serves `format.MJPEG` over RTSP (e.g. ESP32 MiBeeCam RTSP-AVI firmware), encoding resolves to `"MJPEG"` and `createDelegate` builds an `MJPEGRecorder` (AVI+audio capable), overwriting `r.rtspURL` with the rtsp:// URL. Devices without RTSP fall back to `"JPEG"` → `HTTPJPEGRecorder` (legacy video-only path). `probeRTSPEncodingFor` detects H265/H264/**MJPEG** — add any new RTSP video format here too.

## ANTI-PATTERNS

- **DO NOT** use `duration <= 0` guard — sub-millisecond durations truncate to 0 via `.Milliseconds()`, use `duration < time.Millisecond`
- **DO NOT** block on `frameCh` send — use non-blocking `select` to avoid stalling RTP reader
- **DO NOT** start segment without IDR frame — produces black/gray frames until first keyframe
- **DO NOT** forget to clean up temp files on muxer init failure — `os.Remove(tempPath)` on error path
- **DO NOT** set `SegmentDur` > 30s on RPi 3B — MP4Muxer holds all samples in RAM, 2min = 60MB+
