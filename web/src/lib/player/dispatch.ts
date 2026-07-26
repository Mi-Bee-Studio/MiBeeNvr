/**
 * Debounced + deduped + ASYNC player-state dispatch.
 *
 * Why this exists (issue #107): every player adapter (Video/Flv/Wasm/WebRTC)
 * fed its `streamState` through a Svelte `$effect` that fired a `statechange`
 * CustomEvent on every transition. When hls.js oscillated between
 * `buffering ↔ playing` (its `FRAG_LOADED → 'playing'` vs fatal
 * `NETWORK_ERROR → 'buffering'`), the effect re-ran thousands of times per
 * second, each dispatch synchronously reaching the orchestrator's
 * `reportHealth` (which reassigns the reactive `slots` map) → Svelte scheduler
 * overload → console freeze (7428 events/55s observed).
 *
 * ## Architecture: why EVERY emit must be asynchronous (the synchronous-emit trap)
 *
 * The player's `$effect` reads `streamState` and calls `dispatchStateChange`.
 * That dispatch flows to `orchestrator.reportHealth`, which **unconditionally
 * reassigns the reactive `slots` `$state`** (a new object every call — see
 * orchestrator.svelte.ts `setSlot`). `slots` is read by CameraPlayer's
 * `mode = $derived(orchestrator.activeMode(...))`. When `reportHealth` triggers
 * a `demote`/`upgrade`, `activeIndex` changes → `mode` changes → CameraPlayer
 * unmounts one player and mounts another → the new player's `$effect` runs on
 * mount → calls `dispatchStateChange('loading')` → ...
 *
 * If `emit()` were SYNCHRONOUS (called directly inside the player's `$effect`),
 * that whole chain executes WITHIN Svelte's current effect-flush stack. The
 * `slots` write during an effect's execution makes Svelte immediately re-process
 * queued effects; combined with a remount, the new player's `$effect` re-enters
 * the same path synchronously → `effect_update_depth_exceeded` (the regression
 * observed when this module's first revision flushed `'playing'` synchronously
 * while debouncing the other states — out-of-order delivery flipped
 * `activeIndex` during a synchronous flush window).
 *
 * **The fix: every emit is deferred out of the current synchronous stack** via
 * `queueMicrotask` (for `'playing'`, near-immediate recovery) or `setTimeout`
 * (for the debounced non-recovery states). This guarantees the DOM
 * `dispatchEvent` → `reportHealth` → `setSlot` chain runs in a fresh task,
 * NEVER inside Svelte's effect flush. No synchronous `$state` write can happen
 * during an effect → no depth recursion → no crash. The debounce still bounds
 * the storm; the async deferral preserves Svelte's effect quiescence.
 *
 * This module is intentionally DOM-light and Svelte-free: the caller passes an
 * `emit` callback. Keeping it pure makes it unit-testable without jsdom.
 */

/**
 * Player state strings the adapters report. Kept open-ended (`string`) rather
 * than a closed union so each adapter's own (slightly diverging) state type —
 * WasmPlayer adds `'disconnected' | 'offline'`, others widen StreamState with
 * `'loading'` — can flow through unchanged. Only `'playing'` has special
 * semantics (low-latency microtask flush); every other value is debounced.
 */
export type PlayerState = string;

/** The single state value that bypasses the debounce (near-immediate flush). */
export const IMMEDIATE_STATE = 'playing';

/**
 * Window within which consecutive non-`playing` states are coalesced into a
 * single trailing dispatch. 500ms is short enough that a genuine state change
 * (loading → error) is still reported promptly, but long enough that hls.js's
 * sub-second buffering↔playing oscillation collapses to one event. Tuned to
 * match the docs/known-issues-h265-multicam.md recommendation.
 */
export const DEBOUNCE_MS = 500;

export interface StateDispatcher {
  /** Report a new state. Deduped + debounced + async-deferred per the rules above. */
  report(state: PlayerState): void;
  /** Cancel any pending dispatch. Call on unmount. */
  dispose(): void;
}

