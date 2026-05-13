<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getStats, listCameras, healthCheck, getSystemStats, getMergeStatus, getMergePending } from '$lib/api';
  import type { StorageStats, Camera, HealthResponse, SystemStats, MergeStatus, MergePending } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatFileSize, formatDate, getChartUnit } from '$lib/format';

  import { HardDrive, BarChart3, Video, CameraIcon, Activity, Clock, Cpu, Database, MemoryStick, Wifi, ChevronDown, ChevronUp, GitMerge } from 'lucide-svelte';
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

  let stats = $state<StorageStats | null>(null);
  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');

  // Merge monitoring state
  let mergeStatus = $state<MergeStatus | null>(null);
  let mergePending = $state<MergePending | null>(null);
  let mergeCollapsed = $state(false);

  // Auto-refresh interval
  let refreshInterval: number;
  let trendChart: Chart | null = null;
  let cameraChart: Chart | null = null;

  // Camera filter state
  let selectedCameras = $state<Set<string>>(new Set());
  let cameraChartCollapsed = $state(false);
  let lastCameraTotals: Record<string, number> = {};
  let allCameraNames = $state<string[]>([]);
  const BAR_COLORS = [
    'rgba(139, 92, 246, 0.7)',
    'rgba(56, 189, 248, 0.7)',
    'rgba(16, 185, 129, 0.7)',
    'rgba(245, 158, 11, 0.7)',
    'rgba(239, 68, 68, 0.7)',
    'rgba(168, 85, 247, 0.7)',
    'rgba(34, 197, 94, 0.7)',
    'rgba(251, 146, 60, 0.7)',
  ];

  // Health data
  let health = $state<HealthResponse | null>(null);

  // System resource data
  let prevSystemStats = $state<SystemStats | null>(null);
  let currentSystemStats = $state<SystemStats | null>(null);
  let cpuPercent = $state<string | null>(null);
  let memoryPercent = $state<string | null>(null);
  let netRateUp = $state<string | null>(null);
  let netRateDown = $state<string | null>(null);

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

  async function loadSystemStats() {
    try {
      const s = await getSystemStats();
      currentSystemStats = s;

      if (prevSystemStats) {
        const dt = s.timestamp - prevSystemStats.timestamp;
        if (dt > 0) {
          const totalDelta = s.cpu.total - prevSystemStats.cpu.total;
          const idleDelta = s.cpu.idle - prevSystemStats.cpu.idle;
          if (totalDelta > 0) {
            cpuPercent = ((totalDelta - idleDelta) / totalDelta * 100).toFixed(1) + '%';
          }
          netRateUp = formatFileSize((s.network.bytes_sent - prevSystemStats.network.bytes_sent) / dt) + '/s';
          netRateDown = formatFileSize((s.network.bytes_recv - prevSystemStats.network.bytes_recv) / dt) + '/s';
        }
      }

      if (s.memory.total > 0) {
        memoryPercent = ((s.memory.total - s.memory.available) / s.memory.total * 100).toFixed(1) + '%';
      }

      prevSystemStats = s;
    } catch (e) {
      console.error('Failed to load system stats:', e);
    }
  }

  async function loadMergeData() {
    try {
      const [status, pending] = await Promise.all([
        getMergeStatus(),
        getMergePending(),
      ]);
      mergeStatus = status;
      mergePending = pending;
    } catch {
      // silently ignore
    }
  }


  function createCharts(trends: { date: string; total_size: number; cameras?: Record<string, number> }[]) {
    const isDark = getEffectiveTheme() === 'dark';
    const gridColor = isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)';
    const textColor = isDark ? '#a1a1a1' : '#4b5563';
    const accentColor = 'rgba(139, 92, 246, 0.8)';
    const accentFill = 'rgba(139, 92, 246, 0.1)';

    const labels = trends.map(d => d.date.slice(5)); // "MM-DD"
    const rawSizes = trends.map(d => d.total_size);
    const chartUnit = getChartUnit(rawSizes);
    const sizes = rawSizes.map(s => +(s / chartUnit.divisor).toFixed(1));

    // Aggregate camera counts
    const cameraTotals: Record<string, number> = {};
    trends.forEach(d => {
      if (d.cameras) {
        Object.entries(d.cameras).forEach(([cam, count]) => {
          cameraTotals[cam] = (cameraTotals[cam] || 0) + count;
        });
      }
    });

    // Store for filter rebuilds
    lastCameraTotals = cameraTotals;
    allCameraNames = Object.keys(cameraTotals);
    if (selectedCameras.size === 0 && allCameraNames.length > 0) {
      selectedCameras = new Set(allCameraNames);
    }

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
            label: `Storage (${chartUnit.unit})`,
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
    buildCameraChart(cameraTotals, textColor, gridColor);
  }

  function buildCameraChart(cameraTotals: Record<string, number>, textColor: string, gridColor: string) {
    if (cameraChart) { cameraChart.destroy(); cameraChart = null; }
    const cameraCtx = document.getElementById('cameraChart') as HTMLCanvasElement;
    if (!cameraCtx || Object.keys(cameraTotals).length === 0) return;

    const camLabels = Object.keys(cameraTotals).filter(name => selectedCameras.has(name));
    const camData = camLabels.map(name => cameraTotals[name]);
    if (camLabels.length === 0) return;

    const camBarColors = camLabels.map(name => {
      const idx = allCameraNames.indexOf(name) % BAR_COLORS.length;
      return BAR_COLORS[idx];
    });

    cameraChart = new Chart(cameraCtx, {
      type: 'bar',
      data: {
        labels: camLabels,
        datasets: [{
          label: 'Recordings',
          data: camData,
          backgroundColor: camBarColors,
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

  function toggleCameraFilter(name: string) {
    const newSet = new Set(selectedCameras);
    if (newSet.has(name)) { newSet.delete(name); } else { newSet.add(name); }
    selectedCameras = newSet;
    rebuildCameraChart();
  }

  function selectAllCameras() {
    selectedCameras = new Set(allCameraNames);
    rebuildCameraChart();
  }

  function deselectAllCameras() {
    selectedCameras = new Set();
    rebuildCameraChart();
  }

  function rebuildCameraChart() {
    const isDark = getEffectiveTheme() === 'dark';
    const gridColor = isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)';
    const textColor = isDark ? '#a1a1a1' : '#4b5563';
    buildCameraChart(lastCameraTotals, textColor, gridColor);
  }

  // Lifecycle
  onMount(() => {
    loadStats();
    loadCameras();
    loadTrends();
    loadHealth();
    loadSystemStats();
    loadMergeData();
    // Quick second sample after 2s so CPU/network show without waiting 30s
    const quickSample = window.setTimeout(() => loadSystemStats(), 2000);

    // Auto-refresh every 30 seconds
    refreshInterval = window.setInterval(() => {
      loadStats();
      loadCameras();
      loadTrends();
      loadHealth();
      loadSystemStats();
      loadMergeData();
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
      clearTimeout(quickSample);
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
        <!-- Row 1: Summary cards -->
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          <!-- Total storage -->
          <div class="card p-5 border th-border">
            <div class="flex items-center justify-between mb-3">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.totalStorage')}</h3>
              <HardDrive size={18} class="th-text-secondary" />
            </div>
            <p class="text-2xl font-bold th-text-primary">
              {formatFileSize(stats.total_bytes)}
            </p>
          </div>

          <!-- Used storage -->
          <div class="card p-5 border th-border">
            <div class="flex items-center justify-between mb-3">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.used')}</h3>
              <BarChart3 size={18} class="th-text-secondary" />
            </div>
            <p class="text-2xl font-bold th-text-primary">
              {formatFileSize(stats.used_bytes)} <span class="text-sm font-normal th-text-muted">{formatPercentage(stats.used_bytes, stats.total_bytes)}</span>
            </p>
          </div>

          <!-- Recordings count -->
          <div class="card p-5 border th-border">
            <div class="flex items-center justify-between mb-3">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.totalRecordings')}</h3>
              <Video size={18} class="th-text-secondary" />
            </div>
            <p class="text-2xl font-bold th-text-primary">
              {stats.recording_count.toLocaleString()}
            </p>
          </div>

          <!-- Cameras count -->
          <div class="card p-5 border th-border">
            <div class="flex items-center justify-between mb-3">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.activeCameras')}</h3>
              <CameraIcon size={18} class="th-text-secondary" />
            </div>
            <p class="text-2xl font-bold th-text-primary">
              {cameras.filter(c => c.enabled).length}/{cameras.length}
            </p>
          </div>
        </div>

        <!-- Row 2: Storage bar + System Health -->
        <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
          <!-- Storage usage bar -->
          <div class="card p-5 border th-border lg:col-span-2">
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

          <!-- Compact system health -->
          {#if health}
            <div class="card p-5 border th-border lg:col-span-1">
              <h3 class="text-lg font-semibold th-text-primary mb-4">{t('stats.systemStatus')}</h3>
              <!-- Health dot + badge + uptime -->
              <div class="flex items-center gap-2 mb-4">
                <span class="inline-block w-2.5 h-2.5 rounded-full {getHealthDotColor(health.status)}"></span>
                <span class="badge {getHealthBadgeClass(health.status)}">{getHealthLabel(health.status)}</span>
                {#if health.uptime}
                  <span class="ml-auto text-xs th-text-muted">{health.uptime}</span>
                {/if}
              </div>
              <!-- Compact indicators -->
              <div class="space-y-2 text-sm">
                <div class="flex items-center justify-between">
                  <span class="th-text-muted">{t('stats.goroutines')}</span>
                  <span class="font-medium th-text-primary">{parseGoroutineCount(health.checks?.goroutines?.message)}</span>
                </div>
                <div class="flex items-center justify-between">
                  <span class="th-text-muted">{t('stats.checkDatabase')}</span>
                  {#if health.checks?.database?.status === 'ok'}
                    <span class="text-[var(--color-success)]">✓</span>
                  {:else}
                    <span class="text-[var(--color-danger)]">✗</span>
                  {/if}
                </div>
                <div class="flex items-center justify-between">
                  <span class="th-text-muted">{t('stats.checkStorage')}</span>
                  {#if health.checks?.storage}
                    {#if health.checks.storage.status === 'ok'}
                      <span class="text-[var(--color-success)]">✓</span>
                    {:else if health.checks.storage.status === 'warning'}
                      <span class="text-[var(--color-warning)]">⚠</span>
                    {:else}
                      <span class="text-[var(--color-danger)]">✗</span>
                    {/if}
                  {:else}
                    <span class="th-text-muted">—</span>
                  {/if}
                </div>
              </div>
            </div>
          {/if}
        </div>

        <!-- Row 2.5: System Resources (CPU, Memory, Network) -->
        <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
          <!-- CPU -->
          <div class="card p-5 border th-border">
            <div class="flex items-center justify-between mb-3">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.cpu')}</h3>
              <Cpu size={18} class="th-text-secondary" />
            </div>
            <p class="text-2xl font-bold th-text-primary">
              {cpuPercent ?? '--'}
            </p>
            <div class="mt-2 w-full th-bg-tertiary rounded-full h-2 overflow-hidden">
              {#if cpuPercent}
                <div
                  class="h-full {getUsageColor(parseFloat(cpuPercent))} transition-all duration-500"
                  style="width: {cpuPercent}"
                ></div>
              {/if}
            </div>
          </div>

          <!-- Memory -->
          <div class="card p-5 border th-border">
            <div class="flex items-center justify-between mb-3">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.memory')}</h3>
              <MemoryStick size={18} class="th-text-secondary" />
            </div>
            <p class="text-2xl font-bold th-text-primary">
              {currentSystemStats ? formatFileSize(currentSystemStats.memory.total - currentSystemStats.memory.available) : '--'}
              <span class="text-sm font-normal th-text-muted">{memoryPercent}</span>
            </p>
            <p class="text-xs th-text-muted mt-1">
              {t('stats.processMemory')}: {currentSystemStats ? formatFileSize(currentSystemStats.memory.process_rss) : '--'}
            </p>
            <div class="mt-2 w-full th-bg-tertiary rounded-full h-2 overflow-hidden">
              {#if memoryPercent}
                <div
                  class="h-full {getUsageColor(parseFloat(memoryPercent))} transition-all duration-500"
                  style="width: {memoryPercent}"
                ></div>
              {/if}
            </div>
          </div>

          <!-- Network -->
          <div class="card p-5 border th-border">
            <div class="flex items-center justify-between mb-3">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.network')}</h3>
              <Wifi size={18} class="th-text-secondary" />
            </div>
            <p class="text-2xl font-bold th-text-primary">
              <span class="text-base font-medium">↑</span> {netRateUp ?? '--'}
              <span class="text-base font-medium ml-2">↓</span> {netRateDown ?? '--'}
            </p>
            <p class="text-xs th-text-muted mt-1">
              {t('stats.totalUpload')}: {currentSystemStats ? formatFileSize(currentSystemStats.network.bytes_sent) : '--'}
              · {t('stats.totalDownload')}: {currentSystemStats ? formatFileSize(currentSystemStats.network.bytes_recv) : '--'}
            </p>
          </div>
        </div>

        <!-- Row 3: Camera table -->
        <div class="card border th-border">
          <div class="p-5 border-b th-border">
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
                      <td class="font-medium th-text-primary">
                        <span class="inline-block w-2 h-2 rounded-full mr-2 {camera.enabled ? 'bg-[var(--color-success)]' : 'bg-[var(--color-danger)]'}"></span>
                        {camera.name}
                      </td>
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
        <!-- Merge monitoring card -->
        {#if mergeStatus}
          <div class="card border th-border overflow-hidden">
            <button
              class="w-full p-5 flex items-center justify-between hover:th-bg-hover transition-colors cursor-pointer"
              onclick={() => mergeCollapsed = !mergeCollapsed}
            >
              <div class="flex items-center gap-2">
                <GitMerge size={18} class="text-accent" />
                <h3 class="text-lg font-semibold th-text-primary">{t('merge.status')}</h3>
                <span class="badge {mergeStatus.enabled ? 'badge-success' : 'badge-neutral'} text-xs">
                  {mergeStatus.enabled ? t('merge.enabled') : t('merge.disabled')}
                </span>
                {#if mergePending && mergePending.pending && Object.keys(mergePending.pending).length > 0}
                  {@const total = Object.values(mergePending.pending).reduce((a, b) => a + b, 0)}
                  <span class="badge badge-warning text-xs">{t('merge.pendingCount', { total })}</span>
                {/if}
              </div>
              {#if mergeCollapsed}
                <ChevronDown size={18} class="th-text-muted" />
              {:else}
                <ChevronUp size={18} class="th-text-muted" />
              {/if}
            </button>

            {#if !mergeCollapsed}
              <div class="px-5 pb-5 border-t th-border pt-4">
                <div class="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4">
                  <div>
                    <p class="text-xs th-text-muted mb-1">{t('merge.lastRun')}</p>
                    <p class="text-sm font-medium th-text-primary">
                      {mergeStatus.last_run_time && mergeStatus.last_run_time !== '0001-01-01T00:00:00Z'
                        ? formatDate(mergeStatus.last_run_time)
                        : '—'}
                    </p>
                  </div>
                  <div>
                    <p class="text-xs th-text-muted mb-1">{t('merge.segmentsMerged')}</p>
                    <p class="text-sm font-medium th-text-primary">{mergeStatus.segments_merged}</p>
                  </div>
                  <div>
                    <p class="text-xs th-text-muted mb-1">{t('merge.filesCreated')}</p>
                    <p class="text-sm font-medium th-text-primary">{mergeStatus.files_created}</p>
                  </div>
                  <div>
                    <p class="text-xs th-text-muted mb-1">{t('merge.errorCount')}</p>
                    <p class="text-sm font-medium th-text-primary {mergeStatus.error_count > 0 ? 'text-[var(--color-danger)]' : ''}">
                      {mergeStatus.error_count}
                    </p>
                  </div>
                </div>

                {#if mergePending && mergePending.pending && Object.keys(mergePending.pending).length > 0}
                  <div>
                    <p class="text-xs th-text-muted mb-2">{t('merge.pending')}</p>
                    <div class="flex flex-wrap gap-2">
                      {#each Object.entries(mergePending.pending) as [camId, count]}
                        <span class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs th-bg-tertiary th-text-secondary">
                          <span class="font-mono">{camId}</span>
                          <span class="font-semibold th-text-primary">{count}</span>
                        </span>
                      {/each}
                    </div>
                  </div>
                {:else}
                  <p class="text-xs th-text-muted">{t('merge.noPending')}</p>
                {/if}
              </div>
            {/if}
          </div>
        {/if}

        <!-- Charts — Storage Trend & Recordings by Camera -->
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <div class="card p-6 border th-border">
            <h3 class="text-lg font-medium th-text-primary mb-4">{t('stats.storageTrend')}</h3>
            <div class="h-64">
              <canvas id="trendChart"></canvas>
            </div>
          </div>
          <div class="card border th-border overflow-hidden">
            <button
              class="w-full p-5 flex items-center justify-between hover:th-bg-hover transition-colors cursor-pointer"
              onclick={() => {
                cameraChartCollapsed = !cameraChartCollapsed;
                if (!cameraChartCollapsed) {
                  setTimeout(() => rebuildCameraChart(), 50);
                }
              }}
            >
              <h3 class="text-lg font-medium th-text-primary">{t('stats.recordingsByCamera')}</h3>
              {#if cameraChartCollapsed}
                <ChevronDown size={20} class="th-text-muted" />
              {:else}
                <ChevronUp size={20} class="th-text-muted" />
              {/if}
            </button>

            {#if !cameraChartCollapsed}
              {#if allCameraNames.length > 1}
                <div class="px-5 pb-3 border-b th-border">
                  <div class="flex items-center gap-2 mb-2">
                    <span class="text-xs font-medium th-text-muted">{t('stats.filterCameras')}</span>
                    <button
                      class="text-xs text-[var(--color-primary)] hover:underline cursor-pointer"
                      onclick={() => selectAllCameras()}
                    >
                      {t('stats.selectAll')}
                    </button>
                    <span class="text-xs th-text-muted">/</span>
                    <button
                      class="text-xs text-[var(--color-primary)] hover:underline cursor-pointer"
                      onclick={() => deselectAllCameras()}
                    >
                      {t('stats.deselectAll')}
                    </button>
                  </div>
                  <div class="flex flex-wrap gap-2">
                    {#each allCameraNames as name, i}
                      {@const isSelected = selectedCameras.has(name)}
                      <button
                        class="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-medium transition-all duration-200 cursor-pointer"
                        style="background-color: {isSelected ? BAR_COLORS[i % BAR_COLORS.length] : 'var(--bg-tertiary)'}; color: {isSelected ? 'white' : 'var(--text-tertiary)'};"
                        onclick={() => toggleCameraFilter(name)}
                      >
                        {name}
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}
              <div class="p-5">
                <div class="h-64">
                  <canvas id="cameraChart"></canvas>
                </div>
              </div>
            {/if}
          </div>
        </div>

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
