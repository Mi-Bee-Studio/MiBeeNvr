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
   camera with the slowest P2P connection has delayed frame delivery during startup.
   The no-media watchdog (`NO_MEDIA_TIMEOUT_MS = 30000` in WasmPlayer) could misjudge
   a slow-to-start camera as "no media" and report failure. Single-camera testing
   confirmed such cameras are stable on WebCodecs — the issue was multi-camera load
   causing frame-delivery timing contention. (Note: the final fix does NOT change this
   timeout; the demote itself was correct behavior. The freeze came from the un-debounced
   state dispatch amplifying the resulting transitions, fixed separately.)

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

## Fixes Applied (this branch — PR #120, 4 commits)

### Issue #107 — state-oscillation storm (root cause + cascade)

1. **Debounced + deduped + ASYNC state dispatch** (`web/src/lib/player/dispatch.ts`):
   every player adapter (Video/Flv/Wasm/WebRTC) routes its `statechange`
   CustomEvent through a shared dispatcher that (a) drops identical-to-last
   states and (b) coalesces non-`playing` states into a single trailing
   dispatch within a 500ms window. **Every emit is deferred out of the
   synchronous stack** — `'playing'` (recovery) flushes on a `queueMicrotask`
   (near-immediate but NOT synchronous), other states on `setTimeout`. This is
   critical: a synchronous emit inside the player's `$effect` reached
   `orchestrator.reportHealth → setSlot` (a reactive `$state` write) during
   Svelte's effect flush, and combined with a player remount produced
   `effect_update_depth_exceeded`. Async deferral guarantees the DOM dispatch →
   reportHealth → setSlot chain runs in a fresh task, never inside an effect
   flush. A burst of hls.js buffering↔playing oscillation now collapses to ~1
   event per window instead of thousands/sec.

2. **`reportHealth` short-circuits when health is unchanged** (`orchestrator.svelte.ts`):
   the deepest root cause of the H80-triggered `effect_update_depth_exceeded`.
   `reportHealth` previously called `setSlot` (which does `slots = { ...slots }`
   — a new object) on EVERY report, even when `(status, reason)` were identical
   to the current health. Because `healthFromStreamState('error')` returns a new
   object each call (`since: Date.now()` differs), a reference check could not
   short-circuit. An H.265 camera whose WS never reaches steady-state
   `'playing'` (e.g. H80) reports `'failed'` repeatedly → each report churned
   `slots` → every `$derived(mode)` re-ran → player `$effect`s re-entered →
   effect recursion overflow. Fix: skip the reactive `setSlot` write when
   `slot.health.{status,reason}` equals the incoming `h.{status,reason}`. The
   timers/demote logic uses non-reactive internal bookkeeping and is unaffected.
   Also removed a redundant trailing `setSlot` on the `'ok'` path that would
   re-arm the loop for cameras oscillating around the ok/degraded boundary.

### Issue #108 — H.265 LiveView black screen (codec-data-flow integrity)

3. **CameraPlayer feeds the player the RECORDER-PROBED codec, not the DB value**:
   the direct cause of H80's black screen. `CameraPlayer` derived the player's
   `codec` prop from `camera.encoding` (the DB value). H80 is stored as `h264`
   in the DB but streams `h265`; the orchestrator correctly built a wasm/WebCodecs
   chain from the probed `h265`, but CameraPlayer fed WasmPlayer `codec='h264'`,
   so it configured an H.264 decoder that silently failed to decode the H.265
   NALUs (data was flowing — WS 101 + binary frames — but the decoder mismatch
   produced a permanent black screen). Fix: orchestrator exposes
   `resolvedEncoding(cameraId)` (the same `resolveEncoding(camera, resp)` the
   candidate chain was built from), and CameraPlayer uses it with a DB fallback.
   This is a generic data-flow fix — any camera whose DB encoding drifts from
   the real stream benefits, not just H80.

4. **`buildCandidateChain` `!resp` branch no longer traps H.265 on HLS**: when
   the `/protocols` fetch fails (resp=null) and the camera is H.265 with a
   working decode path (WebCodecs or libde265 WASM), the chain is now `[{wasm}]`
   instead of the forced `[{hls}]`. HLS H.265 via MSE is a black screen +
   oscillation in most browsers; wasm always works. H.264 behavior is unchanged.

5. **LiveView no longer clobbers its own registration**: the `listProtocols()`
   `.then()` used to re-call `registerCamera(makeRegistration(camera, null, …))`,
   racing the real registration done in `loadCamera()`. The null `resp`
   collapsed the chain to forced HLS against the codec gate. Removed;
   `listProtocols()` now only populates the global `protocolsMap` and
   re-registers with the SAME probed resp (guarded by a `cameraRegistered`
   flag) when it lands.

6. **LiveView ProtocolSwitcher reads the probed encoding**: the `cameraEncoding`
   prop now prefers `cameraProtocolsResp.encoding` (recorder-probed,
   authoritative) over the possibly-stale DB `camera.encoding`, so an H.265
   camera stored as h264 still shows its H.265 badge and hides WebRTC.

### Regression coverage

- `dispatch.test.ts` (12): dedupe, debounce coalescing, **`'playing'` must NOT
  emit synchronously** (the effect_update_depth guard), `'playing'` cancels
  pending trailing, dispose cancels both queues.
- `orchestrator.test.ts` (+3): a high-frequency buffering↔playing oscillation
  that returns to ok does NOT demote; repeated degraded reports arm exactly one
  degrade timer; **repeated `'failed'` on an exhausted chain does NOT rewrite
  `slots`** (the H80 regression — slot reference stays stable across 50
  identical reports).
- `stream-selection.test.ts` (+4): `!resp` + H.265 + WebCodecs/wasm → `[{wasm}]`;
  H.264 + `!resp` still HLS (regression guard).

## Verification (Banana Pi M5, real cameras)

- **#107 grid storm**: 4-camera H.265 grid stable after 30s+ soak; no console
  freeze; click responsiveness 100–200ms (the pre-fix storm froze the console
  within 55s). All H.265 cameras stayed on WebCodecs (never demoted to HLS).
- **effect_update_depth_exceeded regression**: H80 (cam-608e6966) added to the
  grid + 60s soak (past the 30s `noMediaTimer` + multiple WS reconnect cycles):
  no freeze, `allAlive: true`, expand/shrink/drag all responsive.
- **#108 H80 black screen**: LiveView now shows **"实时" (live)** on WebCodecs
  with the correct H.265 decoder (was "快照模式"/black). ProtocolSwitcher
  shows "H.265 WebCodecs" (was "H.264"). WS handshake confirmed working
  (101 + binary H.265 frames) — the black screen was purely a decoder-config
  mismatch, not a connection issue.
- Backend logs clean; 9 cameras recording; no regressions on healthy cameras.

## What IS Fixed (in the player-orchestrator-adaptive branch, PR #106)

- ✅ Backend encoding probe: Xiaomi cameras now correctly report h265 (was h264)
- ✅ Triple visibility conflict eliminated (ConnectionManager/WasmPlayer/orchestrator)
- ✅ WS zombie/handshake/wall-clock triple guard
- ✅ Frame delivery recorded even during decoder-init pause
- ✅ WebRTC WHEP timeout + connection timeout + firstFramePlayed
- ✅ Decode error threshold raised (10→50)
- ✅ Service Worker per-build cache version
- ✅ Pinned-protocol terminal-failure fallback to HLS
