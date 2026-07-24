import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createPlayerOrchestrator, makeRegistration, type CameraRegistration } from '$lib/player/orchestrator.svelte';
import { health } from '$lib/player/health';
import type { Camera, ProtocolsResponse } from '$lib/stream-selection';

// ─── Fixtures ───────────────────────────────────────────────────────────────

const H264_CAMERA: Camera = { id: 'cam-1', name: 'T', protocol: 'rtsp', encoding: 'h264' } as Camera;

// Backend offers webrtc < flv < hls — a full chain for an H.264 camera.
const H264_RESP: ProtocolsResponse = {
  encoding: 'h264',
  default: 'webrtc',
  protocols: [
    { Protocol: 'webrtc', Available: true, Reason: '' },
    { Protocol: 'flv', Available: true, Reason: '' },
    { Protocol: 'll-hls', Available: true, Reason: '' },
    { Protocol: 'hls', Available: true, Reason: '' },
  ],
};

// Full caps so wasm leads the chain (caps are read from the cache; we stub it).
function reg(
  camera = H264_CAMERA,
  resp: ProtocolsResponse | null = H264_RESP,
  override: string | null = null,
): CameraRegistration {
  return makeRegistration(camera, resp, { override, isHlsCapable: true, isUnsupported: false });
}

// ─── Timing: use fake timers so the debounce/cooldown logic is deterministic ─

beforeEach(() => {
  vi.useFakeTimers();
  // Pre-seed the capability cache with full caps so wasm is selectable.
  // capabilities-cache reads sessionStorage; we write a fresh entry per test.
  try {
    sessionStorage.setItem(
      'mibee_nvr_device_caps',
      JSON.stringify({
        webCodecs: true,
        hevcDecode: true,
        webgpu: true,
        webgl2: true,
        mseH265: true,
        wasmH265: true,
        probedAt: Date.now(),
      }),
    );
  } catch {
    /* sessionStorage may be unavailable in some test envs — skip */
  }
});

afterEach(() => {
  vi.useRealTimers();
  try {
    sessionStorage.removeItem('mibee_nvr_device_caps');
  } catch {
    /* noop */
  }
});

// ─── Tests ──────────────────────────────────────────────────────────────────

describe('PlayerOrchestrator — registration & active mode', () => {
  it('exposes the head of the candidate chain as the initial active mode', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    // With full caps the chain head is wasm (lowest latency, WebCodecs available).
    expect(o.activeMode('cam-1')).toBe('wasm');
    o.dispose();
  });

  it('returns null for an unknown camera', () => {
    const o = createPlayerOrchestrator();
    expect(o.activeMode('nope')).toBeNull();
    o.dispose();
  });

  it('a user override pins the chain but falls back to HLS on terminal failure', () => {
    // A pinned protocol that CANNOT work (e.g. WebRTC 503 for a Xiaomi CS2
    // camera) must NOT leave the user on a permanent black screen. The
    // orchestrator preserves the localStorage override but switches the active
    // mode to a workable fallback (HLS) so the camera keeps showing something.
    const o = createPlayerOrchestrator();
    o.registerCamera(reg(H264_CAMERA, H264_RESP, 'flv'));
    expect(o.activeMode('cam-1')).toBe('flv');
    expect(o.slot('cam-1')?.pinned).toBe(true);
    // failed → fall back to HLS (universal), not stuck on the broken pinned flv.
    o.reportHealth('cam-1', health('failed', 'fatal-error'));
    expect(o.activeMode('cam-1')).toBe('hls');
    expect(o.slot('cam-1')?.pinned).toBe(false);
    o.dispose();
  });

  it('a pinned protocol that reports ok/degraded stays pinned (only failed escapes)', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg(H264_CAMERA, H264_RESP, 'flv'));
    // ok and degraded must NOT escape the pin (only a terminal failure does).
    o.reportHealth('cam-1', health('ok'));
    expect(o.activeMode('cam-1')).toBe('flv');
    expect(o.slot('cam-1')?.pinned).toBe(true);
    o.reportHealth('cam-1', health('degraded', 'buffering'));
    expect(o.activeMode('cam-1')).toBe('flv');
    expect(o.slot('cam-1')?.pinned).toBe(true);
    o.dispose();
  });
});

describe('PlayerOrchestrator — degrade', () => {
  it('demotes immediately on a failed health report', () => {
    const o = createPlayerOrchestrator();
    const events: string[] = [];
    o.onModeChange((_id, from, to, reason) => events.push(`${from}->${to}(${reason})`));
    o.registerCamera(reg());
    expect(o.activeMode('cam-1')).toBe('wasm');
    o.reportHealth('cam-1', health('failed', 'fatal-error'));
    expect(o.activeMode('cam-1')).toBe('webrtc');
    expect(events).toEqual(['wasm->webrtc(fatal-error)']);
    o.dispose();
  });

  it('debounces degraded health — does NOT demote within the threshold', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.reportHealth('cam-1', health('degraded', 'no-frames'));
    // Immediately after — still on wasm (timer armed, not fired).
    expect(o.activeMode('cam-1')).toBe('wasm');
    o.dispose();
  });

  it('demotes after the degrade threshold of continuous degraded', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.reportHealth('cam-1', health('degraded', 'no-frames'));
    vi.advanceTimersByTime(8000);
    expect(o.activeMode('cam-1')).toBe('webrtc');
    o.dispose();
  });

  it('cancels the degrade timer when health returns to ok', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.reportHealth('cam-1', health('degraded', 'no-frames'));
    vi.advanceTimersByTime(4000);
    o.reportHealth('cam-1', health('ok'));
    vi.advanceTimersByTime(8000); // well past the threshold
    expect(o.activeMode('cam-1')).toBe('wasm');
    o.dispose();
  });

  it('stops at the end of the chain (no demotion past the last entry)', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    // Repeatedly fail to walk the whole chain down to HLS.
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('webrtc');
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('flv');
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('hls');
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('hls'); // chain exhausted
    o.dispose();
  });
});

