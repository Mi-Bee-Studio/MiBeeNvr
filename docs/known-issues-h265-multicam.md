# Known Issue: H.265 Multi-Camera Grid — WebCodecs Demote + HLS Oscillation

> **Status**: ✅ **Fixed** (issues #107, #108). This document is retained as a
> postmortem / regression reference.
> **Affected**: H.265 cameras (Xiaomi CS2, ONVIF H.265) in a 4-camera grid on HTTPS.
> **Impact (pre-fix)**: 1 of 4 cameras could demote to HLS and trigger a
> console-freezing state-oscillation loop; LiveView failed to play for H.265
> cameras whose DB `encoding` was stale.

## Problem

In a 4-camera surveillance grid where all cameras are H.265 (HTTPS, WebCodecs available):

- **3/4 cameras** stay on WebCodecs and play correctly.
- **1 camera** (typically the one with the slowest P2P connection) demotes to HLS,
  then HLS's hls.js oscillates between `loading ↔ buffering` hundreds of times per second
  (7428 statechange events in 55s observed). Each state change fires Svelte's `$effect`
  dispatcher, overloading the reactive scheduler and freezing the browser console.

## Root Cause (as originally diagnosed)

1. **WebCodecs demote under load**: With 4 simultaneous WS connections, the Xiaomi CS2
   camera with the slowest P2P connection has delayed frame delivery during the first
   ~20s. The wall-clock no-media guard (`NO_MEDIA_TOTAL_MS`) misjudges this as
   "no media" and demotes to HLS. Single-camera testing confirms this camera is stable
   on WebCodecs — the issue is multi-camera load causing frame-delivery timing contention.

2. **HLS H.265 state-oscillation**: After demotion to HLS, hls.js feeds H.265 to MSE,
   which reports `isTypeSupported('hvc1') = true` but the actual decode stalls at
   `readyState=1` (HAVE_METADATA). hls.js rapidly alternates `loading ↔ buffering`,
   and `VideoPlayer.$effect(dispatchStateChange)` fires on every transition →
   Svelte scheduler overload → console freeze.

3. **Investigation correction (issue #107 deep-dive)**: the chain-walk demote could
   not actually reach HLS for an H.265/no-MSE camera (the codec gate excludes it),
   so the real trigger was a transient `/protocols` fetch failure collapsing
   `buildCandidateChain` to a forced single-`hls` chain via its `!resp` branch —
   which bypassed the codec gate entirely. Either way, the un-debounced state
   dispatch turned the resulting HLS H.265 stall into a console freeze.

## Fixes Applied (this branch)

1. **Debounced + deduped state dispatch** (`web/src/lib/player/dispatch.ts`):
   every player adapter (Video/Flv/Wasm/WebRTC) now routes its `statechange`
   CustomEvent through a shared dispatcher that (a) drops identical-to-last
   states and (b) coalesces non-`playing` states into a single trailing
   dispatch within a 500ms window. `playing` (recovery) flushes immediately and
   cancels any pending trailing dispatch. A burst of hls.js buffering↔playing
   oscillation now collapses to ~1 event per window instead of thousands/sec.
   Assignment-side dedupe was also added to `updateState` in each player so the
   `$effect` itself doesn't re-run on redundant assignments.

2. **LiveView no longer clobbers its own registration** (issue #108): the
   `listProtocols()` `.then()` used to re-call `registerCamera(makeRegistration(
   camera, null, …))`, racing the real registration done in `loadCamera()`. The
   null `resp` collapsed the chain to forced HLS against the codec gate. Removed;
   `listProtocols()` now only populates the global `protocolsMap` (PTZ/HLS
   capability gating) and re-registers with the SAME probed resp when it lands.

3. **LiveView ProtocolSwitcher reads the probed encoding** (issue #108): the
   `cameraEncoding` prop now prefers `cameraProtocolsResp.encoding` (recorder-
   probed, authoritative) over the possibly-stale DB `camera.encoding`, so an
   H.265 camera stored as h264 still shows its H.265 badge and hides WebRTC.

4. **`buildCandidateChain` `!resp` branch no longer traps H.265 on HLS**:
   when `/protocols` fetch fails (resp=null) and the camera is H.265 with a
   working decode path (WebCodecs or libde265 WASM), the chain is now
   `[{wasm}]` instead of the forced `[{hls}]`. HLS H.265 via MSE is a black
   screen + oscillation in most browsers; wasm always works. H.264 behavior
   is unchanged (still HLS).

5. **Orchestrator storm regression guards** (`orchestrator.test.ts`): tests
   confirm a high-frequency buffering↔playing oscillation that returns to ok
   does NOT demote, and repeated degraded reports arm exactly one degrade
   timer (debounce is idempotent).

## What IS Fixed (in the player-orchestrator-adaptive branch, PR #106)

- ✅ Backend encoding probe: Xiaomi cameras now correctly report h265 (was h264)
- ✅ Triple visibility conflict eliminated (ConnectionManager/WasmPlayer/orchestrator)
- ✅ WS zombie/handshake/wall-clock triple guard
- ✅ Frame delivery recorded even during decoder-init pause
- ✅ WebRTC WHEP timeout + connection timeout + firstFramePlayed
- ✅ Decode error threshold raised (10→50)
- ✅ Service Worker per-build cache version
- ✅ Pinned-protocol terminal-failure fallback to HLS
