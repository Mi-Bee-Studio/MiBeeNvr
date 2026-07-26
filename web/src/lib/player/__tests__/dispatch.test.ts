import { describe, it, expect, beforeEach } from 'vitest';
import { createStateDispatcher, DEBOUNCE_MS, type PlayerState } from '$lib/player/dispatch';

// The dispatcher defers EVERY emit out of the synchronous stack (microtask for
// 'playing', setTimeout for the debounced trailing flush) so the downstream
// orchestrator state writes cannot run inside a Svelte effect flush (the fix
// for effect_update_depth_exceeded). To make the async ordering deterministic
// WITHOUT fighting vitest's fake-timer/microtask interplay, tests inject:
//   - `sched`: a manually-flushed queue standing in for the microtask path
//     (used by 'playing'), AND
//   - `clear` + a controllable `setTimeout`-style trailing timer.
// Both are driven by explicit flush helpers below.

interface FakeTimer {
  fn: () => void;
  fired: boolean;
}
let microtasks: Array<() => void> = [];
let timers: FakeTimer[] = [];

beforeEach(() => {
  microtasks = [];
  timers = [];
});

// Inject a microtask-style scheduler: collects callbacks to flush explicitly.
function schedMicrotask(fn: () => void): void {
  microtasks.push(fn);
}
function flushMicrotasks(): void {
  // Drain the queue (microtasks may queue more microtasks).
  while (microtasks.length > 0) {
    const fn = microtasks.shift()!;
    fn();
  }
}
// Trailing-edge setTimeout stand-in: collect + manual advance.
function schedTimer(fn: () => void): FakeTimer {
  const t: FakeTimer = { fn, fired: false };
  timers.push(t);
  return t;
}
function clearTimer(t: FakeTimer): void {
  const i = timers.indexOf(t);
  if (i >= 0) timers.splice(i, 1);
}
/** Fire every trailing timer whose delay has elapsed (we don't model ms precisely;
 *  calling this stands in for `advanceTimersByTime(DEBOUNCE_MS)`). */
function flushTrailing(): void {
  // Snapshot then clear, since firing may re-arm.
  const ready = [...timers];
  timers = [];
  for (const t of ready) {
    if (t.fired) continue;
    t.fired = true;
    t.fn();
  }
  // Firing the trailing callback is itself synchronous; microtasks queued
  // during it are not part of this model (the real impl uses setTimeout, so
  // microtasks drain between macrotasks — not our concern here).
}
function pendingTrailing(): number {
  return timers.length;
}

function newDispatcher(emit: (s: PlayerState) => void) {
  return createStateDispatcher(emit, {
    sched: schedMicrotask,
    schedTrailing: (fn) => schedTimer(fn),
    clear: clearTimer,
  });
}

describe('createStateDispatcher — dedupe', () => {
  it('does not emit when the same state is reported twice in a row', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('buffering');
    flushTrailing();
    d.report('buffering'); // identical → deduped
    flushTrailing();
    expect(emitted).toEqual(['buffering']);
  });

  it('does not emit a duplicate even before the trailing timer fires', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('buffering');
    d.report('buffering'); // pending === 'buffering' → no-op
    flushTrailing();
    expect(emitted).toEqual(['buffering']);
  });
});

describe('createStateDispatcher — debounce', () => {
  it('coalesces a burst of distinct non-playing states into the last one', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('buffering');
    d.report('loading');
    d.report('buffering');
    d.report('loading');
    d.report('buffering');
    expect(emitted).toEqual([]); // nothing yet — all debounced
    expect(pendingTrailing()).toBe(1); // exactly one trailing timer
    flushTrailing();
    expect(emitted).toEqual(['buffering']); // only the last
  });

  it('emits once per debounce window, not per report (the issue #107 storm guard)', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    for (let i = 0; i < 1000; i++) {
      d.report(i % 2 === 0 ? 'buffering' : 'loading');
    }
    flushTrailing();
    expect(emitted.length).toBe(1);
  });

  it('reports a genuinely later state change after the window elapses', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('buffering');
    flushTrailing();
    expect(emitted).toEqual(['buffering']);
    d.report('error');
    flushTrailing();
    expect(emitted).toEqual(['buffering', 'error']);
  });
});

describe('createStateDispatcher — playing flushes asynchronously (never synchronous)', () => {
  it('does NOT emit playing synchronously (must defer out of any effect flush)', () => {
    // THE regression guard for effect_update_depth_exceeded: emit must run on a
    // microtask, not in the report() call stack.
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('playing');
    expect(emitted).toEqual([]); // not yet — microtask not flushed
    flushMicrotasks();
    expect(emitted).toEqual(['playing']);
  });

  it('playing emits before the 500ms debounce window (microtask is near-immediate)', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('playing');
    flushMicrotasks();
    expect(emitted).toEqual(['playing']);
    expect(pendingTrailing()).toBe(0);
  });

  it('cancels a pending trailing dispatch when playing arrives (recovery supersedes it)', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('buffering'); // arms a trailing timer
    expect(pendingTrailing()).toBe(1);
    d.report('playing'); // microtask flush + cancel trailing
    flushMicrotasks();
    expect(emitted).toEqual(['playing']);
    expect(pendingTrailing()).toBe(0);
    flushTrailing(); // would have fired the buffered 'buffering' — must NOT
    expect(emitted).toEqual(['playing']);
  });

  it('coalesces consecutive playing reports into a single microtask flush', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('playing');
    d.report('playing'); // deduped — microtask already queued
    d.report('playing');
    flushMicrotasks();
    expect(emitted).toEqual(['playing']); // exactly one emit
  });

  it('uses DEBOUNCE_MS constant value 500 (documents the storm-bounding window)', () => {
    expect(DEBOUNCE_MS).toBe(500);
  });
});

describe('createStateDispatcher — dispose', () => {
  it('cancels the pending trailing dispatch on dispose', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('buffering');
    d.dispose();
    flushTrailing();
    expect(emitted).toEqual([]);
  });

  it('makes a queued playing microtask a no-op on dispose', () => {
    const emitted: PlayerState[] = [];
    const d = newDispatcher((s) => emitted.push(s));
    d.report('playing');
    d.dispose(); // clear the queued state before the microtask runs
    flushMicrotasks();
    expect(emitted).toEqual([]);
  });
});
