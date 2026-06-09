<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getStats, listCameras, healthCheck, getSystemStats, getHealthCameras, getStatsTrends } from '$lib/api';
  import type { StorageStats, Camera, HealthResponse, SystemStats, CameraHealthDetail } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatFileSize, formatDate } from '$lib/format';
  import { Cpu, MemoryStick, HardDrive, Wifi, Activity, CircleCheck, AlertCircle, CirclePause, BarChart3 } from 'lucide-svelte';
  import { loadChart, createTrendChart, aggregateCameraTotals, BAR_COLORS } from '$lib/charts';
  import { getEffectiveTheme } from '$lib/preferences';
  import Tab from '$lib/components/Tab.svelte';
  import HealthHistory from './HealthHistory.svelte';
  import TranscodingHistory from './TranscodingHistory.svelte';

  let { initialTab = 'storage' }: { initialTab?: string } = $props();

  // Tab state
  let activeTab = $state('storage');

  $effect(() => {
    activeTab = initialTab;
  });

  let tabs = $derived([
    { id: 'storage', label: t('dashboard.tab.storage'), icon: HardDrive },
    { id: 'health', label: t('dashboard.tab.health'), icon: Activity },
    { id: 'transcoding', label: t('dashboard.tab.transcoding'), icon: Cpu },
  ]);

  function handleTabChange(tabId: string) {
    activeTab = tabId;
    const hash = tabId === 'storage' ? '#/dashboard' : `#/dashboard/${tabId}`;
    window.location.hash = hash;
  }

  // System resource state
  let stats = $state<StorageStats | null>(null);
  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let prevSystemStats = $state<SystemStats | null>(null);
  let currentSystemStats = $state<SystemStats | null>(null);
  let cpuPercent = $state<string | null>(null);
  let memoryPercent = $state<string | null>(null);
  let netRateUp = $state<string | null>(null);
  let netRateDown = $state<string | null>(null);
  let health = $state<HealthResponse | null>(null);
  let healthCameras = $state<Record<string, CameraHealthDetail>>({});

  // Chart state
  let ChartJs: any = null;
  let trendChart: any = null;
  let lastTrends = $state<any>(null);

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

  // Compute health summary from health cameras
  let healthSummary = $derived.by(() => {
    const entries = Object.values(healthCameras);
    let online = 0, warning = 0, offline = 0;
    for (const cam of entries) {
      const s = cam.latest_status?.toLowerCase() || '';
      if (s === 'recording' || s === 'active' || s === 'healthy') {
        online++;
      } else if (s === 'reconnecting' || s === 'warning' || s === 'degraded') {
        warning++;
      } else if (s === 'error' || s === 'failed' || s === 'unhealthy') {
        offline++;
      } else {
        // Any other status: count as offline
        offline++;
      }
    }
    return { online, warning, offline, total: entries.length };
  });

  // Load system stats
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

  async function loadStats() {
    try {
      stats = await getStats();
    } catch (e) {
      console.error('Failed to load stats:', e);
    }
  }

  async function loadCameras() {
    try {
      cameras = await listCameras();
    } catch (e) {
      console.error('Failed to load cameras:', e);
    }
  }

  async function loadHealth() {
    try {
      health = await healthCheck();
    } catch (e) {
      console.error('Failed to load health:', e);
    }
  }

  async function loadHealthCameras() {
    try {
      healthCameras = await getHealthCameras();
    } catch (e) {
      console.warn('Failed to load health cameras:', e);
    }
  }

  // Trend chart loading
  async function loadTrends() {
    try {
      const trends = await getStatsTrends(7);
      if (trends && trends.length > 0) {
        if (!ChartJs) ChartJs = await loadChart();
        createChart(trends);
      }
    } catch (e) {
      console.error('Failed to load trends:', e);
    }
  }

  function createChart(trends: { date: string; total_size: number; cameras?: Record<string, number> }[]) {
    lastTrends = trends;
    if (trendChart) { trendChart.destroy(); trendChart = null; }
    const ctx = document.getElementById('dashboardTrendChart') as HTMLCanvasElement;
    if (ctx) {
      trendChart = createTrendChart(ChartJs, ctx, trends);
    }
  }

  function rebuildTrendChart() {
    if (trendChart) { trendChart.destroy(); trendChart = null; }
    const ctx = document.getElementById('dashboardTrendChart') as HTMLCanvasElement;
    if (ctx && lastTrends) {
      trendChart = createTrendChart(ChartJs, ctx, lastTrends);
    }
    // If canvas is not yet in DOM (tab just switched), try again after DOM update
    if (!ctx && lastTrends) {
      requestAnimationFrame(() => {
        const retryCtx = document.getElementById('dashboardTrendChart') as HTMLCanvasElement;
        if (retryCtx && lastTrends) {
          trendChart = createTrendChart(ChartJs, retryCtx, lastTrends);
        }
      });
    }
  }

  let refreshInterval: number;

  onMount(() => {
    loading = true;
    Promise.all([
      loadStats(),
      loadCameras(),
      loadSystemStats(),
      loadHealth(),
      loadHealthCameras(),
      loadTrends(),
    ]).finally(() => { loading = false; });

    // Quick second sample after 2s so CPU/network show without waiting 30s
    const quickSample = window.setTimeout(() => loadSystemStats(), 2000);

    // Auto-refresh every 30 seconds
    refreshInterval = window.setInterval(() => {
      loadStats();
      loadCameras();
      loadSystemStats();
      loadHealth();
      loadHealthCameras();
      loadTrends();
    }, 30000);

    // Re-create charts when theme changes
    const observer = new MutationObserver(() => {
      if (trendChart) {
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
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold th-text-primary">{t('nav.dashboard')}</h2>
    </div>

    <!-- System Resource Cards -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
      <!-- CPU -->
      <div class="card p-5 border th-border">
        <div class="flex items-center justify-between mb-3">
          <h3 class="text-sm font-medium th-text-muted">{t('stats.cpu')}</h3>
          <Cpu size={18} class="th-text-secondary" />
        </div>
        <p class="text-2xl font-bold th-text-primary">{cpuPercent ?? '--'}</p>
        <div class="mt-2 w-full th-bg-tertiary rounded-full h-2 overflow-hidden">
          {#if cpuPercent}
            <div class="h-full {getUsageColor(parseFloat(cpuPercent))} transition-all duration-500" style="width: {cpuPercent}"></div>
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
          <span class="text-sm font-normal th-text-muted">{memoryPercent ?? ''}</span>
        </p>
        <div class="mt-2 w-full th-bg-tertiary rounded-full h-2 overflow-hidden">
          {#if memoryPercent}
            <div class="h-full {getUsageColor(parseFloat(memoryPercent))} transition-all duration-500" style="width: {memoryPercent}"></div>
          {/if}
        </div>
      </div>

      <!-- Disk (Storage) -->
      <div class="card p-5 border th-border">
        <div class="flex items-center justify-between mb-3">
          <h3 class="text-sm font-medium th-text-muted">{t('stats.totalStorage')}</h3>
          <HardDrive size={18} class="th-text-secondary" />
        </div>
        <p class="text-2xl font-bold th-text-primary">
          {stats ? formatFileSize(stats.used_bytes) : '--'}
          <span class="text-sm font-normal th-text-muted">
            {stats ? formatPercentage(stats.used_bytes, stats.total_bytes) : ''}
          </span>
        </p>
        {#if stats}
          <div class="mt-2 w-full th-bg-tertiary rounded-full h-2 overflow-hidden">
            <div
              class="h-full {getUsageColor((stats.used_bytes / stats.total_bytes) * 100)} transition-all duration-500"
              style="width: {formatPercentage(stats.used_bytes, stats.total_bytes)}"
            ></div>
          </div>
        {/if}
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

    <!-- Camera Health Summary -->
    <div class="card p-5 border th-border mb-6">
      <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
        <Activity size={16} class="text-accent" />
        {t('dashboard.healthSummary')}
      </h3>
      {#if healthSummary.total > 0}
        <div class="flex flex-wrap gap-6">
          <div class="flex items-center gap-2">
            <CircleCheck size={16} class="text-[var(--color-success)]" />
            <span class="text-sm th-text-primary">
              <span class="font-semibold">{healthSummary.online}</span> {t('stats.healthy')}
            </span>
          </div>
          <div class="flex items-center gap-2">
            <AlertCircle size={16} class="text-[var(--color-warning)]" />
            <span class="text-sm th-text-primary">
              <span class="font-semibold">{healthSummary.warning}</span> {t('stats.degraded')}
            </span>
          </div>
          <div class="flex items-center gap-2">
            <CirclePause size={16} class="text-[var(--color-danger)]" />
            <span class="text-sm th-text-primary">
              <span class="font-semibold">{healthSummary.offline}</span> {t('stats.unhealthy')}
            </span>
          </div>
          <div class="flex items-center gap-2 text-sm th-text-muted ml-auto">
            {t('stats.activeCameras')}: {cameras.filter(c => c.enabled).length}/{cameras.length}
          </div>
        </div>
      {:else}
        <p class="text-sm th-text-muted">{t('health.noCameras')}</p>
      {/if}
    </div>

    <!-- Tabs -->
    <Tab {tabs} {activeTab} onchange={handleTabChange} />

    <!-- Tab Content -->
    {#if activeTab === 'storage'}
      <!-- Storage Trends Tab -->
      <div class="mt-4 space-y-4">
        {#if loading && !lastTrends}
          <div class="card p-8 flex justify-center">
            <div class="spinner spinner-lg"></div>
          </div>
        {:else if lastTrends}
          <div class="card p-5 border th-border">
            <div class="h-56 sm:h-64">
              <canvas id="dashboardTrendChart"></canvas>
            </div>
          </div>
        {:else}
          <div class="card p-8 text-center th-text-muted">
            <BarChart3 size={32} class="mx-auto mb-2 opacity-50" />
            <p class="text-sm">{t('stats.storageTrend')}</p>
          </div>
        {/if}
      </div>
    {:else if activeTab === 'health'}
      <div class="health-tab-content">
        <HealthHistory />
      </div>
    {:else if activeTab === 'transcoding'}
      <TranscodingHistory />
    {/if}
  </main>
</div>

<style>
  .health-tab-content > :global(:first-child) {
    padding-top: 0 !important;
  }

  .health-tab-content > :global(.min-h-screen) {
    min-height: auto !important;
  }
</style>
