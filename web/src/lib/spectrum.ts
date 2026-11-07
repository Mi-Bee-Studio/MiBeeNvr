import type { Camera } from './api/cameras';

/**
 * Dual-spectrum sibling lookup (#1092 follow-up): a GB28181 device exposing
 * a visible channel and a thermal channel registers as two cameras sharing
 * one gb28181.device_id (the industry-standard dual-channel model — the same
 * device appears as one hardware node with two channels in Milestone/iVMS).
 * Devices that instead register each channel as a SEPARATE SIP device get
 * linked explicitly via a shared non-empty `spectrum_group` (config-level
 * key — the two registrations have distinct device_ids, so nothing can be
 * derived). The explicit key is protocol-agnostic: a dual-spectrum ONVIF
 * camera (one endpoint, a visible and a thermal media profile, added as two
 * cameras) pairs the same way. Returns the other channels of the same
 * physical device, so the live view can offer a one-click "switch to
 * thermal/visible channel" chip and the dual-pane view.
 */
export function gbSiblings(camera: Camera, cameras: Camera[] | null | undefined): Camera[] {
  const deviceId = camera?.protocol === 'gb28181' ? camera?.gb28181?.device_id : undefined;
  const group = camera?.spectrum_group?.trim();
  if ((!deviceId && !group) || !cameras) return [];
  return cameras.filter(
    (c) =>
      c.id !== camera.id &&
      ((deviceId && c.protocol === 'gb28181' && c.gb28181?.device_id === deviceId) ||
        (group && c.spectrum_group?.trim() === group))
  );
}

/** Label key for a sibling switch chip: thermal channel vs the visible one. */
export function spectrumLabelKey(sibling: Camera): 'live.spectrum.thermal' | 'live.spectrum.visible' {
  return sibling.imaging_type === 'thermal' ? 'live.spectrum.thermal' : 'live.spectrum.visible';
}

// ---- Spot-temperature grid (device ?grid=1 via the NVR proxy #1106) ----

/** 32×24 MLX90640 grid per SPEC §3.7: x=col[0..31], y=row[0..23], row-major.
 *  The default geometry when a frame does not announce its own — vendor
 *  radiometric frames do (a Hikvision 160×120 sensor sends cols/rows). */
export const GRID_COLS = 32;
export const GRID_ROWS = 24;

/** Cell index for a normalized (0..1) position on the rendered video. */
export function gridCellAt(
  nx: number,
  ny: number,
  cols = GRID_COLS,
  rows = GRID_ROWS
): { x: number; y: number } {
  const x = Math.min(cols - 1, Math.max(0, Math.floor(nx * cols)));
  const y = Math.min(rows - 1, Math.max(0, Math.floor(ny * rows)));
  return { x, y };
}

/** Cell-center normalized position (for rendering markers). */
export function cellCenter(x: number, y: number, cols = GRID_COLS, rows = GRID_ROWS): { nx: number; ny: number } {
  return { nx: (x + 0.5) / cols, ny: (y + 0.5) / rows };
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

/** Normalized rectangle (0..1) — where the thermal FOV sits inside the
 *  visible picture, when the device reports its registration. */
export type NormRect = { x: number; y: number; w: number; h: number };

/** A decoded temperature frame: row-major values plus its geometry, and the
 *  device's thermal→visible registration when it reports one. */
export type GridFrame = { values: number[]; cols: number; rows: number; visibleRect?: NormRect };

/**
 * Extract the row-major grid from a frame payload (either envelope shape).
 * Geometry comes from grid.cols/grid.rows when present (vendor frames, e.g.
 * 160×120), else the SPEC's 32×24; the value count must match it.
 */
export function gridFrame(frame: unknown): GridFrame | null {
  const d = unwrapFamilyEnvelope(frame);
  if (!d) return null;
  const g = d.grid as { values?: unknown; cols?: unknown; rows?: unknown } | undefined;
  const vals = g?.values;
  const cols = typeof g?.cols === 'number' && g.cols > 0 ? Math.floor(g.cols) : GRID_COLS;
  const rows = typeof g?.rows === 'number' && g.rows > 0 ? Math.floor(g.rows) : GRID_ROWS;
  if (!Array.isArray(vals) || vals.length !== cols * rows) return null;
  const out: GridFrame = {
    values: (vals as unknown[]).map((v) => (typeof v === 'number' ? v : Number(v) || 0)),
    cols,
    rows,
  };
  const r = d.visible_rect as Partial<NormRect> | undefined;
  if (r && [r.x, r.y, r.w, r.h].every((n) => typeof n === 'number' && Number.isFinite(n)) && r.w! > 0 && r.h! > 0) {
    out.visibleRect = { x: r.x!, y: r.y!, w: r.w!, h: r.h! };
  }
  return out;
}

/** Extract just the row-major values (kept for callers that only need them). */
export function gridValues(frame: unknown): number[] | null {
  return gridFrame(frame)?.values ?? null;
}

/** Map a normalized thermal position into the visible picture through the
 *  device-reported registration; identity when there is none. */
export function thermalToVisible(nx: number, ny: number, rect?: NormRect): { nx: number; ny: number } {
  if (!rect) return { nx, ny };
  return { nx: rect.x + nx * rect.w, ny: rect.y + ny * rect.h };
}

/** Cells at or above threshold, hottest first (capped for overlay clarity). */
export function cellsOverThreshold(
  values: number[],
  threshold: number,
  cap = 8,
  cols = GRID_COLS
): Array<{ x: number; y: number; v: number }> {
  const out: Array<{ x: number; y: number; v: number }> = [];
  for (let i = 0; i < values.length; i++) {
    if (values[i] >= threshold) out.push({ x: i % cols, y: Math.floor(i / cols), v: values[i] });
  }
  return out.sort((a, b) => b.v - a.v).slice(0, cap);
}
