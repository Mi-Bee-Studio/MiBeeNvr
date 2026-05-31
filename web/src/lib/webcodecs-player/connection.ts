/**
 * Reusable WebSocket connection manager with exponential backoff reconnect,
 * zombie detection, and state management.
 *
 * Designed for WebCodecs Player but generic enough for other streaming players.
 *
 * Features:
 *   - Exponential backoff reconnect: [2, 4, 8, 16, 32]s, capped at 32s
 *   - Zombie detection: monitors frame delivery rate, auto-reconnects on stall
 *   - Visibility change handler: reconnects when tab returns to foreground
 *   - Freeze-frame callback: allows caller to capture canvas before reconnect
 *   - Clean lifecycle: connect → disconnect → reconnect → destroy
 */

import { MsgType } from './protocol';
import type { CodecInfo } from './protocol';

// ─── Types ───────────────────────────────────────────────────────────────

export type ConnectionState = 'loading' | 'buffering' | 'playing' | 'error' | 'disconnected';

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
  /** Max reconnect attempts before giving up (default: 5) */
  maxReconnectAttempts?: number;
  /** Exponential backoff delays in ms (default: [2, 4, 8, 16, 32] seconds) */
  reconnectDelays?: number[];
  /** Zombie check interval in ms (default: 2000) */
  zombieCheckInterval?: number;
  /** Number of consecutive zombie checks before reconnect (default: 3) */
  zombieMaxMisses?: number;
}

// ─── Defaults ─────────────────────────────────────────────────────────────

const DEFAULT_RECONNECT_DELAYS = [2000, 4000, 8000, 16000, 32000];
const DEFAULT_MAX_RECONNECT_ATTEMPTS = 5;
const DEFAULT_ZOMBIE_CHECK_INTERVAL = 2000;
const DEFAULT_ZOMBIE_MAX_MISSES = 3;

// ─── ConnectionManager ───────────────────────────────────────────────────

export class ConnectionManager {
  private _opts: ConnectionManagerOptions;
  private _ws: WebSocket | null = null;
  private _reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private _reconnectAttempts = 0;
  private _destroyed = false;
  private _currentState: ConnectionState = 'disconnected';

  // Zombie detection
  private _zombieCheckTimer: ReturnType<typeof setInterval> | null = null;
  private _lastFrameTime = 0;
  private _zombieMissCount = 0;

  // Visibility
  private _wasHidden = false;
  private _visibilityHandler: (() => void) | null = null;

  constructor(opts: ConnectionManagerOptions) {
    this._opts = {
      maxReconnectAttempts: DEFAULT_MAX_RECONNECT_ATTEMPTS,
      reconnectDelays: DEFAULT_RECONNECT_DELAYS,
      zombieCheckInterval: DEFAULT_ZOMBIE_CHECK_INTERVAL,
      zombieMaxMisses: DEFAULT_ZOMBIE_MAX_MISSES,
      ...opts,
    };
    this._bindVisibility();
  }

  // ─── Public API ────────────────────────────────────────────────────────

  /** Open WebSocket connection. */
  connect(): void {
    if (this._destroyed) return;
    if (!this._opts.url) return;

    this._setState('loading');
    this._stopReconnectTimer();

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
        this._reconnectAttempts = 0;
        this._startZombieDetection();
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
          this._recordFrameDelivery();
          this._opts.onFrame(data);
          if (this._currentState !== 'playing') {
            this._setState('playing');
          }
        }
      };

      socket.onclose = (event: CloseEvent) => {
        if (this._destroyed) return;
        this._stopZombieDetection();

        // Normal close (1000) or going away (1001) — don't reconnect
        if (event.code === 1000 || event.code === 1001) {
          this._setState('disconnected');
          return;
        }

        this._opts.onFreezeFrame();
        this._scheduleReconnect();
      };

      socket.onerror = () => {
        if (this._destroyed) return;
        // Don't schedule reconnect here — onclose always follows onerror
        // and handles reconnect scheduling. Setting error state is enough.
        this._setState('error');
      };
    } catch {
      this._setState('error');
      this._scheduleReconnect();
    }
  }

  /** Close WebSocket and cancel reconnect timer (but don't destroy). */
  disconnect(): void {
    this._stopReconnectTimer();
    this._closeWebSocket();
    this._stopZombieDetection();
  }

  /** Manual reconnect — resets backoff, disconnects, and reconnects. */
  reconnect(): void {
    this._reconnectAttempts = 0;
    this._opts.onFreezeFrame();
    this._stopReconnectTimer();
    this._closeWebSocket();
    this._stopZombieDetection();
    this.connect();
  }

  /** Full cleanup — no further operations possible. */
  destroy(): void {
    this._destroyed = true;
    this._stopReconnectTimer();
    this._closeWebSocket();
    this._stopZombieDetection();
    this._unbindVisibility();
  }

  // ─── Internal: State ─────────────────────────────────────────────────

  private _setState(state: ConnectionState): void {
    this._currentState = state;
    this._opts.onStateChange(state);
  }

  // ─── Internal: Reconnect ─────────────────────────────────────────────

  private _getBackoffDelay(): number {
    const delays = this._opts.reconnectDelays!;
    if (this._reconnectAttempts >= delays.length) {
      return delays[delays.length - 1];
    }
    return delays[this._reconnectAttempts];
  }

  private _scheduleReconnect(): void {
    if (this._reconnectAttempts >= this._opts.maxReconnectAttempts!) {
      this._setState('error');
      return;
    }
    this._stopReconnectTimer();
    const delay = this._getBackoffDelay();
    this._reconnectAttempts++;
    this._reconnectTimer = setTimeout(() => {
      this._reconnectTimer = null;
      this._closeWebSocket();
      this.connect();
    }, delay);
  }

  private _stopReconnectTimer(): void {
    if (this._reconnectTimer !== null) {
      clearTimeout(this._reconnectTimer);
      this._reconnectTimer = null;
    }
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
        // Zombie detected — reconnect
        this._zombieMissCount = 0;
        this._opts.onFreezeFrame();
        this._stopReconnectTimer();
        this._closeWebSocket();
        this._stopZombieDetection();
        this.connect();
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
        this._reconnectAttempts = 0;
        this._opts.onFreezeFrame();
        this._stopReconnectTimer();
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
