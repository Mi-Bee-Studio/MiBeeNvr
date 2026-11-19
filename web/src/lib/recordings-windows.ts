/**
 * Pure date-window helpers for the Recordings page.
 *
 * Extracted from Recordings.svelte: these are time arithmetic only — no Svelte
 * state, no DOM, no i18n — so they can be unit-tested directly and keep the
 * component script focused on data flow.
 */

/** Inclusive day bounds for a `YYYY-MM-DD` local date, as ISO strings. */
export function remoteDayBounds(date: string): { start: string; end: string } {
  const [y, m, d] = date.split('-').map(Number);
  return {
    start: new Date(y, m - 1, d, 0, 0, 0).toISOString(),
    end: new Date(y, m - 1, d, 23, 59, 59, 999).toISOString(),
  };
}

/**
 * Inclusive lower bound for the `start` query when looking up an existing merge
 * for a preset window. `natural-day` snaps to local midnight; the rest are
 * rolling offsets back from `now`. Unknown durations fall back to 24h.
 */
export function computePresetWindowStart(duration: string, now: Date): Date {
  switch (duration) {
    case 'natural-day': {
      const d = new Date(now);
      d.setHours(0, 0, 0, 0);
      return d;
    }
    case '8h':
      return new Date(now.getTime() - 8 * 3600 * 1000);
    case '24h':
      return new Date(now.getTime() - 24 * 3600 * 1000);
    case '7d':
      return new Date(now.getTime() - 7 * 24 * 3600 * 1000);
    case '30d':
      return new Date(now.getTime() - 30 * 24 * 3600 * 1000);
    default:
      return new Date(now.getTime() - 24 * 3600 * 1000);
  }
}

/** Zero-pads a number to two digits (local helper kept out of the component). */
export function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/**
 * TTL cache for one day's timeline payload. The page returns instantly from
 * cache on a day revisit, then refreshes in the background.
 */
export interface DayCacheEntry<R, E, M> {
  recordings: R;
  events: E;
  merges: M;
  at: number;
}

export const DAY_CACHE_TTL_MS = 30_000;

export function createDayCache<R, E, M>() {
  let entry: { key: string; entry: DayCacheEntry<R, E, M> } | null = null;
  return {
    take(key: string): DayCacheEntry<R, E, M> | null {
      if (entry && entry.key === key && Date.now() - entry.entry.at < DAY_CACHE_TTL_MS) {
        return entry.entry;
      }
      return null;
    },
    put(key: string, recordings: R, events: E, merges: M): void {
      entry = { key, entry: { recordings, events, merges, at: Date.now() } };
    },
    clear(): void {
      entry = null;
    },
  };
}
