/**
 * Stream protocol auto-selection for the surveillance grid.
 *
 * The backend exposes a codec-aware, per-camera protocol ranking at
 * `GET /api/cameras/{id}/protocols` — it probes the running recorder for the
 * REAL codec (correcting ONVIF cameras that lie), checks which stream handlers
 * can serve that codec, and returns `{protocols[], encoding, default}` ordered
 * by latency (webrtc → flv → ll-hls → hls → mjpeg). This module consumes that
 * response, folds in browser-capability gating (which the backend can't know),
 * and produces the concrete playback mode the grid should render.
 *
 * Keeping this logic in a pure module (no Svelte / no I/O) makes it unit-testable
 * and lets it be shared between the surveillance grid and any future surface.
 */

import type { Camera } from '$lib/api';

/** The concrete playback mode the grid renders for a cell. */
export type CameraMode = 'wasm' | 'webrtc' | 'flv' | 'hls' | 'mjpeg' | 'snapshot' | 'unsupported';

/**
 * Whether a camera can plausibly produce an audio track for live preview.
 * MJPEG/JPEG cameras (HTTP JPEG recorders, ESP32 MiBeeCam) are video-only —
 * the audio WebSocket never sends AudioCodecInfo for them, so rendering the
 * speaker button is misleading (clicking it silently does nothing).
 * Also gated on the per-camera `audio_enabled` flag (default false).
 */
export function isAudioCapable(camera: Camera): boolean {
  const enc = (camera.encoding || camera.stream_encoding || '').toLowerCase();
  if (enc === 'mjpeg' || enc === 'jpeg') return false;
  return camera.audio_enabled === true;
}

/** Mirrors the backend `cameraProtocolsResponse` (internal/api/handler.go:533). */
export interface ProtocolDetail {
  Protocol: string;
  Available: boolean;
  Reason: string;
}

export interface ProtocolsResponse {
  protocols: ProtocolDetail[];
  encoding: string;
  default: string;
}

/** Browser capability inputs that influence selection. */
export interface BrowserCaps {
  /** MSE can decode H.265 — when false, FLV renders black on H.265 streams. */
  h265MSE: boolean;
  /** WebCodecs VideoDecoder present — enables the WebCodecs player (HTTPS/localhost). */
  webCodecs: boolean;
  /** libde265 WASM soft-decoder available — enables H.265 on plain HTTP via Canvas2D. */
  wasmH265: boolean;
}

/** Empty (most-conservative) browser capability set — used as a safe fallback. */
export const EMPTY_CAPS: BrowserCaps = { h265MSE: false, webCodecs: false, wasmH265: false };

/**
 * Ordered preference of real-time streaming protocols for runtime fallback.
 * When the selected protocol fails at runtime, the grid demotes to the next
 * entry in this list; only when the list is exhausted does it fall to snapshot.
 * Latency-optimal first, most-compatible last.
 */
export const FALLBACK_ORDER: readonly CameraMode[] = ['webrtc', 'flv', 'hls', 'mjpeg'] as const;

/**
 * Build the runtime fallback chain for a camera given its backend protocol list.
 * Only protocols the backend reports Available are eligible; the order follows
 * FALLBACK_ORDER. Returns at least an empty array when nothing is available
 * (caller then degrades to snapshot/unsupported).
 */
export function fallbackChain(resp: ProtocolsResponse | null): CameraMode[] {
  if (!resp) return [];
  const available = new Set(resp.protocols.filter((p) => p.Available).map((p) => p.Protocol.toLowerCase()));
  const chain: CameraMode[] = [];
  for (const proto of FALLBACK_ORDER) {
    if (available.has(proto)) chain.push(proto);
  }
  return chain;
}

/** Lowercase, trimmed encoding from whichever field is populated. */
export function resolveEncoding(camera: Camera, resp: ProtocolsResponse | null): string {
  // The backend probed encoding (authoritative — it read the live recorder)
  // outranks the stored fields, which may be empty (ESP32 MiBeeCam) or stale.
  const enc = (resp?.encoding || camera.encoding || camera.stream_encoding || '').toLowerCase();
  return enc;
}

/**
 * Decide the next protocol to try after `current` failed at runtime.
 * Returns null when the chain is exhausted (caller should go to snapshot).
 */
export function nextAfter(current: CameraMode, resp: ProtocolsResponse | null): CameraMode | null {
  const chain = fallbackChain(resp);
  const idx = chain.indexOf(current);
  if (idx === -1) {
    // Current mode isn't in the backend's available list (e.g. it was forced by
    // a global default). Start from the beginning of the real chain.
    return chain.length > 0 ? chain[0] : null;
  }
  if (idx + 1 < chain.length) return chain[idx + 1];
  return null;
}

