/**
 * Capability Detection Module
 *
 * Detects browser capabilities for WebCodecs, WebGPU, WebGL2, and related APIs.
 * Returns a playback tier (tier1 / tier2 / tier3) for adaptive streaming quality.
 *
 * All synchronous detection functions are fast / non-blocking.
 * detectHEVC() is async but short-circuits if WebCodecs is unavailable.
 */

export type PlaybackTier = 'tier1' | 'tier2' | 'tier3';

/** Check if WebCodecs API (VideoDecoder) is available. */
export function detectWebCodecs(): boolean {
  // WebCodecs requires a secure context (HTTPS or localhost).
  // On HTTP + non-localhost, VideoDecoder is simply undefined.
  return typeof VideoDecoder !== 'undefined';
}

/**
 * Return a human-readable reason why WebCodecs is unavailable.
 * Returns null when WebCodecs IS available.
 */
export function getWebCodecsUnavailableReason(): string | null {
  if (typeof VideoDecoder !== 'undefined') return null;
  if (typeof window !== 'undefined' && !window.isSecureContext) {
    return 'WebCodecs requires HTTPS or localhost access';
  }
  return 'Browser does not support WebCodecs';
}

/**
 * Check if HEVC (H.265) hardware decoder is supported via WebCodecs.
 * Async — calls VideoDecoder.isConfigSupported().
 * Returns false when WebCodecs is unavailable or the check fails.
 */
export async function detectHEVC(): Promise<boolean> {
  if (typeof VideoDecoder === 'undefined' || VideoDecoder === null) return false;
  try {
    const config: VideoDecoderConfig = {
      codec: 'hvc1.1.6.L93.B0',
      codedWidth: 1920,
      codedHeight: 1080,
    };
    const result = await VideoDecoder.isConfigSupported(config);
    return result.supported;
  } catch {
    return false;
  }
}

/** Check if WebGPU API is available. */
export function detectWebGPU(): boolean {
  return typeof navigator !== 'undefined' && (navigator as Record<string, unknown>).gpu !== undefined;
}

/** Check if WebGL2 is available by attempting context creation. */
export function detectWebGL2(): boolean {
  try {
    const canvas = document.createElement('canvas');
    return !!canvas.getContext('webgl2');
  } catch {
    return false;
  }
}

/** Check if OffscreenCanvas API is available. */
export function detectOffscreenCanvas(): boolean {
  return typeof OffscreenCanvas !== 'undefined';
}

/** Check if SharedArrayBuffer is available. */
export function detectSharedArrayBuffer(): boolean {
  return typeof SharedArrayBuffer !== 'undefined';
}

/**
 * Check if WebAssembly SIMD is supported.
 * Uses WebAssembly.validate() with a minimal WASM module containing
 * a v128 type and i8x16.splat instruction.
 */
export function detectWasmSimd(): boolean {
  try {
    if (
      typeof WebAssembly === 'undefined' ||
      WebAssembly === null ||
      typeof WebAssembly.validate !== 'function'
    ) {
      return false;
    }
    // Minimal WASM module using a SIMD v128 instruction (i8x16.splat)
    const binary = new Uint8Array([
      0x00, 0x61, 0x73, 0x6d, // \0asm  magic
      0x01, 0x00, 0x00, 0x00, // version 1
      // Type section: one function () -> v128
      0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7b,
      // Function section: declare 1 function (index 0)
      0x03, 0x02, 0x01, 0x00,
      // Code section: 1 body with i32.const 0; i8x16.splat; end
      0x0a, 0x08, 0x01, 0x06, 0x00, 0x41, 0x00, 0xfd, 0x0f, 0x0b,
    ]);
    return WebAssembly.validate(binary);
  } catch {
    return false;
  }
}

/**
 * Determine playback tier based on available capabilities.
 *
 *   tier1 — WebCodecs + WebGPU        (best performance)
 *   tier2 — WebCodecs + (WebGL2 | OffscreenCanvas)  (good performance)
 *   tier3 — fallback                   (basic playback)
 */
export function getPlaybackTier(): PlaybackTier {
  if (detectWebCodecs() && detectWebGPU()) {
    return 'tier1';
  }
  if (detectWebCodecs() && (detectWebGL2() || detectOffscreenCanvas())) {
    return 'tier2';
  }
  return 'tier3';
}
