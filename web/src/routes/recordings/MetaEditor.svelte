<script lang="ts">
  // MetaEditor owns the recording info card (title/badges/stat grid) and the
  // actions row (download / transcode / delete), plus the transcode task status
  // display. Merge + delete actions are delegated to the host via callbacks
  // (the host owns the MergePanel and the delete-confirm modal). Transcoding
  // polling + status is self-contained here. Extracted from
  // RecordingDetail.svelte (#136).
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { RefreshCw } from 'lucide-svelte';
  import type { Recording } from '$lib/api';
  import { downloadRecording as apiDownloadRecording } from '$lib/api';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import { enqueueTranscodeTask, getTranscodingStatus, getTranscodingTasks } from '$lib/api/transcoding';

  interface Props {
    recording: Recording;
    currentId: string;
    /** Live merge state mirrored from MergePanel (for badges + actions row). */
    mergeState: { inProgress: boolean; pct: number; eta: string; error: string };
    /** Whether a merged MP4 is available (controls merge button visibility). */
    canMerge: boolean;
    onstartmerge: () => void;
    oncancelmerge: () => void;
    ondelete: () => void;
  }

  let {
    recording,
    currentId,
    mergeState,
    canMerge,
    onstartmerge,
    oncancelmerge,
    ondelete,
  } = $props();

  // --- Download ---
  let downloadProgress = $state(0);
  let isDownloading = $state(false);

  async function handleDownload() {
    if (isDownloading) return;
    isDownloading = true;
    downloadProgress = 0;
    try {
      await apiDownloadRecording(recording.id, (loaded, total) => {
        downloadProgress = Math.round((loaded / total) * 100);
      });
    } catch (e) { console.error('Download failed:', e); }
    finally { isDownloading = false; downloadProgress = 0; }
  }

  // --- Transcoding status (self-contained poll) ---
  let transcodingStatus = $state<ManagerStatus | null>(null);
  let transcodingPollInterval: ReturnType<typeof setInterval> | null = null;
  let transcodeTask = $derived(findTranscodeTask());

  function findTranscodeTask(): TranscodeTask | undefined {
    if (!transcodingStatus?.recent_results) return undefined;
    return transcodingStatus.recent_results.find((tk) => tk.recording_id === currentId);
  }

  async function loadTranscodingStatus() {
    try { transcodingStatus = await getTranscodingStatus(); } catch { /* not critical */ }
  }
  function startTranscodingPoll() {
    stopTranscodingPoll();
    loadTranscodingStatus();
    transcodingPollInterval = setInterval(loadTranscodingStatus, 3000);
  }
  function stopTranscodingPoll() {
    if (transcodingPollInterval) { clearInterval(transcodingPollInterval); transcodingPollInterval = null; }
  }

  async function handleTranscode() {
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
    } catch {
      showToast(t('transcoding.recordings.transcodeFailed'), 'error');
    }
  }

  // Start polling on mount, stop on destroy.
  import { onMount, onDestroy } from 'svelte';
  onMount(startTranscodingPoll);
  onDestroy(stopTranscodingPoll);

  // Restart status load when the recording changes (so transcodeTask reflects
  // the current recording quickly instead of waiting up to 3s).
  let lastId = '';
  $effect(() => {
    if (recording.id !== lastId) { lastId = recording.id; loadTranscodingStatus(); }
  });
</script>

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
      {#if recording.merge_status === 'merged' || recording.merge_status === 'daily_merged'}
        <span class="badge badge-success">{t('recordings.merged')}</span>
      {:else}
        <span class="badge badge-neutral">{t('recordings.originalSegment')}</span>
      {/if}
      <span class="badge {recording.format === 'timelapse' ? 'badge-info' : 'badge-neutral'}">
        {recording.format === 'timelapse'
          ? t('recording.format.timelapse')
          : (recording.format === 'h264' || recording.format === 'h265')
            ? t('recording.format.h264')
            : recording.format === 'avi'
              ? 'AVI'
            : t('recording.format.mjpeg')}
      </span>
      {#if recording.format === 'timelapse' && recording.merge_status}
        <span class="badge {recording.merge_status === 'merged' ? 'badge-success' : recording.merge_status === 'failed' ? 'badge-error' : mergeState.inProgress ? 'badge-info' : 'badge-neutral'}">
          {recording.merge_status === 'merged' ? t('detail.mergeStatusMerged') : recording.merge_status === 'failed' ? t('detail.mergeStatusFailed') : mergeState.inProgress ? t('detail.mergeStatusMerging', { percent: String(mergeState.pct) }) : t('detail.mergeStatusPending')}
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
      {#if canMerge && !mergeState.inProgress}
        <button onclick={onstartmerge} class="btn btn-primary">
          {t('detail.mergeAndPlay')}
        </button>
      {/if}
      {#if mergeState.inProgress}
        <div class="flex items-center gap-3">
          <div class="flex-1 h-1.5 rounded-full th-bg-tertiary overflow-hidden">
            <div class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500" style="width: {mergeState.pct}%"></div>
          </div>
          <span class="text-xs th-text-secondary">{t('detail.mergingProgress', { percent: String(mergeState.pct) })}</span>
          {#if mergeState.eta}
            <span class="text-xs th-text-muted">{mergeState.eta}</span>
          {/if}
          <button onclick={oncancelmerge} class="btn btn-ghost btn-xs th-color-danger">
            {t('detail.cancelMerge')}
          </button>
        </div>
      {/if}
      {#if mergeState.error}
        <div class="flex items-center gap-3">
          <span class="text-xs th-color-danger">{t('detail.mergeFailed', { error: mergeState.error })}</span>
          <button onclick={onstartmerge} class="btn btn-secondary btn-sm">{t('detail.mergeRetry')}</button>
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
      <button onclick={ondelete} class="btn btn-danger">
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
