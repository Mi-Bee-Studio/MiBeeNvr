<script lang="ts">
  import { Eye, Download, Trash2, RefreshCw, Clock, HardDrive, Image, Camera as CameraIcon } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { apiRequestBlob, getRecordingDownloadUrl, getMergedRecordingUrl } from '$lib/api';
  import type { Recording, Camera } from '$lib/api';

  interface Props {
    recording: Recording;
    cameras: Camera[];
    /** Optional transcoding status object (e.g. { enabled: true, tasks: [...] }) */
    transcodingStatus?: Record<string, unknown> | null;
    onview: (recording: Recording) => void;
    ondelete: (recording: Recording) => void;
    /** Called when the user clicks download */
    ondownload?: (recording: Recording) => void;
    /** Called when the user clicks merge (timelapse) */
    onmerge?: (recording: Recording) => void;
    /** Called when the user clicks transcode */
    ontranscode?: (recording: Recording) => void;
    selected: boolean;
    onselect: (recording: Recording) => void;
  }

  let {
    recording,
    cameras,
    transcodingStatus = null,
    onview,
    ondelete,
    ondownload,
    onmerge,
    ontranscode,
    selected,
    onselect,
  }: Props = $props();

  // --- Derived state ---

  let isTimelapse = $derived(recording.format === 'timelapse');
  let isVideo = $derived(recording.format === 'h264' || recording.format === 'h265');
  let isMJPEG = $derived(recording.format === 'mjpeg');
  let isMerged = $derived(recording.format === 'timelapse' && recording.merge_status === 'merged');
  let showMergeButton = $derived(isTimelapse && !isMerged && recording.merge_status !== 'pending');
  let showDownloadButton = $derived(isMerged || isVideo);
  let formatLabel = $derived.by(() => {
    // Prefer i18n keys, fallback to hardcoded
    switch (recording.format) {
      case 'h264': return t('recording.format.h264');
      case 'h265': return t('recording.format.h265');
      case 'mjpeg': return t('recording.format.mjpeg');
      case 'timelapse': return t('recording.format.timelapse');
      default: return recording.format;
    }
  });

  let formatBadgeClass = $derived.by(() => {
    if (isTimelapse) return 'bg-cyan-100 text-cyan-800 dark:bg-cyan-900/50 dark:text-cyan-300';
    if (isVideo) return 'badge-info';
    return 'badge-neutral';
  });

  let mergeStatusLabel = $derived.by(() => {
    if (recording.merge_status === 'merged') return t('detail.mergeStatusMerged');
    if (recording.merge_status === 'failed') return t('detail.mergeStatusFailed');
    if (recording.merge_status === 'pending') return t('detail.mergeStatusPending');
    return recording.merge_status ?? '';
  });

  let mergeBadgeClass = $derived.by(() => {
    if (recording.merge_status === 'merged') return 'badge-success';
    if (recording.merge_status === 'failed') return 'badge-error';
    if (recording.merge_status === 'pending') return 'badge-info';
    return 'badge-neutral';
  });

  function getCameraName(cameraId: string): string {
    const cam = cameras.find((c) => c.id === cameraId);
    return cam ? cam.name : cameraId;
  }

  // --- Thumbnail lazy loading ---

  let thumbnailLoaded = $state(false);
  let thumbnailUrl = $state<string | null>(null);
  let thumbnailError = $state(false);
  let thumbnailContainerEl = $state<HTMLElement | null>(null);
  let thumbnailAbortController = $state<AbortController | null>(null);

  function loadThumbnail() {
    if (thumbnailLoaded || thumbnailError) return;
    thumbnailLoaded = true;

    // H.264/H.265 recordings don't have thumbnail support
    if (!isTimelapse && !isMJPEG) {
      thumbnailError = true;
      return;
    }

    // Cancel any previous pending request
    thumbnailAbortController?.abort();
    thumbnailAbortController = new AbortController();
    const signal = thumbnailAbortController.signal;

    // Use timelapse thumbnail endpoint (also supports MJPEG recordings)
    apiRequestBlob(`/timelapse/${recording.id}/thumbnail`, { signal })
      .then((blob) => {
        if (signal.aborted) return;
        thumbnailUrl = URL.createObjectURL(blob);
      })
      .catch((err) => {
        if (err?.name === 'AbortError') return;
        thumbnailError = true;
      });
  }

  function setupLazyThumbnail(node: HTMLElement) {
    thumbnailContainerEl = node;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0].isIntersecting) {
          loadThumbnail();
          observer.disconnect();
        }
      },
      { rootMargin: '200px' },
    );
    observer.observe(node);

    return {
      destroy() {
        observer.disconnect();
        thumbnailAbortController?.abort();
        if (thumbnailUrl) {
          URL.revokeObjectURL(thumbnailUrl);
        }
      },
    };
  }

  // --- Handlers ---

  function handleCheckboxClick(e: Event) {
    e.stopPropagation();
    onselect(recording);
  }

  function handleCardClick() {
    onview(recording);
  }

  function handleDownloadClick(e: Event) {
    e.stopPropagation();
    if (ondownload) {
      ondownload(recording);
    } else {
      // Default: open download in new tab
      const url = isMerged
        ? getMergedRecordingUrl(recording.id)
        : getRecordingDownloadUrl(recording.id);
      window.open(url, '_blank');
    }
  }

  function handleMergeClick(e: Event) {
    e.stopPropagation();
    onmerge?.(recording);
  }

  function handleDeleteClick(e: Event) {
    e.stopPropagation();
    ondelete(recording);
  }

  function handleTranscodeClick(e: Event) {
    e.stopPropagation();
    ontranscode?.(recording);
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="card recording-card group border th-border overflow-hidden transition-all duration-200 hover:shadow-md hover:-translate-y-0.5 cursor-pointer"
  class:is-selected={selected}
  role="button"
  tabindex="0"
  onclick={handleCardClick}
  onkeydown={(e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      handleCardClick();
    }
  }}
