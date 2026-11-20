<script lang="ts">
  // Archived-cameras tab: archive groups with expandable recording lists,
  // retention editing, archive deletion (with cleanup-progress polling), and
  // per-recording play/download/delete. Extracted from Cameras.svelte.
  //
  // The component mounts once with the page and gates its content on `active`
  // — the cleanup-progress poll must keep running while the user is on the
  // active tab, so the tab visibility must not unmount the polling.
  import { onMount, onDestroy } from 'svelte';
  import {
    listArchives,
    setArchiveRetention,
    deleteArchiveGroup,
    listArchiveRecordings,
    deleteArchiveRecording,
    getArchiveCleanupStatus,
    apiRequestBlob,
  } from '$lib/api';
  import type { ArchiveGroup, Recording, ArchiveCleanupTask } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast.svelte';
  import { formatFileSize, formatDate, formatDuration } from '$lib/format';
  import {
    Archive as ArchiveIcon, Trash2, Clock, Play, Download,
    ChevronDown, ChevronRight, Video, Settings, Loader2,
  } from 'lucide-svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Pagination from '$lib/components/Pagination.svelte';

  interface Props {
    /** Whether this tab is visible (polling runs regardless). */
    active: boolean;
  }

  let { active }: Props = $props();

  let archives = $state<ArchiveGroup[]>([]);
  let confirmDeleteArchive = $state<string | null>(null);
  let deleteArchiveLoading = $state(false);
  // Archive expansion state
  let expandedArchiveId = $state<string | null>(null);
  let archiveRecordings = $state<Recording[]>([]);
  let archiveRecordingsTotal = $state(0);
  let archiveRecordingsOffset = $state(0);
  let archiveRecordingsLimit = $state(20);
  let archiveRecordingsLoading = $state(false);
  let deleteRecordingConfirm = $state<Recording | null>(null);
  let showRetDialog = $state(false);
  let selectedArchiveGroup = $state<ArchiveGroup | null>(null);
  let retentionDays = $state(30);
  let cleanupTasks = $state<ArchiveCleanupTask[]>([]);
  let cleanupPolling: number | null = null;

  let currentArchivePage = $derived(Math.floor(archiveRecordingsOffset / archiveRecordingsLimit) + 1);
  let totalArchivePages = $derived(Math.ceil(archiveRecordingsTotal / archiveRecordingsLimit));

  /** Reload the archive list (host calls after archiving a camera). */
  export async function refresh(): Promise<void> {
    await loadArchives();
  }

  async function loadArchives() {
    try {
      const res = await listArchives();
      archives = res.archives || [];
    } catch (e) {
      console.warn('Failed to load archives:', e);
    }
  }

  async function loadCleanupStatus() {
    try {
      const res = await getArchiveCleanupStatus();
      cleanupTasks = [...res.active, ...res.recent.filter(t => t.status === 'failed')];
      // Stop polling when no active tasks remain
      if (res.active.length === 0 && cleanupPolling) {
        clearInterval(cleanupPolling);
        cleanupPolling = null;
        if (res.recent.some(t => t.status === 'done')) {
          showToast(t('cameras.archive.cleanup.allDone'), 'success');
        }
      }
    } catch (e) { /* silent fail — polling is best-effort */ }
  }

  async function handleRetentionChange(archiveId: string, days: number) {
    try {
      await setArchiveRetention(archiveId, days);
      showToast(t('cameras.archive.retentionUpdateSuccess'), 'success');
      await loadArchives();
    } catch (e) {
      showToast(t('cameras.failedArchive'), 'error');
    }
  }

  async function handleDeleteArchive(archiveId: string) {
    deleteArchiveLoading = true;
    try {
      await deleteArchiveGroup(archiveId);
      showToast(t('cameras.archive.deleteAllSuccess'), 'success');
      confirmDeleteArchive = null;
      await loadArchives();
      // Start polling for cleanup status
      if (!cleanupPolling) {
        await loadCleanupStatus();
        cleanupPolling = window.setInterval(loadCleanupStatus, 3000);
      }
    } catch (e) {
      showToast(t('cameras.archive.cleanup.deleteFailed'), 'error');
    } finally {
      deleteArchiveLoading = false;
    }
  }

  // Archive recording functions
  async function loadArchiveRecordings(cameraId: string) {
    archiveRecordingsLoading = true;
    try {
      const response = await listArchiveRecordings(cameraId, {
        offset: archiveRecordingsOffset,
        limit: archiveRecordingsLimit
      });
      archiveRecordings = response.recordings || [];
      archiveRecordingsTotal = response.total || 0;
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    } finally {
      archiveRecordingsLoading = false;
    }
  }

  function toggleArchive(group: ArchiveGroup) {
    if (expandedArchiveId === group.id) {
      expandedArchiveId = null;
      archiveRecordings = [];
      archiveRecordingsTotal = 0;
      archiveRecordingsOffset = 0;
    } else {
      expandedArchiveId = group.id;
      archiveRecordingsOffset = 0;
      loadArchiveRecordings(group.id);
    }
  }

  function playRecording(rec: Recording) {
    window.location.hash = `#/recordings/${rec.id}`;
  }

  async function downloadRecording(rec: Recording) {
    // apiRequestBlob carries the session auth (and its renewal/gateway
    // handling) instead of a hand-rolled fetch that lost both.
    try {
      const blob = await apiRequestBlob(
        `/archives/${expandedArchiveId}/recordings/${rec.id}/download`
      );
      const objectUrl = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = objectUrl;
      link.download = `archive_${rec.camera_id}_${rec.id}.mp4`;
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
      URL.revokeObjectURL(objectUrl);
    } catch {
      showToast(t('common.error'), 'error');
    }
  }

  async function confirmDeleteRecordingFn() {
    if (!deleteRecordingConfirm || !expandedArchiveId) return;
    try {
      await deleteArchiveRecording(expandedArchiveId, deleteRecordingConfirm.id);
      archiveRecordings = archiveRecordings.filter(r => r.id !== deleteRecordingConfirm!.id);
      archiveRecordingsTotal--;
      showToast(t('archives.deleteRecordingSuccess'), 'success');
      deleteRecordingConfirm = null;
      loadArchives();
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    }
  }

  function openRetDialog(group: ArchiveGroup) {
    selectedArchiveGroup = group;
    retentionDays = group.archive_retention_days;
    showRetDialog = true;
  }

  async function confirmSetRetention() {
    if (!selectedArchiveGroup) return;
    try {
      await setArchiveRetention(selectedArchiveGroup.id, retentionDays);
      archives = archives.map(g =>
        g.id === selectedArchiveGroup!.id ? { ...g, archive_retention_days: retentionDays } : g
      );
      showToast(t('archives.retentionUpdated'), 'success');
      showRetDialog = false;
      selectedArchiveGroup = null;
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    }
  }

  function formatRetention(days: number): string {
    if (days === 0) return t('archives.keepForever');
    return `${days} ${t('archives.retentionDays')}`;
  }

  function handleArchivePageChange(newPage: number) {
    archiveRecordingsOffset = (newPage - 1) * archiveRecordingsLimit;
    if (expandedArchiveId) {
      loadArchiveRecordings(expandedArchiveId);
    }
  }

  onMount(() => {
    loadArchives().then(() => loadCleanupStatus().then(() => {
      if (cleanupTasks.some(t => t.status === 'pending' || t.status === 'running')) {
        cleanupPolling = window.setInterval(loadCleanupStatus, 3000);
      }
    }));
  });

  onDestroy(() => { if (cleanupPolling) clearInterval(cleanupPolling); });
