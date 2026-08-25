<script lang="ts">
  import { onMount } from 'svelte';
  import { listAIEvents, getAIEventStats } from '$lib/api/ai-events';
  import type { AIEvent, AIEventStats } from '$lib/api/ai-events';
  import { listCameras, getRecordingsTimeline } from '$lib/api';
  import type { Camera } from '$lib/api';
  import { findSegmentAt } from '$lib/timeline-utils';
  import { getMiBeeVisionConnected, getMiBeeVisionLoaded, refreshMiBeeVisionStatus } from '$lib/mibeevision-status.svelte';
  import { t } from '$lib/i18n';
  import { formatDate, parseServerDate } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { classLabel, eventTypeLabel, severityLabel, zoneLabel } from '$lib/ai-labels';
  import { AlertCircle, Brain, ChevronDown, Play, Settings } from 'lucide-svelte';
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

  // 事件类型下拉框:label 与列表显示保持一致(中文),value 仍是后端用的英文 key。
  const eventTypes = [
    { value: '', label: t('aiEvents.allTypes') },
    { value: 'zone_intrusion', label: eventTypeLabel('zone_intrusion') },
    { value: 'line_crossing', label: eventTypeLabel('line_crossing') },
    { value: 'loitering', label: eventTypeLabel('loitering') },
    { value: 'object_detected', label: eventTypeLabel('object_detected') },
    { value: 'custom', label: eventTypeLabel('custom') },
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

  // Jump-to-recording: the event list doubles as an index into the recordings.
  // An event knows its absolute time (frame_timestamp || created_at) and,
  // optionally, which recording it belongs to. We deep-link as
  // #/recordings/{recording_id}?at=<epochMs>; the detail page resolves `at`
  // against the recording's started_at to seek the player.
  //
  // The event's recording_id can be stale — the AI backend may have written it
  // against a pre-merge 30s fragment whose id no longer exists in the
  // recordings table (the NVR assigns a new id when merging fragments). So
  // instead of trusting recording_id blindly, we resolve the event's timestamp
  // against the camera's day timeline (the same source the recordings page
  // uses) and jump to whichever real segment covers that moment. This matches
  // how the DayTimeline marker click resolves a target and guarantees the user
  // never lands on a 404 when a recording actually exists at that time.
  let jumping = $state(false);

  function eventEpochMs(evt: AIEvent): number {
    // created_at/frame_timestamp may arrive zoneless (UTC per server clock);
    // parseServerDate pins them to UTC — plain Date.parse reads them as local
    // time and shifts the jump target by the browser's UTC offset.
    const ts = evt.frame_timestamp || evt.created_at;
    return parseServerDate(ts).getTime();
  }

  function canJump(evt: AIEvent): boolean {
    // A valid timestamp is all we need — the camera+time resolution finds the
    // real recording. camera_id is required to scope the timeline lookup.
    return Number.isFinite(eventEpochMs(evt)) && !!evt.camera_id;
  }

  // Resolve an event's moment to a real {recordingId, offset} via the day
  // timeline. Returns null if no recording covers (or snaps near) the event.
  async function resolveTarget(evt: AIEvent): Promise<{ recordingId: string; at: number } | null> {
    const at = eventEpochMs(evt);
    const d = new Date(at);
    const y = d.getFullYear();
    const m = d.getMonth();
    const dd = d.getDate();
    const startISO = new Date(y, m, dd, 0, 0, 0).toISOString();
    const endISO = new Date(y, m, dd, 23, 59, 59).toISOString();
    const resp = await getRecordingsTimeline({
      camera_id: evt.camera_id,
      start: startISO,
      end: endISO,
    });
    const dayStartMs = new Date(y, m, dd).getTime();
    const segs = (resp.segments || [])
      .map((r) => ({
        id: r.id,
        startSec: (Date.parse(r.started_at) - dayStartMs) / 1000,
        endSec: ((r.ended_at ? Date.parse(r.ended_at) : Date.parse(r.started_at) + r.duration * 1000) - dayStartMs) / 1000,
      }))
      .filter((s) => Number.isFinite(s.startSec) && Number.isFinite(s.endSec));
    const eventSec = (at - dayStartMs) / 1000;
    const hit = findSegmentAt(segs, eventSec);
    if (!hit.seg) return null;
    return { recordingId: hit.seg.id, at };
  }

  async function jumpToRecording(evt: AIEvent) {
    if (!canJump(evt) || jumping) return;
    jumping = true;
    try {
      const target = await resolveTarget(evt);
      if (!target) {
        showToast(t('aiEvents.noRecordingAtTime'), 'warning');
        return;
      }
      window.location.hash = `#/recordings/${target.recordingId}?at=${target.at}`;
    } catch {
      showToast(t('aiEvents.jumpFailed'), 'error');
    } finally {
      jumping = false;
    }
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
          <div class="flex items-stretch hover:th-bg-hover transition-colors">
            <!-- Expand/collapse the detail panel (existing behavior) -->
            <button
              class="flex-1 flex items-center gap-3 p-3 text-left"
              onclick={() => expandedEvent = expandedEvent === evt.id ? null : evt.id}
              aria-expanded={expandedEvent === evt.id}
            >
              <!-- Severity badge -->
              <span class="px-2 py-0.5 rounded text-xs font-medium border {severityColors[evt.severity] || severityColors.info}">
                {severityLabel(evt.severity)}
              </span>
              <!-- Event info -->
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2">
                  <span class="font-medium th-text-primary text-sm">{eventTypeLabel(evt.event_type)}</span>
                  {#if evt.class_name}
                    <span class="text-xs th-text-muted">· {classLabel(evt.class_name)}</span>
                  {/if}
                  {#if evt.zone_name}
                    <span class="text-xs th-text-muted">· {zoneLabel(evt.zone_name)}</span>
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
            <!-- Jump-to-recording action. The row also works as an index into
                 the recordings: clicking play opens the recording at this
                 event's timestamp. Disabled (hidden) when no recording_id. -->
            {#if canJump(evt)}
              <button
                class="ai-jump-btn"
                title={t('aiEvents.jumpToRecording')}
                aria-label={t('aiEvents.jumpToRecording')}
                onclick={() => jumpToRecording(evt)}
                disabled={jumping}
              >
                <Play size={16} />
              </button>
            {/if}
          </div>

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
                <div class="flex items-center gap-1 flex-wrap">
                  <span class="th-text-muted">Recording:</span>
                  <span class="th-text-tertiary" title={t('aiEvents.recordingIdHint')}>{evt.recording_id}</span>
                  {#if canJump(evt)}
                    <button
                      class="ai-rec-link"
                      title={t('aiEvents.jumpToRecording')}
                      onclick={() => jumpToRecording(evt)}
                      disabled={jumping}
                    >
                      {t('aiEvents.jumpToRecording')}
                      <Play size={11} />
                    </button>
                  {/if}
                </div>
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
              <div class="th-text-tertiary pt-1">{t('aiEvents.clickToJump')}</div>
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

<style>
  /* Jump-to-recording button on each event row. Green accent signals "play",
     matching the person-marker color used on the recordings timeline so the two
     surfaces read as the same kind of action. */
  .ai-jump-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 2.5rem;
    flex-shrink: 0;
    border: 0;
    border-left: 1px solid var(--border, rgba(255, 255, 255, 0.08));
    background: transparent;
    color: var(--text-muted, inherit);
    cursor: pointer;
    transition: background 0.12s, color 0.12s;
  }
  .ai-jump-btn:hover {
    background: rgba(34, 197, 94, 0.12);
    color: #22c55e;
  }
  .ai-rec-link {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    border: 0;
    background: transparent;
    color: #22c55e;
    font: inherit;
    font-size: inherit;
    padding: 0;
    cursor: pointer;
    text-decoration: none;
  }
  .ai-rec-link:hover {
    text-decoration: underline;
  }
  .ai-rec-link:disabled {
    opacity: 0.5;
    cursor: progress;
  }
</style>