/**
 * Create a debounced + deduped + async state dispatcher.
 *
 * @param emit  Receives the (possibly coalesced) state to actually dispatch.
 *              ALWAYS called from a microtask/timer — never synchronously from
 *              `report()`, so the caller's CustomEvent dispatch (and the
 *              downstream orchestrator state writes) cannot run inside a
 *              Svelte effect flush.
 * @param opts.sched        Injectable ASYNC scheduler for the `'playing'`
 *                          (near-immediate) path. Must defer the call out of
 *                          the current synchronous stack (microtask/macrotask).
 *                          Defaults to `queueMicrotask`.
 * @param opts.schedTrailing Injectable timer for the debounced trailing flush.
 *                          Signature mirrors `setTimeout(fn, ms)`. Defaults to
 *                          the global `setTimeout`.
 * @param opts.clear        Injectable clearer for the trailing timer. Defaults
 *                          to the global `clearTimeout`.
 * @param opts.debounceMs   Trailing debounce window. Defaults to DEBOUNCE_MS.
 */
export function createStateDispatcher(
  emit: (state: PlayerState) => void,
  opts: {
    sched?: (fn: () => void) => void;
    schedTrailing?: (fn: () => void, ms: number) => unknown;
    clear?: (handle: unknown) => void;
    debounceMs?: number;
  } = {},
): StateDispatcher {
  const debounceMs = opts.debounceMs ?? DEBOUNCE_MS;
  // `sched` is used for the IMMEDIATE ('playing') path — it must defer the call
  // out of the current synchronous stack. Default to a microtask so recovery is
  // reported as fast as possible WITHOUT being synchronous.
  const schedImmediate = opts.sched ?? ((fn: () => void) => queueMicrotask(fn));
  // The trailing debounce timer uses setTimeout (a real delay).
  const schedTrailing = opts.schedTrailing ?? globalThis.setTimeout.bind(globalThis);
  const clearTimeoutFn = opts.clear ?? globalThis.clearTimeout.bind(globalThis);

  let lastDispatched: PlayerState | null = null;
  let pending: PlayerState | null = null;
  let trailingTimer: unknown | null = null;
  let immediateQueued = false;
  let immediateState: PlayerState | null = null;

  function clearTrailing(): void {
    if (trailingTimer !== null) {
      clearTimeoutFn(trailingTimer);
      trailingTimer = null;
    }
  }

  function report(state: PlayerState): void {
    // (1) Dedupe: identical to whatever we last emitted OR have pending → no-op.
    // This is the cheap guard that stops the storm when hls.js reports the SAME
    // state repeatedly. (The player's own Svelte $state equality check also
    // dedupes identical-primitive reassignment, but this covers the dispatcher's
    // internal pending/immediate queues too.)
    if (state === lastDispatched && pending === null && !immediateQueued) return;
    if (state === pending) return;

    // (2) `playing` (recovery) flushes on a microtask (near-immediate, but NOT
    // synchronous) and cancels any pending trailing dispatch — a pending
    // degraded state is stale the moment media flows. We coalesce repeated
    // 'playing' reports into a single microtask flush.
    if (state === IMMEDIATE_STATE) {
      clearTrailing();
      pending = null;
      if (immediateQueued) {
        // Already a 'playing' flush scheduled — nothing more to do.
        return;
      }
      immediateQueued = true;
      immediateState = state;
      schedImmediate(() => {
        immediateQueued = false;
        const s = immediateState;
        immediateState = null;
        if (s !== null) {
          lastDispatched = s;
          emit(s);
        }
      });
      return;
    }

    // (3) Non-playing: coalesce into a trailing dispatch. Replace whatever was
    // pending (the latest state wins) and (re)arm the timer. The emit happens
    // asynchronously via the timer, so it cannot run inside a Svelte flush.
    pending = state;
    clearTrailing();
    trailingTimer = schedTrailing(() => {
      trailingTimer = null;
      if (pending !== null) {
        const s = pending;
        pending = null;
        lastDispatched = s;
        emit(s);
      }
    }, debounceMs);
  }

  function dispose(): void {
    clearTrailing();
    // Can't cancel a queued microtask, but zeroing the state makes it a no-op
    // when it fires (post-unmount emit is harmless anyway — the DOM node is
    // gone and dispatchEvent on a detached parent is a silent no-op).
    immediateQueued = false;
    immediateState = null;
    pending = null;
  }

  return { report, dispose };
}
