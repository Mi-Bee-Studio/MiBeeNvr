import { describe, it, expect, beforeEach } from 'vitest';
import {
  MAX_WALL_PRESETS,
  WALL_PRESET_STORAGE_KEY,
  listWallPresets,
  canSaveWallPreset,
  saveWallPreset,
  deleteWallPreset,
  type WallPresetInput,
} from '$lib/wall-presets';

function memStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() { return map.size; },
    clear: () => map.clear(),
    getItem: (k: string) => map.get(k) ?? null,
    key: (i: number) => [...map.keys()][i] ?? null,
    removeItem: (k: string) => { map.delete(k); },
    setItem: (k: string, v: string) => { map.set(k, v); },
  };
}

const input = (name: string, cameraIds: string[]): WallPresetInput => ({
  name,
  cameraIds,
  maxGridCameras: 9,
  photoWall: true,
  rotationSecs: 30,
  wallTemplateId: 'dual',
});

describe('wall presets (max 3 named combinations)', () => {
  let storage: Storage;
  beforeEach(() => { storage = memStorage(); });

  it('starts empty and allows saving', () => {
    expect(listWallPresets(storage)).toEqual([]);
    expect(canSaveWallPreset(storage)).toBe(true);
  });

  it('saves a preset and reads it back with all settings', () => {
    const list = saveWallPreset(input('室内', ['cam-1', 'cam-2']), storage, 1000);
    expect(list).toHaveLength(1);
    const got = listWallPresets(storage)[0];
    expect(got).toMatchObject({
      name: '室内',
      cameraIds: ['cam-1', 'cam-2'],
      maxGridCameras: 9,
      photoWall: true,
      rotationSecs: 30,
      wallTemplateId: 'dual',
    });
    expect(got.id).toBeTruthy();
    expect(got.savedAt).toBe(1000);
  });

  it('caps at 3 presets and refuses a 4th', () => {
    saveWallPreset(input('A', ['a']), storage, 1);
    saveWallPreset(input('B', ['b']), storage, 2);
    saveWallPreset(input('C', ['c']), storage, 3);
    expect(listWallPresets(storage)).toHaveLength(MAX_WALL_PRESETS);
    expect(canSaveWallPreset(storage)).toBe(false);
    expect(saveWallPreset(input('D', ['d']), storage, 4)).toBeNull();
    expect(listWallPresets(storage).map((p) => p.name)).toEqual(['A', 'B', 'C']);
  });

  it('re-saving an existing name replaces that slot without consuming capacity', () => {
    saveWallPreset(input('A', ['a']), storage, 1);
    saveWallPreset(input('B', ['b']), storage, 2);
    const list = saveWallPreset(input('A', ['a-updated']), storage, 3);
    expect(list).toHaveLength(2);
    expect(listWallPresets(storage).find((p) => p.name === 'A')?.cameraIds).toEqual(['a-updated']);
  });

  it('rejects a blank name', () => {
    expect(saveWallPreset(input('   ', ['a']), storage, 1)).toBeNull();
    expect(listWallPresets(storage)).toEqual([]);
  });

  it('deletes by id', () => {
    saveWallPreset(input('A', ['a']), storage, 1);
    const id = listWallPresets(storage)[0].id;
    expect(deleteWallPreset(id, storage)).toEqual([]);
    expect(listWallPresets(storage)).toEqual([]);
  });

  it('skips malformed entries and keeps valid ones', () => {
    storage.setItem(WALL_PRESET_STORAGE_KEY, JSON.stringify([
      { name: 'ok', cameraIds: ['x'], maxGridCameras: 4, photoWall: false, rotationSecs: 0, wallTemplateId: 'hero' },
      { bad: true },
      { name: 'bad-template', cameraIds: [], maxGridCameras: 4, photoWall: false, rotationSecs: 0, wallTemplateId: 'nope' },
    ]));
    const list = listWallPresets(storage);
    expect(list).toHaveLength(1);
    expect(list[0].name).toBe('ok');
  });

  it('tolerates corrupt JSON', () => {
    storage.setItem(WALL_PRESET_STORAGE_KEY, '{not json');
    expect(listWallPresets(storage)).toEqual([]);
  });
});
