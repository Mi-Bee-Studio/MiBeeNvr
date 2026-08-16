import { describe, it, expect } from 'vitest';
import {
  pickCameraMode,
  fallbackChain,
  nextAfter,
  resolveEncoding,
  isProtocolUsable,
  isAudioCapable,
  buildCandidateChain,
  EMPTY_CAPS,
  type ProtocolsResponse,
} from '$lib/stream-selection';
import type { Camera } from '$lib/api';

function makeCamera(over: Partial<Camera> = {}): Camera {
  return { id: 'cam-1', name: 'Test', protocol: 'onvif', ...over } as Camera;
}

// A typical H.264 RTSP/ONVIF camera: backend offers the full real-time set.
const H264_RESP: ProtocolsResponse = {
  encoding: 'h264',
  default: 'webrtc',
  protocols: [
    { protocol: 'webrtc', available: true, reason: '' },
    { protocol: 'flv', available: true, reason: '' },
    { protocol: 'll-hls', available: true, reason: '' },
    { protocol: 'hls', available: true, reason: '' },
    { protocol: 'wasm', available: true, reason: '' },
  ],
};

// H.265 camera: WebRTC/FLV excluded by the backend (can't carry/decode H.265).
const H265_RESP: ProtocolsResponse = {
  encoding: 'h265',
  default: 'hls',
  protocols: [
    { protocol: 'hls', available: true, reason: '' },
    { protocol: 'll-hls', available: true, reason: '' },
    { protocol: 'wasm', available: true, reason: '' },
    { protocol: 'webrtc', available: false, reason: 'WebRTC does not support H.265' },
    { protocol: 'flv', available: false, reason: 'FLV cannot decode H.265 in browser' },
  ],
};

const FULL_CAPS = { h265MSE: true, webCodecs: true, wasmH265: true };

