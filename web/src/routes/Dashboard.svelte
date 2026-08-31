<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import { getStats, listCameras, healthCheck, getSystemStats, getHealthCameras, getStatsTrends, getStatsCameras } from '$lib/api';
  import type { StorageStats, Camera, HealthResponse, SystemStats, CameraHealthDetail, CameraStorageStats } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatFileSize } from '$lib/format';
  import { Cpu, MemoryStick, HardDrive, Wifi, Activity, CircleCheck, AlertCircle, CirclePause, BarChart3, Database, Loader2, Brain } from 'lucide-svelte';
  import { loadChart, createTrendChart } from '$lib/charts';
  import Tab from '$lib/components/Tab.svelte';
  import CameraFlowTree from '$lib/components/CameraFlowTree.svelte';
  import HealthHistory from './HealthHistory.svelte';
  import TranscodingHistory from './TranscodingHistory.svelte';
  import AIEvents from './AIEvents.svelte';
  import { getMiBeeVisionConnected } from '$lib/mibeevision-status';
  import AiStatusCard from '../components/AiStatusCard.svelte';

  let { initialTab = 'storage' }: { initialTab?: string } = $props();

  // Tab state
  let activeTab = $state('storage');

  $effect(() => {
    activeTab = initialTab;
  });

  // The AI Events tab exists only when MiBeeVision is configured (same
  // visibility rule the old top-level nav item had — the page itself is a
  // dashboard sub-page now).
  let miBeeVisionConnected = $derived(getMiBeeVisionConnected());

  let tabs = $derived([
    { id: 'storage', label: t('dashboard.tab.storage'), icon: HardDrive },
    { id: 'health', label: t('dashboard.tab.health'), icon: Activity },
    { id: 'transcoding', label: t('dashboard.tab.transcoding'), icon: Cpu },
    ...(miBeeVisionConnected ? [{ id: 'ai', label: t('dashboard.tab.ai'), icon: Brain }] : []),
  ]);

  function handleTabChange(tabId: string) {
    activeTab = tabId;
    const hash = tabId === 'storage' ? '#/dashboard' : `#/dashboard/${tabId}`;
    window.location.hash = hash;
    // Lazy-load trends only when the storage tab is opened, not on every 30s
    // poll. Daily aggregates change at most once per day — no need to fetch
    // them unless the user is actually looking at the chart.
    if (tabId === 'storage' && !lastTrends) {
      loadTrends();
    }
  }

  // System resource state
  let stats = $state<StorageStats | null>(null);
  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let statsError = $state('');
  let prevSystemStats = $state<SystemStats | null>(null);
  let currentSystemStats = $state<SystemStats | null>(null);
  let cpuPercent = $state<string | null>(null);
  let memoryPercent = $state<string | null>(null);
  let netRateUp = $state<string | null>(null);
  let netRateDown = $state<string | null>(null);
  let health = $state<HealthResponse | null>(null);
  let healthCameras = $state<Record<string, CameraHealthDetail>>({});
  let healthError = $state('');
  // Per-camera storage footprint (storage-usage card). Cached 2min server-side,
  // so it rides the 30s poll without re-running the GROUP BY.
  let cameraStorage = $state<CameraStorageStats[]>([]);
  let cameraStorageError = $state(false);
  // Camera whose flow tree is expanded in the camera-health list.
  let expandedFlow = $state<string | null>(null);
  // Row order frozen at expand time: the live list re-sorts by score every
  // 30s poll, which would drag an inline expanded panel around. While a row
  // is expanded we keep the order it had when the user clicked.
  let orderSnapshot = $state<string[] | null>(null);

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
    if (percentage < 50) return 'var(--color-success)';
    if (percentage < 80) return 'var(--color-warning)';
    return 'var(--color-danger)';
  }

  // Build camera name lookup map
  let cameraNameMap = $derived.by(() => {
    const map = new Map<string, string>();
    for (const cam of cameras) {
      map.set(cam.id, cam.name || cam.id);
    }
    return map;
  });

  // Build enriched camera health entries (camera info + health detail)
  let cameraHealthEntries = $derived.by(() => {
    const entries: { id: string; name: string; status: string; score: number; factors?: string[]; recording_enabled?: boolean | null }[] = [];
    for (const cam of cameras) {
      const detail = healthCameras[cam.id];
      entries.push({
        id: cam.id,
        name: cam.name || cam.id,
        status: detail?.latest_status || cam.status || 'unknown',
        score: detail?.score ?? -1,
        factors: detail?.score_factors,
        recording_enabled: cam.recording_enabled,
      });
    }
    // Sort: unhealthy first (lowest score), then by name — unless a row is
    // expanded, in which case the frozen order wins (see orderSnapshot).
    if (expandedFlow && orderSnapshot) {
      const idx = new Map(orderSnapshot.map((id, i) => [id, i]));
      entries.sort((a, b) => (idx.get(a.id) ?? 1e9) - (idx.get(b.id) ?? 1e9));
    } else {
      entries.sort((a, b) => {
        if (a.score !== b.score) return a.score - b.score;
        return a.name.localeCompare(b.name);
      });
    }
    return entries;
  });

  // Translate a raw backend factor string ("recent_anomalies: -15 (4
  // anomalies in last hour (>3))") into a friendly localized line.
  const factorNames: Record<string, string> = {
    offline_duration: 'offlineDuration',
    recent_anomalies: 'recentAnomalies',
    low_uptime: 'lowUptime',
  };

  function friendlyFactor(raw: string): string {
    const m = raw.match(/^(\w+):\s*([+-]\d+)\s*\((.*)\)$/);
    if (!m) return raw;
    const [, name, impact, detail] = m;
    const label = factorNames[name] ? t(`health.factor.${factorNames[name]}`) : name;
    let text: string;
    let d: RegExpMatchArray | null;
    if ((d = detail.match(/^offline for (.+) \(>5min\)$/))) text = t('health.factor.d.offline5', { d: d[1] });
    else if ((d = detail.match(/^offline for (.+) \(>30min\)$/))) text = t('health.factor.d.offline30', { d: d[1] });
    else if ((d = detail.match(/^(\d+) anomalies in last hour \(>10\)$/))) text = t('health.factor.d.anomalies10', { n: d[1] });
    else if ((d = detail.match(/^(\d+) anomalies in last hour \(>3\)$/))) text = t('health.factor.d.anomalies3', { n: d[1] });
    else if ((d = detail.match(/^uptime ([\d.]+)% \(<80%\)$/))) text = t('health.factor.d.uptime80', { p: d[1] });
    else if ((d = detail.match(/^uptime ([\d.]+)% \(<95%\)$/))) text = t('health.factor.d.uptime95', { p: d[1] });
    else text = detail;
    return `${label} ${impact} · ${text}`;
  }

  // Toggle a camera's flow tree and scroll the expanded panel into view so
  // the user never has to hunt for it manually. 'center' puts the whole
  // block (factors + tree) in the middle of the viewport — 'nearest' left
  // the tail end below the fold.
  async function toggleFlow(camId: string): Promise<void> {
    if (expandedFlow === camId) {
      expandedFlow = null;
      orderSnapshot = null;
      return;
    }
    orderSnapshot = cameraHealthEntries.map((e) => e.id);
    expandedFlow = camId;
  }

  function statusColor(status: string): string {
    const s = status.toLowerCase();
    if (s === 'recording' || s === 'active' || s === 'healthy') return 'var(--color-success)';
    if (s === 'reconnecting' || s === 'warning' || s === 'degraded') return 'var(--color-warning)';
    return 'var(--color-danger)';
  }

  function statusLabel(status: string, recordingEnabled?: boolean | null): string {
    const s = status.toLowerCase();
    if (s === 'recording' || s === 'active') {
      // Distinguish live-only (recording disabled) from active disk recording.
      return recordingEnabled === false ? t('cameras.statusLive') : t('cameras.statusRecording');
    }
    if (s === 'reconnecting') return t('health.status.reconnecting');
    if (s === 'error' || s === 'failed') return t('cameras.statusError');
    if (s === 'stopped') return t('cameras.statusStopped');
    // Camera-health statuses from /api/cameras/{id}/health (model.HealthStatus):
    // healthy / warning / unknown. Without these branches the raw English enum
    // value leaked into the UI (#170). statusColor() above already mapped these
    // to colors, so this also fixes the "right color, wrong text" inconsistency.
    if (s === 'healthy') return t('health.statusHealthy');
    if (s === 'warning' || s === 'degraded') return t('health.statusWarning');
    if (s === 'unknown') return t('health.statusUnknown');
    return s;
  }

  function scoreColor(score: number): string {
    if (score < 0) return 'th-text-muted';
    if (score >= 80) return 'color: var(--color-success)';
    if (score >= 30) return 'color: var(--color-warning)';
    return 'color: var(--color-danger)';
  }

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
      statsError = '';
    } catch (e) {
      console.error('Failed to load stats:', e);
      statsError = String(e);
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
      healthError = '';
    } catch (e) {
      console.warn('Failed to load health cameras:', e);
      healthError = String(e);
    }
  }

  async function loadCameraStorage() {
    try {
      cameraStorage = await getStatsCameras();
      cameraStorageError = false;
    } catch (e) {
      console.warn('Failed to load camera storage stats:', e);
      // Keep the previous rows — a transient failure (e.g. the retention
      // sweep contending with the GROUP BY) must not blank the card; the
      // 30s poll retries. Only surface the error before the first load.
      if (cameraStorage.length === 0) cameraStorageError = true;
    }
  }

  // Storage-usage rows: cameras with recordings (from the API, archived ones
  // flagged) plus active cameras without any recordings shown as 0 B.
  let cameraStorageRows = $derived.by(() => {
    const seen = new Set(cameraStorage.map((s) => s.camera_id));
    const rows = [...cameraStorage];
    for (const cam of cameras) {
      if (!seen.has(cam.id)) {
        rows.push({ camera_id: cam.id, camera_name: cam.name || cam.id, archived: false, recordings: 0, total_bytes: 0 });
      }
    }
    rows.sort((a, b) => b.total_bytes - a.total_bytes || a.camera_name.localeCompare(b.camera_name));
    return rows;
  });

  let cameraStorageTotal = $derived(cameraStorage.reduce((sum, s) => sum + s.total_bytes, 0));

  // Trend chart loading — 14 days for a meaningful trend (was 7).
  async function loadTrends() {
    try {
      const trends = await getStatsTrends(14);
      if (trends && trends.length > 0) {
        if (!ChartJs) ChartJs = await loadChart();
        await createChart(trends);
      }
    } catch (e) {
      console.error('Failed to load trends:', e);
    }
  }

  async function createChart(trends: { date: string; total_size: number; camera_sizes?: Record<string, number> }[]) {
    lastTrends = trends;
    if (trendChart) { trendChart.destroy(); trendChart = null; }
    // Wait for Svelte to flush the DOM — lastTrends was just set, so the canvas
    // may not exist yet (the {:else if lastTrends} branch hasn't rendered). Without
    // this, getElementById returns null and the chart never appears.
    await tick();
    const ctx = document.getElementById('dashboardTrendChart') as HTMLCanvasElement;
    if (ctx) {
      trendChart = createTrendChart(ChartJs, ctx, trends, t('dashboard.perDay'));
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
      loadCameraStorage(),
    ]).finally(() => { loading = false; });

    // Lazy-load trends after core data — storage is the default tab, so the chart
    // needs data, but we don't block the initial render on the (potentially slow)
    // GROUP BY scan. The backend cache (2min TTL) makes this near-instant after
    // the first load.
    if (activeTab === 'storage') {
      void loadTrends();
    }

    // Quick second sample after 2s so CPU/network show without waiting 30s
    const quickSample = window.setTimeout(() => loadSystemStats(), 2000);

    // Auto-refresh every 30 seconds — only the lightweight, frequently-changing
    // data. Trends are NOT polled (they change once/day; backend caches 2min;
    // loaded lazily on tab open). loadHealth is dropped from the poll — the
    // Dashboard's health card derives from healthCameras (lighter, per-camera
    // detail), and the top-level /api/health is only needed once on mount for
    // the overall status badge.
    refreshInterval = window.setInterval(() => {
      loadStats();
      loadCameras();
      loadSystemStats();
      loadHealthCameras();
      loadCameraStorage();
    }, 30000);

    // Re-color existing chart on theme change WITHOUT refetching data.
    // The old code called loadTrends() (a full DB scan) just to change colors.
    const observer = new MutationObserver(() => {
      if (trendChart && lastTrends) {
        void createChart(lastTrends); // canvas already in DOM; just re-render colors
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

    <!-- System Resources — compact single-row layout -->
    <div class="card p-4 border th-border mb-4">
      {#if loading && !currentSystemStats}
        <div class="flex items-center justify-center py-3">
          <Loader2 size={18} class="th-text-secondary animate-spin" />
        </div>
      {:else}
        <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
          <!-- CPU -->
          <div class="flex flex-col gap-1.5">
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium th-text-muted flex items-center gap-1.5">
                <Cpu size={14} />
                {t('stats.cpu')}
              </span>
              <span class="text-sm font-bold th-text-primary">
                {#if cpuPercent}
                  {cpuPercent}
                {:else if currentSystemStats}
                  <Loader2 size={12} class="animate-spin th-text-secondary" />
                {:else}
                  --
                {/if}
              </span>
            </div>
            <div class="w-full th-bg-tertiary rounded-full h-1.5 overflow-hidden">
              {#if cpuPercent}
                <div class="h-full rounded-full transition-all duration-500" style="width: {cpuPercent}; background-color: {getUsageColor(parseFloat(cpuPercent))}"></div>
              {/if}
            </div>
          </div>

          <!-- Memory -->
          <div class="flex flex-col gap-1.5">
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium th-text-muted flex items-center gap-1.5">
                <MemoryStick size={14} />
                {t('stats.memory')}
              </span>
              <span class="text-sm font-bold th-text-primary">
                {#if currentSystemStats}
                  {formatFileSize(currentSystemStats.memory.total - currentSystemStats.memory.available)}
                  <span class="text-xs font-normal th-text-muted ml-1">{memoryPercent ?? ''}</span>
                {:else}
                  --
                {/if}
              </span>
            </div>
            <div class="w-full th-bg-tertiary rounded-full h-1.5 overflow-hidden">
              {#if memoryPercent}
                <div class="h-full rounded-full transition-all duration-500" style="width: {memoryPercent}; background-color: {getUsageColor(parseFloat(memoryPercent))}"></div>
              {/if}
            </div>
          </div>

          <!-- Disk -->
          <div class="flex flex-col gap-1.5">
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium th-text-muted flex items-center gap-1.5">
                <HardDrive size={14} />
                {t('stats.totalStorage')}
              </span>
              <span class="text-sm font-bold th-text-primary">
                {#if stats}
                  {formatFileSize(stats.used_bytes)}
                  <span class="text-xs font-normal th-text-muted ml-1">{formatPercentage(stats.used_bytes, stats.total_bytes)}</span>
                {:else if statsError}
                  <span class="text-xs th-text-muted">--</span>
                {:else}
                  --
                {/if}
              </span>
            </div>
            <div class="w-full th-bg-tertiary rounded-full h-1.5 overflow-hidden">
              {#if stats && stats.total_bytes > 0}
                {@const diskPct = (stats.used_bytes / stats.total_bytes) * 100}
                <div class="h-full rounded-full transition-all duration-500" style="width: {diskPct}%; background-color: {getUsageColor(diskPct)}"></div>
              {/if}
            </div>
          </div>

          <!-- Network -->
          <div class="flex flex-col gap-1.5">
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium th-text-muted flex items-center gap-1.5">
                <Wifi size={14} />
                {t('stats.network')}
              </span>
              <span class="text-sm font-bold th-text-primary">
                {#if netRateUp || netRateDown}
                  <span class="text-xs">↑</span>{netRateUp ?? '--'}
                  <span class="text-xs ml-1.5">↓</span>{netRateDown ?? '--'}
                {:else if currentSystemStats}
                  <Loader2 size={12} class="animate-spin th-text-secondary" />
                {:else}
                  --
                {/if}
              </span>
            </div>
            <p class="text-[10px] th-text-muted truncate">
              {#if currentSystemStats}
                {t('stats.totalUpload')}: {formatFileSize(currentSystemStats.network.bytes_sent)}
                · {t('stats.totalDownload')}: {formatFileSize(currentSystemStats.network.bytes_recv)}
              {/if}
            </p>
          </div>
        </div>
      {/if}
    </div>

    <!-- Camera Health — per-camera status list -->
    <div class="card p-4 border th-border mb-6">
      <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
        <Activity size={16} class="text-accent" />
        {t('dashboard.healthSummary')}
      </h3>
      {#if healthError}
        <div class="flex items-center gap-2 text-sm th-text-muted py-2">
          <AlertCircle size={14} class="th-text-secondary" />
          <span>{t('common.error')}</span>
        </div>
      {:else if cameraHealthEntries.length === 0}
        <p class="text-sm th-text-muted">{t('health.noCameras')}</p>
      {:else}
        <div class="space-y-1">
          {#each cameraHealthEntries as cam (cam.id)}
            <div
              class="flex items-center gap-3 py-1.5 px-2 rounded-md hover:bg-[var(--bg-tertiary)] transition-colors"
              class:row-active={expandedFlow === cam.id}
            >
              <!-- Status dot -->
              <span class="w-2 h-2 rounded-full flex-shrink-0" style="background-color: {statusColor(cam.status)}"></span>

              <!-- Camera name -->
              <span class="text-sm th-text-primary flex-1 truncate">{cam.name}</span>

              <!-- Status badge -->
              <span class="text-xs th-text-secondary hidden sm:inline">{statusLabel(cam.status, cam.recording_enabled)}</span>

              <!-- Health score -->
              {#if cam.score >= 0}
                <span class="text-xs font-semibold tabular-nums min-w-[2rem] text-right" style="{scoreColor(cam.score)}">
                  {cam.score}
                </span>
              {:else}
                <span class="text-xs th-text-muted tabular-nums min-w-[2rem] text-right">--</span>
              {/if}

              <!-- Expand: live flow tree for troubleshooting this camera -->
              <button
                class="expand-btn"
                aria-expanded={expandedFlow === cam.id}
                title={t('flow.expandHint')}
                onclick={() => toggleFlow(cam.id)}
              >
                {expandedFlow === cam.id ? '▾' : '▸'}
              </button>
            </div>
            {#if expandedFlow === cam.id}
              <div class="flow-expand" id="flow-{cam.id}">
                {#if cam.factors && cam.factors.length > 0}
                  <div class="factors">
                    {#each cam.factors as raw}
                      {@const f = friendlyFactor(raw)}
                      <span style="color: {f.includes('-') ? 'var(--color-danger)' : 'var(--color-success)'}">{f}</span>
                    {/each}
                  </div>
                {/if}
                <CameraFlowTree cameraId={cam.id} name={cam.name} status={cam.status} recordingEnabled={cam.recording_enabled !== false} />
              </div>
            {/if}
          {/each}
        </div>
      {/if}
    </div>

    <!-- Camera Storage — per-camera footprint, sibling of the health card -->
    <div class="card p-4 border th-border mb-6">
      <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
        <Database size={16} class="text-accent" />
        {t('dashboard.storageByCamera')}
        {#if cameraStorageTotal > 0}
          <span class="ml-auto text-xs th-text-muted font-normal tabular-nums">{formatFileSize(cameraStorageTotal)}</span>
        {/if}
      </h3>
      {#if cameraStorageError}
        <div class="flex items-center gap-2 text-sm th-text-muted py-2">
          <AlertCircle size={14} class="th-text-secondary" />
          <span>{t('common.error')}</span>
        </div>
      {:else if cameraStorageRows.length === 0}
        <p class="text-sm th-text-muted">{t('health.noCameras')}</p>
      {:else}
        <div class="space-y-1">
          {#each cameraStorageRows as cam (cam.camera_id)}
            <div class="flex items-center gap-3 py-1.5 px-2 rounded-md hover:bg-[var(--bg-tertiary)] transition-colors">
              <!-- Camera name -->
              <span class="text-sm th-text-primary flex-1 truncate">
                {cam.camera_name}
                {#if cam.archived}
                  <span class="text-xs th-text-muted ml-1">({t('cameras.status.archived')})</span>
                {/if}
              </span>

              <!-- Segment count -->
              <span class="text-xs th-text-muted hidden sm:inline tabular-nums">
                {t('dashboard.storageSegments', { n: cam.recordings })}
              </span>

              <!-- Share of total recordings storage -->
              <div class="w-20 md:w-28 th-bg-tertiary rounded-full h-1.5 overflow-hidden flex-shrink-0">
                {#if cameraStorageTotal > 0 && cam.total_bytes > 0}
                  {@const pct = Math.min(100, (cam.total_bytes / cameraStorageTotal) * 100)}
                  <div class="h-full rounded-full transition-all duration-500" style="width: {pct}%; background-color: {getUsageColor(pct)}"></div>
                {/if}
              </div>

              <!-- Size -->
              <span class="text-sm font-semibold th-text-primary tabular-nums min-w-[3.5rem] text-right">
                {cam.total_bytes > 0 ? formatFileSize(cam.total_bytes) : '--'}
              </span>
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- AI Status -->
    <AiStatusCard />


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
            <p class="text-xs th-text-muted mb-3">{t('stats.recordingGrowthHint')}</p>
            <div class="h-64 sm:h-72">
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
    {:else if activeTab === 'ai'}
      <div class="ai-tab-content">
        <AIEvents />
      </div>
    {/if}
  </main>
</div>

<style>
  .expand-btn {
    background: none;
    border: none;
    color: var(--text-tertiary);
    cursor: pointer;
    font-size: 0.8rem;
    padding: 0 0.3rem;
    line-height: 1;
  }
  .expand-btn:hover {
    color: var(--text-primary);
  }
  .factors {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem 1rem;
    font-size: 11px;
    padding: 0.25rem 0 0 1.3rem;
  }
  .flow-expand {
    border: 1px solid var(--border, rgba(128, 128, 128, 0.25));
    border-radius: 8px;
    margin: 0.5rem 0;
    padding: 0.5rem 0.6rem;
    background: var(--bg-secondary, transparent);
  }
  .row-active {
    background: var(--bg-tertiary, rgba(128, 128, 128, 0.12));
  }
  .health-tab-content > :global(:first-child) {
    padding-top: 0 !important;
  }

  .health-tab-content > :global(.min-h-screen) {
    min-height: auto !important;
  }

  /* AIEvents ships its own page-level padding (p-4 md:p-6 max-w-6xl) — trim
     the top padding so it aligns with the other tab contents. */
  .ai-tab-content > :global(:first-child) {
    padding-top: 0 !important;
  }
</style>
