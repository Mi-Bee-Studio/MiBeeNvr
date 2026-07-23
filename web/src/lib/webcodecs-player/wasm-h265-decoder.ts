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

    // Push each NAL unit with start codes
    for (const nalu of nalus) {
      const annexB = prependStartCode(nalu);
      const err = this._decoder.pushNal(annexB, BigInt(pts));
      if (!this._module.isOk(err)) {
        // Push failed — likely need more data or parameter sets
        return null;
      }
      this._decoder.pushEndOfNal();
    }
    this._decoder.pushEndOfFrame();

    // Decode and extract frame
    let frame: VideoFrame | WasmFrame | null = null;
    let more = true;
    while (more) {
      const result = this._decoder.decode();
      more = result.more;

      if (!this._module.isOk(result.error)) {
        // ERROR_WAITING_FOR_INPUT_DATA is normal — need more NALs
        if (result.error === 13) {
          // ERROR_WAITING_FOR_INPUT_DATA
          break;
        }
        // Other errors — stop
        break;
      }

      // Try to get decoded picture
      const image: Libde265Image | null = this._decoder.getNextPicture();
      if (!image) continue;

      // Extract frame dimensions
      this._width = image.getWidth(0);
      this._height = image.getHeight(0);

      if (this._width > 0 && this._height > 0) {
        // Get YUV planes
        const y = image.getImagePlane(0);
        const u = image.getImagePlane(1);
        const v = image.getImagePlane(2);

        // Convert YUV420P → RGBA
        const rgba = yuv420ToRgba(y.bytes, u.bytes, v.bytes, this._width, this._height);

        if (typeof VideoFrame !== 'undefined') {
          // WebCodecs available (HTTPS/localhost): wrap in a synthetic VideoFrame
          // for compatibility with the WebGL2/WebGPU rendering pipeline.
          frame = new VideoFrame(rgba, {
            codedWidth: this._width,
            codedHeight: this._height,
            timestamp: pts,
            format: 'RGBA',
          });
        } else {
          // WebCodecs unavailable (plain HTTP): return raw RGBA for Canvas2D
          // rendering. This is the HTTP H.265 playback path — libde265 WASM
          // decodes fine without a secure context; only the VideoFrame wrapper
          // was blocking it.
          frame = { rgba, width: this._width, height: this._height, pts };
        }
      }

      // Must delete image to prevent ERROR_IMAGE_BUFFER_FULL
      image.delete();

      // Return the first frame found (one decode call = one frame in our use case)
      if (frame) return frame;
    }

    return null;
  }

  /** Reset decoder state (discard pending data, keep config). */
  reset(): void {
    if (this._decoder) {
      this._decoder.reset();
    }
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
