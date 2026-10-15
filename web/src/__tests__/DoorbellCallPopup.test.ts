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
      fn({ type, data: JSON.stringify({ data: payload }) }),
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
