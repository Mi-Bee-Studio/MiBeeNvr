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
    CategoryScale, LinearScale,
    BarController, BarElement,
    LineController, LineElement,
    PointElement, Filler, Tooltip, Legend, Title
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
  const labels = trends.map(d => d.date.slice(5));
  const rawSizes = trends.map(d => d.total_size);
  const chartUnit = getChartUnit(rawSizes);
  const sizes = rawSizes.map(s => +(s / chartUnit.divisor).toFixed(1));

  return new Chart(canvas, {
    type: 'line',
    data: {
      labels,
      datasets: [{
        label: `Storage (${chartUnit.unit})`,
        data: sizes,
        borderColor: accentColor,
        backgroundColor: accentFill,
        fill: true,
        tension: 0.3,
        pointRadius: 4,
        pointBackgroundColor: accentColor,
      }],
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

/**
 * Aggregate camera recording counts from trend data.
 * @param {{ date: string; total_size: number; cameras?: Record<string, number> }[]} trends
 * @returns {Record<string, number>}
 */
export function aggregateCameraTotals(trends) {
  const totals = {};
  trends.forEach(d => {
    if (d.cameras) {
      Object.entries(d.cameras).forEach(([cam, count]) => {
        totals[cam] = (totals[cam] || 0) + count;
      });
    }
  });
  return totals;
}

/**
 * Create the recordings-per-camera bar chart.
 * @param {import('chart.js')} Chart - Chart constructor
 * @param {HTMLCanvasElement} canvas
 * @param {Record<string, number>} cameraTotals
 * @param {string[]} allCameraNames - Full list for color index mapping
 * @param {Set<string>} selectedCameras - Currently selected camera filter
 * @returns {import('chart.js').Chart | null}
 */
export function createCameraChart(Chart, canvas, cameraTotals, allCameraNames, selectedCameras) {
  if (!canvas || Object.keys(cameraTotals).length === 0) return null;

  const { gridColor, textColor } = getChartThemeColors();
  const camLabels = Object.keys(cameraTotals).filter(name => selectedCameras.has(name));
  const camData = camLabels.map(name => cameraTotals[name]);
  if (camLabels.length === 0) return null;

  const camBarColors = camLabels.map(name => {
    const idx = allCameraNames.indexOf(name) % BAR_COLORS.length;
    return BAR_COLORS[idx];
  });

  return new Chart(canvas, {
    type: 'bar',
    data: {
      labels: camLabels,
      datasets: [{
        label: 'Recordings',
        data: camData,
        backgroundColor: camBarColors,
        borderRadius: 6,
      }],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { display: false },
        tooltip: { mode: 'index', intersect: false },
      },
      scales: {
        x: { grid: { display: false }, ticks: { color: textColor } },
        y: { grid: { color: gridColor }, ticks: { color: textColor }, beginAtZero: true },
      },
    },
  });
}