describe('pickCameraMode', () => {
  it('short-circuits JPEG cameras to mjpeg before any HLS gate', () => {
    // ESP32 MiBeeCam: protocol=onvif (hls-capable), but encoding=jpeg.
    const cam = makeCamera({ protocol: 'onvif', encoding: 'jpeg' });
    const resp: ProtocolsResponse = {
      encoding: 'jpeg',
      default: 'mjpeg',
      protocols: [{ protocol: 'mjpeg', available: true, reason: '' }],
    };
    expect(pickCameraMode(cam, resp, FULL_CAPS)).toBe('mjpeg');
  });

  it('short-circuits MJPEG via stream_encoding when encoding is empty', () => {
    const cam = makeCamera({ protocol: 'http', encoding: '', stream_encoding: 'mjpeg' });
    expect(pickCameraMode(cam, null, EMPTY_CAPS)).toBe('mjpeg');
  });

  it('picks backend default (webrtc) for H.264 with full caps', () => {
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    expect(pickCameraMode(cam, H264_RESP, FULL_CAPS)).toBe('webrtc');
  });

  it('honors a valid per-camera override over the backend default', () => {
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    expect(pickCameraMode(cam, H264_RESP, FULL_CAPS, { override: 'flv' })).toBe('flv');
  });

  it('ignores an override that is unusable for the codec (webrtc on h265)', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    // override=webrtc is invalid for h265 → falls back to backend default (hls).
    expect(pickCameraMode(cam, H265_RESP, FULL_CAPS, { override: 'webrtc' })).toBe('hls');
  });

  it('degrades H.265 FLV to HLS when browser lacks H.265 MSE', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    // Backend default forced to flv via override; no MSE → must degrade to hls.
    const resp: ProtocolsResponse = { ...H265_RESP, default: 'flv' };
    expect(pickCameraMode(cam, resp, { h265MSE: false, webCodecs: false }, { override: 'flv' })).toBe('hls');
  });

  it('keeps FLV for H.265 when browser has H.265 MSE', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    const resp: ProtocolsResponse = { ...H265_RESP, default: 'flv' };
    expect(pickCameraMode(cam, resp, FULL_CAPS, { override: 'flv' })).toBe('flv');
  });

  it('routes H.265 HLS/LL-HLS default to wasm when MSE cannot play it (the Chromium/Edge fix)', () => {
    // Regression guard: isTypeSupported('hvc1') is a false positive on
    // Chromium/Edge, so h265MSE=false even though the browser "claims" support.
    // The backend default (hls/ll-hls) would render a permanent black screen via
    // MSE; the player must auto-degrade to the wasm (libde265) path instead.
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    expect(pickCameraMode(cam, H265_RESP, { h265MSE: false, webCodecs: false, wasmH265: true })).toBe('wasm');
    const llResp: ProtocolsResponse = { ...H265_RESP, default: 'll-hls' };
    expect(pickCameraMode(cam, llResp, { h265MSE: false, webCodecs: false, wasmH265: true })).toBe('wasm');
  });

  it('keeps H.265 on HLS when MSE truly supports it', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    expect(pickCameraMode(cam, H265_RESP, FULL_CAPS)).toBe('hls');
  });

  it('keeps H.265 on HLS when MSE lacks it AND wasm is unavailable (no better option)', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    expect(pickCameraMode(cam, H265_RESP, { h265MSE: false, webCodecs: false, wasmH265: false })).toBe('hls');
  });

  it('ignores a wasm override when WebCodecs is unavailable (falls to backend default)', () => {
    // wasm is unusable without WebCodecs → override rejected → backend default (webrtc).
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    expect(pickCameraMode(cam, H264_RESP, { h265MSE: true, webCodecs: false }, { override: 'wasm' })).toBe('webrtc');
  });

  it('degrades a wasm backend default to hls when WebCodecs is unavailable', () => {
    // If the backend somehow defaulted to wasm but WebCodecs is missing → hls.
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    const resp: ProtocolsResponse = { ...H264_RESP, default: 'wasm' };
    expect(pickCameraMode(cam, resp, { h265MSE: true, webCodecs: false })).toBe('hls');
  });

  it('falls back to legacy global default when backend response is null', () => {
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    expect(pickCameraMode(cam, null, EMPTY_CAPS, { legacyDefault: 'flv' })).toBe('flv');
    expect(pickCameraMode(cam, null, EMPTY_CAPS, { legacyDefault: 'll-hls' })).toBe('hls');
    expect(pickCameraMode(cam, null, EMPTY_CAPS, { legacyDefault: 'hls' })).toBe('hls');
  });

  it('returns snapshot when protocol is not HLS-capable (rtmp ingest)', () => {
    // rtmp/srt ingest cameras have no live preview protocol; they fall to snapshot.
    const cam = makeCamera({ protocol: 'rtmp', encoding: 'h264' });
    expect(pickCameraMode(cam, null, EMPTY_CAPS, { isHlsCapable: false })).toBe('snapshot');
  });

  it('returns unsupported when flagged and not HLS-capable', () => {
    const cam = makeCamera({ protocol: 'rtmp', encoding: 'h264' });
    expect(pickCameraMode(cam, null, EMPTY_CAPS, { isHlsCapable: false, isUnsupported: true })).toBe('unsupported');
  });
});

describe('fallbackChain', () => {
  it('orders available protocols by latency (webrtc→flv→hls→mjpeg)', () => {
    expect(fallbackChain(H264_RESP)).toEqual(['webrtc', 'flv', 'hls']);
  });

  it('excludes unavailable protocols', () => {
    // H.265: webrtc/flv unavailable → chain is just hls.
    expect(fallbackChain(H265_RESP)).toEqual(['hls']);
  });

  it('returns empty array for null response', () => {
    expect(fallbackChain(null)).toEqual([]);
  });
});

describe('nextAfter', () => {
  it('returns the next protocol in the chain', () => {
    expect(nextAfter('webrtc', H264_RESP)).toBe('flv');
    expect(nextAfter('flv', H264_RESP)).toBe('hls');
  });

  it('returns null when the chain is exhausted', () => {
    expect(nextAfter('hls', H264_RESP)).toBeNull();
  });

  it('starts from chain[0] when current mode is not in the chain', () => {
    // wasm was forced by legacy default but isn't in the real-time chain.
    expect(nextAfter('wasm', H264_RESP)).toBe('webrtc');
  });

  it('returns null when there is no response', () => {
    expect(nextAfter('flv', null)).toBeNull();
  });
});

