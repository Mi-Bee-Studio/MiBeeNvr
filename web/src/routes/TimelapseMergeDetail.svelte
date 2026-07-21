<script lang="ts">
  import { onMount } from 'svelte';
  import {
    getTimelapseMerge,
    getTimelapseMergeDownloadUrl,
    deleteTimelapseMerge,
  } from '$lib/api';
  import type { TimelapseMerge } from '$lib/api';
  import { getCamera } from '$lib/api';
  import type { Camera } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { formatFileSize } from '$lib/format';
  import { AlertTriangle, ArrowLeft, Download, Trash2, RefreshCw, Loader2 } from 'lucide-svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  // Route prop — the numeric id of the timelapse_merge row.
  let { mergeId = '' } = $props();

  let merge = $state<TimelapseMerge | null>(null);
  let camera = $state<Camera | null>(null);
  let loading = $state(true);
  let error = $state('');
  let videoUrl = $state('');
  let videoError = $state<string | null>(null);
  let videoErrorMsg = $state('');
  let deleteConfirm = $state(false);
  let deleting = $state(false);

  onMount(() => {
    loadMerge();
  });

  async function loadMerge() {
    loading = true;
    error = '';
    try {
      merge = await getTimelapseMerge(mergeId);
      if (!merge) {
        error = t('timelapseMerge.notFound');
        return;
      }
      // Fetch camera for friendly name display.
      try {
        camera = await getCamera(merge.camera_id);
      } catch {
        camera = null;
      }
      if (merge.status === 'completed' && merge.output_path) {
        videoUrl = getTimelapseMergeDownloadUrl(merge.id);
      } else {
        videoUrl = '';
      }
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.error');
    } finally {
      loading = false;
    }
  }

  function handleVideoError(e: Event) {
    const video = e.target as HTMLVideoElement;
    const mediaError = video.error;
    if (!mediaError) return;
    if (mediaError.code === MediaError.MEDIA_ERR_ABORTED) return;
    if (mediaError.code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED) {
      // Most likely: H.265 on a browser without a platform HEVC decoder.
      videoError = 'src_not_supported';
      const codec = merge?.codec?.toUpperCase() ?? 'H.265';
      videoErrorMsg = t('timelapseMerge.playbackUnsupported', { codec });
    } else if (mediaError.code === MediaError.MEDIA_ERR_NETWORK) {
      videoError = 'network';
      videoErrorMsg = t('detail.videoNetworkError');
    } else if (mediaError.code === MediaError.MEDIA_ERR_DECODE) {
      videoError = 'decode';
      videoErrorMsg = t('detail.videoDecodeError');
    } else {
      videoError = 'unknown';
      videoErrorMsg = t('detail.videoUnknownError');
    }
  }

  function goBack() {
    window.location.hash = '#/recordings';
  }

  function formatDateTime(iso: string): string {
    if (!iso) return '';
    try {
      const d = new Date(iso);
      return d.toLocaleString();
    } catch {
      return iso;
    }
  }

  async function handleDelete() {
    if (!merge) return;
    deleting = true;
    try {
      await deleteTimelapseMerge(merge.id);
      showToast(t('timelapseMerge.deleted'), 'success');
      window.location.hash = '#/recordings';
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    } finally {
      deleting = false;
      deleteConfirm = false;
    }
  }

  function statusLabel(status: string): string {
    switch (status) {
      case 'pending': return t('timelapseMerge.statusPending');
      case 'merging': return t('timelapseMerge.statusMerging');
      case 'completed': return t('timelapseMerge.statusCompleted');
      case 'failed': return t('timelapseMerge.statusFailed');
      default: return status;
    }
  }

  function statusBadgeClass(status: string): string {
    switch (status) {
      case 'completed': return 'badge-success';
      case 'merging': return 'badge-warning';
      case 'failed': return 'badge-error';
      default: return 'badge-ghost';
    }
  }
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Back button -->
    <button onclick={goBack} class="btn btn-ghost btn-sm mb-4 flex items-center gap-1">
      <ArrowLeft size={14} />
      {t('detail.goBack')}
    </button>

    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="th-color-danger mb-4 flex justify-center"><AlertTriangle size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadMerge} class="btn btn-primary btn-sm flex items-center gap-1 mx-auto">
          <RefreshCw size={14} />
          {t('common.retry')}
        </button>
      </div>
    {:else if merge}
      <div class="space-y-6">
        <!-- Header -->
        <div class="flex items-start justify-between gap-4 flex-wrap">
          <div>
            <h1 class="text-2xl font-semibold th-text-primary flex items-center gap-2">
              {t('timelapseMerge.title')}
              <span class="text-sm font-normal th-text-tertiary">#{merge.id}</span>
            </h1>
            <p class="text-sm th-text-secondary mt-1">
              {camera?.name ?? merge.camera_id}
              ·
              {formatDateTime(merge.window_start)}
              →
              {formatDateTime(merge.window_end)}
            </p>
          </div>
          <div class="flex items-center gap-2">
            <span class="badge {statusBadgeClass(merge.status)}">{statusLabel(merge.status)}</span>
          </div>
        </div>

        <!-- Player / status -->
        <div class="card border th-border overflow-hidden">
            {#if merge.status === 'completed' && videoUrl}
            {#if videoError === 'src_not_supported'}
              <div class="p-8 text-center">
                <AlertTriangle size={40} class="mx-auto mb-3 th-color-warning" />
                <p class="th-text-primary mb-2">{videoErrorMsg}</p>
                <a href={videoUrl} download class="btn btn-primary btn-sm inline-flex items-center gap-1">
                  <Download size={14} />
                  {t('detail.download')}
                </a>
              </div>
            {:else}
              <video
                controls
                autoplay
                class="w-full max-h-[70vh] bg-black"
                src={videoUrl}
                onerror={handleVideoError}
              ></video>
              {#if videoError}
                <div class="p-3 th-bg-warning-soft th-color-warning text-sm">
                  {videoErrorMsg}
                </div>
              {/if}
            {/if}
          {:else if merge.status === 'merging' || merge.status === 'pending'}
            <div class="p-12 text-center">
              <Loader2 size={40} class="mx-auto mb-3 animate-spin th-text-secondary" />
              <p class="th-text-secondary">{t('timelapseMerge.notReady')}</p>
              <button onclick={loadMerge} class="btn btn-ghost btn-sm mt-4 flex items-center gap-1 mx-auto">
                <RefreshCw size={14} />
                {t('common.retry')}
              </button>
            </div>
          {:else if merge.status === 'failed'}
            <div class="p-8 text-center">
              <AlertTriangle size={40} class="mx-auto mb-3 th-color-danger" />
              <p class="th-text-primary mb-1">{t('timelapseMerge.statusFailed')}</p>
              {#if merge.error}
                <p class="text-sm th-text-tertiary font-mono break-all">{merge.error}</p>
              {/if}
            </div>
          {/if}
        </div>

        <!-- Metadata -->
        <div class="card p-4 border th-border">
          <h3 class="text-sm font-medium th-text-secondary mb-3">{t('timelapseMerge.title')}</h3>
          <dl class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
            <div>
              <dt class="th-text-tertiary">{t('timelapseMerge.camera')}</dt>
              <dd class="th-text-primary">{camera?.name ?? merge.camera_id}</dd>
            </div>
            <div>
              <dt class="th-text-tertiary">{t('timelapseMerge.duration')}</dt>
              <dd class="th-text-primary">{merge.duration_label}</dd>
            </div>
            <div>
              <dt class="th-text-tertiary">{t('timelapseMerge.codec')}</dt>
              <dd class="th-text-primary uppercase">{merge.codec || '—'}</dd>
            </div>
            <div>
              <dt class="th-text-tertiary">{t('timelapseMerge.frames')}</dt>
              <dd class="th-text-primary">{merge.frame_count}</dd>
            </div>
            <div>
              <dt class="th-text-tertiary">{t('timelapseMerge.fileSize')}</dt>
              <dd class="th-text-primary">{merge.file_size > 0 ? formatFileSize(merge.file_size) : '—'}</dd>
            </div>
            <div>
              <dt class="th-text-tertiary">{t('timelapseMerge.windowLabel')}</dt>
              <dd class="th-text-primary">{formatDateTime(merge.window_start)}</dd>
            </div>
            <div class="col-span-2">
              <dt class="th-text-tertiary">{t('timelapseMerge.status')}</dt>
              <dd class="th-text-primary">
                <span class="badge {statusBadgeClass(merge.status)}">{statusLabel(merge.status)}</span>
                {#if merge.completed_at}
                  <span class="text-xs th-text-tertiary ml-2">{formatDateTime(merge.completed_at)}</span>
                {/if}
              </dd>
            </div>
          </dl>

          {#if merge.error}
            <div class="mt-4 p-3 th-bg-danger-soft rounded text-sm">
              <p class="th-color-danger font-medium mb-1">{t('timelapseMerge.statusFailed')}</p>
              <p class="font-mono text-xs break-all">{merge.error}</p>
            </div>
          {/if}

          <!-- Actions -->
          <div class="flex gap-2 mt-4 pt-4 border-t th-border">
            {#if merge.status === 'completed' && videoUrl}
              <a href={videoUrl} download class="btn btn-secondary btn-sm flex items-center gap-1">
                <Download size={14} />
                {t('detail.download')}
              </a>
            {/if}
            <button
              onclick={() => deleteConfirm = true}
              class="btn btn-ghost btn-sm flex items-center gap-1 th-color-danger"
              disabled={deleting}
            >
              <Trash2 size={14} />
              {t('detail.delete')}
            </button>
          </div>
        </div>
      </div>
    {/if}
  </main>
</div>

<ConfirmDialog
  bind:open={deleteConfirm}
  title={t('detail.delete')}
  message={t('timelapseMerge.deleteConfirm')}
  confirmLabel={t('detail.delete')}
  cancelLabel={t('common.cancel')}
  onConfirm={handleDelete}
/>
