<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import {
    listRecordings,
    listCameras,
    deleteRecording,
    pinRecording,
    unpinRecording,
    logout
  } from '$lib/api';
  import type { Recording, Camera } from '$lib/api';
  import Pagination from '../components/Pagination.svelte';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t, onLangChange, getCurrentLang } from '$lib/i18n';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';

  // Re-render on language change
  let lang = getCurrentLang();
  const unsubscribe = onLangChange(() => { lang = getCurrentLang(); });

  onDestroy(() => { unsubscribe(); });

  // Filter state
  let cameraId = '';
  let format = '';
  let pinned = '';
  let cameras: Camera[] = [];
  let limit = 50;
  let offset = 0;

  // Data state
  let recordings: Recording[] = [];
  let totalRecordings = 0;
  let loading = false;
  let error = '';
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
      deleteConfirm = null;
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedDeleteRecording');
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

  // Watch filter changes
  $: loadRecordings();
  
  // Pagination calculations
  $: currentPage = Math.floor(offset / limit) + 1;
  $: totalPages = Math.ceil(totalRecordings / limit);
  $: startRecordings = offset + 1;
  $: endRecordings = Math.min(offset + recordings.length, totalRecordings);
  
  // Handle page change
  function handlePageChange(newPage: number) {
    offset = (newPage - 1) * limit;
    window.scrollTo(0, 0);
  }
</script>

  <div class="min-h-screen bg-slate-900">
  <!-- Header -->
  <header class="bg-slate-800 border-b border-slate-700">
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
      <div class="flex items-center justify-between h-16">
        <div class="flex items-center gap-4">
          <h1 class="text-xl font-bold text-slate-100">MiBee NVR</h1>
          <nav class="flex gap-4">
            <a href="#/recordings" class="text-cyan-500 font-medium">{t('nav.recordings')}</a>
            <a href="#/cameras" class="text-slate-300 hover:text-slate-100 transition-colors">{t('nav.cameras')}</a>
<a href="#/stats" class="text-slate-300 hover:text-slate-100 transition-colors">{t('nav.stats')}</a>
<a href="#/settings" class="text-slate-300 hover:text-slate-100 transition-colors">{t('nav.settings')}</a>
          </nav>
        </div>
        <div class="flex items-center gap-3">
          <LanguageSwitcher />
          <button on:click={logout} class="btn btn-ghost">
            {t('nav.logout')}
          </button>
        </div>
      </div>
    </div>
  </header>

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold text-slate-100 mb-4">{t('recordings.title')}</h2>

      <!-- Filters -->
      <div class="card p-4 mb-4 border border-slate-700/60">
        <div class="flex flex-wrap gap-4 items-end">
          <div class="flex-1 min-w-[200px]">
            <label for="camera" class="input-label">{t('recordings.camera')}</label>
            <select id="camera" class="input" bind:value={cameraId}>
              <option value="">{t('recordings.allCameras')}</option>
              {#each cameras as camera}
                <option value={camera.id}>{camera.name}</option>
              {/each}
            </select>
          </div>
          <div class="flex-1 min-w-[150px]">
            <label for="format" class="input-label">{t('recordings.format')}</label>
            <select id="format" class="input" bind:value={format}>
              <option value="">{t('recordings.allFormats')}</option>
              <option value="h264">{t('recordings.h264')}</option>
              <option value="mjpeg">{t('recordings.mjpeg')}</option>
            </select>
          </div>
          <div class="flex-1 min-w-[150px]">
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
      <div class="mb-4 p-4 bg-red-900/30 border border-red-700 rounded-md text-red-300">
        {error}
      </div>
    {/if}

    <!-- Recordings table -->
    <div class="card border border-slate-700/60">
      {#if loading && recordings.length === 0}
        <div class="p-8 flex justify-center">
          <div class="spinner spinner-lg"></div>
        </div>
      {:else if recordings.length === 0}
        <div class="p-8 text-center text-slate-400">
          {t('recordings.noRecordings')}
        </div>
      {:else}
        <div class="table-container border-slate-700/50">
          <table class="table">
            <thead>
              <tr>
                <th>{t('recordings.tableCamera')}</th>
                <th>{t('recordings.tableFormat')}</th>
                <th>{t('recordings.tableDuration')}</th>
                <th>{t('recordings.tableSize')}</th>
                <th>{t('recordings.tableDate')}</th>
                <th>{t('recordings.tableStatus')}</th>
                <th class="text-right">{t('recordings.tableActions')}</th>
              </tr>
            </thead>
            <tbody>
              {#each recordings as recording}
                <tr>
                  <td class="font-medium text-slate-200">{recording.camera_id}</td>
                  <td>
                    <span class="badge badge-neutral">
                      {recording.format === 'h264' ? 'MP4' : 'JPEG'}
                    </span>
                  </td>
                  <td>{formatDuration(recording.duration)}</td>
                  <td>{formatFileSize(recording.file_size)}</td>
                  <td>{formatDate(recording.started_at)}</td>
                  <td>
                    {#if recording.pinned}
                      <span class="badge badge-warning">{t('recordings.pinnedBadge')}</span>
                    {/if}
                  </td>
                  <td class="text-right">
                    <div class="flex justify-end gap-2">
                      <button
                        on:click={() => viewRecording(recording)}
                        class="btn btn-ghost px-3 py-1 text-sm"
                        title={t('recordings.view')}
                      >
                        {t('recordings.view')}
                      </button>
                      <button
                        on:click={() => togglePin(recording)}
                        class="btn btn-ghost px-3 py-1 text-sm"
                        title={recording.pinned ? t('recordings.unpin') : t('recordings.pin')}
                      >
                        {recording.pinned ? '📌' : '📍'}
                      </button>
                      <button
                        on:click={() => deleteConfirm = recording}
                        class="btn btn-ghost px-3 py-1 text-sm text-red-400 hover:text-red-300"
                        title={t('recordings.delete')}
                      >
                        🗑️
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
        <div class="px-4 py-2 border-t border-slate-700">
          <span class="text-sm text-slate-400">
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
        <div class="px-4 py-2 bg-slate-800/50 border-t border-slate-700 text-center">
          <span class="text-sm text-slate-400">{t('recordings.refreshing')}</span>
        </div>
      {/if}
      {/if}
    </div>
  </main>

  <!-- Delete confirmation modal -->
  {#if deleteConfirm}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold text-slate-100 mb-4">{t('recordings.deleteTitle')}</h3>
        <p class="text-slate-300 mb-6">
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
