/**
 * Snapshot management utilities for camera thumbnails.
 * Handles fetching, caching, and periodic refresh of camera snapshots.
 */

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
 */
export async function fetchSnapshot({
  cameraId,
  getAuthHeader,
  onUrlUpdate,
  onUrlRevoke,
  onLoadingChange,
  onErrorChange,
  onUnsupported,
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
      return;
    }
    if (!response.ok) {
      onErrorChange(cameraId, true);
      return;
    }

    const blob = await response.blob();
    onUrlRevoke(cameraId);
    onUrlUpdate(cameraId, URL.createObjectURL(blob));
    onErrorChange(cameraId, false);
    onLoadingChange(cameraId, false);
  } catch {
    onErrorChange(cameraId, true);
    onLoadingChange(cameraId, false);
  }
}

/**
 * Create a snapshot manager for a set of cameras.
 * Returns start/stop functions for lifecycle management.
 *
 * @param {object} opts
 * @param {number} [opts.intervalMs=3000] - Refresh interval in milliseconds
 * @param {() => string | null} opts.getAuthHeader - Returns "Bearer <token>" or null
 * @param {(id: string, url: string) => void} opts.onUrlUpdate
 * @param {(id: string) => void} opts.onUrlRevoke
 * @param {(id: string, loading: boolean) => void} opts.onLoadingChange
 * @param {(id: string, error: boolean) => void} opts.onErrorChange
 * @param {(id: string) => void} opts.onUnsupported
 */
export function createSnapshotManager(opts) {
  const { intervalMs = 3000, getAuthHeader, onUrlUpdate, onUrlRevoke, onLoadingChange, onErrorChange } = opts;

  const intervals = {};
  const noSnapshotSet = new Set();

  function startRefresh(cameraId) {
    onLoadingChange(cameraId, true);

    const fetchOpts = {
      cameraId,
      getAuthHeader,
      onUrlUpdate,
      onUrlRevoke,
      onLoadingChange,
      onErrorChange,
      onUnsupported: (id) => {
        noSnapshotSet.add(id);
      },
    };

    fetchSnapshot(fetchOpts);
    intervals[cameraId] = setInterval(() => fetchSnapshot(fetchOpts), intervalMs);
  }

  function stopRefresh(cameraId) {
    if (intervals[cameraId]) {
      clearInterval(intervals[cameraId]);
      delete intervals[cameraId];
    }
    onUrlRevoke(cameraId);
    onLoadingChange(cameraId, false);
    onErrorChange(cameraId, false);
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

  return { startRefresh, stopRefresh, isUnsupported, stopAll };
}
