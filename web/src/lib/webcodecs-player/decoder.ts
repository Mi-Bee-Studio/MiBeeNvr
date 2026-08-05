/**
 * Video Decoder with WebCodecs + WASM H.265 fallback.
 *
 * Primary: WebCodecs VideoDecoder (H.264, H.265 on supported browsers).
 * Fallback: libde265 WASM decoder (H.265 on Chromium where WebCodecs
 * doesn't support HEVC).
 *
 * Handles codec configuration, NALU processing, error recovery,
 * and ensures VideoFrame.close() is always called to prevent GPU memory leaks.
 *
 * Designed to run inside a Web Worker — no DOM dependencies.
 */

import type { CodecInfo } from './protocol';
import { WasmH265Decoder } from './wasm-h265-decoder';

// ─── Constants ────────────────────────────────────────────────────────────

const START_CODE = new Uint8Array([0x00, 0x00, 0x00, 0x01]);
const DEFAULT_WIDTH = 1920;
const DEFAULT_HEIGHT = 1080;
const FALLBACK_H264_CODEC = 'avc1.42001E'; // Baseline L3.0
const FALLBACK_H265_CODEC = 'hvc1.1.6.L93.B0'; // Main L3.1
const BACKPRESSURE_THRESHOLD = 5;

// DECODE_STALL_MS: if the decoder is configured but produces zero output frames
// within this many milliseconds, it is "stalled" and the onStall callback fires.
// This catches the long-GOP failure mode where the WS keeps delivering P-frames
// (so the connection-layer zombie detector, which keys on frame ARRIVAL, never
// trips) but the decoder never gets a keyframe and emits nothing — leaving the
// UI stuck in "buffering" forever. 15s comfortably exceeds one 8s GOP plus init
// latency, so it won't false-fire on slow-starting streams.
const DECODE_STALL_MS = 15000;

// ─── Codec string builders ────────────────────────────────────────────────

/**
 * Build an H.264 codec string from SPS NAL unit data.
 *
 * Format: `avc1.{PPCCCLL}` (6 hex chars)
 *   PP  = profile_idc (SPS byte 1)
 *   CCC = constraint_set flags (SPS byte 2)
 *   LL  = level_idc (SPS byte 3)
 *
 * Falls back to `avc1.42001E` if SPS is too short.
 */
export function buildH264CodecString(sps: Uint8Array, profile: number, level: number): string {
  if (sps.length >= 4) {
    const constraintByte = sps[2];
    return `avc1.${hexByte(profile)}${hexByte(constraintByte)}${hexByte(level)}`;
  }
  return FALLBACK_H264_CODEC;
}

/**
 * Build an H.265 codec string from SPS NAL unit data.
 *
 * Format: `hvc1.{profile_idc}.{profile_compat}.{tier}L{level}.{constraint}`
 * Extracted from SPS byte[1]: general_profile_space(2) + general_tier_flag(1) + general_profile_idc(5)
 * Falls back to `hvc1.1.6.L93.B0` if SPS is too short.
 */
export function buildH265CodecString(sps: Uint8Array, level: number): string {
  if (sps.length >= 3) {
    const byte1 = sps[1];
    const tierFlag = (byte1 >> 5) & 0x01;
    const profileIdc = byte1 & 0x1f;
    const tierChar = tierFlag === 1 ? 'H' : 'L';
    return `hvc1.${profileIdc}.6.${tierChar}${level}.B0`;
  }
  return FALLBACK_H265_CODEC;
}

function hexByte(value: number): string {
  return value.toString(16).padStart(2, '0').toUpperCase();
}

// ─── Annex B helpers ───────────────────────────────────────────────────────

/**
 * Prepend Annex B start codes (00 00 00 01) before each NALU.
 *
 * The WebSocket protocol delivers raw NALUs without start codes.
 * VideoDecoder requires Annex B formatted data.
 */
export function prependAnnexB(nalus: Uint8Array[]): Uint8Array {
  if (nalus.length === 0) return new Uint8Array(0);

  let totalSize = 0;
  for (const nalu of nalus) {
    totalSize += START_CODE.length + nalu.byteLength;
  }

  const result = new Uint8Array(totalSize);
  let offset = 0;
  for (const nalu of nalus) {
    result.set(START_CODE, offset);
    offset += START_CODE.length;
    result.set(nalu, offset);
    offset += nalu.byteLength;
  }

  return result;
}

