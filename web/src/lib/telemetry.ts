/**
 * Playback telemetry utility — fire-and-forget via navigator.sendBeacon.
 *
 * Sends telemetry events (playback start/error/end, buffer stats) to the
 * backend POST /api/telemetry endpoint. Uses sendBeacon for non-blocking
 * delivery that survives page unload.
 */

const AUTH_KEY = 'mibee_nvr_auth';
const TELEMETRY_ENDPOINT = '/api/telemetry';

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

/** Get stored BasicAuth credentials from sessionStorage. */
function getAuthCredentials(): { username: string; password: string } | null {
  try {
    const encoded = sessionStorage.getItem(AUTH_KEY);
    if (!encoded) return null;
    const decoded = atob(encoded);
    const colonIdx = decoded.indexOf(':');
    if (colonIdx === -1) return null;
    return {
      username: decoded.substring(0, colonIdx),
      password: decoded.substring(colonIdx + 1),
    };
  } catch {
    return null;
  }
}

/**
 * Send a telemetry event to the backend using navigator.sendBeacon.
 *
 * Gracefully degrades: if sendBeacon is unavailable or auth credentials
 * are missing, silently skips. Never throws.
 *
 * @param event      Event type (e.g., "playback_start", "playback_error")
 * @param cameraId   Camera identifier
 * @param durationMs Optional duration in milliseconds
 * @param details    Optional extra data (error codes, buffer stats, etc.)
 */
export function sendTelemetry(
  event: string,
  cameraId: string,
  durationMs?: number,
  details?: object,
  ): void {
  // Production guard: silently skip unless opted in
  if (import.meta.env.PROD && !_telemetryOptedIn) return;

  if (typeof navigator?.sendBeacon !== 'function') return;

  const creds = getAuthCredentials();
  if (!creds) return;

  const payload: TelemetryEvent = {
    event,
    camera_id: cameraId,
    ...(durationMs !== undefined && { duration_ms: durationMs }),
    ...(details && { details }),
  };

  // sendBeacon cannot set headers directly. Use Blob + FormData to pass
  // BasicAuth. The server's BasicAuth middleware also accepts ?token= for
  // WebSocket, but for POST we use Blob with the right Content-Type and
  // embed auth in a custom header via the fetch-with-keepalive fallback
  // when sendBeacon can't carry the auth header.

  // Build BasicAuth token as query param — the auth middleware supports
  // this fallback (originally for WebSocket).
  const token = btoa(`${creds.username}:${creds.password}`);
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
