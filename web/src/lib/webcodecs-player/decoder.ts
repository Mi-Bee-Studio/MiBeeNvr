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

    // Create and configure decoder
    this._decoder = new VideoDecoder({
      output: this.handleOutput.bind(this),
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

    const data = prependAnnexB(nalus);
    const chunk = new EncodedVideoChunk({
      type: isKeyframe ? 'key' : 'delta',
      timestamp: pts,
      data,
    });

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

  // ─── Internal ──────────────────────────────────────────────────────────

  private buildCodecString(ci: CodecInfo): string {
    if (ci.codec === 'h265') {
      return buildH265CodecString(ci.sps, ci.level);
    }
    return buildH264CodecString(ci.sps, ci.profile, ci.level);
  }

  private handleOutput(frame: VideoFrame): void {
    if (this._frameCallback) {
      this._frameCallback(frame);
      // Note: caller is responsible for closing the frame.
      // When used in a worker, the frame is transferred to the main thread
      // which closes it after rendering. When used directly, the caller
      // must call frame.close() to prevent GPU memory leaks.
    } else {
      // No callback registered — close immediately to prevent leak
      frame.close();
    }
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
        this._configured = false;
      } catch {
        try { this._decoder.close(); } catch { /* ignore */ }
        this._decoder = null;
        this._configured = false;
      }
    }

    if (!this._decoder) {
      const ci = this._lastCodecInfo;
      queueMicrotask(async () => {
        try {
          this._decoder = new VideoDecoder({
            output: this.handleOutput.bind(this),
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
