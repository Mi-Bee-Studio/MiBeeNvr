import { describe, it, expect } from 'vitest';
import { findSegmentAt, parseDayStart, formatLength, epochMsToDaySec } from '$lib/timeline-utils';

describe('findSegmentAt', () => {
  const segments = [
    { id: 'a', startSec: 0, endSec: 300 },       // 00:00–00:05
    { id: 'b', startSec: 600, endSec: 1200 },    // 00:10–00:20
    { id: 'c', startSec: 3600, endSec: 5400 },   // 01:00–01:30
  ];

  it('hits a segment exactly', () => {
    const r = findSegmentAt(segments, 100);
    expect(r.seg?.id).toBe('a');
    expect(r.offset).toBe(100);
    expect(r.snapped).toBe(false);
  });

  it('hits segment boundaries (start)', () => {
    const r = findSegmentAt(segments, 600);
    expect(r.seg?.id).toBe('b');
    expect(r.offset).toBe(0);
    expect(r.snapped).toBe(false);
  });

  it('hits segment boundaries (end)', () => {
    const r = findSegmentAt(segments, 5400);
    expect(r.seg?.id).toBe('c');
    expect(r.offset).toBe(1800);
    expect(r.snapped).toBe(false);
  });

  it('snaps forward to next segment when target is in a gap', () => {
    // 400s is between a (ends 300) and b (starts 600) → snap to b start
    const r = findSegmentAt(segments, 400);
    expect(r.seg?.id).toBe('b');
    expect(r.offset).toBe(0);
    expect(r.snapped).toBe(true);
  });

  it('snaps forward to next segment near prior segment end', () => {
    // 350s is just after a ends → snap to b start
    const r = findSegmentAt(segments, 350);
    expect(r.seg?.id).toBe('b');
    expect(r.snapped).toBe(true);
  });

  it('snaps backward to prior segment when target is after all segments', () => {
    // 99999s is after everything → snap to end of last segment c
    const r = findSegmentAt(segments, 99999);
    expect(r.seg?.id).toBe('c');
    expect(r.offset).toBe(1800);
    expect(r.snapped).toBe(true);
  });

  it('snaps forward when target is before first segment', () => {
    const segs = [{ id: 'x', startSec: 100, endSec: 200 }];
    const r = findSegmentAt(segs, 50);
    expect(r.seg?.id).toBe('x');
    expect(r.offset).toBe(0);
    expect(r.snapped).toBe(true);
  });

  it('returns null for empty segments', () => {
    const r = findSegmentAt([], 100);
    expect(r.seg).toBeNull();
    expect(r.offset).toBe(0);
    expect(r.snapped).toBe(false);
  });

  it('handles unsorted input by sorting internally', () => {
    const unsorted = [
      { id: 'c', startSec: 3600, endSec: 5400 },
      { id: 'a', startSec: 0, endSec: 300 },
      { id: 'b', startSec: 600, endSec: 1200 },
    ];
    const r = findSegmentAt(unsorted, 400);
    expect(r.seg?.id).toBe('b');
    expect(r.snapped).toBe(true);
  });
});

describe('parseDayStart', () => {
  it('parses YYYY-MM-DD to UTC midnight', () => {
    const ms = parseDayStart('2026-06-16');
    expect(new Date(ms).toISOString()).toBe('2026-06-16T00:00:00.000Z');
  });

  it('handles month/year correctly', () => {
    const ms = parseDayStart('2026-01-01');
    expect(new Date(ms).getUTCMonth()).toBe(0);
    expect(new Date(ms).getUTCDate()).toBe(1);
  });
});

describe('epochMsToDaySec', () => {
  it('converts epoch ms to seconds from midnight', () => {
    const dayStart = parseDayStart('2026-06-16');
    // 06:00 UTC = 21600 seconds
    const sixAM = Date.UTC(2026, 5, 16, 6, 0, 0);
    expect(epochMsToDaySec(sixAM, dayStart)).toBe(21600);
  });

  it('returns negative for times before day start', () => {
    const dayStart = parseDayStart('2026-06-16');
    const prev = Date.UTC(2026, 5, 15, 23, 0, 0);
    expect(epochMsToDaySec(prev, dayStart)).toBe(-3600);
  });
});

describe('formatLength', () => {
  it('formats seconds only', () => {
    expect(formatLength(45)).toBe('45s');
  });

  it('formats minutes + seconds', () => {
    expect(formatLength(200)).toBe('3m20s');
  });

  it('formats hours + minutes', () => {
    expect(formatLength(3900)).toBe('1h05m');
  });

  it('formats zero', () => {
    expect(formatLength(0)).toBe('0s');
  });
});
