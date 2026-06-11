<script lang="ts">
  import { onMount, tick } from 'svelte';
  import {
    getRecording,
    deleteRecording,
    downloadRecording as apiDownloadRecording,
    getRecordingVideoUrl,
    getMergedRecordingUrl,
    listRecordings,
    getTimelapseFrames,
    loadTimelapseFrameBlob,
    triggerTimelapseMerge,
    retryRecordingMerge,
    subscribeTimelapseMergeProgress,
    cancelMerge,
    fetchTimelapsePreview,
    ApiRequestError
  } from '$lib/api';
  import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import type { Recording, TimelapseFrame, TimelapsePreviewFrame } from '$lib/api';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { AlertTriangle, HelpCircle, SkipForward, Loader2, RefreshCw, Play, Pause, ChevronLeft, ChevronRight } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import MjpegPlayer from '$lib/components/MjpegPlayer.svelte';
  import { showToast } from '$lib/toast';
  import VideoPlaybackControls from '$lib/components/VideoPlaybackControls.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  let { recordingId = '' } = $props();
  let currentId = $state('');
  let recording = $state<Recording | null>(null);
  let loading = $state(true);
  let error = $state('');
  let deleteConfirm = $state(false);
  let mjpegPlayer: MjpegPlayer | undefined = $state();
  let videoUrl = $state('');
  let videoLoading = $state(false);
let videoSpeed = $state(1);
let videoFullscreen = $state(false);
let videoEl = $state<HTMLVideoElement | null>(null);
let videoCurrentTime = $state(0);
let videoDuration = $state(0);
let videoBuffered = $state(0);
let videoIsPlaying = $state(false);
let formatBadgeVisible = $state(true);
let formatBadgeTimeout = $state<ReturnType<typeof setTimeout> | null>(null);
let videoLoop = $state(false);
let videoError = $state<string | null>(null);
let videoErrorMsg = $state('');
let videoRetryCount = $state(0);
let videoStalled = $state(false);
let videoStallTimeout: ReturnType<typeof setTimeout> | null = null;
const MAX_VIDEO_RETRIES = 3;
let loadErrorType = $state<'generic' | 'not_found'>('generic');
  let downloadProgress = $state(0);
  let isDownloading = $state(false);
  let nextRecordingId = $state<string | null>(null);
  let isTransitioning = $state(false);
  // Transcoding state
  let transcodingStatus = $state<ManagerStatus | null>(null);
  let transcodingPollInterval: ReturnType<typeof setInterval> | null = null;
  let transcodeTask = $derived(findTranscodeTask());

let formatLabel = $derived.by(() => {
  if (!recording) return '';
  switch (recording.format) {
    case 'h264': return t('recording.format.h264');
    case 'h265': return t('recording.format.h265');
    case 'timelapse': return t('recording.format.timelapse');
    default: return recording.format;
  }
});

let formatBadgeClass = $derived.by(() => {
  if (!recording) return 'badge-neutral';
  if (recording.format === 'timelapse') return 'bg-cyan-500/20 text-cyan-300 dark:text-cyan-300';
  if (recording.format === 'h264' || recording.format === 'h265') return 'bg-[var(--color-info)]/20 text-[var(--color-info)]';
  return 'bg-white/10 th-text-secondary';
});

  // Timelapse player state
  let timelapseFrames = $state<TimelapseFrame[]>([]);
  let tlCurrentFrame = $state(0);
  let tlIsPlaying = $state(false);
  let tlSpeed = $state(1);
  let tlLoading = $state(false);
  let tlError = $state('');
  const tlSpeeds = [1, 2, 4];
  let tlPlayTimeout: ReturnType<typeof setTimeout> | null = null;
  let tlBlobCache = $state<Map<number, string>>(new Map());
  let tlAbortController: AbortController | null = null;
  let tlLoop = $state(false);
  let tlSeekLoading = $state(false);
  let tlSeekTimeout: ReturnType<typeof setTimeout> | null = null;

// Merge state
let mergeInProgress = $state(false);
let mergeProgressPct = $state(0);
let mergeErrorMsg = $state('');
let mergeAbortController = $state<AbortController | null>(null);
let selectedMergeDuration = $state('natural-day');
let mergeStartTime = $state(0);
let mergeEta = $state('');
let mergeReconnectTimer: ReturnType<typeof setTimeout> | null = null;
let cancelMergeConfirm = $state(false);

// Timelapse preview state
let timelapsePreviewFrames = $state<TimelapsePreviewFrame[]>([]);
let timelapsePreviewLoading = $state(false);
let timelapsePreviewError = $state('');

// SessionStorage merge tracking — survives navigation away and page refresh
const MERGE_STORAGE_KEY = 'mibee_nvr_merge_active';

function saveMergeState(data: { cameraId: string; recordingId: string; progress: number; status: string }) {
  try {
    const all = JSON.parse(sessionStorage.getItem(MERGE_STORAGE_KEY) || '{}');
    all[data.cameraId] = data;
    sessionStorage.setItem(MERGE_STORAGE_KEY, JSON.stringify(all));
  } catch {}
}

function clearMergeState(cameraId: string) {
  try {
    const all = JSON.parse(sessionStorage.getItem(MERGE_STORAGE_KEY) || '{}');
    delete all[cameraId];
    if (Object.keys(all).length === 0) {
      sessionStorage.removeItem(MERGE_STORAGE_KEY);
    } else {
      sessionStorage.setItem(MERGE_STORAGE_KEY, JSON.stringify(all));
    }
  } catch {}
}

function getMergeStateForCamera(cameraId: string): { progress: number; status: string; recordingId: string } | null {
  try {
    const all = JSON.parse(sessionStorage.getItem(MERGE_STORAGE_KEY) || '{}');
    return all[cameraId] || null;
  } catch { return null; }
}

/** Restore merge state from DB or sessionStorage when returning to this page */
function restoreMergeState(rec: Recording) {
  // Check sessionStorage first — persists across navigation, cleared on completion
  const stored = getMergeStateForCamera(rec.camera_id);
  if (stored && stored.status === 'pending' && stored.progress < 100) {
    mergeInProgress = true;
    mergeProgressPct = stored.progress;
    mergeErrorMsg = '';
    startMergeSse(rec.camera_id, rec.id);
    startMergePolling(rec.id, rec.camera_id);
    return;
  }

  // Fallback: check DB-persisted progress (survives page refresh)
  if (rec.merge_status === 'pending' && rec.merge_progress != null && rec.merge_progress > 0 && rec.merge_progress < 100) {
    mergeInProgress = true;
    mergeProgressPct = rec.merge_progress;
    mergeErrorMsg = '';
    // Re-establish SSE subscription for live progress updates
    startMergeSse(rec.camera_id, rec.id);
    startMergePolling(rec.id, rec.camera_id);
    // Also store in sessionStorage for other pages
    saveMergeState({
      cameraId: rec.camera_id,
      recordingId: rec.id,
      progress: rec.merge_progress,
      status: 'pending',
    });
  }
  // Check DB-persisted failed status
  if (rec.merge_status === 'failed' && rec.merge_error) {
    mergeErrorMsg = rec.merge_error;
  }
}

