<script lang="ts">
  import { onMount, tick } from 'svelte';
  import {
    getRecording,
    deleteRecording,
    downloadRecording as apiDownloadRecording,
    loadRecordingVideoBlob,
    listRecordings,
    getTimelapseFrames,
    loadTimelapseFrameBlob,
    triggerTimelapseMerge,
    subscribeTimelapseMergeProgress
  } from '$lib/api';
  import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import type { Recording, TimelapseFrame } from '$lib/api';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { AlertTriangle, HelpCircle, SkipForward, Loader2, RefreshCw, Play, Pause, ChevronLeft, ChevronRight } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import MjpegPlayer from '$lib/components/MjpegPlayer.svelte';
  import { showToast } from '$lib/toast';

  let { recordingId = '' } = $props();
  let currentId = $state('');
  let recording = $state<Recording | null>(null);
  let loading = $state(true);
  let error = $state('');
  let deleteConfirm = $state(false);
  let mjpegPlayer: MjpegPlayer | undefined = $state();
  let videoBlobUrl = $state('');
  let videoLoading = $state(false);
  let downloadProgress = $state(0);
  let isDownloading = $state(false);
  let nextRecordingId = $state<string | null>(null);
  let nextBlobUrl = $state<string | null>(null);
  let isTransitioning = $state(false);
  // Transcoding state
  let transcodingStatus = $state<ManagerStatus | null>(null);
  let transcodingPollInterval: ReturnType<typeof setInterval> | null = null;
  let transcodeTask = $derived(findTranscodeTask());

  // Timelapse player state
  let timelapseFrames = $state<TimelapseFrame[]>([]);
  let tlCurrentFrame = $state(0);
  let tlIsPlaying = $state(false);
  let tlSpeed = $state(1);
  let tlLoading = $state(false);
  let tlError = $state('');
  const tlSpeeds = [1, 2, 5, 10, 20, 50];
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

  async function loadRecording() {
    loading = true;
    error = '';
    if (nextBlobUrl) { URL.revokeObjectURL(nextBlobUrl); }
    nextRecordingId = null;
    nextBlobUrl = null;
    try {
      recording = await getRecording(currentId);
      if (recording) {
        if (recording.format === 'mjpeg') {
          await tick();
          if (mjpegPlayer) await mjpegPlayer.initPlayer();
        } else if (recording.format === 'timelapse') {
          // For merged timelapse, use video player instead of JPEG player
          if (recording.merge_status === 'merged') {
            initVideoPlayer();
          } else {
            initTimelapsePlayer();
          }
        } else if (recording.format === 'h264' || recording.format === 'h265') {
          initVideoPlayer();
        }
      }
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadRecording');
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
    const next = await loadNextRecording();
    if (next) { isTransitioning = true; currentId = next.id; await loadRecording(); isTransitioning = false; }
  }

  function handleTimeUpdate(e: Event) {
    const video = e.target as HTMLVideoElement;
    if (video.duration && video.currentTime / video.duration > 0.8 && !nextRecordingId) prefetchNextRecording();
  }

  async function prefetchNextRecording() {
    if (nextRecordingId || !recording) return;
    const next = await loadNextRecording();
    if (next) {
      nextRecordingId = next.id;
      // Only prefetch video blob for MP4 formats (h264/h265)
      if (next.format !== 'timelapse' && next.format !== 'mjpeg') {
        try { nextBlobUrl = await loadRecordingVideoBlob(next.id); }
        catch (e) { console.warn('Failed to prefetch next recording:', e); nextRecordingId = null; }
      }
    }
  }

  async function navigateToNext() {
    const next = await loadNextRecording();
    if (next) { isTransitioning = true; currentId = next.id; await loadRecording(); isTransitioning = false; }
  }

  async function initVideoPlayer() {
    videoLoading = true;
    if (videoBlobUrl) { URL.revokeObjectURL(videoBlobUrl); videoBlobUrl = ''; }
    try { videoBlobUrl = await loadRecordingVideoBlob(currentId); }
    catch (e) { console.error('Failed to load video:', e); error = t('detail.failedLoadVideo'); }
    finally { videoLoading = false; }
  }

async function handleMergeAndPlay() {
  if (!recording) return;
  mergeInProgress = true;
  mergeProgressPct = 0;
  mergeErrorMsg = '';

  try {
    await triggerTimelapseMerge(recording.camera_id);

    const ac = subscribeTimelapseMergeProgress(recording.camera_id, (data) => {
      if (data.status === 'completed') {
        mergeInProgress = false;
        mergeProgressPct = 100;
        mergeAbortController = null;
        loadRecording();
      } else if (data.status === 'failed') {
        mergeInProgress = false;
        mergeErrorMsg = data.error || '';
      } else if (data.progress !== undefined) {
        mergeProgressPct = data.progress;
      }
    });

    mergeAbortController = ac;
  } catch (e) {
    mergeInProgress = false;
    mergeErrorMsg = e instanceof Error ? e.message : 'Failed to start merge';
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
      case 'Escape': goBack(); break;
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
      // Abort all in-flight timelapse frame requests
      tlAbortController?.abort();
      tlAbortController = null;
      // Abort merge SSE connection
      mergeAbortController?.abort();
      mergeAbortController = null;
      if (videoBlobUrl) URL.revokeObjectURL(videoBlobUrl);
      if (nextBlobUrl) URL.revokeObjectURL(nextBlobUrl);
      tlBlobCache.forEach(url => URL.revokeObjectURL(url));
      tlBlobCache = new Map();
      stopTimelapsePlayback();
    };
  });

  onMount(() => {
    currentId = recordingId;
    if (!currentId) { error = t('detail.recordingIdRequired'); loading = false; return; }
    loadRecording();
    startTranscodingPoll();
    window.addEventListener('keydown', handleKeydown);
    return () => {
      window.removeEventListener('keydown', handleKeydown);
      stopTranscodingPoll();
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
          <button onclick={loadRecording} class="btn btn-primary btn-sm flex items-center gap-1">
            <RefreshCw size={14} />
            {t('common.retry')}
          </button>
          <button onclick={goBack} class="btn btn-secondary btn-sm">
            {t('detail.goBack')}
          </button>
        </div>
      </div>
    {:else if recording}
      <div class="space-y-6">
        <!-- Playback section -->
        <div class="card border th-border overflow-hidden">
          {#if recording.format === 'h264' || recording.format === 'h265'}
            <div class="relative max-w-full bg-black rounded-t-[var(--radius-md)]">
              {#if isTransitioning}
                <div class="absolute inset-0 bg-black/60 flex items-center justify-center z-10">
                  <Loader2 size={32} class="animate-spin th-text-secondary" />
                </div>
              {/if}
              {#if videoLoading}
                <div class="flex items-center justify-center h-64"><div class="spinner spinner-lg"></div></div>
              {:else if videoBlobUrl}
                <video controls preload="auto" class="w-full max-h-[80vh]" src={videoBlobUrl}
                  onended={handleVideoEnded} ontimeupdate={handleTimeUpdate}>
                  <track kind="captions" />
                  {t('detail.videoUnsupported')}
                </video>
              {:else}
                <div class="flex items-center justify-center h-64 th-text-muted">{t('detail.failedLoadVideo')}</div>
              {/if}
            </div>
            <div class="flex items-center justify-between px-4 py-2 th-bg-secondary">
              <span class="text-sm th-text-muted">{t('detail.playing')} <span class="font-mono th-text-primary">{recording.camera_id}</span></span>
              <button onclick={navigateToNext} class="btn btn-ghost btn-sm flex items-center gap-1">
                {t('detail.nextRecording')} <SkipForward size={16} />
              </button>
            </div>
          {/if}
          {#if recording.format === 'timelapse'}
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
                  {t('detail.spacePlayPause')} | {t('detail.arrowSeek')} | {t('detail.escapeBack')}
                </p>
              </div>
            {/if}
          {/if}
          {#if recording.format !== 'h264' && recording.format !== 'h265' && recording.format !== 'timelapse'}
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
</div>
