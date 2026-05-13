<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
import {
  getRecording,
  deleteRecording,
  downloadRecording as apiDownloadRecording,
  listFrames,
  loadFrameBlob,
  loadRecordingVideoBlob,
  listRecordings
} from '$lib/api';
  import type { Recording, FrameInfo } from '$lib/api';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { AlertTriangle, HelpCircle, SkipBack, SkipForward, Loader2, RefreshCw } from 'lucide-svelte';
  import { t } from '$lib/i18n';

  // Recording ID passed as prop
  let { recordingId = '' } = $props();
  // Internal navigation ID — allows seamless transitions without page teardown
  let currentId = $state('');
  let recording = $state<Recording | null>(null);
  let loading = $state(true);
  let error = $state('');
  let deleteConfirm = $state(false);

  // JPEG frame player state
  let frames = $state<FrameInfo[]>([]);
  let currentFrameIndex = $state(0);
  let isPlaying = $state(false);
  let playInterval: ReturnType<typeof setInterval> | null = null;
  let playSpeed = $state(1); // multiplier: 1x, 2x, 5x
  let framesLoading = $state(false);
  let preloading = $state(false);
  let preloadProgress = $state(0);

  // Non-reactive internals for smooth playback
  let preloadedImages: (HTMLImageElement | null)[] = [];
  let _playbackFrame = 0;

  // DOM refs for direct manipulation (avoids reactive state on hot path)
  let canvasEl: HTMLCanvasElement | undefined = $state();
  let progressFillEl: HTMLDivElement | undefined = $state();
  let progressThumbEl: HTMLDivElement | undefined = $state();
  let frameCounterEl: HTMLSpanElement | undefined = $state();

  // Video blob URL (for MP4 auth)
  let videoBlobUrl = $state('');
  let videoLoading = $state(false);
  let downloadProgress = $state(0);
  let isDownloading = $state(false);

  // Playlist / continuous playback
  let nextRecordingId = $state<string | null>(null);
  let nextBlobUrl = $state<string | null>(null);
  let isTransitioning = $state(false);

  // MJPEG lazy loading
  const LAZY_BATCH_SIZE = 50;
  const LAZY_WINDOW = 20;
  let loadedFrameRange = $state({ start: 0, end: 0 });
  let moreFramesAvailable = $state(true);


  // Speed options
  const speeds = [1, 2, 5];

  async function loadRecording() {
    loading = true;
    error = '';
    // Reset prefetch state for new recording
    if (nextBlobUrl) { URL.revokeObjectURL(nextBlobUrl); }
    nextRecordingId = null;
    nextBlobUrl = null;

    try {
      recording = await getRecording(currentId);
      // After loading recording, init media
      if (recording) {
        if (recording.format === 'mjpeg') {
          initFramePlayer();
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

  // Frame player initialization
  async function initFramePlayer() {
    framesLoading = true;
    try {
      const resp = await listFrames(currentId);
      frames = resp.frames;
    } catch (e) {
      console.error('Failed to load recording:', e);
      framesLoading = false;
      return;
    }

    if (frames.length > 0) {
      await preloadAllFrames();
    }
    framesLoading = false;
  }

  // Lazy load: only load first batch of frames
  async function preloadAllFrames() {
    framesLoading = false;
    preloading = true;
    preloadProgress = 0;
    preloadedImages = new Array(frames.length).fill(null);

    const end = Math.min(LAZY_BATCH_SIZE, frames.length);
    await loadFrameBatch(0, end);
    loadedFrameRange = { start: 0, end };
    moreFramesAvailable = end < frames.length;
    preloading = false;

    if (preloadedImages[0]) {
      await tick();
      renderFrame(0);
      currentFrameIndex = 0;
    }
  }

  async function loadFrameBatch(start: number, end: number) {
    const batchSize = 5;
    let loaded = 0;
    const total = end - start;

    for (let i = start; i < end; i += batchSize) {
      const batch = frames.slice(i, Math.min(i + batchSize, end));
      const results = await Promise.all(
        batch.map(async (frame) => {
          try {
            const blobUrl = await loadFrameBlob(currentId, frame.index);
            const img = new Image();
            await new Promise<void>((resolve, reject) => {
              img.onload = () => resolve();
              img.onerror = () => reject(new Error(`Failed to load frame ${frame.index}`));
              img.src = blobUrl;
            });
            URL.revokeObjectURL(blobUrl);
            return img;
          } catch {
            return null;
          }
        })
      );
      for (let j = 0; j < results.length; j++) {
        preloadedImages[i + j] = results[j];
      }
      loaded += results.length;
      preloadProgress = Math.round((loaded / total) * 100);
    }
  }

  function ensureFramesLoaded(index: number) {
    if (index >= loadedFrameRange.end - 10 && moreFramesAvailable) {
      const newEnd = Math.min(loadedFrameRange.end + LAZY_BATCH_SIZE, frames.length);
      loadFrameBatch(loadedFrameRange.end, newEnd);
      loadedFrameRange = { ...loadedFrameRange, end: newEnd };
      moreFramesAvailable = newEnd < frames.length;
    }

    // Unload frames far from current position
    const keepStart = Math.max(0, index - LAZY_WINDOW);
    const keepEnd = Math.min(frames.length, index + LAZY_WINDOW + 1);
    for (let i = 0; i < keepStart; i++) {
      preloadedImages[i] = null;
    }
    for (let i = keepEnd; i < frames.length; i++) {
      preloadedImages[i] = null;
    }
  }

  // Draw a frame onto the canvas — no DOM/reactive changes
  function renderFrame(index: number) {
    if (!canvasEl || index < 0 || index >= preloadedImages.length) return;
    const img = preloadedImages[index];
    if (!img) return;
    if (canvasEl.width !== img.naturalWidth || canvasEl.height !== img.naturalHeight) {
      canvasEl.width = img.naturalWidth;
      canvasEl.height = img.naturalHeight;
    }
    const ctx = canvasEl.getContext('2d')!;
    ctx.drawImage(img, 0, 0);
  }

  // Direct DOM updates for playback UI — bypasses Svelte reactivity
  function updatePlaybackUI(index: number) {
    const total = frames.length;
    const progress = total > 1 ? (index / (total - 1)) * 100 : 100;
    if (progressFillEl) {
      progressFillEl.style.width = `${progress}%`;
    }
    if (progressThumbEl) {
      progressThumbEl.style.left = `calc(${progress}% - 6px)`;
    }
    if (frameCounterEl) {
      frameCounterEl.textContent = t('detail.frameCounter', {
        current: String(index + 1),
        total: String(total)
      });
    }
  }

  function prevFrame() {
    const idx = isPlaying ? _playbackFrame : currentFrameIndex;
    if (idx <= 0) return;
    const newIdx = idx - 1;
    currentFrameIndex = newIdx;
    _playbackFrame = newIdx;
    renderFrame(newIdx);
    updatePlaybackUI(newIdx);
  }

  function nextFrame() {
    const idx = isPlaying ? _playbackFrame : currentFrameIndex;
    if (idx >= frames.length - 1) return;
    const newIdx = idx + 1;
    currentFrameIndex = newIdx;
    _playbackFrame = newIdx;
    renderFrame(newIdx);
    updatePlaybackUI(newIdx);
    ensureFramesLoaded(newIdx);
  }

  function togglePlay() {
    if (isPlaying) {
      stopPlaying();
    } else {
      startPlaying();
    }
  }

  function startPlaying() {
    if (frames.length === 0 || preloadedImages.length === 0) return;
    isPlaying = true;
    _playbackFrame = currentFrameIndex;
    const fps = 3 * playSpeed;
    playInterval = setInterval(() => {
      const next = _playbackFrame + 1;
      if (next >= frames.length) {
        stopPlaying();
        return;
      }
      _playbackFrame = next;
      renderFrame(next);
      updatePlaybackUI(next);
      ensureFramesLoaded(next);
    }, 1000 / fps);
  }

  function stopPlaying() {
    isPlaying = false;
    if (playInterval) {
      clearInterval(playInterval);
      playInterval = null;
    }
    // Sync playback position back to reactive state
    currentFrameIndex = _playbackFrame;
  }

  function setSpeed(speed: number) {
    playSpeed = speed;
    if (isPlaying) {
      stopPlaying();
      startPlaying();
    }
  }

  function handleProgressClick(e: MouseEvent) {
    if (frames.length === 0) return;
    const target = e.currentTarget as HTMLElement;
    const rect = target.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const ratio = x / rect.width;
    const index = Math.max(0, Math.min(Math.round(ratio * (frames.length - 1)), frames.length - 1));
    currentFrameIndex = index;
    _playbackFrame = index;
    renderFrame(index);
    updatePlaybackUI(index);
    ensureFramesLoaded(index);
  }

  // --- Continuous playback helpers (H.264) ---

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
    } catch {
      return null;
    }
  }

  async function handleVideoEnded() {
    const next = await loadNextRecording();
    if (next) {
      isTransitioning = true;
      currentId = next.id;
      await loadRecording();
      isTransitioning = false;
    }
  }

  function handleTimeUpdate(e: Event) {
    const video = e.target as HTMLVideoElement;
    if (video.duration && video.currentTime / video.duration > 0.8 && !nextRecordingId) {
      prefetchNextRecording();
    }
  }

  async function prefetchNextRecording() {
    if (nextRecordingId || !recording) return;
    const next = await loadNextRecording();
    if (next) {
      nextRecordingId = next.id;
      try {
        nextBlobUrl = await loadRecordingVideoBlob(next.id);
      } catch {
        nextRecordingId = null;
      }
    }
  }

  async function navigateToNext() {
    const next = await loadNextRecording();
    if (next) {
      isTransitioning = true;
      currentId = next.id;
      await loadRecording();
      isTransitioning = false;
    }
  }

  async function initVideoPlayer() {
    videoLoading = true;
    if (videoBlobUrl) { URL.revokeObjectURL(videoBlobUrl); videoBlobUrl = ''; }
    try {
      videoBlobUrl = await loadRecordingVideoBlob(currentId);
    } catch (e) {
      console.error('Failed to load video:', e);
      error = t('detail.failedLoadVideo');
    } finally {
      videoLoading = false;
    }
  }

  // Actions

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

  function goBack() {
    window.location.hash = '#/recordings';
  }

  async function handleDownload() {
    if (isDownloading || !recording) return;
    isDownloading = true;
    downloadProgress = 0;
    try {
      await apiDownloadRecording(recording.id, (loaded, total) => {
        downloadProgress = Math.round((loaded / total) * 100);
      });
    } catch (e) {
      console.error('Download failed:', e);
    } finally {
      isDownloading = false;
      downloadProgress = 0;
    }
  }

  // Keyboard shortcuts
  function handleKeydown(e: KeyboardEvent) {
    // Don't capture when typing in inputs
    const tag = (e.target as HTMLElement).tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;

    switch (e.key) {
      case ' ':
        e.preventDefault();
        if (recording?.format === 'mjpeg') {
          togglePlay();
        } else if (recording?.format === 'h264' || recording?.format === 'h265') {
          // Toggle native video play/pause
          const video = document.querySelector('video');
          if (video) {
            if (video.paused) video.play(); else video.pause();
          }
        }
        break;
      case 'ArrowLeft':
        e.preventDefault();
        if (recording?.format === 'mjpeg') {
          prevFrame();
        } else {
          const video = document.querySelector('video');
          if (video) video.currentTime = Math.max(0, video.currentTime - 5);
        }
        break;
      case 'ArrowRight':
        e.preventDefault();
        if (recording?.format === 'mjpeg') {
          nextFrame();
        } else {
          const video = document.querySelector('video');
          if (video) video.currentTime = Math.min(video.duration, video.currentTime + 5);
        }
        break;
      case 'Escape':
        goBack();
        break;
    }
  }
  // Lifecycle
onMount(() => {
  currentId = recordingId;
  if (!currentId) {
    error = t('detail.recordingIdRequired');
    loading = false;
    return;
  }
    loadRecording();

    // Add keyboard event listener
    window.addEventListener('keydown', handleKeydown);
});


onDestroy(() => {
stopPlaying();
if (videoBlobUrl) URL.revokeObjectURL(videoBlobUrl);
if (nextBlobUrl) URL.revokeObjectURL(nextBlobUrl);
    preloadedImages = [];

    // Remove keyboard event listener
    window.removeEventListener('keydown', handleKeydown);
});
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">

  <!-- Main content -->
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
            <!-- MP4 video player -->
            <div class="relative max-w-full bg-black rounded-t-[var(--radius-md)]">
              {#if isTransitioning}
                <div class="absolute inset-0 bg-black/60 flex items-center justify-center z-10">
                  <Loader2 size={32} class="animate-spin th-text-secondary" />
                </div>
              {/if}
              {#if videoLoading}
                <div class="flex items-center justify-center h-64">
                  <div class="spinner spinner-lg"></div>
                </div>
              {:else if videoBlobUrl}
                <video
                  controls
                  preload="auto"
                  class="w-full max-h-[80vh]"
                  src={videoBlobUrl}
                  onended={handleVideoEnded}
                  ontimeupdate={handleTimeUpdate}
                >
                  {t('detail.videoUnsupported')}
                </video>
              {:else}
                <div class="flex items-center justify-center h-64 th-text-muted">
                  {t('detail.failedLoadVideo')}
                </div>
              {/if}
            </div>
            <!-- Playback navigation -->
            <div class="flex items-center justify-between px-4 py-2 th-bg-secondary">
              <span class="text-sm th-text-muted">
                {t('detail.playing')}
                <span class="font-mono th-text-primary">{recording.camera_id}</span>
              </span>
              <div class="flex gap-2">
                <button
                  onclick={navigateToNext}
                  class="btn btn-ghost btn-sm flex items-center gap-1"
                >
                  {t('detail.nextRecording')}
                  <SkipForward size={16} />
                </button>
              </div>
            </div>
          {:else if recording.format === 'mjpeg'}
            <!-- JPEG frame player (canvas-based) -->
            <div class="bg-black">
              {#if framesLoading}
                <div class="flex items-center justify-center h-64">
                  <div class="spinner spinner-lg"></div>
                  <span class="th-text-muted ml-3">{t('detail.loadingFrames')}</span>
                </div>
              {:else if preloading}
                <div class="flex flex-col items-center justify-center h-64 gap-3">
                  <div class="spinner spinner-lg"></div>
                  <span class="th-text-muted text-sm">{t('detail.loadingFrames')} {preloadProgress}%</span>
                  <div class="w-48 h-1.5 th-bg-tertiary rounded-full overflow-hidden">
                    <div
                      class="h-full th-bg-info rounded-full transition-all duration-150"
                      style="width: {preloadProgress}%"
                    ></div>
                  </div>
                </div>
              {:else if frames.length === 0}
                <div class="flex items-center justify-center h-64">
                  <div class="text-center th-text-muted">
                    <div class="text-4xl mb-2">{t('detail.noFrames')}</div>
                    <p class="text-sm">{t('detail.downloadFrames')}</p>
                  </div>
                </div>
              {:else}
                <!-- Canvas frame display — no reactive state changes during playback -->
                <div class="max-h-[75vh] overflow-hidden flex items-center justify-center bg-black min-h-[200px]">
                  <canvas
                    bind:this={canvasEl}
                    class="max-w-full max-h-[75vh]"
                  ></canvas>
                </div>

                <!-- Controls bar -->
                <div class="th-bg-secondary px-4 py-3 space-y-2">
                  <!-- Progress bar -->
                  <div
                    class="relative h-2 th-bg-tertiary rounded cursor-pointer group"
                    onclick={handleProgressClick}
                    role="progressbar"
                    aria-valuenow={currentFrameIndex}
                    aria-valuemin={0}
                    aria-valuemax={frames.length - 1}
                  >
                    <div
                      bind:this={progressFillEl}
                      class="absolute top-0 left-0 h-full th-bg-accent rounded group-hover:th-bg-info transition-colors"
                      style="width: {frames.length > 1 ? (currentFrameIndex / (frames.length - 1)) * 100 : 100}%"
                    ></div>
                    <div
                      bind:this={progressThumbEl}
                      class="absolute top-1/2 -translate-y-1/2 w-3 h-3 th-bg-info rounded-full shadow group-hover:th-bg-accent transition-colors"
                      style="left: calc({frames.length > 1 ? (currentFrameIndex / (frames.length - 1)) * 100 : 100}% - 6px)"
                    ></div>
                  </div>

                  <!-- Control buttons -->
                  <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                      <button
                        onclick={prevFrame}
                        disabled={currentFrameIndex === 0 || isPlaying}
                        class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
                        style="color: {currentFrameIndex === 0 || isPlaying ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {currentFrameIndex === 0 || isPlaying ? 'transparent' : 'var(--bg-tertiary)'}"
                      >
                        {t('detail.prev')}
                      </button>

                      <button
                        onclick={togglePlay}
                        class="px-4 py-1.5 rounded text-sm font-medium text-white transition-colors"
                        style="background-color: {isPlaying ? 'var(--color-danger)' : 'var(--color-info)'}"
                      >
                        {isPlaying ? t('detail.pause') : t('detail.play')}
                      </button>
                      <button
                        onclick={nextFrame}
                        disabled={currentFrameIndex >= frames.length - 1 || isPlaying}
                        class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
                        style="color: {currentFrameIndex >= frames.length - 1 || isPlaying ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {!(currentFrameIndex >= frames.length - 1 || isPlaying) ? 'var(--bg-tertiary)' : 'transparent'}"
                      >
                        {t('detail.next')}
                      </button>
                    </div>

                    <!-- Frame counter (direct DOM update during playback) -->
                    <span bind:this={frameCounterEl} class="th-text-secondary text-sm font-mono">
                      {t('detail.frameCounter', { current: String(currentFrameIndex + 1), total: String(frames.length) })}
                    </span>

                    <!-- Speed control -->
                    <div class="flex items-center gap-1">
                      <span class="th-text-tertiary text-xs mr-1">{t('detail.speed')}</span>
                      {#each speeds as speed}
                        <button
                          onclick={() => setSpeed(speed)}
                          class="px-2 py-1 rounded text-xs font-medium transition-colors"
                          style="background-color: {playSpeed === speed ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {playSpeed === speed ? 'white' : 'var(--text-secondary)'}"
                        >
                          {speed}x
                        </button>
                      {/each}
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
            </div>
          {:else}
            <!-- Unsupported format -->
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
              <span class="badge badge-neutral">
                {(recording.format === 'h264' || recording.format === 'h265') ? t('recording.format.h264') : t('recording.format.mjpeg')}
              </span>
            </div>
          </div>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-6 mb-8">
            <div>
              <p class="text-sm th-text-tertiary mb-1">{t('detail.duration')}</p>
              <p class="text-lg font-semibold th-text-body">
                {formatDuration(recording.duration)}
              </p>
            </div>
            <div>
              <p class="text-sm th-text-tertiary mb-1">{t('detail.fileSize')}</p>
              <p class="text-lg font-semibold th-text-body">
                {formatFileSize(recording.file_size)}
              </p>
            </div>
            <div>
              <p class="text-sm th-text-tertiary mb-1">{t('detail.frames')}</p>
              <p class="text-lg font-semibold th-text-body">
                {recording.frame_count.toLocaleString()}
              </p>
            </div>
            <div>
              <p class="text-sm th-text-tertiary mb-1">{t('detail.endTime')}</p>
              <p class="text-lg font-semibold th-text-body">
                {formatDate(recording.ended_at)}
              </p>
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
