import { describe, it, expect } from 'vitest';
import { remoteDayBounds, computePresetWindowStart, createDayCache, pad2 } from '../lib/recordings-windows';

describe('remoteDayBounds', () => {
  it('spans the whole local day inclusively', () => {
    const { start, end } = remoteDayBounds('2026-11-19');
    expect(start).toBe(new Date(2026, 10, 19, 0, 0, 0).toISOString());
    expect(end).toBe(new Date(2026, 10, 19, 23, 59, 59, 999).toISOString());
  });

  it('round-trips the calendar date (no off-by-one from UTC conversion)', () => {
    const { start } = remoteDayBounds('2026-01-01');
    const d = new Date(start);
    expect(d.getFullYear()).toBe(2026);
    expect(d.getMonth()).toBe(0);
    expect(d.getDate()).toBe(1);
  });
});

describe('computePresetWindowStart', () => {
  const now = new Date('2026-11-19T15:30:00');

  it('natural-day snaps to local midnight of the same day', () => {
    const got = computePresetWindowStart('natural-day', now);
    expect(got.getHours()).toBe(0);
    expect(got.getMinutes()).toBe(0);
    expect(got.getDate()).toBe(19);
  });

  it('rolling windows subtract their span', () => {
    expect(now.getTime() - computePresetWindowStart('8h', now).getTime()).toBe(8 * 3600 * 1000);
    expect(now.getTime() - computePresetWindowStart('24h', now).getTime()).toBe(24 * 3600 * 1000);
    expect(now.getTime() - computePresetWindowStart('7d', now).getTime()).toBe(7 * 24 * 3600 * 1000);
    expect(now.getTime() - computePresetWindowStart('30d', now).getTime()).toBe(30 * 24 * 3600 * 1000);
  });

  it('unknown duration falls back to 24h', () => {
    expect(now.getTime() - computePresetWindowStart('bogus', now).getTime()).toBe(24 * 3600 * 1000);
  });
});

describe('createDayCache', () => {
  it('returns the entry for the same key inside the TTL', () => {
    const cache = createDayCache<number[], string[], object>();
    cache.put('d1', [1], ['a'], {});
    const hit = cache.take('d1');
    expect(hit?.recordings).toEqual([1]);
    expect(hit?.events).toEqual(['a']);
  });

  it('misses on a different key', () => {
    const cache = createDayCache<number[], string[], object>();
    cache.put('d1', [1], ['a'], {});
    expect(cache.take('d2')).toBeNull();
  });

  it('replaces the previous day (single-slot cache)', () => {
    const cache = createDayCache<number[], string[], object>();
    cache.put('d1', [1], [], {});
    cache.put('d2', [2], [], {});
    expect(cache.take('d1')).toBeNull();
    expect(cache.take('d2')?.recordings).toEqual([2]);
  });

  it('expires after the TTL', async () => {
    const cache = createDayCache<number[], string[], object>();
    cache.put('d1', [1], [], {});
    const realNow = Date.now;
    Date.now = () => realNow() + 31_000;
    try {
      expect(cache.take('d1')).toBeNull();
    } finally {
      Date.now = realNow;
    }
  });
});

describe('pad2', () => {
  it('pads single digits', () => {
    expect(pad2(5)).toBe('05');
    expect(pad2(12)).toBe('12');
  });
});
