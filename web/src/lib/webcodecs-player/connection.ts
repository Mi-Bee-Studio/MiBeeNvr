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
import type { CodecInfo, AudioCodecInfo, AudioFrame, QualityInfo } from './protocol';
import { decodeAudioCodecInfo, decodeAudioFrame, decodeQualityInfo } from './protocol';
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
  /** Optional callback when AudioCodecInfo message received */
  onAudioCodecInfo?: (info: AudioCodecInfo) => void;
  /**
   * Optional callback when QualityInfo message received (#541). Fired once
   * per connection (first in-band message) — reports 'main' when the server
   * fell back from a requested sub-stream, since the 101 upgrade response
   * cannot carry X-Stream-Quality.
   */
  onQualityInfo?: (info: QualityInfo) => void;
  /** Optional callback for each AudioFrame */
  onAudioFrame?: (frame: AudioFrame) => void;
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
/**
 * Max consecutive zombie-triggered reconnects with NO frame delivered between
 * them before we give up on this protocol and report offline (failed health).
 * 3 reconnects × (~6s zombie window) ≈ 18s of no frames → demote. This caps
 * the WS storm for a camera whose socket opens but never produces media.
 */
const MAX_ZOMBIE_RECONNECTS = 3;
/**
 * Max consecutive connect attempts whose socket CLOSES before reaching OPEN
 * (handshake rejected by server) before we give up and report offline. Caps the
 * "closed before connection is established" storm for cameras whose recorder
 * can't serve a WS stream. 5 failures ≈ a few seconds of coordinator-throttled
 * retries, then demote.
 */
const MAX_CONNECT_FAILURES = 5;
/**
 * Absolute wall-clock budget: if no frame has arrived within this many ms of
 * the CM's FIRST connect, force offline regardless of reconnect cycles.
 */
const NO_MEDIA_TOTAL_MS = 20000;

// ─── ConnectionManager ───────────────────────────────────────────────────

export class ConnectionManager {
  private _opts: ConnectionManagerOptions;
  private _ws: WebSocket | null = null;
  private _coordinatedTimer: ReturnType<typeof setTimeout> | null = null;
  private _coordinatedReconnectActive = false;
  private _destroyed = false;
  private _currentState: ConnectionState = 'disconnected';
  // True when the current socket was closed intentionally (disconnect/destroy/
  // reconnect-rotation). Used by onclose to suppress the auto-reconnect that
  // would otherwise fire on EVERY close — including the ones WE initiated.
  // Without this, navigating away or toggling tab visibility closes each
  // camera's WS, but `close()` without a code yields CloseEvent.code === 1005,
  // which is neither 1000 nor 1001, so onclose treated it as a crash and
  // rescheduled a reconnect onto a destroyed coordinator → "closed before
  // connection established" storm.
  private _intentionalClose = false;