/**
 * Build decoder description bytes (Annex B formatted parameter sets).
 *
 * H.264: SPS + PPS (each with start code prefix)
 * H.265: VPS + SPS + PPS (each with start code prefix)
 */
function buildDescription(ci: CodecInfo): Uint8Array {
  const parts: Uint8Array[] = [];

  if (ci.codec === 'h265' && ci.vps) {
    parts.push(START_CODE);
    parts.push(ci.vps);
  }
  if (ci.sps.length > 0) {
    parts.push(START_CODE);
    parts.push(ci.sps);
  }
  if (ci.pps.length > 0) {
    parts.push(START_CODE);
    parts.push(ci.pps);
  }

  let totalLen = 0;
  for (const part of parts) totalLen += part.byteLength;

  const result = new Uint8Array(totalLen);
  let offset = 0;
  for (const part of parts) {
    result.set(part, offset);
    offset += part.byteLength;
  }

  return result;
}

// ─── Decoder class ──────────────────────────────────────────────────────────

export class Decoder {
  private _decoder: VideoDecoder | null = null;
  private _wasmDecoder: WasmH265Decoder | null = null;
  private _mode: 'webcodecs' | 'wasm' | null = null;
  private _closed = false;
  private _configured = false;
  private _lastCodecInfo: CodecInfo | null = null;
  private _frameCallback: ((frame: VideoFrame) => void) | null = null;
  private _wasmFrameCallback:
    ((frame: { rgba: Uint8Array; width: number; height: number; pts: number }) => void) | null = null;
  private _errorCallback: ((error: Error) => void) | null = null;
  private _errorCount = 0;
  private static readonly MAX_RECOVERY_ATTEMPTS = 3;
  private _pendingFrames: Set<VideoFrame> = new Set();
  private _pendingDecodeCount = 0;
  private _frameDropCount = 0;
  private _backpressured = false;
  private _backpressureCallback: ((paused: boolean) => void) | null = null;
  private _decoderEpoch = 0;

  // ─── Decode-stall detection ─────────────────────────────────────────────
  // _configuredAt stamps when configure() last succeeded with no output yet;
  // _lastOutputAt stamps the last decoded frame. _outputCount tracks total
  // outputs so we can tell "never produced anything" (the dangerous case) from
  // "produced then went quiet" (recoverable). _stallTimer arms after configure
  // and fires onStall if no output arrives within DECODE_STALL_MS.
  private _configuredAt = 0;
  private _lastOutputAt = 0;
  private _outputCount = 0;
  private _stallTimer: ReturnType<typeof setTimeout> | null = null;
  private _onStallCallback: (() => void) | null = null;

  /**
   * Configure the decoder with codec info.
   *
   * Strategy:
   *   - H.264 → WebCodecs always (widely supported)
   *   - H.265 → Try WebCodecs first, fall back to WASM libde265 if unsupported
   *
   * @throws Error if all decode methods fail.
   */
  async configure(ci: CodecInfo): Promise<void> {
    if (this._closed) return;

    // For H.264: always use WebCodecs
    if (ci.codec === 'h264') {
      return this._configureWebCodecs(ci);
    }

    // For H.265: try WebCodecs first, fall back to WASM
    try {
      await this._configureWebCodecs(ci);
      this._mode = 'webcodecs';
    } catch {
      // WebCodecs doesn't support H.265 (Chromium) — try WASM fallback
      try {
        await this._configureWasm(ci);
        this._mode = 'wasm';
      } catch (wasmErr: any) {
        throw new Error(
          `H.265 decode failed: WebCodecs unsupported, WASM fallback failed: ${wasmErr?.message || wasmErr}`,
        );
      }
    }
  }

  /**
   * Decode NALUs into a video frame.
   *
   * Routes to WebCodecs or WASM decoder based on active mode.
   */
  decode(nalus: Uint8Array[], pts: number, isKeyframe: boolean): void {
    if (this._closed || !this._configured) return;

    if (this._mode === 'wasm') {
      return this._decodeWasm(nalus, pts, isKeyframe);
    }
    return this._decodeWebCodecs(nalus, pts, isKeyframe);
  }

