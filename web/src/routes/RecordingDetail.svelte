<script lang="ts">
  import { onMount, tick } from 'svelte';
  import {
    getRecording,
    deleteRecording,
    downloadRecording as apiDownloadRecording,
    loadRecordingVideoBlob,
    listRecordings
  } from '$lib/api';
  import { getTranscodingStatus, enqueueTranscodeTask } from '$lib/api/transcoding';
  import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import type { Recording } from '$lib/api';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { AlertTriangle, HelpCircle, SkipForward, Loader2, RefreshCw } from 'lucide-svelte';
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
        } else if (recording.format === 'h264' || recording.format === 'h265' || recording.format === 'timelapse') {
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
      try { nextBlobUrl = await loadRecordingVideoBlob(next.id); }
      catch (e) { console.warn('Failed to prefetch next recording:', e); nextRecordingId = null; }
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
    transcodingPollInterval = setInterval(loadTranscodingStatus, 5000);
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
        else if (recording?.format === 'h264' || recording?.format === 'h265') {
          const video = document.querySelector('video');
          if (video) { if (video.paused) video.play(); else video.pause(); }
        }
        break;
      case 'ArrowLeft':
        e.preventDefault();
        if (recording?.format === 'mjpeg') mjpegPlayer?.handleKeyAction('prevFrame');
        else { const v = document.querySelector('video'); if (v) v.currentTime = Math.max(0, v.currentTime - 5); }
        break;
      case 'ArrowRight':
        e.preventDefault();
        if (recording?.format === 'mjpeg') mjpegPlayer?.handleKeyAction('nextFrame');
        else { const v = document.querySelector('video'); if (v) v.currentTime = Math.min(v.duration, v.currentTime + 5); }
        break;
      case 'Escape': goBack(); break;
    }
  }

  $effect(() => {
    return () => {
      if (videoBlobUrl) URL.revokeObjectURL(videoBlobUrl);
      if (nextBlobUrl) URL.revokeObjectURL(nextBlobUrl);
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
          {:else if recording.format === 'mjpeg'}
            <div class="bg-black">
              <MjpegPlayer bind:this={mjpegPlayer} recordingId={currentId} oninitdone={() => {}} />
            </div>
          {:else}
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
