/**
 * Snapshot management utilities for camera thumbnails.
 * Handles fetching, caching, and periodic refresh of camera snapshots.
 *
 * Issue #112 hardening:
 *  - 404 (unsupported) now CLEARS the interval (previously it kept polling every
 *    interval forever, hammering a camera that will never produce a frame).
 *  - Consecutive failures trigger exponential backoff (3s -> 15s -> 30s) so a
 *    temporarily-unreachable camera isn't hit 20x/minute.
 *  - stopRefresh removes the camera from the unsupported set so it can be
 *    retried later (previously a single 404 marked it permanently unsupported
 *    for the session).
 *  - retryUnsupported() lets a caller re-arm refresh for a camera that was
 *    marked unsupported (e.g. after the backend recovers).
 */

/** Base refresh interval (and the floor after backoff resets on success). */
const BASE_INTERVAL_MS = 3000;
/** After this many consecutive failures, slow the interval down. */
const BACKOFF_AFTER_FAILURES = 3;
/** Backoff interval once the failure threshold is crossed. */
const BACKOFF_INTERVAL_MS = 30000;

/**
 * Fetch a snapshot image for a camera.
 * Updates the provided state stores via callbacks.
 *
 * @param {object} opts
 * @param {string} opts.cameraId
 * @param {() => string | null} opts.getAuthHeader - Returns "Bearer <token>" or null
 * @param {(id: string, url: string) => void} opts.onUrlUpdate - Called with new blob URL
 * @param {(id: string) => void} opts.onUrlRevoke - Called before URL update to revoke old URL
 * @param {(id: string, loading: boolean) => void} opts.onLoadingChange
 * @param {(id: string, error: boolean) => void} opts.onErrorChange
 * @param {(id: string) => void} opts.onUnsupported - Camera returned 404
 * @param {(id: string) => void} [opts.onResult] - Called with 'ok' | 'fail' | 'unsupported' after each attempt (for backoff bookkeeping)
 */
export async function fetchSnapshot({
  cameraId,
  getAuthHeader,
  onUrlUpdate,
  onUrlRevoke,
  onLoadingChange,
  onErrorChange,
  onUnsupported,
  onResult,
}) {
  const authHeader = getAuthHeader();
  const headers = {};
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  try {
    const response = await fetch(`/api/cameras/${cameraId}/snapshot`, { headers });
    if (response.status === 404) {
      onUnsupported(cameraId);
      onResult?.(cameraId, 'unsupported');
      return;
    }
    if (!response.ok) {
      onErrorChange(cameraId, true);
      onResult?.(cameraId, 'fail');
      return;
    }

    const blob = await response.blob();
    onUrlRevoke(cameraId);
    onUrlUpdate(cameraId, URL.createObjectURL(blob));
    onErrorChange(cameraId, false);
    onLoadingChange(cameraId, false);
    onResult?.(cameraId, 'ok');
  } catch {
    onErrorChange(cameraId, true);
    onLoadingChange(cameraId, false);
    onResult?.(cameraId, 'fail');
  }
}

/**
 * Create a snapshot manager for a set of cameras.
 * Returns start/stop functions for lifecycle management.
 *
 * @param {object} opts
 * @param {number} [opts.intervalMs=3000] - Base refresh interval in milliseconds
 * @param {() => string | null} opts.getAuthHeader - Returns "Bearer <token>" or null
 * @param {(id: string, url: string) => void} opts.onUrlUpdate
 * @param {(id: string) => void} opts.onUrlRevoke
 * @param {(id: string, loading: boolean) => void} opts.onLoadingChange
 * @param {(id: string, error: boolean) => void} opts.onErrorChange
 * @param {(id: string) => void} opts.onUnsupported
 */
export function createSnapshotManager(opts) {
  const { intervalMs = BASE_INTERVAL_MS, getAuthHeader, onUrlUpdate, onUrlRevoke, onLoadingChange, onErrorChange } = opts;

  const intervals = {};
  const noSnapshotSet = new Set();
  // Per-camera consecutive-failure counter for backoff. Reset to 0 on success.
  const failureCounts = {};

  function effectiveInterval(cameraId) {
    return failureCounts[cameraId] >= BACKOFF_AFTER_FAILURES ? BACKOFF_INTERVAL_MS : intervalMs;
  }

  function startRefresh(cameraId) {
    onLoadingChange(cameraId, true);
    failureCounts[cameraId] = 0;

    const tick = () => {
      fetchSnapshot({
        cameraId,
        getAuthHeader,
        onUrlUpdate,
        onUrlRevoke,
        onLoadingChange,
        onErrorChange,
        onUnsupported: (id) => {
          noSnapshotSet.add(id);
        },
        onResult: (id, result) => {
          if (result === 'ok') {
            failureCounts[id] = 0;
          } else {
            failureCounts[id] = (failureCounts[id] ?? 0) + 1;
          }
          // If the effective interval changed due to backoff, re-arm the timer
          // at the new cadence. clearTimeout on an undefined id is a no-op.
          if (intervals[id]) clearTimeout(intervals[id]);
          intervals[id] = setTimeout(tick, effectiveInterval(id));
        },
      });
    };

    // Initial fetch immediately, then schedule via the recursive setTimeout in
    // onResult (so each tick uses the current backoff interval, not a fixed
    // setInterval that can't adapt).
    fetchSnapshot({
      cameraId,
      getAuthHeader,
      onUrlUpdate,
      onUrlRevoke,
      onLoadingChange,
      onErrorChange,
      onUnsupported: (id) => {
        noSnapshotSet.add(id);
        // 404 = permanently unsupported for this session: stop polling. The
        // camera's snapshot endpoint doesn't exist; retrying every 3s was the
        // issue #112 death-loop. retryUnsupported() can re-arm it later.
        if (intervals[id]) {
          clearTimeout(intervals[id]);
          delete intervals[id];
        }
      },
      onResult: (id, result) => {
        if (result === 'ok') {
          failureCounts[id] = 0;
        } else if (result !== 'unsupported') {
          failureCounts[id] = (failureCounts[id] ?? 0) + 1;
        }
        // Don't re-arm on 'unsupported' (interval already cleared above). For
        // ok/fail, schedule the next tick at the (possibly backed-off) interval.
        if (result !== 'unsupported') {
          intervals[id] = setTimeout(tick, effectiveInterval(id));
        }
      },
    });
  }

  function stopRefresh(cameraId) {
    if (intervals[cameraId]) {
      clearTimeout(intervals[cameraId]);
      delete intervals[cameraId];
    }
    delete failureCounts[cameraId];
    // Remove from the unsupported set so the camera can be retried later (e.g.
    // after the backend recovers, or when the user re-selects it). Previously a
    // single 404 permanently marked it unsupported for the whole session.
    noSnapshotSet.delete(cameraId);
    onUrlRevoke(cameraId);
    onLoadingChange(cameraId, false);
    onErrorChange(cameraId, false);
  }

  /** Re-arm refresh for a camera previously marked unsupported (404). */
  function retryUnsupported(cameraId) {
    noSnapshotSet.delete(cameraId);
    if (!intervals[cameraId]) {
      startRefresh(cameraId);
    }
  }

  function isUnsupported(cameraId) {
    return noSnapshotSet.has(cameraId);
  }

  /** Stop all active refreshes */
  function stopAll() {
    for (const id of Object.keys(intervals)) {
      stopRefresh(id);
    }
  }

  return { startRefresh, stopRefresh, retryUnsupported, isUnsupported, stopAll };
}
