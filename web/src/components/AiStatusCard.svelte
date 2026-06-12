<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { getAIStatus, getAIDetections } from '$lib/api';
  import type { AIStatus, DetectionEvent } from '$lib/api';
  import { BrainCircuit, ChevronDown, ChevronUp, Loader2 } from 'lucide-svelte';

  let aiStatus = $state<AIStatus | null>(null);
  let loading = $state(true);
  let error = $state('');
  let expandedCamera = $state<string | null>(null);
  let detectionsLoading = $state(false);
  let cameraDetections = $state<DetectionEvent[]>([]);


  // Camera entries sorted: running first, then by name
  let cameraEntries = $derived.by(() => {
    if (!aiStatus || !aiStatus.cameras) return [];
    return Object.entries(aiStatus.cameras)
      .map(([id, status]) => ({ id, status }))
      .sort((a, b) => {
        if (a.status.running !== b.status.running) return a.status.running ? -1 : 1;
        return a.id.localeCompare(b.id);
      });
  });

  let activeCameraCount = $derived(
    aiStatus ? Object.values(aiStatus.cameras).filter(c => c.running).length : 0
  );

  async function loadStatus() {
    try {
      const data = await getAIStatus();
      aiStatus = data;
      error = '';
    } catch (e) {
      error = String(e);
      console.error('Failed to load AI status:', e);
    } finally {
      loading = false;
    }
  }

  async function toggleCamera(cameraId: string) {
    if (expandedCamera === cameraId) {
      expandedCamera = null;
      return;
    }
    expandedCamera = cameraId;
    detectionsLoading = true;
    try {
      const result = await getAIDetections(cameraId, { limit: 10 });
      cameraDetections = result.detections;
    } catch (e) {
      console.error('Failed to load detections:', e);
      cameraDetections = [];
    } finally {
      detectionsLoading = false;
    }
  }

  function formatDetectionTime(ts: string): string {
    try {
      const d = new Date(ts);
      return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch {
      return ts;
    }
  }

  function confidenceColor(conf: number): string {
    if (conf >= 0.8) return 'th-color-success';
    if (conf >= 0.5) return 'th-color-info';
    return 'th-text-muted';
  }

  // Poll every 5 seconds
  let intervalId: number | undefined;

  function startPolling() {
    loadStatus();
    intervalId = window.setInterval(loadStatus, 5000);
  }

  startPolling();

  onDestroy(() => {
    if (intervalId !== undefined) clearInterval(intervalId);
  });
</script>

<div class="card p-4 border th-border mb-6">
  <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
    <BrainCircuit size={16} class="text-accent" />
    {t('ai.title')}
    {#if aiStatus?.enabled}
      <span class="ml-auto text-xs font-normal th-text-muted flex items-center gap-1">
        {t('ai.model', { name: aiStatus.model_name })}
      </span>
    {/if}
  </h3>

  {#if loading && !aiStatus}
    <div class="flex items-center justify-center py-3">
      <Loader2 size={16} class="th-text-secondary animate-spin" />
    </div>
  {:else if error && !aiStatus}
    <div class="flex items-center gap-2 text-sm th-text-muted py-2">
      <span class="text-xs th-color-danger">●</span>
      <span>{t('ai.loadError')}</span>
    </div>
  {:else if !aiStatus?.enabled}
    <!-- AI Globally Disabled -->
    <div class="flex items-center gap-2 py-2">
      <span class="w-2 h-2 rounded-full th-bg-danger flex-shrink-0"></span>
      <span class="text-sm th-text-muted">{t('ai.disabled')}</span>
    </div>
  {:else if cameraEntries.length === 0}
    <p class="text-sm th-text-muted">{t('ai.noCameras')}</p>
  {:else}
    <div class="space-y-1">
      {#each cameraEntries as entry (entry.id)}
        <button
          onclick={() => toggleCamera(entry.id)}
          class="w-full flex items-center gap-3 py-1.5 px-2 rounded-md hover:bg-[var(--bg-tertiary)] transition-colors text-left"
        >
          <!-- Status dot -->
          <span
            class="w-2 h-2 rounded-full flex-shrink-0"
            class:bg-emerald-500={entry.status.running}
            class:th-bg-danger={!entry.status.running}
          ></span>

          <!-- Camera name -->
          <span class="text-sm th-text-primary flex-1 truncate">{entry.id}</span>

          <!-- Status label -->
          <span class="text-xs th-text-secondary hidden sm:inline">
            {entry.status.running ? t('ai.statusRunning') : t('ai.statusStopped')}
          </span>

          <!-- FPS -->
          {#if entry.status.running}
            <span class="text-xs font-semibold tabular-nums th-text-tertiary min-w-[4rem] text-right">
              {t('ai.fps', { fps: entry.status.fps.toFixed(1) })}
            </span>
          {/if}

          <!-- Detection count badge -->
          {#if entry.status.detections > 0}
            <span class="inline-flex items-center justify-center min-w-[1.5rem] h-5 px-1.5 rounded-full text-xs font-semibold bg-accent-subtle text-accent">
              {entry.status.detections}
            </span>
          {/if}

          <!-- Expand indicator -->
          {#if expandedCamera === entry.id}
            <ChevronUp size={14} class="th-text-muted flex-shrink-0" />
          {:else}
            <ChevronDown size={14} class="th-text-muted flex-shrink-0" />
          {/if}
        </button>

        <!-- Expanded detections list -->
        {#if expandedCamera === entry.id}
          <div class="ml-2 pl-4 border-l th-border space-y-0.5 py-1">
            {#if detectionsLoading}
              <div class="flex items-center justify-center py-2">
                <Loader2 size={12} class="th-text-secondary animate-spin" />
              </div>
            {:else if cameraDetections.length === 0}
              <p class="text-xs th-text-muted py-1 px-2">{t('ai.noDetections')}</p>
            {:else}
              <p class="text-xs font-medium th-text-secondary px-2 pb-1">{t('ai.recentDetections')}</p>
              {#each cameraDetections as event}
                <div class="px-2 py-1 rounded hover:bg-[var(--bg-tertiary)] transition-colors">
                  <div class="flex items-center gap-2">
                    <span class="text-[10px] th-text-muted tabular-nums">{formatDetectionTime(event.timestamp)}</span>
                    <span class="text-[10px] th-text-muted">{event.source}</span>
                  </div>
                  <div class="flex flex-wrap gap-1 mt-0.5">
                    {#each event.detections as det}
                      <span
                        class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium"
                        class:bg-accent-subtle={det.confidence >= 0.5}
                        class:th-bg-tertiary={det.confidence < 0.5}
                      >
                        <span class={confidenceColor(det.confidence)}>
                          {det.class_name}
                        </span>
                        <span class="th-text-muted">{(det.confidence * 100).toFixed(0)}%</span>
                      </span>
                    {/each}
                  </div>
                </div>
              {/each}
            {/if}
          </div>
        {/if}
      {/each}
    </div>
  {/if}
</div>

<style>
  .bg-emerald-500 {
    background-color: #10b981;
  }
</style>
