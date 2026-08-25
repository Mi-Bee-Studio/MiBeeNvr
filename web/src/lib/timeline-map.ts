/**
 * Wall-clock ↔ file-timeline mapping for timelapse-compressed recordings
 * (#496): the rolling merge rewrites sparse dwell samples to a fast cadence
 * and stores the piecewise mapping as [[wallSec, fileSec], ...] breakpoints
 * on the recording row. Uncompressed recordings carry no map — the identity
 * fallbacks below keep them exact.
 */
export type TimelineMap = Array<[number, number]>;

export function parseTimelineMap(json?: string | null): TimelineMap | null {
  if (!json) return null;
  try {
    const v = JSON.parse(json);
    if (!Array.isArray(v) || v.length < 2 || !Array.isArray(v[0])) return null;
    return v as TimelineMap;
  } catch {
    return null;
  }
}

function interpForward(map: TimelineMap, x: number): number {
  if (x <= map[0][0]) return map[0][1];
  for (let i = 1; i < map.length; i++) {
    if (x <= map[i][0]) {
      const [w0, f0] = map[i - 1];
      const [w1, f1] = map[i];
      if (w1 <= w0) return f1;
      return f0 + ((x - w0) / (w1 - w0)) * (f1 - f0);
    }
  }
  return map[map.length - 1][1];
}

function interpInverse(map: TimelineMap, y: number): number {
  if (y <= map[0][1]) return map[0][0];
  for (let i = 1; i < map.length; i++) {
    if (y <= map[i][1]) {
      const [w0, f0] = map[i - 1];
      const [w1, f1] = map[i];
      if (f1 <= f0) return w1;
      return w0 + ((y - f0) / (f1 - f0)) * (w1 - w0);
    }
  }
  return map[map.length - 1][0];
}

/** Wall-clock seconds within the recording → file-timeline seconds. */
export function wallToFileSec(map: TimelineMap | null, wallSec: number): number {
  return map ? interpForward(map, wallSec) : wallSec;
}

/** File-timeline seconds → wall-clock seconds within the recording. */
export function fileToWallSec(map: TimelineMap | null, fileSec: number): number {
  return map ? interpInverse(map, fileSec) : fileSec;
}
