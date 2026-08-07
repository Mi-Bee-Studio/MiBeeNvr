<script lang="ts">
  // RecordingDetail.svelte — Host for the recording detail page (#136 refactor).
  //
  // Owns: loading/error/delete states, recording orchestration (loadRecording,
  // next-segment navigation, cross-segment timeline seek, deep-link ?t=/?at=),
  // keyboard dispatch, and the page chrome (loading/error/delete-confirm).
  // Delegates to three child components:
  //   - MergePanel:    merge state machine (SSE/poll/cancel) + onprogress mirror
  //   - PlaybackPanel: all playback modes (video/timelapse/mjpeg/avi/unsupported)
  //   - MetaEditor:    info card + actions row + transcode status
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import type { Recording } from '$lib/api';
  import {
    getRecording,
    deleteRecording,
    probeMergedRecordingCodec,
    clearMergedCodecCache,
  } from '$lib/api';
  import { AlertTriangle, RefreshCw } from 'lucide-svelte';

  import MergePanel from './recordings/MergePanel.svelte';
  import PlaybackPanel from './recordings/PlaybackPanel.svelte';
  import MetaEditor from './recordings/MetaEditor.svelte';

  let { recordingId = '' } = $props();
  let currentId = $state('');
  let recording = $state<Recording | null>(null);
  let loading = $state(true);
  let error = $state('');
  let loadErrorType = $state<'generic' | 'not_found'>('generic');
  let deleteConfirm = $state(false);
  let isTransitioning = $state(false);

  // Deep-link / cross-segment seek offset handed to PlaybackPanel.
  let pendingTimelineSeekOffset = $state<number | null>(null);
  // ?at=<epochMs> absolute timestamp (from the AI events page) — resolved to an
  // offset once the recording's started_at is known.
  let pendingTimelineSeekAtMs = $state<number | null>(null);

  // Merge state mirror — updated by MergePanel via onprogress, read by
  // PlaybackPanel + MetaEditor for badges / inline controls.
  let mergeState = $state({ inProgress: false, pct: 0, eta: '', error: '' });
  let mergePanel: MergePanel | undefined = $state();
  let playbackPanel: PlaybackPanel | undefined = $state();

  // Whether the recording offers a merge action (timelapse/mjpeg without an
  // already-merged output). Drives merge button visibility in the children.
  let canMerge = $derived.by(() => {
    if (!recording) return false;
    if (recording.format !== 'timelapse' && recording.format !== 'mjpeg') return false;
    return recording.merge_status !== 'merged' && recording.merge_status !== 'daily_merged';
  });

  async function loadRecording() {
    loading = true;
    error = '';
    try {
      recording = await getRecording(currentId);
      if (recording) {
        // PlaybackPanel handles player init in its $effect on `recording`.
        // The codec probe that picks <video> vs cycler for timelapse/mjpeg is
        // done lazily by PlaybackPanel's mode derivation.
      }
    } catch (e) {
      const errMsg = e instanceof Error ? e.message : '';
      if (errMsg.includes('404') || errMsg.includes('not found') || errMsg.includes('RECORDING_NOT_FOUND')) {
        loadErrorType = 'not_found';
        error = t('errors.RECORDING_NOT_FOUND');
      } else {
        loadErrorType = 'generic';
        error = e instanceof Error ? e.message : t('common.failedLoadRecording');
      }
      recording = null;
    } finally {
      loading = false;
    }
  }

  async function navigateToNext() {
    if (!recording) return;
    // Delegate to PlaybackPanel's next-segment logic via its exported action;
    // but navigation (currentId + reload) is the host's job. Reuse the same
    // listRecordings-based next-finder that PlaybackPanel uses internally by
    // having it call ongotonext → which lands here.
    const { listRecordings } = await import('$lib/api');
    try {
      const resp = await listRecordings({
        camera_id: recording.camera_id,
        format: recording.format,
        start: recording.ended_at ? new Date(recording.ended_at).toISOString() : undefined,
        sort_by: 'started_at',
        order: 'asc',
        limit: 5,
        offset: 0,
      });
      const next = resp.recordings.find(r => r.merge_status !== 'daily_merged');
      if (next) {
        isTransitioning = true;
        window.location.hash = `#/recordings/${next.id}`;
      }
    } catch { /* ignore */ }
  }

  // <video> ended → try the next segment.
  function handleEnded() {
    void navigateToNext();
  }

  // Cross-segment timeline seek: update the hash; the {#key} block in App.svelte
  // recreates this component with the new recordingId → $effect → loadRecording.
  // The offset is preserved across the remount via pendingTimelineSeekOffset.
  function handleTimelineSeek(recordingId: string, offsetSeconds: number) {
    pendingTimelineSeekOffset = offsetSeconds;
    isTransitioning = true;
    window.location.hash = `#/recordings/${recordingId}`;
  }

  // Resolve ?at= absolute timestamp → offset once recording.started_at is known.
  $effect(() => {
    if (pendingTimelineSeekAtMs == null) return;
    if (!recording || !recording.started_at) return;
    const startedAtMs = Date.parse(recording.started_at);
    if (!Number.isFinite(startedAtMs)) return;
    const offsetSec = Math.max(0, Math.floor((pendingTimelineSeekAtMs - startedAtMs) / 1000));
    pendingTimelineSeekAtMs = null;
    if (pendingTimelineSeekOffset == null) pendingTimelineSeekOffset = offsetSec;
  });

  // Reactively reload when recordingId changes (SPA navigation + cross-segment
  // seeks that update window.location.hash).
  $effect(() => {
    const id = recordingId;
    if (!id) return;
    if (currentId === id && recording) return;
    currentId = id;
    loading = true;
    error = '';
    loadRecording().finally(() => { isTransitioning = false; });
  });

  async function confirmDelete() {
    if (!recording) return;
    try {
      await deleteRecording(recording.id);
      window.location.hash = '#/recordings';
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedDeleteRecording');
      deleteConfirm = false;
    }
  }

  function goBack() { window.location.hash = '#/recordings'; }

  // Merge completion → reload recording (re-probe codec, refresh merge_status).
  function onMergeCompleted() {
    if (recording) clearMergedCodecCache(recording.camera_id);
    void loadRecording();
  }

  // --- Keyboard dispatcher (host owns it; forwards to PlaybackPanel + MergePanel) ---
  function handleKeydown(e: KeyboardEvent) {
    const tag = (e.target as HTMLElement).tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
    switch (e.key) {
      case ' ':
        e.preventDefault();
        playbackPanel?.handleKeyAction('space');
        break;
      case 'ArrowLeft':
        e.preventDefault();
        playbackPanel?.handleKeyAction('arrowleft');
        break;
      case 'ArrowRight':
        e.preventDefault();
        playbackPanel?.handleKeyAction('arrowright');
        break;
      case 'Escape':
        if (document.fullscreenElement) { document.exitFullscreen(); break; }
        goBack();
        break;
      case 'f': case 'F':
        e.preventDefault();
        playbackPanel?.handleKeyAction('f');
        break;
      case 'l': case 'L':
        e.preventDefault();
        playbackPanel?.handleKeyAction('l');
        break;
      case 'Home':
        e.preventDefault();
        playbackPanel?.handleKeyAction('home');
        break;
      case 'c': case 'C':
        mergePanel?.handleKeyAction('c');
        break;
    }
  }

  onMount(() => {
    window.addEventListener('keydown', handleKeydown);
    // Deep-link seek offsets (?t= / ?at=) from the DayTimeline / AI events page.
    try {
      const params = new URLSearchParams(window.location.hash.split('?')[1] || '');
      const tParam = params.get('t');
      if (tParam !== null) {
        const off = Number(tParam);
        if (Number.isFinite(off) && off >= 0) pendingTimelineSeekOffset = off;
      } else {
        const atParam = params.get('at');
        if (atParam !== null) {
          const atMs = Number(atParam);
          if (Number.isFinite(atMs)) pendingTimelineSeekAtMs = atMs;
        }
      }
    } catch { /* ignore malformed query */ }

    return () => {
      window.removeEventListener('keydown', handleKeydown);
      // MergePanel owns its own teardown (onDestroy); no merge cleanup needed here.
    };
  });
