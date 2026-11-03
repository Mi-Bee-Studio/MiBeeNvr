import { describe, it, expect } from 'vitest';
import {
  WALL_TEMPLATES,
  wallGeometry,
  getWallTemplate,
  tileQualityFor,
  wallRowEstimate,
  fillSelection,
  CLASSIC_GRID_SIZE,
} from '$lib/wall-layouts';

// Counts a wall can realistically show (capacity options are 4/6/9/12/16 and
// the wall needs >4; sweep everything in between too since the selection can
// be any size up to the cap).
const COUNTS = [5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16];

describe('wall layout templates', () => {
  it('exposes exactly one template per known id, hero first (default)', () => {
    const ids = WALL_TEMPLATES.map((t) => t.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids[0]).toBe('hero');
  });

  it('keeps every placement inside the column budget and non-overlapping', () => {
    for (const tpl of WALL_TEMPLATES) {
      for (const count of COUNTS) {
        const g = wallGeometry(tpl, count);
        expect(g.featured).toBe(tpl.featured);
        expect(g.placements).toHaveLength(tpl.featured);
        expect(g.cols).toBeGreaterThanOrEqual(2);
        for (const p of g.placements) {
          expect(p.col).toBeGreaterThanOrEqual(1);
          expect(p.col + 1).toBeLessThanOrEqual(g.cols); // 2-wide span fits
          expect(p.row).toBeGreaterThanOrEqual(1);
        }
        for (let i = 0; i < g.placements.length; i++) {
          for (let j = i + 1; j < g.placements.length; j++) {
            const a = g.placements[i];
            const b = g.placements[j];
            const overlaps = a.col < b.col + 2 && b.col < a.col + 2 && a.row < b.row + 2 && b.row < a.row + 2;
            expect(overlaps, `${tpl.id}@${count}: featured ${i} and ${j} overlap`).toBe(false);
          }
        }
      }
    }
  });

  it('hero anchors top-left at every count', () => {
    for (const count of COUNTS) {
      expect(wallGeometry(getWallTemplate('hero'), count).placements[0]).toEqual({ col: 1, row: 1 });
    }
  });

  it('hero corners mirror across the column count', () => {
    for (const count of COUNTS) {
      const cols = wallGeometry(getWallTemplate('hero'), count).cols;
      expect(wallGeometry(getWallTemplate('hero-tr'), count).placements[0]).toEqual({ col: cols - 1, row: 1 });
      expect(wallGeometry(getWallTemplate('hero-bl'), count).placements[0]).toEqual({ col: 1, row: 2 });
      expect(wallGeometry(getWallTemplate('hero-br'), count).placements[0]).toEqual({ col: cols - 1, row: 2 });
    }
  });

  it('center@9 forms the exact 4-column ring (8 smalls around a centered 2×2)', () => {
    const g = wallGeometry(getWallTemplate('center'), 9);
    expect(g.cols).toBe(4);
    expect(g.placements[0]).toEqual({ col: 2, row: 2 });
    // 8 smalls fill row 1 plus both side columns — a gapless 3-row wall.
  });

  it('dual and triple fill the top with side-by-side 2×2 tiles', () => {
    expect(wallGeometry(getWallTemplate('dual'), 9).placements).toEqual([
      { col: 1, row: 1 },
      { col: 3, row: 1 },
    ]);
    const triple = wallGeometry(getWallTemplate('triple'), 12);
    expect(triple.cols).toBe(6);
    expect(triple.placements).toEqual([
      { col: 1, row: 1 },
      { col: 3, row: 1 },
      { col: 5, row: 1 },
    ]);
  });

  it('unknown ids fall back to hero', () => {
    expect(getWallTemplate('circle').id).toBe('hero');
  });
});

