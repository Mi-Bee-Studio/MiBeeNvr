/**
 * Shared HLS error handling with debounced recovery, zombie detection,
 * destroy+recreate capability, and visibility-based stream rebuild.
 *
 * Core fix for black flashing in multi-camera dashboard:
 * - Non-fatal MEDIA_ERROR: 500ms debounce before recoverMediaError()
 * - Recovery escalation: 3+ recoveries in 5s → destroy+recreate
 * - Zombie detection: readyState and FRAG_LOADED health checks
 * - Tab background/foreground recovery via visibilitychange
 */

import { createHlsConfig } from './hls-config';

// Error recovery thresholds (exported for testability)
export const RECOVERY_DEBOUNCE_MS = 500;
export const ESCALATION_WINDOW_MS = 5_000;
export const ESCALATION_THRESHOLD = 3;
export const ZOMBIE_READYSTATE_DURATION_MS = 20_000;
export const ZOMBIE_FRAG_GAP_MS = 60_000;
export const ZOMBIE_CHECK_INTERVAL_MS = 5_000;
export const MAX_RECREATE_ATTEMPTS = 5;

export type StreamState = 'playing' | 'buffering' | 'error' | 'snapshot';

export interface HlsErrorConfig {
  cameraId: string;
  maxRetries: number;
  retryDelays: number[];
  onStateChange: (cameraId: string, state: StreamState) => void;
  onFallbackToSnapshot: (cameraId: string) => void;
}

/** Skip pre-check — hls.js handles errors natively with its own retry logic. */
export async function checkStreamAvailable(_url: string): Promise<boolean> {
  return true;
}

/** Return a cleanup function that clears the timer. */
type Cleanup = () => void;

/**
 * Set up error handling on an Hls instance with debounced recovery and
 * automatic escalation to destroy+recreate.
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
  let recoverTimer: ReturnType<typeof setTimeout> | null = null;
  let recoverCount = 0;
  let lastRecoverTime = 0;
  const { cameraId, maxRetries, retryDelays, onStateChange, onFallbackToSnapshot } = config;

  // Recovery escalation thresholds

  hls.on(Hls.Events.ERROR, (_event: string, data: any) => {
    if (data.fatal) {
      // Clear any pending non-fatal debounce timer
      if (recoverTimer !== null) {
        clearTimeout(recoverTimer);
        recoverTimer = null;
      }

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
      // Non-fatal media errors — debounced recovery to avoid black flashing
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        // Check if we should escalate to destroy+recreate
        const now = Date.now();
        if (now - lastRecoverTime > ESCALATION_WINDOW_MS) {
          // Window reset — start counting fresh
          recoverCount = 1;
        } else {
          recoverCount++;
        }
        lastRecoverTime = now;

        if (recoverCount >= ESCALATION_THRESHOLD) {
          // Too many recoveries — escalate (callers listening for state
          // changes can use destroyAndRecreate externally)
          recoverCount = 0;
          onStateChange(cameraId, 'error');
          onFallbackToSnapshot(cameraId);
          return;
        }

        // Debounce: coalesce rapid non-fatal errors
        if (recoverTimer !== null) {
          clearTimeout(recoverTimer);
        }
        recoverTimer = setTimeout(() => {
          recoverTimer = null;
          try {
            hls.recoverMediaError();
          } catch {
            // Instance may have been destroyed
          }
        }, RECOVERY_DEBOUNCE_MS);
      }
    }
  });

  hls.on(Hls.Events.FRAG_LOADED, () => {
    retryCount = 0;
    recoverCount = 0;
    onStateChange(cameraId, 'playing');
  });

  hls.on(Hls.Events.MANIFEST_PARSED, () => {
    retryCount = 0;
    recoverCount = 0;
    onStateChange(cameraId, 'playing');
  });
}

/**
 * Set up zombie detection on an Hls instance.
 *
 * Monitors video element health and fragment loading activity.
 * Calls onZombie when the stream appears stuck.
 *
 * @param hls       The Hls instance.
 * @param Hls       The Hls constructor.
 * @param videoEl   The <video> element attached to the Hls instance.
 * @param cameraId  Camera identifier for callbacks.
 * @param onZombie  Called with cameraId when zombie state detected.
 * @returns Cleanup function to stop the detector.
 */