>
  <!-- Thumbnail Area -->
  <div
    class="aspect-video th-bg-tertiary overflow-hidden relative"
    use:setupLazyThumbnail
  >
    <!-- Checkbox (top-left, visible on hover or when selected) -->
    <div
      class="absolute top-1.5 left-1.5 z-10 transition-opacity duration-150
        {selected ? 'opacity-100' : 'opacity-0 group-hover:opacity-100'}"
    >
      <input
        type="checkbox"
        checked={selected}
        onchange={handleCheckboxClick}
        onclick={(e) => e.stopPropagation()}
        class="w-4 h-4 rounded cursor-pointer th-border bg-black/30
          checked:bg-[var(--color-primary)] checked:border-[var(--color-primary)]
          focus:ring-2 focus:ring-[var(--color-primary)] focus:ring-offset-1"
      />
    </div>

    <!-- Thumbnail / Placeholder -->
    {#if thumbnailUrl}
      <img
        src={thumbnailUrl}
        alt={formatDate(recording.started_at)}
        class="w-full h-full object-cover"
        onerror={() => { thumbnailError = true; }}
      />
    {:else if !thumbnailError}
      <!-- Skeleton placeholder -->
      <div class="absolute inset-0 flex items-center justify-center">
        <div class="spinner spinner-lg th-text-muted"></div>
      </div>
    {:else}
      <!-- Error / format-specific fallback icon -->
      <div class="absolute inset-0 flex items-center justify-center">
        {#if isTimelapse || isMJPEG}
          <Image size={32} class="th-text-tertiary opacity-40" />
        {:else}
          <CameraIcon size={32} class="th-text-tertiary opacity-40" />
        {/if}
      </div>
    {/if}

    <!-- Duration badge (bottom-left) -->
    <div class="absolute bottom-1.5 left-1.5">
      <span class="inline-flex items-center gap-1 px-1.5 py-0.5 text-[10px] leading-none font-medium
        rounded bg-black/60 text-white/90 backdrop-blur-sm">
        <Clock size={10} />
        {#if isTimelapse}
          {recording.frame_count} frames
        {:else}
          {formatDuration(recording.duration)}
        {/if}
      </span>
    </div>

    <!-- Merge progress bar (bottom edge of thumbnail) -->
    {#if recording.merge_status === 'pending' && recording.merge_progress != null && recording.merge_progress > 0 && recording.merge_progress < 100}
      <div class="absolute bottom-0 left-0 right-0 h-1 bg-black/40">
        <div
          class="h-full bg-[var(--color-info)] transition-all duration-500"
          style="width: {recording.merge_progress}%"
        ></div>
      </div>
    {/if}

    <!-- Status badges (top-right) -->
    <div class="absolute top-1.5 right-1.5 flex flex-col gap-1 items-end">
      <!-- Merge status badge -->
      {#if recording.merge_status}
        <span
          class="badge text-[10px] leading-none py-0.5 px-1.5 {mergeBadgeClass}"
          title={recording.merge_status === 'failed' && recording.merge_error ? recording.merge_error : ''}
        >
          {mergeStatusLabel}
        </span>
      {/if}

      <!-- Merged recording indicator -->
      {#if isMerged && !recording.merge_status}
        <span class="badge text-[10px] leading-none py-0.5 px-1.5 badge-success">
          {t('detail.mergeStatusMerged')}
        </span>
      {/if}
    </div>
  </div>

  <!-- Info Section -->
  <div class="p-3 space-y-1.5">
    <!-- Camera name -->
    <p class="text-sm font-medium th-text-primary truncate" title={getCameraName(recording.camera_id)}>
      {getCameraName(recording.camera_id)}
    </p>

    <!-- Format badge -->
    <div class="flex items-center gap-2 flex-wrap">
      <span class="badge text-[10px] leading-none py-0.5 px-1.5 {formatBadgeClass}">
        {formatLabel}
      </span>
    </div>

    <!-- Metadata row (frame count / duration + file size) -->
    <div class="flex items-center gap-3 text-xs th-text-muted">
      {#if isTimelapse}
        <span class="inline-flex items-center gap-1">
          <Image size={12} />
          {recording.frame_count} {t('detail.framesLabel')}
        </span>
      {:else}
        <span class="inline-flex items-center gap-1">
          <Clock size={12} />
          {formatDuration(recording.duration)}
        </span>
      {/if}
      <span class="inline-flex items-center gap-1">
        <HardDrive size={12} />
        {formatFileSize(recording.file_size)}
      </span>
    </div>

    <!-- Date -->
    <p class="text-xs th-text-tertiary">
      {formatDate(recording.started_at)}
    </p>

    <!-- Actions bar -->
    <div class="flex items-center gap-1 pt-1.5 border-t th-border">
      <!-- View -->
      <button
        onclick={(e) => { e.stopPropagation(); onview(recording); }}
        class="btn btn-ghost px-2 py-1 text-xs transition-all duration-200"
        title={t('recordings.view')}
      >
        <Eye size={14} />
      </button>

      <!-- Download (for merged recordings) -->
      {#if showDownloadButton}
        <button
          onclick={handleDownloadClick}
          class="btn btn-ghost px-2 py-1 text-xs th-text-secondary hover:text-green-500 transition-all duration-200"
          title={t('detail.download')}
        >
          <Download size={14} />
        </button>
      {/if}

      <!-- Merge (for unmerged timelapse) -->
      {#if showMergeButton}
        <button
          onclick={handleMergeClick}
          class="btn btn-ghost px-2 py-1 text-xs th-text-secondary hover:text-cyan-400 transition-all duration-200"
          title="Merge"
        >
          <RefreshCw size={14} />
        </button>
      {/if}

      <!-- Transcode (if transcoding enabled + not currently transcoding) -->
      {#if transcodingStatus?.enabled && ontranscode}
        <button
          onclick={handleTranscodeClick}
          class="btn btn-ghost px-2 py-1 text-xs th-text-secondary hover:text-blue-500 transition-all duration-200"
          title={t('transcoding.recordings.transcodeBtn')}
        >
          <RefreshCw size={14} />
        </button>
      {/if}

      <!-- Spacer -->
      <div class="flex-1"></div>

      <!-- Delete -->
      <button
        onclick={handleDeleteClick}
        class="btn btn-ghost px-2 py-1 text-xs th-color-danger hover:text-red-500 transition-all duration-200"
        title={t('recordings.delete')}
      >
        <Trash2 size={14} />
      </button>
    </div>
  </div>
</div>

<style>
  .recording-card.is-selected {
    border-color: var(--color-primary);
    box-shadow: 0 0 0 1px var(--color-primary), var(--shadow-sm);
  }

  .recording-card.is-selected:hover {
    box-shadow: 0 0 0 1px var(--color-primary), var(--shadow-md);
  }

  /* Checkbox — preserve brand color on dark bg */
  .recording-card input[type="checkbox"] {
    accent-color: var(--color-primary);
  }
</style>
