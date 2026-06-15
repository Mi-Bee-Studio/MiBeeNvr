/**
 * Timeline utility functions — pure logic extracted from TimelineBar.svelte
 * for unit testing. No Svelte / DOM dependencies.
 */

export interface TimelineSegment {
  id: string;
  /** Seconds from day start (00:00) */
  startSec: number;
  /** Seconds from day start (00:00) */
  endSec: number;
}

export interface SeekResult {
  /** The segment containing or nearest to the target */
  seg: TimelineSegment | null;
  /** Offset in seconds within the segment */
  offset: number;
  /** True if target fell in a gap and was snapped */
  snapped: boolean;
}

/**
 * Find the segment containing targetSec, or snap to the nearest segment edge.
 *
 * @param segments  Time-ordered list of recording segments (any order accepted)
 * @param targetSec Target wall-clock position in seconds-from-midnight
 * @returns The hit/snapped segment + intra-segment offset + snapped flag
 */
export function findSegmentAt(
  segments: TimelineSegment[],
  targetSec: number,
): SeekResult {
  if (segments.length === 0) return { seg: null, offset: 0, snapped: false };

  // Exact hit?
  for (const seg of segments) {
    if (targetSec >= seg.startSec && targetSec <= seg.endSec) {
      return { seg, offset: targetSec - seg.startSec, snapped: false };
    }
  }

  // Gap — snap to nearest segment
  const sorted = [...segments].sort((a, b) => a.startSec - b.startSec);

  // Prefer snapping forward (to start of next segment after the gap)
  const nextSeg = sorted.find((s) => s.startSec > targetSec);
  if (nextSeg) {
    return { seg: nextSeg, offset: 0, snapped: true };
  }

  // Otherwise snap to end of last segment before the gap
  for (let i = sorted.length - 1; i >= 0; i--) {
    if (sorted[i].endSec <= targetSec) {
      return { seg: sorted[i], offset: sorted[i].endSec - sorted[i].startSec, snapped: true };
    }
  }

  return { seg: null, offset: 0, snapped: false };
}

/**
 * Convert a wall-clock epoch-ms to seconds-from-midnight for a given day start.
 */
export function epochMsToDaySec(epochMs: number, dayStartMs: number): number {
  return (epochMs - dayStartMs) / 1000;
}

/**
 * Parse a YYYY-MM-DD date string to UTC midnight epoch-ms.
 */
export function parseDayStart(dateStr: string): number {
  const [y, m, d] = dateStr.split('-').map(Number);
  return Date.UTC(y, m - 1, d, 0, 0, 0);
}

/**
 * Format a length in seconds as a human-readable string (e.g. "1h05m", "3m20s", "45s").
 */
export function formatLength(sec: number): string {
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  if (h > 0) return `${h}h${String(m).padStart(2, '0')}m`;
  if (m > 0) return `${m}m${String(s).padStart(2, '0')}s`;
  return `${s}s`;
}
