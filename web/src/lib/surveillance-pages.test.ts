import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  SURVEILLANCE_PAGE_COUNT,
  SURVEILLANCE_PAGES_KEY,
  loadSurveillancePages,
  saveSurveillancePages,
  type SurveillancePages,
} from './surveillance-pages';

describe('surveillance-pages persistence', () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => localStorage.clear());

  it('absent record migrates the legacy selection into page 0', () => {
    const p = loadSurveillancePages(['cam-a', 'cam-b']);
    expect(p.saved).toBe(false);
    expect(p.pages).toHaveLength(SURVEILLANCE_PAGE_COUNT);
    expect(p.pages[0]).toEqual(['cam-a', 'cam-b']);
    expect(p.pages[1]).toEqual([]);
    expect(p.pages[2]).toEqual([]);
    expect(p.active).toBe(0);
  });

  it('round-trips a saved record and ignores the legacy ids', () => {
    const model: SurveillancePages = { pages: [['a'], ['b', 'c'], []], active: 2 };
    saveSurveillancePages(model);
    const p = loadSurveillancePages(['legacy-stale']);
    expect(p.saved).toBe(true);
    expect(p.pages).toEqual([['a'], ['b', 'c'], []]);
    expect(p.active).toBe(2);
  });

  it('normalizes a short pages array up to 3 pages', () => {
    localStorage.setItem(SURVEILLANCE_PAGES_KEY, JSON.stringify({ pages: [['x']], active: 0 }));
    const p = loadSurveillancePages([]);
    expect(p.saved).toBe(true);
    expect(p.pages).toHaveLength(3);
    expect(p.pages[0]).toEqual(['x']);
    expect(p.pages[1]).toEqual([]);
    expect(p.pages[2]).toEqual([]);
  });

  it('drops non-string ids and non-array pages', () => {
    localStorage.setItem(
      SURVEILLANCE_PAGES_KEY,
      JSON.stringify({ pages: [['a', 5, null, 'b'], 'nope', 42], active: 0 }),
    );
    const p = loadSurveillancePages([]);
    expect(p.pages[0]).toEqual(['a', 'b']);
    expect(p.pages[1]).toEqual([]);
    expect(p.pages[2]).toEqual([]);
  });

  it('rejects a record whose pages field is not an array', () => {
    localStorage.setItem(SURVEILLANCE_PAGES_KEY, JSON.stringify({ pages: 'nope', active: 1 }));
    const p = loadSurveillancePages(['cam-z']);
    expect(p.saved).toBe(false);
    expect(p.pages[0]).toEqual(['cam-z']);
    expect(p.active).toBe(0);
  });

  it('clamps an out-of-range or non-numeric active index', () => {
    const save = (active: unknown) =>
      localStorage.setItem(
        SURVEILLANCE_PAGES_KEY,
        JSON.stringify({ pages: [['a'], ['b'], ['c']], active }),
      );
    save(9);
    expect(loadSurveillancePages([]).active).toBe(2);
    save(-3);
    expect(loadSurveillancePages([]).active).toBe(0);
    save('1');
    expect(loadSurveillancePages([]).active).toBe(0);
    save(Number.NaN);
    expect(loadSurveillancePages([]).active).toBe(0);
  });

  it('falls back to the migration shape on corrupt JSON', () => {
    localStorage.setItem(SURVEILLANCE_PAGES_KEY, '{not json');
    const p = loadSurveillancePages(['cam-z']);
    expect(p.saved).toBe(false);
    expect(p.pages[0]).toEqual(['cam-z']);
  });
});