/**
 * Pick the best playback mode for a camera. This is the primary hook for
 * "smart protocol auto-selection" in the surveillance grid.
 *
 * Decision cascade (in order):
 *  1. JPEG/MJPEG cameras → always `mjpeg` (HLS/FLV/WebRTC need H.264/H.265).
 *     MUST run before any HLS-capability gate: ONVIF JPEG delegates report
 *     protocol="onvif" (capabilities.hls=true) but stream JPEG, so HLS would
 *     connect to a non-existent H.264 stream and render black.
 *  2. If the protocol isn't HLS-capable at all → snapshot/unsupported.
 *  3. Per-camera user override (localStorage) if still available for this codec.
 *  4. Backend-computed default, refined by browser capability:
 *     - H.265 + no H.265 MSE → degrade FLV/WebRTC to HLS (black-screen guard).
 *     - WebCodecs selected/available only when browser supports it.
 *  5. Backend default unavailable or fetch failed → legacy global default.
 *
 * @param override  Per-camera user override from localStorage, or null.
 * @param legacyDefault  The global `streaming.default_protocol` (fallback only).
 */
export function pickCameraMode(
  camera: Camera,
  resp: ProtocolsResponse | null,
  caps: BrowserCaps,
  opts: {
    override?: string | null;
    legacyDefault?: string;
    isHlsCapable?: boolean;
    isUnsupported?: boolean;
  } = {},
): CameraMode {
  const proto = (camera.protocol || '').toLowerCase();
  const enc = resolveEncoding(camera, resp);

  // (1) JPEG/MJPEG short-circuit.
  if (proto === 'http' || enc === 'mjpeg' || enc === 'jpeg') {
    return 'mjpeg';
  }

  // (2) Protocol with no HLS path at all.
  const isHlsCapable = opts.isHlsCapable ?? true;
  if (!isHlsCapable) {
    return opts.isUnsupported ? 'unsupported' : 'snapshot';
  }

  const available = new Set(resp ? resp.protocols.filter((p) => p.Available).map((p) => p.Protocol.toLowerCase()) : []);
  const backendDefault = (resp?.default || '').toLowerCase();

  // Choose the "candidate" protocol: override > backend default > legacy.
  let candidate: string;
  if (opts.override && isProtocolUsable(opts.override, enc, caps, available)) {
    candidate = opts.override.toLowerCase();
  } else if (backendDefault) {
    candidate = backendDefault;
  } else {
    candidate = (opts.legacyDefault || 'hls').toLowerCase();
  }

  // Map backend protocol names to a concrete mode, applying browser caps.
  // ll-hls and hls both map to the HLS player (hls.js handles low-latency).
  if (candidate === 'll-hls' || candidate === 'hls') return 'hls';
  if (candidate === 'mjpeg') return 'mjpeg';

  if (candidate === 'webrtc') {
    // WebRTC can't carry H.265 — if the camera is H.265, it can't have been
    // advertised by the backend as Available (handler gates it), but guard
    // anyway for the override/legacy path.
    if (enc === 'h265') return 'hls';
    return 'webrtc';
  }

  if (candidate === 'flv') {
    // H.265 + no MSE H.265 → FLV renders black (mpegts.js can't decode). Degrade.
    if (enc === 'h265' && !caps.h265MSE) return 'hls';
    return 'flv';
  }

  if (candidate === 'wasm') {
    // WebCodecs player (HTTPS/localhost) for any codec, OR libde265 WASM
    // fallback for H.265 on plain HTTP. Without either, fall to HLS.
    if (caps.webCodecs) return 'wasm';
    if (enc === 'h265' && caps.wasmH265) return 'wasm';
    return 'hls';
  }

  // Unknown candidate — safest universal default.
  return 'hls';
}

/**
 * Is a given protocol actually usable given codec + browser caps?
 * Used to validate a user override before honoring it.
 */
export function isProtocolUsable(
  protocol: string,
  encoding: string,
  caps: BrowserCaps,
  available: Set<string>,
): boolean {
  const p = protocol.toLowerCase();
  const enc = encoding.toLowerCase();

  // WebRTC: H.264 only (backend handler enforces this, but the override path
  // may not have consulted the backend).
  if (p === 'webrtc') return enc !== 'h265';
  // FLV: H.265 needs MSE H.265, else black screen.
  if (p === 'flv') return enc !== 'h265' || caps.h265MSE;
  // WebCodecs (HTTPS) for any codec, or libde265 WASM fallback for H.265 (HTTP).
  if (p === 'wasm') return caps.webCodecs || (enc === 'h265' && caps.wasmH265);
  // MJPEG: only for JPEG/MJPEG streams.
  if (p === 'mjpeg') return enc === 'mjpeg' || enc === 'jpeg';
  // HLS / LL-HLS: universally usable for H.264/H.265 (browsers decode natively).
  if (p === 'hls' || p === 'll-hls') return true;

  // If the backend explicitly listed it as available, trust it.
  return available.has(p);
}
