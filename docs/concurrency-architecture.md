# Concurrency Architecture

This document records the synchronization discipline of the NVR's hot paths so
contributors can reason about correctness **and** avoid re-proposing refactors
that were already evaluated and rejected. It is the durable companion to the
GitHub issues that established each decision (#219, #220, #226, #230, #231,
#234, #245, #246).

## Synchronization primitives in use

| Primitive | Where | Why |
|---|---|---|
| `sync.Mutex` | `baseRecorder.mu`, `Manager.mu` (hls/cleanup/…), `muxer.MP4Muxer.mu` | Multi-field consistency on lifecycle/per-segment state |
| `sync.RWMutex` | `StreamHub.mu`, `CameraManager` snapshot reads | Many readers, rare writers |
| `atomic.Pointer[T]` | `baseRecorder.codec` (#219), `baseRecorder.audio` (#226), `CameraManager.snapshot` | Immutable config snapshot published once, read by many goroutines |
| `atomic.Int64` | frame counters, bitrate accounting | Independent counters, no multi-field consistency needed |
| `sync.Map` | `CameraManager.lifecycleMu` (per-camera lifecycle guards) | Per-key (camera) mutex, rare contention |
| Go channels | `EventBus` per-subscriber delivery, recorder `frameCh` | MPSC queues; the runtime's lock-free fast path is well-optimized on ARM |

## recorder: three-tier lock discipline (`internal/recorder/base.go`)

`baseRecorder` fields are classified into three tiers. The classification is
enforced by doc-comments on the struct and guarded by `go test -race` (see
`codec_race_test.go`, `audio_race_test.go`).

### Tier 1 — lifecycle + per-segment state, guarded by `mu`

```
status, cancel, done, muxer, audioTrackID, segStart
```

`mu` serializes multi-field consistency. The `writeFrames` goroutine is the
sole writer of `muxer`/`segStart`/`audioTrackID`; the lock exists for the
**audio RTP callback** reader (a separate goroutine) and the lifecycle methods.
`createNewSegment` publishes `muxer`+`segStart`+`audioTrackID` together under
one `Lock`/`Unlock` so the audio callback observes an aligned triplet (#226
fixed a torn-view bug where `audioTrackID` was published outside that block).

### Tier 2 — cross-goroutine configuration, behind `atomic.Pointer`

```
codec  atomic.Pointer[codecParams]   // SPS/PPS/VPS — #219
audio  atomic.Pointer[audioConfig]   // codec/sampleRate/channels/muxerConfig/g711* — #226
```

Both are written **once** during `connectAndRecord` and read by many goroutines
(`writeFrames`, the audio RTP callback, and the external HLS/WebRTC/WS/relay/
status accessor paths). The snapshot is immutable after `Store`, which makes
the concurrent reads race-free without a mutex and guarantees a coherent view
(no torn `codec`↔`muxerConfig` pairing). Byte slices (`sps`/`pps`/`vps`,
`muxerConfig`) are deep-copied on `Store` so the snapshot is independent of the
writer's reusable buffers.

### Tier 3 — `writeFrames`-owned, NO lock (single-goroutine invariant)

```
trackID, curFinalPath, curTempPath, frameCount, lastFrameTime
```

Only the `writeFrames` goroutine reads/writes these. HTTP handlers and RTP
callbacks MUST NOT touch them — there is intentionally no lock, and adding
cross-goroutine access here would require promoting the field to Tier 1 or 2.

### Why the muxer is safe to call from two goroutines

`muxer.MP4Muxer` is itself goroutine-safe: `WriteSample` and `WriteAudioSample`
each take the muxer's own `sync.Mutex` (`internal/muxer/mp4mux.go`). So
concurrent video writes (from `writeFrames`) and audio writes (from the RTP
callback) are safe by construction — the concern in #226 was the audio
**config** fields, not the muxer.

## StreamHub: snapshot-then-release

`StreamHub.Broadcast` takes `h.mu` only long enough to snapshot the consumer
list, then releases the lock before invoking each consumer's callback. This is
the textbook pattern for "one producer, many dynamic subscribers" and keeps
`Broadcast` (the RTP hot path, ~320 broadcasts/sec) from holding the lock
across slow consumer callbacks. Subscribe/Unsubscribe are rare (play/stop
events), so `h.mu` is uncontended on the hot path.

## EventBus: snapshot pattern + MPSC channels

`Publish` takes `b.mu` only to snapshot the subscriber list, then releases
before sending. Each subscriber's delivery is an **MPSC** (multiple-producer,
single-consumer) Go channel — many cameras/goroutines call `Publish`
concurrently. Go channels are the correct primitive here (the runtime's
lock-free fast path is well-optimized on ARM); a hand-rolled SPSC ring would be
unsafe under multiple producers on ARM's weak memory model.

---

# Rejected refactors (do not re-propose)

Three lock-free / low-lock refactors were evaluated during the concurrency
audit + Oracle consultation and **explicitly rejected**. They are documented
here so future contributors don't re-litigate them. (Source: #246.)

## ❌ 1. StreamHub consumers → COW `atomic.Pointer[[]*consumerEntry]`

**Proposed:** Replace `h.mu` with a copy-on-write immutable consumer slice;
Subscribe/Unsubscribe do append/filter + CAS.

**Rejected:**
- `h.mu` is **uncontended on the hot path** — one RTP-producer goroutine per
  hub; Subscribe/Unsubscribe are rare (play/stop events).
- At N=20 consumers, the slice copy under lock is ~160 bytes — sub-microsecond
  on Cortex-A53.
- COW doesn't eliminate `h.mu` anyway: `idrCache` (protected by `h.mu`) still
  needs it → you'd end up with two synchronization mechanisms.
- Adds CAS retry loops + allocations on every Subscribe/Unsubscribe.

**Verdict:** Not worth it at 320 broadcasts/sec on RPi 3B. The
Mutex→RWMutex tweak (#245) is the maximum justified change.

## ❌ 2. StreamHub hooks → `atomic.Pointer[HubHooks]`

**Proposed:** Extract the 8 callback hooks (`OnDrop`, `OnBroadcast`,
`OnBufferDepth`, …) into an immutable struct set via `atomic.Pointer.Store`.

**Rejected:**
- All 8 hooks are plain fields set **once** in `initStreamHub`, which runs
  before `Start()` / the first `Broadcast`.
- There's a happens-before edge via the goroutine spawn sequence
  (constructor → `initStreamHub` → `Start`).
- **No race exists today.** Adding `Load()` per `Broadcast` is motion for zero
  correctness gain.

**Adopted instead:** A doc comment on the struct: hooks must be set before
`Start()`; do not mutate after the first `Broadcast`. (If runtime hook
hot-swap is ever added — e.g. debug instrumentation — promote to
`atomic.Pointer[HubHooks]` at that time; ~10-minute change.)

## ❌ 3. EventBus → SPSC ring buffer per subscriber

**Proposed:** Replace each subscriber's buffered channel with a hand-rolled
SPSC (single-producer, single-consumer) ring buffer; eliminates `s.mu`.

**Rejected:**
- The bus is **MPSC** per subscriber channel: many cameras/goroutines call
  `Publish` concurrently (verified: `Publish` releases `b.mu` before sending,
  so publishers overlap). A hand-rolled SPSC ring is **unsafe under multiple
  producers** — you'd get lost writes or memory corruption on ARM's weak
  memory model.
- Go channels are already the right primitive and are well-optimized on ARM.
- At 320 events/sec × 20 subscribers = 6,400 sends/sec, channel send cost
  (~50ns on A53) is ~0.3ms/sec total — 0.03% of one core. Not a hotspot.

**Verdict:** Wrong topology (MPSC, not SPSC) + not worth it (channel cost
negligible).

---

## What WAS approved and landed

| Issue | Change |
|---|---|
| #219 | `baseRecorder` SPS/PPS/VPS → `atomic.Pointer[codecParams]` |
| #220 | EventBus drain guard on shutdown |
| #226 | `baseRecorder` audio config → `atomic.Pointer[audioConfig]`; three-tier discipline documented; `audioTrackID` publish-ordering fix |
| #230 | HLS `m.mu`/`entry.mu` lock-order audit (no cycle) + `writeLoop`/`idleWatchdog` WaitGroup join |
| #231 / #234 | Xiaomi `cs2.mu`/`cmdMu` + HLS lock-order audits (no cycles; documented) |
| #245 | StreamHub `Mutex`→`RWMutex` (the maximum justified change) |
