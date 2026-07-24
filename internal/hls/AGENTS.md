# MiBee NVR — HLS Live Streaming Package

## OVERVIEW

On-demand HLS streaming for live camera preview. Manages gohlslib muxers per camera with async write buffers, idle eviction, and sub-stream fallback.

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Start/stop HLS stream | `manager.go` `StartStream()`/`StopStream()` | Creates muxer, starts idle watchdog goroutine |
| Write frames to HLS | `WriteH264()`/`WriteH265()` | Non-blocking send to channel; drops if buffer full |
| Proxy HLS requests | `Handle()` | Forwards HTTP request to gohlslib muxer |
| Query frozen codec | `CodecFor(cameraID)` | Returns the codec baked into the muxer at stream-start. Persists across recorder reconnects (unlike the recorder's `CodecParams()`, which resets on a xiaomi P2P blip). `ok=false` if no stream entry (never started / idle-evicted). Used by `handleCameraProtocols` as a fallback when the recorder probe is empty |
| Sub-stream fallback | `StartSubStreamReader()` | Separate RTSP connection for low-bandwidth preview |
| Change stream limits | Constants at top of file | `defaultMaxStreams=4`, `defaultIdleTimeout=60s`, `writeBufSize=180` |
| Error types | `errors.go` | `ErrStreamNotFound`, `ErrMaxStreams` |

## CONVENTIONS

- **On-demand creation**: Muxer created on first frame, destroyed after idle timeout
- **Async write buffer**: 180-frame channel (`hlsFrame`) decouples recording pipeline from HLS I/O
- **Frame dropping**: Non-blocking channel send — drops frames when buffer full, logs every 100th drop
- **LRU eviction**: When max streams (4) reached, evicts oldest (by `lastUsed` timestamp)
- **Idle watchdog**: Goroutine checks `lastUsed` every 10s, evicts after 60s idle
- **Sub-stream support**: `StartSubStreamReader()` connects to camera's sub-stream URL for bandwidth savings. Falls back to main stream frames if sub-stream fails
- **H.265 support**: Uses FMP4 variant for HEVC, MPEG-TS for H.264
- **Frame rate limiting**: `maxFPS` per camera — credit-based throttle produces consistent intervals by accumulating elapsed time between frames
- **Thread safety**: `sync.RWMutex` protects `streams` map

## ANTI-PATTERNS

- **DO NOT** block on HLS frame writes — use non-blocking send to avoid stalling recording pipeline
- **DO NOT** create unlimited streams — RPi 3B memory constraint; max 4 concurrent with eviction
- **DO NOT** forget to call `StopAll()` on shutdown — leaks goroutines and temp directories
- **DO NOT** assume `Handle()` always succeeds — camera may have been evicted between stream start and HTTP request
- **DO NOT** use `enableWorker: true` in hls.js frontend — RPi browser doesn't support Web Workers well
