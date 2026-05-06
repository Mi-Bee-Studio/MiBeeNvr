<script lang="ts">
import { onMount, onDestroy } from 'svelte';
  import {
    listRecordings,
    listCameras,
    deleteRecording,
    batchDeleteRecordings,
    pinRecording,
    unpinRecording
  } from '$lib/api';
  import { getItemsPerPage, getAutoRefresh, parseRefreshInterval } from '../lib/preferences';

  import type { Recording, Camera } from '$lib/api';
  import Pagination from '../components/Pagination.svelte';
  import { t } from '$lib/i18n';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { Pin, MapPin, Trash2, Search, ChevronUp, ChevronDown, CheckSquare, Square, ArrowUp, Video, AlertCircle, Eye } from 'lucide-svelte';

  // Helper function to get camera name by ID
  function getCameraName(cameraId: string): string {
    const camera = cameras.find(c => c.id === cameraId);
    return camera ? camera.name : cameraId; // Fallback to camera_id if camera not found
  }


  // Filter state
  let cameraId = $state('');
  let format = $state('');
  let searchQuery = $state('');
  const pad = (n) => String(n).padStart(2, '0');
  const toLocalDT = (d) => `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  let startDate = $state(toLocalDT(new Date(Date.now() - 3600000)));
  let endDate = $state(toLocalDT(new Date()));
  let cameras = $state<Camera[]>([]);
  let limit = $state(getItemsPerPage());
  let offset = $state(0);
  let sortBy = $state('started_at');
  let sortOrder = $state<'asc' | 'desc'>('desc');
  let selectedIds = $state<Set<string>>(new Set());
  let showBatchDeleteConfirm = $state(false);

  // Data state
  let recordings = $state<Recording[]>([]);
  let totalRecordings = $state(0);
  let loading = $state(false);
  let error = $state('');
  let deleteConfirm = $state<Recording | null>(null);
  let showBackToTop = $state(false);

  function toggleSelectAll() {
    if (selectedIds.size === filteredRecordings.length) {
      selectedIds = new Set();
    } else {
      selectedIds = new Set(filteredRecordings.map(r => r.id));
    }
  }

  function toggleSelect(id: string) {
    const newSet = new Set(selectedIds);
    if (newSet.has(id)) {
      newSet.delete(id);
    } else {
      newSet.add(id);
    }
    selectedIds = newSet;
  }

  async function confirmBatchDelete() {
    try {
      await batchDeleteRecordings(Array.from(selectedIds));
      showToast(t('recordings.batchDeleteSuccess', { count: String(selectedIds.size) }), 'success');
      selectedIds = new Set();
      showBatchDeleteConfirm = false;
      loadRecordings();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('recordings.batchDeleteFailed'), 'error');
    }
  }
  let filteredRecordings = $derived.by<Recording[]>(() => {
    if (searchQuery) {
      const q = searchQuery.toLowerCase();
      return recordings.filter(r => getCameraName(r.camera_id).toLowerCase().includes(q));
    }
    return recordings;
  });

  function handleSort(field: string) {
    if (sortBy === field) {
      sortOrder = sortOrder === 'asc' ? 'desc' : 'asc';
    } else {
      sortBy = field;
      sortOrder = 'asc';
    }
  }

  // Auto-refresh interval
  let refreshInterval: number;


  // Get the current auto-refresh preference
  function getRefreshInterval(): number {
    return parseRefreshInterval(getAutoRefresh());
  }
  // Load data
  async function loadRecordings() {
    loading = true;
    error = '';

    try {
      const response = await listRecordings({
        camera_id: cameraId || undefined,
        format: format || undefined,
        start: startDate ? new Date(startDate).toISOString() : undefined,
        end: endDate ? new Date(endDate).toISOString() : undefined,
        offset,
        limit,
        sort_by: sortBy,
        order: sortOrder
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


  function clearFilters() {
    searchQuery = '';
    cameraId = '';
    format = '';
    startDate = '';
    endDate = '';
  }

  function viewRecording(recording: Recording) {
    window.location.hash = `#/recordings/${recording.id}`;
  }

  // Lifecycle
  onMount(() => {
    loadCameras();
    loadRecordings();

    // Auto-refresh using preference
    refreshInterval = window.setInterval(() => {
      loadRecordings();
    }, getRefreshInterval());

    // Scroll listener for back to top button
    const handleScroll = () => {
      showBackToTop = window.scrollY > 300;
    };
    window.addEventListener('scroll', handleScroll);

    return () => {
      if (refreshInterval) clearInterval(refreshInterval);
      window.removeEventListener('scroll', handleScroll);
    };
  });

  // Watch filter changes — debounce to avoid double-fire with onMount
  let loadTimeout: number;
  $effect(() => {
    // Read all filter variables to track them as dependencies
    const _ = [cameraId, format, startDate, endDate, offset, limit, sortBy, sortOrder];
    clearTimeout(loadTimeout);
    loadTimeout = window.setTimeout(() => loadRecordings(), 100);
    return () => clearTimeout(loadTimeout);
  });

  // Handle preference changes
  $effect(() => {
    // Update refresh interval when auto-refresh preference changes
    if (refreshInterval) {
      clearInterval(refreshInterval);
    }
    refreshInterval = window.setInterval(() => {
      loadRecordings();
    }, getRefreshInterval());
    
    // Update limit when items per page preference changes
    limit = getItemsPerPage();
    
    return () => {
      if (refreshInterval) {
        clearInterval(refreshInterval);
      }
    };
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
      <div class="card p-5 mb-6 border th-border space-y-3">
        <div class="flex flex-wrap gap-3 items-end">
          <div class="flex-1 min-w-[160px]">
            <label for="camera" class="input-label">{t('recordings.camera')}</label>
            <select id="camera" class="input" bind:value={cameraId}>
              <option value="">{t('recordings.allCameras')}</option>
              {#each cameras as camera}
                <option value={camera.id}>{camera.name}</option>
              {/each}
            </select>
          </div>
          <div class="flex-1 min-w-[120px]">
            <label for="format" class="input-label">{t('recordings.format')}</label>
            <select id="format" class="input" bind:value={format}>
              <option value="">{t('recordings.allFormats')}</option>
              <option value="h264">{t('recordings.h264')}</option>
              <option value="mjpeg">{t('recordings.mjpeg')}</option>
              <option value="h265">H.265 (HEVC)</option>
            </select>
          </div>
          <div class="flex-1 min-w-[180px]">
            <label class="input-label">{t('recordings.search')}</label>
            <div class="relative">
              <Search size={16} class="absolute left-3 top-1/2 -translate-y-1/2 th-text-tertiary" />
              <input
                type="text"
                class="input pl-9"
                placeholder={t('recordings.search')}
                bind:value={searchQuery}
              />
            </div>
          </div>
        </div>
        <!-- Time range row -->
        <div class="flex flex-wrap gap-4 sm:gap-3 items-stretch sm:items-end mt-1">
          <div class="flex-1 min-w-0">
            <label class="input-label">{t('recordings.startDate')}</label>
            <div class="flex flex-col md:flex-row gap-2 items-stretch md:items-center">
              <input type="datetime-local" class="input flex-1" bind:value={startDate} />
              <span class="th-text-tertiary shrink-0">~</span>
              <input type="datetime-local" class="input flex-1" bind:value={endDate} />
          </div>
          <div class="flex flex-col sm:flex-row gap-2 items-stretch sm:items-center shrink-0">
            <button class="btn btn-secondary btn-sm !px-3 !py-1" onclick={clearFilters}>{t('recordings.clearFilters')}</button>
          </div>
        </div>
      </div>

    <!-- Error message -->
    {#if error}
      <div class="card border th-border-danger p-8 text-center">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadRecordings} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    <!-- Recordings table -->
    <div class="card border th-border">
      {#if loading && recordings.length === 0}
        <div class="p-6 space-y-4">
          <!-- Skeleton header -->
          <div class="flex justify-between items-center">
            <div class="h-8 w-48 th-bg-tertiary rounded animate-pulse"></div>
            <div class="h-8 w-24 th-bg-tertiary rounded animate-pulse"></div>
          </div>
          <!-- Skeleton filter bar -->
          <div class="h-12 th-bg-tertiary rounded animate-pulse"></div>
          <!-- Skeleton table -->
          <div class="space-y-3">
            {#each Array(5) as _}
              <div class="flex gap-4 items-center">
                <div class="h-4 w-4 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-24 th-bg-tertiary rounded animate-pulse ml-auto"></div>
              </div>
            {/each}
          </div>
        </div>
      {:else if recordings.length === 0}
        <div class="p-12 text-center">
          <div class="flex justify-center mb-4 th-text-muted">
            <Video size={48} />
          </div>
          <h3 class="text-lg font-medium th-text-primary mb-2">{t('recordings.noRecordings')}</h3>
          <p class="text-sm th-text-muted mb-4">{t('recordings.noRecordingsHint')}</p>
          <button onclick={clearFilters} class="btn btn-primary btn-sm">{t('recordings.clearFilters')}</button>
        </div>
      {:else}
        <div class="table-container th-border">
          <table class="table">
            <thead>
              <tr>
                <th class="w-10">
                  <button onclick={toggleSelectAll} class="th-text-secondary hover:th-text-primary transition-colors">
                    {#if selectedIds.size === filteredRecordings.length && filteredRecordings.length > 0}
                      <CheckSquare size={18} />
                    {:else}
                      <Square size={18} />
                    {/if}
                  </button>
                </th>
                <th class="min-w-[80px] sm:min-w-[100px] cursor-pointer hover:th-bg-tertiary select-none" onclick={() => handleSort('camera_id')}>
                  <span class="inline-flex items-center gap-1">
                    {t('recordings.tableCamera')}
                    {#if sortBy === 'camera_id'}
                      {#if sortOrder === 'asc'}
                        <ChevronUp size={14} />
                      {:else}
                        <ChevronDown size={14} />
                      {/if}
                    {/if}
                  </span>
                </th>
                <th class="min-w-[80px]">{t('recordings.tableFormat')}</th>
                <th class="min-w-[80px] cursor-pointer hover:th-bg-tertiary select-none" onclick={() => handleSort('duration')}>
                  <span class="inline-flex items-center gap-1">
                    {t('recordings.tableDuration')}
                    {#if sortBy === 'duration'}
                      {#if sortOrder === 'asc'}
                        <ChevronUp size={14} />
                      {:else}
                        <ChevronDown size={14} />
                      {/if}
                    {/if}
                  </span>
                </th>
                <th class="min-w-[80px] cursor-pointer hover:th-bg-tertiary select-none" onclick={() => handleSort('file_size')}>
                  <span class="inline-flex items-center gap-1">
                    {t('recordings.tableSize')}
                    {#if sortBy === 'file_size'}
                      {#if sortOrder === 'asc'}
                        <ChevronUp size={14} />
                      {:else}
                        <ChevronDown size={14} />
                      {/if}
                    {/if}
                  </span>
                </th>
                <th class="min-w-[100px] sm:min-w-[120px] cursor-pointer hover:th-bg-tertiary select-none" onclick={() => handleSort('started_at')}>
                  <span class="inline-flex items-center gap-1">
                    {t('recordings.tableDate')}
                    {#if sortBy === 'started_at'}
                      {#if sortOrder === 'asc'}
                        <ChevronUp size={14} />
                      {:else}
                        <ChevronDown size={14} />
                      {/if}
                    {/if}
                  </span>
                </th>
                <th class="hidden sm:table-cell min-w-[80px]">{t('recordings.tableStatus')}</th>
                <th class="text-right min-w-[100px] sm:min-w-[140px]">{t('recordings.tableActions')}</th>
              </tr>
            </thead>
            <tbody>
              {#each filteredRecordings as recording}
                <tr class="transition-all duration-200 hover:th-bg-hover">
                  <td>
                    <input
                      type="checkbox"
                      checked={selectedIds.has(recording.id)}
                      onchange={() => toggleSelect(recording.id)}
                      class="w-4 h-4 rounded cursor-pointer"
                    />
                  </td>
                  <td>
                    <div class="flex flex-col">
                      <span class="font-medium th-text-primary">{getCameraName(recording.camera_id)}</span>
                      <span class="text-xs th-text-tertiary hidden sm:inline">{recording.camera_id}</span>
                    </div>
                  </td>
                  <td>
                    <span class="badge badge-neutral text-xs">
                      {(recording.format === 'h264' || recording.format === 'h265') ? 'MP4' : 'JPEG'}
                    </span>
                  </td>
                  <td class="font-mono text-sm">{formatDuration(recording.duration)}</td>
                  <td>{formatFileSize(recording.file_size)}</td>
                  <td class="whitespace-nowrap">{formatDate(recording.started_at)}</td>
                  <td class="hidden sm:table-cell">
                    {#if recording.pinned}
                      <span class="badge badge-warning">{t('recordings.pinnedBadge')}</span>
                    {/if}
                  </td>
                  <td class="text-right">
                    <div class="flex justify-end gap-1">
                      <button
                        onclick={() => viewRecording(recording)}
                        class="btn btn-ghost px-2 sm:px-3 py-1.5 text-sm transition-all duration-200"
                        title={t('recordings.view')}
                      >
                        <span class="hidden sm:inline">{t('recordings.view')}</span>
                        <Eye size={16} class="sm:hidden" />
                      </button>
                      <button
                        onclick={() => togglePin(recording)}
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
                        onclick={() => deleteConfirm = recording}
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
  </div>


  <!-- Floating batch action bar -->
  {#if selectedIds.size > 0}
    <div class="fixed bottom-6 left-1/2 -translate-x-1/2 z-40 flex items-center gap-3 px-4 py-2.5 rounded-lg shadow-lg border th-border th-bg-primary">
      <span class="text-sm font-medium th-text-primary">
        {t('recordings.selected', { count: String(selectedIds.size) })}
      </span>
      <button
        onclick={() => showBatchDeleteConfirm = true}
        class="btn btn-danger btn-sm"
      >
        {t('recordings.deleteSelected')}
      </button>
      <button
        onclick={() => selectedIds = new Set()}
        class="btn btn-ghost btn-sm"
      >
        {t('recordings.cancel')}
      </button>
    </div>
  {/if}

  <!-- Batch delete confirmation modal -->
  {#if showBatchDeleteConfirm}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('recordings.batchDeleteTitle')}</h3>
        <p class="th-text-secondary mb-6">
          {t('recordings.batchDeleteMessage', { count: String(selectedIds.size) })}
        </p>
        <div class="flex gap-3 justify-end">
          <button onclick={() => showBatchDeleteConfirm = false} class="btn btn-secondary">
            {t('recordings.cancel')}
          </button>
          <button onclick={confirmBatchDelete} class="btn btn-danger">
            {t('recordings.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}

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
            onclick={() => deleteConfirm = null}
            class="btn btn-secondary"
          >
            {t('recordings.cancel')}
          </button>
          <button
            onclick={confirmDelete}
            class="btn btn-danger"
          >
            {t('recordings.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}
  <!-- Back to top button -->
  {#if showBackToTop}
    <button
      onclick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}
      class="fixed bottom-6 right-6 z-30 w-10 h-10 rounded-full bg-primary text-primary-foreground shadow-lg flex items-center justify-center hover:bg-primary/90 transition-all duration-300"
      title={t('recordings.backToTop')}
    >
      <ArrowUp size={20} />
    </button>
  {/if}
