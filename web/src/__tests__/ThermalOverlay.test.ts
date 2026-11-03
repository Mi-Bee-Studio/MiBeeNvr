import { render, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import ThermalOverlay from '$lib/components/ThermalOverlay.svelte';

// Mock i18n: t() returns the key (assertions match raw keys).
vi.mock('$lib/i18n', () => ({
  t: (key: string) => key,
}));

vi.mock('$lib/api', () => ({
  appendAuthToken: (url: string) => url,
  API_BASE: '/api',
}));

// Minimal EventSource stub: capture listeners, let tests fire events.
type SseListener = (e: { type: string; data: string }) => void;
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static last(): FakeEventSource {
    return FakeEventSource.instances[0];
  }
  url: string;
  closed = false;
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
      // Real wire shape: {"Topic":...,"Data":{...}}.
      fn({ type, data: JSON.stringify({ Topic: type, Data: payload }) }),
    );
  }
  close() {
    this.closed = true;
  }
}

function fireReading(maxC: number, minC: number) {
  FakeEventSource.last().fire('thermal.temperature', {
    camera_id: 'cam-hot',
    timestamp: new Date().toISOString(),
    max_c: maxC,
    min_c: minC,
    spots: [{ name: '炉体', x: 0.5, y: 0.5, temp_c: maxC }],
  });
}

describe('ThermalOverlay (dual-spectrum temperature chip)', () => {
  beforeEach(() => {
    vi.stubGlobal('EventSource', FakeEventSource as unknown as typeof EventSource);
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    FakeEventSource.instances = [];
  });

  it('subscribes to the per-camera events feed', () => {
    render(ThermalOverlay, { props: { cameraId: 'cam-hot' } });
    expect(FakeEventSource.last().url).toBe('/api/cameras/cam-hot/events');
  });

  it('shows the waiting state before the first reading', () => {
    const { container } = render(ThermalOverlay, { props: { cameraId: 'cam-hot' } });
    const chip = container.querySelector('[data-testid="thermal-overlay"]')!;
    expect(chip.textContent).toContain('thermal.waiting');
  });

  it('renders max/min once a reading arrives', async () => {
    const { container } = render(ThermalOverlay, { props: { cameraId: 'cam-hot' } });
    fireReading(88.4, 21.7);
    await vi.waitFor(() => {
      const chip = container.querySelector('[data-testid="thermal-overlay"]')!;
      expect(chip.textContent).toContain('88.4');
      expect(chip.textContent).toContain('21.7');
      expect(chip.textContent).toContain('thermal.max');
      expect(chip.textContent).toContain('thermal.min');
    });
  });

  it('closes the EventSource on teardown', () => {
    render(ThermalOverlay, { props: { cameraId: 'cam-hot' } });
    const es = FakeEventSource.last();
    cleanup();
    expect(es.closed).toBe(true);
  });
});
