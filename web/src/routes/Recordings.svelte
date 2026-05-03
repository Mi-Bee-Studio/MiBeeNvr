<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import {
    listRecordings,
    listCameras,
    deleteRecording,
    pinRecording,
    unpinRecording
  } from '$lib/api';
  import type { Recording, Camera } from '$lib/api';
  import Pagination from '../components/Pagination.svelte';
  import { t } from '$lib/i18n';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { Pin, MapPin, Trash2 } from 'lucide-svelte';


  // Filter state
  let cameraId = '';
  let format = '';
  let pinned = '';
  let cameras: Camera[] = [];
  let limit = 50;
  let offset = 0;

  // Data state
  let recordings = $state<Recording[]>([]);
  let totalRecordings = $state(0);
  let loading = $state(false);
  let error = $state('');
  let deleteConfirm: Recording | null = null;

  // Auto-refresh interval
  let refreshInterval: number;

  // Load data
  async function loadRecordings() {
    loading = true;
    error = '';

    try {
      const response = await listRecordings({
        camera_id: cameraId || undefined,
        format: format || undefined,
        pinned: pinned === 'true' ? true : pinned === 'false' ? false : undefined,
        offset,
        limit
      });
      recordings = response.recordings;
      totalRecordings = response.total || 0;
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadRecordings');
    } finally {
      loading = false;
    }
  }

  async function loadCameras() {
    try {
      cameras = await listCameras();
    } catch (e) {
      console.error(t('common.failedLoadCameras'), e);
    }
  }

  // Actions
  async function togglePin(recording: Recording) {
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
    if (!deleteConfirm) return;

    try {
      await deleteRecording(deleteConfirm.id);
      recordings = recordings.filter(r => r.id !== deleteConfirm.id);
      showToast(t('common.recordingDeleted'), 'success');
      deleteConfirm = null;
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedDeleteRecording'), 'error');
    }
  }

  function viewRecording(recording: Recording) {
    window.location.hash = `#/recordings/${recording.id}`;
  }

  // Lifecycle
  onMount(() => {
    loadCameras();
    loadRecordings();

    // Auto-refresh every 30 seconds
    refreshInterval = window.setInterval(() => {
      loadRecordings();
    }, 30000);

    return () => {
      if (refreshInterval) clearInterval(refreshInterval);
    };
  });

  // Watch filter changes — debounce to avoid double-fire with onMount
  let loadTimeout: number;
  $effect(() => {
    // Read all filter variables to track them as dependencies
    const _ = [cameraId, format, pinned, offset, limit];
    clearTimeout(loadTimeout);
    loadTimeout = window.setTimeout(() => loadRecordings(), 100);
    return () => clearTimeout(loadTimeout);
  });

  // Pagination calculations
  let currentPage = $derived(Math.floor(offset / limit) + 1);
  let totalPages = $derived(Math.ceil(totalRecordings / limit));
  let startRecordings = $derived(offset + 1);
  let endRecordings = $derived(Math.min(offset + recordings.length, totalRecordings));
  
  // Handle page change
  function handlePageChange(newPage: number) {
    offset = (newPage - 1) * limit;
    window.scrollTo(0, 0);
  }
