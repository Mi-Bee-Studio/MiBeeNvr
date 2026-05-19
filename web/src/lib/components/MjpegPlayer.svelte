<script lang="ts">
  import { tick } from 'svelte';
  import { listFrames, loadFrameBlob } from '$lib/api';
  import type { FrameInfo } from '$lib/api';
  import { t } from '$lib/i18n';
  import PlaybackControls from './PlaybackControls.svelte';

  interface Props {
    recordingId: string;
    oninitdone: () => void;
  }

  let {
    recordingId,
    oninitdone,
  } = $props();

  // Lazy loading constants
  const LAZY_BATCH_SIZE = 50;
  const LAZY_WINDOW = 20;
  const speeds = [1, 2, 5];

  // Reactive state
  let frames = $state<FrameInfo[]>([]);
  let currentFrameIndex = $state(0);
  let isPlaying = $state(false);
  let playSpeed = $state(1);
  let framesLoading = $state(false);
  let preloading = $state(false);
  let preloadProgress = $state(0);

  // Non-reactive internals for smooth playback
  let preloadedImages: (HTMLImageElement | null)[] = [];
  let _playbackFrame = 0;
  let playInterval: ReturnType<typeof setInterval> | null = null;
  let loadedFrameRange = { start: 0, end: 0 };
  let moreFramesAvailable = true;
  let abortController: AbortController | null = null;

  // DOM refs
  let canvasEl: HTMLCanvasElement | undefined = $state();
  let controlsRef: PlaybackControls | undefined = $state();

  // --- Canvas rendering ---

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

  // --- Frame preloading ---

  async function loadFrameBatch(start: number, end: number) {
    const batchSize = 5;
    let loaded = 0;
    const total = end - start;

    for (let i = start; i < end; i += batchSize) {
      if (abortController?.signal.aborted) return;

      const batch = frames.slice(i, Math.min(i + batchSize, end));
      const results = await Promise.all(
        batch.map(async (frame) => {
          try {
            const blobUrl = await loadFrameBlob(recordingId, frame.index);
            const img = new Image();
            await new Promise<void>((resolve, reject) => {
              img.onload = () => resolve();
              img.onerror = () => reject(new Error(`Failed to load frame ${frame.index}`));
              img.src = blobUrl;
            });
            URL.revokeObjectURL(blobUrl);
            return img;
          } catch (e) { console.warn('Failed to load frame:', e);
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

  function ensureFramesLoaded(index: number) {
    if (index >= loadedFrameRange.end - 10 && moreFramesAvailable) {
      const newEnd = Math.min(loadedFrameRange.end + LAZY_BATCH_SIZE, frames.length);
      loadFrameBatch(loadedFrameRange.end, newEnd);
      loadedFrameRange = { ...loadedFrameRange, end: newEnd };
      moreFramesAvailable = newEnd < frames.length;
    }

    const keepStart = Math.max(0, index - LAZY_WINDOW);
    const keepEnd = Math.min(frames.length, index + LAZY_WINDOW + 1);
    for (let i = 0; i < keepStart; i++) {
      preloadedImages[i] = null;
    }
    for (let i = keepEnd; i < frames.length; i++) {
      preloadedImages[i] = null;
    }
  }

  // --- Playback controls ---

  function prevFrame() {
    const idx = isPlaying ? _playbackFrame : currentFrameIndex;
    if (idx <= 0) return;
    const newIdx = idx - 1;
    currentFrameIndex = newIdx;
    _playbackFrame = newIdx;
    renderFrame(newIdx);
    controlsRef?.updatePlaybackUI(newIdx, frames.length);
  }

  function nextFrame() {
    const idx = isPlaying ? _playbackFrame : currentFrameIndex;
    if (idx >= frames.length - 1) return;
    const newIdx = idx + 1;
    currentFrameIndex = newIdx;
    _playbackFrame = newIdx;
    renderFrame(newIdx);
    controlsRef?.updatePlaybackUI(newIdx, frames.length);
    ensureFramesLoaded(newIdx);
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
      controlsRef?.updatePlaybackUI(next, frames.length);
      ensureFramesLoaded(next);
    }, 1000 / fps);
  }

  function stopPlaying() {
    isPlaying = false;
    if (playInterval) {
      clearInterval(playInterval);
      playInterval = null;
    }
    currentFrameIndex = _playbackFrame;
  }

  function togglePlay() {
    if (isPlaying) stopPlaying();
    else startPlaying();
  }

  function setSpeed(speed: number) {
    playSpeed = speed;
    if (isPlaying) {
      stopPlaying();
      startPlaying();
    }
  }

  function handleProgressClick(ratio: number) {
    if (frames.length === 0) return;
    const index = Math.max(0, Math.min(Math.round(ratio * (frames.length - 1)), frames.length - 1));
    currentFrameIndex = index;
    _playbackFrame = index;
    renderFrame(index);
    controlsRef?.updatePlaybackUI(index, frames.length);
    ensureFramesLoaded(index);
  }

  // --- Public API for parent ---

  export async function initPlayer() {
    framesLoading = true;
    abortController = new AbortController();
    try {
      const resp = await listFrames(recordingId);
      frames = resp.frames;
    } catch (e) {
      console.error('Failed to load frames:', e);
      framesLoading = false;
      return;
    }

    if (frames.length > 0) {
      await preloadAllFrames();
    }
    framesLoading = false;
    oninitdone();
  }

  export function handleKeyAction(action: 'togglePlay' | 'prevFrame' | 'nextFrame') {
    if (action === 'togglePlay') togglePlay();
    else if (action === 'prevFrame') prevFrame();
    else if (action === 'nextFrame') nextFrame();
  }

  // --- Cleanup: fix memory leak ---

  $effect(() => {
    return () => {
      if (abortController) {
        abortController.abort();
        abortController = null;
      }
      if (playInterval) {
        clearInterval(playInterval);
        playInterval = null;
      }
      preloadedImages = [];
    };
  });
</script>

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
  <!-- Canvas frame display -->
  <div class="max-h-[75vh] overflow-hidden flex items-center justify-center bg-black min-h-[200px]">
    <canvas
      bind:this={canvasEl}
      class="max-w-full max-h-[75vh]"
    ></canvas>
  </div>

  <!-- Playback controls (delegated to sub-component) -->
  <PlaybackControls
    bind:this={controlsRef}
    {isPlaying}
    {currentFrameIndex}
    totalFrames={frames.length}
    {playSpeed}
    {speeds}
    ontoggleplay={togglePlay}
    onprev={prevFrame}
    onnext={nextFrame}
    onsetspeed={setSpeed}
    onprogressclick={handleProgressClick}
  />
{/if}