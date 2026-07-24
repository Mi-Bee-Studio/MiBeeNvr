# Known Issue: H.265 Multi-Camera Grid — WebCodecs Demote + HLS Oscillation

> **Status**: Identified, not yet fixed. Tracking issue for follow-up.
> **Affected**: H.265 cameras (Xiaomi CS2, ONVIF H.265) in a 4-camera grid on HTTPS.
> **Impact**: 1 of 4 cameras may demote to HLS and trigger a console-freezing state-oscillation loop.

## Problem

In a 4-camera surveillance grid where all cameras are H.265 (HTTPS, WebCodecs available):

- **3/4 cameras** stay on WebCodecs and play correctly.
- **1 camera** (typically the one with the slowest P2P connection) demotes to HLS,
  then HLS's hls.js oscillates between `loading ↔ buffering` hundreds of times per second
  (7428 statechange events in 55s observed). Each state change fires Svelte's `$effect`
  dispatcher, overloading the reactive scheduler and freezing the browser console.

## Root Cause

1. **WebCodecs demote under load**: With 4 simultaneous WS connections, the Xiaomi CS2
   camera with the slowest P2P connection has delayed frame delivery during the first
   ~20s. The wall-clock no-media guard (`NO_MEDIA_TOTAL_MS = 20000`) misjudges this as
   "no media" and demotes to HLS. Single-camera testing confirms this camera is stable
   on WebCodecs — the issue is multi-camera load causing frame-delivery timing contention.

2. **HLS H.265 state-oscillation**: After demotion to HLS, hls.js feeds H.265 to MSE,
   which reports `isTypeSupported('hvc1') = true` but the actual decode stalls at
   `readyState=1` (HAVE_METADATA). hls.js rapidly alternates `loading ↔ buffering`,
   and `VideoPlayer.$effect(dispatchStateChange)` fires on every transition →
   Svelte scheduler overload → console freeze.

## Suggested Fixes

1. **Don't demote H.265 WebCodecs to HLS**: If WebCodecs is available and the camera
   is H.265, raise the demote threshold significantly or skip HLS as a demote target
   (HLS H.265 is unreliable in most browsers anyway).

2. **Debounce VideoPlayer state dispatch**: Coalesce `loading ↔ buffering` transitions
   within a 500ms window so the reactive scheduler isn't flooded.

3. **Staggered multi-camera init**: Delay each camera's WS connect by ~500ms to avoid
   simultaneous P2P connection contention.

## What IS Fixed (in the player-orchestrator-adaptive branch)

- ✅ Backend encoding probe: Xiaomi cameras now correctly report h265 (was h264)
- ✅ Triple visibility conflict eliminated (ConnectionManager/WasmPlayer/orchestrator)
- ✅ WS zombie/handshake/wall-clock triple guard
- ✅ Frame delivery recorded even during decoder-init pause
- ✅ WebRTC WHEP timeout + connection timeout + firstFramePlayed
- ✅ Decode error threshold raised (10→50)
- ✅ Service Worker per-build cache version
- ✅ Pinned-protocol terminal-failure fallback to HLS
