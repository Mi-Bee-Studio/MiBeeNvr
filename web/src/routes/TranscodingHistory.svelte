<script lang="ts">
  import { onMount } from 'svelte';
  import { getTranscodingTasks } from '$lib/api/transcoding';
  import type { TranscodeTask } from '$lib/api/transcoding';
  import { listCameras } from '$lib/api';
  import type { Camera } from '$lib/api';
  import Pagination from '../components/Pagination.svelte';
  import { t } from '$lib/i18n';
  import { formatDate, formatDuration } from '$lib/format';
  import { AlertCircle, Video, RefreshCw, ChevronDown } from 'lucide-svelte';

  // Filter state
  let statusFilter = $state('');
  let cameraFilter = $state('');
  let cameras = $state<Camera[]>([]);
  let limit = $state(25);
  let page = $state(1);

  // Data state
  let tasks = $state<TranscodeTask[]>([]);
  let totalTasks = $state(0);
  let loading = $state(false);
  let error = $state('');
  let expandedTaskId = $state<number | null>(null);

  // Pagination calculations
  let totalPages = $derived(Math.ceil(totalTasks / limit));
  let startItem = $derived((page - 1) * limit + 1);
  let endItem = $derived(Math.min(page * limit, totalTasks));

  // Helper: camera name lookup
  function getCameraName(cameraId: string): string {
    const camera = cameras.find(c => c.id === cameraId);
    return camera ? camera.name : cameraId;
  }

  // Helper: status badge class
  function statusBadgeClass(status: string): string {
    switch (status) {
      case 'completed': return 'badge badge-success';
      case 'failed': return 'badge bg-red-100 text-red-800 dark:bg-red-900/50 dark:text-red-300';
      case 'running': return 'badge bg-blue-100 text-blue-800 dark:bg-blue-900/50 dark:text-blue-300';
      case 'pending': return 'badge bg-yellow-100 text-yellow-800 dark:bg-yellow-900/50 dark:text-yellow-300';
      case 'cancelled': return 'badge badge-neutral';
      default: return 'badge badge-neutral';
    }
  }

  // Helper: status label
  function statusLabel(status: string): string {
    switch (status) {
      case 'completed': return t('transcoding.completed');
      case 'failed': return t('transcoding.failed');
      case 'running': return t('transcoding.running');
      case 'pending': return t('transcoding.pending');
      case 'cancelled': return t('transcoding.cancelled');
      default: return status;
    }
  }

  // Helper: format task duration
  function formatTaskDuration(task: TranscodeTask): string {
    const start = task.started_at;
    const end = task.completed_at;
    if (!start) return '—';
    const endTime = end ? new Date(end) : new Date();
    const startTime = new Date(start);
    const diffSec = Math.max(0, (endTime.getTime() - startTime.getTime()) / 1000);
    if (diffSec < 1) return '—';
    return formatDuration(diffSec);
  }

  // Load tasks
  async function loadTasks() {
    loading = true;
    error = '';
    try {
      const response = await getTranscodingTasks({
        status: statusFilter || undefined,
        camera_id: cameraFilter || undefined,
        page,
        limit,
      });
      tasks = response.tasks || [];
      totalTasks = response.total || 0;
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.error');
    } finally {
      loading = false;
    }
  }

  // Load cameras for filter dropdown
  async function loadCameras() {
    try {
      cameras = await listCameras();
    } catch {
      // Silently fail — camera filter is optional
    }
  }

  // Handle page change
  function handlePageChange(newPage: number) {
    page = newPage;
    window.scrollTo(0, 0);
  }

  // Toggle expanded task row
  function toggleExpand(taskId: number) {
    expandedTaskId = expandedTaskId === taskId ? null : taskId;
  }

  // Lifecycle
  onMount(() => {
    loadCameras();
    loadTasks();
  });

  // Reset page when filters change
  let prevFilters = "";
  $effect(() => {
    const current = `${statusFilter}|${cameraFilter}|${limit}`;
    if (current !== prevFilters) {
      prevFilters = current;
      page = 1;
    }
  });

  // Reload when filters/pagination change
  let loadTimeout: ReturnType<typeof setTimeout>;
  $effect(() => {
    const _ = [statusFilter, cameraFilter, page, limit];
    clearTimeout(loadTimeout);
    loadTimeout = setTimeout(() => loadTasks(), 100);
    return () => clearTimeout(loadTimeout);
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-2xl font-bold th-text-primary">{t('transcoding.history.title')}</h2>
        <button
          onclick={loadTasks}
          class="btn btn-ghost btn-sm flex items-center gap-1.5"
          disabled={loading}
        >
          <RefreshCw size={16} class={loading ? 'animate-spin' : ''} />
          <span class="hidden sm:inline">{t('common.retry')}</span>
        </button>
      </div>

      <!-- Filter bar -->
      <div class="card p-5 mb-6 border th-border">
        <div class="flex flex-wrap gap-3 items-end">
          <div class="flex-1 min-w-[160px]">
            <label for="status-filter" class="input-label">{t('transcoding.history.filter_status')}</label>
            <select id="status-filter" class="input" bind:value={statusFilter}>
              <option value="">{t('transcoding.history.all_statuses')}</option>
              <option value="pending">{t('transcoding.pending')}</option>
              <option value="running">{t('transcoding.running')}</option>
              <option value="completed">{t('transcoding.completed')}</option>
              <option value="failed">{t('transcoding.failed')}</option>
              <option value="cancelled">{t('transcoding.cancelled')}</option>
            </select>
          </div>
          <div class="flex-1 min-w-[160px]">
            <label for="camera-filter" class="input-label">{t('transcoding.history.filter_camera')}</label>
            <select id="camera-filter" class="input" bind:value={cameraFilter}>
              <option value="">{t('transcoding.history.all_cameras')}</option>
              {#each cameras as camera}
                <option value={camera.id}>{camera.name}</option>
              {/each}
            </select>
          </div>
          <div class="min-w-[100px]">
            <label for="page-size" class="input-label">{t('transcoding.history.page_size')}</label>
            <select id="page-size" class="input" bind:value={limit}>
              <option value={25}>25</option>
              <option value={50}>50</option>
              <option value={100}>100</option>
            </select>
          </div>
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
        <button onclick={loadTasks} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    <!-- Tasks table -->
    <div class="card border th-border">
      {#if loading && tasks.length === 0}
        <div class="p-6 space-y-4">
          <div class="flex justify-between items-center">
            <div class="h-8 w-48 th-bg-tertiary rounded animate-pulse"></div>
            <div class="h-8 w-24 th-bg-tertiary rounded animate-pulse"></div>
          </div>
          <div class="space-y-3">
            {#each Array(5) as _}
              <div class="flex gap-4 items-center">
                <div class="h-4 w-8 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse"></div>
                <div class="h-4 w-28 th-bg-tertiary rounded animate-pulse ml-auto"></div>
              </div>
            {/each}
          </div>
        </div>
      {:else if tasks.length === 0}
        <div class="p-12 text-center">
          <div class="flex justify-center mb-4 th-text-muted">
            <Video size={48} />
          </div>
          <h3 class="text-lg font-medium th-text-primary mb-2">{t('transcoding.history.no_tasks')}</h3>
        </div>
      {:else}
        <div class="table-container th-border">
          <table class="table">
            <thead>
              <tr>
                <th class="min-w-[50px]">{t('transcoding.history.id')}</th>
                <th class="min-w-[100px]">{t('transcoding.history.camera')}</th>
                <th class="min-w-[120px]">{t('transcoding.history.conversion')}</th>
                <th class="min-w-[90px]">{t('transcoding.history.status')}</th>
                <th class="min-w-[80px]">{t('transcoding.history.progress')}</th>
                <th class="min-w-[120px]">{t('transcoding.history.created')}</th>
                <th class="min-w-[80px]">{t('transcoding.history.duration')}</th>
              </tr>
            </thead>
            <tbody>
              {#each tasks as task (task.id)}
                <tr
                  class="transition-all duration-200 hover:th-bg-hover cursor-pointer"
                  onclick={() => task.status === 'failed' && task.error && toggleExpand(task.id)}
                  role={task.status === 'failed' && task.error ? 'button' : undefined}
                >
                  <td class="font-mono text-sm th-text-secondary">{task.id}</td>
                  <td>
                    <div class="flex flex-col">
                      <span class="font-medium th-text-primary">{getCameraName(task.camera_id)}</span>
                      <span class="text-xs th-text-tertiary">{task.camera_id}</span>
                    </div>
                  </td>
                  <td>
                    <span class="text-sm">
                      <span class="badge badge-neutral text-xs">{task.input_format}</span>
                      <span class="mx-1 th-text-tertiary">→</span>
                      <span class="badge badge-info text-xs">{task.output_format}</span>
                    </span>
                  </td>
                  <td>
                    <span class="{statusBadgeClass(task.status)} text-xs">
                      {#if task.status === 'running'}
                        <span class="inline-block animate-pulse mr-1">●</span>
                      {/if}
                      {statusLabel(task.status)}
                    </span>
                  </td>
                  <td>
                    {#if task.status === 'running'}
                      <div class="flex items-center gap-2">
                        <div class="flex-1 h-1.5 rounded-full th-bg-tertiary overflow-hidden min-w-[60px]">
                          <div
                            class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500"
                            style="width: {Math.max(task.progress, 2)}%"
                          ></div>
                        </div>
                        <span class="text-xs th-text-secondary font-mono">{task.progress}%</span>
                      </div>
                    {:else if task.status === 'completed'}
                      <span class="text-xs th-text-secondary font-mono">100%</span>
                    {:else}
                      <span class="text-xs th-text-tertiary">{task.progress}%</span>
                    {/if}
                  </td>
                  <td class="whitespace-nowrap text-sm th-text-secondary">{formatDate(task.created_at)}</td>
                  <td class="whitespace-nowrap text-sm th-text-secondary">{formatTaskDuration(task)}</td>
                </tr>
                {#if task.status === 'failed' && task.error && expandedTaskId === task.id}
                  <tr class="th-bg-hover/50">
                    <td colspan="7" class="py-1 px-4">
                      <div class="flex items-start gap-2">
                        <AlertCircle size={14} class="th-color-danger mt-0.5 shrink-0" />
                        <pre class="text-[11px] th-text-secondary whitespace-pre-wrap break-all max-h-32 overflow-y-auto flex-1">{task.error}</pre>
                      </div>
                    </td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>

        {#if totalPages > 1}
          <!-- Page info -->
          <div class="px-4 py-2 border-t th-border">
            <span class="text-sm th-text-muted">
              {t('recordings.showing', { start: String(startItem), end: String(endItem), total: String(totalTasks) })}
            </span>
          </div>

          <!-- Pagination -->
          <Pagination
            {page}
            {totalPages}
            onPageChange={handlePageChange}
          />
        {/if}

        <!-- Loading indicator for refresh -->
        {#if loading && tasks.length > 0}
          <div class="px-4 py-2 th-bg-secondary border-t th-border text-center">
            <span class="text-sm th-text-muted">{t('recordings.refreshing')}</span>
          </div>
        {/if}
      {/if}
    </div>
  </main>
</div>