describe('resolveEncoding', () => {
  it('prefers backend-probed encoding over stored fields', () => {
    const cam = makeCamera({ encoding: 'h264', stream_encoding: 'h264' });
    expect(resolveEncoding(cam, { encoding: 'h265', default: '', protocols: [] } as ProtocolsResponse)).toBe('h265');
  });

  it('falls back to camera.encoding then stream_encoding', () => {
    expect(resolveEncoding(makeCamera({ encoding: 'h264' }), null)).toBe('h264');
    expect(resolveEncoding(makeCamera({ encoding: '', stream_encoding: 'mjpeg' }), null)).toBe('mjpeg');
  });
});

describe('isProtocolUsable', () => {
  // All protocols advertised as available — isolates the codec/caps gate.
  const avail = new Set(['webrtc', 'flv', 'hls', 'll-hls', 'mjpeg']);

  it('rejects webrtc for h265', () => {
    expect(isProtocolUsable('webrtc', 'h265', FULL_CAPS, avail)).toBe(false);
    expect(isProtocolUsable('webrtc', 'h264', FULL_CAPS, avail)).toBe(true);
  });

  it('rejects flv for h265 without MSE', () => {
    expect(isProtocolUsable('flv', 'h265', { h265MSE: false, webCodecs: true }, avail)).toBe(false);
    expect(isProtocolUsable('flv', 'h265', FULL_CAPS, avail)).toBe(true);
  });

  it('rejects wasm without WebCodecs', () => {
    expect(isProtocolUsable('wasm', 'h264', { h265MSE: true, webCodecs: false }, avail)).toBe(false);
  });

  it('restricts mjpeg to jpeg/mjpeg streams', () => {
    expect(isProtocolUsable('mjpeg', 'jpeg', EMPTY_CAPS, avail)).toBe(true);
    expect(isProtocolUsable('mjpeg', 'h264', EMPTY_CAPS, avail)).toBe(false);
  });

  it('gates hls/ll-hls H.265 on real MSE support (isTypeSupported is a false positive)', () => {
    // H.264 over HLS is always playable (native decode).
    expect(isProtocolUsable('ll-hls', 'h264', EMPTY_CAPS, avail)).toBe(true);
    expect(isProtocolUsable('hls', 'h264', EMPTY_CAPS, avail)).toBe(true);
    // H.265 over HLS needs real MSE H.265 support — without it hls.js connects
    // via MSE but the SourceBuffer silently drops hvc1 bytes (black screen).
    expect(isProtocolUsable('hls', 'h265', EMPTY_CAPS, avail)).toBe(false);
    expect(isProtocolUsable('ll-hls', 'h265', EMPTY_CAPS, avail)).toBe(false);
    expect(isProtocolUsable('hls', 'h265', FULL_CAPS, avail)).toBe(true);
    expect(isProtocolUsable('ll-hls', 'h265', FULL_CAPS, avail)).toBe(true);
  });

  // ─── Backend availability gate (issue #112) ───────────────────────────────
  // A named real-time protocol is usable ONLY if the backend reports it as
  // Available. Without this gate, an empty /protocols response (device down,
  // recorder not started) would still yield [webrtc,flv,hls] from codec reasoning
  // alone, mounting players that storm the backend with requests it can't serve.
  it('rejects every named protocol when the backend reports nothing available', () => {
    const empty = new Set<string>();
    expect(isProtocolUsable('webrtc', 'h264', FULL_CAPS, empty)).toBe(false);
    expect(isProtocolUsable('flv', 'h264', FULL_CAPS, empty)).toBe(false);
    expect(isProtocolUsable('hls', 'h264', FULL_CAPS, empty)).toBe(false);
    expect(isProtocolUsable('ll-hls', 'h264', FULL_CAPS, empty)).toBe(false);
    expect(isProtocolUsable('mjpeg', 'jpeg', FULL_CAPS, empty)).toBe(false);
  });

  it('accepts hls mode when the backend advertises ll-hls (and vice versa)', () => {
    // Backend advertises HLS as 'll-hls'; the hls render mode must still match.
    expect(isProtocolUsable('hls', 'h264', FULL_CAPS, new Set(['ll-hls']))).toBe(true);
    expect(isProtocolUsable('ll-hls', 'h264', FULL_CAPS, new Set(['hls']))).toBe(true);
  });
});

