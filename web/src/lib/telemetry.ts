/**
 * Playback telemetry utility — fire-and-forget via navigator.sendBeacon.
 *
 * Sends telemetry events (playback start/error/end, buffer stats) to the
 * backend POST /api/telemetry endpoint. Uses sendBeacon for non-blocking
 * delivery that survives page unload.
 */

import { API_BASE, getTokenForUrl } from '$lib/api/client';

const TELEMETRY_ENDPOINT = `${API_BASE}/telemetry`;

/** Whether user has explicitly opted into telemetry in production mode. */
let _telemetryOptedIn = false;

/**
 * Opt in to telemetry in production mode.
 * Telemetry is always sent in dev mode.
 */
export function optInTelemetry(): void {
  _telemetryOptedIn = true;
}

/** Returns whether telemetry has been opted in. Exported for testing. */
export function isTelemetryOptedIn(): boolean {
  return _telemetryOptedIn;
}

/** @internal Reset opt-in state for testing. */
export function __resetOptIn(): void {
  _telemetryOptedIn = false;
}

interface TelemetryEvent {
  event: string;
  camera_id: string;
  duration_ms?: number;
  details?: object;
}

/**
 * Send a telemetry event to the backend using navigator.sendBeacon.
 *
 * Gracefully degrades: if sendBeacon is unavailable or no session token
 * is present, silently skips. Never throws.
 *
 * @param event      Event type (e.g., "playback_start", "playback_error")
 * @param cameraId   Camera identifier
 * @param durationMs Optional duration in milliseconds
 * @param details    Optional extra data (error codes, buffer stats, etc.)
 */
export function sendTelemetry(event: string, cameraId: string, durationMs?: number, details?: object): void {
  // Production guard: silently skip unless opted in
  if (import.meta.env.PROD && !_telemetryOptedIn) return;

  if (typeof navigator?.sendBeacon !== 'function') return;

  const token = getTokenForUrl();
  if (!token) return;

  const payload: TelemetryEvent = {
    event,
    camera_id: cameraId,
    ...(durationMs !== undefined && { duration_ms: durationMs }),
    ...(details && { details }),
  };

  // sendBeacon cannot set headers, so pass the session token as a ?token= query
  // param — the auth middleware accepts ?token=mbs_... on the same path as the
  // Bearer header. The token is already the signed "mbs_..." string.
  const url = `${TELEMETRY_ENDPOINT}?token=${encodeURIComponent(token)}`;

  const blob = new Blob([JSON.stringify(payload)], {
    type: 'application/json',
  });

  try {
    navigator.sendBeacon(url, blob);
  } catch {
    // Fire-and-forget: never throw
  }
}