  // Zombie detection
  private _zombieCheckTimer: ReturnType<typeof setInterval> | null = null;
  private _lastFrameTime = 0;
  private _zombieMissCount = 0;
  // Wall-clock guard: the epoch ms when this CM first connected. If no frame
  // has arrived within NO_MEDIA_TOTAL_MS of that timestamp (across ALL reconnect
  // cycles), force offline. Unlike the per-cycle zombie/handshake counters,
  // this can't be reset by a reconnect — it's the absolute time since the first
  // connect attempt. This is the reliable last-resort guard against cameras
  // whose WS opens but never produces media (the reconnect loop that the
  // zombie/handshake counters failed to stop reliably).
  private _firstConnectTime = 0;
  private _everReceivedFrame = false;
  // Count of consecutive zombie-triggered reconnects with NO frame delivered
  // between them. When this exceeds MAX_ZOMBIE_RECONNECTS we stop reconnecting
  // and report the camera offline (failed health), so the Player Orchestrator
  // can demote to the next protocol (e.g. wasm → hls). Without this cap a
  // camera whose WS connects but never delivers frames (H.265 on plain HTTP
  // where WebCodecs can't decode, or a recorder that accepted the WS but
  // produces no media) loops forever — the "closed before connection is
  // established" WS storm (thousands of log lines). Reset on any frame.
  private _zombieReconnectCount = 0;
  // Count of consecutive connect attempts whose socket CLOSED before ever
  // reaching OPEN (handshake rejected by the server — e.g. a camera whose
  // recorder doesn't speak the WS stream protocol, so the WHEP/WS handler
  // rejects/drops the upgrade). Without a cap this loops forever:
  // connect→handshake-fail→onclose(non-1000)→_scheduleCoordinatedReconnect→
  // connect→... — and crucially it bypasses the zombie detector (which only
  // runs after OPEN), so the zombieReconnectCount cap never trips. This is the
  // "WebSocket closed before the connection is established" storm (10k+/min).
  // Capped at MAX_CONNECT_FAILURES → report offline (failed) → orchestrator
  // demotes to the next protocol (e.g. wasm→hls). Reset on a successful OPEN.
  private _connectFailCount = 0;
  // Whether the current socket's onopen fired (handshake completed). Reset at
  // the start of each connect(); checked in onclose to distinguish a handshake
  // rejection (never opened) from a post-open drop (zombie/reconnect territory).
  private _socketOpened = false;

  // ─── Decode-stall detection ─────────────────────────────────────────────
  // Distinct from the zombie detector above: zombie keys on WS frame ARRIVAL
  // (_lastFrameTime), so it never trips when a long-GOP camera keeps sending
  // P-frames but the decoder never gets a keyframe and emits nothing. The
  // decoder itself fires the stall signal (worker 'decode-stall' →
  // handleDecoderStall). We cap consecutive stall-triggered reconnects just
  // like zombie reconnects; exceeding the cap reports offline so the orchestrator
  // can demote to another protocol (e.g. wasm → hls).
  private _decodeStallReconnectCount = 0;
  private static readonly MAX_DECODE_STALL_RECONNECTS = 3;

  // Backpressure
  private _paused = false;
  private _frameDropCount = 0;

