/**
 * Unified player health reporting.
 *
 * Every player adapter reports its wellbeing to the PlayerOrchestrator via a
 * normalized {@link HealthState}. The orchestrator uses these signals — not the
 * transport-specific error codes each player library emits — to make
 * cross-protocol decisions: degrade on failure, attempt upgrade after stability.
 *
 * Design note: this layer intentionally knows nothing about WebSocket close
 * codes, hls.js error types, or WebRTC ICE states. Adapters translate those
 * transport-specific events into one of the three {@link HealthStatus} buckets
 * and a coarse {@link HealthReason}. Keeping the vocabulary small lets the
 * orchestrator's decision logic stay protocol-agnostic and unit-testable.
 */

/** Coarse-grained wellbeing of a single camera's active player. */
export type HealthStatus = 'ok' | 'degraded' | 'failed';

/**
 * Why the player is in its current state. The set is deliberately small —
 * adapters collapse the dozens of transport-specific error codes into these.
 */
export type HealthReason =
  | 'no-frames' // stream open but no frames arriving (zombie)
  | 'decode-errors' // decoder throwing / rejecting frames
  | 'buffering' // rebuffering / stalling
  | 'reconnecting' // transport dropped, retrying
  | 'codec-unsupported' // codec can't be decoded in this browser
  | 'transport-refused' // server/transport rejected the connection
  | 'fatal-error'; // unrecoverable, give up this protocol

/**
 * A normalized health snapshot reported by a player adapter.
 *
 * `since` is the epoch millis at which the current `status` began — the
 * orchestrator uses it for debounce windows (degrade-after-Ns-degraded,
 * upgrade-after-Ms-ok). `metrics` is optional telemetry for the UI; the
 * orchestrator does not depend on it for decisions.
 */
export interface HealthState {
  status: HealthStatus;
  reason?: HealthReason;
  /** Epoch millis when this status began. 0 means "unknown/now". */
  since: number;
  /** Optional runtime metrics surfaced to the UI only. */
  metrics?: { fps?: number; latencyMs?: number };
}

/** A healthy player with no qualifying reason. */
export const OK: HealthState = { status: 'ok', since: 0 };

/**
 * Helper to build a non-OK state stamped at the current time when `since` is
 * omitted. Adapters call this instead of building the object inline so the
 * timestamp is always consistent with `Date.now()`.
 */
export function health(
  status: HealthStatus,
  reason?: HealthReason,
  since: number = Date.now(),
  metrics?: HealthState['metrics'],
): HealthState {
  return { status, reason, since, metrics };
}

/** True when a health state represents a condition the orchestrator must act on. */
export function isActionable(h: HealthState): boolean {
  return h.status === 'failed' || h.status === 'degraded';
}

// ─── Player signal → HealthState bridges ────────────────────────────────────

/**
 * Map a player's internal stream-state string (VideoPlayer/FlvPlayer/WebRTC use
 * the `StreamState` type: 'playing' | 'buffering' | 'error' | 'snapshot', plus
 * 'loading' as an initial state) to a normalized {@link HealthState} for the
 * orchestrator. Kept here so every player maps identically — no per-component
 * translation logic drifts.
 *
 *   loading   → degraded (reconnecting) — orchestrator tolerates startup time
 *   buffering → degraded (buffering)
 *   playing   → ok
 *   error     → failed (fatal-error) — triggers immediate demote
 *   snapshot  → failed (terminal — player gave up on real-time)
 *
 * `since` is stamped to now on each call so the orchestrator's debounce window
 * measures from the moment the player entered this state.
 */
export function healthFromStreamState(state: string): HealthState {
  switch (state) {
    case 'playing':
      return { status: 'ok', since: Date.now() };
    case 'buffering':
      return health('degraded', 'buffering');
    case 'loading':
      return health('degraded', 'reconnecting');
    case 'error':
      return health('failed', 'fatal-error');
    case 'snapshot':
      return health('failed', 'fatal-error');
    default:
      return health('degraded', 'no-frames');
  }
}

/**
 * Map WasmPlayer's connection state (the `ConnectionState` type from
 * connection.ts: 'loading' | 'buffering' | 'playing' | 'error' |
 * 'disconnected' | 'offline') to a normalized {@link HealthState}.
 *
 *   playing       → ok
 *   buffering     → degraded (buffering)
 *   loading       → degraded (reconnecting) — startup / handshake
 *   disconnected  → degraded (reconnecting) — transient drop, auto-retry armed
 *   offline       → failed (EOS — camera went offline; demote or snapshot)
 *   error         → failed (fatal-error)
 */
export function healthFromConnectionState(state: string): HealthState {
  switch (state) {
    case 'playing':
      return { status: 'ok', since: Date.now() };
    case 'buffering':
      return health('degraded', 'buffering');
    case 'loading':
    case 'disconnected':
      return health('degraded', 'reconnecting');
    case 'offline':
      // Camera EOS — treat as failed so the orchestrator can demote / snapshot.
      return health('failed', 'fatal-error');
    case 'error':
      return health('failed', 'fatal-error');
    default:
      return health('degraded', 'no-frames');
  }
}
