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
 * Create a per-camera stacked bar chart showing daily recording growth (bytes
 * written per camera per day). Each bar is one day, split by camera — the user
 * sees at a glance which cameras consume the most storage.
 *
 * @param {import('chart.js')} Chart - Chart constructor
 * @param {HTMLCanvasElement} canvas
 * @param {{ date: string; total_size: number; camera_sizes?: Record<string, number> }[]} trends
 * @param {string} [unitSuffix] - localized suffix for the y-axis title (e.g. "/day")
 * @returns {import('chart.js').Chart | null}
 */
export function createTrendChart(Chart, canvas, trends, unitSuffix = '/day') {
  if (!canvas) return null;

  const { gridColor, textColor } = getChartThemeColors();
  const labels = trends.map((d) => d.date.slice(5)); // MM-DD

  // Collect all camera names across all days (for stable legend order).
  const cameraNames = [...new Set(trends.flatMap((d) => Object.keys(d.camera_sizes || {})))];
  // Determine unit from the max single-day total.
  const maxDay = Math.max(...trends.map((d) => d.total_size), 0);
  const chartUnit = getChartUnit([maxDay]);

  // Build one dataset per camera (stacked).
  const datasets = cameraNames.map((cam, i) => ({
    label: cam,
    data: trends.map((d) => +(((d.camera_sizes || {})[cam] || 0) / chartUnit.divisor).toFixed(2)),
    backgroundColor: BAR_COLORS[i % BAR_COLORS.length],
    borderColor: BAR_COLORS[i % BAR_COLORS.length].replace('0.7', '0.9'),
    borderWidth: 1,
  }));

  return new Chart(canvas, {
    type: 'bar',
    data: { labels, datasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: {
          labels: { color: textColor, boxWidth: 12, font: { size: 10 } },
          position: 'bottom',
          // Hide legend if there are too many cameras (chart gets cluttered).
          display: cameraNames.length <= 8,
        },
        tooltip: {
          mode: 'index',
          intersect: false,
          callbacks: {
            label: (ctx) => `${ctx.dataset.label}: ${ctx.parsed.y} ${chartUnit.unit}`,
          },
        },
      },
      scales: {
        x: { stacked: true, grid: { color: gridColor }, ticks: { color: textColor } },
        y: {
          stacked: true,
          grid: { color: gridColor },
          ticks: { color: textColor },
          beginAtZero: true,
          title: { display: true, text: `${chartUnit.unit}${unitSuffix}`, color: textColor },
        },
      },
    },
  });
}
