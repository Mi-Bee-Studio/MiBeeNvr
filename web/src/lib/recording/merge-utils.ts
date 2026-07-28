/**
 * Pure utility functions for timelapse merge state management.
 *
 * Extracted from RecordingDetail.svelte (#136) to enable unit testing and
 * reuse. These functions have NO Svelte rune dependencies — they operate on
 * plain sessionStorage and return plain values. The component wires the return
 * values into its $state variables.
 */

/** sessionStorage key for cross-page merge-in-progress tracking. */
export const MERGE_STORAGE_KEY = 'mibee_nvr_merge_active';

export interface MergeStateData {
  cameraId: string;
  recordingId: string;
  progress: number;
  status: string;
}

export interface StoredMergeState {
  progress: number;
  status: string;
  recordingId: string;
}

/** Persist merge progress to sessionStorage so it survives navigation away. */
export function saveMergeState(data: MergeStateData): void {
  try {
    const all = JSON.parse(sessionStorage.getItem(MERGE_STORAGE_KEY) || '{}');
    all[data.cameraId] = data;
    sessionStorage.setItem(MERGE_STORAGE_KEY, JSON.stringify(all));
  } catch {
    // sessionStorage may be unavailable (private mode, SSR) — silently skip
  }
}

/** Remove a camera's merge state from sessionStorage. */
export function clearMergeState(cameraId: string): void {
  try {
    const all = JSON.parse(sessionStorage.getItem(MERGE_STORAGE_KEY) || '{}');
    delete all[cameraId];
    if (Object.keys(all).length === 0) {
      sessionStorage.removeItem(MERGE_STORAGE_KEY);
    } else {
      sessionStorage.setItem(MERGE_STORAGE_KEY, JSON.stringify(all));
    }
  } catch {
    // silently skip
  }
}

/** Read a camera's persisted merge state, or null if not found. */
export function getMergeStateForCamera(cameraId: string): StoredMergeState | null {
  try {
    const all = JSON.parse(sessionStorage.getItem(MERGE_STORAGE_KEY) || '{}');
    return all[cameraId] || null;
  } catch {
    return null;
  }
}

/**
 * Estimate remaining time for an in-progress merge based on elapsed time and
 * current progress percentage.
 *
 * @param startTime - epoch ms when the merge started (0 = not started)
 * @param progressPct - current progress 0-100
 * @param now - epoch ms (injectable for testing; defaults to Date.now())
 * @returns human-readable ETA string ('< 1min', '~Xm Ys') or '' if not enough data
 */
export function computeMergeEta(startTime: number, progressPct: number, now: number = Date.now()): string {
  if (!startTime || progressPct <= 0) {
    return '';
  }
  const elapsed = now - startTime;
  if (elapsed < 1000) {
    return '';
  }
  const totalEstimate = (elapsed / progressPct) * 100;
  const remaining = totalEstimate - elapsed;
  if (remaining < 60000) {
    return '< 1min';
  }
  const mins = Math.floor(remaining / 60000);
  const secs = Math.floor((remaining % 60000) / 1000);
  return `~${mins}m ${secs}s`;
}
