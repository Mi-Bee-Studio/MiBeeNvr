/**
 * WebCodecs VideoDecoder lifecycle management.
 *
 * Wraps the WebCodecs VideoDecoder API for H.264/H.265 decoding.
 * Handles codec configuration, NALU processing, error recovery,
 * and ensures VideoFrame.close() is always called to prevent GPU memory leaks.
 *
 * Designed to run inside a Web Worker — no DOM dependencies.
 */

import type { CodecInfo } from './protocol';

// ─── Constants ────────────────────────────────────────────────────────────

const START_CODE = new Uint8Array([0x00, 0x00, 0x00, 0x01]);
const DEFAULT_WIDTH = 1920;
const DEFAULT_HEIGHT = 1080;
const FALLBACK_H264_CODEC = 'avc1.42001E';   // Baseline L3.0
const FALLBACK_H265_CODEC = 'hvc1.1.6.L93.B0'; // Main L3.1
const BACKPRESSURE_THRESHOLD = 5;

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
export function buildH264CodecString(
  sps: Uint8Array,
  profile: number,
  level: number,
): string {
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
export function buildH265CodecString(
  sps: Uint8Array,
  level: number,
): string {
  if (sps.length >= 3) {
    const byte1 = sps[1];
    const tierFlag = (byte1 >> 5) & 0x01;
    const profileIdc = byte1 & 0x1F;
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
  private _closed = false;
  private _configured = false;
  private _lastCodecInfo: CodecInfo | null = null;
  private _frameCallback: ((frame: VideoFrame) => void) | null = null;
  private _errorCallback: ((error: Error) => void) | null = null;
  private _errorCount = 0;
  private static readonly MAX_RECOVERY_ATTEMPTS = 3;
  private _pendingFrames: Set<VideoFrame> = new Set();
  private _pendingDecodeCount = 0;
  private _frameDropCount = 0;
  private _backpressured = false;
  private _backpressureCallback: ((paused: boolean) => void) | null = null;
  private _decoderEpoch = 0;

  /**
   * Configure the VideoDecoder with codec info.
   *
   * @throws Error if codec is not supported or WebCodecs is unavailable.
   */
  async configure(ci: CodecInfo): Promise<void> {
    if (this._closed) return;

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

    // Create and configure decoder with epoch tracking for stale-frame detection
    // Create and configure decoder with epoch tracking for stale-frame detection
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
  }

  /**
   * Decode NALUs into a video frame.
   *
   * Prepends Annex B start codes and creates an EncodedVideoChunk.
   */
  decode(nalus: Uint8Array[], pts: number, isKeyframe: boolean): void {
    if (this._closed || !this._decoder || !this._configured) return;

    // Backpressure: skip frame if decode queue is full
    if (this._pendingDecodeCount >= BACKPRESSURE_THRESHOLD) {
      this._frameDropCount++;
      if (!this._backpressured) {
        this._backpressured = true;
        if (this._backpressureCallback) {
          try { this._backpressureCallback(true); } catch { /* ignore */ }
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

  /**
   * Reset the decoder (discard pending frames, keep configuration capability).
   */
  reset(): void {
    if (this._closed || !this._decoder) return;
    try {
      this._decoder.reset();
      this._configured = false;
    } catch {
      // reset() throws if decoder state is 'closed'
      try { this._decoder.close(); } catch { /* ignore */ }
      this._decoder = null;
      this._configured = false;
    }
    // Reset backpressure state — all queued decodes are flushed
    this._pendingDecodeCount = 0;
    if (this._backpressured) {
      this._backpressured = false;
      if (this._backpressureCallback) {
        try { this._backpressureCallback(false); } catch { /* ignore */ }
      }
    }
  }

  /**
   * Full cleanup — close the decoder and prevent further operations.
   */
  close(): void {
    if (this._closed) return;
    this._closed = true;
    this._configured = false;
    if (this._decoder) {
      try {
        this._decoder.close();
      } catch {
        // Already closed
      }
      this._decoder = null;
    }
    // Clean up any remaining pending frames to prevent GPU memory leaks
    for (const f of this._pendingFrames) {
      try { f.close(); } catch { /* already closed */ }
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

  /** Number of decode requests currently in the WebCodecs pipeline. */
  get pendingDecodeCount(): number {
    return this._pendingDecodeCount;
  }

  /** Total number of frames dropped due to backpressure. */
  get frameDropCount(): number {
    return this._frameDropCount;
  }

  // ─── Internal ──────────────────────────────────────────────────────────

  private buildCodecString(ci: CodecInfo): string {
    if (ci.codec === 'h265') {
      return buildH265CodecString(ci.sps, ci.level);
    }
    return buildH264CodecString(ci.sps, ci.profile, ci.level);
  }

  private handleOutput(frame: VideoFrame, epoch: number): void {
    // Discard frames from a stale decoder (after close, reset, or error recovery)
    if (this._closed || epoch !== this._decoderEpoch) {
      try { frame.close(); } catch { /* already closed */ }
      return;
    }

    this._pendingDecodeCount--;

    // Check backpressure recovery
    if (this._backpressured && this._pendingDecodeCount < BACKPRESSURE_THRESHOLD) {
      this._backpressured = false;
      if (this._backpressureCallback) {
        try { this._backpressureCallback(false); } catch { /* ignore */ }
      }
    }

    this._pendingFrames.add(frame);
    if (this._frameCallback) {
      try {
        this._frameCallback(frame);
        // Frame transferred to main thread — caller owns it now.
      } catch {
        // Callback failed (e.g., postMessage threw) — we still own it.
        try { frame.close(); } catch { /* already closed */ }
      }
    } else {
      // No callback registered — close immediately to prevent leak
      try { frame.close(); } catch { /* already closed */ }
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
        try { this._decoder.close(); } catch { /* ignore */ }
      }
      this._decoder = null;
      this._configured = false;
      return;
    }

    if (!this._lastCodecInfo || this._closed || !this._decoder) return;

    if (this._decoder.state === 'closed') {
      try { this._decoder.close(); } catch { /* already closed */ }
      this._decoder = null;
      this._configured = false;
    } else {
      try {
        this._decoder.reset();
        this._decoder = null;
        this._configured = false;
      } catch {
        try { this._decoder.close(); } catch { /* ignore */ }
        this._decoder = null;
        this._configured = false;
      }
    }

    // Clean up any pending frames to prevent GPU memory leaks
    for (const f of this._pendingFrames) {
      try { f.close(); } catch { /* already closed */ }
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