</script>

<!-- MergePanel is a headless controller (no visible UI except its cancel dialog).
     It tracks merge progress and pushes state updates via onprogress. -->
<MergePanel
  bind:this={mergePanel}
  {recording}
  {currentId}
  onmergecompleted={onMergeCompleted}
  onprogress={(info) => (mergeState = info)}
/>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="th-color-danger mb-4 flex justify-center"><AlertTriangle size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <div class="flex justify-center gap-3">
          {#if loadErrorType === 'generic'}
            <button onclick={loadRecording} class="btn btn-primary btn-sm flex items-center gap-1">
              <RefreshCw size={14} />
              {t('common.retry')}
            </button>
          {/if}
          <button onclick={goBack} class="btn btn-secondary btn-sm">
            {t('detail.goBack')}
          </button>
        </div>
      </div>
    {:else if recording}
      <div class="space-y-6">
        <!-- Playback section -->
        <div class="card border th-border overflow-hidden">
          <PlaybackPanel
            bind:this={playbackPanel}
            {recording}
            {currentId}
            {isTransitioning}
            bind:pendingTimelineSeekOffset
            {mergeState}
            {canMerge}
            onstartmerge={() => mergePanel?.startMerge()}
            oncancelmerge={() => mergePanel?.requestCancel()}
            onended={handleEnded}
            ontimelineseek={handleTimelineSeek}
            ongotonext={navigateToNext}
            oncrosssegment={(r) => (recording = r)}
          />
        </div>

        <!-- Recording info + actions + transcode status -->
        <MetaEditor
          {recording}
          {currentId}
          {mergeState}
          {canMerge}
          onstartmerge={() => mergePanel?.startMerge()}
          oncancelmerge={() => mergePanel?.requestCancel()}
          ondelete={() => (deleteConfirm = true)}
        />
      </div>
    {/if}
  </main>

  <!-- Delete confirmation modal -->
  {#if deleteConfirm && recording}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('detail.deleteTitle')}</h3>
        <p class="th-text-secondary mb-6">
          {t('detail.deleteMessage', { camera_id: recording.camera_id })}
        </p>
        <div class="flex gap-3 justify-end">
          <button onclick={() => (deleteConfirm = false)} class="btn btn-secondary">
            {t('detail.cancel')}
          </button>
          <button onclick={confirmDelete} class="btn btn-danger">
            {t('detail.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>
