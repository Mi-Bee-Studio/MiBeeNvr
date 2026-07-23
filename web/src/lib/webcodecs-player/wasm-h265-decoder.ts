/**
 * WASM H.265 (HEVC) decoder backend using libde265.
 *
 * Wraps @yume-chan/libde265 to provide NAL→YUV→RGBA→VideoFrame decoding
 * as a fallback when WebCodecs VideoDecoder doesn't support H.265 (Chromium).
 *
 * Lifecycle:
 *   1. configure(codecInfo) — init WASM module, push VPS/SPS/PPS
 *   2. decode(nalus, pts, isKeyframe) — push NALs, decode, return VideoFrame
 *   3. reset() / close() — cleanup
 *
 * Designed to run inside a Web Worker — no DOM dependencies.
 */

import type { CodecId, CodecInfo } from './protocol';
import { loadLibde265, getLibde265, isLibde265Loaded } from './libde265-loader';
import type { MainModule, Decoder as Libde265Decoder, Image as Libde265Image } from '@yume-chan/libde265';
import { yuv420ToRgba } from './yuv-rgba';

// ─── Annex B start code ──────────────────────────────────────────────────────

const START_CODE = new Uint8Array([0x00, 0x00, 0x00, 0x01]);

// ─── Types ───────────────────────────────────────────────────────────────────

/**
 * A decoded H.265 frame as raw RGBA pixels — used when WebCodecs VideoFrame is
 * unavailable (plain HTTP, non-localhost). The caller renders it via Canvas2D
 * putImageData instead of the WebGL2/WebGPU VideoFrame pipeline.
 */
export interface WasmFrame {
  /** RGBA pixel data (width * height * 4 bytes). */
  rgba: Uint8Array;
  width: number;
  height: number;
  /** Presentation timestamp (same units as the input PTS). */
  pts: number;
}

// ─── WasmH265Decoder ─────────────────────────────────────────────────────────

export class WasmH265Decoder {
  private _decoder: Libde265Decoder | null = null;
  private _module: MainModule | null = null;
  private _initialized = false;
  private _width = 0;
  private _height = 0;

  /**
   * Initialize the WASM decoder with codec parameters.
   * Loads the WASM module lazily on first call.
   */
  async configure(ci: CodecInfo): Promise<void> {
    // Load WASM module if not already loaded
    this._module = await loadLibde265();

    // Create decoder instance
    this._decoder = new this._module.Decoder();

    // Push parameter sets (VPS, SPS, PPS) as initial data
    // libde265 needs these before decoding actual frames
    const paramSets: Uint8Array[] = [];
    if (ci.codec === 'h265' && ci.vps && ci.vps.byteLength > 0) {
      paramSets.push(ci.vps);
    }
    if (ci.sps.byteLength > 0) {
      paramSets.push(ci.sps);
    }
    if (ci.pps.byteLength > 0) {
      paramSets.push(ci.pps);
    }

    // Push each parameter set with Annex B start codes
    for (const ps of paramSets) {
      const annexB = prependStartCode(ps);
      this._decoder.pushData(annexB, 0n);
      this._decoder.pushEndOfNal();
    }

    // Decode the parameter sets so decoder knows frame dimensions
    this._decoder.decode();

    this._initialized = true;
  }

