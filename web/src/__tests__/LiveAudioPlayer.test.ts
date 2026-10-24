import { render, cleanup, waitFor, fireEvent } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import LiveAudioPlayer from '$lib/components/LiveAudioPlayer.svelte';

// Mock i18n: echo keys (assertions match key fragments).
vi.mock('$lib/i18n', () => ({ t: (k: string) => k }));
vi.mock('$lib/api', () => ({
  appendAuthToken: (url: string) => url + '?token=t',
  API_BASE: '/api',
}));
// Mock lucide icons.
vi.mock('lucide-svelte', () => ({
  Play: () => document.createElement('span'),
  Square: () => document.createElement('span'),
  Loader2: () => document.createElement('span'),
  RotateCw: () => document.createElement('span'),
}));

function wavHeader(rate = 8000, channels = 1): Uint8Array {
  const h = new Uint8Array(44);
  const dv = new DataView(h.buffer);
  const ws = (o: number, s: string) => { for (let i = 0; i < s.length; i++) h[o + i] = s.charCodeAt(i); };
  ws(0, 'RIFF'); dv.setUint32(4, 0xFFFFFFFF, true); ws(8, 'WAVEfmt ');
  dv.setUint32(16, 16, true); dv.setUint16(20, 1, true); dv.setUint16(22, channels, true);
  dv.setUint32(24, rate, true); dv.setUint32(28, rate * channels * 2, true);
  dv.setUint16(32, channels * 2, true); dv.setUint16(34, 16, true);
  ws(36, 'data'); dv.setUint32(40, 0xFFFFFFFF, true);
  return h;
}

// A stream that emits the header, then 0.2s PCM chunks in real-time-ish ticks.
function stream() {
  const header = wavHeader();
  const chunk = new Uint8Array(3200); // 0.2s of mono 16-bit
  return new ReadableStream<Uint8Array>({
    async start(c) {
      c.enqueue(header);
      await new Promise((r) => setTimeout(r, 10));
      c.enqueue(chunk);
      await new Promise((r) => setTimeout(r, 10));
      c.enqueue(chunk);
    },
  });
}

const gainConnect = vi.fn();
const createGain = vi.fn(() => ({ connect: gainConnect }));
const bufferSources: Array<{ buf: unknown; started: number | null; connect: ReturnType<typeof vi.fn>; start: (t: number) => void; disconnect: () => void; onended: (() => void) | null }> = [];
const createBuffer = vi.fn((ch: number, frames: number, rate: number) => ({
  ch, frames, rate,
  getChannelData: () => new Float32Array(frames),
}));
const startCalls: number[] = [];

function installFakeAudio() {
  (window as unknown as Record<string, unknown>).AudioContext = function () {
    return {
      state: 'running',
      resume: vi.fn(() => Promise.resolve()),
      currentTime: 1.0,
      destination: {},
      createGain,
      createBuffer,
      createBufferSource: vi.fn(() => {
        const src = {
          buf: null as unknown,
          started: null as number | null,
          connect: vi.fn(),
          start: (t: number) => { src.started = t; startCalls.push(t); },
          disconnect: vi.fn(),
          onended: null as (() => void) | null,
        };
        bufferSources.push(src);
        return src;
      }),
    };
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  delete (window as unknown as Record<string, unknown>).AudioContext;
});

describe('LiveAudioPlayer', () => {
  it('plays through Web Audio as chunks arrive (no <audio> buffering)', async () => {
    installFakeAudio();
    globalThis.fetch = vi.fn(async () => new Response(stream(), { status: 200 })) as unknown as typeof fetch;
    const { container } = render(LiveAudioPlayer, { cameraId: 'cam-mic' });
    const btn = container.querySelector('button')!;
    await fireEvent.click(btn);
    await waitFor(() => expect(bufferSources.length).toBeGreaterThan(0));
    // Buffer framed at the stream's own rate; sources scheduled ahead of now.
    expect(createBuffer).toHaveBeenCalledWith(1, expect.any(Number), 8000);
    expect(startCalls[0]).toBeGreaterThan(0);
  });

  it('stop aborts the fetch', async () => {
    installFakeAudio();
    const abortSpy = vi.fn();
    globalThis.AbortController = class {
      signal = { aborted: false };
      abort = abortSpy;
    } as unknown as typeof AbortController;
    globalThis.fetch = vi.fn(async () => new Response(stream(), { status: 200 })) as unknown as typeof fetch;
    const { container } = render(LiveAudioPlayer, { cameraId: 'cam-mic' });
    const btn = container.querySelector('button')!;
    await fireEvent.click(btn);
    await waitFor(() => expect(bufferSources.length).toBeGreaterThan(0));
    await fireEvent.click(btn); // stop
    expect(abortSpy).toHaveBeenCalled();
  });

  it('shows the failed state with retry when the endpoint refuses', async () => {
    installFakeAudio();
    globalThis.fetch = vi.fn(async () => new Response('nope', { status: 503 })) as unknown as typeof fetch;
    const { container } = render(LiveAudioPlayer, { cameraId: 'cam-mic' });
    const btn = container.querySelector('button')!;
    await fireEvent.click(btn);
    await waitFor(() => expect(container.textContent).toContain('live.audioInstant.failed'));
    expect(container.textContent).toContain('common.retry');
  });
});
