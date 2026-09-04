import { render, cleanup } from '@testing-library/svelte';
import { describe, it, expect, afterEach } from 'vitest';
import MiniTimeChart from '$lib/components/MiniTimeChart.svelte';

function pts(n: number, stepMs = 30_000, startMs = Date.parse('2026-09-03T00:00:00Z')) {
  return Array.from({ length: n }, (_, i) => ({ t: startMs + i * stepMs, v: i }));
}

describe('MiniTimeChart', () => {
  afterEach(() => cleanup());

  it('renders y-axis tick labels including 0 and the ceiling, plus the unit', () => {
    const { container, getByText } = render(MiniTimeChart, {
      props: { points: pts(10), unit: '段', color: '#3b82f6' },
    });
    const svg = container.querySelector('svg[data-testid="vm-timechart"]');
    expect(svg).toBeTruthy();
    // 0 / mid / max tick labels exist as text nodes.
    expect(getByText(/^0$/)).toBeTruthy();
    expect(getByText('段')).toBeTruthy();
  });

  it('renders three x-axis time labels from the series timestamps', () => {
    // 06:00, 06:07:30, 06:15 UTC on a fixed local-formatted series — assert
    // count of HH:MM-shaped labels inside the chart instead of exact locale.
    const { container } = render(MiniTimeChart, {
      props: { points: pts(30), unit: '段' },
    });
    const labels = [...container.querySelectorAll('text.vm-x')]
      .map((el) => el.textContent ?? '')
      .filter((s) => /^\d{2}:\d{2}$/.test(s));
    expect(labels).toHaveLength(3);
    // Monotonic left→right ordering.
    expect(labels[0] <= labels[2]).toBe(true);
  });

  it('shows the empty placeholder when fewer than 2 points', () => {
    const { container, getByText } = render(MiniTimeChart, {
      props: { points: [{ t: 1, v: 5 }], unit: '段', emptyLabel: '暂无数据' },
    });
    expect(getByText(/no data|暂无数据/i)).toBeTruthy();
    expect(container.querySelector('polyline')).toBeNull();
  });

  it('decimates long series and still draws a line', () => {
    const { container } = render(MiniTimeChart, {
      props: { points: pts(600), unit: '段' },
    });
    const poly = container.querySelector('polyline');
    expect(poly).toBeTruthy();
    // Decimated down to ~160 buckets, never the raw 600 vertices.
    const vertices = (poly?.getAttribute('points') ?? '').trim().split(/\s+/).length;
    expect(vertices).toBeLessThanOrEqual(200);
    expect(vertices).toBeGreaterThanOrEqual(2);
  });

  it('annotates the latest value', () => {
    const { getByTestId } = render(MiniTimeChart, {
      props: { points: pts(5), unit: '段' },
    });
    expect(getByTestId('vm-chart-last-value').textContent).toBe('4');
  });
});
