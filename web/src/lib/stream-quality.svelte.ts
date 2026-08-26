/**
 * Stream quality preference state (main vs sub) — #513.
 *
 * Two independent knobs, because the two live surfaces want opposite
 * defaults:
 *  - Grid (Surveillance): default `sub` when a camera has one — tiles are
 *    small and decoding N full-resolution mains is the grid's dominant cost
 *    (4K H.265 wasm decode is throughput-bound; the sub stream decodes
 *    trivially).
 *  - Single-camera (LiveView): default `main` — the detail view wants full
 *    resolution; a per-camera override persists an explicit user choice.
 *
 * The preference is an INTENT; `effectiveQuality` (stream-selection.ts)
 * resolves it against the camera's sub availability, the active playback
 * mode, and browser codec capabilities — sub requests degrade to main, never
 * fail. Cameras without a sub stream are unaffected entirely.
 */

import { getPreference, setPreference } from '$lib/preferences';
import type { StreamQuality } from '$lib/stream-selection';

const GRID_QUALITY_KEY = 'grid_stream_quality';
const CAMERA_QUALITY_PREFIX = 'camera_quality_';

// Module-level reactive state so the Surveillance toolbar toggle re-renders
// every grid cell without prop drilling through the route.
let gridQuality = $state<StreamQuality>(readStored('sub'));

function readStored(fallback: StreamQuality): StreamQuality {
  const v = getPreference<StreamQuality>(GRID_QUALITY_KEY, fallback);
  return v === 'main' || v === 'sub' ? v : fallback;
}

export function getGridQuality(): StreamQuality {
  return gridQuality;
}

export function setGridQuality(q: StreamQuality): void {
  gridQuality = q;
  setPreference(GRID_QUALITY_KEY, q);
}

export function toggleGridQuality(): void {
  setGridQuality(gridQuality === 'sub' ? 'main' : 'sub');
}

// Per-camera quality (LiveView). Read once when the route mounts; the toggle
// writes both the local component state and this store.
export function getCameraQuality(cameraId: string): StreamQuality {
  const v = getPreference<StreamQuality>(`${CAMERA_QUALITY_PREFIX}${cameraId}`, 'main');
  return v === 'main' || v === 'sub' ? v : 'main';
}

export function setCameraQuality(cameraId: string, q: StreamQuality): void {
  setPreference(`${CAMERA_QUALITY_PREFIX}${cameraId}`, q);
}
