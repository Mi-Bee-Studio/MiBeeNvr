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

/**
 * Check if the browser's MediaSource Extensions (MSE) can decode H.265/HEVC.
 *
 * This is distinct from detectHEVC() (which checks WebCodecs VideoDecoder):
 * MSE is used by mpegts.js (FLV) and hls.js (HLS fMP4) for playback. When MSE
 * lacks an H.265 decoder — common on Linux desktop, or Windows without the
 * "HEVC Video Extensions" pack — FLV/HLS players connect but render a black
 * screen. Detecting this lets the caller auto-degrade to a working protocol.
 *
 * IMPORTANT: this is a *fast* check and historically relied on
 * `MediaSource.isTypeSupported('hvc1')`. That is a KNOWN FALSE POSITIVE on
 * Chromium/Edge: `isTypeSupported('hvc1')` returns true (because the OS HEVC
 * decoder is registered for *native* `<video>` playback) but the MSE
 * SourceBuffer silently drops the appended fMP4 bytes — `video.buffered`
 * stays empty and hls.js/FLV render a permanent black screen for H.265.
 *
 * To avoid the false positive, this sync function returns the result of the
 * authoritative async probe once it has run (cached). Before the probe runs
 * it falls back to false (conservative: route H.265 to the wasm/WebCodecs
 * player, which always works), never to the isTypeSupported lie. Callers that
 * can await should use probeMSEH265() once at startup and read the cached
 * value afterwards via detectMSEH265().
 */
export function detectMSEH265(): boolean {
  if (typeof MediaSource === 'undefined' || MediaSource === null) return false;
  return cachedMSEH265Probe;
}

// Cached result of probeMSEH265(). Starts false (conservative) so that before
// the async probe completes, H.265 is NOT assumed to work over MSE.
let cachedMSEH265Probe = false;
let mseH265ProbePromise: Promise<boolean> | null = null;

// Minimal static HEVC (hvc1) fMP4 init segment — ftyp + moov(hvc1 + hvcC).
// Used only to test whether a SourceBuffer actually retains appended hvc1
// data. Codec profile/level are irrelevant to the buffer-acceptance test.
const H265_INIT_SEGMENT_B64 =
  'AAAAIGZ0eXBtcDQyAAAAAW1wNDFtcDQyaXNvbWhsc2YAAALVbW9vdgAAAGxtdmhkAAAA' +
  'AAAAAAAAAAAAAAAAAAAD6AAAAAAAAQAAAQAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAA' +
  'AAAABAAAAAAAAAAAAAAAAAABAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' +
  'AP////8AAAAI5dHJhawAAAFx0a2hkAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAAAAA' +
  'AAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAQAAAAAo' +
  'AAAAFoAAAABAABHVbWRpYQAAACBtZGhkAAAAAAAAAAAAAAAAAAFfkAAAAABVxAAAAAAAA' +
  'LWhkbHIAAAAAAAAAAHZpZGUAAAAAAAAAAAAAAABWaWRlb0hhbmRsZXIAAAABgG1pbmYA' +
  'AAAUdm1oZAAAAAEAAAAAAAAAAAAAACRkaW5mAAAAHGRyZWYAAAAAAAAAAQAAAAx1cmwg' +
  'AAAAAQAAAUBzdGJsAAAA9HN0c2QAAAAAAAAAAQAAAORodmMxAAAAAAAAAAEAAAAAAAAA' +
  'AAAAAAAAAAACgAFoABIAAAASAAAAAAAAAABAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' +
  'AAAAAAAAAAAGP//AAAAemh2Y0MBAUAAAAADAJAAAAOW8AD8/fj4AAATAyAAAQAYQAEMA' +
  'f//IUAAAAMAkAAAAwAAAwCWJQJAIQABAC1CAQEhQAAAAwCQAAADAAADAJagAUAgBaFnr' +
  'uRKFzUBAQEEAAADAAQAAAMAUCAiAAEAB0QBwPfA5tkAAAAUYnRydAAAAAAAD0JAAA9CQ' +
  'AAAABBzdHRzAAAAAAAAAAAAAAAQc3RzYwAAAAAAAAAAAAAAFHN0c3oAAAAAAAAAAAAA' +
  'AAAAAAAAQc3RjbwAAAAAAAAAAAAAAKG12ZXgAAAAgdHJleAAAAAAAAAABAAAAAQAAAA' +
  'AAAAAAAAAA';

