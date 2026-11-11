// Surveillance multi-page grid — three independently configured camera
// combinations ("pages") on the big-screen view; the header tab strip flips
// between them. This iteration ships exactly 3 pages (SURVEILLANCE_PAGE_COUNT).
//
// Persistence: one localStorage record under SURVEILLANCE_PAGES_KEY holding
// { pages: string[][], active: number }. When the record is absent (first run
// after this feature ships), page 0 is seeded from the legacy
// 'dashboard-selected-cameras' key so existing users keep their grid;
// page 0 keeps mirroring the legacy key on every save so builds predating
// multi-page (and any other reader of that key) stay valid across a
// downgrade window.

export const SURVEILLANCE_PAGES_KEY = 'dashboard-surveillance-pages';
export const SURVEILLANCE_PAGE_COUNT = 3;

export interface SurveillancePages {
  pages: string[][];
  active: number;
}

export interface LoadedSurveillancePages extends SurveillancePages {
  /** true when a valid SURVEILLANCE_PAGES_KEY record was found on storage */
  saved: boolean;
}

function normalizePages(value: unknown): string[][] | null {
  if (!Array.isArray(value)) return null;
  const pages: string[][] = [];
  for (let i = 0; i < SURVEILLANCE_PAGE_COUNT; i++) {
    const page = value[i];
    pages.push(
      Array.isArray(page)
        ? page.filter((id): id is string => typeof id === 'string')
        : [],
    );
  }
  return pages;
}

function clampActive(value: unknown): number {
  const n =
    typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : 0;
  return Math.min(Math.max(n, 0), SURVEILLANCE_PAGE_COUNT - 1);
}

// Load the saved pages, falling back to a single-page record seeded from the
// legacy selection. Corrupt or malformed records degrade to the migration
// shape instead of throwing — the big-screen grid must always come up.
export function loadSurveillancePages(legacyIds: string[]): LoadedSurveillancePages {
  try {
    const raw = localStorage.getItem(SURVEILLANCE_PAGES_KEY);
    if (raw) {
      const parsed: unknown = JSON.parse(raw);
      if (parsed && typeof parsed === 'object') {
        const record = parsed as { pages?: unknown; active?: unknown };
        const pages = normalizePages(record.pages);
        if (pages) {
          return { pages, active: clampActive(record.active), saved: true };
        }
      }
    }
  } catch (e) {
    console.warn('Failed to load surveillance pages:', e);
  }
  return { pages: [legacyIds, [], []], active: 0, saved: false };
}

export function saveSurveillancePages(pages: SurveillancePages): void {
  localStorage.setItem(SURVEILLANCE_PAGES_KEY, JSON.stringify(pages));
}
