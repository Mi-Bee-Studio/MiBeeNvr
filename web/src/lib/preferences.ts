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
} as const;

// Preference types
export interface Preferences {
  items_per_page: number;
  auto_refresh: string;
  theme: 'dark' | 'light';
}

// Default values
export const DEFAULT_PREFERENCES: Preferences = {
  items_per_page: 50,
  auto_refresh: '30s',
  theme: 'dark',
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
  const validValues = ['10s', '30s', '60s', 'off'];
  if (validValues.includes(val)) {
    setPreference(PREFERENCE_KEYS.AUTO_REFRESH, val);
  } else {
    console.warn('Auto refresh must be one of: 10s, 30s, 60s, off');
  }
}

export function getTheme(): 'dark' | 'light' {
  return getPreference(PREFERENCE_KEYS.THEME, DEFAULT_PREFERENCES.theme);
}

export function setTheme(theme: 'dark' | 'light'): void {
  setPreference(PREFERENCE_KEYS.THEME, theme);
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