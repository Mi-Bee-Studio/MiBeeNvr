<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { listPlugins, restartPlugin } from '$lib/api';
  import type { Plugin } from '$lib/api';
  import { t } from '$lib/i18n';
  import { AlertCircle, Package, RefreshCw, CheckCircle2, XCircle, MinusCircle, Clock, Activity, RotateCw } from 'lucide-svelte';
  import { showToast } from '$lib/toast';

  let plugins = $state<Plugin[]>([]);
  let loading = $state(true);
  let error = $state('');
  let restartingPlugin = $state<string | null>(null);
  let refreshTimer: ReturnType<typeof setInterval> | null = null;

  function formatUptime(seconds: number): string {
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ${minutes % 60}m`;
    const days = Math.floor(hours / 24);
    return `${days}d ${hours % 24}h`;
  }

  function statusColor(status: string): string {
    switch (status) {
      case 'running': return 'badge-success';
      case 'error': return 'badge-error';
      case 'stopped': return 'badge-neutral';
      default: return 'badge-neutral';
    }
  }

  function statusIcon(status: string) {
    switch (status) {
      case 'running': return CheckCircle2;
      case 'error': return XCircle;
      case 'stopped': return MinusCircle;
      default: return MinusCircle;
    }
  }

  async function loadPlugins() {
    try {
      plugins = await listPlugins();
      error = '';
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.error');
    } finally {
      loading = false;
    }
  }

  async function handleRestart(name: string) {
    restartingPlugin = name;
    try {
      await restartPlugin(name);
      showToast(t('plugins.restart') + ' — ' + name, 'success');
      await loadPlugins();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    } finally {
      restartingPlugin = null;
    }
  }

  onMount(() => {
    loadPlugins();
    refreshTimer = setInterval(loadPlugins, 10000);
  });

  onDestroy(() => {
    if (refreshTimer) clearInterval(refreshTimer);
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <Package size={28} class="th-text-secondary" />
        <h2 class="text-2xl font-bold th-text-primary">{t('plugins.title')}</h2>
      </div>
      <button
        onclick={loadPlugins}
        class="btn btn-ghost btn-sm"
        disabled={loading}
      >
        <RotateCw size={16} class={loading ? 'animate-spin' : ''} />
      </button>
    </div>

    <!-- Error state -->
    {#if error}
      <div class="card border th-border-danger p-8 text-center">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadPlugins} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    <!-- Loading state -->
    {#if loading}
      <div class="space-y-4">
        {#each Array(2) as _}
          <div class="card border th-border p-6">
            <div class="flex items-start gap-4">
              <div class="h-10 w-10 th-bg-tertiary rounded-lg animate-pulse"></div>
              <div class="flex-1 space-y-3">
                <div class="h-5 w-40 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
                <div class="grid grid-cols-2 md:grid-cols-4 gap-4 pt-2">
                  <div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div>
                  <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse"></div>
                  <div class="h-4 w-28 th-bg-tertiary rounded animate-pulse"></div>
                  <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse"></div>
                </div>
              </div>
            </div>
          </div>
        {/each}
      </div>
    {:else if !error}
      {#if plugins.length === 0}
        <!-- Empty state -->
        <div class="card border th-border p-12 text-center">
          <div class="flex justify-center mb-4 th-text-muted">
            <Package size={48} />
          </div>
          <h3 class="text-lg font-medium th-text-primary mb-2">{t('plugins.noPlugins')}</h3>
        </div>
      {:else}
        <div class="space-y-4">
          {#each plugins as plugin (plugin.name)}
            {@const StatusIcon = statusIcon(plugin.status)}
            <div class="card border th-border p-6">
              <div class="flex flex-col sm:flex-row sm:items-start gap-4">
                <!-- Icon & Name -->
                <div class="flex items-center gap-3 sm:min-w-[200px]">
                  <div class="h-10 w-10 rounded-lg flex items-center justify-center
                    {plugin.status === 'running' ? 'bg-[rgba(16,185,129,0.15)]' : plugin.status === 'error' ? 'bg-[rgba(239,68,68,0.15)]' : 'th-bg-tertiary'}">
                    <Package size={20} class={plugin.status === 'running' ? 'text-emerald-500' : plugin.status === 'error' ? 'text-red-500' : 'th-text-tertiary'} />
                  </div>
                  <div>
                    <h3 class="font-semibold th-text-primary">{plugin.name}</h3>
                    <span class="text-xs th-text-muted">{t('plugins.version')} {plugin.version}</span>
                  </div>
                </div>

                <!-- Details -->
                <div class="flex-1">
                  <div class="flex items-center gap-2 mb-3">
                    <StatusIcon size={16} class={plugin.status === 'running' ? 'text-emerald-500' : plugin.status === 'error' ? 'text-red-500' : 'th-text-tertiary'} />
                    <span class="badge {statusColor(plugin.status)}">
                      {t('plugins.status.' + plugin.status) || plugin.status}
                    </span>
                  </div>

                  <div class="grid grid-cols-2 sm:grid-cols-4 gap-x-6 gap-y-2 text-sm">
                    <div>
                      <span class="th-text-muted">{t('plugins.protocols')}</span>
                      <p class="th-text-secondary font-medium">
                        {#each plugin.protocols as proto, i}
                          {proto}{#if i < plugin.protocols.length - 1}, {/if}
                        {:else}
                          —
                        {/each}
                      </p>
                    </div>
                    <div>
                      <span class="th-text-muted">{t('plugins.supportedEncodings')}</span>
                      <p class="th-text-secondary font-medium">
                        {#each plugin.supported_encodings as enc, i}
                          {enc}{#if i < plugin.supported_encodings.length - 1}, {/if}
                        {:else}
                          —
                        {/each}
                      </p>
                    </div>
                    <div>
                      <span class="th-text-muted flex items-center gap-1"><Clock size={12} /> {t('plugins.uptime')}</span>
                      <p class="th-text-secondary font-medium">{formatUptime(plugin.uptime_seconds)}</p>
                    </div>
                    <div>
                      <span class="th-text-muted flex items-center gap-1"><Activity size={12} /> {t('plugins.restartCount')}</span>
                      <p class="th-text-secondary font-medium">{plugin.restart_count}</p>
                    </div>
                  </div>

                  {#if plugin.capabilities}
                    <div class="mt-3 flex flex-wrap gap-2">
                      {#if plugin.capabilities.hls}
                        <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-[rgba(59,130,246,0.15)] text-blue-400">HLS</span>
                      {/if}
                      {#if plugin.capabilities.ptz}
                        <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-[rgba(168,85,247,0.15)] text-purple-400">PTZ</span>
                      {/if}
                      {#if plugin.capabilities.snapshot}
                        <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-[rgba(245,158,11,0.15)] text-amber-400">Snapshot</span>
                      {/if}
                      {#if plugin.capabilities.discovery}
                        <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-[rgba(16,185,129,0.15)] text-emerald-400">Discovery</span>
                      {/if}
                      {#if plugin.capabilities.auth}
                        <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-[rgba(236,72,153,0.15)] text-pink-400">Auth</span>
                      {/if}
                    </div>
                  {/if}
                </div>

                <!-- Restart button -->
                <div class="sm:ml-auto sm:mt-0 mt-3 sm:mt-0">
                  <button
                    onclick={() => handleRestart(plugin.name)}
                    class="btn btn-ghost btn-sm flex items-center gap-1.5"
                    disabled={restartingPlugin === plugin.name}
                  >
                    {#if restartingPlugin === plugin.name}
                      <span class="spinner"></span>
                      <span class="text-sm">{t('plugins.restarting')}</span>
                    {:else}
                      <RefreshCw size={14} />
                      <span class="text-sm">{t('plugins.restart')}</span>
                    {/if}
                  </button>
                </div>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    {/if}
  </main>
</div>