  /**
   * Decode a frame from raw NAL units.
   *
   * @returns Decoded frame, or null if no frame is available yet.
   *          When WebCodecs is available, returns a synthetic VideoFrame (RGBA).
   *          When WebCodecs is unavailable (HTTP non-localhost), returns a
   *          WasmFrame (raw RGBA + dimensions) so the caller can render via
   *          Canvas2D putImageData without depending on the VideoFrame API.
   */
  decode(nalus: Uint8Array[], pts: number, _isKeyframe: boolean): VideoFrame | WasmFrame | null {
    if (!this._initialized || !this._decoder || !this._module) return null;

    // Push each NAL unit. pushNal() expects ONE NAL WITHOUT a start code — the
    // decoder treats the whole buffer as a single NAL payload (whereas pushData()
    // expects a raw bytestream WITH start codes and splits NALs itself). The WS
    // protocol already delivers raw NAL payloads (no start codes), so we feed
    // them directly. Prepending a start code here corrupts the NAL header (the
    // decoder reads `00 00` as the 2-byte HEVC NAL header → forbidden
    // nal_unit_type=0 → every frame rejected → "configure OK, no frames").
    for (const nalu of nalus) {
      const err = this._decoder.pushNal(nalu, BigInt(pts));
      if (!this._module.isOk(err)) {
        // Push failed — likely need more data or parameter sets
        return null;
      }
      this._decoder.pushEndOfNal();
    }
    this._decoder.pushEndOfFrame();

    // Decode and extract frame
    let more = true;
    while (more) {
      const result = this._decoder.decode();
      more = result.more;

      if (!this._module.isOk(result.error)) {
        // ERROR_WAITING_FOR_INPUT_DATA (13) is normal — need more NALs.
        if (result.error === 13) break;
        // Warnings (>= 1000) are non-fatal per libde265 — a malformed slice,
        // reference picture issue, etc. Keep going in case a picture is still
        // available. Only hard errors (1..502) abort this decode cycle.
        if (result.error >= 1000) {
          // Still try to retrieve any picture produced before the warning.
          const warnImage = this._decoder.getNextPicture();
          if (warnImage) {
            const produced = this._emitFrame(warnImage, pts);
            if (produced) return produced;
          }
          continue;
        }
        break;
      }

      // Try to get decoded picture
      const image: Libde265Image | null = this._decoder.getNextPicture();
      if (!image) continue;

      // Must delete image to prevent ERROR_IMAGE_BUFFER_FULL — done by _emitFrame
      const produced = this._emitFrame(image, pts);
      if (produced) return produced;
    }

    return null;
  }

  /** Reset decoder state (discard pending data, keep config). */
  reset(): void {
    if (this._decoder) {
      this._decoder.reset();
    }
  }

  /**
   * Convert a libde265 decoded image to an output frame (VideoFrame or WasmFrame).
   * Handles YUV→RGBA conversion and ALWAYS deletes the image handle to avoid
   * ERROR_IMAGE_BUFFER_FULL. Returns null if the image has no valid dimensions.
   *
   * @returns output frame, or null if the image was empty/malformed. The image
   *          is deleted either way (caller must not touch it after this call).
   */
  private _emitFrame(image: Libde265Image, pts: number): VideoFrame | WasmFrame | null {
    let produced: VideoFrame | WasmFrame | null = null;
    try {
      this._width = image.getWidth(0);
      this._height = image.getHeight(0);
      if (this._width > 0 && this._height > 0) {
        const y = image.getImagePlane(0);
        const u = image.getImagePlane(1);
        const v = image.getImagePlane(2);
        const rgba = yuv420ToRgba(y.bytes, u.bytes, v.bytes, this._width, this._height);
        if (typeof VideoFrame !== 'undefined') {
          // WebCodecs available (HTTPS/localhost): wrap in a synthetic VideoFrame
          // for compatibility with the WebGL2/WebGPU rendering pipeline.
          produced = new VideoFrame(rgba, {
            codedWidth: this._width,
            codedHeight: this._height,
            timestamp: pts,
            format: 'RGBA',
          });
        } else {
          // WebCodecs unavailable (plain HTTP): raw RGBA for Canvas2D putImageData.
          produced = { rgba, width: this._width, height: this._height, pts };
        }
      }
    } finally {
      // Always delete to prevent ERROR_IMAGE_BUFFER_FULL.
      image.delete();
    }
    return produced;
  }

  /** Full cleanup — release WASM decoder and free memory. */
  close(): void {
    if (this._decoder) {
      try {
        this._decoder.delete();
      } catch {
        /* ignore */
      }
      this._decoder = null;
    }
    this._initialized = false;
    this._width = 0;
    this._height = 0;
  }
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

/**
 * Prepend Annex B start code (00 00 00 01) before a NAL unit.
 */
function prependStartCode(nalu: Uint8Array): Uint8Array {
  const result = new Uint8Array(START_CODE.length + nalu.byteLength);
  result.set(START_CODE, 0);
  result.set(nalu, START_CODE.length);
  return result;
}
