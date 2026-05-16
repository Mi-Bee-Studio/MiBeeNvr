# MiBee NVR — REST API Package

## OVERVIEW

Chi-based REST API. All endpoints, JSON responses, HLS proxy, ONVIF proxy, file download, snapshot caching. Test handlers exported for integration tests.

## STRUCTURE

```
handler.go           # Handler struct, Routes(), all endpoint methods, test factories
handler_test.go      # Unit tests for recordings, cameras, stats, settings
onvif_test.go        # ONVIF discovery and camera tests
onvif_camera_test.go # ONVIF-specific camera management tests
ptz_test.go          # PTZ control tests
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add endpoint | `handler.go` | Add method on Handler, register in `Routes()` |
| Change auth | `Routes()` | `authMW` wraps authenticated routes |
| File download | `serveRecording()` | Uses `http.ServeFile()` for Content-Length + range support |
| Snapshot caching | `handleSnapshot()` | In-memory cache per camera, TTL-based invalidation |
| HLS proxy | `handleHLSStream()` | Forwards to HLS Manager, handles stream-not-found |
| ONVIF proxy | `handleONVIF*()` | Discovery + device detail + PTZ |
| Health check | `handleHealth()` | DB + storage disk space checks |
| System stats | `handleSystemStats()` | CPU/Memory/Network from `/proc` |
| Test factories | `TestHandler()` / `TestHandlerWithAuth()` | Exported, used by integration tests too |

## CONVENTIONS

- **Chi router**: `chi.NewRouter()` with middleware chain. Public routes: `/api/health`, `/api/metrics`
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
