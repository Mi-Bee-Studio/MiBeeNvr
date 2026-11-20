// Pure presentation + lookup helpers for the recordings compact list.
// Extracted from CompactList.svelte so the badge/format contracts are
// unit-testable without mounting the list.
import { t } from '$lib/i18n';
import type { Recording } from '$lib/api';
import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';

export function motionBadgeStyle(score: number): string {
  const hue = Math.max(0, Math.round(120 - Math.min(1, score) * 120));
  return `background-color: hsl(${hue} 65% 42%)`;
}

export function formatLabel(recording: Recording): string {
  switch (recording.format) {
    case 'h264':
      return t('recording.format.h264');
    case 'h265':
      return t('recording.format.h265');
    case 'mjpeg':
      return t('recording.format.mjpeg');
    case 'avi':
      return t('recording.format.avi');
    case 'timelapse':
      return t('recording.format.timelapse');
  }
}

export function formatBadgeClass(recording: Recording): string {
  if (recording.format === 'timelapse') return 'bg-cyan-100 text-cyan-800 dark:bg-cyan-900/50 dark:text-cyan-300';
  if (recording.format === 'avi') return 'bg-purple-100 text-purple-800 dark:bg-purple-900/50 dark:text-purple-300';
  if (recording.format === 'h264' || recording.format === 'h265') return 'badge-info';
  return 'badge-neutral';
}

// AI processing state badge (#671). Only meaningful states render — the
// field is absent for recordings the AI backend never touched.
export function aiBadgeClass(status: string): string {
  switch (status) {
    case 'completed':
      return 'badge-success';
    case 'processing':
      return 'bg-blue-100 text-blue-800 dark:bg-blue-900/50 dark:text-blue-300 animate-pulse';
    case 'failed':
      return 'bg-red-100 text-red-800 dark:bg-red-900/50 dark:text-red-300';
    case 'skipped':
      return 'bg-amber-100 text-amber-800 dark:bg-amber-900/50 dark:text-amber-300';
    default:
      return 'badge-neutral';
  }
}

export function aiBadgeLabel(status: string): string {
  return t(`recordings.ai.${status}`);
}

// --- Transcoding lookups (over the manager's recent-results window) ---

export function activeTranscodeTask(status: ManagerStatus | null, recordingId: string): TranscodeTask | undefined {
  if (!status?.recent_results) return undefined;
  return status.recent_results.find(
    (t) => t.recording_id === recordingId && (t.status === 'running' || t.status === 'pending'),
  );
}

export function failedTranscodeTask(status: ManagerStatus | null, recordingId: string): TranscodeTask | undefined {
  if (!status?.recent_results) return undefined;
  return status.recent_results.find((t) => t.recording_id === recordingId && t.status === 'failed' && t.error);
}

export function completedTranscodeTask(status: ManagerStatus | null, recordingId: string): TranscodeTask | undefined {
  if (!status?.recent_results) return undefined;
  return status.recent_results.find((t) => t.recording_id === recordingId && t.status === 'completed');
}
