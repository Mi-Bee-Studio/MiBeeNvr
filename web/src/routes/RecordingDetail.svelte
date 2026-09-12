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
  import { showToast } from '$lib/toast';
  import type { Recording, TimelapseMerge } from '$lib/api';
  import {
    getRecording,
    deleteRecording,
    probeMergedRecordingCodec,
    clearMergedCodecCache,
    listTimelapseMerges,
    getTimelapseMerge,
    deleteTimelapseMerge,
    getTimelapseMergeDownloadUrl,
    listRecordings,
    getCamera,
  } from '$lib/api';
  import { AlertTriangle, RefreshCw, Clapperboard, Hourglass, Download, Trash2, ChevronLeft, ChevronRight } from 'lucide-svelte';
  import { parseServerDate, formatFileSize } from '$lib/format';
  import { parseTimelineMap, wallToFileSec } from '$lib/timeline-map';

  import MergePanel from './recordings/MergePanel.svelte';
  import PlaybackPanel from './recordings/PlaybackPanel.svelte';
  import MetaEditor from './recordings/MetaEditor.svelte';
  import TimelapseMergePlayer from '$lib/components/TimelapseMergePlayer.svelte';

  // Unified viewer props: exactly one is set by the router. A recordingId
  // entry starts in recording mode; a mergeId entry (#/timelapse-merge/{id})
  // starts in merged-timelapse mode with the merge's anchor recording (first
  // row inside the window) loaded for the toggle — same page either way.
  let { recordingId = '', mergeId = '' } = $props();
  let currentId = $state('');
  let recording = $state<Recording | null>(null);
  let loading = $state(true);
  let error = $state('');
  let loadErrorType = $state<'generic' | 'not_found'>('generic');
  let deleteConfirm = $state(false);
  let isTransitioning = $state(false);
  // Guards against out-of-order in-place loads: only the latest loadRecording
  // call may write `recording` / clear the transitioning overlay.
  let loadToken = 0;
  // Last recordingId prop consumed by the props-effect (mount-time entry point).
  // In-place segment switches own `currentId` from then on; the prop only
  // changes again on a full route remount.
  let lastRoutedId = '';

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

  // ── Dual-mode playback (recording ↔ merged timelapse, no page flash) ──
  // When the recording's camera has a completed merge intersecting the
  // recording's local day, a segmented control offers in-place switching
  // between the normal recording playback and the merged timelapse. Both
  // players stay mounted (the inactive one hidden + paused), so toggling is
  // instant — no route navigation, no skeleton, no re-buffer.
  let dayMerge = $state<TimelapseMerge | null>(null);
  let tlMode = $state(false);
  let tlPlayer: TimelapseMergePlayer | undefined = $state();
  // Merge-side chrome (info card): camera display name + delete state.
  let cameraName = $state('');
  let mergeDeleteConfirm = $state(false);
  let mergeDeleting = $state(false);
  const mergeDownloadUrl = $derived(
    dayMerge?.status === 'completed' && dayMerge.output_path ? getTimelapseMergeDownloadUrl(dayMerge.id) : '',
  );

  // Entry via #/timelapse-merge/{id}: load the merge, then best-effort load
  // its anchor recording (first row inside the window) so the toggle + day
  // timeline + MetaEditor all work exactly like a recording entry. A merge
  // with no recordings left in its window (e.g. delete_recordings_after_merge)
  // renders merge-only: player + info card, no toggle.
  async function loadFromMerge(id: string) {
    loading = true;
    error = '';
    try {
      const m = await getTimelapseMerge(id);
      if (!m) {
        loadErrorType = 'generic';
        error = t('timelapseMerge.notFound');
        return;
      }
      dayMerge = m;
      tlMode = true;
      try {
        const cam = await getCamera(m.camera_id);
        cameraName = cam?.name || '';
      } catch {
        cameraName = '';
      }
      try {
        const resp = await listRecordings({
          camera_id: m.camera_id,
          start: m.window_start,
          end: m.window_end,
          order: 'asc',
          limit: 1,
        });
        const anchor = resp.recordings[0];
        if (anchor) {
          currentId = anchor.id;
          recording = anchor;
          // No syncDetailDateInURL here: rewriting to #/recordings/{id} would
          // lose the merge deep-link (a refresh would land in recording mode
          // instead of the merge the URL names). The URL migrates naturally
          // when the user seeks across segments from recording mode.
        }
      } catch {
        // Anchor is best-effort; merge-only view is fine.
      }
    } catch (e) {
      loadErrorType = 'generic';
      error = e instanceof Error ? e.message : t('common.error');
      recording = null;
      dayMerge = null;
    } finally {
      loading = false;
    }
  }

  async function handleMergeDelete() {
    if (!dayMerge) return;
    mergeDeleting = true;
    try {
      await deleteTimelapseMerge(dayMerge.id);
      showToast(t('timelapseMerge.deleted'), 'success');
      window.location.hash = goBackTarget();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    } finally {
      mergeDeleting = false;
      mergeDeleteConfirm = false;
    }
  }

  // Best-effort lookup of the day's newest completed merge for the camera.
  // The API bounds window_start only, so multi-day windows still covering the
  // day are client-filtered by window_end ≥ dayStart.
  let dayMergeToken = 0;
  async function loadDayMerge() {
    const token = ++dayMergeToken;
    if (!recording?.started_at) {
      dayMerge = null;
      return;
    }
    const day = new Date(recording.started_at);
    const dayStartMs = new Date(day.getFullYear(), day.getMonth(), day.getDate()).getTime();
    const dayEndISO = new Date(day.getFullYear(), day.getMonth(), day.getDate(), 23, 59, 59, 999).toISOString();
    try {
      const resp = await listTimelapseMerges({
        camera_id: recording.camera_id,
        end: dayEndISO,
        status: 'completed',
        limit: 30,
      });
      if (token !== dayMergeToken) return;
      dayMerge =
        resp.merges.find((m) => parseServerDate(m.window_end).getTime() >= dayStartMs - 1) ?? null;
    } catch {
      if (token === dayMergeToken) dayMerge = null;
    }
  }

  // A segment switch onto a day with no merge must fall back to recording mode.
  $effect(() => {
    if (tlMode && !dayMerge) tlMode = false;
  });

  function setTlMode(on: boolean) {
    if (on && !dayMerge) return;
    if (tlMode === on) return;
    tlMode = on;
    // Freeze the player going out of view (decode + audio keep running on a
    // merely-hidden element otherwise).
    if (on) {
      playbackPanel?.pausePlayback();
      tlPlayer?.resume();
    } else {
      tlPlayer?.pause();
    }
  }

  // Merge-URL entries start with tlMode already true — the player mounts
  // afterwards, so resume it once it binds (autoplay is off to keep the
  // hidden player silent on recording entries).
  $effect(() => {
    if (tlMode) tlPlayer?.resume();
  });

  // ── In-page day navigation ──
  // Jump straight to the first recording of the previous/next calendar day
  // for the same camera — an in-place segment switch (no remount, no flash),
  // so the user never has to bounce through the recordings list.
  const currentViewDay = $derived.by(() => {
    const base = recording?.started_at ?? dayMerge?.window_start;
    return base ? new Date(base).toLocaleDateString('en-CA') : '';
  });

  async function gotoDay(delta: number) {
    const camId = recording?.camera_id ?? dayMerge?.camera_id;
    const base = recording?.started_at ?? dayMerge?.window_start;
    if (!camId || !base) return;
    const b = new Date(base);
    const start = new Date(b.getFullYear(), b.getMonth(), b.getDate() + delta, 0, 0, 0);
    const end = new Date(b.getFullYear(), b.getMonth(), b.getDate() + delta, 23, 59, 59, 999);
    try {
      const resp = await listRecordings({
        camera_id: camId,
        start: start.toISOString(),
        end: end.toISOString(),
        order: 'asc',
        limit: 1,
      });
      const first = resp.recordings[0];
      if (first) {
        switchRecordingInPlace(first.id, 0);
        return;
      }
      showToast(t('detail.noRecordingsOnDay'), 'warning');
    } catch {
      showToast(t('detail.noRecordingsOnDay'), 'warning');
    }
  }

  // Whether the recording offers a merge action (timelapse/mjpeg without an
  // already-merged output). Drives merge button visibility in the children.
  let canMerge = $derived.by(() => {
    if (!recording) return false;
    if (recording.format !== 'timelapse' && recording.format !== 'mjpeg') return false;
    return recording.merge_status !== 'merged' && recording.merge_status !== 'daily_merged';
  });

  async function loadRecording(opts: { keepPanel?: boolean } = {}) {
    const token = ++loadToken;
    // keepPanel: mid-session segment switch — keep the playback panel (and its
    // <video>) mounted; only the metadata reloads. The panel shows the
    // isTransitioning overlay meanwhile.
    if (!opts.keepPanel) loading = true;
    error = '';
    try {
      const rec = await getRecording(currentId);
      if (token !== loadToken) return;
      recording = rec;
      syncDetailDateInURL();
      // Dual-mode entry: the day's merge (if any) enables the toggle.
      void loadDayMerge();
      // PlaybackPanel handles player init in its $effect on `recording`.
      // The codec probe that picks <video> vs cycler for timelapse/mjpeg is
      // done lazily by PlaybackPanel's mode derivation.
    } catch (e) {
      if (token !== loadToken) return;
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
      if (token === loadToken) {
        if (!opts.keepPanel) loading = false;
        isTransitioning = false;
      }
    }
  }

  // In-place segment switch (same route, different recording): no component
  // remount (App.svelte keys recording-detail without the id), no skeleton —
  // the playback panel stays mounted and swaps its source. The URL is synced
  // via replaceState so deep-links/refresh stay correct WITHOUT stacking one
  // history entry per segment.
  function switchRecordingInPlace(recordingId: string, offsetSeconds: number | null) {
    if (!recordingId) return;
    if (recordingId === currentId) {
      if (offsetSeconds != null) pendingTimelineSeekOffset = offsetSeconds;
      return;
    }
    if (offsetSeconds != null) pendingTimelineSeekOffset = offsetSeconds;
    isTransitioning = true;
    // Set currentId before anything else: the props-effect below must see
    // currentId already matching to skip (recording is still the old, non-null
    // one until the fetch resolves).
    currentId = recordingId;
    try {
      history.replaceState(null, '', `#/recordings/${recordingId}`);
    } catch { /* non-browser env */ }
    void loadRecording({ keepPanel: true });
  }

  async function navigateToNext() {
    if (!recording) return;
    // Fallback path when PlaybackPanel could not seamlessly chain the next
    // segment (different playback mode, prefetch failed, …).
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
      if (next) switchRecordingInPlace(next.id, null);
    } catch { /* ignore */ }
  }

  // <video> ended → try the next segment.
  function handleEnded() {
    void navigateToNext();
  }

  // Cross-segment timeline seek: switch the recording in place. The offset is
  // applied by PlaybackPanel once the target segment's video is ready
  // (standby-buffer swap, falling back to a fresh load).
  function handleTimelineSeek(recordingId: string, offsetSeconds: number) {
    switchRecordingInPlace(recordingId, offsetSeconds);
  }

  // PlaybackPanel chains seamlessly into a prefetched next segment (video
  // double-buffer adoption or timelapse cycler adoption). Adopt its metadata
  // in place — no fetch, no skeleton, no remount. Sync the URL for deep-links.
  function handleCrossSegment(r: Recording) {
    if (!r || r.id === currentId) return;
    currentId = r.id;
    recording = r;
    try {
      history.replaceState(null, '', `#/recordings/${r.id}`);
    } catch { /* non-browser env */ }
    syncDetailDateInURL();
    // The adopted segment may sit on another day — its merge differs.
    void loadDayMerge();
  }

  // Resolve ?at= absolute timestamp → offset once recording.started_at is known.
  $effect(() => {
    if (pendingTimelineSeekAtMs == null) return;
    if (!recording || !recording.started_at) return;
    const startedAtMs = Date.parse(recording.started_at);
    if (!Number.isFinite(startedAtMs)) return;
    // #496: timelapse-compressed recordings keep the wall clock only in the
    // row's timeline map — resolve ?at= through it so AI-event deep links
    // land on the right frame of the (much shorter) file.
    const wallOffsetSec = Math.max(0, (pendingTimelineSeekAtMs - startedAtMs) / 1000);
    const offsetSec = Math.floor(
      wallToFileSec(parseTimelineMap(recording.timeline_map), wallOffsetSec)
    );
    pendingTimelineSeekAtMs = null;
    if (pendingTimelineSeekOffset == null) pendingTimelineSeekOffset = offsetSec;
  });

  // Keep the watched day in the detail URL (?date=YYYY-MM-DD). The Header
  // back button reads it to return to the recordings list on the SAME day
  // (#321 follow-up); deep links like ?t=/?at= are preserved.
  function syncDetailDateInURL() {
    if (!recording?.started_at) return;
    const day = new Date(recording.started_at).toLocaleDateString('en-CA');
    if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) return;
    try {
      const params = new URLSearchParams(window.location.hash.split('?')[1] || '');
      if (params.get('date') === day) return;
      params.set('date', day);
      history.replaceState(null, '', `#/recordings/${currentId}?${params.toString()}`);
    } catch { /* non-browser env */ }
  }

  // Route-entry effect: reacts to the recordingId / mergeId PROPs (fresh
  // mount, or a remount from another route). Mid-session segment switches do
  // NOT come through here — they own currentId via switchRecordingInPlace,
  // and replaceState never fires hashchange — so once mounted, this effect
  // runs exactly once per entry prop.
  $effect(() => {
    const id = recordingId;
    if (!id) {
      // #/timelapse-merge/{id} entry — the same unified viewer, initialized
      // from the merge side.
      const mid = mergeId;
      if (!mid || mid === lastRoutedId) return;
      lastRoutedId = mid;
      void loadFromMerge(mid);
      return;
    }
    if (id === lastRoutedId) return;
    lastRoutedId = id;
    currentId = id;
    loading = true;
    error = '';
    void loadRecording();
  });

  // Back target keeps the watched day: #/recordings?date=<local day of the
  // recording> — returning from yesterday's playback must land on yesterday's
  // list, not jump to today (#321 follow-up). Merge-only entries (no anchor
  // recording) fall back to the merge window's day.
  function goBackTarget(): string {
    if (recording?.started_at) {
      const day = new Date(recording.started_at).toLocaleDateString('en-CA');
      if (/^\d{4}-\d{2}-\d{2}$/.test(day)) return `#/recordings?date=${day}`;
    }
    if (dayMerge?.window_start) {
      const day = new Date(dayMerge.window_start).toLocaleDateString('en-CA');
      if (/^\d{4}-\d{2}-\d{2}$/.test(day)) return `#/recordings?date=${day}`;
    }
    return '#/recordings';
  }

  function recordingsHashWithDay(): string {
    return goBackTarget();
  }

  // Metadata-facing recording for MetaEditor: seamless chaining swaps
  // `recording` up to ~3×/s on fragmented cameras — throttled here (1/s,
  // trailing flush) so the info card doesn't re-render itself into a strobe.
  let metaRecording = $state<Recording | null>(null);
  let metaFlushTimer: ReturnType<typeof setTimeout> | null = null;
  let metaLastSwap = 0;
  $effect(() => {
    const r = recording;
    if (!r) {
      metaRecording = null;
      return;
    }
    if (metaRecording?.id === r.id) return;
    const elapsed = Date.now() - metaLastSwap;
    if (metaFlushTimer) {
      clearTimeout(metaFlushTimer);
      metaFlushTimer = null;
    }
    if (elapsed >= 1000) {
      metaRecording = r;
      metaLastSwap = Date.now();
    } else {
      metaFlushTimer = setTimeout(() => {
        metaRecording = recording;
        metaLastSwap = Date.now();
        metaFlushTimer = null;
      }, 1000 - elapsed);
    }
  });

  async function confirmDelete() {
    if (!recording) return;
    try {
      await deleteRecording(recording.id);
      window.location.hash = recordingsHashWithDay();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedDeleteRecording');
      deleteConfirm = false;
    }
  }

  function goBack() { window.location.hash = recordingsHashWithDay(); }

  // Merge completion → reload recording (re-probe codec, refresh merge_status).
  function onMergeCompleted() {
    if (recording) clearMergedCodecCache(recording.camera_id);
    void loadRecording();
  }

  // --- Keyboard dispatcher (host owns it; forwards to PlaybackPanel + MergePanel) ---
  function handleKeydown(e: KeyboardEvent) {
    const tag = (e.target as HTMLElement).tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
    // In merged-timelapse mode the recording player is hidden — its hotkeys
    // must not steer it (the merge <video> carries its own native controls).
    const playerActive = !tlMode;
    switch (e.key) {
      case ' ':
        e.preventDefault();
        if (playerActive) playbackPanel?.handleKeyAction('space');
        break;
      case 'ArrowLeft':
        e.preventDefault();
        if (playerActive) playbackPanel?.handleKeyAction('arrowleft');
        break;
      case 'ArrowRight':
        e.preventDefault();
        if (playerActive) playbackPanel?.handleKeyAction('arrowright');
        break;
      case 'Escape':
        if (document.fullscreenElement) { document.exitFullscreen(); break; }
        goBack();
        break;
      case 'f': case 'F':
        e.preventDefault();
        if (playerActive) playbackPanel?.handleKeyAction('f');
        break;
      case 'l': case 'L':
        e.preventDefault();
        if (playerActive) playbackPanel?.handleKeyAction('l');
        break;
      case 'Home':
        e.preventDefault();
        if (playerActive) playbackPanel?.handleKeyAction('home');
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
    {:else if recording || dayMerge}
      <div class="space-y-6">
        <!-- Toolbar: playback-mode segmented control (left) + day navigation
             (right). Both act in place — no route navigation, no flash. -->
        {#if currentViewDay}
          <div class="flex items-center justify-between gap-2 flex-wrap">
            {#if dayMerge && recording}
              <div class="flex items-center gap-0.5 p-0.5 rounded-lg border th-border th-bg-secondary">
                <button
                  type="button"
                  class="btn btn-sm {!tlMode ? 'btn-primary' : 'btn-ghost'} flex items-center gap-1"
                  onclick={() => setTlMode(false)}
                  aria-pressed={!tlMode}
                >
                  <Clapperboard size={14} />
                  {t('detail.viewModeRecording')}
                </button>
                <button
                  type="button"
                  class="btn btn-sm {tlMode ? 'btn-primary' : 'btn-ghost'} flex items-center gap-1"
                  onclick={() => setTlMode(true)}
                  aria-pressed={tlMode}
                  title="{t('detail.viewModeTimelapse')} · {dayMerge.duration_label}"
                >
                  <Hourglass size={14} />
                  {t('detail.viewModeTimelapse')}
                </button>
              </div>
            {:else}
              <span></span>
            {/if}
            <div class="flex items-center gap-0.5 p-0.5 rounded-lg border th-border th-bg-secondary">
              <button
                type="button"
                class="btn btn-sm btn-ghost flex items-center gap-1"
                onclick={() => gotoDay(-1)}
                aria-label={t('library.prevDay')}
                title={t('library.prevDay')}
              >
                <ChevronLeft size={14} />
                <span class="hidden sm:inline">{t('library.prevDay')}</span>
              </button>
              <span class="text-xs font-mono th-text-secondary px-1 tabular-nums">{currentViewDay}</span>
              <button
                type="button"
                class="btn btn-sm btn-ghost flex items-center gap-1"
                onclick={() => gotoDay(1)}
                aria-label={t('library.nextDay')}
                title={t('library.nextDay')}
              >
                <span class="hidden sm:inline">{t('library.nextDay')}</span>
                <ChevronRight size={14} />
              </button>
            </div>
          </div>
        {/if}

        <!-- Playback section -->
        <div class="card border th-border overflow-hidden">
          {#if recording}
            <div class={tlMode ? 'hidden' : ''}>
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
                oncrosssegment={handleCrossSegment}
              />
            </div>
          {/if}
          {#if dayMerge}
            <!-- Mounted as soon as the merge is known (autoplay off — no
                 audio while hidden) so toggling in and out never reloads the
                 player; visibility flips via the hidden class. -->
            <div class={tlMode ? '' : 'hidden'}>
              <TimelapseMergePlayer bind:this={tlPlayer} merge={dayMerge} autoplay={false} />
            </div>
          {/if}
        </div>

        <!-- Merge metadata (visible while in merged-timelapse mode) -->
        {#if tlMode && dayMerge}
          <div class="card p-4 border th-border">
            <div class="flex items-center justify-between gap-3 flex-wrap mb-3">
              <h3 class="text-sm font-medium th-text-secondary">{t('timelapseMerge.title')}</h3>
              <div class="flex items-center gap-2">
                {#if mergeDownloadUrl}
                  <a href={mergeDownloadUrl} download class="btn btn-secondary btn-sm flex items-center gap-1">
                    <Download size={14} />
                    {t('detail.download')}
                  </a>
                {/if}
                <button
                  onclick={() => (mergeDeleteConfirm = true)}
                  class="btn btn-ghost btn-sm flex items-center gap-1 th-color-danger"
                  disabled={mergeDeleting}
                >
                  <Trash2 size={14} />
                  {t('detail.delete')}
                </button>
              </div>
            </div>
            <dl class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
              <div>
                <dt class="th-text-tertiary">{t('timelapseMerge.camera')}</dt>
                <dd class="th-text-primary">{cameraName || dayMerge.camera_id}</dd>
              </div>
              <div>
                <dt class="th-text-tertiary">{t('timelapseMerge.duration')}</dt>
                <dd class="th-text-primary">{dayMerge.duration_label}</dd>
              </div>
              <div>
                <dt class="th-text-tertiary">{t('timelapseMerge.frames')}</dt>
                <dd class="th-text-primary">{dayMerge.frame_count}</dd>
              </div>
              <div>
                <dt class="th-text-tertiary">{t('timelapseMerge.fileSize')}</dt>
                <dd class="th-text-primary">{dayMerge.file_size > 0 ? formatFileSize(dayMerge.file_size) : '—'}</dd>
              </div>
              <div class="col-span-2">
                <dt class="th-text-tertiary">{t('timelapseMerge.windowLabel')}</dt>
                <dd class="th-text-primary">{new Date(dayMerge.window_start).toLocaleString()} → {new Date(dayMerge.window_end).toLocaleString()}</dd>
              </div>
            </dl>
          </div>
        {/if}

        <!-- Recording info + actions + transcode status -->
        {#if metaRecording}
          <MetaEditor
            recording={metaRecording}
            {currentId}
            {mergeState}
            {canMerge}
            onstartmerge={() => mergePanel?.startMerge()}
            oncancelmerge={() => mergePanel?.requestCancel()}
            ondelete={() => (deleteConfirm = true)}
          />
        {/if}
      </div>
    {/if}
  </main>

  <!-- Merge delete confirmation modal -->
  {#if mergeDeleteConfirm && dayMerge}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('detail.deleteTitle')}</h3>
        <p class="th-text-secondary mb-6">{t('timelapseMerge.deleteConfirm')}</p>
        <div class="flex gap-3 justify-end">
          <button onclick={() => (mergeDeleteConfirm = false)} class="btn btn-secondary">
            {t('detail.cancel')}
          </button>
          <button onclick={handleMergeDelete} class="btn btn-danger" disabled={mergeDeleting}>
            {t('detail.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}

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
