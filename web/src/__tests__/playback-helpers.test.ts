import { describe, it, expect, vi } from 'vitest';
import { segmentVideoUrlSync, clampOffset } from '../lib/playback-helpers';

const urls = {
  segment: (id: string) => `/api/recordings/${id}/stream`,
  merged: (id: string) => `/api/recordings/${id}/merged`,
};

describe('segmentVideoUrlSync', () => {
  it('streams h264 and h265 segments directly', () => {
    expect(segmentVideoUrlSync({ id: 'r1', format: 'h264' }, urls)).toBe('/api/recordings/r1/stream');
    expect(segmentVideoUrlSync({ id: 'r2', format: 'h265' }, urls)).toBe('/api/recordings/r2/stream');
  });

  it('uses the merged file for merged timelapse/mjpeg', () => {
    const rec = { id: 'r3', format: 'timelapse', merge_status: 'merged', merge_path: '/x.mp4' };
    expect(segmentVideoUrlSync(rec, urls)).toBe('/api/recordings/r3/merged');
  });

  it('returns null for unmerged timelapse/mjpeg (JPEG cycler owns it)', () => {
    expect(segmentVideoUrlSync({ id: 'r4', format: 'timelapse' }, urls)).toBeNull();
    expect(segmentVideoUrlSync({ id: 'r5', format: 'mjpeg', merge_status: 'pending' }, urls)).toBeNull();
  });

  it('returns null for unknown formats', () => {
    expect(segmentVideoUrlSync({ id: 'r6', format: 'avi' }, urls)).toBeNull();
  });
});

describe('clampOffset', () => {
  it('clamps into [0, duration]', () => {
    expect(clampOffset(-5, 60)).toBe(0);
    expect(clampOffset(120, 60)).toBe(60);
    expect(clampOffset(30, 60)).toBe(30);
  });

  it('returns 0 for an unknown duration', () => {
    expect(clampOffset(30, 0)).toBe(0);
    expect(clampOffset(30, NaN)).toBe(0);
  });
});