</script>

  <div class="min-h-screen th-bg-primary pt-[68px]">

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold th-text-primary mb-4">{t('recordings.title')}</h2>

      <!-- Filters -->
      <div class="card p-5 mb-6 border th-border">
        <div class="flex flex-wrap gap-3 items-end">
          <div class="flex-1 min-w-[180px]">
            <label for="camera" class="input-label">{t('recordings.camera')}</label>
            <select id="camera" class="input" bind:value={cameraId}>
              <option value="">{t('recordings.allCameras')}</option>
              {#each cameras as camera}
                <option value={camera.id}>{camera.name}</option>
              {/each}
            </select>
          </div>
          <div class="flex-1 min-w-[140px]">
            <label for="format" class="input-label">{t('recordings.format')}</label>
            <select id="format" class="input" bind:value={format}>
              <option value="">{t('recordings.allFormats')}</option>
              <option value="h264">{t('recordings.h264')}</option>
              <option value="mjpeg">{t('recordings.mjpeg')}</option>
            </select>
          </div>
          <div class="flex-1 min-w-[140px]">
            <label for="pinned" class="input-label">{t('recordings.status')}</label>
            <select id="pinned" class="input" bind:value={pinned}>
              <option value="">{t('recordings.all')}</option>
              <option value="true">{t('recordings.pinned')}</option>
              <option value="false">{t('recordings.notPinned')}</option>
            </select>
          </div>
        </div>
      </div>
    </div>

    <!-- Error message -->
    {#if error}
      <div class="mb-4 p-4 bg-[rgba(239,68,68,0.3)] border th-border-danger rounded-md th-color-danger" aria-live="polite">
        {error}
      </div>
    {/if}

    <!-- Recordings table -->
    <div class="card border th-border">
      {#if loading && recordings.length === 0}
        <div class="p-8 flex justify-center">
          <div class="spinner spinner-lg"></div>
        </div>
      {:else if recordings.length === 0}
        <div class="p-8 text-center th-text-muted">
          {t('recordings.noRecordings')}
        </div>
      {:else}
        <div class="table-container th-border">
          <table class="table">
            <thead>
              <tr>
                <th class="min-w-[100px]">{t('recordings.tableCamera')}</th>
                <th class="min-w-[80px]">{t('recordings.tableFormat')}</th>
                <th class="min-w-[80px]">{t('recordings.tableDuration')}</th>
                <th class="min-w-[80px]">{t('recordings.tableSize')}</th>
                <th class="min-w-[120px]">{t('recordings.tableDate')}</th>
                <th class="min-w-[80px]">{t('recordings.tableStatus')}</th>
                <th class="text-right min-w-[140px]">{t('recordings.tableActions')}</th>
              </tr>
            </thead>
            <tbody>
              {#each recordings as recording}
                <tr class="transition-all duration-200 hover:th-bg-hover">
                  <td class="font-medium th-text-primary">{recording.camera_id}</td>
                  <td>
                    <span class="badge badge-neutral">
                      {t(`recording.format.${recording.format}`)}
                    </span>
                  </td>
                  <td class="font-mono text-sm">{formatDuration(recording.duration)}</td>
                  <td>{formatFileSize(recording.file_size)}</td>
                  <td class="whitespace-nowrap">{formatDate(recording.started_at)}</td>
                  <td>
                    {#if recording.pinned}
                      <span class="badge badge-warning">{t('recordings.pinnedBadge')}</span>
                    {/if}
                  </td>
                  <td class="text-right">
                    <div class="flex justify-end gap-1">
                      <button
                        on:click={() => viewRecording(recording)}
                        class="btn btn-ghost px-3 py-1.5 text-sm transition-all duration-200"
                        title={t('recordings.view')}
                      >
                        {t('recordings.view')}
                      </button>
                      <button
                        on:click={() => togglePin(recording)}
                        class="btn btn-ghost px-2 py-1.5 text-sm transition-all duration-200"
                        title={recording.pinned ? t('recordings.unpin') : t('recordings.pin')}
                      >
                        {#if recording.pinned}
                          <Pin size={16} />
                        {:else}
                          <MapPin size={16} />
                        {/if}
                      </button>
                      <button
                        on:click={() => deleteConfirm = recording}
                        class="btn btn-ghost px-2 py-1.5 text-sm th-color-danger transition-all duration-200"
                        title={t('recordings.delete')}
                      >
                        <Trash2 size={16} />
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

      {#if totalPages > 1}
        <!-- Page info -->
        <div class="px-4 py-2 border-t th-border">
          <span class="text-sm th-text-muted">
            {t('recordings.showing', { start: String(startRecordings), end: String(endRecordings), total: String(totalRecordings) })}
          </span>
        </div>
        
        <!-- Pagination -->
        <Pagination 
          {currentPage}
          {totalPages}
          onPageChange={handlePageChange}
        />
      {/if}

      <!-- Loading indicator for refresh -->
      {#if loading && recordings.length > 0}
        <div class="px-4 py-2 th-bg-secondary border-t th-border text-center">
          <span class="text-sm th-text-muted">{t('recordings.refreshing')}</span>
        </div>
      {/if}
      {/if}
    </div>
  </main>

  <!-- Delete confirmation modal -->
  {#if deleteConfirm}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('recordings.deleteTitle')}</h3>
        <p class="th-text-secondary mb-6">
          {t('recordings.deleteMessage', { camera_id: deleteConfirm.camera_id })}
        </p>
        <div class="flex gap-3 justify-end">
          <button
            on:click={() => deleteConfirm = null}
            class="btn btn-secondary"
          >
            {t('recordings.cancel')}
          </button>
          <button
            on:click={confirmDelete}
            class="btn btn-danger"
          >
            {t('recordings.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>