describe('isAudioCapable', () => {
  it('returns false for MJPEG/JPEG cameras (video-only)', () => {
    expect(isAudioCapable(makeCamera({ encoding: 'mjpeg', audio_enabled: true }))).toBe(false);
    expect(isAudioCapable(makeCamera({ encoding: 'jpeg', audio_enabled: true }))).toBe(false);
    // stream_encoding fallback (ESP32 MiBeeCam).
    expect(isAudioCapable(makeCamera({ encoding: '', stream_encoding: 'jpeg', audio_enabled: true }))).toBe(false);
  });

  it('returns false when audio_enabled is not explicitly true', () => {
    expect(isAudioCapable(makeCamera({ encoding: 'h264', audio_enabled: false }))).toBe(false);
    expect(isAudioCapable(makeCamera({ encoding: 'h264' }))).toBe(false); // undefined -> false
  });

  it('returns true for H.264/H.265 cameras with audio_enabled', () => {
    expect(isAudioCapable(makeCamera({ encoding: 'h264', audio_enabled: true }))).toBe(true);
    expect(isAudioCapable(makeCamera({ encoding: 'h265', audio_enabled: true }))).toBe(true);
  });
});

describe('buildCandidateChain', () => {
  it('returns a single-element mjpeg chain for JPEG cameras', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'jpeg' });
    const chain = buildCandidateChain(cam, null, FULL_CAPS);
    expect(chain.map((c) => c.mode)).toEqual(['mjpeg']);
    expect(chain[0].pinned).toBe(false);
  });

  it('orders by latency (wasm > webrtc > flv > hls) for H.264 with full caps', () => {
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    const chain = buildCandidateChain(cam, H264_RESP, FULL_CAPS);
    // Full caps → wasm leads (frontend-only, gated on webCodecs), then the
    // backend-advertised real-time modes in preference order.
    expect(chain.map((c) => c.mode)).toEqual(['wasm', 'webrtc', 'flv', 'hls']);
  });

  it('excludes webrtc, flv AND hls for H.265 without MSE (codec gate)', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    const chain = buildCandidateChain(cam, H265_RESP, { h265MSE: false, webCodecs: true, wasmH265: true });
    // wasm only — webrtc/flv/hls all blocked for H.265 without MSE H.265.
    // (HLS via MSE can't decode H.265 on most browsers → black screen.)
    expect(chain.map((c) => c.mode)).toEqual(['wasm']);
  });

  it('excludes wasm when neither WebCodecs nor libde265 WASM is available', () => {
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    const chain = buildCandidateChain(cam, H264_RESP, { h265MSE: true, webCodecs: false, wasmH265: false });
    expect(chain.map((c) => c.mode)).toEqual(['webrtc', 'flv', 'hls']);
    expect(chain.find((c) => c.mode === 'wasm')).toBeUndefined();
  });

  it('pins a single-element chain for a valid user override', () => {
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    const chain = buildCandidateChain(cam, H264_RESP, FULL_CAPS, { override: 'flv' });
    expect(chain.map((c) => c.mode)).toEqual(['flv']);
    expect(chain[0].pinned).toBe(true);
  });

  it('ignores an unusable override and falls through to auto-selection', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    // webrtc is unusable for h265 → override rejected → full auto chain.
    const chain = buildCandidateChain(cam, H265_RESP, FULL_CAPS, { override: 'webrtc' });
    expect(chain.find((c) => c.mode === 'webrtc')).toBeUndefined();
    expect(chain.every((c) => !c.pinned)).toBe(true);
  });

  // ─── Issue #112: empty /protocols must not fabricate a chain ───────────────
  // The root cause of the protocol storm: an ONVIF camera whose recorder is
  // briefly down returns {protocols:[]} + encoding="". Without the availability
  // gate, buildCandidateChain fabricated [webrtc,flv,hls] and mounted players
  // that stormed the backend. Now the empty list yields an empty chain → the
  // grid falls to snapshot (with re-fetch backoff, see Surveillance.svelte).
  it('returns an empty chain when the backend reports no available protocols', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: '' });
    const emptyResp: ProtocolsResponse = { protocols: [], encoding: '', default: '' };
    const chain = buildCandidateChain(cam, emptyResp, FULL_CAPS);
    expect(chain).toEqual([]);
  });

  it('returns an empty chain for an h264 camera when the backend reports nothing available', () => {
    // Even with a known codec, if the backend says it can't serve any protocol,
    // don't mount a real-time player — the stream doesn't exist right now.
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h264' });
    const emptyResp: ProtocolsResponse = { protocols: [], encoding: 'h264', default: '' };
    const chain = buildCandidateChain(cam, emptyResp, FULL_CAPS);
    expect(chain).toEqual([]);
  });

  it('rejects a user override when the backend reports no available protocols', () => {
    // A stale pinned override (e.g. 'hls' from when the camera was healthy)
    // must not force a player onto a camera the backend says it can't serve.
    const cam = makeCamera({ protocol: 'onvif', encoding: '' });
    const emptyResp: ProtocolsResponse = { protocols: [], encoding: '', default: '' };
    const chain = buildCandidateChain(cam, emptyResp, FULL_CAPS, { override: 'hls' });
    expect(chain).toEqual([]);
  });

  it('returns an empty chain for non-HLS-capable protocols', () => {
    const cam = makeCamera({ protocol: 'rtmp', encoding: 'h264' });
    const chain = buildCandidateChain(cam, null, EMPTY_CAPS, { isHlsCapable: false });
    expect(chain).toEqual([]);
  });

  it('falls back to the legacy default as a single-element chain when backend is null', () => {
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    const chain = buildCandidateChain(cam, null, EMPTY_CAPS, { legacyDefault: 'flv' });
    expect(chain.map((c) => c.mode)).toEqual(['flv']);
  });

  // ─── Issue #107: !resp must not collapse H.265 onto HLS ────────────────────
  // When /protocols transiently fails (resp=null) the old code returned a forced
  // single-`hls` chain REGARDLESS of codec. For an H.265 camera that's a death
  // sentence: hls.js feeds hvc1 to MSE, which claims isTypeSupported but silently
  // fails to decode → permanent black screen + the loading↔buffering state
  // oscillation that froze browsers. When the browser can actually decode H.265
  // (WebCodecs/wasm), prefer the wasm mode instead — a single-element chain that
  // can't demote into the HLS trap.
  it('returns a single-element wasm chain for H.265 + WebCodecs when resp is null (no HLS trap)', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    const chain = buildCandidateChain(cam, null, { h265MSE: false, webCodecs: true, wasmH265: false });
    expect(chain.map((c) => c.mode)).toEqual(['wasm']);
  });

  it('returns a single-element wasm chain for H.265 + libde265 WASM when resp is null', () => {
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    const chain = buildCandidateChain(cam, null, { h265MSE: false, webCodecs: false, wasmH265: true });
    expect(chain.map((c) => c.mode)).toEqual(['wasm']);
  });

  it('falls back to HLS for H.265 when resp is null AND no H.265 decode path exists', () => {
    // No WebCodecs, no wasm → there is no good option. HLS at least avoids the
    // oscillation (the player will fail fast). This preserves pre-fix behavior
    // for the rare browser that can't decode H.265 at all.
    const cam = makeCamera({ protocol: 'onvif', encoding: 'h265' });
    const chain = buildCandidateChain(cam, null, { h265MSE: false, webCodecs: false, wasmH265: false });
    expect(chain.map((c) => c.mode)).toEqual(['hls']);
  });

  it('still returns HLS for H.264 when resp is null (unchanged behavior)', () => {
    // Regression guard: the !resp H.265 fix must NOT change H.264 behavior.
    const cam = makeCamera({ protocol: 'rtsp', encoding: 'h264' });
    const chain = buildCandidateChain(cam, null, EMPTY_CAPS);
    expect(chain.map((c) => c.mode)).toEqual(['hls']);
  });
});
