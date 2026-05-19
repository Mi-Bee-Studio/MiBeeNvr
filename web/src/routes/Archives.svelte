<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listArchives,
    listArchiveRecordings,
    deleteArchiveGroup,
    deleteArchiveRecording,
    setArchiveRetention
  } from '$lib/api';
  import type { ArchiveGroup, Recording } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatDate, formatFileSize, formatDuration } from '$lib/format';
  import { showToast } from '$lib/toast';
  import Pagination from '../components/Pagination.svelte';
  import {
    Archive, Trash2, Download, Play, Clock, Settings,
    ChevronDown, ChevronRight, RefreshCw, AlertCircle, Video
  } from 'lucide-svelte';

  // State
  let archiveGroups = $state<ArchiveGroup[]>([]);
  let loading = $state(false);
  let error = $state('');
  let expandedGroupId = $state<string | null>(null);
  let recordings = $state<Recording[]>([]);
  let recordingsLoading = $state(false);
  let recordingsTotal = $state(0);
  let recordingsOffset = $state(0);
  let recordingsLimit = $state(20);

  // Dialogs
  let showRetDialog = $state(false);
  let showDeleteDialog = $state(false);
  let showDeleteRecordingDialog = $state(false);
  let selectedGroup = $state<ArchiveGroup | null>(null);
  let selectedRecording = $state<Recording | null>(null);
  let retentionDays = $state(30);

  // Auto-refresh
  let refreshTimer: ReturnType<typeof setInterval>;
  let abortController: AbortController | null = null;

  // Load archive groups
  async function loadArchives() {
    if (abortController) {
      abortController.abort();
    }
    abortController = new AbortController();

    loading = true;
    error = '';

    try {
      const response = await listArchives(abortController.signal);
      archiveGroups = response.archives || [];
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      error = e instanceof Error ? e.message : String(t('common.error'));
    } finally {
      loading = false;
    }
  }

  // Load recordings for an expanded group
  async function loadRecordings(cameraId: string) {
    recordingsLoading = true;
    try {
      const response = await listArchiveRecordings(cameraId, {
        offset: recordingsOffset,
        limit: recordingsLimit,
        signal: abortController?.signal
      });
      recordings = response.recordings || [];
      recordingsTotal = response.total || 0;
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    } finally {
      recordingsLoading = false;
    }
  }

  // Toggle expand/collapse group
  function toggleGroup(group: ArchiveGroup) {
    if (expandedGroupId === group.id) {
      expandedGroupId = null;
      recordings = [];
      recordingsTotal = 0;
      recordingsOffset = 0;
    } else {
      expandedGroupId = group.id;
      recordingsOffset = 0;
      loadRecordings(group.id);
    }
  }

  // Delete archive group
  function openDeleteGroup(group: ArchiveGroup) {
    selectedGroup = group;
    showDeleteDialog = true;
  }

  async function confirmDeleteGroup() {
    if (!selectedGroup) return;
    try {
      await deleteArchiveGroup(selectedGroup.id);
      archiveGroups = archiveGroups.filter(g => g.id !== selectedGroup!.id);
      if (expandedGroupId === selectedGroup.id) {
        expandedGroupId = null;
        recordings = [];
      }
      showToast(t('archives.deleteSuccess'), 'success');
      showDeleteDialog = false;
      selectedGroup = null;
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    }
  }

  // Delete single recording
  function openDeleteRecording(rec: Recording) {
    selectedRecording = rec;
    showDeleteRecordingDialog = true;
  }

  async function confirmDeleteRecording() {
    if (!selectedRecording || !expandedGroupId) return;
    try {
      await deleteArchiveRecording(expandedGroupId, selectedRecording.id);
      recordings = recordings.filter(r => r.id !== selectedRecording!.id);
      recordingsTotal--;
      showToast(t('archives.deleteRecordingSuccess'), 'success');
      showDeleteRecordingDialog = false;
      selectedRecording = null;
      // Refresh archives to update counts
      loadArchives();
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    }
  }

  // Retention dialog
  function openRetDialog(group: ArchiveGroup) {
    selectedGroup = group;
    retentionDays = group.archive_retention_days;
    showRetDialog = true;
  }

  async function confirmSetRetention() {
    if (!selectedGroup) return;
    try {
      await setArchiveRetention(selectedGroup.id, retentionDays);
      archiveGroups = archiveGroups.map(g =>
        g.id === selectedGroup!.id ? { ...g, archive_retention_days: retentionDays } : g
      );
      showToast(t('archives.retentionUpdated'), 'success');
      showRetDialog = false;
      selectedGroup = null;
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    }
  }

  // Pagination for recordings
  let currentRecordingPage = $derived(Math.floor(recordingsOffset / recordingsLimit) + 1);
  let totalRecordingPages = $derived(Math.ceil(recordingsTotal / recordingsLimit));

  function handleRecordingPageChange(newPage: number) {
    recordingsOffset = (newPage - 1) * recordingsLimit;
    if (expandedGroupId) {
      loadRecordings(expandedGroupId);
    }
  }

  // Play recording — navigate to recording detail
  function playRecording(rec: Recording) {
    window.location.hash = `#/recordings/${rec.id}`;
  }

  // Download recording
  function downloadRecording(rec: Recording) {
    const url = `/api/archives/${expandedGroupId}/recordings/${rec.id}/download`;
    const a = document.createElement('a');
    a.href = url;

    // Add auth header via fetch
    const encoded = localStorage.getItem('mibee_nvr_auth');
    if (encoded) {
      const decoded = atob(encoded);
      const [, password] = decoded.split(':');
      // Use fetch + blob for auth
      fetch(url, {
        headers: {
          'Authorization': `Basic ${encoded}`
        }
      })
        .then(res => {
          if (!res.ok) throw new Error(`HTTP ${res.status}`);
          return res.blob();
        })
        .then(blob => {
          const objectUrl = URL.createObjectURL(blob);
          const link = document.createElement('a');
          link.href = objectUrl;
          link.download = `archive_${rec.camera_id}_${rec.id}.mp4`;
          document.body.appendChild(link);
          link.click();
          document.body.removeChild(link);
          URL.revokeObjectURL(objectUrl);
        })
        .catch(() => {
          showToast(t('common.error'), 'error');
        });
      return;
    }
    a.download = `archive_${rec.camera_id}_${rec.id}.mp4`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }

  // Format retention display
  function formatRetention(days: number): string {
    if (days === 0) return t('archives.keepForever');
    return `${days} ${t('archives.retentionDays')}`;
  }

  // Lifecycle
  onMount(() => {
    loadArchives();

    refreshTimer = window.setInterval(() => {
      loadArchives();
    }, 30000);

    return () => {
      if (refreshTimer) clearInterval(refreshTimer);
      if (abortController) { abortController.abort(); abortController = null; }
    };
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-3">
        <Archive size={28} class="th-text-secondary" />
        <h2 class="text-2xl font-bold th-text-primary">{t('archives.title')}</h2>
      </div>
      <button
        onclick={loadArchives}
        class="btn btn-secondary btn-sm flex items-center gap-2"
        disabled={loading}
      >
        <RefreshCw size={16} class={loading ? 'animate-spin' : ''} />
        {t('common.retry')}
      </button>
    </div>

    <!-- Error -->
    {#if error}
      <div class="card border th-border-danger p-8 text-center">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadArchives} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    <!-- Loading skeleton -->
    {#if loading && archiveGroups.length === 0}
      <div class="space-y-4">
        {#each Array(3) as _}
          <div class="card border th-border p-5">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-3">
                <div class="h-5 w-5 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-5 w-40 th-bg-tertiary rounded animate-pulse"></div>
              </div>
              <div class="flex gap-2">
                <div class="h-8 w-24 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-8 w-24 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-8 w-24 th-bg-tertiary rounded animate-pulse"></div>
              </div>
            </div>
            <div class="flex gap-6 mt-3">
              <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div>
            </div>
          </div>
        {/each}
      </div>
    {:else if archiveGroups.length === 0 && !error}
      <!-- Empty state -->
      <div class="card border th-border p-12 text-center">
        <div class="flex justify-center mb-4 th-text-muted">
          <Video size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('archives.noArchives')}</h3>
        <p class="text-sm th-text-muted">{t('archives.noArchivesHint')}</p>
      </div>
    {:else}
      <!-- Archive groups -->
      <div class="space-y-3">
        {#each archiveGroups as group (group.id)}
          <div class="card border th-border overflow-hidden">
            <!-- Group header -->
            <div
              class="w-full p-5 text-left hover:th-bg-hover transition-colors duration-200 cursor-pointer"
              onclick={() => toggleGroup(group)}
              role="button"
              tabindex="0"
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleGroup(group); } }}
            >
              <div class="flex items-center justify-between gap-4">
                <div class="flex items-center gap-3 min-w-0">
                  {#if expandedGroupId === group.id}
                    <ChevronDown size={20} class="th-text-secondary shrink-0" />
                  {:else}
                    <ChevronRight size={20} class="th-text-secondary shrink-0" />
                  {/if}
                  <div class="min-w-0">
                    <h3 class="font-semibold th-text-primary truncate">{group.name}</h3>
                    <div class="flex flex-wrap gap-x-5 gap-y-1 mt-1.5 text-sm th-text-secondary">
                      <span class="flex items-center gap-1.5">
                        <Video size={14} />
                        {group.recording_count} {t('archives.recordings')}
                      </span>
                      <span class="flex items-center gap-1.5">
                        <Archive size={14} />
                        {formatFileSize(group.total_size)} {t('archives.totalSize').toLowerCase()}
                      </span>
                      <span class="flex items-center gap-1.5">
                        <Clock size={14} />
                        {t('archives.archivedAt')}: {formatDate(group.archived_at)}
                      </span>
                      <span class="flex items-center gap-1.5">
                        <Settings size={14} />
                        {formatRetention(group.archive_retention_days)}
                      </span>
                    </div>
                  </div>
                </div>
                <div class="flex items-center gap-2 shrink-0" role="group" aria-label={t('archives.actions')}>
                  <button
                    class="btn btn-ghost btn-sm"
                    onclick={(e) => { e.stopPropagation(); openRetDialog(group); }}
                    title={t('archives.setRetention')}
                  >
                    <Clock size={16} />
                  </button>
                  <button
                    class="btn btn-ghost btn-sm th-color-danger"
                    onclick={(e) => { e.stopPropagation(); openDeleteGroup(group); }}
                    title={t('archives.deleteGroup')}
                  >
                    <Trash2 size={16} />
                  </button>
                </div>
              </div>
            </div>

            <!-- Expanded recordings -->
            {#if expandedGroupId === group.id}
              <div class="border-t th-border">
                {#if recordingsLoading}
                  <div class="p-6 space-y-3">
                    {#each Array(3) as _}
                      <div class="flex gap-4 items-center">
                        <div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div>
                        <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
                        <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
                        <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse ml-auto"></div>
                      </div>
                    {/each}
                  </div>
                {:else if recordings.length === 0}
                  <div class="p-6 text-center th-text-muted text-sm">
                    {t('archives.noArchives')}
                  </div>
                {:else}
                  <div class="table-container">
                    <table class="table">
                      <thead>
                        <tr>
                          <th>{t('archives.camera')}</th>
                          <th>{t('archives.date')}</th>
                          <th>{t('archives.duration')}</th>
                          <th>{t('archives.size')}</th>
                          <th class="text-right">{t('archives.actions')}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each recordings as rec (rec.id)}
                          <tr class="transition-all duration-200 hover:th-bg-hover">
                            <td>
                              <span class="font-mono text-xs th-text-tertiary">{rec.camera_id}</span>
                            </td>
                            <td class="whitespace-nowrap">{formatDate(rec.started_at)}</td>
                            <td class="font-mono text-sm">{formatDuration(rec.duration)}</td>
                            <td>{formatFileSize(rec.file_size)}</td>
                            <td class="text-right">
                              <div class="flex justify-end gap-1">
                                <button
                                  class="btn btn-ghost px-2 py-1.5 text-sm"
                                  onclick={() => playRecording(rec)}
                                  title={t('archives.play')}
                                >
                                  <Play size={16} />
                                </button>
                                <button
                                  class="btn btn-ghost px-2 py-1.5 text-sm"
                                  onclick={() => downloadRecording(rec)}
                                  title={t('archives.download')}
                                >
                                  <Download size={16} />
                                </button>
                                <button
                                  class="btn btn-ghost px-2 py-1.5 text-sm th-color-danger"
                                  onclick={() => openDeleteRecording(rec)}
                                  title={t('archives.delete')}
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

                  {#if totalRecordingPages > 1}
                    <div class="px-4 py-2 border-t th-border">
                      <span class="text-sm th-text-muted">
                        {t('recordings.showing', {
                          start: String(recordingsOffset + 1),
                          end: String(Math.min(recordingsOffset + recordings.length, recordingsTotal)),
                          total: String(recordingsTotal)
                        })}
                      </span>
                    </div>
                    <Pagination
                      currentPage={currentRecordingPage}
                      totalPages={totalRecordingPages}
                      onPageChange={handleRecordingPageChange}
                    />
                  {/if}
                {/if}
              </div>
            {/if}
          </div>
        {/each}
      </div>

      <!-- Refresh indicator -->
      {#if loading && archiveGroups.length > 0}
        <div class="mt-4 text-center">
          <span class="text-sm th-text-muted">{t('recordings.refreshing')}</span>
        </div>
      {/if}
    {/if}
  </main>
</div>

<!-- Retention dialog -->
{#if showRetDialog && selectedGroup}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog" aria-modal="true">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('archives.setRetention')}</h3>
      <p class="th-text-secondary mb-4">{selectedGroup.name}</p>
      <div class="mb-6">
        <label for="retention-select" class="input-label">{t('archives.retention')}</label>
        <select id="retention-select" class="input mt-1" bind:value={retentionDays}>
          <option value={0}>{t('archives.keepForever')}</option>
          <option value={7}>7 {t('archives.retentionDays')}</option>
          <option value={14}>14 {t('archives.retentionDays')}</option>
          <option value={30}>30 {t('archives.retentionDays')}</option>
          <option value={60}>60 {t('archives.retentionDays')}</option>
          <option value={90}>90 {t('archives.retentionDays')}</option>
          <option value={180}>180 {t('archives.retentionDays')}</option>
          <option value={365}>365 {t('archives.retentionDays')}</option>
        </select>
      </div>
      <div class="flex gap-3 justify-end">
        <button onclick={() => { showRetDialog = false; selectedGroup = null; }} class="btn btn-secondary">
          {t('recordings.cancel')}
        </button>
        <button onclick={confirmSetRetention} class="btn btn-primary">
          {t('archives.setRetention')}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Delete group dialog -->
{#if showDeleteDialog && selectedGroup}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog" aria-modal="true">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('archives.deleteGroup')}</h3>
      <p class="th-text-secondary mb-6">
        {t('archives.confirmDeleteGroup', { name: selectedGroup.name })}
      </p>
      <div class="flex gap-3 justify-end">
        <button onclick={() => { showDeleteDialog = false; selectedGroup = null; }} class="btn btn-secondary">
          {t('recordings.cancel')}
        </button>
        <button onclick={confirmDeleteGroup} class="btn btn-danger">
          {t('recordings.deleteConfirm')}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Delete recording dialog -->
{#if showDeleteRecordingDialog && selectedRecording}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog" aria-modal="true">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('archives.delete')}</h3>
      <p class="th-text-secondary mb-6">
        {t('archives.confirmDeleteRecording')}
      </p>
      <div class="flex gap-3 justify-end">
        <button onclick={() => { showDeleteRecordingDialog = false; selectedRecording = null; }} class="btn btn-secondary">
          {t('recordings.cancel')}
        </button>
        <button onclick={confirmDeleteRecording} class="btn btn-danger">
          {t('recordings.deleteConfirm')}
        </button>
      </div>
    </div>
  </div>
{/if}
