<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getStats, listCameras } from '$lib/api';
  import type { StorageStats, Camera } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatFileSize } from '$lib/format';

  let stats: StorageStats | null = null;
  let cameras: Camera[] = [];
  let loading = true;
  let error = '';

  // Auto-refresh interval
  let refreshInterval: number;

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

  // Lifecycle
  onMount(() => {
    loadStats();
    loadCameras();

    // Auto-refresh every 30 seconds
    refreshInterval = window.setInterval(() => {
      loadStats();
      loadCameras();
    }, 30000);

    return () => {
      if (refreshInterval) clearInterval(refreshInterval);
    };
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
          <div class="card p-6">
            <div class="flex items-center justify-between mb-4">
              <h3 class="text-sm font-medium th-text-muted">{t('stats.totalStorage')}</h3>
              <span class="text-2xl">💾</span>
            </div>
            <p class="text-3xl font-bold th-text-primary mb-1">
              {formatFileSize(stats.total_bytes)}
            </p>
            <p class="text-sm th-text-muted">{t('stats.capacity')}</p>
          </div>

          <!-- Used storage -->
          <div class="card p-6 border th-border bg-gradient-to-br from-[var(--bg-secondary)] to-[rgba(20,20,20,0.8)]">
            <div class="flex items-center justify-between mb-4">
              <span class="text-2xl">📊</span>
            </div>
            <p class="text-4xl font-bold th-text-primary mb-1">
              {formatFileSize(stats.used_bytes)}
            </p>
            <p class="text-sm th-text-muted">
              {formatPercentage(stats.used_bytes, stats.total_bytes)} {t('stats.used')}
            </p>
          </div>

          <!-- Recordings count -->
          <div class="card p-6 border th-border bg-gradient-to-br from-[var(--bg-secondary)] to-[rgba(20,20,20,0.8)]">
            <div class="flex items-center justify-between mb-4">
              <span class="text-2xl">🎬</span>
            </div>
            <p class="text-4xl font-bold th-text-primary mb-1">
              {stats.recording_count.toLocaleString()}
            </p>
            <p class="text-sm th-text-muted">{t('stats.totalRecordings')}</p>
          </div>

          <!-- Cameras count -->
          <div class="card p-6 border th-border bg-gradient-to-br from-[var(--bg-secondary)] to-[rgba(20,20,20,0.8)]">
            <div class="flex items-center justify-between mb-4">
              <span class="text-2xl">📷</span>
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
                    <tr>
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
