/**
 * Reusable WebSocket connection manager with zombie detection,
 * visibility handling, and coordinated reconnection via ReconnectCoordinator.
 *
 * Designed for WebCodecs Player but generic enough for other streaming players.
 *
 * Features:
 *   - Coordinated reconnect via ReconnectCoordinator (thundering herd prevention)
 *   - Zombie detection: monitors frame delivery rate, auto-reconnects on stall
 *   - Visibility change handler: reconnects when tab returns to foreground
 *   - Freeze-frame callback: allows caller to capture canvas before reconnect
 *   - Clean lifecycle: connect → disconnect → reconnect → destroy
 */

import { MsgType } from './protocol';
import type { CodecInfo } from './protocol';
import type { ReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';

// ─── Types ───────────────────────────────────────────────────────────────

export type ConnectionState = 'loading' | 'buffering' | 'playing' | 'error' | 'disconnected' | 'offline';

export interface ConnectionManagerOptions {
  /** WebSocket URL (without auth token) */
  url: string;
  /** Optional BasicAuth token to append as ?token= query param */
  authToken?: string;
  /** Callback for state transitions */
  onStateChange: (state: ConnectionState) => void;
  /** Callback when CodecInfo message received */
  onCodecInfo: (ci: CodecInfo) => void;
  /** Callback for each VideoFrame ArrayBuffer */
  onFrame: (data: ArrayBuffer) => void;
  /** Callback before reconnect (allows caller to capture freeze frame) */
  onFreezeFrame: () => void;
  /** Optional callback when frames are dropped due to backpressure */
  onFrameDrop?: (count: number) => void;
  /** Optional callback when camera goes offline (EOS received) */
  onCameraOffline?: () => void;
  /** Optional reconnect coordinator for thundering herd prevention */
  coordinator?: ReconnectCoordinator;
  /** Camera ID (required when coordinator is provided) */
  cameraId?: string;
  /** Zombie check interval in ms (default: 2000) */
  zombieCheckInterval?: number;
  /** Number of consecutive zombie checks before reconnect (default: 3) */
  zombieMaxMisses?: number;
}

// ─── Defaults ─────────────────────────────────────────────────────────────

const DEFAULT_ZOMBIE_CHECK_INTERVAL = 2000;
const DEFAULT_ZOMBIE_MAX_MISSES = 3;

// ─── ConnectionManager ───────────────────────────────────────────────────

export class ConnectionManager {
  private _opts: ConnectionManagerOptions;
  private _ws: WebSocket | null = null;
  private _coordinatedTimer: ReturnType<typeof setTimeout> | null = null;
  private _coordinatedReconnectActive = false;
  private _destroyed = false;
  private _currentState: ConnectionState = 'disconnected';

  // Zombie detection
  private _zombieCheckTimer: ReturnType<typeof setInterval> | null = null;
  private _lastFrameTime = 0;
  private _zombieMissCount = 0;

  // Visibility
  private _wasHidden = false;
  private _visibilityHandler: (() => void) | null = null;
  // Backpressure
  private _paused = false;
  private _frameDropCount = 0;

  // Visibility

  constructor(opts: ConnectionManagerOptions) {
    this._opts = {
      zombieCheckInterval: DEFAULT_ZOMBIE_CHECK_INTERVAL,
      zombieMaxMisses: DEFAULT_ZOMBIE_MAX_MISSES,
      ...opts,
    };
    this._bindVisibility();
  }

  // ─── Public API ────────────────────────────────────────────────────────


  /** Whether incoming frames are being skipped due to backpressure. */
  get paused(): boolean {
    return this._paused;
  }

  /** Total frames dropped due to backpressure at the connection level. */
  get frameDropCount(): number {
    return this._frameDropCount;
  }

  /**
   * Set backpressure pause state.
   * When paused, incoming video frames are discarded without passing to onFrame.
   */
  setPaused(paused: boolean): void {
    this._paused = paused;
  }

  /** Open WebSocket connection. */
  connect(): void {
    if (this._destroyed) return;
    if (!this._opts.url) return;

    this._setState('loading');
    this._stopCoordinatedTimer();

    let url = this._opts.url;
    if (this._opts.authToken) {
      url += `?token=${encodeURIComponent(this._opts.authToken)}`;
    }

    try {
      const socket = new WebSocket(url);
      socket.binaryType = 'arraybuffer';
      this._ws = socket;

      socket.onopen = () => {
        if (this._destroyed) { socket.close(); return; }
        this._setState('buffering');
        this._startZombieDetection();
        // Notify coordinator that reconnect succeeded
        if (this._opts.coordinator && this._opts.cameraId && this._coordinatedReconnectActive) {
          this._opts.coordinator.completeReconnect(this._opts.cameraId);
          this._coordinatedReconnectActive = false;
        }
      };

      socket.onmessage = (event: MessageEvent) => {
        if (this._destroyed || !this._ws) return;

        if (!(event.data instanceof ArrayBuffer)) return;
        const data = event.data as ArrayBuffer;
        if (data.byteLength < 1) return;

        const msgType = new Uint8Array(data)[0];

        if (msgType === MsgType.CodecInfo) {
          try {
            this._opts.onCodecInfo(decodeCodecInfoInline(data));
          } catch {
            // parse error — ignore
          }
        } else if (msgType === MsgType.VideoFrame) {
          // Backpressure: skip incoming frames when decoder is overloaded
          if (this._paused) {
            this._frameDropCount++;
            this._opts.onFrameDrop?.(this._frameDropCount);
            return;
          }
          this._recordFrameDelivery();
          this._opts.onFrame(data);
          if (this._currentState !== 'playing') {
            this._setState('playing');
          }
        } else if (msgType === MsgType.EOS) {
          // Camera went offline — notify and set state
          this._stopZombieDetection();
          this._setState('offline');
          this._opts.onCameraOffline?.();
          // Close WS without triggering reconnect — server already did
          try { this._ws.close(1000); } catch { /* already closed */ }
        }
      };

      socket.onclose = (event: CloseEvent) => {
        if (this._destroyed) return;
        this._stopZombieDetection();

        // Normal close (1000) or going away (1001) — don't reconnect
        if (event.code === 1000 || event.code === 1001) {
          // Preserve 'offline' state — don't overwrite with 'disconnected'
          if (this._currentState !== 'offline') {
            this._setState('disconnected');
          }
          return;
        }

        this._scheduleCoordinatedReconnect();
      };

      socket.onerror = () => {
        if (this._destroyed) return;
        // Don't schedule reconnect here — onclose always follows onerror
        // and handles reconnect scheduling. Setting error state is enough.
        this._setState('error');
      };
    } catch {
      this._setState('error');
    }
  }

  /** Close WebSocket and cancel reconnect timer (but don't destroy). */
  disconnect(): void {
    this._stopCoordinatedTimer();
    this._cancelCoordinatorRequest();
    this._closeWebSocket();
    this._stopZombieDetection();
    this._paused = false;
  }

  /** Manual reconnect — disconnects and reconnects. */
  reconnect(): void {
    this._stopCoordinatedTimer();
    this._cancelCoordinatorRequest();
    this._scheduleCoordinatedReconnect();
  }

  /** Full cleanup — no further operations possible. */
  destroy(): void {
    this._destroyed = true;
    this._stopCoordinatedTimer();
    this._cancelCoordinatorRequest();
    this._closeWebSocket();
    this._stopZombieDetection();
    this._unbindVisibility();
    this._paused = false;
  }

  // ─── Internal: State ─────────────────────────────────────────────────

  private _setState(state: ConnectionState): void {
    this._currentState = state;
    this._opts.onStateChange(state);
  }

  // ─── Internal: Coordinated reconnect ─────────────────────────────────

  private _scheduleCoordinatedReconnect(): void {
    if (this._destroyed) return;
    this._opts.onFreezeFrame();
    this._closeWebSocket();
    this._stopZombieDetection();

    if (this._opts.coordinator && this._opts.cameraId) {
      const coordinator = this._opts.coordinator;
      const cameraId = this._opts.cameraId;
      this._coordinatedReconnectActive = true;

      const delay = coordinator.requestReconnect(cameraId, (grantedDelay) => {
        this._coordinatedTimer = setTimeout(() => {
          this._coordinatedTimer = null;
          this.connect();
        }, grantedDelay);
      });

      if (delay >= 0) {
        this._coordinatedTimer = setTimeout(() => {
          this._coordinatedTimer = null;
          this.connect();
        }, delay);
      }
      // If -1, queued — callback will fire when slot opens
    } else {
      // No coordinator — immediate reconnect
      this.connect();
    }
  }

  private _stopCoordinatedTimer(): void {
    if (this._coordinatedTimer !== null) {
      clearTimeout(this._coordinatedTimer);
      this._coordinatedTimer = null;
    }
  }

  private _cancelCoordinatorRequest(): void {
    if (this._opts.coordinator && this._opts.cameraId) {
      this._opts.coordinator.cancelRequest(this._opts.cameraId);
    }
    this._coordinatedReconnectActive = false;
  }

  private _closeWebSocket(): void {
    if (this._ws) {
      try { this._ws.close(); } catch { /* already closed */ }
      this._ws = null;
    }
  }

  // ─── Internal: Zombie detection ──────────────────────────────────────

  private _startZombieDetection(): void {
    this._stopZombieDetection();
    this._lastFrameTime = Date.now();
    this._zombieMissCount = 0;

    this._zombieCheckTimer = setInterval(() => {
      if (this._destroyed || !this._ws) return;
      if (this._ws.readyState !== WebSocket.OPEN) return;

      const now = Date.now();
      if (now - this._lastFrameTime >= this._opts.zombieCheckInterval!) {
        this._zombieMissCount++;
      } else {
        this._zombieMissCount = 0;
      }

      if (this._zombieMissCount >= this._opts.zombieMaxMisses!) {
        // Zombie detected — reconnect via coordinator
        this._zombieMissCount = 0;
        this._scheduleCoordinatedReconnect();
      }
    }, this._opts.zombieCheckInterval!);
  }

  private _stopZombieDetection(): void {
    if (this._zombieCheckTimer !== null) {
      clearInterval(this._zombieCheckTimer);
      this._zombieCheckTimer = null;
    }
    this._zombieMissCount = 0;
  }

  private _recordFrameDelivery(): void {
    this._lastFrameTime = Date.now();
    this._zombieMissCount = 0;
  }

  // ─── Internal: Visibility ────────────────────────────────────────────

  private _bindVisibility(): void {
    this._visibilityHandler = () => {
      if (this._destroyed) return;

      if (document.hidden) {
        this._wasHidden = true;
      } else if (this._wasHidden) {
        this._wasHidden = false;
        this._opts.onFreezeFrame();
        this._stopCoordinatedTimer();
        this._closeWebSocket();
        this._stopZombieDetection();
        this.connect();
      }
    };
    document.addEventListener('visibilitychange', this._visibilityHandler);
  }

  private _unbindVisibility(): void {
    if (this._visibilityHandler) {
      document.removeEventListener('visibilitychange', this._visibilityHandler);
      this._visibilityHandler = null;
    }
  }
}

// ─── Inline CodecInfo decoder (avoids circular import, reuses protocol format) ───

function decodeCodecInfoInline(data: ArrayBuffer): CodecInfo {
  if (data.byteLength < 5) {
    throw new Error(`CodecInfo too short: ${data.byteLength} bytes`);
  }

  const dv = new DataView(data);
  if (dv.getUint8(0) !== MsgType.CodecInfo) {
    throw new Error(`Expected msg type 0x01, got 0x${dv.getUint8(0).toString(16)}`);
  }

  const codecByte = dv.getUint8(1);
  const codec = codecByte === 5 ? 'h265' : 'h264';
  const profile = dv.getUint8(2);
  const level = dv.getUint8(3);

  let off = 4;

  const spsLen = dv.getUint16(off); off += 2;
  if (off + spsLen > data.byteLength) throw new Error('CodecInfo truncated at SPS');
  const sps = new Uint8Array(data, off, spsLen); off += spsLen;

  const ppsLen = dv.getUint16(off); off += 2;
  if (off + ppsLen > data.byteLength) throw new Error('CodecInfo truncated at PPS');
  const pps = new Uint8Array(data, off, ppsLen); off += ppsLen;

  let vps: Uint8Array | undefined;
  if (codec === 'h265') {
    const vpsLen = dv.getUint16(off); off += 2;
    if (off + vpsLen > data.byteLength) throw new Error('CodecInfo truncated at VPS');
    vps = new Uint8Array(data, off, vpsLen); off += vpsLen;
  }

  return { codec, profile, level, sps, pps, vps };
}
