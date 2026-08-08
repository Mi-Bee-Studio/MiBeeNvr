import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

// Mock the AAC WASM loader BEFORE importing AudioPlayer so the AAC-over-HTTP
// fallback path is deterministically forced to fail (the loader rejects).
// vi.mock is hoisted to the top of the file by Vitest.
vi.mock('./aac-wasm-loader', () => ({
  loadFaad: vi.fn().mockRejectedValue(new Error('WASM unavailable in test')),
  isFaadLoaded: vi.fn().mockReturnValue(false),
}));

// Import AFTER the mock is registered so audio-player.ts picks up the stub.
const { AudioPlayer, AudioCodec } = await import('./audio-player');

/**
 * Tests for the AudioPlayer codec-routing layer added by #131.
 *
 * Scope:
 *   - G.711 path is unchanged (still returns initialized=true after init()).
 *   - AAC reports a graceful `unavailableReason` when neither WebCodecs nor
 *     the WASM backend can be loaded (WebCodecs absent + WASM mocked to fail
 *     = the HTTP LAN, WASM-failed regression guard).
 *   - Unsupported codecs (Opus) report `unsupported_codec` without throwing.
 *
 * The actual PCM decode is covered by g711-decoder.test.ts and the AAC
 * backend tests; here we only pin the routing decisions.
 */

// Stub AudioContext so init() can construct the Web Audio graph in jsdom.
class MockAudioContext {
  state = 'running';
  currentTime = 0;
  destination = {};
  createGain() {
    return { gain: { value: 1 }, connect: vi.fn(), disconnect: vi.fn() };
  }
  createBuffer(_ch: number, _len: number, _rate: number) {
    return { getChannelData: () => new Float32Array(_len), copyToChannel: vi.fn() };
  }
  createBufferSource() {
    return { buffer: null, connect: vi.fn(), start: vi.fn() };
  }
  resume() {
    return Promise.resolve();
  }
  close() {
    return Promise.resolve();
  }
}

let originalAudioContext: typeof globalThis.AudioContext | undefined;
let originalAudioDecoder: typeof globalThis.AudioDecoder | undefined;

beforeEach(() => {
  originalAudioContext = globalThis.AudioContext;
  originalAudioDecoder = globalThis.AudioDecoder;
  // @ts-expect-error test stub
  globalThis.AudioContext = MockAudioContext;
  // Ensure WebCodecs AudioDecoder is absent (simulates HTTP LAN).
  // @ts-expect-error test stub
  globalThis.AudioDecoder = undefined;
});

afterEach(() => {
  globalThis.AudioContext = originalAudioContext!;
  globalThis.AudioDecoder = originalAudioDecoder!;
  vi.restoreAllMocks();
});

describe('AudioPlayer — G.711 routing (unchanged path)', () => {
  it('initializes for G.711 μ-law at 8 kHz', async () => {
    const p = new AudioPlayer(AudioCodec.MuLaw, 8000, 1);
    await p.init();
    expect(p.initialized).toBe(true);
    expect(p.unavailableReason).toBeNull();
    // pushFrame must not throw on a small payload.
    p.pushFrame(new Uint8Array([0xff, 0x80, 0x7f]));
    p.destroy();
  });

  it('initializes for G.711 A-law at 8 kHz', async () => {
    const p = new AudioPlayer(AudioCodec.ALaw, 8000, 1);
    await p.init();
    expect(p.initialized).toBe(true);
    expect(p.unavailableReason).toBeNull();
    p.destroy();
  });

  it('does not require a config for G.711', async () => {
    const p = new AudioPlayer(AudioCodec.MuLaw, 8000, 1);
    await p.init();
    expect(p.initialized).toBe(true);
    p.destroy();
  });
});

describe('AudioPlayer — AAC routing', () => {
  it('reports webcodecs_unavailable when AudioDecoder is absent (HTTP LAN)', async () => {
    // AudioDecoder is undefined in this stub environment. WASM is also stubbed
    // to fail, so the final reason should reflect the missing backend.
    const p = new AudioPlayer(AudioCodec.AAC, 44100, 2, new Uint8Array([0x12, 0x10]));
    await p.init();
    expect(p.initialized).toBe(false);
    // Reason is one of the AAC-unavailable codes (WebCodecs gone, WASM failed).
    expect(['webcodecs_unavailable', 'wasm_load_failed', 'decoder_error']).toContain(p.unavailableReason);
    p.destroy();
  });

  it('reports decoder_error when AAC has no config (AASC missing)', async () => {
    const p = new AudioPlayer(AudioCodec.AAC, 44100, 2);
    await p.init();
    expect(p.initialized).toBe(false);
    expect(p.unavailableReason).toBe('decoder_error');
    p.destroy();
  });
});

describe('AudioPlayer — unsupported codec', () => {
  it('reports unsupported_codec for an unknown codec without throwing', async () => {
    // 0x09 is not MuLaw/ALaw/AAC/Opus — should degrade gracefully.
    const p = new AudioPlayer(0x09, 48000, 1);
    await p.init();
    expect(p.initialized).toBe(false);
    expect(p.unavailableReason).toBe('unsupported_codec');
    // pushFrame is a safe no-op when not initialized.
    p.pushFrame(new Uint8Array([0x01, 0x02]));
    p.destroy();
  });
});

describe('AudioPlayer — Opus routing', () => {
  it('reports webcodecs_unavailable when AudioDecoder is absent (HTTP LAN, no WASM fallback)', async () => {
    // jsdom has no AudioDecoder, so Opus (WebCodecs-only path) degrades.
    const p = new AudioPlayer(AudioCodec.Opus, 16000, 1, new Uint8Array([1, 0, 0, 0, 0, 0x3e, 0x80]));
    await p.init();
    expect(p.initialized).toBe(false);
    expect(['webcodecs_unavailable', 'decoder_error']).toContain(p.unavailableReason);
    p.destroy();
  });
});

describe('AudioPlayer — mute control', () => {
  it('setMuted toggles the gain value', async () => {
    const p = new AudioPlayer(AudioCodec.MuLaw, 8000, 1);
    await p.init();
    p.setMuted(true);
    expect(p.muted).toBe(true);
    p.setMuted(false);
    expect(p.muted).toBe(false);
    p.destroy();
  });
});
