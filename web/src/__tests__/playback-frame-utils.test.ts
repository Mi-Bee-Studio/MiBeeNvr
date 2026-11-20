import { describe, it, expect } from 'vitest';
import { timelineDate, frameIndexAtOffset, cyclerOffsetSec, formatFrameTimestamp } from '$lib/playback-frame-utils';
import type { Recording, TimelapseFrame } from '$lib/api';

function rec(overrides: Partial<Recording> = {}): Recording {
  return {
    id: 'rec-1',
    camera_id: 'cam-1',
    started_at: '2026-01-15T10:00:00Z',
    ended_at: '2026-01-15T10:01:40Z',
    duration: 100,
    format: 'timelapse',
    ...overrides,
  } as Recording;
}

function framesAt(offsetsSec: number[]): TimelapseFrame[] {
  return offsetsSec.map((s) => ({
    timestamp: new Date(Date.parse('2026-01-15T10:00:00Z') + s * 1000).toISOString(),
  })) as TimelapseFrame[];
}

describe('frameIndexAtOffset', () => {
  it('maps wall-clock offsets onto per-frame timestamps', () => {
    const frames = framesAt([0, 10, 20, 30]);
    expect(frameIndexAtOffset(frames, rec(), 0)).toBe(0);
    expect(frameIndexAtOffset(frames, rec(), 5)).toBe(1); // nearest frame ≥ offset
    expect(frameIndexAtOffset(frames, rec(), 20)).toBe(2);
    expect(frameIndexAtOffset(frames, rec(), 999)).toBe(3); // clamp to last
    expect(frameIndexAtOffset(frames, rec(), -5)).toBe(0); // negative floors to first
  });

  it('falls back to even spacing over duration when frames carry no timestamps', () => {
    const frames = [{}, {}, {}, {}, {}, {}, {}, {}, {}, {}] as TimelapseFrame[];
    expect(frameIndexAtOffset(frames, rec(), 0)).toBe(0);
    expect(frameIndexAtOffset(frames, rec(), 50)).toBe(5);
    expect(frameIndexAtOffset(frames, rec(), 100)).toBe(9);
    expect(frameIndexAtOffset(frames, rec(), 999)).toBe(9);
  });

  it('returns 0 for empty frame lists, missing recording, or zero duration', () => {
    expect(frameIndexAtOffset([], rec(), 10)).toBe(0);
    expect(frameIndexAtOffset(framesAt([0]), null, 10)).toBe(0);
    expect(frameIndexAtOffset([{}] as TimelapseFrame[], rec({ duration: 0 }), 10)).toBe(0);
  });
});

describe('cyclerOffsetSec', () => {
  it('derives wall-clock seconds from the frame timestamp', () => {
    const frames = framesAt([0, 10, 42]);
    expect(cyclerOffsetSec(frames, rec(), 2)).toBe(42);
    expect(cyclerOffsetSec(frames, rec(), 0)).toBe(0);
  });

  it('estimates from duration when timestamps are absent', () => {
    const frames = [{}, {}, {}, {}] as TimelapseFrame[];
    expect(cyclerOffsetSec(frames, rec(), 2)).toBeCloseTo(50, 5);
  });

  it('returns 0 without a recording or frame', () => {
    expect(cyclerOffsetSec(framesAt([0]), null, 0)).toBe(0);
    expect(cyclerOffsetSec([], rec(), 0)).toBe(0);
  });
});

describe('formatFrameTimestamp', () => {
  it('formats per-frame timestamps as HH:MM:SS from the recording start', () => {
    const frames = framesAt([0, 3661]); // 1h 1m 1s
    expect(formatFrameTimestamp(frames, rec(), 1)).toBe('01:01:01');
    expect(formatFrameTimestamp(frames, rec(), 0)).toBe('00:00:00');
  });

  it('estimates evenly when frames carry no timestamps', () => {
    const frames = [{}, {}, {}, {}] as TimelapseFrame[];
    // index 2 of 4 over 100s ⇒ 50s
    expect(formatFrameTimestamp(frames, rec(), 2)).toBe('00:00:50');
  });

  it('returns empty string without a recording or matching frame', () => {
    expect(formatFrameTimestamp(framesAt([0]), null, 0)).toBe('');
    expect(formatFrameTimestamp([], rec(), 0)).toBe('');
  });
});

describe('timelineDate', () => {
  it('formats the recording start as the host-local YYYY-MM-DD day', () => {
    expect(timelineDate(rec())).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it('falls back to today without a recording', () => {
    expect(timelineDate(null)).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(timelineDate(rec({ started_at: '' }))).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });
});