</script>

{#if active}
  {#if cleanupTasks.length > 0}
    <div class="card border th-border p-4 mb-4 flex items-center gap-3">
      <Loader2 size={20} class="animate-spin th-text-secondary shrink-0" />
      <div class="flex-1 min-w-0">
        <p class="text-sm th-text-primary font-medium">
          {t('cameras.archive.cleanup.inProgress', { count: String(cleanupTasks.filter(t => t.status === 'pending' || t.status === 'running').length) })}
        </p>
        <div class="mt-1 space-y-0.5">
          {#each cleanupTasks as task (task.camera_id)}
            <p class="text-xs th-text-muted">
              {t('cameras.archive.cleanup.taskItem', {
                name: task.camera_name,
                count: String(task.recording_count),
                size: formatFileSize(task.total_size),
                status: t('cameras.archive.cleanup.status' + task.status.charAt(0).toUpperCase() + task.status.slice(1))
              })}
            </p>
          {/each}
        </div>
      </div>
    </div>
  {/if}
  {#if archives.length === 0}
    <div class="card border th-border p-12 text-center mt-6">
      <div class="flex justify-center mb-4 th-text-muted">
        <ArchiveIcon size={48} />
      </div>
      <h3 class="text-lg font-medium th-text-primary mb-2">{t('cameras.archive.noArchives')}</h3>
      <p class="text-sm th-text-muted mb-4">{t('cameras.archive.noArchivesHint')}</p>
    </div>
  {:else}
    <div class="space-y-3 mt-6">
      {#each archives as group (group.id)}
        <div class="card border th-border overflow-hidden">
          <!-- Group header -->
          <div
            class="w-full p-5 text-left hover:th-bg-hover transition-colors duration-200 cursor-pointer"
            onclick={() => toggleArchive(group)}
            role="button"
            tabindex="0"
            onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleArchive(group); } }}
          >
            <div class="flex items-center justify-between gap-4">
              <div class="flex items-center gap-3 min-w-0">
                {#if expandedArchiveId === group.id}
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
                      <ArchiveIcon size={14} />
                      {formatFileSize(group.total_size)}
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
                  onclick={(e) => { e.stopPropagation(); confirmDeleteArchive = group.id; }}
                  title={t('archives.deleteGroup')}
                >
                  <Trash2 size={16} />
                </button>
              </div>
            </div>
          </div>

          <!-- Expanded recordings -->
          {#if expandedArchiveId === group.id}
            <div class="border-t th-border">
              {#if archiveRecordingsLoading}
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
              {:else if archiveRecordings.length === 0}
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
                      {#each archiveRecordings as rec (rec.id)}
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
                                <span class="hidden sm:inline-flex ml-1 text-xs">{t('cameras.action.viewLabel')}</span>
                              </button>
                              <button
                                class="btn btn-ghost px-2 py-1.5 text-sm"
                                onclick={() => downloadRecording(rec)}
                                title={t('archives.download')}
                              >
                                <Download size={16} />
                                <span class="hidden sm:inline-flex ml-1 text-xs">{t('cameras.action.downloadLabel')}</span>
                              </button>
                              <button
                                class="btn btn-ghost px-2 py-1.5 text-sm th-color-danger"
                                onclick={() => deleteRecordingConfirm = rec}
                                title={t('archives.delete')}
                              >
                                <Trash2 size={16} />
                                <span class="hidden sm:inline-flex ml-1 text-xs">{t('cameras.action.deleteLabel')}</span>
                              </button>
                            </div>
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>

                {#if totalArchivePages > 1}
                  <div class="px-4 py-2 border-t th-border">
                    <span class="text-sm th-text-muted">
                      {t('recordings.showing', {
                        start: String(archiveRecordingsOffset + 1),
                        end: String(Math.min(archiveRecordingsOffset + archiveRecordings.length, archiveRecordingsTotal)),
                        total: String(archiveRecordingsTotal)
                      })}
                    </span>
                  </div>
                  <Pagination
                    currentPage={currentArchivePage}
                    totalPages={totalArchivePages}
                    onPageChange={handleArchivePageChange}
                  />
                {/if}
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}

  <!-- Archive Delete Confirm Dialog -->
  {#if confirmDeleteArchive}
    <ConfirmDialog
      title={t('cameras.action.deleteAll')}
      message={t('cameras.archive.deleteAllConfirm')}
      onconfirm={() => handleDeleteArchive(confirmDeleteArchive!)}
      oncancel={() => { if (!deleteArchiveLoading) confirmDeleteArchive = null; }}
      variant="danger"
      loading={deleteArchiveLoading}
    />
  {/if}
  <!-- Retention dialog -->
  {#if showRetDialog && selectedArchiveGroup}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog" aria-modal="true">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('archives.setRetention')}</h3>
        <p class="th-text-secondary mb-4">{selectedArchiveGroup.name}</p>
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
          <button onclick={() => { showRetDialog = false; selectedArchiveGroup = null; }} class="btn btn-secondary">
            {t('recordings.cancel')}
          </button>
          <button onclick={confirmSetRetention} class="btn btn-primary">
            {t('archives.setRetention')}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Delete recording dialog -->
  {#if deleteRecordingConfirm}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog" aria-modal="true">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('archives.delete')}</h3>
        <p class="th-text-secondary mb-6">
          {t('archives.confirmDeleteRecording')}
        </p>
        <div class="flex gap-3 justify-end">
          <button onclick={() => { deleteRecordingConfirm = null; }} class="btn btn-secondary">
            {t('recordings.cancel')}
          </button>
          <button onclick={confirmDeleteRecordingFn} class="btn btn-danger">
            {t('recordings.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}
{/if}
