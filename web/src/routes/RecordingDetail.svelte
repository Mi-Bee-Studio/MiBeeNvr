<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import {
    getRecording,
    deleteRecording,
    pinRecording,
    unpinRecording,
    downloadRecording as apiDownloadRecording,
    listFrames,
    loadFrameBlob,
    loadRecordingVideoBlob
  } from '$lib/api';
  import type { Recording, FrameInfo } from '$lib/api';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t, onLangChange, getCurrentLang } from '$lib/i18n';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';

  // Re-render on language change
  let lang = getCurrentLang();
  onLangChange(() => { lang = getCurrentLang(); });

  // Recording ID passed as prop
  export let recordingId = '';
  let recording: Recording | null = null;
  let loading = true;
  let error = '';
  let deleteConfirm = false;

  // JPEG frame player state
  let frames: FrameInfo[] = [];
  let currentFrameIndex = 0;
  let frameBlobUrl = '';
  let isPlaying = false;
  let playInterval: ReturnType<typeof setInterval> | null = null;
  let playSpeed = 1; // multiplier: 1x, 2x, 5x
  let framesLoading = false;
  let frameChanging = false;
  let progressDragging = false;

  // Video blob URL (for MP4 auth)
  let videoBlobUrl = '';
  let videoLoading = false;

  // Speed options
  const speeds = [1, 2, 5];

  // Load recording data
  async function loadRecording() {
    loading = true;
    error = '';

    try {
      recording = await getRecording(recordingId);
      // After loading recording, init media
      if (recording) {
        if (recording.format === 'mjpeg') {
          initFramePlayer();
        } else if (recording.format === 'h264') {
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
      const resp = await listFrames(recordingId);
      frames = resp.frames;
      if (frames.length > 0) {
        await showFrame(0);
      }
    } catch (e) {
      console.error(t('common.failedLoadRecording'), e);
    } finally {
      framesLoading = false;
    }
  }

  async function showFrame(index: number) {
    if (index < 0 || index >= frames.length) return;
    frameChanging = true;
    // Revoke old blob URL
    if (frameBlobUrl) {
      URL.revokeObjectURL(frameBlobUrl);
      frameBlobUrl = '';
    }
    try {
      frameBlobUrl = await loadFrameBlob(recordingId, frames[index].index);
      currentFrameIndex = index;
    } catch (e) {
      console.error(t('common.failedLoadRecording'), e);
    } finally {
      frameChanging = false;
    }
  }

  function prevFrame() {
    if (currentFrameIndex > 0) {
      showFrame(currentFrameIndex - 1);
    }
  }

  function nextFrame() {
    if (currentFrameIndex < frames.length - 1) {
      showFrame(currentFrameIndex + 1);
    }
  }

  function togglePlay() {
    if (isPlaying) {
      stopPlaying();
    } else {
      startPlaying();
    }
  }

  function startPlaying() {
    if (frames.length === 0) return;
    isPlaying = true;
    const fps = 3 * playSpeed;
    playInterval = setInterval(() => {
      const next = currentFrameIndex + 1;
      if (next >= frames.length) {
        stopPlaying();
        return;
      }
      showFrame(next);
    }, 1000 / fps);
  }

  function stopPlaying() {
    isPlaying = false;
    if (playInterval) {
      clearInterval(playInterval);
      playInterval = null;
    }
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
    const index = Math.round(ratio * (frames.length - 1));
    showFrame(Math.max(0, Math.min(index, frames.length - 1)));
  }

  // Video player initialization (MP4 with auth)
  async function initVideoPlayer() {
    videoLoading = true;
    try {
      videoBlobUrl = await loadRecordingVideoBlob(recordingId);
    } catch (e) {
      console.error(t('detail.failedLoadVideo'), e);
      error = t('detail.failedLoadVideo');
    } finally {
      videoLoading = false;
    }
  }

  // Actions
  async function togglePin() {
    if (!recording) return;

    try {
      if (recording.pinned) {
        await unpinRecording(recording.id);
        recording.pinned = false;
      } else {
        await pinRecording(recording.id);
        recording.pinned = true;
      }
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedUpdatePin');
    }
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

  function goBack() {
    window.location.hash = '#/recordings';
  }

  function downloadRecording() {
    if (!recording) return;
    apiDownloadRecording(recording.id);
  }

  // Lifecycle
  onMount(() => {
    if (!recordingId) {
      error = t('detail.recordingIdRequired');
      loading = false;
      return;
    }
    loadRecording();
  });

  onDestroy(() => {
    stopPlaying();
    if (frameBlobUrl) URL.revokeObjectURL(frameBlobUrl);
    if (videoBlobUrl) URL.revokeObjectURL(videoBlobUrl);
  });
</script>

<div class="min-h-screen bg-slate-900">
  <!-- Header -->
  <header class="bg-slate-800 border-b border-slate-700">
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
      <div class="flex items-center justify-between h-16">
        <div class="flex items-center gap-4">
          <button on:click={goBack} class="text-slate-300 hover:text-slate-100">
            {t('detail.back')}
          </button>
          <h1 class="text-xl font-bold text-slate-100">{t('detail.title')}</h1>
        </div>
        <nav class="flex gap-4">
          <a href="#/recordings" class="text-slate-300 hover:text-slate-100 transition-colors">
            {t('nav.recordings')}
          </a>
          <a href="#/stats" class="text-slate-300 hover:text-slate-100 transition-colors">
            {t('nav.stats')}
          </a>
          <a href="#/settings" class="text-slate-300 hover:text-slate-100 transition-colors">
            {t('nav.settings')}
          </a>
        </nav>
        <div class="flex items-center gap-3">
          <LanguageSwitcher />
        </div>
      </div>
    </div>
  </header>

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Loading state -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="text-red-400 mb-4">&#x26A0;&#xFE0F;</div>
        <p class="text-slate-300 mb-4">{error}</p>
        <button on:click={goBack} class="btn btn-primary">
          {t('detail.goBack')}
        </button>
      </div>
    {:else if recording}
      <div class="space-y-6">
        <!-- Playback section -->
        <div class="card border border-slate-700/60">
          {#if recording.format === 'h264'}
            <!-- MP4 video player -->
            <div class="max-w-full bg-black">
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
                >
                  {t('detail.videoUnsupported')}
                </video>
              {:else}
                <div class="flex items-center justify-center h-64 text-slate-400">
                  {t('detail.failedLoadVideo')}
                </div>
              {/if}
            </div>
          {:else if recording.format === 'mjpeg'}
            <!-- JPEG frame player -->
            <div class="bg-black">
              {#if framesLoading}
                <div class="flex items-center justify-center h-64">
                  <div class="spinner spinner-lg"></div>
                  <span class="text-slate-400 ml-3">{t('detail.loadingFrames')}</span>
                </div>
              {:else if frames.length === 0}
                <div class="flex items-center justify-center h-64">
                  <div class="text-center text-slate-400">
                    <div class="text-4xl mb-2">{t('detail.noFrames')}</div>
                    <p class="text-sm">{t('detail.downloadFrames')}</p>
                  </div>
                </div>
              {:else}
                <!-- Frame display -->
                <div class="relative max-h-[75vh] overflow-hidden flex items-center justify-center bg-black min-h-[200px]">
                  {#if frameChanging}
                    <div class="absolute inset-0 flex items-center justify-center bg-black/40 z-10">
                      <div class="spinner spinner-lg"></div>
                    </div>
                  {/if}
                  {#if frameBlobUrl}
                    <img
                      src={frameBlobUrl}
                      alt="Frame {currentFrameIndex + 1}"
                      class="max-w-full max-h-[75vh] object-contain"
                    />
                  {/if}
                </div>

                <!-- Controls bar -->
                <div class="bg-slate-800/90 px-4 py-3 space-y-2">
                  <!-- Progress bar -->
                  <div
                    class="relative h-2 bg-slate-700 rounded cursor-pointer group"
                    on:click={handleProgressClick}
                    role="progressbar"
                    aria-valuenow={currentFrameIndex}
                    aria-valuemin={0}
                    aria-valuemax={frames.length - 1}
                  >
                    <div
                      class="absolute top-0 left-0 h-full bg-blue-500 rounded group-hover:bg-blue-400 transition-colors"
                      style="width: {frames.length > 1 ? (currentFrameIndex / (frames.length - 1)) * 100 : 100}%"
                    ></div>
                    <div
                      class="absolute top-1/2 -translate-y-1/2 w-3 h-3 bg-blue-400 rounded-full shadow group-hover:bg-blue-300 transition-colors"
                      style="left: calc({frames.length > 1 ? (currentFrameIndex / (frames.length - 1)) * 100 : 100}% - 6px)"
                    ></div>
                  </div>

                  <!-- Control buttons -->
                  <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                      <button
                        on:click={prevFrame}
                        disabled={currentFrameIndex === 0 || frameChanging}
                        class="px-3 py-1.5 rounded text-sm font-medium transition-colors
                          {currentFrameIndex === 0 || frameChanging
                            ? 'text-slate-600 cursor-not-allowed'
                            : 'text-slate-200 bg-slate-700 hover:bg-slate-600'}"
                      >
                        {t('detail.prev')}
                      </button>

                      <button
                        on:click={togglePlay}
                        disabled={frameChanging}
                        class="px-4 py-1.5 rounded text-sm font-medium transition-colors
                          {isPlaying
                            ? 'bg-red-600 text-white hover:bg-red-500'
                            : 'bg-blue-600 text-white hover:bg-blue-500'}
                          {frameChanging ? 'opacity-50 cursor-not-allowed' : ''}"
                      >
                        {isPlaying ? t('detail.pause') : t('detail.play')}
                      </button>

                      <button
                        on:click={nextFrame}
                        disabled={currentFrameIndex >= frames.length - 1 || frameChanging}
                        class="px-3 py-1.5 rounded text-sm font-medium transition-colors
                          {currentFrameIndex >= frames.length - 1 || frameChanging
                            ? 'text-slate-600 cursor-not-allowed'
                            : 'text-slate-200 bg-slate-700 hover:bg-slate-600'}"
                      >
                        {t('detail.next')}
                      </button>
                    </div>

                    <!-- Frame counter -->
                    <div class="text-slate-300 text-sm font-mono">
                      {t('detail.frameCounter', { current: String(currentFrameIndex + 1), total: String(frames.length) })}
                    </div>

                    <!-- Speed control -->
                    <div class="flex items-center gap-1">
                      <span class="text-slate-400 text-xs mr-1">{t('detail.speed')}</span>
                      {#each speeds as speed}
                        <button
                          on:click={() => setSpeed(speed)}
                          class="px-2 py-1 rounded text-xs font-medium transition-colors
                            {playSpeed === speed
                              ? 'bg-blue-600 text-white'
                              : 'bg-slate-700 text-slate-300 hover:bg-slate-600'}"
                        >
                          {speed}x
                        </button>
                      {/each}
                    </div>
                  </div>
                </div>
              {/if}
            </div>
          {:else}
            <!-- Unsupported format -->
            <div class="flex items-center justify-center h-64 bg-black">
              <div class="text-center text-slate-400">
                <div class="text-4xl mb-2">&#x2753;</div>
                <p class="text-lg">{t('detail.unsupportedFormat')}</p>
                <p class="text-sm mt-2">{t('detail.format')}: {recording.format}</p>
              </div>
            </div>
          {/if}
        </div>

        <!-- Recording info -->
        <div class="card p-6 border border-slate-700/60">
          <div class="flex items-start justify-between mb-6">
            <div>
              <h2 class="text-2xl font-bold text-slate-100 mb-2">
                {recording.camera_id}
              </h2>
              <p class="text-slate-400">
                {formatDate(recording.started_at)}
              </p>
            </div>
            <div class="flex gap-2">
              {#if recording.pinned}
                <span class="badge badge-warning">{t('detail.pinnedBadge')}</span>
              {/if}
              <span class="badge badge-neutral">
                {recording.format === 'h264' ? 'MP4' : 'JPEG'}
              </span>
            </div>
          </div>
          <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
            <div>
              <p class="text-sm text-slate-400 mb-1">{t('detail.duration')}</p>
              <p class="text-lg font-semibold text-slate-200">
                {formatDuration(recording.duration)}
              </p>
            </div>
            <div>
              <p class="text-sm text-slate-400 mb-1">{t('detail.fileSize')}</p>
              <p class="text-lg font-semibold text-slate-200">
                {formatFileSize(recording.file_size)}
              </p>
            </div>
            <div>
              <p class="text-sm text-slate-400 mb-1">{t('detail.frames')}</p>
              <p class="text-lg font-semibold text-slate-200">
                {recording.frame_count.toLocaleString()}
              </p>
            </div>
            <div>
              <p class="text-sm text-slate-400 mb-1">{t('detail.endTime')}</p>
              <p class="text-lg font-semibold text-slate-200">
                {formatDate(recording.ended_at)}
              </p>
            </div>
          </div>

          <!-- Actions -->
          <div class="flex flex-wrap gap-3 border-t border-slate-700 pt-6">
            <button
              on:click={downloadRecording}
              class="btn btn-primary"
            >
              {t('detail.download')}
            </button>
            <button
              on:click={togglePin}
              class="btn btn-secondary"
            >
              {recording.pinned ? t('detail.unpin') : t('detail.pin')}
            </button>
            <button
              on:click={() => deleteConfirm = true}
              class="btn btn-danger"
            >
              {t('detail.delete')}
            </button>
          </div>
        </div>
      </div>
    {/if}
  </main>

  <!-- Delete confirmation modal -->
  {#if deleteConfirm && recording}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold text-slate-100 mb-4">{t('detail.deleteTitle')}</h3>
        <p class="text-slate-300 mb-6">
          {t('detail.deleteMessage', { camera_id: recording.camera_id })}
        </p>
        <div class="flex gap-3 justify-end">
          <button
            on:click={() => deleteConfirm = false}
            class="btn btn-secondary"
          >
            {t('detail.cancel')}
          </button>
          <button
            on:click={confirmDelete}
            class="btn btn-danger"
          >
            {t('detail.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>
