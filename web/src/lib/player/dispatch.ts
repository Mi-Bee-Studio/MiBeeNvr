/**
 * Debounced + deduped player-state dispatch.
 *
 * Why this exists (issue #107): every player adapter (Video/Flv/Wasm/WebRTC)
 * fed its `streamState` through a Svelte `$effect` that fired a `statechange`
 * CustomEvent on every transition. When hls.js oscillated between
 * `buffering ↔ playing` (its `FRAG_LOADED → 'playing'` vs fatal
 * `NETWORK_ERROR → 'buffering'`), the effect re-ran thousands of times per
 * second, each dispatch synchronously reaching the orchestrator's
 * `reportHealth` (which reassigns the reactive `slots` map) → Svelte's
 * scheduler was flooded and the console froze.
 *
 * This module provides a tiny, framework-agnostic dispatcher that:
 *  1. **Dedupes** — a state equal to the last one dispatched is a no-op.
 *     (The old `updateState` assigned `streamState = state` unconditionally,
 *      so even a redundant assignment re-triggered the effect.)
 *  2. **Debounces** — non-recovered states (`loading`/`buffering`/`error`) are
 *     coalesced into a single trailing dispatch within DEBOUNCE_MS, so a burst
 *     of oscillation collapses to one event per window.
 *  3. **Flushes `playing` immediately** — recovery must be reported at once
 *     (a debounced "playing" would leave the orchestrator in `degraded` and
 *      needlessly arm/keep the degrade timer); any pending trailing dispatch
 *      is cancelled because the immediate `playing` supersedes it.
 *
 * It is intentionally DOM-light: the caller passes an `emit` callback that
 * performs the actual `CustomEvent` dispatch on its own root element. Keeping
 * this pure (no Svelte, no DOM access of its own) makes it unit-testable
 * without jsdom event plumbing.
 */

/**
 * Player state strings the adapters report. Kept open-ended (`string`) rather
 * than a closed union so each adapter's own (slightly diverging) state type —
 * WasmPlayer adds `'disconnected' | 'offline'`, others widen StreamState with
 * `'loading'` — can flow through unchanged. Only `'playing'` has special
 * semantics (immediate flush); every other value is debounced identically.
 */
export type PlayerState = string;

/** The single state value that bypasses the debounce (immediate recovery flush). */
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
  /** Report a new state. Deduped + debounced per the rules above. */
  report(state: PlayerState): void;
  /** Cancel any pending trailing dispatch. Call on unmount. */
  dispose(): void;
}

/**
 * Create a debounced + deduped state dispatcher.
 *
 * @param emit  Receives the (possibly coalesced) state to actually dispatch.
 *              Called synchronously for `playing`, on a timer otherwise.
 * @param sched Injectable timer scheduler for tests (defaults to `setTimeout`).
 * @param clear Injectable timer clearer for tests (defaults to `clearTimeout`).
 */
export function createStateDispatcher(
  emit: (state: PlayerState) => void,
  opts: {
    sched?: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>;
    clear?: (id: ReturnType<typeof setTimeout>) => void;
    debounceMs?: number;
  } = {},
): StateDispatcher {
  const sched = opts.sched ?? setTimeout;
  const clear = opts.clear ?? clearTimeout;
  const debounceMs = opts.debounceMs ?? DEBOUNCE_MS;

  let lastDispatched: PlayerState | null = null;
  let pending: PlayerState | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  function report(state: PlayerState): void {
    // (1) Dedupe: identical to whatever we last emitted OR have pending → no-op.
    // This is the cheap fix that stops the storm when hls.js reports the SAME
    // state repeatedly (the old code reassigned streamState unconditionally,
    // re-triggering the effect each time).
    if (state === lastDispatched && pending === null) return;
    if (state === pending) return;

    // (2) `playing` (recovery) flushes immediately and cancels any trailing
    // dispatch — a pending degraded state is stale the moment media flows.
    // (Reached here only when not already playing, per the dedupe above.)
    if (state === IMMEDIATE_STATE) {
      if (timer !== null) {
        clear(timer);
        timer = null;
      }
      pending = null;
      lastDispatched = state;
      emit(state);
      return;
    }

    // (3) Non-playing: coalesce into a trailing dispatch. Replace whatever was
    // pending (the latest state wins) and (re)arm the timer.
    pending = state;
    if (timer !== null) clear(timer);
    timer = sched(() => {
      timer = null;
      if (pending !== null) {
        const s = pending;
        pending = null;
        lastDispatched = s;
        emit(s);
      }
    }, debounceMs);
  }

  function dispose(): void {
    if (timer !== null) {
      clear(timer);
      timer = null;
    }
    pending = null;
  }

  return { report, dispose };
}