  /**
   * Reset the decoder (discard pending frames, keep configuration capability).
   */
  reset(): void {
    if (this._closed) return;

    // Cancel any pending stall watchdog; a re-configure will re-arm it.
    this._clearStallTimer();

    // Reset WASM decoder
    if (this._wasmDecoder) {
      this._wasmDecoder.reset();
    }

    // Reset WebCodecs decoder
    if (this._decoder) {
      try {
        this._decoder.reset();
        this._configured = false;
      } catch {
        try {
          this._decoder.close();
        } catch {
          /* ignore */
        }
        this._decoder = null;
        this._configured = false;
      }
    }

    // Reset backpressure state
    this._pendingDecodeCount = 0;
    if (this._backpressured) {
      this._backpressured = false;
      if (this._backpressureCallback) {
        try {
          this._backpressureCallback(false);
        } catch {
          /* ignore */
        }
      }
    }
  }

  /**
   * Full cleanup — close both decoders and prevent further operations.
   */
  close(): void {
    if (this._closed) return;
    this._closed = true;
    this._configured = false;
    this._mode = null;
    this._clearStallTimer();

    if (this._decoder) {
      try {
        this._decoder.close();
      } catch {
        /* already closed */
      }
      this._decoder = null;
    }
    if (this._wasmDecoder) {
      this._wasmDecoder.close();
      this._wasmDecoder = null;
    }

    // Clean up pending frames
    for (const f of this._pendingFrames) {
      try {
        f.close();
      } catch {
        /* already closed */
      }
    }
    this._pendingFrames.clear();
    this._pendingDecodeCount = 0;
    this._backpressured = false;
  }

  /**
   * Register a callback for decoded VideoFrames.
   * The frame is automatically closed after the callback returns.
   */
  onFrame(callback: (frame: VideoFrame) => void): void {
    this._frameCallback = callback;
  }

  /**
   * Register a callback for decoder errors.
   * On error, the decoder auto-resets and attempts re-configuration.
   */
  onError(callback: (error: Error) => void): void {
    this._errorCallback = callback;
  }

  /**
   * Register a callback for backpressure state changes.
   * Called with true when decoder is overloaded (pending count >= threshold),
   * false when pressure has subsided.
   */
  onBackpressure(callback: (paused: boolean) => void): void {
    this._backpressureCallback = callback;
  }

  /**
   * Register a callback for decoded WasmFrames (raw RGBA).
   * Used ONLY when WebCodecs VideoFrame is unavailable (plain HTTP) — the
   * caller renders these via Canvas2D putImageData instead of the WebGL2
   * VideoFrame pipeline.
   */
  onWasmFrame(callback: (frame: { rgba: Uint8Array; width: number; height: number; pts: number }) => void): void {
    this._wasmFrameCallback = callback;
  }

  /** Number of decode requests currently in the WebCodecs pipeline. */
  get pendingDecodeCount(): number {
    return this._pendingDecodeCount;
  }

  /** Total number of frames dropped due to backpressure. */
  get frameDropCount(): number {
    return this._frameDropCount;
  }

  /** Whether the decoder is running in WASM mode (vs WebCodecs). */
  get isWasm(): boolean {
    return this._mode === 'wasm';
  }

  // ─── Internal ──────────────────────────────────────────────────────────

  /** Configure WebCodecs VideoDecoder. */
  private async _configureWebCodecs(ci: CodecInfo): Promise<void> {
    const codec = this.buildCodecString(ci);

    // Check if codec is supported
    if (typeof VideoDecoder !== 'undefined' && VideoDecoder.isConfigSupported) {
      const config: VideoDecoderConfig = {
        codec,
        codedWidth: DEFAULT_WIDTH,
        codedHeight: DEFAULT_HEIGHT,
        description: buildDescription(ci),
      };
      const support = await VideoDecoder.isConfigSupported(config);
      if (!support.supported) {
        throw new Error(`Unsupported codec: ${codec}`);
      }
    }

    this._decoderEpoch++;
    const epoch = this._decoderEpoch;
    this._decoder = new VideoDecoder({
      output: (frame: VideoFrame) => this.handleOutput(frame, epoch),
      error: this.handleError.bind(this),
    });
    await this._decoder.configure({
      codec,
      codedWidth: DEFAULT_WIDTH,
      codedHeight: DEFAULT_HEIGHT,
      description: buildDescription(ci),
    });

    this._configured = true;
    this._lastCodecInfo = ci;
    this._errorCount = 0;
    this._armStallTimer();
  }

