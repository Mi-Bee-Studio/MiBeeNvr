// Photo-wall layout templates (#1072).
//
// A template is a *rule*, not a fixed map: it declares how many featured
// 2×2 tiles anchor the wall and where they sit (1-based grid start lines);
// every remaining camera is a 1×1 tile dense-packed around them. That way
// each template stays recognizable at any camera count and never leaves a
// mid-grid hole — counts that don't tile perfectly just get a ragged tail
// row, like any plain grid.

export type WallTemplateId =
  | 'hero'
  | 'hero-tr'
  | 'hero-bl'
  | 'hero-br'
  | 'center'
  | 'dual'
  | 'triple';

export const WALL_TEMPLATE_IDS: readonly WallTemplateId[] = [
  'hero',
  'hero-tr',
  'hero-bl',
  'hero-br',
  'center',
  'dual',
  'triple',
];

/** 1-based grid start line of a featured 2×2 tile. */
export interface FeaturedPlacement {
  col: number;
  row: number;
}

export interface WallTemplate {
  id: WallTemplateId;
  /** How many featured 2×2 tiles anchor the layout. */
  featured: number;
  /** Below this camera count the template is disabled in the picker. */
  minCameras: number;
  /** Grid column count for a given camera count. */
  cols: (count: number) => number;
  /** Explicit start lines for each featured tile. */
  placements: (count: number) => FeaturedPlacement[];
}

// Column tiers shared by the single-hero variants: 6 → 3×3, 9 → 4-wide,
// 12+ → 5-wide, matching the original photo-wall packing.
const heroCols = (count: number): number => (count <= 6 ? 3 : count <= 9 ? 4 : 5);

export const WALL_TEMPLATES: readonly WallTemplate[] = [
  {
    id: 'hero',
    featured: 1,
    minCameras: 5,
    cols: heroCols,
    placements: () => [{ col: 1, row: 1 }],
  },
  {
    id: 'hero-tr',
    featured: 1,
    minCameras: 5,
    cols: heroCols,
    placements: (count) => [{ col: heroCols(count) - 1, row: 1 }],
  },
  {
    id: 'hero-bl',
    featured: 1,
    minCameras: 5,
    cols: heroCols,
    placements: () => [{ col: 1, row: 2 }],
  },
  {
    id: 'hero-br',
    featured: 1,
    minCameras: 5,
    cols: heroCols,
    placements: (count) => [{ col: heroCols(count) - 1, row: 2 }],
  },
  {
    // Centered 2×2 with smalls ringing it: exact at 9 (4×3) and 13 (4×4);
    // between those counts dense flow leaves symmetric side holes or a
    // short tail row — both read as "big picture in the middle".
    id: 'center',
    featured: 1,
    minCameras: 6,
    cols: () => 4,
    placements: () => [{ col: 2, row: 2 }],
  },
  {
    // Two featured tiles side by side across the top; exact at 6/10/14.
    id: 'dual',
    featured: 2,
    minCameras: 6,
    cols: () => 4,
    placements: () => [
      { col: 1, row: 1 },
      { col: 3, row: 1 },
    ],
  },
  {
    // Three featured tiles filling the top half (6 columns); exact at
    // 9 (3 big + one full small row) and 15.
    id: 'triple',
    featured: 3,
    minCameras: 9,
    cols: () => 6,
    placements: () => [
      { col: 1, row: 1 },
      { col: 3, row: 1 },
      { col: 5, row: 1 },
    ],
  },
];

export function getWallTemplate(id: string): WallTemplate {
  return WALL_TEMPLATES.find((tpl) => tpl.id === id) ?? WALL_TEMPLATES[0];
}

export interface WallGeometry {
  cols: number;
  featured: number;
  placements: FeaturedPlacement[];
}

/** Resolve a template to concrete grid numbers for an exact camera count. */
export function wallGeometry(template: WallTemplate, count: number): WallGeometry {
  const cols = template.cols(count);
  return {
    cols,
    featured: template.featured,
    placements: template.placements(count).map((p) => ({ col: p.col, row: p.row })),
  };
}
