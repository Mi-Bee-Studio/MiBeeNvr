/**
 * Reconnection coordinator — prevents thundering herd when multiple cameras
 * lose connection simultaneously.
 *
 * - Limits concurrent reconnection attempts to `maxConcurrent` (default 2)
 * - Applies global exponential backoff (starts at 1s, doubles each round, caps at 30s)
 * - Detects backend pressure (HTTP 503) and triggers 10s global cooldown
 * - Queues excess reconnect requests and grants them FIFO when slots open
 */

/** Maximum concurrent reconnection attempts across all players. */
const MAX_CONCURRENT = 2;

/** Initial backoff delay in milliseconds. */
const INITIAL_BACKOFF_MS = 1000;

/** Maximum backoff delay in milliseconds. */
const MAX_BACKOFF_MS = 30000;

/** Duration of global cooldown after backend pressure detection (503). */
const COOLDOWN_DURATION_MS = 10000;

export interface ReconnectCoordinator {
  /** Number of currently active reconnection attempts. */
  readonly activeReconnects: number;
  /** Maximum allowed concurrent reconnection attempts. */
  readonly maxConcurrent: number;
  /** Current global backoff delay in ms. Starts at 1000, doubles each round, caps at 30000. */
  readonly globalBackoffMs: number;
  /** Whether a global cooldown is active (triggered by HTTP 503). */
  readonly globalCooldown: boolean;

  /**
   * Request permission to reconnect.
   *
   * @param cameraId - Unique camera identifier
   * @param onGranted - Callback fired when a reconnect slot is granted (for queued requests).
   *                     Receives the delay in ms to wait before reconnecting.
   * @returns Delay in ms to wait before reconnecting (>= 0), or -1 if queued.
   */
  requestReconnect(cameraId: string, onGranted: (delayMs: number) => void): number;

  /**
   * Report that a reconnection attempt completed (stream resumed or failed permanently).
   * Frees a slot for queued cameras.
   */
  completeReconnect(cameraId: string): void;

  /**
   * Cancel a pending reconnect request. Call when the player component is destroyed
   * or the stream recovers before the coordinator grants a slot.
   */
  cancelRequest(cameraId: string): void;

  /**
   * Signal that the backend returned HTTP 503 (Service Unavailable).
   * Triggers a 10s global cooldown and doubles the backoff.
   */
  reportBackendPressure(): void;

  /** Clean up all timers and pending requests. Call on Dashboard unmount. */
  dispose(): void;
}

/**
 * Create a new reconnection coordinator instance.
 * Uses Svelte 5 $state for reactive state tracking.
 */
export function createReconnectCoordinator(): ReconnectCoordinator {
  let activeReconnects = $state(0);
  let globalBackoffMs = $state(INITIAL_BACKOFF_MS);
  let globalCooldown = $state(false);

  const pendingQueue: string[] = [];
  const pendingCallbacks = new Map<string, (delayMs: number) => void>();
  let cooldownTimer: ReturnType<typeof setTimeout> | null = null;

  function processQueue(): void {
    while (
      pendingQueue.length > 0 &&
      activeReconnects < MAX_CONCURRENT &&
      !globalCooldown
    ) {
      const nextId = pendingQueue.shift()!;
      const cb = pendingCallbacks.get(nextId);
      pendingCallbacks.delete(nextId);
      activeReconnects++;

      const delay = globalBackoffMs;
      // Increase backoff for next round
      globalBackoffMs = Math.min(MAX_BACKOFF_MS, globalBackoffMs * 2);

      if (cb) {
        // Use setTimeout to avoid deep stack from synchronous callback chains
        setTimeout(() => cb(delay), 0);
      }
    }
  }

  return {
    get activeReconnects() {
      return activeReconnects;
    },
    get maxConcurrent() {
      return MAX_CONCURRENT;
    },
    get globalBackoffMs() {
      return globalBackoffMs;
    },
    get globalCooldown() {
      return globalCooldown;
    },

    requestReconnect(cameraId: string, onGranted: (delayMs: number) => void): number {
      // During global cooldown — queue it
      if (globalCooldown) {
        if (!pendingQueue.includes(cameraId)) {
          pendingQueue.push(cameraId);
        }
        pendingCallbacks.set(cameraId, onGranted);
        return -1;
      }

      // At capacity — queue it
      if (activeReconnects >= MAX_CONCURRENT) {
        if (!pendingQueue.includes(cameraId)) {
          pendingQueue.push(cameraId);
        }
        pendingCallbacks.set(cameraId, onGranted);
        return -1;
      }

      // Grant slot immediately
      activeReconnects++;
      return globalBackoffMs;
    },

    completeReconnect(_cameraId: string): void {
      if (activeReconnects > 0) {
        activeReconnects--;
      }
      // Partial backoff reset — reduce by 25% on success (don't fully reset)
      globalBackoffMs = Math.max(
        INITIAL_BACKOFF_MS,
        Math.floor(globalBackoffMs * 0.75),
      );
      processQueue();
    },

    cancelRequest(cameraId: string): void {
      const idx = pendingQueue.indexOf(cameraId);
      if (idx !== -1) {
        pendingQueue.splice(idx, 1);
      }
      pendingCallbacks.delete(cameraId);
    },

    reportBackendPressure(): void {
      globalCooldown = true;
      // Double backoff on backend pressure
      globalBackoffMs = Math.min(MAX_BACKOFF_MS, globalBackoffMs * 2);

      if (cooldownTimer) clearTimeout(cooldownTimer);
      cooldownTimer = setTimeout(() => {
        globalCooldown = false;
        cooldownTimer = null;
        processQueue();
      }, COOLDOWN_DURATION_MS);
    },

    dispose(): void {
      if (cooldownTimer) {
        clearTimeout(cooldownTimer);
        cooldownTimer = null;
      }
      pendingQueue.length = 0;
      pendingCallbacks.clear();
      activeReconnects = 0;
    },
  };
}
