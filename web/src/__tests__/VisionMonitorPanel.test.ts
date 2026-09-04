import { render, cleanup, waitFor } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import VisionMonitorPanel from '$lib/components/VisionMonitorPanel.svelte';
import { getVisionStatus, getVisionMetrics } from '$lib/api/settings';

// Mock the settings API module — the panel is a pure view over the two
// heartbeat-derived endpoints (#671).
vi.mock('$lib/api/settings', () => ({
  getVisionStatus: vi.fn(),
  getVisionMetrics: vi.fn(),
}));

// Mock lucide icons (BrainCircuit).
vi.mock('lucide-svelte', () => ({
  BrainCircuit: () => document.createElement('span'),
}));

const statusWithMetrics = {
  enabled: true,
  healthy: true,
  device: 'cuda',
  queue_depth: 12,
  drops_marked_total: 2,
  metrics: {
    queue_capacity: 64,
    decode_workers: 2,
    workers_busy: 1,
    received_total: 2049,
    dropped_total: 1045,
    dropped_queue_full: 1040,
    dropped_ttl: 5,
    events_emitted: 88,
    seg_ms_p50: 16600,
    seg_ms_p90: 76400,
    mem_available_mb: 620,
    load1: 2.1,
  },
};

function mockFetches(status: unknown, metrics: unknown) {
  vi.mocked(getVisionStatus).mockResolvedValue(status as never);
  vi.mocked(getVisionMetrics).mockResolvedValue(metrics as never);
}

describe('VisionMonitorPanel', () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('renders nothing when the integration is disabled', async () => {
    mockFetches({ enabled: false }, { enabled: false, points: [], marked_total: 0 });
    const { container } = render(VisionMonitorPanel);
    await waitFor(() => expect(getVisionStatus).toHaveBeenCalled());
    expect(container.querySelector('[data-testid="vision-monitor-panel"]')).toBeNull();
  });

  it('renders metric tiles from heartbeat v2 status', async () => {
    mockFetches(statusWithMetrics, {
      enabled: true,
      points: [],
      marked_total: 2,
    });
    const { container, getByText } = render(VisionMonitorPanel);
    await waitFor(() =>
      expect(container.querySelector('[data-testid="vision-monitor-panel"]')).toBeTruthy(),
    );
    // Queue tile shows depth/capacity.
    expect(getByText('12').textContent).toContain('64');
    // Hit events tile carries events_emitted.
    expect(getByText('88')).toBeTruthy();
  });

  it('renders axis-labelled charts when history has multiple samples', async () => {
    const points = [0, 1, 2, 3].map((i) => ({
      ts: `2026-09-02T04:0${i}:00Z`,
      queue_depth: i,
      processed_count: i * 10,
      dropped_total: i, // 每采样窗 +1
      decode_workers: 1,
      workers_busy: 0,
      events_emitted: 0,
    }));
    mockFetches(statusWithMetrics, { enabled: true, points, marked_total: 2 });
    const { container } = render(VisionMonitorPanel);
    await waitFor(() =>
      expect(container.querySelector('[data-testid="vm-chart-queue"]')).toBeTruthy(),
    );
    expect(container.querySelector('[data-testid="vm-chart-drop"]')).toBeTruthy();
    // Charts carry y-axis tick text (0 baseline) — the readability fix (#692).
    const queueChart = container.querySelector('[data-testid="vm-chart-queue"]');
    expect(queueChart?.querySelector('svg[data-testid="vm-timechart"]')).toBeTruthy();
    const yTexts = [...(queueChart?.querySelectorAll('text.vm-y') ?? [])].map(
      (el) => el.textContent ?? '',
    );
    expect(yTexts).toContain('0');
    const poly = queueChart?.querySelector('polyline');
    expect(poly?.getAttribute('points').trim().split(/\s+/).length).toBe(4);
  });

  it('shows the legacy note when metrics are absent (old consumer)', async () => {
    mockFetches(
      { enabled: true, healthy: true, queue_depth: 0 },
      { enabled: true, points: [], marked_total: 0 },
    );
    const { container, getByText } = render(VisionMonitorPanel);
    await waitFor(() =>
      expect(container.querySelector('[data-testid="vision-monitor-panel"]')).toBeTruthy(),
    );
    expect(getByText(/does not report runtime metrics/i)).toBeTruthy();
  });
});
