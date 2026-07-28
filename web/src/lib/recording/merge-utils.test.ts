import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
  saveMergeState,
  clearMergeState,
  getMergeStateForCamera,
  computeMergeEta,
  MERGE_STORAGE_KEY,
} from './merge-utils';

describe('merge-utils sessionStorage', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it('saveMergeState persists data keyed by cameraId', () => {
    saveMergeState({ cameraId: 'cam-1', recordingId: 'rec-1', progress: 42, status: 'pending' });
    const stored = getMergeStateForCamera('cam-1');
    expect(stored).not.toBeNull();
    expect(stored!.progress).toBe(42);
    expect(stored!.status).toBe('pending');
    expect(stored!.recordingId).toBe('rec-1');
  });

  it('saveMergeState preserves other cameras', () => {
    saveMergeState({ cameraId: 'cam-1', recordingId: 'r1', progress: 10, status: 'pending' });
    saveMergeState({ cameraId: 'cam-2', recordingId: 'r2', progress: 20, status: 'pending' });
    expect(getMergeStateForCamera('cam-1')?.progress).toBe(10);
    expect(getMergeStateForCamera('cam-2')?.progress).toBe(20);
  });

  it('clearMergeState removes only the specified camera', () => {
    saveMergeState({ cameraId: 'cam-1', recordingId: 'r1', progress: 10, status: 'pending' });
    saveMergeState({ cameraId: 'cam-2', recordingId: 'r2', progress: 20, status: 'pending' });
    clearMergeState('cam-1');
    expect(getMergeStateForCamera('cam-1')).toBeNull();
    expect(getMergeStateForCamera('cam-2')?.progress).toBe(20);
  });

  it('clearMergeState removes the storage key entirely when empty', () => {
    saveMergeState({ cameraId: 'cam-1', recordingId: 'r1', progress: 10, status: 'pending' });
    clearMergeState('cam-1');
    expect(sessionStorage.getItem(MERGE_STORAGE_KEY)).toBeNull();
  });

  it('getMergeStateForCamera returns null for unknown camera', () => {
    expect(getMergeStateForCamera('nope')).toBeNull();
  });

  it('handles corrupted sessionStorage gracefully', () => {
    sessionStorage.setItem(MERGE_STORAGE_KEY, '{invalid json');
    expect(getMergeStateForCamera('cam-1')).toBeNull();
    // Should not throw on save/clear either
    expect(() => saveMergeState({ cameraId: 'x', recordingId: 'y', progress: 1, status: 's' })).not.toThrow();
    expect(() => clearMergeState('x')).not.toThrow();
  });
});

describe('computeMergeEta', () => {
  it('returns empty string when startTime is 0', () => {
    expect(computeMergeEta(0, 50, 1000000)).toBe('');
  });

  it('returns empty string when progress is 0 or negative', () => {
    expect(computeMergeEta(1000, 0, 2000)).toBe('');
    expect(computeMergeEta(1000, -5, 2000)).toBe('');
  });

  it('returns empty string when elapsed < 1 second', () => {
    expect(computeMergeEta(1000, 50, 1500)).toBe('');
  });

  it('returns "< 1min" when remaining is under 1 minute', () => {
    // 50% progress after 10s → total ~20s → remaining ~10s < 60s
    expect(computeMergeEta(1000, 50, 11000)).toBe('< 1min');
  });

  it('returns "~Xm Ys" format for longer remaining times', () => {
    // 25% progress after 30s → total ~120s → remaining ~90s → but that's <1min
    // Let's use: 10% progress after 60s → total ~600s → remaining ~540s = 9min
    const result = computeMergeEta(1000, 10, 61000);
    expect(result).toMatch(/^~\d+m \d+s$/);
    // 540s = 9m 0s
    expect(result).toBe('~9m 0s');
  });

  it('computes correctly at high progress', () => {
    // 90% progress after 90s → total ~100s → remaining ~10s < 60s
    expect(computeMergeEta(1000, 90, 91000)).toBe('< 1min');
  });
});
