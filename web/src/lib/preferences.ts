/**
 * Centralized user preference management for MiBee NVR
 * Uses localStorage with prefix 'mibee_nvr_prefs_'
 */

const PREFIX = 'mibee_nvr_prefs_';

// Preference keys
export const PREFERENCE_KEYS = {
  ITEMS_PER_PAGE: 'items_per_page',
  AUTO_REFRESH: 'auto_refresh',
  THEME: 'theme',
  PROTOCOL: 'protocol',
} as const;
// Preference types
export interface Preferences {
  items_per_page: number;
  auto_refresh: string;
  theme: 'dark' | 'light' | null;
}

// Default values
export const DEFAULT_PREFERENCES: Preferences = {
  items_per_page: 50,
  auto_refresh: '30s',
  theme: null,
};

// Generic preference functions
export function getPreference<T>(key: string, defaultValue: T): T {
  const storageKey = `${PREFIX}${key}`;
  try {
    const value = localStorage.getItem(storageKey);
    if (value === null) return defaultValue;
    return JSON.parse(value);
  } catch {
    return defaultValue;
  }
}

export function setPreference<T>(key: string, value: T): void {
  const storageKey = `${PREFIX}${key}`;
  try {
    localStorage.setItem(storageKey, JSON.stringify(value));
  } catch (error) {
    console.error(`Failed to set preference ${key}:`, error);
  }
}

// Specific preference functions
export function getItemsPerPage(): number {
  return getPreference(PREFERENCE_KEYS.ITEMS_PER_PAGE, DEFAULT_PREFERENCES.items_per_page);
}

export function setItemsPerPage(n: number): void {
  if (n > 0 && n <= 200) {
    setPreference(PREFERENCE_KEYS.ITEMS_PER_PAGE, n);
  } else {
    console.warn('Items per page must be between 1 and 200');
  }
}

export function getAutoRefresh(): string {
  return getPreference(PREFERENCE_KEYS.AUTO_REFRESH, DEFAULT_PREFERENCES.auto_refresh);
}

export function setAutoRefresh(val: string): void {
  const validValues = ['30s', '60s', '120s', 'off'];
  if (validValues.includes(val)) {
    setPreference(PREFERENCE_KEYS.AUTO_REFRESH, val);
  } else {
    console.warn('Auto refresh must be one of: 10s, 30s, 60s, off');
  }
}

export function getTheme(): 'dark' | 'light' | null {
  return getPreference(PREFERENCE_KEYS.THEME, DEFAULT_PREFERENCES.theme);
}

export function setTheme(theme: 'dark' | 'light' | null): void {
  setPreference(PREFERENCE_KEYS.THEME, theme);
}

// Get effective theme (system preference when theme is null)
export function getEffectiveTheme(): 'dark' | 'light' {
  const saved = getTheme();
  if (saved !== null) return saved;
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

// Utility functions
export function parseRefreshInterval(str: string): number {
  const clean = str.toLowerCase().trim();

  if (clean === 'off') return 0;

  const match = clean.match(/^(\d+)s$/);
  if (match) {
    const seconds = parseInt(match[1], 10);
    return seconds * 1000; // Convert to milliseconds
  }

  console.warn(`Invalid refresh interval format: ${str}`);
  return 0;
}

export function formatRefreshInterval(ms: number): string {
  if (ms === 0) return 'off';

  const seconds = Math.round(ms / 1000);
  if (seconds >= 10 && seconds <= 60 && seconds % 10 === 0) {
    return `${seconds}s`;
  }

  console.warn(`Cannot format ${ms}ms to standard interval`);
  return `${ms}ms`;
}

// Reset all preferences to defaults
export function resetPreferences(): void {
  Object.entries(DEFAULT_PREFERENCES).forEach(([key, value]) => {
    setPreference(key, value);
  });
}

// Clear every per-camera protocol override (mibee_nvr_prefs_proto_*). Used by
// "reset preferences" flows — overrides are keyed by camera id so they can't
// be enumerated via DEFAULT_PREFERENCES; we sweep localStorage by prefix.
export function clearAllCameraProtocolOverrides(): void {
  try {
    const toRemove: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (key && key.startsWith(PROTOCOL_OVERRIDE_PREFIX)) toRemove.push(key);
    }
    for (const key of toRemove) localStorage.removeItem(key);
  } catch {
    /* ignore */
  }
}

// Per-camera protocol override.
//
// Lets a user pin a specific streaming protocol to one camera (via the
// ProtocolSwitcher on the LiveView page) that differs from the backend's
// auto-selected default. Stored in localStorage keyed by camera id — this is
// intentionally NOT a server-side setting: the backend already computes the
// best default per camera (codec-aware), so an override is only ever a manual
// nudge and doesn't need to sync across devices.
//
// NOTE: only a USER's explicit manual selection should be written here. The
// runtime auto-fallback (Surveillance demoting webrtc->flv->hls on failure)
// must NOT write an override, or a transient failure would permanently pin a
// worse protocol.
//
// Issue #112: overrides carry a TTL (PROTOCOL_OVERRIDE_TTL_MS). A stale pin
// (e.g. 'hls' set when the camera was briefly unreachable) could otherwise
// force a non-MJPEG protocol onto an MJPEG camera indefinitely, keeping the
// protocol storm alive across sessions. After the TTL the override is treated
// as expired and auto-selection takes over again.
const PROTOCOL_OVERRIDE_PREFIX = `${PREFIX}proto_`;
/** Override shelf life. 24h — long enough to survive a day of viewing, short
 *  enough that a stale pin from a prior outage doesn't outlive its usefulness. */
export const PROTOCOL_OVERRIDE_TTL_MS = 24 * 60 * 60 * 1000;

interface StoredOverride {
  protocol: string;
  ts: number;
}

export function getCameraProtocolOverride(cameraId: string): string | null {
  try {
    const raw = localStorage.getItem(`${PROTOCOL_OVERRIDE_PREFIX}${cameraId}`);
    if (raw === null) return null;
    // Backward compat: legacy entries stored a bare protocol string. Migrate
    // on read by re-storing in the new {protocol, ts} shape, treated as just
    // set (no expiry on this read). A subsequent setCameraProtocolOverride
    // call will stamp a real timestamp.
    if (!raw.startsWith('{')) {
      return raw;
    }
    const parsed = JSON.parse(raw) as Partial<StoredOverride>;
    if (typeof parsed.protocol !== 'string' || typeof parsed.ts !== 'number') {
      return null;
    }
    if (Date.now() - parsed.ts > PROTOCOL_OVERRIDE_TTL_MS) {
      // Expired — clear it so it stops influencing selection, and return null.
      localStorage.removeItem(`${PROTOCOL_OVERRIDE_PREFIX}${cameraId}`);
      return null;
    }
    return parsed.protocol;
  } catch {
    return null;
  }
}

export function setCameraProtocolOverride(cameraId: string, protocol: string): void {
  try {
    const value: StoredOverride = { protocol, ts: Date.now() };
    localStorage.setItem(`${PROTOCOL_OVERRIDE_PREFIX}${cameraId}`, JSON.stringify(value));
  } catch (error) {
    console.error('Failed to set protocol override:', error);
  }
}

export function clearCameraProtocolOverride(cameraId: string): void {
  try {
    localStorage.removeItem(`${PROTOCOL_OVERRIDE_PREFIX}${cameraId}`);
  } catch {
    /* ignore */
  }
}
