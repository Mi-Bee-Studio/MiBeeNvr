import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
  getCameraProtocolOverride,
  setCameraProtocolOverride,
  clearCameraProtocolOverride,
  clearAllCameraProtocolOverrides,
  PROTOCOL_OVERRIDE_TTL_MS,
  getMaxGridCameras,
  setMaxGridCameras,
  isPhotoWallEnabled,
  setPhotoWallEnabled,
  MAX_GRID_CAMERAS_DEFAULT,
  MAX_GRID_CAMERAS_OPTIONS,
} from '$lib/preferences';

beforeEach(() => {
  localStorage.clear();
});

describe('per-camera protocol override', () => {
  it('returns null when no override is set', () => {
    expect(getCameraProtocolOverride('cam-1')).toBeNull();
  });

  it('stores and retrieves an override by camera id', () => {
    setCameraProtocolOverride('cam-1', 'flv');
    expect(getCameraProtocolOverride('cam-1')).toBe('flv');
    // Other cameras unaffected.
    expect(getCameraProtocolOverride('cam-2')).toBeNull();
  });

  it('clears a single override', () => {
    setCameraProtocolOverride('cam-1', 'webrtc');
    clearCameraProtocolOverride('cam-1');
    expect(getCameraProtocolOverride('cam-1')).toBeNull();
  });

  it('clears all overrides via clearAllCameraProtocolOverrides', () => {
    setCameraProtocolOverride('cam-1', 'flv');
    setCameraProtocolOverride('cam-2', 'hls');
    clearAllCameraProtocolOverrides();
    expect(getCameraProtocolOverride('cam-1')).toBeNull();
    expect(getCameraProtocolOverride('cam-2')).toBeNull();
  });

  it('isolates overrides from other localStorage keys', () => {
    localStorage.setItem('mibee_nvr_prefs_theme', '"dark"');
    setCameraProtocolOverride('cam-1', 'webrtc');
    clearAllCameraProtocolOverrides();
    // Theme pref must survive the override sweep.
    expect(localStorage.getItem('mibee_nvr_prefs_theme')).toBe('"dark"');
    expect(getCameraProtocolOverride('cam-1')).toBeNull();
  });

  // ─── Issue #112: TTL expiry + backward compat ─────────────────────────────
  it('expires an override older than the TTL (returns null and clears it)', () => {
    vi.useFakeTimers();
    setCameraProtocolOverride('cam-1', 'hls');
    // Advance past the TTL — the override should now be treated as stale.
    vi.advanceTimersByTime(PROTOCOL_OVERRIDE_TTL_MS + 1);
    expect(getCameraProtocolOverride('cam-1')).toBeNull();
    // And it should have been removed from localStorage (not just ignored).
    expect(localStorage.getItem('mibee_nvr_prefs_proto_cam-1')).toBeNull();
    vi.useRealTimers();
  });

  it('keeps an override that is within the TTL', () => {
    vi.useFakeTimers();
    setCameraProtocolOverride('cam-1', 'flv');
    vi.advanceTimersByTime(PROTOCOL_OVERRIDE_TTL_MS - 1000); // just under TTL
    expect(getCameraProtocolOverride('cam-1')).toBe('flv');
    vi.useRealTimers();
  });

  it('reads a legacy bare-string override (backward compat)', () => {
    // Pre-#112 entries stored a plain protocol string without a timestamp.
    // They must still be readable so existing users don't lose their pins.
    localStorage.setItem('mibee_nvr_prefs_proto_cam-legacy', 'webrtc');
    expect(getCameraProtocolOverride('cam-legacy')).toBe('webrtc');
  });
});

