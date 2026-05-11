/**
 * Shared HLS error handling with retry logic, exponential backoff,
 * and automatic snapshot fallback.
 *
 * Usage: call `setupHlsErrorHandling(hls, Hls, config)` after creating
 * the Hls instance but before `loadSource()`.
 */

import { getCredentials } from '$lib/api';

export type StreamState = 'playing' | 'buffering' | 'error' | 'snapshot';

export interface HlsErrorConfig {
  cameraId: string;
  maxRetries: number;
  retryDelays: number[];
  onStateChange: (cameraId: string, state: StreamState) => void;
  onFallbackToSnapshot: (cameraId: string) => void;
}

/** Check if HLS stream endpoint returns 429 (max streams reached). */
export async function checkStreamAvailable(url: string): Promise<boolean> {
  try {
    const creds = getCredentials();
    const headers: HeadersInit = {};
    if (creds) {
      headers['Authorization'] = 'Basic ' + btoa(`${creds.username}:${creds.password}`);
    }
    const resp = await fetch(url, { method: 'HEAD', headers });
    return resp.status !== 429;
  } catch {
    return true; // Assume available if check fails
  }
}

/**
 * Set up error handling on an Hls instance.
 *
 * @param hls   The Hls instance (from dynamic import).
 * @param Hls   The Hls constructor (needed for static enum access).
 * @param config  Error handling callbacks.
 */
export function setupHlsErrorHandling(
  hls: any,
  Hls: any,
  config: HlsErrorConfig,
): void {
  let retryCount = 0;
  const { cameraId, maxRetries, retryDelays, onStateChange, onFallbackToSnapshot } = config;

  hls.on(Hls.Events.ERROR, (_event: string, data: any) => {
    if (data.fatal) {
      switch (data.type) {
        case Hls.ErrorTypes.NETWORK_ERROR: {
          if (retryCount < maxRetries) {
            const delay = retryDelays[retryCount] || retryDelays[retryDelays.length - 1];
            retryCount++;
            onStateChange(cameraId, 'buffering');
            setTimeout(() => {
              hls.startLoad();
            }, delay);
          } else {
            onStateChange(cameraId, 'error');
            onFallbackToSnapshot(cameraId);
          }
          break;
        }
        case Hls.ErrorTypes.MEDIA_ERROR: {
          if (retryCount < maxRetries) {
            retryCount++;
            hls.recoverMediaError();
          } else {
            onStateChange(cameraId, 'error');
            onFallbackToSnapshot(cameraId);
          }
          break;
        }
        default:
          onStateChange(cameraId, 'error');
          onFallbackToSnapshot(cameraId);
          break;
      }
    } else {
      // Non-fatal media errors — attempt recovery
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        hls.recoverMediaError();
      }
    }
  });

  hls.on(Hls.Events.FRAG_LOADED, () => {
    retryCount = 0;
    onStateChange(cameraId, 'playing');
  });

  hls.on(Hls.Events.MANIFEST_PARSED, () => {
    retryCount = 0;
    onStateChange(cameraId, 'playing');
  });
}
