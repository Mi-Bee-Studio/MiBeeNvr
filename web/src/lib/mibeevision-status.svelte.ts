/**
 * MiBeeVision connection status — shared reactive state.
 *
 * The AI Events nav item and page are only shown when at least one
 * non-revoked API key is configured (set via Settings → MiBeeVision).
 * This module provides a Svelte 5 runes-compatible reactive store so
 * Header.svelte and AIEvents.svelte can both read the same state.
 */
import { getSettings } from './api';

let _connected = $state(false);
let _loaded = false;

export function getMiBeeVisionConnected(): boolean {
  return _connected;
}

export function getMiBeeVisionLoaded(): boolean {
  return _loaded;
}

/** Fetch the current API key status from the backend and update state. */
export async function refreshMiBeeVisionStatus(): Promise<void> {
  try {
    const settings = await getSettings();
    _connected = (settings.mibeevision?.api_keys?.length ?? 0) > 0;
  } catch {
    // Non-fatal — default to not connected
    _connected = false;
  } finally {
    _loaded = true;
  }
}