  constructor(opts: ConnectionManagerOptions) {
    this._opts = {
      zombieCheckInterval: DEFAULT_ZOMBIE_CHECK_INTERVAL,
      zombieMaxMisses: DEFAULT_ZOMBIE_MAX_MISSES,
      ...opts,
    };
    // NOTE: visibility is NOT handled here. There used to be a _bindVisibility
    // that listened to document.visibilitychange directly — but that created a
    // THREE-way conflict with WasmPlayer's tabVisible $effect AND the Player
    // Orchestrator's setTabVisible, all firing on the same event, each closing
    // / reopening the WS → reactive loop → "closed before established" storm +
    // console freeze. Visibility is now owned solely by the orchestrator
    // (setTabVisible → route pauses/resumes players). Do not re-add it here.
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

    // Stamp the first-connect time for the wall-clock no-media guard.
    if (this._firstConnectTime === 0) this._firstConnectTime = Date.now();

    // Idempotent: if a socket is already open or mid-handshake, don't open a
    // second one. This is the defensive fix for the WS reconnect storm — even
    // if a caller invokes connect() repeatedly (e.g. from a reactive effect),
    // we never stack overlapping WebSockets. The previous socket is left alone
    // and its onclose/onopen handlers continue to drive state.
    if (this._ws && (this._ws.readyState === WebSocket.OPEN || this._ws.readyState === WebSocket.CONNECTING)) {
      return;
    }

    this._setState('loading');
    this._stopCoordinatedTimer();
    // A fresh connect means the previous socket (if any) is being superseded;
    // any onclose it fires is part of the rotation, not a crash to recover from.
    this._intentionalClose = false;
    this._socketOpened = false;

    let url = this._opts.url;
    if (this._opts.authToken) {
      url += `?token=${encodeURIComponent(this._opts.authToken)}`;
    }

    try {
      const socket = new WebSocket(url);
      socket.binaryType = 'arraybuffer';
      this._ws = socket;

      socket.onopen = () => {
        if (this._destroyed) {
          // We were destroyed while the handshake was in flight. The error
          // console spam ("closed before connection is established") comes from
          // closing here, but it's correct. The important part: if this connect
          // had consumed a coordinator reconnect slot, release it so queued
          // cameras aren't starved forever.
          if (this._opts.coordinator && this._opts.cameraId && this._coordinatedReconnectActive) {
            this._opts.coordinator.completeReconnect(this._opts.cameraId);
            this._coordinatedReconnectActive = false;
          }
          socket.close();
          return;
        }
        this._setState('buffering');
        this._startZombieDetection();
        this._socketOpened = true;
        // Handshake succeeded — this protocol's transport works at the WS layer.
        // Reset the handshake-failure counter (frames-not-arriving is the zombie
        // detector's job, tracked separately).
        this._connectFailCount = 0;
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
          // Even when paused (decoder still initializing / backpressure), the
          // fact that a VideoFrame arrived proves the WS stream is alive and
          // delivering media. Record it so the zombie/wall-clock guards don't
          // mistakenly demote a working stream just because the decoder hasn't
          // signaled codec-ready yet (libde265 WASM init can take seconds).
          this._recordFrameDelivery();
          // Backpressure: skip passing the frame to the decoder when paused
          if (this._paused) {
            this._frameDropCount++;
            this._opts.onFrameDrop?.(this._frameDropCount);
            return;
          }
          this._opts.onFrame(data);
          if (this._currentState !== 'playing') {
            this._setState('playing');
          }
        } else if (msgType === MsgType.AudioCodecInfo) {
          try {
            this._opts.onAudioCodecInfo?.(decodeAudioCodecInfo(data));
          } catch {
            // parse error — ignore
          }
        } else if (msgType === MsgType.QualityInfo) {
          try {
            this._opts.onQualityInfo?.(decodeQualityInfo(data));
          } catch {
            // parse error — ignore
          }
        } else if (msgType === MsgType.AudioFrame) {
          try {
            this._opts.onAudioFrame?.(decodeAudioFrame(data));
          } catch {
            // parse error — ignore
          }
        } else if (msgType === MsgType.EOS) {
          // Camera went offline — notify and set state
          this._stopZombieDetection();
          this._setState('offline');
          this._opts.onCameraOffline?.();
          // Close WS without triggering reconnect — server already did
          try {
            this._ws.close(1000);
          } catch {
            /* already closed */
          }
        }
      };

      socket.onclose = (event: CloseEvent) => {
        if (this._destroyed) return;
        this._stopZombieDetection();

        // We initiated this close (disconnect/reconnect-rotation/destroy). It is
        // NOT a failure to recover from — do not schedule a reconnect. This is
        // what stops the post-navigation / tab-toggle WS storm: closing a socket
        // without a code yields CloseEvent.code 1005 (no status), which is
        // neither 1000 nor 1001, so previously onclose treated it as a crash and
        // rescheduled a reconnect onto a destroyed coordinator.
        if (this._intentionalClose) {
          this._intentionalClose = false;
          if (this._currentState !== 'offline') {
            this._setState('disconnected');
          }
          return;
        }

        // Normal close (1000) or going away (1001) — don't reconnect
        if (event.code === 1000 || event.code === 1001) {
          // Preserve 'offline' state — don't overwrite with 'disconnected'
          if (this._currentState !== 'offline') {
            this._setState('disconnected');
          }
          return;
        }

        // Handshake-rejection storm guard: if this socket CLOSED before its
        // onopen ever fired (server rejected the WS upgrade — e.g. a camera
        // whose recorder can't serve the WS stream protocol), the loop below
        // (_scheduleCoordinatedReconnect → connect → handshake-fail → here)
        // would run forever and bypass the zombie detector (which only runs
        // after OPEN). Cap consecutive handshake failures; on exceed, report
        // offline so the orchestrator demotes to the next protocol.
        if (!this._socketOpened) {
          this._connectFailCount++;
          if (this._connectFailCount > MAX_CONNECT_FAILURES) {
            this._setState('offline');
            this._opts.onCameraOffline?.();
            return; // do NOT reconnect — let the orchestrator demote
          }
        }

        // Wall-clock no-media guard (the RELIABLE last resort): if we've been
        // trying to connect for NO_MEDIA_TOTAL_MS since the FIRST connect and
        // NOT A SINGLE frame has ever arrived, this protocol cannot serve media
        // for this camera. Stop reconnecting and report offline so the
        // orchestrator demotes. Unlike the zombie/handshake counters above,
        // this check is immune to reconnect-cycle resets — _firstConnectTime is
        // stamped once and _everReceivedFrame is never cleared by a reconnect.
        if (
          !this._everReceivedFrame &&
          this._firstConnectTime > 0 &&
          Date.now() - this._firstConnectTime > NO_MEDIA_TOTAL_MS
        ) {
          this._setState('offline');
          this._opts.onCameraOffline?.();
          return; // do NOT reconnect — let the orchestrator demote
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
    // Reset the wall-clock no-media guard so a fresh connect() session starts
    // clean (e.g. orchestrator reconnects after a mode switch).
    this._firstConnectTime = 0;
    this._everReceivedFrame = false;
    this._connectFailCount = 0;
    this._zombieReconnectCount = 0;
  }

  /** Manual reconnect — disconnects and reconnects. */
  reconnect(): void {
    this._stopCoordinatedTimer();
    this._cancelCoordinatorRequest();
    this._scheduleCoordinatedReconnect();
  }

  /**
   * Called when the decoder reports a stall (configured but no output for
   * DECODE_STALL_MS). This is the failure mode the zombie detector can't see:
   * the WS keeps delivering P-frames (so _lastFrameTime stays fresh and the
   * zombie never trips) yet the decoder never receives a keyframe and produces
   * nothing — UI stuck in "buffering". We reconnect (a fresh stream start is the
   * best chance to catch an IDR, especially now that the backend replays a
   * cached IDR to new subscribers) up to MAX_DECODE_STALL_RECONNECTS times;
   * beyond that we report offline so the orchestrator demotes to another
   * protocol.
   */
  handleDecoderStall(): void {
    if (this._destroyed) return;
    this._decodeStallReconnectCount++;
    if (this._decodeStallReconnectCount > ConnectionManager.MAX_DECODE_STALL_RECONNECTS) {
      // Exhausted stall-driven reconnects — give up on this protocol so the
      // orchestrator can demote (e.g. wasm/webcodecs → hls).
      this._setState('offline');
      this._opts.onCameraOffline?.();
      return;
    }
    // Reset the zombie cycle cap so this reconnect isn't blocked by the
    // zombie-reconnect limiter (these are independent failure signals).
    this._zombieReconnectCount = 0;
    this.reconnect();
  }

  /**
   * Reset the decode-stall reconnect counter. Called when the decoder produces
   * output (a decoded frame reaches the main thread), proving the pipeline
   * works — so a future stall starts the reconnect count fresh rather than
   * building on a stale tally from a previous bad stretch.
   */
  resetDecodeStallCount(): void {
    this._decodeStallReconnectCount = 0;
  }

  /** Full cleanup — no further operations possible. */
  destroy(): void {
    this._destroyed = true;
    this._stopCoordinatedTimer();
    this._cancelCoordinatorRequest();
    this._closeWebSocket();
    this._stopZombieDetection();
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
      // Mark this close as intentional so the socket's onclose handler does not
      // treat the resulting CloseEvent (code 1005 "no status", since we call
      // close() without a code) as a crash to reconnect from. Every call here is
      // an initiated teardown (disconnect / destroy / reconnect-rotation /
      // visibility-driven); the next connect()/reconnect path decides recovery.
      this._intentionalClose = true;
      try {
        this._ws.close();
      } catch {
        /* already closed */
      }
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
        // Zombie detected — reconnect via coordinator. But cap consecutive
        // frameless reconnects: if we've reconnected MAX_ZOMBIE_RECONNECTS
        // times without a single frame arriving, the protocol is unusable for
        // this camera (e.g. H.265 on plain HTTP where WebCodecs can't decode,
        // or a recorder whose WS opens but produces no media). Stop the cycle
        // and report offline so the orchestrator demotes to the next protocol.
        this._zombieMissCount = 0;

        // Wall-clock guard also applies here (OPEN + no frames).
        if (
          !this._everReceivedFrame &&
          this._firstConnectTime > 0 &&
          Date.now() - this._firstConnectTime > NO_MEDIA_TOTAL_MS
        ) {
          this._stopZombieDetection();
          this._setState('offline');
          this._opts.onCameraOffline?.();
          try {
            if (this._ws) this._ws.close(1000);
          } catch {
            /* closed */
          }
          return;
        }

        this._zombieReconnectCount++;
        if (this._zombieReconnectCount > MAX_ZOMBIE_RECONNECTS) {
          this._stopZombieDetection();
          this._setState('offline');
          this._opts.onCameraOffline?.();
          try {
            if (this._ws) this._ws.close(1000);
          } catch {
            /* already closed */
          }
          return;
        }
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
    this._everReceivedFrame = true;
    // A frame arrived — this protocol works. Reset the frameless-reconnect cap.
    this._zombieReconnectCount = 0;
  }

  // ─── Internal: Visibility ────────────────────────────────────────────
  // REMOVED: ConnectionManager no longer binds its own visibilitychange
  // listener. Visibility pause/resume is owned by the Player Orchestrator
  // (setTabVisible) to avoid the three-way conflict that caused the WS storm.
  // See the constructor note for the full rationale.
}

// ─── Inline CodecInfo decoder (avoids circular import, reuses protocol format) ───

function decodeCodecInfoInline(data: ArrayBuffer): CodecInfo {
  if (data.byteLength < 2) {
    throw new Error(`CodecInfo too short: ${data.byteLength} bytes`);
  }

  const dv = new DataView(data);
  if (dv.getUint8(0) !== MsgType.CodecInfo) {
    throw new Error(`Expected msg type 0x01, got 0x${dv.getUint8(0).toString(16)}`);
  }

  const codecByte = dv.getUint8(1);
  // MJPEG: only type + codec byte, no SPS/PPS/VPS.
  if (codecByte === 6) {
    return { codec: 'mjpeg', profile: 0, level: 0, sps: new Uint8Array(0), pps: new Uint8Array(0) };
  }

  if (data.byteLength < 5) {
    throw new Error(`CodecInfo too short: ${data.byteLength} bytes`);
  }

  const codec = codecByte === 5 ? 'h265' : 'h264';
  const profile = dv.getUint8(2);
  const level = dv.getUint8(3);

  let off = 4;

  const spsLen = dv.getUint16(off);
  off += 2;
  if (off + spsLen > data.byteLength) throw new Error('CodecInfo truncated at SPS');
  const sps = new Uint8Array(data, off, spsLen);
  off += spsLen;

  const ppsLen = dv.getUint16(off);
  off += 2;
  if (off + ppsLen > data.byteLength) throw new Error('CodecInfo truncated at PPS');
  const pps = new Uint8Array(data, off, ppsLen);
  off += ppsLen;

  let vps: Uint8Array | undefined;
  if (codec === 'h265') {
    const vpsLen = dv.getUint16(off);
    off += 2;
    if (off + vpsLen > data.byteLength) throw new Error('CodecInfo truncated at VPS');
    vps = new Uint8Array(data, off, vpsLen);
    off += vpsLen;
  }

  return { codec, profile, level, sps, pps, vps };
}
