<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listRecordings,
    listTimelapseRecordings,
    listCameras,
    deleteRecording,
    batchDeleteRecordings,
    downloadRecording,
    batchMergeTimelapse,
    getRecordingDailySummary,
  } from '$lib/api';
  import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import { getTranscodingStatus, enqueueTranscodeTask, cancelTranscodeTask } from '$lib/api/transcoding';
  import { getItemsPerPage, getAutoRefresh, parseRefreshInterval } from '../lib/preferences';

  import type { Recording, Camera, RecordingDaySummary } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatDate } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { Search, ChevronUp, LayoutGrid, Table2, ArrowUp, AlertCircle, Trash2 } from 'lucide-svelte';

  // New components
  import FormatFilter from '../components/library/FormatFilter.svelte';
  import CompactList from '../components/library/CompactList.svelte';
  import GalleryGrid from '../components/timelapse/GalleryGrid.svelte';
  import CalendarView from '../components/timelapse/CalendarView.svelte';
  import AviPlayback from '../components/AviPlayback.svelte';

  // ── URL params initialization ──
  let initialViewMode: 'gallery' | 'list' = 'gallery';
  let initialFormat = 'All';
  let initialCameraId = '';
  try {
    const params = new URLSearchParams(window.location.hash.split('?')[1] || '');
    const v = params.get('view');
    if (v === 'list') initialViewMode = v;
    const f = params.get('format');
    if (f && ['All', 'Video', 'Timelapse', 'MJPEG'].includes(f)) initialFormat = f;
    const c = params.get('camera');
    if (c) initialCameraId = c;
  } catch {}

  // ── Filter state ──
  let formatPill = $state(initialFormat);
  let cameraId = $state(initialCameraId);
  let searchQuery = $state('');
  let mergedFilter = $state('');
  let showArchived = $state(false);
  let cameras = $state<Camera[]>([]);

  // ── Date/time state ──
  let currentMonth = $state(new Date());
  let selectedDate = $state<string | null>(null);

  // ── Calendar summary (lightweight per-day aggregate, no row limit) ──
  let calendarSummary = $state<RecordingDaySummary[]>([]);
  let calLoading = $state(false);
  let calError = $state('');

  // ── Gallery recordings (selected day only — naturally bounded) ──
  let galleryRecordings = $state<Recording[]>([]);
  let galleryLoading = $state(false);

  // ── List mode data (paginated) ──
  let listRecordingsData = $state<Recording[]>([]);
  let listLoading = $state(false);
  let totalRecordings = $state(0);
  let offset = $state(0);
  let limit = $state(getItemsPerPage());
  let sortBy = $state('started_at');
  let sortOrder = $state<'asc' | 'desc'>('desc');

  // ── View mode ──
  let viewMode = $state<'gallery' | 'list'>(initialViewMode);

  // ── Selection ──
  let selectedIds = $state<Set<string>>(new Set());
  let showBatchDeleteConfirm = $state(false);
  let deleteConfirm = $state<Recording | null>(null);

  // ── Transcoding ──
  let transcodingStatus = $state<ManagerStatus | null>(null);
  let transcodingPollInterval: ReturnType<typeof setInterval> | null = null;

  // ── UI state ──
  let showBackToTop = $state(false);
  let calAbortController: AbortController | null = null;
  let galleryAbortController: AbortController | null = null;
  let listAbortController: AbortController | null = null;

  // ── AVI playback modal state ──
  let showAviPlayback = $state(false);
  let playbackRecordingId = $state('');
  let refreshInterval: number;

// ── Merge tracking ──
let prevMergeStatuses = $state<Record<string, string>>({});
const MERGE_STORAGE_KEY = 'mibee_nvr_merge_active';

function getActiveMergesFromStorage(): Record<string, { progress: number; status: string }> {
  try {
    return JSON.parse(sessionStorage.getItem(MERGE_STORAGE_KEY) || '{}');
  } catch { return {}; }
}

