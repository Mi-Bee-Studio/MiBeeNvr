import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
  getCameraProtocolOverride,
  setCameraProtocolOverride,
  clearCameraProtocolOverride,
  clearAllCameraProtocolOverrides,
  PROTOCOL_OVERRIDE_TTL_MS,
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
