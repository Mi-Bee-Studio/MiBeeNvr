/**
 * Chart.js configuration factory functions.
 * Uses dynamic import for tree-shaking — Chart.js is only loaded when charts are rendered.
 */
import { getChartUnit } from './format';
import { getEffectiveTheme } from './preferences';

/** Bar color palette for camera charts */
export const BAR_COLORS = [
  'rgba(139, 92, 246, 0.7)',
  'rgba(56, 189, 248, 0.7)',
  'rgba(16, 185, 129, 0.7)',
  'rgba(245, 158, 11, 0.7)',
  'rgba(239, 68, 68, 0.7)',
  'rgba(168, 85, 247, 0.7)',
  'rgba(34, 197, 94, 0.7)',
  'rgba(251, 146, 60, 0.7)',
];

/** Cached Chart.js module (lazy-loaded once) */
let _chartModule = null;

/**
 * Dynamically import and register Chart.js components.
 * Returns the Chart constructor. Only loads once, then caches.
 */
export async function loadChart() {
  if (_chartModule) return _chartModule;

  const {
    Chart,
    CategoryScale,
    LinearScale,
    BarController,
    BarElement,
    LineController,
    LineElement,
    PointElement,
    Filler,
    Tooltip,
    Legend,
    Title,
  } = await import('chart.js');

  Chart.register(
    CategoryScale,
    LinearScale,
    BarController,
    BarElement,
    LineController,
    LineElement,
    PointElement,
    Filler,
    Tooltip,
    Legend,
    Title,
  );

  _chartModule = Chart;
  return Chart;
}

/** Get theme-aware chart colors */
export function getChartThemeColors() {
  const isDark = getEffectiveTheme() === 'dark';
  return {
    gridColor: isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)',
    textColor: isDark ? '#a1a1a1' : '#4b5563',
    accentColor: 'rgba(139, 92, 246, 0.8)',
    accentFill: 'rgba(139, 92, 246, 0.1)',
  };
}

/**
 * Create the storage trend line chart.
 * @param {import('chart.js')} Chart - Chart constructor
 * @param {HTMLCanvasElement} canvas
 * @param {{ date: string; total_size: number }[]} trends
 * @returns {import('chart.js').Chart | null}
 */
export function createTrendChart(Chart, canvas, trends) {
  if (!canvas) return null;

  const { gridColor, textColor, accentColor, accentFill } = getChartThemeColors();
  const labels = trends.map((d) => d.date.slice(5));
  const rawSizes = trends.map((d) => d.total_size);
  const chartUnit = getChartUnit(rawSizes);
  const sizes = rawSizes.map((s) => +(s / chartUnit.divisor).toFixed(1));

  return new Chart(canvas, {
    type: 'line',
    data: {
      labels,
      datasets: [
        {
          label: `Storage (${chartUnit.unit})`,
          data: sizes,
          borderColor: accentColor,
          backgroundColor: accentFill,
          fill: true,
          tension: 0.3,
          pointRadius: 4,
          pointBackgroundColor: accentColor,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { labels: { color: textColor } },
        tooltip: { mode: 'index', intersect: false },
      },
      scales: {
        x: { grid: { color: gridColor }, ticks: { color: textColor } },
        y: { grid: { color: gridColor }, ticks: { color: textColor }, beginAtZero: true },
      },
    },
  });
}
