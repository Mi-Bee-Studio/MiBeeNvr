<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listRecordings,
    listCameras,
    deleteRecording,
    batchDeleteRecordings,
    downloadRecording,
    batchMergeTimelapse,
    triggerTimelapseMerge,
    subscribeTimelapseMergeProgress,
    listTimelapseMerges,
    getRecordingDailySummary,
    getRecordingsTimeline,
  } from '$lib/api';
  import type { TimelapseMerge } from '$lib/api';
  import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import { getTranscodingStatus, enqueueTranscodeTask, cancelTranscodeTask } from '$lib/api/transcoding';
  import { getItemsPerPage, getAutoRefresh, parseRefreshInterval } from '../lib/preferences';

  import type { Recording, Camera, RecordingDaySummary, RecordingTimelineSegment } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatDate } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { Search, ChevronUp, Table2, ArrowUp, AlertCircle, Trash2, Clock, Hourglass } from 'lucide-svelte';

  // New components
  import FormatFilter from '../components/library/FormatFilter.svelte';
  import CompactList from '../components/library/CompactList.svelte';
  import CalendarView from '../components/timelapse/CalendarView.svelte';
  import AviPlayback from '../components/AviPlayback.svelte';
  import DayTimeline from '../lib/components/DayTimeline.svelte';
  import type { TimelineAIEvent } from '../lib/components/DayTimeline.svelte';
  import { listAIEvents } from '../lib/api/ai-events';
  import { getMiBeeVisionConnected } from '../lib/mibeevision-status.svelte';
  import { Brain } from 'lucide-svelte';

  // ── URL params initialization ──
  // Timeline is the default view for continuous 24/7 recording (the natural
  // interaction model). List falls back to per-segment cards, which suit
  // sparse event clips but not thousands of 30s fragments. Timelapse is its
  // own view because timelapse segments are sparse point-samples (not coverage)
  // and mixing them with video bands distorts the perceived recording gaps.
  let initialViewMode: 'timeline' | 'list' | 'timelapse' = 'timeline';
  let initialFormat = 'All';
  let initialCameraId = '';
  let initialDate: string | null = null;
  try {
    const params = new URLSearchParams(window.location.hash.split('?')[1] || '');
    const v = params.get('view');
    if (v === 'list' || v === 'timeline' || v === 'timelapse') initialViewMode = v;
    // Legacy ?view=gallery URLs (gallery view was removed) silently fall back
    // to the default timeline view rather than rendering an empty page.
    const f = params.get('format');
    if (f && ['All', 'Video', 'Timelapse', 'MJPEG'].includes(f)) initialFormat = f;
    const c = params.get('camera');
    if (c) initialCameraId = c;
    // ?date=YYYY-MM-DD restores the watched day when returning from a
    // recording's detail page (back-nav must land on the day you were
    // watching, not jump to today).
    const dt = params.get('date');
    if (dt && /^\d{4}-\d{2}-\d{2}$/.test(dt)) initialDate = dt;
  } catch {}

  // ── Day-data session cache (silky list↔detail navigation) ──
  // Navigating list → detail → back remounts this page and used to re-fetch
  // the whole day (recordings + AI events) on every return, making it FEEL
  // like a full page reload. Cache the last rendered day module-scope: the
  // return renders instantly from cache, then a background refresh keeps it
  // honest. Invalidated when the selected day changes.
  interface DayCacheEntry {
    recordings: typeof timelineRecordings;
    events: typeof aiTimelineEvents;
    at: number;
  }
  let dayCache: { key: string; entry: DayCacheEntry } | null = null;
  const DAY_CACHE_TTL_MS = 30_000;

  function takeDayCache(key: string): DayCacheEntry | null {
    if (dayCache && dayCache.key === key && Date.now() - dayCache.entry.at < DAY_CACHE_TTL_MS) {
      return dayCache.entry;
    }
    return null;
  }

  function putDayCache(key: string, recordings: typeof timelineRecordings, events: typeof aiTimelineEvents) {
    dayCache = { key, entry: { recordings, events, at: Date.now() } };
  }

  // Prefetch the detail route chunk on first pointer interaction so the
  // list→detail transition has no lazy-load skeleton flash (the module import
  // is cached by the browser after the first prefetch).
  let detailChunkPrefetched = false;
  function prefetchDetailChunk() {
    if (detailChunkPrefetched) return;
    detailChunkPrefetched = true;
    void import('./RecordingDetail.svelte').catch(() => {});
  }

  // ── Filter state ──
  let formatPill = $state(initialFormat);
  let cameraId = $state(initialCameraId);
  let searchQuery = $state('');
  let mergedFilter = $state('');
  let showArchived = $state(false);
  // AI class filter: '' = all, 'person' / 'car' / ... = only recordings with such an event.
  // Populated from ai_events written by the external AI backend; requires that backend running.
  let aiClass = $state('');
  let cameras = $state<Camera[]>([]);

  // ── Date/time state ──
  let currentMonth = $state(new Date());
  let selectedDate = $state<string | null>(initialDate);

  // ── Calendar summary (lightweight per-day aggregate, no row limit) ──
  let calendarSummary = $state<RecordingDaySummary[]>([]);
  let calLoading = $state(false);
  let calError = $state('');

  // ── List mode data (paginated) ──
  let listRecordingsData = $state<Recording[]>([]);
  let listLoading = $state(false);
  let totalRecordings = $state(0);
  let offset = $state(0);
  let limit = $state(getItemsPerPage());
  let sortBy = $state('started_at');
  let sortOrder = $state<'asc' | 'desc'>('desc');
  // Keyset cursor chain for sequential next/prev navigation (O(1) deep pages vs OFFSET's O(N)).
  // cursorStack[0] = page 1 (no cursor). cursorStack[i] = cursor to reach page i+1.
  // When the user clicks next/prev sequentially we use cursors; arbitrary page jumps
  // (e.g. "go to page 5") fall back to OFFSET via handlePageChange.
  let cursorStack = $state<string[]>(['']);
  let currentPageNum = $state(1);

  // ── View mode ──
  let viewMode = $state<'timeline' | 'list' | 'timelapse'>(initialViewMode);

  // ── Timeline data (all recordings for the selected day, grouped by camera in-component) ──
  // Uses the lightweight RecordingTimelineSegment (7 fields) from
  // /api/recordings/timeline instead of the full Recording — issue #115: the
  // full-row endpoint caps at 500 and silently truncated the afternoon on
  // fragmented days. The lightweight endpoint caps at 10k and ships ~10x less.
  let timelineRecordings = $state<RecordingTimelineSegment[]>([]);
  let timelineLoading = $state(false);
  let timelineTruncated = $state(false);
  let timelineAbortController: AbortController | null = null;

  // ── AI event markers overlay (stage E) ──
  // Loaded alongside the day's recordings when MiBeeVision is connected; passed
  // to DayTimeline as colored click-to-seek markers. Disabled/hidden otherwise.
  let aiTimelineEvents = $state<TimelineAIEvent[]>([]);
  let showAIMarkers = $state(true);
  let aiEventsAbortController: AbortController | null = null;
  const miBeeVisionConnected = $derived(getMiBeeVisionConnected());

  // Slices of the day's recordings keyed off the active view: the Timeline tab
  // shows video bands only (no cyan timelapse noise), the Timelapse tab shows
  // timelapse bands only. We fetch all formats once (loadTimelineData) and slice
  // in-component to avoid a second network round-trip and keep totals consistent.
  let timelineRecordingsVideo = $derived(timelineRecordings.filter(r => r.format !== 'timelapse'));
  let timelineRecordingsTimelapse = $derived(timelineRecordings.filter(r => r.format === 'timelapse'));

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
let batchMergeDuration = $state('1h');
let batchMerging = $state(false);