/**
 * Authoritative MSE H.265 probe. Creates a throwaway MediaSource + hvc1
 * SourceBuffer, appends a real HEVC init segment, and checks whether the
 * video element gains a buffered range. On Chromium/Edge the appendBuffer
 * call completes without error but produces no buffered range → returns
 * false (the isTypeSupported false positive). On Safari and any environment
 * with genuine MSE H.265 support, a range appears → returns true.
 *
 * Result is cached for the page lifetime (and in sessionStorage so a reload
 * within the session is free). Safe to call repeatedly; only the first call
 * does the actual probe.
 *
 * @returns true iff MSE can actually buffer/play hvc1 data.
 */
export async function probeMSEH265(force = false): Promise<boolean> {
  if (typeof MediaSource === 'undefined' || MediaSource === null) {
    cachedMSEH265Probe = false;
    return false;
  }
  // Fast path: isTypeSupported must at least claim it; if it doesn't, skip.
  let claimed = false;
  try { claimed = MediaSource.isTypeSupported('video/mp4; codecs="hvc1.1.6.L93.B0"'); } catch { claimed = false; }
  if (!claimed) { cachedMSEH265Probe = false; return false; }

  if (!force) {
    // Session-cached result from a previous probe this session.
    try {
      const sc = sessionStorage.getItem('mibee_mse_h265_probe');
      if (sc === '1') { cachedMSEH265Probe = true; return true; }
      if (sc === '0') { cachedMSEH265Probe = false; return false; }
    } catch { /* sessionStorage may be unavailable */ }
    if (mseH265ProbePromise) return mseH265ProbePromise;
  }

  mseH265ProbePromise = (async () => {
    let result = false;
    let ms: MediaSource | null = null;
    let sb: SourceBuffer | null = null;
    let video: HTMLVideoElement | null = null;
    let objectUrl = '';
    try {
      ms = new MediaSource();
      video = document.createElement('video');
      video.muted = true;
      objectUrl = URL.createObjectURL(ms);
      video.src = objectUrl;
      // Await sourceopen.
      await new Promise<void>((resolve, reject) => {
        const t = setTimeout(() => reject(new Error('sourceopen timeout')), 2000);
        ms!.addEventListener('sourceopen', () => { clearTimeout(t); resolve(); }, { once: true });
      });
      sb = ms.addSourceBuffer('video/mp4; codecs="hvc1.1.6.L93.B0"');
      // Decode the init segment and append; wait for updateend (or error).
      const bin = atob(H265_INIT_SEGMENT_B64);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      await new Promise<void>((resolve) => {
        const done = () => resolve();
        sb!.addEventListener('updateend', done, { once: true });
        sb!.addEventListener('error', done, { once: true });
        try { sb!.appendBuffer(bytes); } catch { done(); }
        setTimeout(done, 1500);
      });
      // The decisive test: did MSE actually retain the hvc1 data as a
      // buffered range? An empty range (Chromium/Edge false positive) → false.
      let bufferedLength = 0;
      try { bufferedLength = video.buffered.length; } catch { /* ignore */ }
      result = bufferedLength > 0;
    } catch {
      result = false;
    } finally {
      try { if (sb && ms && ms.readyState === 'open') ms.removeSourceBuffer(sb); } catch { /* ignore */ }
      try { if (video) video.src = ''; } catch { /* ignore */ }
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    }
    cachedMSEH265Probe = result;
    try { sessionStorage.setItem('mibee_mse_h265_probe', result ? '1' : '0'); } catch { /* ignore */ }
    return result;
  })();
  return mseH265ProbePromise;
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
    if (typeof WebAssembly === 'undefined' || WebAssembly === null || typeof WebAssembly.validate !== 'function') {
      return false;
    }
    // Minimal WASM module using a SIMD v128 instruction (i8x16.splat)
    const binary = new Uint8Array([
      0x00,
      0x61,
      0x73,
      0x6d, // \0asm  magic
      0x01,
      0x00,
      0x00,
      0x00, // version 1
      // Type section: one function () -> v128
      0x01,
      0x05,
      0x01,
      0x60,
      0x00,
      0x01,
      0x7b,
      // Function section: declare 1 function (index 0)
      0x03,
      0x02,
      0x01,
      0x00,
      // Code section: 1 body with i32.const 0; i8x16.splat; end
      0x0a,
      0x08,
      0x01,
      0x06,
      0x00,
      0x41,
      0x00,
      0xfd,
      0x0f,
      0x0b,
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
