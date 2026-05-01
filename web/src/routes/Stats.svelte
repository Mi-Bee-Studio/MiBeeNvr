<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getStats, listCameras, logout } from '$lib/api';
  import type { StorageStats, Camera } from '$lib/api';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t, onLangChange, getCurrentLang } from '$lib/i18n';
  import { formatFileSize } from '$lib/format';

  // Re-render on language change
  let lang = getCurrentLang();
  const unsubscribe = onLangChange(() => { lang = getCurrentLang(); });

  onDestroy(() => { unsubscribe(); });

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
    if (percentage < 50) return 'bg-emerald-500';
    if (percentage < 80) return 'bg-amber-500';
    return 'bg-red-500';
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

<div class="min-h-screen bg-slate-900">
  <!-- Header -->
  <header class="bg-slate-800 border-b border-slate-700">
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
      <div class="flex items-center justify-between h-16">
        <div class="flex items-center gap-4">
          <h1 class="text-xl font-bold text-slate-100">MiBee NVR</h1>
          <nav class="flex gap-4">
            <a href="#/recordings" class="text-slate-300 hover:text-slate-100 transition-colors">
              {t('nav.recordings')}
            </a>
            <a href="#/cameras" class="text-slate-300 hover:text-slate-100 transition-colors">{t('nav.cameras')}</a>
            <a href="#/stats" class="text-cyan-500 font-medium">{t('nav.stats')}</a>
            <a href="#/settings" class="text-slate-300 hover:text-slate-100 transition-colors">{t('nav.settings')}</a>
        </div>
        <div class="flex items-center gap-3">
          <LanguageSwitcher />
          <button on:click={logout} class="btn btn-ghost">
            {t('nav.logout')}
          </button>
        </div>
      </div>
    </div>
  </header>

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold text-slate-100">{t('stats.title')}</h2>
    </div>

    <!-- Error message -->
    {#if error}
      <div class="mb-4 p-4 bg-red-900/30 border border-red-700 rounded-md text-red-300">
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
              <h3 class="text-sm font-medium text-slate-400">{t('stats.totalStorage')}</h3>
              <span class="text-2xl">💾</span>
            </div>
            <p class="text-3xl font-bold text-slate-100 mb-1">
              {formatFileSize(stats.total_bytes)}
            </p>
            <p class="text-sm text-slate-400">{t('stats.capacity')}</p>
          </div>

          <!-- Used storage -->
          <div class="card p-6 border border-slate-700/60 bg-gradient-to-br from-slate-800 to-slate-800/80">
            <div class="flex items-center justify-between mb-4">
              <span class="text-2xl">📊</span>
            </div>
            <p class="text-4xl font-bold text-slate-100 mb-1">
              {formatFileSize(stats.used_bytes)}
            </p>
            <p class="text-sm text-slate-400">
              {formatPercentage(stats.used_bytes, stats.total_bytes)} {t('stats.used')}
            </p>
          </div>

          <!-- Recordings count -->
          <div class="card p-6 border border-slate-700/60 bg-gradient-to-br from-slate-800 to-slate-800/80">
            <div class="flex items-center justify-between mb-4">
              <span class="text-2xl">🎬</span>
            </div>
            <p class="text-4xl font-bold text-slate-100 mb-1">
              {stats.recording_count.toLocaleString()}
            </p>
            <p class="text-sm text-slate-400">{t('stats.totalRecordings')}</p>
          </div>

          <!-- Cameras count -->
          <div class="card p-6 border border-slate-700/60 bg-gradient-to-br from-slate-800 to-slate-800/80">
            <div class="flex items-center justify-between mb-4">
              <span class="text-2xl">📷</span>
            </div>
            <p class="text-4xl font-bold text-slate-100 mb-1">
              {stats.camera_count}
            </p>
            <p class="text-sm text-slate-400">{t('stats.activeCameras')}</p>
          </div>
        </div>

        <!-- Storage usage bar -->
        <div class="card p-6 border border-slate-700/60">
          <h3 class="text-lg font-semibold text-slate-100 mb-4">{t('stats.storageUsage')}</h3>
          <div class="mb-2">
            <div class="flex justify-between text-sm mb-2">
              <span class="text-slate-400">{t('stats.usedOf', { used: formatFileSize(stats.used_bytes) })}</span>
              <span class="text-slate-400">{t('stats.freeOf', { free: formatFileSize(stats.total_bytes - stats.used_bytes) })}</span>
            </div>
            <div class="w-full bg-slate-700 rounded-full h-4 overflow-hidden">
              <div
                class="h-full {getUsageColor((stats.used_bytes / stats.total_bytes) * 100)} transition-all duration-500"
                style="width: {formatPercentage(stats.used_bytes, stats.total_bytes)}"
              ></div>
            </div>
          </div>
          <p class="text-sm text-slate-400 mt-2">
            {t('stats.ofStorageUsed', { percentage: formatPercentage(stats.used_bytes, stats.total_bytes) })}
          </p>
        </div>

        <!-- Camera list -->
        <div class="card border border-slate-700/60">
          <div class="p-6 border-b border-slate-700/60">
            <h3 class="text-lg font-semibold text-slate-100">{t('stats.cameras')}</h3>
          </div>
          <div class="table-container border-0 rounded-none">
            {#if cameras.length === 0}
              <div class="p-8 text-center text-slate-400">
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
                      <td class="font-medium text-slate-200">{camera.name}</td>
                      <td class="text-slate-400 font-mono text-sm">{camera.id}</td>
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
          <div class="text-center text-sm text-slate-400 py-4">
            <span class="spinner mr-2"></span>
            {t('stats.refreshing')}
          </div>
        {/if}
      </div>
    {/if}
  </main>
</div>
