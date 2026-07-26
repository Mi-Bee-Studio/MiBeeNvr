import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createStateDispatcher, DEBOUNCE_MS, type PlayerState } from '$lib/player/dispatch';

// The dispatcher uses real setTimeout by default; tests inject a controllable
// scheduler so the debounce timing is deterministic without vitest fake timers
// (either works; the injected form makes the assertions explicit about which
// timer fired and is easier to reason about for the "playing cancels pending"
// case).

interface FakeTimer {
  fn: () => void;
  ms: number;
  fired: boolean;
}
let queue: FakeTimer[] = [];

function sched(fn: () => void, ms: number): FakeTimer {
  const t: FakeTimer = { fn, ms, fired: false };
  queue.push(t);
  return t;
}
function clear(t: FakeTimer): void {
  const i = queue.indexOf(t);
  if (i >= 0) queue.splice(i, 1);
}
/** Advance fake time, firing timers whose delay has elapsed (in insertion order). */
function advance(ms: number): void {
  const ready = queue.filter((t) => t.ms <= ms).sort((a, b) => a.ms - b.ms);
  for (const t of ready) {
    if (t.fired) continue;
    t.fired = true;
    const i = queue.indexOf(t);
    if (i >= 0) queue.splice(i, 1);
    t.fn();
  }
}
function pendingCount(): number {
  return queue.length;
}

beforeEach(() => {
  queue = [];
});

afterEach(() => {
  queue = [];
});

describe('createStateDispatcher — dedupe', () => {
  it('does not emit when the same state is reported twice in a row', () => {
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    d.report('buffering');
    advance(DEBOUNCE_MS);
    d.report('buffering'); // identical → deduped
    advance(DEBOUNCE_MS);
    expect(emitted).toEqual(['buffering']);
  });

  it('does not emit a duplicate even before the trailing timer fires', () => {
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    d.report('buffering');
    // Immediately report buffering again WITHOUT advancing — pending state is
    // 'buffering', so the second call is a no-op.
    d.report('buffering');
    advance(DEBOUNCE_MS);
    expect(emitted).toEqual(['buffering']);
    expect(pendingCount()).toBe(0);
  });
});

describe('createStateDispatcher — debounce', () => {
  it('coalesces a burst of distinct non-playing states into the last one', () => {
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    // Simulate the hls.js oscillation: buffering → loading → buffering within
    // the debounce window. Only the final 'buffering' should be emitted.
    d.report('buffering');
    d.report('loading');
    d.report('buffering');
    d.report('loading');
    d.report('buffering');
    expect(emitted).toEqual([]); // nothing yet — all debounced
    expect(pendingCount()).toBe(1); // exactly one trailing timer
    advance(DEBOUNCE_MS);
    expect(emitted).toEqual(['buffering']); // only the last
  });

  it('emits once per debounce window, not per report (the issue #107 storm guard)', () => {
    // The bug: 7428 statechange events in 55s. With a 500ms debounce the worst
    // case is ~110 events in 55s even if a new state arrives every tick.
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    for (let i = 0; i < 1000; i++) {
      // Alternate to bypass dedupe but stay within the window.
      d.report(i % 2 === 0 ? 'buffering' : 'loading');
    }
    advance(DEBOUNCE_MS);
    expect(emitted.length).toBe(1);
  });

  it('reports a genuinely later state change after the window elapses', () => {
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    d.report('buffering');
    advance(DEBOUNCE_MS);
    expect(emitted).toEqual(['buffering']);
    d.report('error');
    advance(DEBOUNCE_MS);
    expect(emitted).toEqual(['buffering', 'error']);
  });
});

describe('createStateDispatcher — playing flushes immediately', () => {
  it('emits playing synchronously (no debounce) so recovery is reported at once', () => {
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    d.report('playing');
    expect(emitted).toEqual(['playing']);
    expect(pendingCount()).toBe(0);
  });

  it('cancels a pending trailing dispatch when playing arrives (recovery supersedes it)', () => {
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    d.report('buffering'); // arms a trailing timer
    expect(pendingCount()).toBe(1);
    d.report('playing'); // flush + cancel
    expect(emitted).toEqual(['playing']);
    expect(pendingCount()).toBe(0);
    advance(DEBOUNCE_MS); // would have fired the buffered 'buffering' — must NOT
    expect(emitted).toEqual(['playing']);
  });

  it('dedupes consecutive playing reports (no spurious duplicate)', () => {
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    d.report('playing');
    d.report('playing'); // duplicate → no-op
    d.report('playing'); // duplicate → no-op
    expect(emitted).toEqual(['playing']);
  });
});

describe('createStateDispatcher — dispose', () => {
  it('cancels the pending trailing dispatch on dispose', () => {
    const emitted: PlayerState[] = [];
    const d = createStateDispatcher((s) => emitted.push(s), { sched, clear });
    d.report('buffering');
    expect(pendingCount()).toBe(1);
    d.dispose();
    expect(pendingCount()).toBe(0);
    advance(DEBOUNCE_MS);
    expect(emitted).toEqual([]);
  });
});