  /** Configure WASM H.265 fallback decoder. */
  private async _configureWasm(ci: CodecInfo): Promise<void> {
    this._wasmDecoder = new WasmH265Decoder();
    await this._wasmDecoder.configure(ci);
    this._configured = true;
    this._lastCodecInfo = ci;
    this._errorCount = 0;
    this._armStallTimer();
  }

  // ─── Decode-stall watchdog ──────────────────────────────────────────────

  /**
   * Arm the stall timer after a successful configure. If no decoded frame
   * arrives within DECODE_STALL_MS, fire onStall so the caller (worker → main →
   * ConnectionManager) can reconnect or demote. Re-armable: each configure and
   * each output cancels and reschedules.
   */
  private _armStallTimer(): void {
    this._clearStallTimer();
    this._configuredAt = performance.now();
    if (this._onStallCallback === null) return; // no listener — skip the timer
    this._stallTimer = setTimeout(() => {
      this._stallTimer = null;
      // Only fire if STILL no output since this configure (output cancels the
      // timer, but guard against a race where output arrived just as the timer
      // elapsed).
      if (this._outputCount === 0 || this._lastOutputAt < this._configuredAt) {
        try {
          this._onStallCallback?.();
        } catch {
          /* listener threw — ignore */
        }
      }
    }, DECODE_STALL_MS);
  }

  /** Cancel any pending stall timer. */
  private _clearStallTimer(): void {
    if (this._stallTimer !== null) {
      clearTimeout(this._stallTimer);
      this._stallTimer = null;
    }
  }

  /**
   * Register the stall callback. Pass null to disable. Must be set BEFORE
   * configure to observe the first stall window.
   */
  onStall(cb: (() => void) | null): void {
    this._onStallCallback = cb;
  }

  /** Decode via WebCodecs. */
  private _decodeWebCodecs(nalus: Uint8Array[], pts: number, isKeyframe: boolean): void {
    if (!this._decoder || !this._configured) return;

    // Backpressure
    if (this._pendingDecodeCount >= BACKPRESSURE_THRESHOLD) {
      this._frameDropCount++;
      if (!this._backpressured) {
        this._backpressured = true;
        if (this._backpressureCallback) {
          try {
            this._backpressureCallback(true);
          } catch {
            /* ignore */
          }
        }
      }
      return;
    }

    const data = prependAnnexB(nalus);
    const chunk = new EncodedVideoChunk({
      type: isKeyframe ? 'key' : 'delta',
      timestamp: pts,
      data,
    });

    this._pendingDecodeCount++;
    this._decoder.decode(chunk);
  }

  /** Decode via WASM — synchronous, outputs a frame immediately. */
  private _decodeWasm(nalus: Uint8Array[], pts: number, isKeyframe: boolean): void {
    if (!this._wasmDecoder) return;

    // Backpressure (same threshold for consistency)
    if (this._pendingDecodeCount >= BACKPRESSURE_THRESHOLD) {
      this._frameDropCount++;
      if (!this._backpressured) {
        this._backpressured = true;
        if (this._backpressureCallback) {
          try {
            this._backpressureCallback(true);
          } catch {
            /* ignore */
          }
        }
      }
      return;
    }

    const frame = this._wasmDecoder.decode(nalus, pts, isKeyframe);
    if (frame) {
      this._pendingDecodeCount++;
      // Backpressure recovery (WASM decodes synchronously, count decremented below)
      if (this._backpressured && this._pendingDecodeCount < BACKPRESSURE_THRESHOLD) {
        this._backpressured = false;
        if (this._backpressureCallback) {
          try {
            this._backpressureCallback(false);
          } catch {
            /* ignore */
          }
        }
      }
      // Route WasmFrame (HTTP, no VideoFrame) vs VideoFrame (HTTPS).
      if (typeof VideoFrame !== 'undefined') {
        this.handleOutput(frame as VideoFrame, this._decoderEpoch);
      } else if (this._wasmFrameCallback) {
        this._pendingDecodeCount--;
        try {
          this._wasmFrameCallback(frame as any);
        } catch {
          /* callback failed — frame is plain data, no close() needed */
        }
      }
    }
  }

