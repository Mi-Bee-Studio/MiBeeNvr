<script lang="ts">
  import { onMount } from 'svelte';
  import { listAIEvents, getAIEventStats } from '$lib/api/ai-events';
  import type { AIEvent, AIEventStats } from '$lib/api/ai-events';
  import { listCameras } from '$lib/api';
  import type { Camera } from '$lib/api';
  import { getMiBeeVisionConnected, getMiBeeVisionLoaded, refreshMiBeeVisionStatus } from '$lib/mibeevision-status.svelte';
  import { t } from '$lib/i18n';
  import { formatDate } from '$lib/format';
  import { AlertCircle, Brain, ChevronDown, Settings } from 'lucide-svelte';
  import Pagination from '../components/Pagination.svelte';

  let events = $state<AIEvent[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state('');
  let cameraFilter = $state('');
  let eventTypeFilter = $state('');
  let cameras = $state<Camera[]>([]);
  let page = $state(0);
  let expandedEvent = $state<number | null>(null);
  let stats = $state<AIEventStats[]>([]);
  const pageSize = 20;

  const eventTypes = [
    { value: '', label: 'All Types' },
    { value: 'zone_intrusion', label: 'Zone Intrusion' },
    { value: 'line_crossing', label: 'Line Crossing' },
    { value: 'loitering', label: 'Loitering' },
    { value: 'object_detected', label: 'Object Detected' },
    { value: 'custom', label: 'Custom' },
  ];

  const severityColors: Record<string, string> = {
    info: 'text-blue-400 bg-blue-500/10 border-blue-500/30',
    warning: 'text-yellow-400 bg-yellow-500/10 border-yellow-500/30',
    critical: 'text-red-400 bg-red-500/10 border-red-500/30',
  };

  function cameraName(id: string): string {
    const cam = cameras.find(c => c.id === id);
    return cam?.name || id;
  }

  function parseBBox(bbox?: string): [number, number, number, number] | null {
    if (!bbox) return null;
    try {
      const arr = JSON.parse(bbox);
      if (Array.isArray(arr) && arr.length === 4) return arr as [number, number, number, number];
    } catch { /* ignore */ }
    return null;
  }

  async function loadData() {
    loading = true;
    error = '';
    try {
      const resp = await listAIEvents({
        camera_id: cameraFilter || undefined,
        event_type: eventTypeFilter || undefined,
        limit: pageSize,
        offset: page * pageSize,
      });
      events = resp.events || [];
      total = resp.total;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  async function loadStats() {
    if (!cameraFilter) {
      stats = [];
      return;
    }
    try {
      const resp = await getAIEventStats(cameraFilter, '24h');
      stats = resp.stats || [];
    } catch {
      stats = [];
    }
  }

  function onFilterChange() {
    page = 0;
    loadData();
    loadStats();
  }

  const miBeeVisionConnected = $derived(getMiBeeVisionConnected());
  const miBeeVisionLoaded = $derived(getMiBeeVisionLoaded());

  onMount(async () => {
    await refreshMiBeeVisionStatus();
    if (!getMiBeeVisionConnected()) return;
    try {
      cameras = await listCameras();
    } catch { /* ignore */ }
    await loadData();
  });
</script>

<div class="p-4 md:p-6 max-w-6xl mx-auto">
  <!-- Not connected guard -->
  {#if miBeeVisionLoaded && !miBeeVisionConnected}
    <div class="flex flex-col items-center justify-center py-20 text-center">
      <Brain size={48} class="text-gray-400 mb-4 opacity-50" />
      <h2 class="text-lg font-semibold th-text-primary mb-2">{t('aiEvents.notConnectedTitle')}</h2>
      <p class="text-sm th-text-muted mb-6 max-w-md">{t('aiEvents.notConnectedDesc')}</p>
      <a href="#/settings" class="btn btn-primary flex items-center gap-2">
        <Settings size={16} />
        {t('aiEvents.goToSettings')}
      </a>
    </div>
  {:else if !miBeeVisionLoaded}
    <!-- Loading -->
    <div class="flex items-center justify-center py-20">
      <span class="spinner"></span>
    </div>
  {:else}
  <!-- Header -->
  <div class="flex items-center gap-3 mb-6">
    <Brain size={28} class="text-purple-400" />
    <div>
      <h1 class="text-xl font-bold th-text-primary">{t('aiEvents.title')}</h1>
      <p class="text-sm th-text-muted">{t('aiEvents.subtitle')}</p>
    </div>
  </div>

  <!-- Filters -->
  <div class="flex flex-wrap gap-3 mb-4">
    <select class="input" bind:value={cameraFilter} onchange={onFilterChange}>
      <option value="">{t('aiEvents.allCameras')}</option>
      {#each cameras as cam}
        <option value={cam.id}>{cam.name || cam.id}</option>
      {/each}
    </select>
    <select class="input" bind:value={eventTypeFilter} onchange={onFilterChange}>
      {#each eventTypes as et}
        <option value={et.value}>{et.label}</option>
      {/each}
    </select>
  </div>

  <!-- Stats summary (when camera selected) -->
  {#if stats.length > 0}
    <div class="flex flex-wrap gap-2 mb-4">
      {#each stats as s}
        <div class="px-3 py-1.5 rounded-md th-bg-surface th-border text-sm flex items-center gap-2">
          <span class="th-text-muted">{s.event_type}</span>
          <span class="font-bold th-text-primary">{s.count}</span>
        </div>
      {/each}
    </div>
  {/if}

  <!-- Error -->
  {#if error}
    <div class="flex items-center gap-2 p-3 rounded-md bg-red-500/10 border border-red-500/30 text-red-400 text-sm mb-4">
      <AlertCircle size={16} />
      {error}
    </div>
  {/if}

  <!-- Loading -->
  {#if loading}
    <div class="flex items-center justify-center py-12">
      <div class="w-6 h-6 border-2 border-purple-500/30 border-t-purple-500 rounded-full animate-spin"></div>
    </div>
  {:else if events.length === 0}
    <div class="text-center py-12 th-text-muted">
      <Brain size={40} class="mx-auto mb-3 opacity-30" />
      <p>{t('aiEvents.noEvents')}</p>
    </div>
  {:else}
    <!-- Event list -->
    <div class="space-y-2">
      {#each events as evt (evt.id)}
        <div class="th-bg-surface th-border rounded-lg overflow-hidden">
          <button
            class="w-full flex items-center gap-3 p-3 hover:th-bg-hover transition-colors text-left"
            onclick={() => expandedEvent = expandedEvent === evt.id ? null : evt.id}
          >
            <!-- Severity badge -->
            <span class="px-2 py-0.5 rounded text-xs font-medium border {severityColors[evt.severity] || severityColors.info}">
              {evt.severity}
            </span>
            <!-- Event info -->
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <span class="font-medium th-text-primary text-sm">{evt.event_type}</span>
                {#if evt.class_name}
                  <span class="text-xs th-text-muted">· {evt.class_name}</span>
                {/if}
                {#if evt.zone_name}
                  <span class="text-xs th-text-muted">· {evt.zone_name}</span>
                {/if}
              </div>
              <div class="text-xs th-text-muted mt-0.5">
                {cameraName(evt.camera_id)} · {formatDate(evt.created_at)}
              </div>
            </div>
            <!-- Confidence -->
            {#if evt.confidence > 0}
              <div class="text-xs th-text-muted">
                {(evt.confidence * 100).toFixed(0)}%
              </div>
            {/if}
            <ChevronDown
              size={16}
              class="th-text-muted transition-transform {expandedEvent === evt.id ? 'rotate-180' : ''}"
            />
          </button>

          <!-- Expanded detail -->
          {#if expandedEvent === evt.id}
            <div class="px-3 pb-3 pt-1 border-t th-border space-y-1 text-xs th-text-secondary">
              {#if evt.frame_timestamp}
                <div><span class="th-text-muted">Frame time:</span> {evt.frame_timestamp}</div>
              {/if}
              {#if evt.frame_idx}
                <div><span class="th-text-muted">Frame index:</span> {evt.frame_idx}</div>
              {/if}
              {#if evt.recording_id}
                <div><span class="th-text-muted">Recording:</span> {evt.recording_id}</div>
              {/if}
              {#if parseBBox(evt.bbox)}
                <div><span class="th-text-muted">Bounding box:</span> {parseBBox(evt.bbox)!.map(v => v.toFixed(3)).join(', ')}</div>
              {/if}
              {#if evt.snapshot_path}
                <div><span class="th-text-muted">Snapshot:</span> {evt.snapshot_path}</div>
              {/if}
              {#if evt.metadata && evt.metadata !== 'null'}
                <div><span class="th-text-muted">Metadata:</span> {evt.metadata}</div>
              {/if}
              <div><span class="th-text-muted">Event ID:</span> {evt.id}</div>
            </div>
          {/if}
        </div>
      {/each}
    </div>

    <!-- Pagination -->
    {#if total > pageSize}
      <div class="mt-4 flex justify-center">
        <Pagination
          total={total}
          limit={pageSize}
          offset={page * pageSize}
          onPageChange={(newPage: number) => { page = newPage; loadData(); }}
        />
      </div>
    {/if}
  {/if} <!-- /loading -->
  {/if} <!-- /miBeeVision guard -->
</div>