function detectMergeChanges(recordingsList: Recording[]) {
  for (const r of recordingsList) {
    const prev = prevMergeStatuses[r.id];
    if (prev && prev === 'pending') {
      if (r.merge_status === 'merged') {
        showToast(t('detail.mergeCompleted'), 'success');
      } else if (r.merge_status === 'failed') {
        showToast(t('detail.mergeFailed', { error: r.merge_error || '' }), 'error');
      }
    }
    // Update stored status
    prevMergeStatuses[r.id] = r.merge_status || '';
  }
  // Clean up stale entries (recordings no longer in the list)
  const currentIds = new Set(recordingsList.map(r => r.id));
  for (const id of Object.keys(prevMergeStatuses)) {
    if (!currentIds.has(id)) {
      delete prevMergeStatuses[id];
    }
  }
}

// ── Batch merge state ──
let batchMergeDuration = $state('natural-day');
let batchMerging = $state(false);


  // ── Derived ──
  let apiFormat = $derived.by(() => {
    if (formatPill === 'Timelapse') return 'timelapse';
    if (formatPill === 'MJPEG') return 'mjpeg';
    return '';
  });
  let useTimelapseApi = $derived(formatPill === 'Timelapse');
  let currentPage = $derived(offset > 0 || limit > 0 ? Math.floor(offset / limit) + 1 : 1);
  let totalPages = $derived(totalRecordings > 0 && limit > 0 ? Math.ceil(totalRecordings / limit) : 0);
  let selectedTimelapseRecordings = $derived(
    galleryRecordings.filter(r => selectedIds.has(r.id) && r.format === 'timelapse')
  );
  let showBatchMergeButton = $derived(selectedTimelapseRecordings.length >= 2);

  // ── Helper functions ──
  function getCameraName(cameraId: string): string {
    const camera = cameras.find(c => c.id === cameraId);
    return camera ? camera.name : cameraId;
  }

  function pad(n: number): string {
    return String(n).padStart(2, '0');
  }

  function getRefreshInterval(): number {
    return parseRefreshInterval(getAutoRefresh());
  }


  function viewRecording(recording: Recording) {
    window.location.hash = `#/recordings/${recording.id}`;
  }

  function handlePlay(recordingId: string) {
    playbackRecordingId = recordingId;
    showAviPlayback = true;
  }


  function handleSort(field: string) {
    if (sortBy === field) {
      sortOrder = sortOrder === 'asc' ? 'desc' : 'asc';
    } else {
      sortBy = field;
      sortOrder = 'asc';
    }
    offset = 0;
  }

  function handlePageChange(newPage: number) {
    offset = (newPage - 1) * limit;
    window.scrollTo(0, 0);
  }

  function clearFilters() {
    searchQuery = '';
    cameraId = '';
    formatPill = 'All';
    mergedFilter = '';
    showArchived = false;
    selectedDate = null;
    offset = 0;
  }

  // ── Selection ──
  function toggleSelect(id: string) {
    const newSet = new Set(selectedIds);
    if (newSet.has(id)) {
      newSet.delete(id);
    } else {
      newSet.add(id);
    }
    selectedIds = newSet;
  }

  async function confirmDelete() {
    if (!deleteConfirm) return;
    try {
      await deleteRecording(deleteConfirm.id);
      galleryRecordings = galleryRecordings.filter(r => r.id !== deleteConfirm.id);
      listRecordingsData = listRecordingsData.filter(r => r.id !== deleteConfirm.id);
      showToast(t('common.recordingDeleted'), 'success');
      deleteConfirm = null;
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedDeleteRecording'), 'error');
    }
  }

  async function confirmBatchDelete() {
    try {
      await batchDeleteRecordings(Array.from(selectedIds));
      showToast(t('recordings.batchDeleteSuccess', { count: String(selectedIds.size) }), 'success');
      selectedIds = new Set();
      showBatchDeleteConfirm = false;
      loadCalendarSummary();
      loadGalleryData();
      if (viewMode === 'list') loadListData();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('recordings.batchDeleteFailed'), 'error');
    }
  }

  // ── Data loading ──

  // Shared filter params (camera, search, merged, archived).
  function sharedFilterParams() {
    return {
      camera_id: cameraId || undefined,
      search: searchQuery || undefined,
      merged: mergedFilter === 'true' ? true : mergedFilter === 'false' ? false : undefined,
      archived: showArchived ? true : undefined,
    };
  }

  // Calendar summary: lightweight per-day aggregate for the whole month.
  // No row-level limit — the result is bounded by the number of days (max 31).
  async function loadCalendarSummary() {
    if (calAbortController) calAbortController.abort();
    calAbortController = new AbortController();
    calLoading = true;
    calError = '';

    try {
      const calStart = new Date(currentMonth.getFullYear(), currentMonth.getMonth(), 1);
      const calEnd = new Date(currentMonth.getFullYear(), currentMonth.getMonth() + 1, 0, 23, 59, 59, 999);

      const response = await getRecordingDailySummary({
        ...sharedFilterParams(),
        start: calStart.toISOString(),
        end: calEnd.toISOString(),
        formats: formatPill === 'Timelapse' ? 'timelapse,mjpeg' : undefined,
        format: formatPill === 'MJPEG' ? 'mjpeg' : (apiFormat || undefined),
        tz_offset: -new Date().getTimezoneOffset(),
        signal: calAbortController.signal,
      });
      calendarSummary = response.days;
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      calError = e instanceof Error ? e.message : t('common.failedLoadRecordings');
    } finally {
      calLoading = false;
    }
  }

  // Gallery: full recordings for the selected day only (naturally bounded by one day).
  async function loadGalleryData() {
    if (!selectedDate) {
      galleryRecordings = [];
      return;
    }
    if (galleryAbortController) galleryAbortController.abort();
    galleryAbortController = new AbortController();
    galleryLoading = true;

    try {
      const dayStart = new Date(selectedDate + 'T00:00:00');
      const dayEnd = new Date(selectedDate + 'T23:59:59.999');

      if (useTimelapseApi) {
        // Timelapse API returns timelapse + mjpeg formats (preserves prior behavior).
        const response = await listTimelapseRecordings({
          camera_id: cameraId || undefined,
          start: dayStart.toISOString(),
          end: dayEnd.toISOString(),
          signal: galleryAbortController.signal,
        });
        galleryRecordings = response.recordings;
      } else {
        const response = await listRecordings({
          ...sharedFilterParams(),
          format: apiFormat || undefined,
          start: dayStart.toISOString(),
          end: dayEnd.toISOString(),
          signal: galleryAbortController.signal,
        });
        galleryRecordings = response.recordings;
      }
      // Detect merge status changes (completed/failed)
      detectMergeChanges(galleryRecordings);
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      // Non-fatal: gallery stays stale on error
    } finally {
      galleryLoading = false;
    }
  }

  async function loadListData() {
    if (listAbortController) listAbortController.abort();
    listAbortController = new AbortController();
    listLoading = true;

    try {
      if (formatPill === 'All') {
        // listRecordings returns ALL formats when no format filter is set — no need to also call
        // listTimelapseRecordings (which would duplicate mjpeg/timelapse and break pagination/total).
        const response = await listRecordings({
          camera_id: cameraId || undefined,
          search: searchQuery || undefined,
          merged: mergedFilter === 'true' ? true : mergedFilter === 'false' ? false : undefined,
          archived: showArchived ? true : undefined,
          offset,
          limit,
          sort_by: sortBy,
          order: sortOrder,
          signal: listAbortController.signal,
        });
        listRecordingsData = response.recordings;
        totalRecordings = response.total || 0;
      } else {
        const response = await listRecordings({
          camera_id: cameraId || undefined,
          format: apiFormat || undefined,
          search: searchQuery || undefined,
          merged: mergedFilter === 'true' ? true : mergedFilter === 'false' ? false : undefined,
          archived: showArchived ? true : undefined,
          offset,
          limit,
          sort_by: sortBy,
          order: sortOrder,
          signal: listAbortController.signal,
        });
        listRecordingsData = response.recordings;
        totalRecordings = response.total || 0;
      }
      // Detect merge status changes
      detectMergeChanges(listRecordingsData);
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
    } finally {
      listLoading = false;
    }
  }

  async function loadCameras() {
    try {
      cameras = await listCameras();
    } catch (e) {
      console.error('Failed to load cameras:', e);
    }
  }

  // ── Transcoding ──
  async function loadTranscodingStatus() {
    try {
      transcodingStatus = await getTranscodingStatus();
      // Self-limiting poll: if no transcoding tasks are running or pending, stop the 3s
      // poll entirely. This avoids a steady request-per-3s against the DB read pool while
      // the page sits idle. The poll is restarted by handleTranscode() when the user kicks
      // off a new job, and by the visibilitychange handler when the tab regains focus.
      const hasActive = transcodingStatus?.recent_results?.some(
        (t) => t.status === 'running' || t.status === 'pending'
      );
      if (!hasActive) {
        stopTranscodingPoll();
      }
    } catch {
      // Silently fail
    }
  }

  function startTranscodingPoll() {
    stopTranscodingPoll();
    loadTranscodingStatus();
    transcodingPollInterval = setInterval(loadTranscodingStatus, 3000);
  }

  function stopTranscodingPoll() {
    if (transcodingPollInterval) {
      clearInterval(transcodingPollInterval);
      transcodingPollInterval = null;
    }
  }

  function isTranscodingRecording(recordingId: string): TranscodeTask | undefined {
    if (!transcodingStatus?.recent_results) return undefined;
    return transcodingStatus.recent_results.find(
      (t) => t.recording_id === recordingId && (t.status === 'running' || t.status === 'pending')
    );
  }

  function getCompletedTranscodeTask(recordingId: string): TranscodeTask | undefined {
    if (!transcodingStatus?.recent_results) return undefined;
    return transcodingStatus.recent_results.find(
      (t) => t.recording_id === recordingId && t.status === 'completed'
    );
  }

  function getFailedTranscodeTask(recordingId: string): TranscodeTask | undefined {
    if (!transcodingStatus?.recent_results) return undefined;
    return transcodingStatus.recent_results.find(
      (t) => t.recording_id === recordingId && t.status === 'failed' && t.error
    );
  }

  async function handleTranscode(recording: Recording) {
    const target = recording.format === 'h264' ? 'h265' : recording.format === 'h265' ? 'h264' : 'h264';
    try {
      await enqueueTranscodeTask({
        camera_id: recording.camera_id,
        recording_id: recording.id,
        target_codec: target,
        replace_original: true,
      });
      showToast(t('transcoding.recordings.transcodeSuccess', { camera: getCameraName(recording.camera_id) }), 'success');
      // Restart the 3s progress poll now that a task is active (loadTranscodingStatus
      // self-stops when there's nothing running, so we must re-arm it here).
      startTranscodingPoll();
    } catch {
      showToast(t('transcoding.recordings.transcodeFailed'), 'error');
    }
  }

  async function handleBatchTranscode() {
    const selectedRecordings = galleryRecordings.filter(r => selectedIds.has(r.id));
    if (selectedRecordings.length === 0) return;
    if (!transcodingStatus?.enabled) {
      showToast(t('transcoding.warning_global_disabled'), 'error');
      return;
    }
    let queued = 0;
    let failed = 0;
    for (const rec of selectedRecordings) {
      if (isTranscodingRecording(rec.id)) continue;
      const target = rec.format === 'h264' ? 'h265' : rec.format === 'h265' ? 'h264' : 'h264';
      try {
        await enqueueTranscodeTask({
          camera_id: rec.camera_id,
          recording_id: rec.id,
          target_codec: target,
          replace_original: true,
        });
        queued++;
      } catch {
        failed++;
      }
    }
    if (queued > 0) {
      showToast(t('transcoding.batch_queued', { count: String(queued) }), 'success');
      selectedIds = new Set();
      // Re-arm the progress poll for the newly-queued tasks (see handleTranscode).
      startTranscodingPoll();
    }
    if (failed > 0) {
      showToast(t('transcoding.recordings.transcodeFailed'), 'error');
    }
  }

  async function handleDownload(recordingId: string) {
    await downloadRecording(recordingId);
  }

  async function handleBatchMerge() {
    if (selectedTimelapseRecordings.length < 2) return;
    batchMerging = true;
    try {
      const camera_ids = [...new Set(selectedTimelapseRecordings.map(r => r.camera_id))];
      await batchMergeTimelapse({
        camera_ids,
        duration: batchMergeDuration,
        date: selectedDate || undefined,
      });
      showToast(t('detail.mergeCompleted'), 'success');
      selectedIds = new Set();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    } finally {
      batchMerging = false;
    }
  }

  // ── Deferred load timers ──
  let calLoadTimeout: number;
  let galleryLoadTimeout: number;
  let listLoadTimeout: number;

  // ── Lifecycle ──
  let visibilityHandler: (() => void) | null = null;

  onMount(() => {
    loadCameras();
    startTranscodingPoll();

    refreshInterval = window.setInterval(() => {
      loadCalendarSummary();
      loadGalleryData();
    }, getRefreshInterval());

    const handleScroll = () => {
      showBackToTop = window.scrollY > 300;
    };
    window.addEventListener('scroll', handleScroll);

    // Pause all polling when the tab is hidden (e.g. backgrounded) to avoid firing DB
    // queries against the shared read pool while the user isn't looking. Resume on focus.
    // This is the single biggest lever for reducing idle DB read load at scale.
    visibilityHandler = () => {
      if (document.hidden) {
        if (refreshInterval) {
          clearInterval(refreshInterval);
          refreshInterval = null;
        }
        stopTranscodingPoll();
      } else if (refreshInterval === null) {
        // Resumed: restart polling and refresh immediately so data is fresh.
        refreshInterval = window.setInterval(() => {
          loadCalendarSummary();
          loadGalleryData();
        }, getRefreshInterval());
        loadCalendarSummary();
        loadGalleryData();
        startTranscodingPoll();
      }
    };
    document.addEventListener('visibilitychange', visibilityHandler);

    return () => {
      if (refreshInterval) clearInterval(refreshInterval);
      window.removeEventListener('scroll', handleScroll);
      if (visibilityHandler) {
        document.removeEventListener('visibilitychange', visibilityHandler);
      }
      stopTranscodingPoll();
    };
  });

  // ── Effects ──

  // Calendar summary: reload when month or filters change (independent of selected date)
  $effect(() => {
    const _ = [cameraId, formatPill, searchQuery, mergedFilter, showArchived, currentMonth];
    clearTimeout(calLoadTimeout);
    calLoadTimeout = window.setTimeout(() => loadCalendarSummary(), 100);
    return () => clearTimeout(calLoadTimeout);
  });

  // Gallery: reload when selected day or filters change
  $effect(() => {
    const _ = [selectedDate, cameraId, formatPill, searchQuery, mergedFilter, showArchived];
    clearTimeout(galleryLoadTimeout);
    galleryLoadTimeout = window.setTimeout(() => loadGalleryData(), 100);
    return () => clearTimeout(galleryLoadTimeout);
  });

  // Watch list mode pagination/sort → reload list data
  $effect(() => {
    if (viewMode === 'list') {
      const _ = [offset, limit, sortBy, sortOrder, cameraId, formatPill, searchQuery, mergedFilter, showArchived];
      clearTimeout(listLoadTimeout);
      listLoadTimeout = window.setTimeout(() => loadListData(), 100);
      return () => clearTimeout(listLoadTimeout);
    }
  });

  // Handle preference changes (refresh interval, items per page)
  $effect(() => {
    if (refreshInterval) clearInterval(refreshInterval);
    refreshInterval = window.setInterval(() => {
      loadCalendarSummary();
      loadGalleryData();
    }, getRefreshInterval());
    limit = getItemsPerPage();
    return () => {
      if (refreshInterval) clearInterval(refreshInterval);
    };
  });

  // Sync viewMode + formatPill + cameraId to URL
  $effect(() => {
    const hash = window.location.hash;
    const qIdx = hash.indexOf('?');
    const base = qIdx !== -1 ? hash.slice(0, qIdx) : hash;

    const params = new URLSearchParams();
    if (viewMode !== 'gallery') params.set('view', viewMode);
    if (formatPill !== 'All') params.set('format', formatPill);
    if (cameraId) params.set('camera', cameraId);

    const qs = params.toString();
    const newHash = qs ? base + '?' + qs : base;
    if (window.location.hash !== newHash) {
      window.location.hash = newHash;
    }
  });

  // React to URL hash changes from nav clicks
  $effect(() => {
    const handler = () => {
      try {
        const params = new URLSearchParams(window.location.hash.split('?')[1] || '');
        const v = params.get('view');
        if (v === 'gallery' || v === 'list') viewMode = v;
        const f = params.get('format');
        if (f && ['All', 'Video', 'Timelapse', 'MJPEG'].includes(f)) formatPill = f;
        const c = params.get('camera');
        if (c !== null) cameraId = c;
      } catch {}
    };
    window.addEventListener('hashchange', handler);
    return () => window.removeEventListener('hashchange', handler);
  });

  // Auto-select today's date when in gallery mode and no date selected
  $effect(() => {
    if (viewMode === 'gallery' && !selectedDate) {
      const today = new Date();
      const y = today.getFullYear();
      const m = String(today.getMonth() + 1).padStart(2, '0');
      const d = String(today.getDate()).padStart(2, '0');
      selectedDate = `${y}-${m}-${d}`;
    }
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold th-text-primary mb-4">{t('nav.recordings')}</h2>

      <!-- ── Filter bar ── -->
      <div class="card p-4 mb-6 border th-border">
        <div class="flex flex-wrap items-end gap-3">
          <div class="flex items-center gap-2 pb-[2px]">
            <FormatFilter bind:selectedFormat={formatPill} />
          </div>
          <div class="flex-1 min-w-[160px]">
            <label for="camera" class="input-label">{t('recordings.camera')}</label>
            <select id="camera" class="input" bind:value={cameraId}>
              <option value="">{t('recordings.allCameras')}</option>
              {#each cameras as camera}
                <option value={camera.id}>{camera.name}</option>
              {/each}
            </select>
          </div>
          <div class="flex-1 min-w-[180px]">
            <label class="input-label" for="search-input">{t('recordings.search')}</label>
            <div class="relative">
              <Search size={16} class="absolute left-3 top-1/2 -translate-y-1/2 th-text-tertiary" />
              <input
                id="search-input"
                type="text"
                class="input pl-9"
                placeholder={t('recordings.search')}
                bind:value={searchQuery}
              />
            </div>
          </div>
          <div class="flex items-center gap-1 pb-[2px]">
            <button onclick={clearFilters} class="btn btn-ghost btn-sm">
              {t('recordings.clearFilters')}
            </button>
          </div>
        </div>
      </div>


      <!-- ── Calendar view (always visible) ── -->
      <CalendarView bind:currentMonth bind:selectedDate days={calendarSummary} />

      <!-- ── View mode tabs ── -->
      <div class="flex items-center gap-2 mb-4 mt-4">
        <button
          class="btn btn-sm {viewMode === 'gallery' ? 'btn-primary' : 'btn-ghost'}"
          onclick={() => viewMode = 'gallery'}
        >
          <LayoutGrid size={16} class="mr-1" />
          {t('library.viewGallery')}
        </button>
        <button
          class="btn btn-sm {viewMode === 'list' ? 'btn-primary' : 'btn-ghost'}"
          onclick={() => viewMode = 'list'}
        >
          <Table2 size={16} class="mr-1" />
          {t('library.viewList')}
        </button>
      </div>

      <!-- ── Error state ── -->
      {#if calError}
        <div class="card border th-border-danger p-8 text-center">
          <div class="flex justify-center mb-4 th-color-danger">
            <AlertCircle size={48} />
          </div>
          <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
          <p class="th-text-secondary mb-4">{calError}</p>
          <button onclick={loadCalendarSummary} class="btn btn-primary btn-sm">{t('common.retry')}</button>
        </div>
      {:else if viewMode === 'gallery'}
        <!-- ── Gallery view ── -->
        <GalleryGrid
          bind:selectedDate
          recordings={galleryRecordings}
          {cameras}
          onselectRecording={viewRecording}
          selectedIds={[]}
          ontoggleselect={(r: Recording) => toggleSelect(r.id)}
          selectMode={selectedIds.size > 0}
          ondeleteRecording={(r: Recording) => deleteConfirm = r}
          onplay={handlePlay}

        />
      {:else}
        <!-- ── List view ── -->
        <CompactList
          recordings={listRecordingsData}
          {cameras}
          bind:selectedIds
          ontoggleselect={toggleSelect}
          onview={viewRecording}
          ondelete={(r: Recording) => deleteConfirm = r}
          ontranscode={handleTranscode}
          ondownload={handleDownload}
          bind:sortBy
          bind:sortOrder
          onsort={handleSort}
          {transcodingStatus}
          loading={listLoading}
          {currentPage}
          {totalPages}
          totalRecordings={totalRecordings}
          onpagechange={handlePageChange}
          onplay={handlePlay}

        />
      {/if}
    </div>
  </main>
</div>

<!-- ── Floating batch action bar ── -->
{#if selectedIds.size > 0}
  <div class="fixed bottom-6 left-1/2 -translate-x-1/2 z-40 flex items-center gap-3 px-4 py-2.5 rounded-lg shadow-lg border th-border th-bg-primary">
    <span class="text-sm font-medium th-text-primary">
      {t('recordings.selected', { count: String(selectedIds.size) })}
    </span>
    <button
      onclick={() => showBatchDeleteConfirm = true}
      class="btn btn-danger btn-sm"
    >
      {t('recordings.deleteSelected')}
    </button>
    {#if transcodingStatus?.enabled}
      <button
        onclick={handleBatchTranscode}
        class="btn btn-primary btn-sm"
      >
        {t('transcoding.transcode_selected')}
      </button>
    {/if}
    {#if showBatchMergeButton}
      <select
        class="input input-sm w-auto"
        bind:value={batchMergeDuration}
        disabled={batchMerging}
      >
        <option value="8h">{t('timelapse.mergeDuration8h')}</option>
        <option value="12h">{t('timelapse.mergeDuration12h')}</option>
        <option value="24h">{t('timelapse.mergeDuration24h')}</option>
        <option value="natural-day">{t('timelapse.mergeDurationNaturalDay')}</option>
        <option value="7d">{t('timelapse.mergeDuration7d')}</option>
        <option value="30d">{t('timelapse.mergeDuration30d')}</option>
      </select>
      <button
        onclick={handleBatchMerge}
        class="btn btn-primary btn-sm"
        disabled={batchMerging}
      >
        {t('timelapse.batchMerge')}
      </button>
    {/if}
    <button
      onclick={() => selectedIds = new Set()}
      class="btn btn-ghost btn-sm"
    >
      {t('recordings.cancel')}
    </button>
  </div>
{/if}

<!-- ── Batch delete confirmation modal ── -->
{#if showBatchDeleteConfirm}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('recordings.batchDeleteTitle')}</h3>
      <p class="th-text-secondary mb-6">
        {t('recordings.batchDeleteMessage', { count: String(selectedIds.size) })}
      </p>
      <div class="flex gap-3 justify-end">
        <button onclick={() => showBatchDeleteConfirm = false} class="btn btn-secondary">
          {t('recordings.cancel')}
        </button>
        <button onclick={confirmBatchDelete} class="btn btn-danger">
          {t('recordings.deleteConfirm')}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- ── Delete confirmation modal ── -->
{#if deleteConfirm}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('recordings.deleteTitle')}</h3>
      <p class="th-text-secondary mb-6">
        {t('recordings.deleteMessage', { camera_id: deleteConfirm.camera_id })}
      </p>
      <div class="flex gap-3 justify-end">
        <button
          onclick={() => deleteConfirm = null}
          class="btn btn-secondary"
        >
          {t('recordings.cancel')}
        </button>
        <button
          onclick={confirmDelete}
          class="btn btn-danger"
        >
          {t('recordings.deleteConfirm')}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- ── Back to top button ── -->
{#if showBackToTop}
  <button
    onclick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}
    class="fixed bottom-6 right-6 z-30 w-10 h-10 rounded-full bg-primary text-primary-foreground shadow-lg flex items-center justify-center hover:bg-primary/90 transition-all duration-300"
    title={t('recordings.backToTop')}
  >
    <ArrowUp size={20} />
  </button>
{/if}

<!-- ── AVI Playback modal ── -->
{#if showAviPlayback}
  <div
    class="fixed inset-0 bg-black/70 flex items-center justify-center p-4 z-50"
    onclick={() => showAviPlayback = false}
    role="dialog"
    aria-modal="true"
  >
    <div
      class="card max-w-3xl w-full p-4"
      onclick={(e) => e.stopPropagation()}
    >
      <div class="flex items-center justify-between mb-3">
        <h3 class="text-lg font-semibold th-text-primary">AVI Playback</h3>
        <button
          onclick={() => showAviPlayback = false}
          class="btn btn-ghost btn-sm"
        >
          ✕
        </button>
      </div>
      <AviPlayback recordingId={playbackRecordingId} />
    </div>
  </div>
{/if}

