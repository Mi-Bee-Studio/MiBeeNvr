import { describe, it, expect, vi } from 'vitest';

// t() echoes the key — assertions match on key fragments.
vi.mock('$lib/i18n', () => ({
  t: (k: string) => k,
}));
import {
  motionBadgeStyle,
  formatBadgeClass,
  aiBadgeClass,
  aiBadgeLabel,
  activeTranscodeTask,
  failedTranscodeTask,
  completedTranscodeTask,
} from '$lib/library-list-helpers';
import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';

describe('motionBadgeStyle', () => {
  it('maps 0 → green and 1 → red, clamped', () => {
    expect(motionBadgeStyle(0)).toContain('hsl(120');
    expect(motionBadgeStyle(1)).toContain('hsl(0');
    expect(motionBadgeStyle(5)).toContain('hsl(0'); // clamp above 1
    // The hue (not the score) is clamped: a negative input lands at 240 (the
    // raw 120 - (-1)*120). The badge never renders for negative scores — the
    // template guards motion_score >= 0 — so this is inert contract-pinning.
    expect(motionBadgeStyle(-1)).toContain('hsl(240');
  });
});

describe('formatBadgeClass', () => {
  it('splits formats into color families', () => {
    const rec = (format: string) => ({ format }) as never;
    expect(formatBadgeClass(rec('timelapse'))).toContain('cyan');
    expect(formatBadgeClass(rec('avi'))).toContain('purple');
    expect(formatBadgeClass(rec('h264'))).toBe('badge-info');
    expect(formatBadgeClass(rec('h265'))).toBe('badge-info');
    expect(formatBadgeClass(rec('mjpeg'))).toBe('badge-neutral');
  });
});

describe('aiBadgeClass', () => {
  it('maps meaningful states, neutral default', () => {
    expect(aiBadgeClass('completed')).toBe('badge-success');
    expect(aiBadgeClass('processing')).toContain('blue');
    expect(aiBadgeClass('failed')).toContain('red');
    expect(aiBadgeClass('skipped')).toContain('amber');
    expect(aiBadgeClass('pending')).toBe('badge-neutral');
  });

  it('labels via i18n keys', () => {
    expect(aiBadgeLabel('completed')).toBe('recordings.ai.completed');
  });
});

describe('transcode task lookups', () => {
  const task = (over: Partial<TranscodeTask>): TranscodeTask =>
    ({ id: 1, recording_id: 'r1', camera_id: 'c1', status: 'running', ...over }) as TranscodeTask;
  const status = (results: TranscodeTask[]): ManagerStatus =>
    ({ enabled: true, recent_results: results }) as ManagerStatus;

  it('finds the active (running/pending) task', () => {
    const s = status([task({ id: 1, status: 'running' }), task({ id: 2, status: 'completed' })]);
    expect(activeTranscodeTask(s, 'r1')?.id).toBe(1);
  });

  it('finds failed tasks only when they carry an error message', () => {
    const s = status([task({ id: 3, status: 'failed', error: 'boom' }), task({ id: 4, status: 'failed' })]);
    expect(failedTranscodeTask(s, 'r1')?.id).toBe(3);
  });

  it('finds completed tasks', () => {
    const s = status([task({ id: 5, status: 'completed' })]);
    expect(completedTranscodeTask(s, 'r1')?.id).toBe(5);
  });

  it('returns undefined without manager status or results', () => {
    expect(activeTranscodeTask(null, 'r1')).toBeUndefined();
    expect(activeTranscodeTask({} as ManagerStatus, 'r1')).toBeUndefined();
  });
});
