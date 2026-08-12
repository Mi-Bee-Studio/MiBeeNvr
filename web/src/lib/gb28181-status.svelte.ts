/**
 * GB28181 server enabled state — shared reactive state.
 *
 * The GB28181 Devices nav item and page are only shown when the GB/T 28181
 * server is enabled (set via Settings → GB/T 28181 Server). This module
 * provides a Svelte 5 runes-compatible reactive store so Header.svelte and
 * GB28181Devices.svelte can both read the same state.
 */
import { getSettings } from './api';

let _enabled = $state(false);
let _loaded = false;

export function getGB28181Enabled(): boolean {
  return _enabled;
}

export function getGB28181Loaded(): boolean {
  return _loaded;
}

/** Fetch the current GB28181 server status from the backend and update state. */
export async function refreshGB28181Status(): Promise<void> {
  try {
    const settings = await getSettings();
    _enabled = settings.gb28181?.enabled === true;
  } catch {
    // Non-fatal — default to disabled
    _enabled = false;
  } finally {
    _loaded = true;
  }
}