  private buildCodecString(ci: CodecInfo): string {
    if (ci.codec === 'h265') {
      return buildH265CodecString(ci.sps, ci.level);
    }
    return buildH264CodecString(ci.sps, ci.profile, ci.level);
  }

  private handleOutput(frame: VideoFrame, epoch: number): void {
    // Discard frames from a stale decoder (after close, reset, or error recovery)
    if (this._closed || epoch !== this._decoderEpoch) {
      try {
        frame.close();
      } catch {
        /* already closed */
      }
      return;
    }

    // Record that the decoder produced output. This is the single chokepoint
    // for decoded frames (both WebCodecs and WASM paths funnel through it), so
    // stamping here lets the stall watchdog distinguish "WS alive, decoder
    // silent" from "decoder working". Cancel the pending stall timer: any
    // output proves the pipeline is functioning.
    this._lastOutputAt = performance.now();
    this._outputCount++;
    this._clearStallTimer();

    this._pendingDecodeCount--;

    // Check backpressure recovery
    if (this._backpressured && this._pendingDecodeCount < BACKPRESSURE_THRESHOLD) {
      this._backpressured = false;
      if (this._backpressureCallback) {
        try {
          this._backpressureCallback(false);
        } catch {
          /* ignore */
        }
      }
    }

    this._pendingFrames.add(frame);
    if (this._frameCallback) {
      try {
        this._frameCallback(frame);
        // Frame transferred to main thread — caller owns it now.
      } catch {
        // Callback failed (e.g., postMessage threw) — we still own it.
        try {
          frame.close();
        } catch {
          /* already closed */
        }
      }
    } else {
      // No callback registered — close immediately to prevent leak
      try {
        frame.close();
      } catch {
        /* already closed */
      }
    }
    this._pendingFrames.delete(frame);
  }
  private handleError(error: Error): void {
    this._errorCount++;

    if (this._errorCallback) {
      this._errorCallback(error);
    }

    // Stop recovering after max attempts
    if (this._errorCount > Decoder.MAX_RECOVERY_ATTEMPTS) {
      // Permanently give up — set decoder to null so decode() is a no-op
      if (this._decoder) {
        try {
          this._decoder.close();
        } catch {
          /* ignore */
        }
      }
      this._decoder = null;
      this._configured = false;
      return;
    }

    if (!this._lastCodecInfo || this._closed || !this._decoder) return;

    if (this._decoder.state === 'closed') {
      try {
        this._decoder.close();
      } catch {
        /* already closed */
      }
      this._decoder = null;
      this._configured = false;
    } else {
      try {
        this._decoder.reset();
        this._decoder = null;
        this._configured = false;
      } catch {
        try {
          this._decoder.close();
        } catch {
          /* ignore */
        }
        this._decoder = null;
        this._configured = false;
      }
    }

    // Clean up any pending frames to prevent GPU memory leaks
    for (const f of this._pendingFrames) {
      try {
        f.close();
      } catch {
        /* already closed */
      }
    }
    this._pendingFrames.clear();

    if (!this._decoder) {
      const ci = this._lastCodecInfo;
      queueMicrotask(async () => {
        try {
          this._decoderEpoch++;
          const epoch = this._decoderEpoch;
          this._decoder = new VideoDecoder({
            output: (frame: VideoFrame) => this.handleOutput(frame, epoch),
            error: this.handleError.bind(this),
          });
          await this._decoder.configure({
            codec: this.buildCodecString(ci),
            codedWidth: DEFAULT_WIDTH,
            codedHeight: DEFAULT_HEIGHT,
            description: buildDescription(ci),
          });
          this._configured = true;
          this._errorCount = 0;
        } catch {
          this._decoder = null;
        }
      });
    }
  }
}
