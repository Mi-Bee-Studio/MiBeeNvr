# MiBee NVR — REST API Package

## OVERVIEW

Chi-based REST API. All endpoints, JSON responses, HLS proxy, ONVIF proxy, file download, snapshot caching. Dual auth: public routes → API Key (Bearer `mbv_`) → BasicAuth → setup gate.

## STRUCTURE

```
handler.go                # Handler struct, Routes(), endpoint registration
handlers_camera.go        # Camera CRUD, test-connection, merge config
handlers_recording.go     # Recording list, download, frames, batch operations
handlers_stream.go        # Multi-protocol streaming (HLS proxy, protocol registry)
handlers_system.go        # System stats, settings, config, storage info
handlers_onvif.go         # ONVIF discovery, device details, PTZ, imaging, users
handlers_xiaomi.go        # Xiaomi cloud auth, device sync
handlers_timelapse.go     # Timelapse config GET/PUT per camera
handlers_transcode.go     # Transcoding: hardware check, FFmpeg download, task management, backfill
handlers_health.go        # Camera health history
handlers_archive.go       # Camera archiving
handlers_merge.go         # Per-camera merge config
handlers_hls.go           # HLS segment proxy
handlers_flv.go           # HTTP-FLV streaming
handlers_webrtc.go        # WebRTC WHEP session management
handlers_ws.go            # WebSocket streaming
handlers_setup.go         # First-time setup
events_handler.go         # SSE event streaming endpoint
handlers_ai.go            # AI event CRUD for MiBeeVision integration (requires API Key)
ai_handler.go             # AI config + ROI zone management (browser-side ONNX zones)
handlers_mjpeg.go         # MJPEG stream URL + latest-frame cache endpoint
handlers_relay.go         # Relay platform preset endpoints
telemetry.go              # Playback telemetry logging (rate-limited)
cloud_auth.go             # Cloud auth proxy interface (Xiaomi)
xiaomi_local.go           # Local Xiaomi auth implementation
*_test.go                 # Per-handler tests

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add endpoint | `handler.go` | Add method on Handler, register in `Routes()` |
| Camera management | `handlers_camera.go` | CRUD ops, test-connection, merge config |
| Recording operations | `handlers_recording.go` | List, download, frames, batch operations |
| Streaming protocols | `handlers_stream.go` | HLS proxy, protocol registry, multi-protocol support |
| System settings | `handlers_system.go` | Stats, config, storage info |
| ONVIF integration | `handlers_onvif.go` | Discovery, PTZ, imaging, users |
| Xiaomi cameras | `handlers_xiaomi.go` | Cloud auth, device sync |
| Timelapse config | `handlers_timelapse.go` | GET/PUT per-camera timelapse settings |
| Transcoding tasks | `handlers_transcode.go` | Hardware check, FFmpeg jobs, backfill |
| Health monitoring | `handlers_health.go` | Camera health history |
| Camera archiving | `handlers_archive.go` | Archive/restore operations |
| Merge policies | `handlers_merge.go` | Per-camera segment merge config |
| HLS streaming | `handlers_hls.go` | HLS segment proxy |
| HTTP-FLV | `handlers_flv.go` | HTTP-FLV streaming endpoint |
| WebRTC | `handlers_webrtc.go` | WHEP session management |
| WebSocket | `handlers_ws.go` | WebSocket streaming endpoint |
| First-time setup | `handlers_setup.go` | Initial setup wizard |
| Event streaming | `events_handler.go` | SSE event streaming |
| Auth middleware | `Routes()` | Dual auth: public → API Key (Bearer mbv_) → BasicAuth. Use `middleware.IsAPIKeyAuthenticated(r)` to distinguish |
| File download | `serveRecording()` | Uses `http.ServeFile()` for range support |
| Snapshot cache | `handleSnapshot()` | In-memory cache per camera, TTL 5s |
| AI events (MiBeeVision) | `handlers_ai.go` | Event CRUD + recording ai_status update — API Key required |
| Relay presets | `handlers_relay.go` | Platform preset list for push-out targets |
| Test helpers | `TestHandler()` | Exported, used by integration tests |

## CONVENTIONS

- **Chi router**: `chi.NewRouter()` with middleware chain. Public routes: `/api/health`, `/api/metrics`, `/models/{filename}`, `/api/recordings/{id}/download` + `/merged`
- **Merged-file existence check**: `handleMergedRecording` (`/merged`) and `handleTimelapseDownload` `os.Stat` `rec.MergePath` before serving and return 404 if missing/empty. The frontend `RecordingDetail` treats a merged-timelapse 404/decode error as a signal to fall back to the JPEG frame viewer (`useFrameFallback`) instead of a dead-end error. `handleTimelapseFrames`/`handleTimelapseFrame` accept `timelapse` AND `mjpeg` formats (MJPEG recordings are also JPEG frame sequences).
- **JSON responses**: `writeJSON(w, status, data)` helper. Errors: `writeError(w, status, message)`
- **Camera protocol**: Frontend sends `protocol` + `encoding` separately. Backend combines to `rtsp_h264`, `rtsp_h265`, etc. in `camera/manager.go`
- **Pagination**: Recordings use `offset/limit` query params. Response includes `total` count
- **Config update**: `handleUpdateSettings()` applies config changes via `config.MergeConfig()` + atomic save
- **Snapshot cache**: `sync.RWMutex`-protected map. Cache entries have TTL (5s). Evicted on camera update
- **Test helpers**: `TestHandler()` creates Handler with temp dir + in-memory DB. `t.Helper()` enforced

## ANTI-PATTERNS

- **DO NOT** use `os.ReadFile()+w.Write()` for file downloads — no Content-Length/Accept-Ranges; use `http.ServeFile()`
- **DO NOT** use `os.O_RDONLY` in bit-flag checks — it's 0, so `flags&os.O_RDONLY != 0` is always false
- **DO NOT** forget `t.Helper()` in test helper functions — strictly enforced project-wide
