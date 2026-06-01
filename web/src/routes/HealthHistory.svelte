<script lang="ts">
  import { onMount } from 'svelte';
  import { getHealthEvents, getHealthCameras, listCameras } from '$lib/api';
  import type { HealthEvent, HealthEventsResponse, Camera, CameraHealthDetail } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatDate } from '$lib/format';
  import { AlertCircle, Activity } from 'lucide-svelte';
  import Pagination from '../components/Pagination.svelte';

  let events = $state<HealthEvent[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state('');
  let cameraFilter = $state('');
  let eventTypeFilter = $state('');
  let cameras = $state<Camera[]>([]);
  let healthData = $state<Record<string, CameraHealthDetail>>({});
  let healthLoading = $state(true);
  let page = $state(0);
  let expandedCamera = $state<string | null>(null);
  const pageSize = 20;

  const eventTypes = [
    { value: '', label: () => t('health.filter.allTypes') },
    { value: 'connection_lost', label: () => t('health.eventTypes.connectionLost') },
    { value: 'connection_restored', label: () => t('health.eventTypes.connectionRestored') },
    { value: 'stream_anomaly', label: () => t('health.eventTypes.streamAnomaly') },
    { value: 'freeze_detected', label: () => t('health.eventTypes.freezeDetected') },
    { value: 'freeze_recovered', label: () => t('health.eventTypes.freezeRecovered') },
  ];

  function scoreColor(score: number): string {
    if (score >= 80) return 'var(--color-success)';
    if (score >= 30) return 'var(--color-warning)';
    return 'var(--color-danger)';
  }

  function scoreBgColor(score: number): string {
    if (score >= 80) return 'rgba(16, 185, 129, 0.1)';
    if (score >= 30) return 'rgba(245, 158, 11, 0.1)';
    return 'rgba(239, 68, 68, 0.1)';
  }

  function scoreBorderColor(score: number): string {
    if (score >= 80) return 'rgba(16, 185, 129, 0.25)';
    if (score >= 30) return 'rgba(245, 158, 11, 0.25)';
    return 'rgba(239, 68, 68, 0.25)';
  }

  function scoreLabel(score: number): string {
    if (score >= 80) return t('health.score.excellent');
    if (score >= 30) return t('health.score.good');
    return t('health.score.poor');
  }

  function statusDotColor(status: string): string {
    const s = status.toLowerCase();
    if (s === 'recording' || s === 'active' || s === 'healthy') return 'var(--color-success)';
    if (s === 'reconnecting' || s === 'warning') return 'var(--color-warning)';
    if (s === 'error' || s === 'failed' || s === 'unhealthy') return 'var(--color-danger)';
    return 'var(--text-tertiary)';
  }

  function statusLabel(status: string): string {
    const s = status.toLowerCase();
    if (s === 'recording' || s === 'active') return t('health.status.recording');
    if (s === 'reconnecting') return t('health.status.reconnecting');
    if (s === 'error' || s === 'failed') return t('health.status.error');
    return t('health.status.unknown');
  }

  function statusBadgeClass(status: string): string {
    switch (status) {
      case 'healthy': return 'badge badge-success';
      case 'warning': return 'badge bg-amber-100 text-amber-800 dark:bg-amber-900/50 dark:text-amber-300';
      case 'error': return 'badge badge-danger';
      default: return 'badge badge-neutral';
    }
  }

  function eventTypeLabel(eventType: string): string {
    switch (eventType) {
      case 'connection_lost': return t('health.eventTypes.connectionLost');
      case 'connection_restored': return t('health.eventTypes.connectionRestored');
      case 'stream_anomaly': return t('health.eventTypes.streamAnomaly');
      case 'freeze_detected': return t('health.eventTypes.freezeDetected');
      case 'freeze_recovered': return t('health.eventTypes.freezeRecovered');
      default: return eventType;
    }
  }

  function getCameraName(cameraId: string): string {
    const camera = cameras.find(c => c.id === cameraId);
    return camera ? camera.name : cameraId;
  }

  let healthEntries = $derived(Object.entries(healthData));

  async function loadEvents() {
    loading = true;
    error = '';
    try {
      const response: HealthEventsResponse = await getHealthEvents({
        camera_id: cameraFilter || undefined,
        event_type: (eventTypeFilter || undefined) as HealthEvent['event_type'] | undefined,
        limit: pageSize,
        offset: page * pageSize
      });
      events = response.events;
      total = response.total;
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.error');
    } finally {
      loading = false;
    }
  }

  async function loadCameras() {
    try {
      cameras = await listCameras();
    } catch (e) {
      console.warn('Failed to load cameras:', e);
    }
  }

  async function loadHealth() {
    healthLoading = true;
    try {
      healthData = await getHealthCameras();
    } catch (e) {
      console.warn('Failed to load health cameras:', e);
    } finally {
      healthLoading = false;
    }
  }

  let currentPage = $derived(Math.floor(page * pageSize / pageSize) + 1);
  let totalPages = $derived(Math.ceil(total / pageSize));

  function handlePageChange(newPage: number) {
    page = newPage - 1;
    window.scrollTo(0, 0);
  }

  // Reset page when filters change
  let prevCameraFilter = $state('');
  let prevEventTypeFilter = $state('');
  $effect(() => {
    if (cameraFilter !== prevCameraFilter || eventTypeFilter !== prevEventTypeFilter) {
      prevCameraFilter = cameraFilter;
      prevEventTypeFilter = eventTypeFilter;
      page = 0;
    }
  });

  // Auto-refresh + initial load
  let refreshTimer: ReturnType<typeof setInterval> | null = null;

  $effect(() => {
    const _ = [cameraFilter, eventTypeFilter, page];
    loadEvents();
    if (refreshTimer) clearInterval(refreshTimer);
    refreshTimer = setInterval(loadEvents, 30000);
    return () => {
      if (refreshTimer) clearInterval(refreshTimer);
    };
  });

  let healthTimer: ReturnType<typeof setInterval> | null = null;

  $effect(() => {
    loadHealth();
    healthTimer = setInterval(loadHealth, 30000);
    return () => {
      if (healthTimer) clearInterval(healthTimer);
    };
  });

  onMount(() => {
    loadCameras();
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">

    <!-- Health Score Cards -->
    <div class="mb-6">
      <h3 class="text-sm font-semibold th-text-secondary uppercase tracking-wider mb-3">{t('health.score')}</h3>
      {#if healthLoading && healthEntries.length === 0}
        <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-3">
          {#each Array(4) as _}
            <div class="card p-4 animate-pulse">
              <div class="h-4 w-20 th-bg-tertiary rounded mb-3"></div>
              <div class="h-8 w-16 th-bg-tertiary rounded mx-auto mb-2"></div>
              <div class="h-3 w-12 th-bg-tertiary rounded mx-auto"></div>
            </div>
          {/each}
        </div>
      {:else if healthEntries.length > 0}
        <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-3">
          {#each healthEntries as [id, detail] (id)}
            <div
              class="card p-4 border transition-all duration-200 hover:scale-[1.02] cursor-pointer"
              style="border-color: {scoreBorderColor(detail.score)}"
              role="button"
              tabindex="0"
              aria-expanded={expandedCamera === id}
              onclick={() => expandedCamera = expandedCamera === id ? null : id}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); expandedCamera = expandedCamera === id ? null : id; } }}
            >
              <div class="flex items-center justify-between mb-2">
                <span class="text-sm font-medium th-text-primary truncate" title={getCameraName(id)}>
                  {getCameraName(id)}
                </span>
                <span
                  class="inline-block w-2 h-2 rounded-full flex-shrink-0"
                  style="background-color: {statusDotColor(detail.latest_status)}"
                  title={statusLabel(detail.latest_status)}
                ></span>
              </div>
              <div class="text-center my-1">
                <span
                  class="text-2xl font-bold tabular-nums"
                  style="color: {scoreColor(detail.score)}"
                >
                  {detail.score}
                </span>
              </div>
              <div class="text-center">
                <span
                  class="text-[10px] font-medium px-2 py-0.5 rounded-full"
                  style="background: {scoreBgColor(detail.score)}; color: {scoreColor(detail.score)}"
                >
                  {scoreLabel(detail.score)}
                </span>
              </div>
            </div>
            {#if expandedCamera === id && detail.score_factors && Object.keys(detail.score_factors).length > 0}
              <div class="card factor-breakdown mt-1 p-3 border" style="border-color: {scoreBorderColor(detail.score)}">
                {#each Object.entries(detail.score_factors) as [factor, impact]}
                  <div class="factor-item" style="color: {impact < 0 ? 'var(--color-danger)' : 'var(--color-success)'}">
                    <span class="font-medium">{factor}</span>: {impact < 0 ? '' : '+'}{impact}
                  </div>
                {/each}
              </div>
            {/if}
          {/each}
        </div>
      {:else}
        <div class="card p-6 text-center th-text-secondary">
          <Activity size={24} class="mx-auto mb-2 opacity-50" />
          <span class="text-sm">{t('health.noCameras')}</span>
        </div>
      {/if}
    </div>

    <!-- Filters -->
    <div class="card p-5 mb-6 border th-border space-y-3">
      <div class="flex flex-wrap gap-3 items-end">
        <div class="flex-1 min-w-[160px]">
          <label for="camera-filter" class="input-label">{t('health.filter.camera')}</label>
          <select id="camera-filter" class="input" bind:value={cameraFilter}>
            <option value="">{t('health.filter.all')}</option>
            {#each cameras as camera}
              <option value={camera.id}>{camera.name}</option>
            {/each}
          </select>
        </div>
        <div class="flex-1 min-w-[160px]">
          <label for="event-type-filter" class="input-label">{t('health.filter.eventType')}</label>
          <select id="event-type-filter" class="input" bind:value={eventTypeFilter}>
            {#each eventTypes as et (et.value)}
              <option value={et.value}>{et.label()}</option>
            {/each}
          </select>
        </div>
      </div>
    </div>

    <!-- Error -->
    {#if error}
      <div class="card border th-border-danger p-8 text-center">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadEvents} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    <!-- Events table -->
    <div class="card border th-border">
      {#if loading && events.length === 0}
        <div class="p-6 space-y-4">
          <div class="flex justify-between items-center">
            <div class="h-8 w-48 th-bg-tertiary rounded animate-pulse"></div>
          </div>
          <div class="space-y-3">
            {#each Array(5) as _}
              <div class="flex gap-4 items-center">
                <div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-40 th-bg-tertiary rounded animate-pulse ml-auto"></div>
              </div>
            {/each}
          </div>
        </div>
      {:else if events.length === 0}
        <div class="p-12 text-center">
          <div class="flex justify-center mb-4 th-text-muted">
            <Activity size={48} />
          </div>
          <h3 class="text-lg font-medium th-text-primary mb-2">{t('health.noEvents')}</h3>
        </div>
      {:else}
        <div class="table-container th-border">
          <table class="table">
            <thead>
              <tr>
                <th class="min-w-[120px] sm:min-w-[140px]">{t('health.time')}</th>
                <th class="min-w-[100px]">{t('health.camera')}</th>
                <th class="min-w-[120px]">{t('health.eventType')}</th>
                <th class="min-w-[80px]">{t('health.status')}</th>
                <th>{t('health.message')}</th>
              </tr>
            </thead>
            <tbody>
              {#each events as event (event.id)}
                <tr class="transition-all duration-200 hover:th-bg-hover">
                  <td class="whitespace-nowrap">{formatDate(event.created_at)}</td>
                  <td>
                    <span class="font-medium th-text-primary">{getCameraName(event.camera_id)}</span>
                  </td>
                  <td>
                    <span class="badge badge-neutral text-xs">{eventTypeLabel(event.event_type)}</span>
                  </td>
                  <td>
                    <span class={statusBadgeClass(event.status)}>{statusLabel(event.status)}</span>
                  </td>
                  <td class="th-text-secondary text-sm">{event.message || '—'}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        {#if totalPages > 1}
          <div class="px-4 py-2 border-t th-border">
            <span class="text-sm th-text-muted">
              {t('recordings.showing', {
                start: String(page * pageSize + 1),
                end: String(Math.min(page * pageSize + events.length, total)),
                total: String(total)
              })}
            </span>
          </div>
          <Pagination
            {currentPage}
            {totalPages}
            onPageChange={handlePageChange}
          />
        {/if}

        {#if loading && events.length > 0}
          <div class="px-4 py-2 th-bg-secondary border-t th-border text-center">
            <span class="text-sm th-text-muted">{t('recordings.refreshing')}</span>
          </div>
        {/if}
      {/if}
    </div>
  </main>
</div>

<style>
  .factor-breakdown {
    background: var(--bg-secondary, rgba(0, 0, 0, 0.15));
    font-size: 11px;
    line-height: 1.6;
    border-radius: 6px;
  }

  .factor-item + .factor-item {
    margin-top: 2px;
  }
</style>