/** Subscribe to merge SSE progress updates for a camera, with auto-reconnection */
function startMergeSse(cameraId: string, recordingId: string) {
  let reconnectAttempt = 0;
  const maxBackoff = 30000;

  function connect() {
    mergeAbortController?.abort();
    mergeAbortController = null;

    if (!mergeInProgress) return;

  const ac = subscribeTimelapseMergeProgress(cameraId, (data) => {
        reconnectAttempt = 0; // Reset backoff on any event
        if (data.status === 'completed') {
          stopMergePolling();
          mergeInProgress = false;
          mergeProgressPct = 100;
          mergeEta = '';
          mergeAbortController = null;
          clearMergeState(cameraId);
          loadRecording();
          showToast(t('detail.mergeCompleted'), 'success');
        } else if (data.status === 'failed') {
          stopMergePolling();
          mergeInProgress = false;
          mergeEta = '';
          mergeErrorMsg = data.error || '';
          clearMergeState(cameraId);
          showToast(t('detail.mergeFailed', { error: data.error || '' }), 'error');
        } else if (data.progress !== undefined) {
          mergeProgressPct = data.progress;
          updateMergeEta();
          saveMergeState({
            cameraId,
            recordingId,
            progress: data.progress,
            status: 'pending',
          });
        }
      },
      () => {
        // SSE error — schedule reconnect
        scheduleReconnect(cameraId);
      }
    );

    mergeAbortController = ac;
  }

  function scheduleReconnect(cameraId: string) {
    if (!mergeInProgress) return;
    const delay = Math.min(maxBackoff, 1000 * Math.pow(2, reconnectAttempt));
    reconnectAttempt++;
    if (mergeReconnectTimer) clearTimeout(mergeReconnectTimer);
    mergeReconnectTimer = setTimeout(async () => {
      if (!mergeInProgress) return;
      // Poll DB for current progress before reconnecting
      try {
        const rec = await getRecording(recordingId);
        if (!rec) return;
        if (rec.merge_status === 'merged') {
          stopMergePolling();
          mergeInProgress = false;
          mergeProgressPct = 100;
          mergeEta = '';
          clearMergeState(cameraId);
          loadRecording();
          showToast(t('detail.mergeCompleted'), 'success');
          return;
        }
        if (rec.merge_status === 'failed') {
          stopMergePolling();
          mergeInProgress = false;
          mergeEta = '';
          mergeErrorMsg = rec.merge_error || '';
          clearMergeState(cameraId);
          showToast(t('detail.mergeFailed', { error: mergeErrorMsg }), 'error');
          return;
        }
        if (rec.merge_progress > 0) {
          mergeProgressPct = rec.merge_progress;
          updateMergeEta();
        }
      } catch {}
      connect();
    }, delay);
  }

  connect();
}

  async function loadRecording() {
    loading = true;
    error = '';
    nextRecordingId = null;
    try {
      recording = await getRecording(currentId);
      if (recording) {
        // Restore merge state from DB if merge is in progress
        restoreMergeState(recording);

        if (recording.format === 'timelapse' || recording.format === 'mjpeg') {
          // initPlayer called reactively via $effect when mjpegPlayer ref is set
          if (recording.merge_status === 'merged') {
            initVideoPlayer();
          } else {
            initTimelapsePlayer();
            loadTimelapsePreview();
          }
        } else if (recording.format === 'h264' || recording.format === 'h265') {
          initVideoPlayer();
        }
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

  async function loadNextRecording() {
    if (!recording) return null;
    try {
      const resp = await listRecordings({
        camera_id: recording.camera_id,
        format: recording.format,
        start: recording.ended_at ? new Date(recording.ended_at).toISOString() : undefined,
        sort_by: 'started_at',
        order: 'asc',
        limit: 1,
        offset: 0,
      });
      return resp.recordings.length > 0 ? resp.recordings[0] : null;
    } catch (e) { return null; }
  }
  async function handleVideoEnded() {
    if (videoLoop && videoEl) {
      videoEl.currentTime = 0;
      await videoEl.play();
      return;
    }
    const next = await loadNextRecording();
    if (next) { isTransitioning = true; currentId = next.id; await loadRecording(); isTransitioning = false; }
  }

  function handleTimeUpdate(e: Event) {
    const video = e.target as HTMLVideoElement;
    videoCurrentTime = video.currentTime;
    videoDuration = video.duration || 0;
    if (video.duration && video.currentTime / video.duration > 0.8 && !nextRecordingId) prefetchNextRecording();
  }

  async function prefetchNextRecording() {
    if (nextRecordingId || !recording) return;
    const next = await loadNextRecording();
    if (next) {
      nextRecordingId = next.id;
    }
  }

  async function navigateToNext() {
    const next = await loadNextRecording();
    if (next) { isTransitioning = true; currentId = next.id; await loadRecording(); isTransitioning = false; }
  }

function initVideoPlayer() {
  videoSpeed = 1;
  videoLoading = true;
  // Merged timelapse/MJPEG uses /merged endpoint; regular video uses /download
  if (recording && (recording.format === 'timelapse' || recording.format === 'mjpeg')) {
    videoUrl = getMergedRecordingUrl(currentId);
  } else {
    videoUrl = getRecordingVideoUrl(currentId);
  }
  if (formatBadgeTimeout) { clearTimeout(formatBadgeTimeout); formatBadgeTimeout = null; }
  videoError = null;
  videoErrorMsg = '';
  videoRetryCount = 0;
  videoStalled = false;
  if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
}

function setVideoSpeed(speed: number) {
  videoSpeed = speed;
  const video = document.querySelector('video');
  if (video) video.playbackRate = speed;
}


function handleVideoLoadedMetadata(e: Event) {
  const video = e.target as HTMLVideoElement;
  videoDuration = video.duration || 0;
}

function handleVideoLoadedData() {
  videoLoading = false;
  videoError = null;
  videoErrorMsg = '';
  videoRetryCount = 0;
  videoStalled = false;
  if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
}
function handleVideoProgress() {
  if (!videoEl || !videoEl.buffered.length || !videoEl.duration) {
    videoBuffered = 0;
    return;
  }
  const bf = videoEl.buffered;
  for (let i = 0; i < bf.length; i++) {
    if (bf.start(i) <= videoEl.currentTime && bf.end(i) >= videoEl.currentTime) {
      videoBuffered = (bf.end(i) / videoEl.duration) * 100;
      return;
    }
  }
  videoBuffered = (bf.end(bf.length - 1) / videoEl.duration) * 100;
}
function handleVideoPlay() {
  videoIsPlaying = true;
  // Auto-hide format badge after 3 seconds of playback
  if (formatBadgeTimeout) clearTimeout(formatBadgeTimeout);
  formatBadgeTimeout = setTimeout(() => { formatBadgeVisible = false; }, 3000);
}
function handleVideoPause() {
  videoIsPlaying = false;
  formatBadgeVisible = true;
  if (formatBadgeTimeout) { clearTimeout(formatBadgeTimeout); formatBadgeTimeout = null; }
}

function handleVideoContainerMouseEnter() {
  formatBadgeVisible = true;
  if (formatBadgeTimeout) { clearTimeout(formatBadgeTimeout); formatBadgeTimeout = null; }
}
function handleVideoContainerMouseLeave() {
  if (videoIsPlaying) {
    formatBadgeTimeout = setTimeout(() => { formatBadgeVisible = false; }, 3000);
  }
}
function toggleVideoLoop() {
  videoLoop = !videoLoop;
}

function handleVideoError(e: Event) {
  const video = e.target as HTMLVideoElement;
  const mediaError = video.error;
  if (!mediaError) return;

  videoStalled = false;
  if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }

  const code = mediaError.code;
  // MEDIA_ERR_ABORTED (1) is user-initiated — no recovery UI needed
  if (code === MediaError.MEDIA_ERR_ABORTED) return;

  videoLoading = false;

  if (code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED) {
    videoError = 'src_not_supported';
    videoErrorMsg = t('detail.videoFormatNotSupported');
  } else if (code === MediaError.MEDIA_ERR_NETWORK) {
    videoError = 'network';
    videoErrorMsg = t('detail.videoNetworkError');
  } else if (code === MediaError.MEDIA_ERR_DECODE) {
    videoError = 'decode';
    videoErrorMsg = t('detail.videoDecodeError');
  } else {
    videoError = 'unknown';
    videoErrorMsg = t('detail.videoUnknownError');
  }
}

function handleVideoRetry() {
  if (videoRetryCount >= MAX_VIDEO_RETRIES) return;
  videoRetryCount++;
  videoError = null;
  videoErrorMsg = '';
  videoLoading = true;
  videoStalled = false;
  if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }

  const video = document.querySelector('video');
  if (video) {
    video.removeAttribute('src');
    video.load();
    video.src = videoUrl;
    video.load();
  }
}

function handleVideoCanPlay(e: Event) {
  videoStalled = false;
  if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
  videoError = null;
  videoErrorMsg = '';
  videoRetryCount = 0;
}

function handleVideoWaiting() {
  if (videoStallTimeout) clearTimeout(videoStallTimeout);
  videoStallTimeout = setTimeout(() => {
    videoStalled = true;
  }, 3000);
}

function handleVideoPlaying() {
  videoStalled = false;
  if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
}

async function handleMergeAndPlay() {
  if (!recording) return;
  mergeInProgress = true;
  mergeProgressPct = 0;
  mergeErrorMsg = '';
  mergeStartTime = Date.now();
  mergeEta = '';

  // Store in sessionStorage so other pages can track this merge
  saveMergeState({
    cameraId: recording.camera_id,
    recordingId: recording.id,
    progress: 0,
    status: 'pending',
  });

  try {
    if (recording.format === 'timelapse') {
      // Use direct retry-merge endpoint for timelapse recordings
      await retryRecordingMerge(recording.id);
    } else {
      await triggerTimelapseMerge(recording.camera_id, undefined, selectedMergeDuration);
    }
    startMergeSse(recording.camera_id, recording.id);
    // DB polling fallback when SSE is unavailable (e.g. RollingMergeManager is nil)
    startMergePolling(recording.id, recording.camera_id);
  } catch (e) {
    mergeInProgress = false;
    mergeErrorMsg = e instanceof Error ? e.message : 'Failed to start merge';
    clearMergeState(recording.camera_id);
  }
}

// DB polling fallback for merge progress (SSE may be unavailable)
let mergePollTimer = null;

function startMergePolling(recId, camId) {
  stopMergePolling();
  let attempts = 0;
  const maxAttempts = 60; // 2 minutes at 2s interval
  mergePollTimer = setInterval(async () => {
    attempts++;
    try {
      const rec = await getRecording(recId);
      if (!rec) { stopMergePolling(); return; }
      if (rec.merge_progress > 0 && rec.merge_progress < 100) {
        mergeProgressPct = rec.merge_progress;
        attempts = 0; // reset timeout when progress changes
      }
      if (rec.merge_status === 'merged') {
        stopMergePolling();
        mergeInProgress = false;
        mergeProgressPct = 100;
        mergeAbortController?.abort();
        mergeAbortController = null;
        clearMergeState(camId);
        loadRecording();
        showToast(t('detail.mergeCompleted'), 'success');
      } else if (rec.merge_status === 'failed') {
        stopMergePolling();
        mergeInProgress = false;
        mergeErrorMsg = rec.merge_error || 'Merge failed';
        mergeAbortController?.abort();
        mergeAbortController = null;
        clearMergeState(camId);
        showToast(t('detail.mergeFailed', { error: mergeErrorMsg }), 'error');
      } else if (attempts >= maxAttempts) {
        // Timeout: merge didn't progress, stop polling
        stopMergePolling();
        mergeInProgress = false;
        mergeErrorMsg = 'Merge timed out — no progress detected';
        clearMergeState(camId);
      }
    } catch (_e) { /* retry next interval */ }
  }, 2000);
}

function stopMergePolling() {
  if (mergePollTimer) { clearInterval(mergePollTimer); mergePollTimer = null; }
}

// --- Cancel Merge ---
async function handleCancelMerge() {
  if (!recording) return;
  cancelMergeConfirm = false;
  try {
    await cancelMerge(recording.camera_id);
    mergeInProgress = false;
    mergeProgressPct = 0;
    mergeEta = '';
    mergeErrorMsg = '';
    mergeAbortController?.abort();
    mergeAbortController = null;
    if (mergeReconnectTimer) { clearTimeout(mergeReconnectTimer); mergeReconnectTimer = null; }
    stopMergePolling();
    clearMergeState(recording.camera_id);
    loadRecording();
    showToast(t('detail.mergeCancelled'), 'success');
  } catch (e) {
    showToast(e instanceof Error ? e.message : 'Failed to cancel merge', 'error');
  }
}

// --- Merge ETA ---
function updateMergeEta() {
  if (!mergeStartTime || mergeProgressPct <= 0) {
    mergeEta = '';
    return;
  }
  const elapsed = Date.now() - mergeStartTime;
  if (elapsed < 1000) {
    mergeEta = '';
    return;
  }
  const totalEstimate = elapsed / mergeProgressPct * 100;
  const remaining = totalEstimate - elapsed;
  if (remaining < 60000) {
    mergeEta = '< 1min';
  } else {
    const mins = Math.floor(remaining / 60000);
    const secs = Math.floor((remaining % 60000) / 1000);
    mergeEta = `~${mins}m ${secs}s`;
  }
}

// --- Timelapse Preview ---
async function loadTimelapsePreview() {
  if (!recording || recording.format !== 'timelapse') return;
  timelapsePreviewLoading = true;
  timelapsePreviewError = '';
  try {
    timelapsePreviewFrames = await fetchTimelapsePreview(currentId, 6);
  } catch (e) {
    timelapsePreviewError = e instanceof Error ? e.message : 'Failed to load preview';
    timelapsePreviewFrames = [];
  } finally {
    timelapsePreviewLoading = false;
  }
}
  // --- Timelapse JPEG sequence player ---

  async function initTimelapsePlayer() {
    tlLoading = true;
    tlError = '';
    tlIsPlaying = false;
    tlCurrentFrame = 0;
    stopTimelapsePlayback();
    // Abort any in-flight requests from previous recording
    tlAbortController?.abort();
    tlAbortController = new AbortController();
    const signal = tlAbortController.signal;
    // Clear old blob URLs
    tlBlobCache.forEach(url => URL.revokeObjectURL(url));
    tlBlobCache = new Map();
    try {
      timelapseFrames = await getTimelapseFrames(currentId, signal);
      if (timelapseFrames.length > 0) {
        await ensureFrameCached(0, signal);
        prefetchAhead(0, signal);
      }
    } catch (e) {
      if (signal.aborted) return; // cancelled, not an error
      console.error('Failed to load timelapse frames:', e);
      tlError = t('detail.failedLoadVideo');
      timelapseFrames = [];
    } finally {
      tlLoading = false;
    }
  }

  async function ensureFrameCached(index: number, signal?: AbortSignal) {
    if (tlBlobCache.has(index) || !timelapseFrames[index]) return;
    if (signal?.aborted) return;
    try {
      const blobUrl = await loadTimelapseFrameBlob(currentId, timelapseFrames[index].filename, signal);
      if (signal?.aborted) return; // aborted while waiting
      tlBlobCache.set(index, blobUrl);
      // Evict old cache entries when over 500 limit
      if (tlBlobCache.size >= 500) {
        const keys = [...tlBlobCache.keys()].sort((a, b) => a - b);
        const toEvict = keys.slice(0, keys.length - 400);
        for (const k of toEvict) {
          const url = tlBlobCache.get(k);
          if (url) URL.revokeObjectURL(url);
          tlBlobCache.delete(k);
        }
      }
    } catch (e) {
      if (signal?.aborted) return;
      console.warn('Failed to load timelapse frame:', index, e);
    }
  }

  async function prefetchAhead(fromIndex: number, signal?: AbortSignal) {
    const windowSize = 200;
    const batchSize = 20;
    const end = Math.min(fromIndex + windowSize, timelapseFrames.length);
    for (let i = fromIndex; i < end; i += batchSize) {
      if (signal?.aborted) return;
      const batch = [];
      for (let j = i; j < Math.min(i + batchSize, end); j++) {
        if (!tlBlobCache.has(j)) {
          batch.push(ensureFrameCached(j, signal));
        }
      }
      await Promise.all(batch);
    }
  }

  function stopTimelapsePlayback() {
    if (tlPlayTimeout) {
      clearTimeout(tlPlayTimeout);
      tlPlayTimeout = null;
    }
  }

  function playNextFrame() {
    if (!tlIsPlaying) return;
    const signal = tlAbortController?.signal;
    if (signal?.aborted) return;
    const next = tlCurrentFrame + 1;
    if (next >= timelapseFrames.length) {
      if (tlLoop) {
        tlCurrentFrame = 0;
        tlPlayTimeout = setTimeout(playNextFrame, 50);
        return;
      }
      tlIsPlaying = false;
      // Auto-advance to next recording
      navigateToNext();
      return;
    }
    tlCurrentFrame = next;
    const loadPromise = tlBlobCache.has(next)
      ? Promise.resolve()
      : ensureFrameCached(next, signal);
    prefetchAhead(next + 1, signal);
    loadPromise.then(() => {
      if (signal?.aborted) return;
      const fps = 10 * tlSpeed;
      const delay = Math.max(0, (1000 / fps) - 10);
      tlPlayTimeout = setTimeout(playNextFrame, delay);
    });
  }

  function tlTogglePlay() {
    if (tlIsPlaying) {
      tlIsPlaying = false;
      stopTimelapsePlayback();
    } else {
      if (timelapseFrames.length === 0) return;
      tlIsPlaying = true;
      stopTimelapsePlayback();
      playNextFrame();
    }
  }

  function tlSetSpeed(speed: number) {
    tlSpeed = speed;
  }

  function tlSeek(index: number) {
    const target = Math.max(0, Math.min(index, timelapseFrames.length - 1));
    tlCurrentFrame = target;
    const signal = tlAbortController?.signal;
    if (!tlBlobCache.has(target)) {
      // Show spinner only if loading takes >500ms
      tlSeekTimeout = setTimeout(() => { tlSeekLoading = true; }, 500);
      ensureFrameCached(target, signal).finally(() => {
        if (tlSeekTimeout) { clearTimeout(tlSeekTimeout); tlSeekTimeout = null; }
        tlSeekLoading = false;
      });
    }
    prefetchAhead(target + 1, signal);
  }

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

  async function handleDownload() {
    if (isDownloading || !recording) return;
    isDownloading = true;
    downloadProgress = 0;
    try {
      await apiDownloadRecording(recording.id, (loaded, total) => {
        downloadProgress = Math.round((loaded / total) * 100);
      });
    } catch (e) { console.error('Download failed:', e); }
    finally { isDownloading = false; downloadProgress = 0; }
  }

  // --- Transcoding ---
  async function loadTranscodingStatus() {
    try {
      transcodingStatus = await getTranscodingStatus();
    } catch (e) {
      // Silently fail — not critical
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

  function findTranscodeTask(): TranscodeTask | undefined {
    if (!transcodingStatus?.recent_results) return undefined;
    return transcodingStatus.recent_results.find(
      (t) => t.recording_id === currentId
    );
  }

  async function handleTranscode() {
    if (!recording) return;
    const targetCodec = recording.format === 'h264' ? 'h265' : recording.format === 'h265' ? 'h264' : 'h264';
    try {
      await enqueueTranscodeTask({
        camera_id: recording.camera_id,
        recording_id: recording.id,
        target_codec: targetCodec,
        replace_original: false,
      });
      showToast(t('transcoding.recordings.transcodeSuccess', { camera: recording.camera_id }), 'success');
      loadTranscodingStatus();
    } catch (e) {
      showToast(t('transcoding.recordings.transcodeFailed'), 'error');
    }
  }
  function handleKeydown(e: KeyboardEvent) {
    const tag = (e.target as HTMLElement).tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
    switch (e.key) {
      case ' ':
        e.preventDefault();
        if (recording?.format === 'mjpeg') mjpegPlayer?.handleKeyAction('togglePlay');
        else if (recording?.format === 'timelapse') tlTogglePlay();
        else if (recording?.format === 'h264' || recording?.format === 'h265') {
          const video = document.querySelector('video');
          if (video) { if (video.paused) video.play(); else video.pause(); }
        }
        break;
      case 'ArrowLeft':
        e.preventDefault();
        if (recording?.format === 'mjpeg') mjpegPlayer?.handleKeyAction('prevFrame');
        else if (recording?.format === 'timelapse') tlSeek(tlCurrentFrame - 1);
        else { const v = document.querySelector('video'); if (v) v.currentTime = Math.max(0, v.currentTime - 5); }
        break;
      case 'ArrowRight':
        e.preventDefault();
        if (recording?.format === 'mjpeg') mjpegPlayer?.handleKeyAction('nextFrame');
        else if (recording?.format === 'timelapse') tlSeek(tlCurrentFrame + 1);
        else { const v = document.querySelector('video'); if (v) v.currentTime = Math.min(v.duration, v.currentTime + 5); }
        break;
      case 'Escape':
        if (document.fullscreenElement) { document.exitFullscreen(); break; }
        goBack();
        break;
      case 'f':
      case 'F':
        e.preventDefault();
        if (recording?.format === 'mjpeg') mjpegPlayer?.handleKeyAction('toggleFullscreen');
        else if (recording?.format === 'timelapse') toggleFullscreen();
        else toggleVideoFullscreen();
        break;
      case 'l':
      case 'L':
        e.preventDefault();
        if (recording?.format === 'mjpeg') mjpegPlayer?.handleKeyAction('toggleLoop');
        else if (recording?.format === 'timelapse') tlToggleLoop();
        else if (recording?.format === 'h264' || recording?.format === 'h265') toggleVideoLoop();
        break;
      case 'Home':
        e.preventDefault();
        if (recording?.format === 'mjpeg') mjpegPlayer?.handleKeyAction('home');
        else if (recording?.format === 'timelapse') tlSeek(0);
        else if (recording?.format === 'h264' || recording?.format === 'h265') setVideoSpeed(1);
        break;
    }
  }
  function tlToggleLoop() {
    tlLoop = !tlLoop;
  }

  function toggleFullscreen() {
    const el = document.querySelector('.timelapse-container');
    if (!el) return;
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else {
      el.requestFullscreen();
    }
  }

  function toggleVideoFullscreen() {
    const el = document.querySelector('.video-container');
    if (!el) return;
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else {
      el.requestFullscreen();
    }
  }

  $effect(() => {
    function onFSChange() {
      videoFullscreen = !!document.fullscreenElement;
    }
    document.addEventListener('fullscreenchange', onFSChange);
    return () => {
      document.removeEventListener('fullscreenchange', onFSChange);
    };
  });


  function getFrameTimestamp(): string {
    if (!recording || !timelapseFrames[tlCurrentFrame]) return '';
    const start = new Date(recording.started_at).getTime();
    const frame = timelapseFrames[tlCurrentFrame];
    // Use frame.timestamp if available, otherwise estimate from index
    if (frame.timestamp) {
      const ts = new Date(frame.timestamp).getTime();
      const diff = Math.max(0, ts - start);
      const totalSec = Math.floor(diff / 1000);
      const h = Math.floor(totalSec / 3600);
      const m = Math.floor((totalSec % 3600) / 60);
      const s = totalSec % 60;
      return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
    }
    // Fallback: estimate from frame index
    const totalFrames = timelapseFrames.length;
    const durationMs = recording.duration * 1000;
    const estimatedSec = Math.floor((tlCurrentFrame / Math.max(1, totalFrames)) * (durationMs / 1000));
    const h = Math.floor(estimatedSec / 3600);
    const m = Math.floor((estimatedSec % 3600) / 60);
    const s = estimatedSec % 60;
    return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  }

  $effect(() => {
    return () => {
      // Save current merge state before cleanup so other pages can pick it up
      if (mergeInProgress && recording) {
        saveMergeState({
          cameraId: recording.camera_id,
          recordingId: recording.id,
          progress: mergeProgressPct,
          status: 'pending',
        });
      }
      // Abort all in-flight timelapse frame requests
      tlAbortController?.abort();
      tlAbortController = null;
      // Abort merge SSE connection (merge continues on server, progress in DB)
      mergeAbortController?.abort();
      mergeAbortController = null;
      // Clear merge reconnect timer
      if (mergeReconnectTimer) { clearTimeout(mergeReconnectTimer); mergeReconnectTimer = null; }
      tlBlobCache.forEach(url => URL.revokeObjectURL(url));
      tlBlobCache = new Map();
      stopTimelapsePlayback();
      // Clear format badge auto-hide timeout
      if (formatBadgeTimeout) { clearTimeout(formatBadgeTimeout); formatBadgeTimeout = null; }
      if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
    };
  });

// --- MJPEG player auto-init when component mounts ---

$effect(() => {
  if (recording?.format === 'mjpeg' && mjpegPlayer) {
    mjpegPlayer.initPlayer();
  }
});

  // Reactively reload when recordingId prop changes (handles SPA navigation between recordings)
  $effect(() => {
    const id = recordingId;
    if (!id) return;
    if (currentId === id && recording) return; // already loaded this recording
    currentId = id;
    loading = true;
    error = '';
    loadRecording();
  });

  onMount(() => {
    startTranscodingPoll();
    window.addEventListener('keydown', handleKeydown);
    return () => {
      window.removeEventListener('keydown', handleKeydown);
      stopTranscodingPoll();
      stopMergePolling();
    };
  });
</script>
<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Loading state -->
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
          {#if recording.format === 'h264' || recording.format === 'h265' || (recording.format === 'timelapse' && recording.merge_status === 'merged')}
            <div role="presentation"
              class="video-container relative max-w-full bg-black rounded-t-[var(--radius-md)]"
              onmouseenter={handleVideoContainerMouseEnter}
              onmouseleave={handleVideoContainerMouseLeave}>
              {#if isTransitioning}
                <div class="absolute inset-0 bg-black/60 flex items-center justify-center z-10">
                  <Loader2 size={32} class="animate-spin th-text-secondary" />
                </div>
              {/if}
              {#if videoUrl}
                <video bind:this={videoEl} preload="metadata" controlsList="nodownload" class="w-full max-h-[80vh]" src={videoUrl}
                  onended={handleVideoEnded} ontimeupdate={handleTimeUpdate} onplay={handleVideoPlay} onpause={handleVideoPause}
                  onloadedmetadata={handleVideoLoadedMetadata} onprogress={handleVideoProgress} onloadeddata={handleVideoLoadedData}
                  onerror={handleVideoError} onwaiting={handleVideoWaiting} oncanplay={handleVideoCanPlay} onplaying={handleVideoPlaying}>
                  <track kind="captions" />
                  {t('detail.videoUnsupported')}
                </video>
                {#if videoLoading}
                  <div class="absolute inset-0 skeleton-shimmer" style="border-radius: var(--radius-md) var(--radius-md) 0 0;"></div>
                {/if}
                {#if videoError}
                <div class="absolute inset-0 bg-black/80 flex flex-col items-center justify-center z-20 p-6">
                  <AlertTriangle size={48} class="th-color-danger mb-3" />
                  <p class="text-white text-center text-sm mb-4">{videoErrorMsg}</p>
                  {#if videoRetryCount < MAX_VIDEO_RETRIES}
                    <button onclick={handleVideoRetry} class="btn btn-primary btn-sm flex items-center gap-1">
                      <RefreshCw size={14} />
                      {videoRetryCount > 0 ? t('detail.videoRetrying', { count: String(videoRetryCount), max: String(MAX_VIDEO_RETRIES) }) : t('common.retry')}
                    </button>
                  {:else}
                    <p class="text-white/70 text-xs mb-3">{t('detail.videoMaxRetries')}</p>
                    <button onclick={goBack} class="btn btn-secondary btn-sm">{t('detail.goBack')}</button>
                  {/if}
                </div>
                {:else if videoStalled}
                <div class="absolute inset-0 bg-black/40 flex items-center justify-center z-20">
                  <div class="flex items-center gap-2 text-white/80">
                    <Loader2 size={20} class="animate-spin" />
                    <span class="text-sm">{t('detail.videoBuffering')}</span>
                  </div>
                </div>
                {/if}
              {:else if !loading}
                <div class="flex items-center justify-center h-64 th-text-muted">{t('detail.failedLoadVideo')}</div>
              {/if}

            <!-- Format badge overlay -->
            <div class="absolute top-2 left-2 z-10 pointer-events-none transition-opacity duration-300 ease-in-out"
              style="opacity: {formatBadgeVisible ? 1 : 0};">
              <span class="badge text-[10px] leading-none py-0.5 px-1.5 {formatBadgeClass}">
                {formatLabel}
              </span>
            </div>
            </div>
            <VideoPlaybackControls
              currentTime={videoCurrentTime}
              duration={videoDuration}
              isPlaying={videoIsPlaying}
              playbackRate={videoSpeed}
              buffered={videoBuffered}
              isLooping={videoLoop}
              ontoggleplay={() => { if (videoEl) { if (videoEl.paused) videoEl.play(); else videoEl.pause(); } }}
              onseek={(ratio) => { if (videoEl) videoEl.currentTime = ratio * videoDuration; }}
              onsetspeed={(speed) => setVideoSpeed(speed)}
              onfullscreen={toggleVideoFullscreen}
              ontoggleloop={toggleVideoLoop}
              onarrowleft={() => { if (videoEl) videoEl.currentTime = Math.max(0, videoEl.currentTime - 5); }}
              onarrowright={() => { if (videoEl) videoEl.currentTime = Math.min(videoEl.duration, videoEl.currentTime + 5); }}
            />
            <div class="flex items-center justify-between px-4 py-2 th-bg-secondary border-t th-border">
              <span class="text-sm th-text-muted">{t('detail.playing')} <span class="font-mono th-text-primary">{recording.camera_id}</span></span>
              <button onclick={navigateToNext} class="btn btn-ghost btn-sm flex items-center gap-1">
                {t('detail.nextRecording')} <SkipForward size={16} />
              </button>
            </div>
          {/if}
          {#if recording.format === 'timelapse' && recording.merge_status !== 'merged'}
            <!-- Timelapse JPEG sequence player -->
            {#if tlLoading}
              <div class="flex items-center justify-center h-64 bg-black">
                <div class="spinner spinner-lg"></div>
                <span class="th-text-muted ml-3">{t('detail.loadingFrames')}</span>
              </div>
            {:else if tlError}
              <div class="flex items-center justify-center h-64 bg-black">
                <div class="text-center th-text-muted">
                  <AlertTriangle size={48} class="mx-auto mb-2" />
                  <p>{tlError}</p>
                </div>
              </div>
            {:else if timelapseFrames.length === 0}
              <div class="flex items-center justify-center h-64 bg-black">
                <div class="text-center th-text-muted">
                  <HelpCircle size={48} class="mx-auto mb-2" />
                  <p>{t('detail.noFrames')}</p>
                </div>
              </div>
            {:else}
              <!-- Frame display -->
              <div class="timelapse-container relative max-h-[75vh] overflow-hidden flex items-center justify-center bg-black min-h-[200px]">
                {#if timelapseFrames[tlCurrentFrame]}
                  {@const frame = timelapseFrames[tlCurrentFrame]}
                  {#if tlBlobCache.has(tlCurrentFrame)}
                    <img
                      src={tlBlobCache.get(tlCurrentFrame)}
                      alt={frame.filename}
                      class="max-w-full max-h-[75vh]"
                      style="transition: opacity 0.2s ease-in-out"
                    />
                  {:else if tlCurrentFrame > 0 && tlBlobCache.has(tlCurrentFrame - 1)}
                    <!-- Show previous frame while loading, with fade -->
                    <img
                      src={tlBlobCache.get(tlCurrentFrame - 1)}
                      alt={frame.filename}
                      class="max-w-full max-h-[75vh] opacity-50"
                      style="transition: opacity 0.3s ease-in-out"
                    />
                    {#if tlSeekLoading}
                      <div class="absolute inset-0 flex items-center justify-center bg-black/30">
                        <div class="spinner spinner-lg"></div>
                      </div>
                    {/if}
                  {:else}
                    <div class="flex items-center justify-center h-64 bg-black">
                      <div class="spinner spinner-lg"></div>
                    </div>
                  {/if}
                {/if}
              </div>
              <!-- Timelapse preview thumbnails -->
              {#if timelapsePreviewLoading}
                <div class="th-bg-secondary px-4 py-3 border-t th-border text-center">
                  <span class="text-xs th-text-secondary">{t('common.loading')}</span>
                </div>
              {:else if timelapsePreviewFrames.length > 0}
                <div class="th-bg-secondary px-4 py-3 border-t th-border">
                  <p class="text-xs th-text-muted mb-2">{t('detail.timelapsePreview')}</p>
                  <div class="grid grid-cols-6 gap-1">
                    {#each timelapsePreviewFrames as frame}
                      <img src={frame.url} alt={frame.filename} class="w-full h-16 object-cover rounded" loading="lazy" />
                    {/each}
                  </div>
                </div>
              {/if}

              <!-- Merge controls -->
              {#if recording.merge_status !== 'merged' && !mergeInProgress}
                <div class="th-bg-secondary px-4 py-3 border-t th-border">
                  <div class="flex items-center justify-center gap-3">
                    <select
                      class="input text-sm py-1 w-auto"
                      value={selectedMergeDuration}
                      onchange={(e) => selectedMergeDuration = (e.target as HTMLSelectElement).value}
                    >
                      <option value="8h">{t('timelapse.mergeDuration8h')}</option>
                      <option value="12h">{t('timelapse.mergeDuration12h')}</option>
                      <option value="24h">{t('timelapse.mergeDuration24h')}</option>
                      <option value="natural-day">{t('timelapse.mergeDurationNaturalDay')}</option>
                      <option value="7d">{t('timelapse.mergeDuration7d')}</option>
                      <option value="30d">{t('timelapse.mergeDuration30d')}</option>
                    </select>
                    <button onclick={handleMergeAndPlay} class="btn btn-primary flex items-center gap-2">
                      <Play size={16} /> {t('detail.mergeAndPlay')}
                    </button>
                  </div>
                </div>
              {/if}
              {#if mergeInProgress}
                <div class="th-bg-secondary px-4 py-3 border-t th-border">
                  <div class="flex items-center gap-3 justify-center flex-wrap">
                    <div class="w-32 h-1.5 rounded-full th-bg-tertiary overflow-hidden">
                      <div class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500" style="width: {mergeProgressPct}%"></div>
                    </div>
                    <span class="text-xs th-text-secondary">{t('detail.mergingProgress', { percent: String(mergeProgressPct) })}</span>
                    {#if mergeEta}
                      <span class="text-xs th-text-muted">{mergeEta}</span>
                    {/if}
                    <button
                      onclick={() => cancelMergeConfirm = true}
                      class="btn btn-ghost btn-xs text-xs th-color-danger"
                    >
                      {t('detail.cancelMerge')}
                    </button>
                  </div>
                </div>
              {/if}
              {#if mergeErrorMsg}
                <div class="th-bg-secondary px-4 py-3 border-t th-border text-center">
                  <div class="flex items-center gap-3 justify-center">
                    <span class="text-xs th-color-danger">{t('detail.mergeFailed', { error: mergeErrorMsg })}</span>
                    <button onclick={handleMergeAndPlay} class="btn btn-secondary btn-sm">{t('detail.mergeRetry')}</button>
                  </div>
                </div>
              {/if}

              <!-- Controls -->
              <div class="th-bg-secondary px-4 py-3 space-y-2">
                <!-- Progress bar -->
                <div
                  class="relative h-2 th-bg-tertiary rounded cursor-pointer group"
                  role="slider"
                  tabindex="0"
                  aria-label={t('detail.frameCounter', { current: String(tlCurrentFrame + 1), total: String(timelapseFrames.length) })}
                  aria-valuenow={tlCurrentFrame}
                  aria-valuemin={0}
                  aria-valuemax={timelapseFrames.length - 1}
                  onclick={(e) => {
                    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
                    const ratio = (e.clientX - rect.left) / rect.width;
                    tlSeek(Math.round(ratio * (timelapseFrames.length - 1)));
                  }}
                  onkeydown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); }
                    else if (e.key === 'ArrowLeft') { e.preventDefault(); tlSeek(tlCurrentFrame - 1); }
                    else if (e.key === 'ArrowRight') { e.preventDefault(); tlSeek(tlCurrentFrame + 1); }
                  }}
                >
                  <div
                    class="absolute top-0 left-0 h-full th-bg-accent rounded group-hover:th-bg-info transition-colors"
                    style="width: {timelapseFrames.length > 1 ? (tlCurrentFrame / (timelapseFrames.length - 1)) * 100 : 100}%"
                  ></div>
                  <div
                    class="absolute top-1/2 -translate-y-1/2 w-3 h-3 th-bg-info rounded-full shadow group-hover:th-bg-accent transition-colors"
                    style="left: calc({timelapseFrames.length > 1 ? (tlCurrentFrame / (timelapseFrames.length - 1)) * 100 : 100}% - 6px)"
                  ></div>
                </div>

                <!-- Control buttons -->
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-2">
                    <button
                      onclick={() => tlSeek(tlCurrentFrame - 1)}
                      disabled={tlCurrentFrame === 0 || tlIsPlaying}
                      class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
                      style="color: {tlCurrentFrame === 0 || tlIsPlaying ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {tlCurrentFrame === 0 || tlIsPlaying ? 'transparent' : 'var(--bg-tertiary)'}"
                    >
                      <ChevronLeft size={16} />
                    </button>

                    <button
                      onclick={tlTogglePlay}
                      class="px-4 py-1.5 rounded text-sm font-medium text-white transition-colors flex items-center gap-1"
                      style="background-color: {tlIsPlaying ? 'var(--color-danger)' : 'var(--color-info)'}"
                    >
                      {#if tlIsPlaying}
                        <Pause size={14} /> {t('detail.pause')}
                      {:else}
                        <Play size={14} /> {t('detail.play')}
                      {/if}
                    </button>

                    <button
                      onclick={() => tlSeek(tlCurrentFrame + 1)}
                      disabled={tlCurrentFrame >= timelapseFrames.length - 1 || tlIsPlaying}
                      class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
                      style="color: {tlCurrentFrame >= timelapseFrames.length - 1 || tlIsPlaying ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {tlCurrentFrame >= timelapseFrames.length - 1 || tlIsPlaying ? 'transparent' : 'var(--bg-tertiary)'}"
                    >
                      <ChevronRight size={16} />
                    </button>
                  </div>

                  <!-- Frame counter + timestamp -->
                  <div class="flex items-center gap-3">
                    <span class="th-text-secondary text-sm font-mono">
                      {tlCurrentFrame + 1} / {timelapseFrames.length}
                    </span>
                    <span class="th-text-tertiary text-xs font-mono">
                      {getFrameTimestamp()}
                    </span>
                  </div>

                  <!-- Speed control -->
                  <div class="flex items-center gap-1">
                    <span class="th-text-tertiary text-xs mr-1">{t('detail.speed')}</span>
                    {#each tlSpeeds as speed}
                      <button
                        onclick={() => tlSetSpeed(speed)}
                        class="px-2 py-1 rounded text-xs font-medium transition-colors"
                        style="background-color: {tlSpeed === speed ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {tlSpeed === speed ? 'white' : 'var(--text-secondary)'}"
                      >
                        {speed}x
                      </button>
                    {/each}
                  </div>
                  <div class="flex items-center gap-2">
                    <!-- Loop toggle -->
                    <button
                      onclick={tlToggleLoop}
                      class="px-2 py-1 rounded text-xs font-medium transition-colors"
                      style="background-color: {tlLoop ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {tlLoop ? 'white' : 'var(--text-secondary)'}"
                      title="Loop playback"
                    >
                      {#if tlLoop}
                        🔁 Loop
                      {:else}
                        🔁 Loop
                      {/if}
                    </button>
                    <!-- Fullscreen button -->
                    <button
                      onclick={toggleFullscreen}
                      class="px-2 py-1 rounded text-xs font-medium transition-colors th-bg-tertiary th-text-secondary"
                      title={t('live.fullscreen')}
                    >
                      ⛶ {t('live.fullscreen')}
                    </button>
                  </div>
                </div>
              </div>

              <!-- Keyboard shortcuts hint -->
              <div class="px-4 py-2 th-bg-tertiary">
                <p class="text-xs text-center th-text-muted">
                  {t('detail.spacePlayPause')} | {t('detail.arrowSeek')} | Home {t('detail.homeReset')} | F {t('live.fullscreen')} | L {t('detail.loop')} | {t('detail.escapeBack')}
                </p>
              </div>
            {/if}
          {/if}
          {#if recording.format === 'mjpeg'}
            <MjpegPlayer bind:this={mjpegPlayer} recordingId={currentId} oninitdone={() => {}} />
            <!-- Keyboard shortcuts hint -->
            <div class="px-4 py-2 th-bg-tertiary">
              <p class="text-xs text-center th-text-muted">
                {t('detail.spacePlayPause')} | {t('detail.arrowSeek')} | Home {t('detail.homeReset')} | F {t('live.fullscreen')} | L {t('detail.loop')} | {t('detail.escapeBack')}
              </p>
            </div>
          {:else if recording.format !== 'h264' && recording.format !== 'h265' && recording.format !== 'timelapse'}
            <div class="flex items-center justify-center h-64 bg-black">
              <div class="text-center th-text-tertiary">
                <div class="text-4xl mb-2 flex justify-center"><HelpCircle size={48} /></div>
                <p class="text-lg">{t('detail.unsupportedFormat')}</p>
                <p class="text-sm mt-2">{t('detail.format')}: {recording.format}</p>
              </div>
            </div>
          {/if}
        </div>

        <!-- Recording info -->
        <div class="card p-6 border th-border">
          <div class="flex items-start justify-between mb-6">
            <div>
              <h2 class="text-2xl font-bold th-text-primary mb-2">
                {recording.camera_id}
              </h2>
              <p class="th-text-tertiary">
                {formatDate(recording.started_at)}
              </p>
            </div>
            <div class="flex gap-2">
              {#if recording.merged}
                <span class="badge badge-success">{t('recordings.merged')}</span>
              {:else}
                <span class="badge badge-neutral">{t('recordings.originalSegment')}</span>
              {/if}
              <span class="badge {recording.format === 'timelapse' ? 'badge-info' : 'badge-neutral'}">
                {recording.format === 'timelapse'
                  ? t('recording.format.timelapse')
                  : (recording.format === 'h264' || recording.format === 'h265')
                    ? t('recording.format.h264')
                    : t('recording.format.mjpeg')}
              </span>
              {#if recording.format === 'timelapse' && recording.merge_status}
                <span class="badge {recording.merge_status === 'merged' ? 'badge-success' : recording.merge_status === 'failed' ? 'badge-error' : mergeInProgress ? 'badge-info' : 'badge-neutral'}">
                  {recording.merge_status === 'merged' ? t('detail.mergeStatusMerged') : recording.merge_status === 'failed' ? t('detail.mergeStatusFailed') : mergeInProgress ? t('detail.mergeStatusMerging', { percent: String(mergeProgressPct) }) : t('detail.mergeStatusPending')}
                </span>
              {/if}
            </div>
          </div>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-6 mb-8">
            <div>
              <p class="text-sm th-text-tertiary mb-1">{t('detail.duration')}</p>
              <p class="text-lg font-semibold th-text-body">{formatDuration(recording.duration)}</p>
            </div>
            <div>
              <p class="text-sm th-text-tertiary mb-1">{t('detail.fileSize')}</p>
              <p class="text-lg font-semibold th-text-body">{formatFileSize(recording.file_size)}</p>
            </div>
            <div>
              <p class="text-sm th-text-tertiary mb-1">{t('detail.frames')}</p>
              <p class="text-lg font-semibold th-text-body">{recording.frame_count.toLocaleString()}</p>
            </div>
            <div>
              <p class="text-sm th-text-tertiary mb-1">{t('detail.endTime')}</p>
              <p class="text-lg font-semibold th-text-body">{formatDate(recording.ended_at)}</p>
            </div>
          </div>

          <!-- Actions -->
          <div class="flex flex-wrap gap-3 border-t th-border pt-6">
            <div class="flex flex-wrap gap-3">
              {#if isDownloading}
                <button disabled class="btn btn-primary opacity-75 flex items-center gap-2">
                  <div class="spinner spinner-sm"></div>
                  {downloadProgress}%
                </button>
              {:else}
                <button onclick={handleDownload} class="btn btn-primary">
                  {t('detail.download')}
                </button>
              {/if}
              {#if recording.format === 'timelapse' && recording.merge_status !== 'merged' && !mergeInProgress}
                <button onclick={handleMergeAndPlay} class="btn btn-primary">
                  {t('detail.mergeAndPlay')}
                </button>
              {/if}
              {#if mergeInProgress}
                <div class="flex items-center gap-3">
                  <div class="flex-1 h-1.5 rounded-full th-bg-tertiary overflow-hidden">
                    <div class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500" style="width: {mergeProgressPct}%"></div>
                  </div>
                  <span class="text-xs th-text-secondary">{t('detail.mergingProgress', { percent: String(mergeProgressPct) })}</span>
                  {#if mergeEta}
                    <span class="text-xs th-text-muted">{mergeEta}</span>
                  {/if}
                  <button
                    onclick={() => cancelMergeConfirm = true}
                    class="btn btn-ghost btn-xs th-color-danger"
                  >
                    {t('detail.cancelMerge')}
                  </button>
                </div>
              {/if}
              {#if mergeErrorMsg}
                <div class="flex items-center gap-3">
                  <span class="text-xs th-color-danger">{t('detail.mergeFailed', { error: mergeErrorMsg })}</span>
                  <button onclick={handleMergeAndPlay} class="btn btn-secondary btn-sm">{t('detail.mergeRetry')}</button>
                </div>
              {/if}
              {#if transcodingStatus?.enabled && !transcodeTask}
                <button onclick={handleTranscode} class="btn btn-secondary" title={t('transcoding.recordings.transcodeBtn')}>
                  <RefreshCw size={16} />
                  {t('transcoding.recordings.transcodeBtn')}
                </button>
              {/if}
            </div>
            <div class="flex gap-3 ml-auto">
              <button
                onclick={() => deleteConfirm = true}
                class="btn btn-danger"
              >
                {t('detail.delete')}
              </button>
            </div>
          </div>

          {#if transcodingStatus?.enabled && transcodeTask}
            <div class="mt-3 border-t th-border pt-4">
              {#if transcodeTask.status === 'running' || transcodeTask.status === 'pending'}
                <div class="flex items-center gap-3">
                  <span class="badge bg-blue-100 text-blue-800 dark:bg-blue-900/50 dark:text-blue-300 animate-pulse text-xs">{t('transcoding.running')}</span>
                  <span class="text-xs th-text-secondary">
                    {t('transcoding.recordings.transcodingProgress', { percent: String(transcodeTask.progress ?? 0) })}
                  </span>
                  <div class="flex-1 h-1.5 rounded-full th-bg-tertiary overflow-hidden">
                    <div
                      class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500"
                      style="width: {Math.max(transcodeTask.progress ?? 0, 2)}%"
                    ></div>
                  </div>
                </div>
              {:else if transcodeTask.status === 'completed'}
                <div class="flex items-center gap-2">
                  <span class="badge badge-success text-xs">{t('transcoding.completed')}</span>
                  <span class="text-xs th-text-secondary">{t('transcoding.queue.codecConversion', { input: transcodeTask.input_format?.toUpperCase() || '?', output: transcodeTask.output_format?.toUpperCase() || '?' })}</span>
                </div>
              {:else if transcodeTask.status === 'failed'}
                <div class="flex items-center gap-2">
                  <span class="badge badge-danger text-xs">{t('transcoding.failed')}</span>
                  <span class="text-xs th-text-secondary">{transcodeTask.error || ''}</span>
                </div>
              {:else}
                <div class="flex items-center gap-2">
                  <span class="badge badge-neutral text-xs">{t('transcoding.pending')}</span>
                </div>
              {/if}
            </div>
          {/if}
        </div>
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
          <button
            onclick={() => deleteConfirm = false}
            class="btn btn-secondary"
          >
            {t('detail.cancel')}
          </button>
          <button
            onclick={confirmDelete}
            class="btn btn-danger"
          >
            {t('detail.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Cancel merge confirmation dialog -->
  {#if cancelMergeConfirm}
    <ConfirmDialog
      title={t('detail.cancelMerge')}
      message={t('detail.cancelMergeConfirm')}
      onconfirm={handleCancelMerge}
      oncancel={() => cancelMergeConfirm = false}
      confirmText={t('detail.cancelMerge')}
      variant="danger"
    />
  {/if}
</div>