// ── Timelapse preset-range state ──
// Preset buttons (Today / Last 8h / Last 24h / This week / This month) appear
// above the Timelapse view. Clicking one either plays an existing periodic
// merge output covering that range, or offers to generate one on demand.
let presetGenerating = $state(false);
let presetMergeProgress = $state(0); // 0-100, polled during generation
let presetMergeId = $state<number | null>(null); // merge id being polled
let presetProgressAbort: AbortController | null = null;

// Each preset maps to a (duration_label, window_start_fn, window_end_fn) tuple.
// window_start/end are computed relative to NOW at click time, in the user's
// local timezone — the backend aligns them to the named window via ParseMergeDuration.
const timelapsePresets: Array<{ key: string; labelKey: string; duration: string }> = [
  { key: 'today', labelKey: 'timelapseMerge.presetToday', duration: 'natural-day' },
  { key: '8h', labelKey: 'timelapseMerge.preset8h', duration: '8h' },
  { key: '24h', labelKey: 'timelapseMerge.preset24h', duration: '24h' },
  { key: '7d', labelKey: 'timelapseMerge.preset7d', duration: '7d' },
  { key: '30d', labelKey: 'timelapseMerge.preset30d', duration: '30d' },
];

// selectedPresetCamera is the camera the preset buttons act on. Defaults to
// the URL ?camera= param or the first camera in the list.
let selectedPresetCamera = $state<string>('');


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
    listRecordingsData.filter(r => selectedIds.has(r.id) && r.format === 'timelapse')
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
    // Arbitrary page jump: use OFFSET (O(N) for deep pages, but page jumps are rare).
    // Reset the cursor chain since we're leaving sequential navigation.
    offset = (newPage - 1) * limit;
    currentPageNum = newPage;
    cursorStack = [''];
    window.scrollTo(0, 0);
  }

  // Sequential next page via keyset cursor — O(1) regardless of page depth.
  async function goToNextPage() {
    const currentCursor = cursorStack[cursorStack.length - 1];
    const nextCursor = await loadListDataCursor(currentCursor);
    if (nextCursor !== null) {
      cursorStack = [...cursorStack, nextCursor];
      offset += limit;
      currentPageNum++;
      window.scrollTo(0, 0);
    }
  }

  // Sequential prev page — pop the cursor chain back to the previous page.
  async function goToPrevPage() {
    if (cursorStack.length <= 1) return;
    cursorStack = cursorStack.slice(0, -1);
    offset = Math.max(0, offset - limit);
    currentPageNum = Math.max(1, currentPageNum - 1);
    const prevCursor = cursorStack[cursorStack.length - 1];
    await loadListDataCursor(prevCursor);
    window.scrollTo(0, 0);
  }

  function clearFilters() {
    searchQuery = '';
    cameraId = '';
    formatPill = 'All';
    mergedFilter = '';
    showArchived = false;
    aiClass = '';
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
      loadListData();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('recordings.batchDeleteFailed'), 'error');
    }
  }

  // ── Data loading ──

  // Shared filter params (camera, search, merged, archived, ai_class).
  function sharedFilterParams() {
    return {
      camera_id: cameraId || undefined,
      search: searchQuery || undefined,
      merged: mergedFilter === 'true' ? true : mergedFilter === 'false' ? false : undefined,
      archived: showArchived ? true : undefined,
      ai_class: aiClass || undefined,
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

  // Timeline data: fetch all recordings for the selected day (no camera filter —
  // the component groups by camera itself). With rolling merge consolidating
  // 30s fragments into ~hourly segments, a full day is a few hundred rows max.
  // If a camera still has heavy fragmentation (rolling merge disabled / failed),
  // the limit bounds the query; the timeline renders whatever it gets.
  async function loadTimelineData() {
    if (!selectedDate) {
      timelineRecordings = [];
      aiTimelineEvents = [];
      return;
    }
    // Instant paint from the session cache (returning from a detail page),
    // then continue to the network refresh below for freshness.
    const cacheKey = selectedDate;
    const cached = takeDayCache(cacheKey);
    if (cached && timelineRecordings !== cached.recordings) {
      timelineRecordings = cached.recordings;
      aiTimelineEvents = cached.events;
    }
    if (timelineAbortController) timelineAbortController.abort();
    timelineAbortController = new AbortController();
    timelineLoading = true;
    // Kick off the AI events fetch in parallel (best-effort, never blocks the
    // recordings render). One call covers ALL cameras for the day (no camera_id
    // filter) — DayTimeline buckets events per-row in-component.
    void loadDayAIEvents(selectedDate);
    try {
      const dayStart = new Date(selectedDate + 'T00:00:00');
      const dayEnd = new Date(selectedDate + 'T23:59:59.999');
      // Lightweight timeline endpoint (issue #115): 7-column projection with a
      // 10k cap and fixed ASC order. The old path used /api/recordings with
      // limit=500 + asc, which silently dropped everything after the first 500
      // segments on fragmented days (Xiaomi storms ~5k/day) — the afternoon
      // vanished from the timeline even though the recordings existed.
      const response = await getRecordingsTimeline({
        start: dayStart.toISOString(),
        end: dayEnd.toISOString(),
        signal: timelineAbortController.signal,
      });
      timelineRecordings = response.segments;
      timelineTruncated = response.truncated;
      putDayCache(cacheKey, timelineRecordings, aiTimelineEvents);
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      // Non-fatal: timeline stays stale on error
    } finally {
      timelineLoading = false;
    }
  }

  // Best-effort fetch of the selected day's AI events across all cameras.
  // Mirrors TimelineBar.loadAIEvents' range/limit (asc + 2000 cap). Silently
  // no-ops when MiBeeVision is not connected — the toggle is hidden anyway.
  async function loadDayAIEvents(date: string) {
    if (!getMiBeeVisionConnected()) {
      aiTimelineEvents = [];
    if (dayCache && dayCache.key === date) putDayCache(date, timelineRecordings, aiTimelineEvents);
      return;
    }
    if (aiEventsAbortController) aiEventsAbortController.abort();
    aiEventsAbortController = new AbortController();
    try {
      const [y, m, d] = date.split('-').map(Number);
      // Local-midnight range: a "day" is the user's calendar day (matches how
      // recordings are queried + how the 24h axis is labelled).
      const startISO = new Date(y, m - 1, d, 0, 0, 0).toISOString();
      const endISO = new Date(y, m - 1, d, 23, 59, 59).toISOString();
      const resp = await listAIEvents({ start: startISO, end: endISO, asc: true, limit: 2000 });
      aiTimelineEvents = (resp.events || []).map((e) => ({
        id: e.id,
        camera_id: e.camera_id,
        created_at: e.created_at,
        event_type: e.event_type,
        severity: e.severity,
        class_name: e.class_name,
        confidence: e.confidence,
        recording_id: e.recording_id,
      }));
    } catch {
      // Events are an overlay, not critical — fail silent.
      aiTimelineEvents = [];
    }
  }

  // Seek from the timeline → navigate to the recording detail with the clicked
  // offset as a query param (?t=N). The detail page reads ?t on mount and seeks.
  function handleTimelineSeek(recordingId: string, offsetSeconds: number) {
    prefetchDetailChunk();
    window.location.hash = `#/recordings/${recordingId}?t=${Math.floor(offsetSeconds)}`;
  }

  async function loadListData() {
    // Standard load (OFFSET-based, triggered by filter/sort changes).
    // Reset cursor chain on fresh loads.
    cursorStack = [''];
    currentPageNum = 1;
    await loadListDataCursor('');
  }

  // loadListDataCursor fetches a page using either a cursor (keyset, O(1) deep page)
  // or OFFSET (when cursor is ''). Returns the next_cursor from the response, or null
  // if there are no more pages. When cursor is '', uses OFFSET for page 1 / arbitrary jumps.
  async function loadListDataCursor(cursor: string): Promise<string | null> {
    if (listAbortController) listAbortController.abort();
    listAbortController = new AbortController();
    listLoading = true;

    try {
      const useCursor = cursor !== '';
      // 复用 sharedFilterParams()(含 ai_class),避免列表加载漏掉过滤参数。
      const baseParams = {
        ...sharedFilterParams(),
        limit,
        sort_by: sortBy,
        order: sortOrder,
        signal: listAbortController.signal,
      };
      // Cursor request: pass cursor, omit offset. Offset request: pass offset, omit cursor.
      const params = useCursor
        ? { ...baseParams, cursor }
        : { ...baseParams, offset };

      let response;
      if (formatPill === 'All') {
        response = await listRecordings(params);
      } else {
        response = await listRecordings({ ...params, format: apiFormat || undefined });
      }
      listRecordingsData = response.recordings;
      totalRecordings = response.total || 0;
      detectMergeChanges(listRecordingsData);
      return response.next_cursor || null;
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return null;
      return null;
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
    const selectedRecordings = listRecordingsData.filter(r => selectedIds.has(r.id));
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

  // ── Timelapse preset-range handler ──
  // Given a preset (duration_label), find an existing periodic-merge output
  // covering the current time window for the selected camera. If found, play
  // it; otherwise offer to generate one on demand.
  async function handlePresetClick(preset: { key: string; duration: string }) {
    const cameraId = selectedPresetCamera || cameras[0]?.id;
    if (!cameraId) {
      showToast(t('timelapseMerge.noMergeForRange'), 'error');
      return;
    }
    const now = new Date();
    try {
      // Look for a completed merge for this camera whose window_start is on
      // or after the start of the named window (so e.g. "7d" matches a merge
      // started within the last 7 days). The backend lists DESC by window_start.
      const start = computePresetWindowStart(preset.duration, now);
      const resp = await listTimelapseMerges({
        camera_id: cameraId,
        duration: preset.duration,
        start: start.toISOString(),
        status: 'completed',
        limit: 1,
      });
      if (resp.merges.length > 0) {
        // Play the most recent matching merge.
        window.location.hash = `#/timelapse-merge/${resp.merges[0].id}`;
        return;
      }
      // No existing merge → offer to generate one. Use confirm() for simplicity.
      const ok = window.confirm(
        `${t('timelapseMerge.noMergeForRange')}\n${t('timelapseMerge.generatePrompt')}\n\n` +
        `camera: ${cameras.find(c => c.id === cameraId)?.name ?? cameraId}\n` +
        `duration: ${preset.duration}`
      );
      if (!ok) return;
      await generateAndPlayPreset(cameraId, preset.duration, now);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    }
  }

  // computePresetWindowStart returns the inclusive lower bound to pass as the
  // `start` query when looking for an existing merge for the given window.
  function computePresetWindowStart(duration: string, now: Date): Date {
    switch (duration) {
      case 'natural-day': {
        // Start of today in local time.
        const d = new Date(now);
        d.setHours(0, 0, 0, 0);
        return d;
      }
      case '8h': return new Date(now.getTime() - 8 * 3600 * 1000);
      case '24h': return new Date(now.getTime() - 24 * 3600 * 1000);
      case '7d': return new Date(now.getTime() - 7 * 24 * 3600 * 1000);
      case '30d': return new Date(now.getTime() - 30 * 24 * 3600 * 1000);
      default: return new Date(now.getTime() - 24 * 3600 * 1000);
    }
  }

  // generateAndPlayPreset triggers a merge for the given camera/duration and
  // polls progress until completion, then navigates to the player route.
  async function generateAndPlayPreset(cameraId: string, duration: string, refTime: Date) {
    if (presetGenerating) return;
    presetGenerating = true;
    presetMergeProgress = 0;
    presetMergeId = null;
    try {
      // triggerTimelapseMerge runs the merge for the window containing refTime.
      // The handler returns 202 immediately; the merge runs in the background.
      const dateStr = refTime.toISOString().slice(0, 10); // YYYY-MM-DD
      await triggerTimelapseMerge(cameraId, dateStr, duration);
      showToast(t('detail.mergeStarted'), 'success');
      // Subscribe to SSE progress for this camera.
      presetProgressAbort?.abort();
      presetProgressAbort = new AbortController();
      await new Promise<void>((resolve, reject) => {
        const signal = presetProgressAbort!.signal;
        const ac = subscribeTimelapseMergeProgress(
          cameraId,
          (data) => {
            if (signal.aborted) return;
            presetMergeProgress = data.progress ?? 0;
            if (data.status === 'completed') {
              // Find the merge row for this window+duration and navigate to it.
              void findAndPlayMerge(cameraId, duration, refTime).then(resolve, reject);
            } else if (data.status === 'failed') {
              reject(new Error(data.error || t('timelapseMerge.statusFailed')));
            }
          },
          (err) => {
            if (!signal.aborted) reject(err);
          },
        );
        // Stash the AbortController returned by subscribe so cleanup works.
        presetProgressAbort = ac;
      });
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    } finally {
      presetGenerating = false;
      presetProgressAbort?.abort();
      presetProgressAbort = null;
    }
  }

  // findAndPlayMerge locates the timelapse_merges row for the just-completed
  // window and navigates to the player. Retries a few times because the DB
  // write happens just before the SSE 'completed' event.
  async function findAndPlayMerge(cameraId: string, duration: string, refTime: Date) {
    const start = computePresetWindowStart(duration, refTime);
    for (let attempt = 0; attempt < 5; attempt++) {
      const resp = await listTimelapseMerges({
        camera_id: cameraId,
        duration,
        start: start.toISOString(),
        status: 'completed',
        limit: 1,
      });
      if (resp.merges.length > 0) {
        window.location.hash = `#/timelapse-merge/${resp.merges[0].id}`;
        return;
      }
      await new Promise(r => setTimeout(r, 500));
    }
    // Fallback: couldn't find the row — send the user to the recordings list.
    showToast(t('timelapseMerge.notReady'), 'warning');
    window.location.hash = '#/recordings';
  }

  // ── Deferred load timers ──
  let calLoadTimeout: number;
  let listLoadTimeout: number;

  // ── Lifecycle ──
  let visibilityHandler: (() => void) | null = null;

  onMount(() => {
    loadCameras();
    startTranscodingPoll();
    // Idle-time prefetch: the detail route chunk loads in the background so
    // the FIRST list→detail click doesn't flash the lazy-load skeleton either.
    window.setTimeout(prefetchDetailChunk, 1500);

    refreshInterval = window.setInterval(() => {
      loadCalendarSummary();
      loadTimelineData();
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
          loadTimelineData();
        }, getRefreshInterval());
        loadCalendarSummary();
        loadTimelineData();
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
    const _ = [cameraId, formatPill, searchQuery, mergedFilter, showArchived, aiClass, currentMonth];
    clearTimeout(calLoadTimeout);
    calLoadTimeout = window.setTimeout(() => loadCalendarSummary(), 100);
    return () => clearTimeout(calLoadTimeout);
  });

  // Watch list mode pagination/sort → reload list data
  $effect(() => {
    if (viewMode === 'list') {
      const _ = [offset, limit, sortBy, sortOrder, cameraId, formatPill, searchQuery, mergedFilter, showArchived, aiClass];
      clearTimeout(listLoadTimeout);
      listLoadTimeout = window.setTimeout(() => loadListData(), 100);
      return () => clearTimeout(listLoadTimeout);
    }
  });

  // Timeline: reload when the selected day changes (camera/format filters don't
  // apply here — the timeline shows all cameras for the whole day; the coverage
  // bands are colored by format). AI class DOES apply (e.g. "含人" narrows the day's
  // bands to recordings with person events). viewMode gating avoids fetching when
  // neither timeline-viewing tab is visible (same lazy pattern as list). The Timelapse
  // tab reuses the same day fetch and slices off the timelapse-format rows.
  let timelineLoadTimeout: number;
  $effect(() => {
    if (viewMode === 'timeline' || viewMode === 'timelapse') {
      const _ = [selectedDate, aiClass];
      clearTimeout(timelineLoadTimeout);
      timelineLoadTimeout = window.setTimeout(() => loadTimelineData(), 100);
      return () => clearTimeout(timelineLoadTimeout);
    }
  });

  // Handle preference changes (refresh interval, items per page)
  $effect(() => {
    if (refreshInterval) clearInterval(refreshInterval);
    refreshInterval = window.setInterval(() => {
      loadCalendarSummary();
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
    params.set('view', viewMode);
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
        if (v === 'timeline' || v === 'list' || v === 'timelapse') viewMode = v;
        // Legacy ?view=gallery (gallery view removed) falls back to default
        // timeline via the initial value — no explicit handling needed.
        const f = params.get('format');
        if (f && ['All', 'Video', 'Timelapse', 'MJPEG'].includes(f)) formatPill = f;
        const c = params.get('camera');
        if (c !== null) cameraId = c;
        const dt = params.get('date');
        if (dt && /^\d{4}-\d{2}-\d{2}$/.test(dt)) selectedDate = dt;
      } catch {}
    };
    window.addEventListener('hashchange', handler);
    return () => window.removeEventListener('hashchange', handler);
  });

  // Auto-select today's date when in a timeline-viewing mode and no date selected
  $effect(() => {
    if ((viewMode === 'timeline' || viewMode === 'timelapse') && !selectedDate) {
      const today = new Date();
      const y = today.getFullYear();
      const m = String(today.getMonth() + 1).padStart(2, '0');
      const d = String(today.getDate()).padStart(2, '0');
      selectedDate = `${y}-${m}-${d}`;
    }
  });

  // Mirror the selected date into the URL (?date=YYYY-MM-DD, replaceState — no
  // history spam) so navigating to a recording and back returns to the SAME
  // day instead of resetting to today (#321 follow-up).
  $effect(() => {
    const d = selectedDate;
    if (!d) return;
    try {
      const params = new URLSearchParams(window.location.hash.split('?')[1] || '');
      if (params.get('date') === d) return;
      params.set('date', d);
      history.replaceState(null, '', `#/recordings?${params.toString()}`);
    } catch { /* non-browser env */ }
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
            {#if miBeeVisionConnected}
              <button
                type="button"
                class="ai-marker-toggle {showAIMarkers ? 'is-active' : ''}"
                onclick={() => showAIMarkers = !showAIMarkers}
                title={t('library.showAIMarkers')}
                aria-pressed={showAIMarkers}
              >
                <Brain size={14} />
                <span>{t('library.showAIMarkers')}</span>
              </button>
            {/if}
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
          <div class="flex-1 min-w-[140px]">
            <label for="ai-class" class="input-label" title={t('recordings.aiClassHint')}>
              {t('recordings.aiClass')}
            </label>
            <select id="ai-class" class="input" bind:value={aiClass}>
              <option value="">{t('recordings.aiClassAll')}</option>
              <option value="person">{t('recordings.aiClassPerson')}</option>
              <option value="car">{t('recordings.aiClassCar')}</option>
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
          class="btn btn-sm {viewMode === 'timeline' ? 'btn-primary' : 'btn-ghost'}"
          onclick={() => viewMode = 'timeline'}
        >
          <Clock size={16} class="mr-1" />
          {t('library.viewTimeline')}
        </button>
        <button
          class="btn btn-sm {viewMode === 'timelapse' ? 'btn-primary' : 'btn-ghost'}"
          onclick={() => viewMode = 'timelapse'}
        >
          <Hourglass size={16} class="mr-1" />
          {t('library.viewTimelapse')}
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
      {:else if viewMode === 'timeline'}
        <!-- ── Timeline view (video recordings only; timelapse has its own tab) ── -->
        {#if timelineLoading && timelineRecordings.length === 0}
          <div class="card p-12 text-center border th-border">
            <div class="flex justify-center mb-4 th-text-tertiary">
              <Clock size={48} class="animate-pulse" />
            </div>
            <p class="th-text-secondary">{t('common.loading')}</p>
          </div>
        {:else}
          <div class="card p-4 border th-border">
            <p class="text-xs th-text-tertiary mb-2">{t('library.videoOnly')}</p>
            {#if timelineTruncated}
              <div class="flex items-start gap-2 mb-3 p-2 rounded border text-xs th-text-secondary"
                   style="background: rgba(234, 179, 8, 0.08); border-color: rgba(234, 179, 8, 0.35);">
                <AlertCircle size={14} class="mt-0.5 shrink-0" style="color: #eab308;" />
                <span>{t('library.timelineTruncated', { count: timelineRecordings.length })}</span>
              </div>
            {/if}
            <DayTimeline
              {cameras}
              recordings={timelineRecordingsVideo}
              selectedDate={selectedDate || ''}
              onseek={handleTimelineSeek}
              aiEvents={showAIMarkers && miBeeVisionConnected ? aiTimelineEvents : []}
            />
          </div>
        {/if}
      {:else if viewMode === 'timelapse'}
        <!-- ── Timelapse view (timelapse-format recordings only) ── -->
        {#if timelineLoading && timelineRecordings.length === 0}
          <div class="card p-12 text-center border th-border">
            <div class="flex justify-center mb-4 th-text-tertiary">
              <Hourglass size={48} class="animate-pulse" />
            </div>
            <p class="th-text-secondary">{t('common.loading')}</p>
          </div>
        {:else}
          <!-- Preset range buttons: play (or generate) a long-window timelapse -->
          <div class="card p-3 border th-border mb-3">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="text-xs th-text-tertiary mr-1">{t('timelapseMerge.title')}:</span>
              <!-- Camera picker for presets -->
              <select
                class="input input-xs w-auto"
                bind:value={selectedPresetCamera}
                disabled={presetGenerating}
                aria-label={t('timelapseMerge.camera')}
              >
                {#each cameras as cam}
                  <option value={cam.id}>{cam.name || cam.id}</option>
                {/each}
              </select>
              {#each timelapsePresets as preset}
                <button
                  class="btn btn-sm {presetGenerating ? 'btn-ghost' : 'btn-secondary'}"
                  disabled={presetGenerating || cameras.length === 0}
                  onclick={() => handlePresetClick(preset)}
                  title={t(preset.labelKey)}
                >
                  {t(preset.labelKey)}
                </button>
              {/each}
              {#if presetGenerating}
                <span class="text-xs th-text-secondary flex items-center gap-1">
                  <Hourglass size={12} class="animate-pulse" />
                  {presetMergeProgress}%
                </span>
              {/if}
            </div>
            <p class="text-[10px] th-text-tertiary mt-2">
              {t('timelapseMerge.generatePrompt')}
            </p>
          </div>

          <div class="card p-4 border th-border">
            <p class="text-xs th-text-tertiary mb-2">{t('library.timelapseOnly')}</p>
            {#if timelineTruncated}
              <div class="flex items-start gap-2 mb-3 p-2 rounded border text-xs th-text-secondary"
                   style="background: rgba(234, 179, 8, 0.08); border-color: rgba(234, 179, 8, 0.35);">
                <AlertCircle size={14} class="mt-0.5 shrink-0" style="color: #eab308;" />
                <span>{t('library.timelineTruncated', { count: timelineRecordings.length })}</span>
              </div>
            {/if}
            <DayTimeline
              {cameras}
              recordings={timelineRecordingsTimelapse}
              selectedDate={selectedDate || ''}
              onseek={handleTimelineSeek}
              aiEvents={showAIMarkers && miBeeVisionConnected ? aiTimelineEvents : []}
            />
          </div>
        {/if}
      {:else if viewMode === 'list'}
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
          currentPage={currentPageNum}
          {totalPages}
          totalRecordings={totalRecordings}
          onpagechange={handlePageChange}
          onnext={goToNextPage}
          onprev={goToPrevPage}
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
        <option value="1h">{t('timelapse.mergeDuration1h')}</option>
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
        <h3 class="text-lg font-semibold th-text-primary">{t('avi.title')}</h3>
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

<style>
  /* AI markers toggle pill — sits beside FormatFilter. Reuses surface/border
     tokens so it tracks light/dark like the surrounding controls. */
  .ai-marker-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.25rem 0.625rem;
    border-radius: 0.375rem;
    border: 1px solid var(--border, rgba(255, 255, 255, 0.1));
    background: transparent;
    color: inherit;
    font-size: 0.75rem;
    line-height: 1;
    cursor: pointer;
    opacity: 0.7;
    transition: opacity 0.12s, background 0.12s;
  }
  .ai-marker-toggle:hover {
    opacity: 1;
  }
  .ai-marker-toggle.is-active {
    opacity: 1;
    color: #22c55e; /* green — matches the person/AI marker accent */
    border-color: rgba(34, 197, 94, 0.5);
    background: rgba(34, 197, 94, 0.08);
  }
</style>