describe('PlayerOrchestrator — upgrade (anti-flap)', () => {
  it('does NOT upgrade on a manual request when not stable enough… actually manual bypasses stability', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('webrtc');
    // Manual requestUpgrade bypasses the stability window.
    o.requestUpgrade('cam-1');
    expect(o.activeMode('cam-1')).toBe('wasm');
    o.dispose();
  });

  it('auto-upgrade only fires after the stable window AND an environment trigger', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('webrtc');
    // Report ok and let it stabilize.
    o.reportHealth('cam-1', health('ok'));
    vi.advanceTimersByTime(30000); // past UPGRADE_STABLE_MS
    // No trigger yet → still on webrtc (no polling).
    expect(o.activeMode('cam-1')).toBe('webrtc');
    // wasm is cooled for 60s after the demote — advance past that too so the
    // tab-visible trigger can actually promote.
    vi.advanceTimersByTime(60000);
    o.reportHealth('cam-1', health('ok')); // refresh stability window
    vi.advanceTimersByTime(30000);
    // Now an environment trigger: tab visible.
    o.setTabVisible(false);
    o.setTabVisible(true);
    expect(o.activeMode('cam-1')).toBe('wasm');
    o.dispose();
  });

  it('reverts a promotion that does not reach ok within the probe window and cools the entry (auto)', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    // Demote wasm→webrtc.
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('webrtc');
    // Stabilize, then a tab-visible trigger promotes back to wasm.
    o.reportHealth('cam-1', health('ok'));
    vi.advanceTimersByTime(60000); // past stability + wasm cooldown
    o.reportHealth('cam-1', health('ok'));
    vi.advanceTimersByTime(30000);
    o.setTabVisible(false);
    o.setTabVisible(true);
    expect(o.activeMode('cam-1')).toBe('wasm'); // promoted
    // Do NOT report ok; advance past the probe window.
    vi.advanceTimersByTime(5000);
    // Reverted to webrtc.
    expect(o.activeMode('cam-1')).toBe('webrtc');
    // wasm is now cooled again: an AUTO upgrade (tab-visible) must NOT re-promote.
    o.reportHealth('cam-1', health('ok'));
    vi.advanceTimersByTime(30000);
    o.setTabVisible(false);
    o.setTabVisible(true);
    expect(o.activeMode('cam-1')).toBe('webrtc'); // still cooled
    o.dispose();
  });

  it('a MANUAL requestUpgrade bypasses cooldown (explicit user intent)', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('webrtc');
    // wasm is cooled, but the user explicitly asks → bypass.
    o.requestUpgrade('cam-1');
    expect(o.activeMode('cam-1')).toBe('wasm');
    o.dispose();
  });

  it('does not upgrade while the tab is hidden', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('webrtc');
    // Promote back via tab-visible (advance past cooldown first).
    o.reportHealth('cam-1', health('ok'));
    vi.advanceTimersByTime(60000);
    o.reportHealth('cam-1', health('ok'));
    vi.advanceTimersByTime(30000);
    o.setTabVisible(false);
    o.setTabVisible(true); // visible again → upgrade to wasm
    expect(o.activeMode('cam-1')).toBe('wasm');
    // Now hide and demote again, then try to upgrade while hidden.
    o.setTabVisible(false);
    o.reportHealth('cam-1', health('failed'));
    expect(o.activeMode('cam-1')).toBe('webrtc'); // demoted
    o.reportHealth('cam-1', health('ok'));
    vi.advanceTimersByTime(90000); // past stability + cooldown
    // Hidden now → no auto upgrade, no manual upgrade.
    expect(o.activeMode('cam-1')).toBe('webrtc');
    o.requestUpgrade('cam-1');
    expect(o.activeMode('cam-1')).toBe('webrtc'); // manual also blocked while hidden
    o.dispose();
  });
});

describe('PlayerOrchestrator — setOverride / unregister / dispose', () => {
  it('setOverride rebuilds the chain and pins when usable', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    expect(o.activeMode('cam-1')).toBe('wasm');
    o.setOverride('cam-1', 'hls');
    expect(o.activeMode('cam-1')).toBe('hls');
    expect(o.slot('cam-1')?.pinned).toBe(true);
    o.dispose();
  });

  it('clearing the override returns to auto-selection', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.setOverride('cam-1', 'hls');
    o.setOverride('cam-1', null);
    expect(o.activeMode('cam-1')).toBe('wasm');
    expect(o.slot('cam-1')?.pinned).toBe(false);
    o.dispose();
  });

  it('unregister removes the camera and cancels its timers', () => {
    const o = createPlayerOrchestrator();
    o.registerCamera(reg());
    o.reportHealth('cam-1', health('degraded'));
    o.unregisterCamera('cam-1');
    expect(o.activeMode('cam-1')).toBeNull();
    // Advancing timers must not throw on the removed slot.
    expect(() => vi.advanceTimersByTime(8000)).not.toThrow();
    o.dispose();
  });

  it('dispose clears all timers and callbacks', () => {
    const o = createPlayerOrchestrator();
    const cb = vi.fn();
    const unsub = o.onModeChange(cb);
    o.registerCamera(reg());
    o.dispose();
    unsub();
    // After dispose, reportHealth is a no-op (no throw, no callback).
    expect(() => o.reportHealth('cam-1', health('failed'))).not.toThrow();
    expect(cb).not.toHaveBeenCalled();
  });
});
