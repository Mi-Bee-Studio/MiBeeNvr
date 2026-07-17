import { describe, it, expect, beforeEach } from 'vitest';
import {
  getCameraProtocolOverride,
  setCameraProtocolOverride,
  clearCameraProtocolOverride,
  clearAllCameraProtocolOverrides,
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
});
