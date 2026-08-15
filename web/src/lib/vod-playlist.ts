/**
 * VOD playlist mapping utilities (#321 Phase 2).
 *
 * The day playlist stitches every recording into one media timeline; these
 * helpers translate between MEDIA time (0..totalDur on the <video> element,
 * owned by hls.js/MSE) and the per-recording view the UI works in
 * (recording id + within-recording offset + wall clock).
 */

export interface VodEntry {
  /** Recording ID (from the period's EXT-X-MAP URI). */
  rid: string;
  /** Media-time offset where this recording's period starts. */
  mediaStart: number;
  /** Total content duration of the recording (sum of its EXTINF values). */
  dur: number;
  /** Wall-clock start (epoch ms, from #EXT-X-PROGRAM-DATE-TIME; 0 if absent). */
  wallStart: number;
}

/**
 * Parses a VOD playlist into per-recording entries: each EXT-X-MAP starts a
 * period; its EXTINF durations accumulate; an optional
 * #EXT-X-PROGRAM-DATE-TIME line before the map anchors the period in wall
 * clock.
 */
export function parseVodPlaylist(text: string): VodEntry[] {
  const entries: VodEntry[] = [];
  let current: VodEntry | null = null;
  let pendingWall = 0; // PDT precedes its period's EXT-X-MAP — hold it until the map arrives
  for (const line of text.split('\n')) {
    const pdt = line.match(/^#EXT-X-PROGRAM-DATE-TIME:(.+)$/);
    if (pdt) {
      pendingWall = Date.parse(pdt[1]) || 0;
      continue;
    }
    const mapMatch = line.match(/^#EXT-X-MAP:URI="\/api\/cameras\/[^/]+\/playback\/([^/]+)\/init\.mp4"/);
    if (mapMatch) {
      current = { rid: mapMatch[1], mediaStart: 0, dur: 0, wallStart: pendingWall };
      pendingWall = 0;
      entries.push(current);
      continue;
    }
    const infMatch = line.match(/^#EXTINF:([\d.]+),/);
    if (infMatch && current) {
      current.dur += parseFloat(infMatch[1]);
    }
  }
  let cum = 0;
  for (const e of entries) {
    e.mediaStart = cum;
    cum += e.dur;
  }
  return entries;
}

/** Entry containing the given media time (last entry past the end). */
export function entryAt(entries: VodEntry[], mediaTime: number): VodEntry | null {
  for (const e of entries) {
    if (mediaTime >= e.mediaStart && mediaTime < e.mediaStart + e.dur) return e;
  }
  return entries.length > 0 ? entries[entries.length - 1] : null;
}

/**
 * Entry whose wall-clock start is nearest to `epochMs`. Used when rebuilding
 * the session after rolling merges replaced the currently-playing recording:
 * its ID is gone from the fresh playlist, but its wall-clock position maps to
 * the successor segment covering (or nearest to) the same moment.
 */
export function nearestEntryByWallClock(entries: VodEntry[], epochMs: number): VodEntry | null {
  let best: VodEntry | null = null;
  let bestDiff = Infinity;
  for (const e of entries) {
    if (!e.wallStart) continue;
    const diff = Math.abs(e.wallStart - epochMs);
    if (diff < bestDiff) {
      bestDiff = diff;
      best = e;
    }
  }
  return best ?? (entries.length > 0 ? entries[0] : null);
}

/** Media time for (recording, offset); null when the rid is not in the map. */
export function mediaTimeFor(entries: VodEntry[], rid: string, offsetSec: number): number | null {
  const e = entries.find((x) => x.rid === rid);
  if (!e) return null;
  return e.mediaStart + Math.max(0, Math.min(offsetSec, e.dur));
}
