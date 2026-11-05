import type { Camera } from './api/cameras';

/**
 * Dual-spectrum sibling lookup (#1092 follow-up): a GB28181 device exposing
 * a visible channel and a thermal channel registers as two cameras sharing
 * one gb28181.device_id (the industry-standard dual-channel model — the same
 * device appears as one hardware node with two channels in Milestone/iVMS).
 * Returns the other channels of the same physical device, so the live view
 * can offer a one-click "switch to thermal/visible channel" chip.
 */
export function gbSiblings(camera: Camera, cameras: Camera[] | null | undefined): Camera[] {
  const deviceId = camera?.gb28181?.device_id;
  if (!deviceId || !cameras) return [];
  return cameras.filter(
    (c) =>
      c.id !== camera.id &&
      c.protocol === 'gb28181' &&
      c.gb28181?.device_id === deviceId
  );
}

/** Label key for a sibling switch chip: thermal channel vs the visible one. */
export function spectrumLabelKey(sibling: Camera): 'live.spectrum.thermal' | 'live.spectrum.visible' {
  return sibling.imaging_type === 'thermal' ? 'live.spectrum.thermal' : 'live.spectrum.visible';
}
