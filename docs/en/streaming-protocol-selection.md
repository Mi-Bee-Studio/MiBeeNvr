# Streaming Protocol Auto-Selection

> How the surveillance grid picks the best live-streaming protocol **per camera** — codec-aware, browser-capability-aware, with the **Player Orchestrator** doing runtime cross-protocol degrade *and* upgrade.

## Why per-camera auto-selection?

A typical NVR deployment has a **mixed fleet**: an H.264 RTSP camera, an H.265 ONVIF camera, and an ESP32 MJPEG camera. No single streaming protocol works for all three:

| Protocol | H.264 | H.265 | JPEG/MJPEG | Latency | Browser support |
|----------|-------|-------|------------|---------|-----------------|
| WebRTC (WHEP) | ✅ | ❌ (can't carry H.265) | ❌ | <500ms | Modern browsers |
| HTTP-FLV | ✅ | ❌ (mpegts.js can't decode H.265 → black screen) | ❌ | ~1s | Chrome/Edge/Firefox |
| HLS / LL-HLS | ✅ | ✅ (native fMP4) | ❌ | 3-10s | Universal |
| WebSocket (WebCodecs) | ✅ | ✅ (libde265 WASM) | ❌ | <500ms | WebCodecs + HTTPS |
| MJPEG (poll) | ❌ | ❌ | ✅ | 500ms | Universal |

Requiring the user to pick one "default protocol" in Settings that's wrong for some camera was the old model. The grid auto-selects per camera now, and **new users see no protocol picker at all** — the orchestrator picks the lowest-latency protocol each browser+codec can handle.

---

## The architecture (three layers)

### Layer 1 — Backend: per-camera protocol ranking

`GET /api/cameras/{id}/protocols` (`internal/api/handler.go:handleCameraProtocols`) does three things:

1. **Probes the real codec** — reads the *running recorder's* actual codec, not the DB-stored value. ONVIF cameras lie (e.g. advertise H.264 but stream H.265); the recorder's `detectEncoding` (RTSP DESCRIBE) is authoritative.
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

### Layer 2 — Frontend: capability probe + per-camera chain build

On mount, each route (`Surveillance.svelte`, `LiveView.svelte`):

1. **Probes device capabilities once** via `probeCaps()` (`web/src/lib/player/capabilities-cache.ts`) — `webCodecs`, `mseH265`, `wasmH265`, `webgpu`, `webgl2`, `hevcDecode`. The result is cached in `sessionStorage` for the tab session so it's never re-run in a reactive path (re-probing in a Svelte `$effect` was the root cause of the WS reconnect storm — see `wasm-player-design.md`).
2. **Fetches `/api/cameras/{id}/protocols`** in parallel for each camera (`Promise.allSettled`), non-blocking.
3. **Builds an ordered candidate chain** per camera via `buildCandidateChain(camera, resp, caps, opts)` (`web/src/lib/stream-selection.ts`). This returns **every** playable mode for that camera's codec + browser caps, latency-optimal first:
   ```
   PREFERENCE_ORDER = [wasm, webrtc, flv, hls, mjpeg]
   wasm   ← frontend-only, gated on WebCodecs (any codec) or libde265 WASM (H.265 on HTTP)
   webrtc ← backend Available + H.264 only (H.265 excluded by codec gate)
   flv    ← backend Available + (H.264, or H.265 only when MSE H.265 present)
   hls    ← backend Available (universal fallback)
   mjpeg  ← JPEG/MJPEG cameras only (single-element chain)
   ```
   The chain head is what the grid renders initially; subsequent entries are degrade targets. A user override **pins** the chain to a single element (see "User manual override" below).

### Layer 3 — Player Orchestrator: adaptive degrade + upgrade (runtime)

The **Player Orchestrator** (`web/src/lib/player/orchestrator.svelte.ts`) owns the per-camera state machine. It is created once per route, provided to `CameraPlayer` via Svelte context, and holds:

- the candidate chain + the active index (reactive `$state`),
- the latest health reported by the active player (reactive `$state`),
- the reconnect coordinator (thundering-herd control, see below).

Each player reports a normalized **`HealthState`** (`web/src/lib/player/health.ts`) — `ok | degraded | failed` — via the DOM `statechange` event that every player already bubbles. `CameraPlayer` bridges that to `orchestrator.reportHealth()`. The orchestrator then decides:

| Health | Action |
|--------|--------|
| `failed` | **Demote immediately** to the next chain entry (e.g. webrtc → flv). Toast once. |
| `degraded` for > 8s | **Demote** (debounced — a brief rebuffer must NOT trigger a switch). |
| `ok` for 30s + trigger | **Attempt upgrade** to a lower-latency entry. Triggers: tab becomes visible, or explicit "retry HD" button. |
| upgrade fails to reach `ok` within 5s | **Revert** + cool that entry 60s (anti-flap). |

**Anti-flap guarantees:**
- Upgrades only fire on an explicit trigger (never on a timer/poll), so a flaky network can't thrash between two protocols.
- A reverted upgrade cools the offending entry for 60s.
- A user-pinned override disables auto-degrade/upgrade entirely.

Example cascade: WebRTC WHEP fails → auto-switch to FLV (toast) → FLV fails → switch to HLS → HLS fails → only then drop to snapshot. Later, if WebRTC recovers and the tab is toggled, the orchestrator promotes back to WebRTC (with the 5s probe guard).

**Visibility:** the orchestrator owns tab visibility (`setTabVisible`). On hide it pauses every player (releases WS / `RTCPeerConnection`); on show it resumes and gives stable cameras a chance to reclaim low-latency. This replaced the per-player visibility `$effect` that caused the WS storm.

---

## User manual override ("Auto" vs pinned)

The **ProtocolSwitcher** (LiveView + Surveillance) defaults to **"Auto (recommended)"** — the orchestrator drives selection. Selecting a specific protocol **pins** the chain to that single mode via `setCameraProtocolOverride()` (`web/src/lib/preferences.ts`, stored in `localStorage` under `mibee_nvr_prefs_proto_<cameraId>`), and the orchestrator treats it as a single-element pinned chain: **no auto-degrade, no auto-upgrade** — the user's explicit choice is respected. Picking "Auto" again clears the override.

The override is validated at chain-build time: if the camera's codec changes (e.g. H.264 → H.265) and the pinned protocol can't serve it, the override is ignored and auto-selection takes over.

---

## The `default_protocol` setting

`streaming.default_protocol` is a **backend-only field** kept for backward compatibility. The frontend **no longer exposes a UI for it** (the "Fallback Protocol" selector was removed — the orchestrator auto-selects). When `/protocols` is unreachable, `buildCandidateChain` falls back to the universal HLS candidate on its own, so the setting is effectively unused on the frontend. It remains in the config/API for older deployments.

---

## Key files

| File | Role |
|------|------|
| `internal/api/handler.go` `handleCameraProtocols` | Backend: probes codec, checks handlers, computes default |
| `internal/api/handlers_stream.go` `getCodecParams` + `CanHandle` | Per-handler codec gating |
| `web/src/lib/player/orchestrator.svelte.ts` `createPlayerOrchestrator` | **Runtime state machine**: candidate chain, degrade/upgrade, visibility, owns reconnect coordinator |
| `web/src/lib/player/health.ts` | Normalized `HealthState` + `healthFromStreamState` / `healthFromConnectionState` mappers |
| `web/src/lib/player/capabilities-cache.ts` `probeCaps` / `getCaps` | One-shot device-capability probe, cached in sessionStorage |
| `web/src/lib/stream-selection.ts` `buildCandidateChain` / `pickCameraMode` | Pure chain-build + single-mode decision (unit-tested). `pickCameraMode` is a thin wrapper over `buildCandidateChain[0]`, kept for legacy callers. |
| `web/src/components/CameraPlayer.svelte` | Single dispatcher: reads `orchestrator.activeMode(id)`, renders the matching player, bridges health via DOM `statechange` |
| `web/src/components/ProtocolSwitcher.svelte` | Per-camera "Auto" + manual pin (the only override writer) |
| `web/src/lib/preferences.ts` `setCameraProtocolOverride` / `clearCameraProtocolOverride` | localStorage per-camera override storage |
| `web/src/lib/webcodecs-player/capabilities.ts` `detectMSEH265` / `detectWebCodecs` / `getPlaybackTier` | Low-level browser capability probes (consumed by capabilities-cache) |

> See also: `wasm-player-design.md` for the WebCodecs decode pipeline + the WS reconnect-storm fix details.
