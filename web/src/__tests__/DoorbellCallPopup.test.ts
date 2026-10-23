import { render, cleanup, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import DoorbellCallPopup from '$lib/components/DoorbellCallPopup.svelte';

// t() echoes keys; icons are stub spans.
vi.mock('$lib/i18n', () => ({ t: (k: string) => k }));
vi.mock('lucide-svelte', () => {
  const icons = ['Phone', 'PhoneOff', 'X', 'BellRing'];
  const mock: Record<string, () => HTMLElement> = {};
  for (const name of icons) mock[name] = () => document.createElement('span');
  return mock;
});

const mockApiRequest = vi.hoisted(() => vi.fn());
vi.mock('$lib/api/client', async () => {
  const actual = await vi.importActual('$lib/api/client');
  return {
    ...actual,
    apiRequest: mockApiRequest,
    appendAuthToken: (url: string) => url,
    getTokenForUrl: () => null,
  };
});

// Minimal EventSource stub: capture listeners, let tests fire events.
type SseListener = (e: { type: string; data: string }) => void;
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static last(): FakeEventSource {
    return FakeEventSource.instances[0];
  }
  url: string;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, Set<SseListener>>();
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  addEventListener(type: string, fn: SseListener) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(fn);
  }
  fire(type: string, payload: unknown) {
    this.listeners.get(type)?.forEach((fn) =>
      // Real wire shape from /api/events: {"Topic":...,"Data":{...}}.
      fn({ type, data: JSON.stringify({ Topic: type, Data: payload }) }),
    );
  }
  close() {}
}

function fireRing(name = 'Front door') {
  FakeEventSource.last().fire('doorbell.ring', {
    call_id: 'c1',
    user: 'door1',
    name,
    snapshot_path: 'snapshots/cam/1.jpg',
  });
}

describe('DoorbellCallPopup (#971)', () => {
  beforeEach(() => {
    vi.stubGlobal('EventSource', FakeEventSource as unknown as typeof EventSource);
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    mockApiRequest.mockReset();
    FakeEventSource.instances = [];
  });

  it('stays hidden until a ring arrives, then shows caller + snapshot', async () => {
    const { container } = render(DoorbellCallPopup);
    expect(container.textContent || '').not.toContain('doorbell.title');

    await waitFor(() => expect(FakeEventSource.instances.length).toBe(1));
    expect(FakeEventSource.last().url).toContain('/api/events?filter=doorbell.');

    fireRing();
    await waitFor(() => {
      expect(container.textContent).toContain('Front door');
      expect(container.textContent).toContain('doorbell.answer');
      expect(container.textContent).toContain('doorbell.reject');
    });
    const img = container.querySelector('img');
    expect(img?.getAttribute('src')).toContain('/api/doorbell/calls/c1/snapshot');
  });

  it('reject posts the reject action and dismisses', async () => {
    mockApiRequest.mockResolvedValue(undefined);
    const { container } = render(DoorbellCallPopup);
    await waitFor(() => expect(FakeEventSource.instances.length).toBe(1));
    fireRing('Back door');
    await waitFor(() => expect(container.textContent).toContain('Back door'));

    const reject = [...container.querySelectorAll('button')].find((b) =>
      b.textContent?.includes('doorbell.reject'),
    )!;
    reject.click();
    await waitFor(() => {
      expect(mockApiRequest).toHaveBeenCalledWith('/doorbell/calls/c1/reject', { method: 'POST' });
      expect(container.textContent || '').not.toContain('Back door');
    });
  });

  it('dismisses on doorbell.ended for the active call', async () => {
    const { container } = render(DoorbellCallPopup);
    await waitFor(() => expect(FakeEventSource.instances.length).toBe(1));
    fireRing();
    await waitFor(() => expect(container.textContent).toContain('Front door'));

    FakeEventSource.last().fire('doorbell.ended', { call_id: 'c1', user: 'door1', reason: 'bye' });
    await waitFor(() => expect(container.textContent || '').not.toContain('doorbell.answer'));
  });
});

// --- G.711 codec anchors (shared with the GB28181 talk path) ---
// Standard-form A-law: linear 0 → 0xD5, rails → 0xAA/0x2A; decoder anchors
// 0xD5 → 8, 0x8A → 8064, 0xAA → 32256, 0x2A → −32256.
import { pcmToAlawItu, encodeAlawItu, decodeAlaw } from '$lib/alaw-encoder';

describe('ITU G.711 A-law anchors + round-trip', () => {
  it('encodes the documented anchor values', () => {
    expect(pcmToAlawItu(0)).toBe(0xd5);
    expect(pcmToAlawItu(32767)).toBe(0xaa);
    expect(pcmToAlawItu(-32768)).toBe(0x2a);
  });

  it('decodes the documented anchor values', () => {
    const out = decodeAlaw(new Uint8Array([0xd5, 0x8a, 0xaa, 0x2a]));
    expect(out[0] * 32768).toBeCloseTo(8, -1);
    expect(out[1] * 32768).toBeCloseTo(8064, -1);
    expect(out[2] * 32768).toBeCloseTo(32256, -1);
    expect(out[3] * 32768).toBeCloseTo(-32256, -1);
  });

  it('keeps sign polarity on a mid-level sample', () => {
    const pos = decodeAlaw(new Uint8Array([pcmToAlawItu(8000)]))[0];
    const neg = decodeAlaw(new Uint8Array([pcmToAlawItu(-8000)]))[0];
    expect(pos).toBeGreaterThan(0);
    expect(neg).toBeLessThan(0);
  });

  it('decodeAlaw(encodeAlawItu(x)) stays within G.711 quantization error', () => {
    // Inherent A-law error: 13-bit fold (≤8) + half the top-segment step
    // (16-bit step 1024 → 512) + the codec's 32256 ceiling vs 32767 rails.
    for (let x = -32768; x <= 32767; x += 997) {
      const bytes = encodeAlawItu(new Float32Array([x / 32767]));
      const back = decodeAlaw(bytes)[0] * 32768;
      expect(Math.abs(back - x)).toBeLessThanOrEqual(520);
    }
  });

  it('round-trips a Float32 buffer without changing length', () => {
    const pcm = new Float32Array(160);
    for (let i = 0; i < pcm.length; i++) pcm[i] = Math.sin((i / 160) * 2 * Math.PI) * 0.5;
    const bytes = encodeAlawItu(pcm);
    const back = decodeAlaw(bytes);
    expect(bytes).toHaveLength(160);
    expect(back).toHaveLength(160);
    for (let i = 0; i < 160; i++) {
      expect(Math.abs(back[i] - pcm[i])).toBeLessThanOrEqual(520 / 32768);
    }
  });
});
