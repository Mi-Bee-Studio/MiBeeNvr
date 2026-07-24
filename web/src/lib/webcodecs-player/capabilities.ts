/**
 * Capability Detection Module
 *
 * Low-level browser-capability probes for WebCodecs, WebGPU, WebGL2, MSE H.265,
 * and libde265 WASM. Returns a playback tier (tier1 / tier2 / tier3) for the
 * WebCodecs player's render-path selection.
 *
 * Consumers should NOT call these directly in render paths — use the cached
 * {@link probeCaps} / {@link getCaps} from `$lib/player/capabilities-cache`,
 * which probes once per session and shares the result. Calling these probes in
 * a Svelte `$effect` risks the reactive loop that caused the WS reconnect storm.
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

/**
 * Check if the browser's MediaSource Extensions (MSE) can decode H.265/HEVC.
 *
 * This is distinct from detectHEVC() (which checks WebCodecs VideoDecoder):
 * MSE is used by mpegts.js (FLV) and hls.js (HLS fMP4) for playback. When MSE
 * lacks an H.265 decoder — common on Linux desktop, or Windows without the
 * "HEVC Video Extensions" pack — FLV/HLS players connect but render a black
 * screen. Detecting this lets the caller auto-degrade to a working protocol.
 *
 * Synchronous and side-effect free. Returns false when MediaSource is absent.
 */
export function detectMSEH265(): boolean {
  if (typeof MediaSource === 'undefined' || MediaSource === null) return false;
  try {
    // hvc1.1.6.L93.B0 = HEVC Main profile, level 3.1 — a widely testable codec string.
    return MediaSource.isTypeSupported('video/mp4; codecs="hvc1.1.6.L93.B0"');
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

/**
 * Check if OffscreenCanvas API is available.
 * Used internally by {@link getPlaybackTier}; kept exported for the capability
 * test suite.
 */
export function detectOffscreenCanvas(): boolean {
  return typeof OffscreenCanvas !== 'undefined';
}

// NOTE: detectSharedArrayBuffer() and detectWasmSimd() were removed — they had
// no consumers in src/ (libde265 loads lazily and self-probes). Their tests in
// capabilities.test.ts were removed too.

/**
 * Determine playback tier based on available capabilities.
 *
 *   tier1 — WebCodecs + WebGPU        (best performance)
 *   tier2 — WebCodecs + (WebGL2 | OffscreenCanvas)  (good playback)
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

/**
 * Check whether H.265 can be played via the libde265 WASM soft-decoder.
 *
 * Unlike WebCodecs (which needs HTTPS/localhost), libde265 is pure WASM and
 * works on plain HTTP. It renders via Canvas2D putImageData (no WebGL needed).
 * This is the fallback path that enables H.265 live playback in HTTP
 * environments where HLS/FLV/WebRTC all fail (browsers can't decode HEVC via
 * MSE or native <video> on Linux/no-HEVC-extension).
 *
 * Returns true whenever WebAssembly is available (libde265 loads lazily on
 * first decode; we don't probe the module here to avoid a network fetch).
 */
export function detectWasmH265(): boolean {
  return typeof WebAssembly !== 'undefined' && typeof CanvasRenderingContext2D !== 'undefined';
}