// ─── Grid wall preferences (#1072) ─────────────────────────────────────────
describe('grid wall preferences', () => {
  it('defaults to 4 cameras and photo-wall off', () => {
    expect(getMaxGridCameras()).toBe(MAX_GRID_CAMERAS_DEFAULT);
    expect(MAX_GRID_CAMERAS_DEFAULT).toBe(4);
    expect(isPhotoWallEnabled()).toBe(false);
  });

  it('round-trips the preview count within the allowed options', () => {
    setMaxGridCameras(9);
    expect(getMaxGridCameras()).toBe(9);
    expect(localStorage.getItem('mibee_nvr_prefs_max_grid_cameras')).toBe('9');
    setMaxGridCameras(4);
    expect(getMaxGridCameras()).toBe(4);
  });

  it('rejects values outside the options list', () => {
    setMaxGridCameras(9);
    setMaxGridCameras(5 as unknown as never); // not an option
    expect(getMaxGridCameras()).toBe(9);
  });

  it('guards against hand-edited localStorage', () => {
    localStorage.setItem('mibee_nvr_prefs_max_grid_cameras', '7');
    expect(getMaxGridCameras()).toBe(7); // positive integers pass — clamped by server budget at runtime
    localStorage.setItem('mibee_nvr_prefs_max_grid_cameras', '"nope"');
    expect(getMaxGridCameras()).toBe(MAX_GRID_CAMERAS_DEFAULT);
    localStorage.setItem('mibee_nvr_prefs_max_grid_cameras', '-3');
    expect(getMaxGridCameras()).toBe(MAX_GRID_CAMERAS_DEFAULT);
  });

  it('round-trips the photo-wall flag', () => {
    setPhotoWallEnabled(true);
    expect(isPhotoWallEnabled()).toBe(true);
    setPhotoWallEnabled(false);
    expect(isPhotoWallEnabled()).toBe(false);
  });

  it('keeps the default option list ascending with 4 first', () => {
    const opts = [...MAX_GRID_CAMERAS_OPTIONS];
    expect(opts[0]).toBe(4);
    expect(opts[0]).toBe(MAX_GRID_CAMERAS_DEFAULT);
    for (let i = 1; i < opts.length; i++) expect(opts[i]).toBeGreaterThan(opts[i - 1]);
  });
});

// ─── Wall rotation preference (#1072 P1) ───────────────────────────────────
describe('wall rotation preference', () => {
  it('defaults to off (0)', async () => {
    const { getWallRotationSecs, WALL_ROTATION_DEFAULT } = await import('$lib/preferences');
    expect(getWallRotationSecs()).toBe(WALL_ROTATION_DEFAULT);
    expect(WALL_ROTATION_DEFAULT).toBe(0);
  });

  it('round-trips within the allowed options', async () => {
    const { getWallRotationSecs, setWallRotationSecs } = await import('$lib/preferences');
    setWallRotationSecs(60);
    expect(getWallRotationSecs()).toBe(60);
    expect(localStorage.getItem('mibee_nvr_prefs_wall_rotation_secs')).toBe('60');
    setWallRotationSecs(0);
    expect(getWallRotationSecs()).toBe(0);
  });

  it('rejects values outside the options and falls back on garbage storage', async () => {
    const { getWallRotationSecs, setWallRotationSecs, WALL_ROTATION_DEFAULT } = await import('$lib/preferences');
    setWallRotationSecs(45);
    expect(getWallRotationSecs()).toBe(0); // still the previously stored 0
    localStorage.setItem('mibee_nvr_prefs_wall_rotation_secs', '"fast"');
    expect(getWallRotationSecs()).toBe(WALL_ROTATION_DEFAULT);
  });
});

// ─── Wall layout template preference (#1072) ───────────────────────────────
describe('wall layout template preference', () => {
  it('defaults to hero', async () => {
    const { getWallTemplateId, WALL_TEMPLATE_DEFAULT } = await import('$lib/preferences');
    expect(getWallTemplateId()).toBe(WALL_TEMPLATE_DEFAULT);
    expect(WALL_TEMPLATE_DEFAULT).toBe('hero');
  });

  it('round-trips a known template and persists it', async () => {
    const { getWallTemplateId, setWallTemplateId } = await import('$lib/preferences');
    setWallTemplateId('center');
    expect(getWallTemplateId()).toBe('center');
    expect(localStorage.getItem('mibee_nvr_prefs_wall_template')).toBe('"center"');
  });

  it('rejects unknown ids and falls back on garbage storage', async () => {
    const { getWallTemplateId, setWallTemplateId, WALL_TEMPLATE_DEFAULT } = await import('$lib/preferences');
    setWallTemplateId('circle'); // not a template
    expect(getWallTemplateId()).toBe(WALL_TEMPLATE_DEFAULT);
    localStorage.setItem('mibee_nvr_prefs_wall_template', '"nope"');
    expect(getWallTemplateId()).toBe(WALL_TEMPLATE_DEFAULT);
  });
});
