import { describe, it, expect } from 'vitest';
import { WALL_TEMPLATES, wallGeometry, getWallTemplate } from '$lib/wall-layouts';

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