// ─── HD quality clamp rule (#1072) ──────────────────────────────────────────
describe('tileQualityFor', () => {
  it('passes the grid quality through at or below the classic grid size', () => {
    expect(CLASSIC_GRID_SIZE).toBe(4);
    expect(tileQualityFor('main', 4, 1, 3)).toBe('main'); // every tile may ride main
    expect(tileQualityFor('main', 1, 1, 0)).toBe('main');
    expect(tileQualityFor('sub', 4, 1, 0)).toBe('sub');
  });

  it('never upgrades when the grid setting is sub', () => {
    expect(tileQualityFor('sub', 9, 1, 0)).toBe('sub');
    expect(tileQualityFor('sub', 16, 3, 2)).toBe('sub');
  });

  it('clamps HD to the featured tiles above the classic grid', () => {
    expect(tileQualityFor('main', 9, 1, 0)).toBe('main');
    expect(tileQualityFor('main', 9, 1, 1)).toBe('sub');
    // dual template: two featured tiles
    expect(tileQualityFor('main', 9, 2, 1)).toBe('main');
    expect(tileQualityFor('main', 9, 2, 2)).toBe('sub');
    // triple template: three featured tiles
    expect(tileQualityFor('main', 16, 3, 2)).toBe('main');
    expect(tileQualityFor('main', 16, 3, 3)).toBe('sub');
  });

  it('uniform grids above the classic size keep exactly one featured tile', () => {
    expect(tileQualityFor('main', 6, 1, 0)).toBe('main');
    expect(tileQualityFor('main', 6, 1, 1)).toBe('sub');
  });
});

// ─── Wall height row estimate ───────────────────────────────────────────────
describe('wallRowEstimate', () => {
  it('counts each featured 2×2 tile as three extra cells', () => {
    expect(wallRowEstimate(4, 1, 9)).toBe(3); // hero@9 → 4×3
    expect(wallRowEstimate(4, 2, 6)).toBe(3); // dual@6 → bigs + one small row
    expect(wallRowEstimate(6, 3, 9)).toBe(3); // triple@9 → bigs + one full row
    expect(wallRowEstimate(4, 1, 13)).toBe(4); // center@13 exact ring
  });

  it('never returns zero rows for degenerate counts', () => {
    expect(wallRowEstimate(4, 1, 1)).toBeGreaterThanOrEqual(1);
    expect(wallRowEstimate(1, 0, 0)).toBeGreaterThanOrEqual(1);
  });
});

// ─── Tier-raise auto-fill (#1072 follow-up) ─────────────────────────────────
describe('fillSelection', () => {
  const cams = [
    { id: 'a', status: 'recording' },
    { id: 'b', status: 'stopped' },
    { id: 'c', status: 'active' },
    { id: 'd', status: 'reconnecting' },
    { id: 'e', status: 'error' },
    { id: 'f', status: '' },
  ];

  it('grows the selection with online cameras in list order, skipping dead ones', () => {
    expect(fillSelection(['a'], cams, 3)).toEqual(['a', 'c', 'd']);
    // a(recording) kept, b(stopped)/e(error) never auto-filled,
    // c(active) first, d(reconnecting) only after online runs out
  });

  it('is a no-op at or above the target', () => {
    expect(fillSelection(['a', 'c'], cams, 2)).toEqual(['a', 'c']);
    expect(fillSelection(['a', 'c', 'd'], cams, 2)).toEqual(['a', 'c', 'd']);
  });

  it('fills fewer when online+recovering candidates run out', () => {
    expect(fillSelection(['a'], cams, 9)).toEqual(['a', 'c', 'd', 'f']);
    // f has an unknown status — treated as recovering-tier, allowed after online
  });

  it('never duplicates an already-selected camera', () => {
    const out = fillSelection(['c', 'd'], cams, 5);
    expect(out).toEqual(['c', 'd', 'a', 'f']);
    expect(new Set(out).size).toBe(out.length);
  });

  it('round-trips a tier down-up without losing the fill (regression)', () => {
    // 9 → 6 trim happens in the route (clamp effect); raising back must
    // re-fill to the tier, not stick at the trimmed count.
    const pool = [...cams, ...['x', 'y', 'z', 'w', 'v'].map((id) => ({ id, status: 'recording' }))];
    const nine = ['a', 'c', 'd', 'f', 'x', 'y', 'z', 'w', 'v'];
    const afterDown = nine.slice(0, 6); // what the clamp effect persists
    const backUp = fillSelection(afterDown, pool, 9);
    expect(backUp).toHaveLength(9);
    expect(backUp.slice(0, 6)).toEqual(afterDown); // existing order preserved
  });
});
