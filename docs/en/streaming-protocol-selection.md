# Streaming Protocol Auto-Selection

> How the surveillance grid picks the best live-streaming protocol **per camera** — codec-aware, browser-capability-aware, with runtime cross-protocol fallback.

## Why per-camera auto-selection?

A typical NVR deployment has a **mixed fleet**: an H.264 RTSP camera, an H.265 ONVIF camera, and an ESP32 MJPEG camera. No single streaming protocol works for all three:

| Protocol | H.264 | H.265 | JPEG/MJPEG | Latency | Browser support |
|----------|-------|-------|------------|---------|-----------------|
| WebRTC (WHEP) | ✅ | ❌ (can't carry H.265) | ❌ | <500ms | Modern browsers |
| HTTP-FLV | ✅ | ❌ (mpegts.js can't decode H.265 → black screen) | ❌ | ~1s | Chrome/Edge/Firefox |
| HLS / LL-HLS | ✅ | ✅ (native fMP4) | ❌ | 3-10s | Universal |
| WebSocket (WebCodecs) | ✅ | ✅ (libde265 WASM) | ❌ | <500ms | WebCodecs + HTTPS |
| MJPEG (poll) | ❌ | ❌ | ✅ | 500ms | Universal |

Requiring the user to pick one "default protocol" in Settings that's wrong for some camera was the old model. The grid now auto-selects per camera.

---

## The four layers

### Layer 1 — Backend: per-camera protocol ranking

`GET /api/cameras/{id}/protocols` (`internal/api/handler.go:handleCameraProtocols`) does three things:

1. **Probes the real codec** — resolved in this priority order (the DB-stored value can be stale/wrong, e.g. a xiaomi camera stored as h264 that actually streams h265):
   - **① Running recorder's probed value** (authoritative) — from real video packets via `getCodecParams(rec)` (RTSP DESCRIBE for ONVIF; first-packet codec ID for xiaomi). But returns empty before the first packet arrives, and during a xiaomi P2P reconnect (its codec field resets).
   - **② HLS muxer frozen value fallback** (`hls.Manager.CodecFor(id)`) — the HLS muxer bakes the codec into the track at stream-start and **keeps it across recorder reconnects**. So when the recorder probe is empty, this covers the reconnect window and prevents the frontend from flickering back to the stale DB value and misrouting to a black screen. Available only while an HLS stream entry exists for the camera (not after it was never started / idle-evicted).
   - **③ DB-stored value** — last resort.
   - ONVIF cameras lie (e.g. advertise H.264 but stream H.265); the recorder probe is authoritative.
2. **Checks stream handler capabilities** — asks each registered handler `CanHandle(codec)`:
   - WebRTC: H.264 only
   - FLV: H.264 only (mpegts.js can't decode H.265 in the browser)
   - HLS / LL-HLS: H.264 + H.265
   - WebSocket (wasm): H.264 + H.265 (needs WebCodecs)
   - MJPEG: JPEG/MJPEG only
3. **Computes a default** — prefers the user-configured `streaming.default_protocol` if available for this codec; otherwise walks `webrtc → flv → ll-hls → hls → mjpeg` and picks the first available.

**Response:**
```json
{
  "protocols": [
    {"Protocol": "webrtc", "Available": true,  "Reason": ""},
    {"Protocol": "flv",    "Available": true,  "Reason": ""},
    {"Protocol": "hls",    "Available": true,  "Reason": ""},
    {"Protocol": "wasm",   "Available": true,  "Reason": ""},
    {"Protocol": "webrtc", "Available": false, "Reason": "WebRTC does not support H.265"}
  ],
  "encoding": "h265",
  "default": "hls"
}
```

### Layer 2 — Frontend: parallel fetch + cache

On mount, `Surveillance.svelte` fetches `/api/cameras/{id}/protocols` for each selected camera **in parallel** (`Promise.allSettled`), caching results in a `Map<cameraId, ProtocolsResponse>`. This is **non-blocking**: cameras render immediately with the legacy default and re-resolve once responses arrive. Failures store `null` (fall back to legacy default, never block the grid).

Browser capabilities are detected once:
- `probeMSEH265()` / `detectMSEH265()` — can MediaSource decode H.265? **Note**: `MediaSource.isTypeSupported('hvc1')` is a **false positive** on Chromium/Edge (the OS HEVC decoder is registered for native `<video>` playback, so MSE accepts hvc1 bytes but never buffers them → black screen). So we use the authoritative probe `probeMSEH265()`: create a throwaway hvc1 SourceBuffer, append a real HEVC init segment, check `video.buffered`. Result cached in sessionStorage. Until the probe resolves, `detectMSEH265()` conservatively returns false. (false on Linux desktop, Windows without HEVC extensions)
- `detectWebCodecs()` — is `VideoDecoder` available? (requires HTTPS or localhost)
- `detectWasmH265()` — is libde265 WASM available? (the only reliable H.265 decode path on plain HTTP, rendered via Canvas2D)

### Layer 3 — `getCameraMode(camera)` decision cascade

Each grid cell calls `getCameraMode(camera)` on every render. Priority from high to low:

```
① runtimeFallback[camera.id]          ← runtime demotion from a failed player (Layer 4)
② pickCameraMode(camera, resp, caps, opts):
   a. JPEG/MJPEG encoding → 'mjpeg'   ← early short-circuit (ONVIF JPEG delegates report
                                          protocol=onvif but stream JPEG; HLS would black-screen)
   b. Protocol not HLS-capable (rtmp/srt ingest) → 'snapshot' or 'unsupported'
   c. User override (localStorage per-camera) → use it IF still usable for this codec
   d. Backend default → refine by browser capability:
        webrtc + H.265           → 'hls'  (WebRTC can't carry H.265)
        flv + H.265 + no MSE     → 'hls'  (FLV renders black without H.265 MSE)
        wasm + no WebCodecs      → 'hls'  (WASM player needs WebCodecs)
        ll-hls / hls             → 'hls'  (hls.js handles low-latency)
   e. Backend response null (unreachable) → legacy global default_protocol (last resort)
```

**Key point**: it's NOT one global protocol for all cameras. A mixed fleet gets different protocols per cell automatically.

### Layer 4 — Runtime cross-protocol fallback

When a player exhausts its reconnect attempts (previously: drop to static snapshot), it now calls `onProtocolFailed` first:

```
handleProtocolFailed(cameraId, currentMode):
  Build fallback chain from backend response: [webrtc, flv, hls, mjpeg] filtered to Available
  Find currentMode's position in the chain
  If a next protocol exists:
    Set runtimeFallback[cameraId] = next
    Remount the cell with the new player
    Toast: "Switched playback to FLV for better stability"
    Return true (player: don't snapshot yet)
  Else (chain exhausted):
    Return false (player: fall back to snapshot)
```

Example cascade: WebRTC WHEP fails → auto-switch to FLV → FLV fails → switch to HLS → HLS fails → only then drop to snapshot.

`runtimeFallback` is cleared when the grid selection changes (camera starts fresh from auto-selection).

---

## User manual override

A user can pin a specific protocol to one camera via the **ProtocolSwitcher** on the LiveView page (`#/live/{id}`). This is stored in `localStorage` (`mibee_nvr_prefs_proto_<cameraId>`) and read by the grid's `getCameraMode` (Layer 3c).

**Important**: only an explicit *manual* selection writes the override. Runtime auto-fallbacks (Layer 4) do NOT persist — otherwise a transient failure would permanently pin a worse protocol. The override is also validated: if the camera's codec changes (e.g. H.264 → H.265) and the pinned protocol can't serve it, the override is ignored and auto-selection takes over.

---

## The `default_protocol` setting

`streaming.default_protocol` in the config is now a **fallback only** — used when the per-camera `/protocols` endpoint can't be reached (camera still connecting, endpoint down). It's no longer the primary protocol choice. The Settings UI labels it "Fallback streaming protocol" to reflect this.

---

## Key files

| File | Role |
|------|------|
| `internal/api/handler.go` `handleCameraProtocols` | Backend: probes codec (recorder → HLS muxer fallback → DB), checks handlers, computes default |
| `internal/api/handlers_stream.go` `getCodecParams` + `CanHandle` | Per-handler codec gating; `getCodecParams` reads the recorder's real codec via `model.HLSProvider.CodecParams()` |
| `internal/hls/manager.go` `CodecFor` | Query the HLS muxer's frozen codec (the reconnect-safe fallback source) |
| `web/src/lib/stream-selection.ts` `pickCameraMode` / `fallbackChain` / `nextAfter` | Pure decision logic (unit-tested) |
| `web/src/routes/Surveillance.svelte` `getCameraMode` / `handleProtocolFailed` | Grid integration: fetch, cache, resolve, demote |
| `web/src/components/ProtocolSwitcher.svelte` | LiveView per-camera override (the only override writer) |
| `web/src/lib/preferences.ts` `getCameraProtocolOverride` | localStorage per-camera override storage |
| `web/src/lib/webcodecs-player/capabilities.ts` `detectMSEH265` / `detectWebCodecs` | Browser capability detection |
