// Saved surveillance-wall combinations ("组合方案"): a named snapshot of the
// selected cameras + photo-wall settings, so an operator can switch between a
// few fixed preview walls in one click. Deliberately capped at 3 presets —
// the picker is a compact panel and the point is a handful of fixed views,
// not an unlimited library (user-ruled 2026-11-21).
//
// Pure localStorage-backed module (no Svelte state) so the cap/validation
// logic is unit-testable. The applied grid still lives in Surveillance.svelte;
// loading a preset hands the parent the snapshot to apply.

import { WALL_TEMPLATE_IDS, type WallTemplateId } from './wall-layouts';

export const WALL_PRESET_STORAGE_KEY = 'mibee_nvr_prefs_wall_presets';
export const MAX_WALL_PRESETS = 3;
export const MAX_PRESET_NAME_LEN = 32;

export interface WallPreset {
  id: string;
  name: string;
  cameraIds: string[];
  maxGridCameras: number;
  photoWall: boolean;
  rotationSecs: number;
  wallTemplateId: WallTemplateId;
  savedAt: number;
}

/** Input accepted from the picker when saving the current configuration. */
export interface WallPresetInput {
  name: string;
  cameraIds: string[];
  maxGridCameras: number;
  photoWall: boolean;
  rotationSecs: number;
  wallTemplateId: string;
}

function isTemplateId(v: unknown): v is WallTemplateId {
  return typeof v === 'string' && (WALL_TEMPLATE_IDS as readonly string[]).includes(v);
}

/** Validate one stored entry; drop malformed ones rather than crashing the UI. */
function parsePreset(raw: unknown, index: number): WallPreset | null {
  if (!raw || typeof raw !== 'object') return null;
  const p = raw as Record<string, unknown>;
  if (typeof p.name !== 'string' || !p.name.trim()) return null;
  if (!Array.isArray(p.cameraIds) || !p.cameraIds.every((v) => typeof v === 'string')) return null;
  if (!isTemplateId(p.wallTemplateId)) return null;
  if (typeof p.maxGridCameras !== 'number' || !Number.isFinite(p.maxGridCameras)) return null;
  if (typeof p.photoWall !== 'boolean') return null;
  if (typeof p.rotationSecs !== 'number' || !Number.isFinite(p.rotationSecs)) return null;
  return {
    id: typeof p.id === 'string' && p.id ? p.id : `preset-${index}-${p.savedAt ?? 0}`,
    name: p.name.trim().slice(0, MAX_PRESET_NAME_LEN),
    cameraIds: p.cameraIds as string[],
    maxGridCameras: p.maxGridCameras,
    photoWall: p.photoWall,
    rotationSecs: p.rotationSecs,
    wallTemplateId: p.wallTemplateId,
    savedAt: typeof p.savedAt === 'number' ? p.savedAt : 0,
  };
}

function readStorage(storage: Storage): WallPreset[] {
  try {
    const raw = storage.getItem(WALL_PRESET_STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed
      .map(parsePreset)
      .filter((p): p is WallPreset => p !== null)
      .slice(0, MAX_WALL_PRESETS);
  } catch {
    return [];
  }
}

function writeStorage(storage: Storage, presets: WallPreset[]): void {
  try {
    storage.setItem(WALL_PRESET_STORAGE_KEY, JSON.stringify(presets.slice(0, MAX_WALL_PRESETS)));
  } catch (e) {
    console.warn('Failed to persist wall presets:', e);
  }
}

/** List saved presets (oldest first). Corrupt entries are skipped. */
export function listWallPresets(storage: Storage = localStorage): WallPreset[] {
  return readStorage(storage);
}

/** Whether another preset can still be saved (cap = 3). */
export function canSaveWallPreset(storage: Storage = localStorage): boolean {
  return readStorage(storage).length < MAX_WALL_PRESETS;
}

/**
 * Save the current configuration as a named preset. Returns the saved list,
 * or null when the name is empty or the 3-preset cap is reached. Re-saving
 * under an existing name replaces that slot instead of consuming a new one.
 */
export function saveWallPreset(
  input: WallPresetInput,
  storage: Storage = localStorage,
  now = Date.now(),
): WallPreset[] | null {
  const name = input.name.trim().slice(0, MAX_PRESET_NAME_LEN);
  if (!name) return null;
  const list = readStorage(storage);
  const existing = list.findIndex((p) => p.name === name);
  const template = isTemplateId(input.wallTemplateId) ? input.wallTemplateId : 'hero';
  const preset: WallPreset = {
    id: existing >= 0 ? list[existing].id : `preset-${now}-${Math.random().toString(36).slice(2, 8)}`,
    name,
    cameraIds: [...input.cameraIds],
    maxGridCameras: input.maxGridCameras,
    photoWall: input.photoWall,
    rotationSecs: input.rotationSecs,
    wallTemplateId: template,
    savedAt: now,
  };
  if (existing >= 0) list[existing] = preset;
  else {
    if (list.length >= MAX_WALL_PRESETS) return null;
    list.push(preset);
  }
  writeStorage(storage, list);
  return list;
}

/** Remove a preset by id. Returns the remaining list. */
export function deleteWallPreset(id: string, storage: Storage = localStorage): WallPreset[] {
  const list = readStorage(storage).filter((p) => p.id !== id);
  writeStorage(storage, list);
  return list;
}
