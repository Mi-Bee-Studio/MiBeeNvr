import type { Camera } from './api/cameras';

/**
 * Dual-spectrum sibling lookup (#1092 follow-up): a GB28181 device exposing
 * a visible channel and a thermal channel registers as two cameras sharing
 * one gb28181.device_id (the industry-standard dual-channel model — the same
 * device appears as one hardware node with two channels in Milestone/iVMS).
 * Devices that instead register each channel as a SEPARATE SIP device get
 * linked explicitly via a shared non-empty `spectrum_group` (config-level
 * key — the two registrations have distinct device_ids, so nothing can be
 * derived). Returns the other channels of the same physical device, so the
 * live view can offer a one-click "switch to thermal/visible channel" chip
 * and the dual-pane view.
 */
export function gbSiblings(camera: Camera, cameras: Camera[] | null | undefined): Camera[] {
  const deviceId = camera?.gb28181?.device_id;
  const group = camera?.spectrum_group?.trim();
  if ((!deviceId && !group) || !cameras) return [];
  return cameras.filter(
    (c) =>
      c.id !== camera.id &&
      c.protocol === 'gb28181' &&
      ((deviceId && c.gb28181?.device_id === deviceId) ||
        (group && c.spectrum_group?.trim() === group))
  );
}

/** Label key for a sibling switch chip: thermal channel vs the visible one. */
export function spectrumLabelKey(sibling: Camera): 'live.spectrum.thermal' | 'live.spectrum.visible' {
  return sibling.imaging_type === 'thermal' ? 'live.spectrum.thermal' : 'live.spectrum.visible';
}

// ---- Spot-temperature grid (device ?grid=1 via the NVR proxy #1106) ----

/** 32×24 MLX90640 grid per SPEC §3.7: x=col[0..31], y=row[0..23], row-major. */
export const GRID_COLS = 32;
export const GRID_ROWS = 24;

/** Cell index for a normalized (0..1) position on the rendered video. */
export function gridCellAt(nx: number, ny: number): { x: number; y: number } {
  const x = Math.min(GRID_COLS - 1, Math.max(0, Math.floor(nx * GRID_COLS)));
  const y = Math.min(GRID_ROWS - 1, Math.max(0, Math.floor(ny * GRID_ROWS)));
  return { x, y };
}

/** Cell-center normalized position (for rendering markers). */
export function cellCenter(x: number, y: number): { nx: number; ny: number } {
  return { nx: (x + 0.5) / GRID_COLS, ny: (y + 0.5) / GRID_ROWS };
}

/**
 * Unwrap the family envelope ({"ok":true,"data":{…}}) — same shape rule as
 * the Go poller (#1103). Returns the inner object or null.
 */
export function unwrapFamilyEnvelope(body: unknown): Record<string, unknown> | null {
  if (body && typeof body === 'object' && 'data' in body) {
    const env = body as { ok?: boolean; data: unknown };
    if (env.data && typeof env.data === 'object' && env.ok !== false) {
      return env.data as Record<string, unknown>;
    }
    return null;
  }
  return (body && typeof body === 'object' ? body : null) as Record<string, unknown> | null;
}

/** Extract the 768-value row-major grid from a frame payload (either shape). */
export function gridValues(frame: unknown): number[] | null {
  const d = unwrapFamilyEnvelope(frame);
  if (!d) return null;
  const g = d.grid as { values?: unknown } | undefined;
  const vals = g?.values;
  if (!Array.isArray(vals) || vals.length !== GRID_COLS * GRID_ROWS) return null;
  return (vals as unknown[]).map((v) => (typeof v === 'number' ? v : Number(v) || 0));
}

/** Cells at or above threshold, hottest first (capped for overlay clarity). */
export function cellsOverThreshold(
  values: number[],
  threshold: number,
  cap = 8
): Array<{ x: number; y: number; v: number }> {
  const out: Array<{ x: number; y: number; v: number }> = [];
  for (let i = 0; i < values.length; i++) {
    if (values[i] >= threshold) out.push({ x: i % GRID_COLS, y: Math.floor(i / GRID_COLS), v: values[i] });
  }
  return out.sort((a, b) => b.v - a.v).slice(0, cap);
}