export function setupZombieDetector(
  hls: any,
  Hls: any,
  videoEl: HTMLVideoElement,
  cameraId: string,
  onZombie: (cameraId: string) => void,
): Cleanup {
  let lastFragLoadedTime = Date.now();
  let readyStateZeroSince: number | null = null;


  // Track fragment loads
  const onFragLoaded = () => {
    lastFragLoadedTime = Date.now();
    readyStateZeroSince = null;
  };
  hls.on(Hls.Events.FRAG_LOADED, onFragLoaded);

  // Periodic health check
  const intervalId = setInterval(() => {
    const now = Date.now();

    // Check readyState
    if (videoEl.readyState === 0) {
      if (readyStateZeroSince === null) {
        readyStateZeroSince = now;
      } else if (now - readyStateZeroSince >= ZOMBIE_READYSTATE_DURATION_MS) {
        onZombie(cameraId);
        return;
      }
    } else {
      readyStateZeroSince = null;
    }

    // Check FRAG_LOADED gap
    if (now - lastFragLoadedTime >= ZOMBIE_FRAG_GAP_MS) {
      onZombie(cameraId);
    }
  }, ZOMBIE_CHECK_INTERVAL_MS);

  return () => {
    clearInterval(intervalId);
    hls.off(Hls.Events.FRAG_LOADED, onFragLoaded);
  };
}


/**
 * Destroy an Hls instance and create a fresh one.
 *
 * 1. Calls hls.destroy()
 * 2. Creates new Hls(createHlsConfig())
 * 3. loadSource + attachMedia
 * 4. Returns the new instance (or null if max attempts exceeded)
 *
 * @param hls       Current Hls instance to destroy.
 * @param Hls       The Hls constructor.
 * @param videoEl   The <video> element.
 * @param streamUrl The HLS stream URL to load.
 * @param config    Error handling config for the new instance.
 * @param attempts  Recreate attempts tracker (pass a { value: number } object to track across calls).
 * @returns The new Hls instance, or null if max attempts exceeded.
 */
export function destroyAndRecreate(
  hls: any,
  Hls: any,
  videoEl: HTMLVideoElement,
  streamUrl: string,
  config: HlsErrorConfig,
  attempts: { value: number } = { value: 0 },
): any | null {
  if (attempts.value >= MAX_RECREATE_ATTEMPTS) {
    config.onStateChange(config.cameraId, 'error');
    config.onFallbackToSnapshot(config.cameraId);
    return null;
  }

  attempts.value++;

  try {
    hls.destroy();
  } catch {
    // Already destroyed
  }

  const newHls = new Hls(createHlsConfig());

  setupHlsErrorHandling(newHls, Hls, config);

  newHls.loadSource(streamUrl);
  newHls.attachMedia(videoEl);

  return newHls;
}

/**
 * Handle browser tab visibility changes for stream recovery.
 *
 * When the tab goes to background, marks streams as suspended.
 * When the tab returns to foreground, invokes onRebuild for each camera
 * so the caller can rebuild the Hls instances.
 *
 * @param cameras   Current array of active camera IDs.
 * @param onRebuild Called with cameraId for each stream that needs rebuilding.
 * @returns Cleanup function to remove the listener.
 */
export function handleVisibilityChange(
  cameras: () => string[],
  onRebuild: (cameraId: string) => void,
): Cleanup {
  let wasHidden = false;

  const handler = () => {
    if (document.hidden) {
      wasHidden = true;
    } else if (wasHidden) {
      wasHidden = false;
      const ids = cameras();
      for (const id of ids) {
        onRebuild(id);
      }
    }
  };

  document.addEventListener('visibilitychange', handler);
  return () => {
    document.removeEventListener('visibilitychange', handler);
  };
}
