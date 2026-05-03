<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getStats, listCameras, healthCheck } from '$lib/api';
  import type { StorageStats, Camera, HealthResponse } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatFileSize } from '$lib/format';
  import { HardDrive, BarChart3, Video, CameraIcon, Activity, Clock, Cpu, Server } from 'lucide-svelte';
  import {
    Chart,
    CategoryScale,
    LinearScale,
    BarController,
    BarElement,
    LineController,
    LineElement,
    PointElement, Filler, Tooltip, Legend, Title
  } from 'chart.js';
  import { getStatsTrends } from '$lib/api';
  import { getEffectiveTheme } from '$lib/preferences';

  Chart.register(
    CategoryScale, LinearScale,
    BarController, BarElement,
    LineController, LineElement,
    PointElement, Filler, Tooltip, Legend, Title
  );

  let stats: StorageStats | null = null;
  let cameras: Camera[] = [];
  let loading = true;
  let error = '';

  // Auto-refresh interval
  let refreshInterval: number;
  let trendChart: Chart | null = null;
  let cameraChart: Chart | null = null;

  // Health data
  let health: HealthResponse | null = null;

  function formatPercentage(used: number, total: number): string {
    if (total === 0) return '0%';
    const pct = (used / total) * 100;
    return `${pct.toFixed(1)}%`;
  }

  function getUsageColor(percentage: number): string {
    if (percentage < 50) return 'bg-[var(--color-success)]';
    if (percentage < 80) return 'bg-[var(--color-warning)]';
    return 'th-bg-danger';
  }

  function getHealthDotColor(status: string): string {
    if (status === 'ok') return 'bg-[var(--color-success)]';
    if (status === 'degraded' || status === 'warning') return 'bg-[var(--color-warning)]';
    return 'bg-[var(--color-danger)]';
  }

  function getHealthBadgeClass(status: string): string {
    if (status === 'ok') return 'badge-success';
    if (status === 'degraded') return 'badge-warning';
    return 'badge-error';
  }

  function getHealthLabel(status: string): string {
    if (status === 'ok') return t('stats.healthy');
    if (status === 'degraded') return t('stats.degraded');
    return t('stats.unhealthy');
  }

  function parseGoroutineCount(msg?: string): string {
    if (!msg) return '—';
    const match = msg.match(/(\d+)/);
    return match ? match[1] : msg;
  }

  // Load data
  async function loadStats() {
    loading = true;
    error = '';

    try {
      stats = await getStats();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadStats');
    } finally {
      loading = false;
    }
  }

  async function loadCameras() {
    try {
      cameras = await listCameras();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadCameras');
    }
  }

  async function loadTrends() {
    try {
      const trends = await getStatsTrends(7);
      if (trends && trends.length > 0) {
        createCharts(trends);
      }
    } catch (e) {
      console.error('Failed to load trends:', e);
    }
  }

  async function loadHealth() {
    try {
      health = await healthCheck();
    } catch (e) {
      console.error('Failed to load health:', e);
    }
  }

  function createCharts(trends: { date: string; total_size: number; cameras?: Record<string, number> }[]) {
    const isDark = getEffectiveTheme() === 'dark';
    const gridColor = isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)';
    const textColor = isDark ? '#a1a1a1' : '#4b5563';
    const accentColor = 'rgba(139, 92, 246, 0.8)';
    const accentFill = 'rgba(139, 92, 246, 0.1)';

    const labels = trends.map(d => d.date.slice(5)); // "MM-DD"
    const sizes = trends.map(d => +(d.total_size / (1024 * 1024)).toFixed(1)); // MB as number

    // Aggregate camera counts
    const cameraTotals: Record<string, number> = {};
    trends.forEach(d => {
      if (d.cameras) {
        Object.entries(d.cameras).forEach(([cam, count]) => {
          cameraTotals[cam] = (cameraTotals[cam] || 0) + count;
        });
      }
    });

    // Destroy existing
    if (trendChart) { trendChart.destroy(); trendChart = null; }
    if (cameraChart) { cameraChart.destroy(); cameraChart = null; }

    // Line chart - Storage Trend
    const trendCtx = document.getElementById('trendChart') as HTMLCanvasElement;
    if (trendCtx) {
      trendChart = new Chart(trendCtx, {
        type: 'line',
        data: {
          labels,
          datasets: [{
            label: 'Storage (MB)',
            data: sizes,
            borderColor: accentColor,
            backgroundColor: accentFill,
            fill: true,
            tension: 0.3,
            pointRadius: 4,
            pointBackgroundColor: accentColor,
          }]
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          plugins: {
            legend: { labels: { color: textColor } },
            tooltip: { mode: 'index', intersect: false }
          },
          scales: {
            x: { grid: { color: gridColor }, ticks: { color: textColor } },
            y: { grid: { color: gridColor }, ticks: { color: textColor }, beginAtZero: true }
          }
        }
      });
    }

    // Bar chart - Recordings per Camera
    const cameraCtx = document.getElementById('cameraChart') as HTMLCanvasElement;
    if (cameraCtx && Object.keys(cameraTotals).length > 0) {
      const camLabels = Object.keys(cameraTotals);
      const camData = Object.values(cameraTotals);
      const barColors = [
        'rgba(139, 92, 246, 0.7)',
        'rgba(56, 189, 248, 0.7)',
        'rgba(16, 185, 129, 0.7)',
        'rgba(245, 158, 11, 0.7)',
        'rgba(239, 68, 68, 0.7)',
        'rgba(168, 85, 247, 0.7)',
        'rgba(34, 197, 94, 0.7)',
        'rgba(251, 146, 60, 0.7)',
      ];

      cameraChart = new Chart(cameraCtx, {
        type: 'bar',
        data: {
          labels: camLabels,
          datasets: [{
            label: 'Recordings',
            data: camData,
            backgroundColor: barColors.slice(0, camLabels.length),
            borderRadius: 6,
          }]
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          plugins: {
            legend: { display: false },
            tooltip: { mode: 'index', intersect: false }
          },
          scales: {
            x: { grid: { display: false }, ticks: { color: textColor } },
            y: { grid: { color: gridColor }, ticks: { color: textColor }, beginAtZero: true }
          }
        }
      });
    }
  }

  // Lifecycle
  onMount(() => {
    loadStats();
    loadCameras();
    loadTrends();
    loadHealth();

    // Auto-refresh every 30 seconds
    refreshInterval = window.setInterval(() => {
      loadStats();
      loadCameras();
      loadTrends();
      loadHealth();
    }, 30000);

    // Re-create charts when theme changes
    const observer = new MutationObserver(() => {
      if (trendChart || cameraChart) {
        loadTrends();
      }
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme']
    });

    return () => {
      if (refreshInterval) clearInterval(refreshInterval);
      observer.disconnect();
    };
  });

  onDestroy(() => {
    if (trendChart) { trendChart.destroy(); trendChart = null; }
    if (cameraChart) { cameraChart.destroy(); cameraChart = null; }
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold th-text-primary">{t('stats.title')}</h2>
    </div>

    <!-- Error message -->
    {#if error}
      <div class="mb-4 p-4 bg-[rgba(239,68,68,0.3)] border th-border-danger rounded-md th-color-danger">
        {error}
      </div>
    {/if}

    <!-- Loading state -->
    {#if loading && !stats}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else if stats}
      <div class="space-y-6">
        <!-- Storage stats cards -->
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          <!-- Total storage -->
          <div class="card p-6 border th-border">
            <div class="flex items-center justify-between mb-4">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.totalStorage')}</h3>
              <HardDrive size={20} class="th-text-secondary" />
            </div>
            <p class="text-3xl font-bold th-text-primary mb-1">
              {formatFileSize(stats.total_bytes)}
            </p>
            <p class="text-sm th-text-muted mt-1">{t('stats.capacity')}</p>
          </div>

          <!-- Used storage -->
          <div class="card p-6 border th-border">
            <div class="flex items-center justify-between mb-4">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.used')}</h3>
              <BarChart3 size={20} class="th-text-secondary" />
            </div>
            <p class="text-4xl font-bold th-text-primary mb-1">
              {formatFileSize(stats.used_bytes)}
            </p>
            <p class="text-sm th-text-muted">
              {formatPercentage(stats.used_bytes, stats.total_bytes)} {t('stats.used')}
            </p>
          </div>

          <!-- Recordings count -->
          <div class="card p-6 border th-border">
            <div class="flex items-center justify-between mb-4">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.totalRecordings')}</h3>
              <Video size={20} class="th-text-secondary" />
            </div>
            <p class="text-4xl font-bold th-text-primary mb-1">
              {stats.recording_count.toLocaleString()}
            </p>
            <p class="text-sm th-text-muted">{t('stats.totalRecordings')}</p>
          </div>

          <!-- Cameras count -->
          <div class="card p-6 border th-border">
            <div class="flex items-center justify-between mb-4">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.activeCameras')}</h3>
              <CameraIcon size={20} class="th-text-secondary" />
            </div>
            <p class="text-4xl font-bold th-text-primary mb-1">
              {stats.camera_count}
            </p>
            <p class="text-sm th-text-muted">{t('stats.activeCameras')}</p>
          </div>
        </div>

        <!-- Storage usage bar -->
        <div class="card p-6 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-4">{t('stats.storageUsage')}</h3>
          <div class="mb-2">
            <div class="flex justify-between text-sm mb-2">
              <span class="th-text-muted">{t('stats.usedOf', { used: formatFileSize(stats.used_bytes) })}</span>
              <span class="th-text-muted">{t('stats.freeOf', { free: formatFileSize(stats.total_bytes - stats.used_bytes) })}</span>
            </div>
            <div class="w-full th-bg-tertiary rounded-full h-4 overflow-hidden">
              <div
                class="h-full {getUsageColor((stats.used_bytes / stats.total_bytes) * 100)} transition-all duration-500"
                style="width: {formatPercentage(stats.used_bytes, stats.total_bytes)}"
              ></div>
            </div>
          </div>
          <p class="text-sm th-text-muted mt-2">
            {t('stats.ofStorageUsed', { percentage: formatPercentage(stats.used_bytes, stats.total_bytes) })}
          </p>
        </div>

        <!-- Camera list -->
        <div class="card border th-border">
          <div class="p-6 border-b th-border">
            <h3 class="text-lg font-semibold th-text-primary">{t('stats.cameras')}</h3>
          </div>
          <div class="table-container border-0 rounded-none">
            {#if cameras.length === 0}
              <div class="p-8 text-center th-text-muted">
                {t('stats.noCameras')}
              </div>
            {:else}
              <table class="table">
                <thead>
                  <tr>
                    <th>{t('stats.tableName')}</th>
                    <th>{t('stats.tableId')}</th>
                    <th>{t('stats.tableProtocol')}</th>
                    <th>{t('stats.tableStatus')}</th>
                  </tr>
                </thead>
                <tbody>
                  {#each cameras as camera}
                    <tr class="transition-all duration-200 hover:th-bg-hover">
                      <td class="font-medium th-text-primary">{camera.name}</td>
                      <td class="th-text-muted font-mono text-sm">{camera.id}</td>
                      <td>
                        <span class="badge badge-neutral">{camera.protocol}</span>
                      </td>
                      <td>
                        {#if camera.enabled}
                          <span class="badge badge-success">{t('stats.enabled')}</span>
                        {:else}
                          <span class="badge badge-error">{t('stats.disabled')}</span>
                        {/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {/if}
          </div>
        </div>
        <!-- Charts — Storage Trend & Recordings by Camera -->
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <div class="card p-6 border th-border">
            <h3 class="text-lg font-medium th-text-primary mb-4">{t('stats.storageTrend')}</h3>
            <div class="h-64">
              <canvas id="trendChart"></canvas>
            </div>
          </div>
          <div class="card p-6 border th-border">
            <h3 class="text-lg font-medium th-text-primary mb-4">{t('stats.recordingsByCamera')}</h3>
            <div class="h-64">
              <canvas id="cameraChart"></canvas>
            </div>
          </div>
        </div>
        <!-- System Status — Health + Runtime Metrics -->
        {#if health}
          <div class="card p-6 border th-border">
            <h3 class="text-lg font-semibold th-text-primary mb-5">{t('stats.systemStatus')}</h3>

            <!-- Health indicator row -->
            <div class="flex items-center gap-3 mb-6">
              <span class="inline-block w-3 h-3 rounded-full {getHealthDotColor(health.status)}"></span>
              <span class="badge {getHealthBadgeClass(health.status)}">{getHealthLabel(health.status)}</span>
            </div>

            <!-- Runtime metric cards -->
            <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
              <!-- Uptime -->
              <div class="p-4 rounded-[var(--radius-sm)] th-bg-secondary border th-border">
                <div class="flex items-center gap-2 mb-2">
                  <Clock size={16} class="th-text-secondary" />
                  <span class="text-xs font-medium th-text-muted">{t('stats.uptime')}</span>
                </div>
                <p class="text-lg font-bold th-text-primary">{health.uptime || '—'}</p>
              </div>

              <!-- Goroutines -->
              <div class="p-4 rounded-[var(--radius-sm)] th-bg-secondary border th-border">
                <div class="flex items-center gap-2 mb-2">
                  <Cpu size={16} class="th-text-secondary" />
                  <span class="text-xs font-medium th-text-muted">{t('stats.goroutines')}</span>
                </div>
                <p class="text-lg font-bold th-text-primary">{parseGoroutineCount(health.checks?.goroutines?.message)}</p>
              </div>

              <!-- Database -->
              <div class="p-4 rounded-[var(--radius-sm)] th-bg-secondary border th-border">
                <div class="flex items-center gap-2 mb-2">
                  <Server size={16} class="th-text-secondary" />
                  <span class="text-xs font-medium th-text-muted">{t('stats.checkDatabase')}</span>
                </div>
                <p class="text-lg font-bold th-text-primary">
                  {#if health.checks?.database?.status === 'ok'}
                    <span class="badge badge-success">OK</span>
                  {:else}
                    <span class="badge badge-error">Error</span>
                  {/if}
                </p>
              </div>

              <!-- Storage health -->
              <div class="p-4 rounded-[var(--radius-sm)] th-bg-secondary border th-border">
                <div class="flex items-center gap-2 mb-2">
                  <Activity size={16} class="th-text-secondary" />
                  <span class="text-xs font-medium th-text-muted">{t('stats.checkStorage')}</span>
                </div>
                <p class="text-lg font-bold th-text-primary">
                  {#if health.checks?.storage}
                    <span class="badge {getHealthBadgeClass(health.checks.storage.status)}">
                      {health.checks.storage.status === 'ok' ? 'OK' : health.checks.storage.status === 'warning' ? 'Warn' : 'Error'}
                    </span>
                  {:else}
                    <span class="th-text-muted">—</span>
                  {/if}
                </p>
              </div>
            </div>

            <!-- Camera status list -->
            {#if cameras.length > 0}
              <div>
                <h4 class="text-sm font-medium th-text-muted mb-3">{t('stats.cameraStatus')}</h4>
                <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
                  {#each cameras as camera}
                    <div class="flex items-center gap-2 px-3 py-2 rounded-[var(--radius-sm)] th-bg-secondary border th-border">
                      <span class="inline-block w-2 h-2 rounded-full {camera.enabled ? 'bg-[var(--color-success)]' : 'bg-[var(--color-danger)]'}"></span>
                      <span class="text-sm th-text-primary truncate">{camera.name}</span>
                      <span class="ml-auto text-xs th-text-muted">{camera.enabled ? t('stats.active') : t('stats.inactive')}</span>
                    </div>
                  {/each}
                </div>
              </div>
            {/if}
          </div>
        {/if}

        <!-- Loading indicator for refresh -->
        {#if loading}
          <div class="text-center text-sm th-text-muted py-4">
            <span class="spinner mr-2"></span>
            {t('stats.refreshing')}
          </div>
        {/if}
      </div>
    {/if}
  </main>
</div>
