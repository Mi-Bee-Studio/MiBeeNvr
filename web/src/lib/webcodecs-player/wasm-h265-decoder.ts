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

  // ── Throughput & integrity degradation ──────────────────────────────
  // wasm H.265 on weak CPUs decodes high resolutions at a small fraction of
  // the source frame rate (measured ~0.2fps for 4K vs 15-25fps input on an
  // ARM-class box). An ever-growing backlog then corrupts output (users see
  // bottom-half green/white concealment). Two guards keep the picture honest:
  //  - _skipUntilKeyframe: after a failed push the reference chain is broken;
  //    P-frames decoded against missing references display as concealment.
  //    Skip everything until the next IDR.
  //  - _keyframeOnly: when decode cost persistently exceeds the frame budget,
  //    decode keyframes only — a stable still image per GOP instead of a
  //    backlog of corrupted halves. Releases if decoding becomes cheap again
  //    (e.g. the stream resolution dropped).
  private _skipUntilKeyframe = false;
  private _keyframeOnly = false;
  private _decodeMsEma = 0;
  private _decodeCount = 0;
  private _frameIntervalMs = 0;
  private _lastPts = 0;

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
  decode(nalus: Uint8Array[], pts: number, isKeyframe: boolean): VideoFrame | WasmFrame | null {
    if (!this._initialized || !this._decoder || !this._module) return null;

    // Track the source frame interval (90kHz PTS → ms) as the decode budget.
    if (this._lastPts > 0 && pts > this._lastPts) {
      const interval = (pts - this._lastPts) / 90;
      if (interval > 1 && interval < 10000) {
        this._frameIntervalMs = this._frameIntervalMs
          ? this._frameIntervalMs * 0.9 + interval * 0.1
          : interval;
      }
    }
    this._lastPts = pts;

    if (this._skipUntilKeyframe && !isKeyframe) return null;
    if (this._keyframeOnly && !isKeyframe) return null;

    const t0 = performance.now();
    const produced = this._decodePushed(nalus, pts);
    this._noteDecodeMs(performance.now() - t0);
    return produced;
  }

  private _decodePushed(nalus: Uint8Array[], pts: number): VideoFrame | WasmFrame | null {

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
        // Push failed: the reference chain is now broken — decoding subsequent
        // P-frames displays as bottom-half concealment. Skip to the next IDR.
        this._skipUntilKeyframe = true;
        return null;
      }
      this._decoder.pushEndOfNal();
    }
    this._decoder.pushEndOfFrame();

    // In degraded mode damaged pictures are worse than a stale one: suppress
    // warned pictures (they carry concealed regions) so the tile keeps the
    // last clean frame instead of flashing corruption.
    const suppressWarned = this._keyframeOnly || this._skipUntilKeyframe;

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
            if (suppressWarned) {
              warnImage.delete();
            } else {
              const produced = this._emitFrame(warnImage, pts);
              if (produced) return produced;
            }
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
      if (produced) {
        // A clean picture proves the chain recovered.
        this._skipUntilKeyframe = false;
        return produced;
      }
    }

    return null;
  }

  /** Update decode-cost EMA and toggle keyframe-only mode on sustained overload. */
  private _noteDecodeMs(ms: number): void {
    this._decodeCount++;
    this._decodeMsEma = this._decodeMsEma ? this._decodeMsEma * 0.8 + ms * 0.2 : ms;
    if (this._decodeCount < 4) return; // let the EMA and frame interval settle
    const budget = this._frameIntervalMs || 66;
    if (!this._keyframeOnly && this._decodeMsEma > budget * 1.5) {
      this._keyframeOnly = true;
    } else if (this._keyframeOnly && this._decodeMsEma < budget * 0.5) {
      // Cheap again (resolution drop / faster machine) — try full rate back.
      this._keyframeOnly = false;
    }
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
        // Plane strides: libde265 may pad rows beyond width; reading with
        // tight strides on padded planes skews or corrupts the image.
        const rgba = yuv420ToRgba(
          y.bytes, u.bytes, v.bytes, this._width, this._height,
          y.stride || this._width, u.stride || (this._width >> 1), v.stride || (this._width >> 1),
        );
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
