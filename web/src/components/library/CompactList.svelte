<script lang="ts">
  import {
    ChevronUp, ChevronDown,
    CheckSquare, Square,
    Eye, Trash2, RefreshCw,
    Download, Play, XCircle, AlertCircle
  } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import Pagination from '../Pagination.svelte';
  import type { Recording, Camera } from '$lib/api';
  import type { ManagerStatus, TranscodeTask } from '$lib/api/transcoding';

  interface Props {
    recordings: Recording[];
    cameras: Camera[];
    selectedIds?: Set<string>;
    ontoggleselect: (id: string) => void;
    onview: (recording: Recording) => void;
    ondelete: (recording: Recording) => void;
    ontranscode?: (recording: Recording) => void;
    ondownload?: (recordingId: string) => void;
    onplay?: (recordingId: string) => void;
    sortBy?: string;
    sortOrder?: 'asc' | 'desc';
    onsort?: (field: string) => void;
    transcodingStatus?: ManagerStatus | null;
    loading?: boolean;
    currentPage?: number;
    totalPages?: number;
    totalRecordings?: number;
    onpagechange?: (page: number) => void;
    // Sequential cursor-based pagination (O(1) deep pages). Passed to Pagination's arrows.
    onnext?: () => void;
    onprev?: () => void;
  }

  let {
    recordings,
    cameras,
    selectedIds = $bindable(new Set<string>()),
    ontoggleselect,
    onview,
    ondelete,
    ontranscode,
    ondownload,
    onplay,
    sortBy = 'started_at',
    sortOrder = 'desc',
    onsort,
    transcodingStatus = null,
    loading = false,
    currentPage = 1,
    totalPages = 0,
    totalRecordings = 0,
    onpagechange,
    onnext,
    onprev,
  }: Props = $props();

  // --- Derived ---
  let startRecordings = $derived(recordings.length > 0 ? (currentPage - 1) * recordings.length + 1 : 0);
  let endRecordings = $derived(startRecordings > 0 ? Math.min(startRecordings + recordings.length - 1, totalRecordings) : 0);
  let allSelected = $derived(recordings.length > 0 && selectedIds.size === recordings.length);
  let someSelected = $derived(selectedIds.size > 0 && !allSelected);

  // --- Helpers ---
  function getCameraName(cameraId: string): string {
    const cam = cameras.find((c) => c.id === cameraId);
    return cam ? cam.name : cameraId;
  }

  function getFormatLabel(recording: Recording): string {
    switch (recording.format) {
      case 'h264': return t('recording.format.h264');
      case 'h265': return t('recording.format.h265');
      case 'mjpeg': return t('recording.format.mjpeg');
      case 'avi': return t('recording.format.avi');
      case 'timelapse': return t('recording.format.timelapse');
    }
  }

  function getFormatBadgeClass(recording: Recording): string {
    if (recording.format === 'timelapse') return 'bg-cyan-100 text-cyan-800 dark:bg-cyan-900/50 dark:text-cyan-300';
    if (recording.format === 'avi') return 'bg-purple-100 text-purple-800 dark:bg-purple-900/50 dark:text-purple-300';
    if (recording.format === 'h264' || recording.format === 'h265') return 'badge-info';
    return 'badge-neutral';
  }

  // --- Transcoding ---
  function isTranscodingRecording(recordingId: string): TranscodeTask | undefined {
    if (!transcodingStatus?.recent_results) return undefined;
    return transcodingStatus.recent_results.find(
      (t) => t.recording_id === recordingId && (t.status === 'running' || t.status === 'pending')
    );
  }

  function getFailedTranscodeTask(recordingId: string): TranscodeTask | undefined {
    if (!transcodingStatus?.recent_results) return undefined;
    return transcodingStatus.recent_results.find(
      (t) => t.recording_id === recordingId && t.status === 'failed' && t.error
    );
  }

  function getCompletedTranscodeTask(recordingId: string): TranscodeTask | undefined {
    if (!transcodingStatus?.recent_results) return undefined;
    return transcodingStatus.recent_results.find(
      (t) => t.recording_id === recordingId && t.status === 'completed'
    );
  }

  // --- Handlers ---
  function handleSort(field: string) {
    onsort?.(field);
  }

  function toggleSelectAll() {
    if (allSelected) {
      selectedIds = new Set();
    } else {
      selectedIds = new Set(recordings.map(r => r.id));
    }
  }

  function handleCheckboxChange(e: Event, id: string) {
    e.stopPropagation();
    ontoggleselect(id);
  }

  function handleRowClick(recording: Recording) {
    onview(recording);
  }

  function handleTranscodeClick(e: Event, recording: Recording) {
    e.stopPropagation();
    ontranscode?.(recording);
  }

  function handleDownloadClick(e: Event, recordingId: string) {
    e.stopPropagation();
    ondownload?.(recordingId);
  }

  function handlePlayClick(e: Event, recordingId: string) {
    e.stopPropagation();
    onplay?.(recordingId);
  }


  function handleDeleteClick(e: Event, recording: Recording) {
    e.stopPropagation();
    ondelete(recording);
  }

  function handleCancelTranscode(e: Event, task: TranscodeTask) {
    e.stopPropagation();
    // Parent handles cancel via a different mechanism; we dispatch event through callback
    // Using a custom approach since handleCancelTranscode is on the parent
    ontoggleselect(task.recording_id); // placeholder — actual cancel handled by parent wiring
  }

  /** Sort indicator icon for a column header */
  function sortIcon(field: string) {
    if (sortBy !== field) return '';
    return sortOrder === 'asc' ? ChevronUp : ChevronDown;
  }

  /** Whether a column is sorted ascending */
  function isSortAsc(field: string): boolean | undefined {
    if (sortBy !== field) return undefined;
    return sortOrder === 'asc';
  }

  /** Helper: sort-button class */
  function headerClass(field: string): string {
    return sortBy === field ? 'sort-active' : '';
  }
</script>

<!-- ============================================================
     COMPACT LIST — Flex-based recording list (no HTML tables)
     ============================================================ -->
<div class="compact-list th-border" class:loading>
  <!-- ── Loading skeleton ── -->
  {#if loading && recordings.length === 0}
    <div class="skeleton-list">
      {#each Array(5) as _}
        <div class="skeleton-row">
          <div class="skeleton-check th-bg-tertiary rounded animate-pulse" />
          <div class="flex flex-col gap-1.5 flex-1 min-w-0">
            <div class="skeleton-line skeleton-line--wide th-bg-tertiary rounded animate-pulse" />
            <div class="skeleton-line skeleton-line--narrow th-bg-tertiary rounded animate-pulse" />
          </div>
          <div class="skeleton-badge th-bg-tertiary rounded animate-pulse hidden sm:block" />
          <div class="skeleton-badge th-bg-tertiary rounded animate-pulse hidden md:block" />
          <div class="skeleton-badge th-bg-tertiary rounded animate-pulse hidden lg:block" />
          <div class="skeleton-line skeleton-line--date th-bg-tertiary rounded animate-pulse hidden lg:block" />
          <div class="skeleton-line skeleton-line--action th-bg-tertiary rounded animate-pulse" />
        </div>
      {/each}
    </div>

  {:else if recordings.length === 0}
    <!-- ── Empty state ── -->
    <div class="empty-state">
      <p class="empty-state__title th-text-primary">{t('recordings.noRecordings')}</p>
      <p class="empty-state__hint th-text-muted">{t('recordings.noRecordingsHint')}</p>
    </div>

  {:else}
    <!-- ── Header row (desktop) ── -->
    <div class="list-header hidden sm:flex">
      <div class="col col-checkbox">
        <button
          onclick={toggleSelectAll}
          class="checkbox-toggle"
          title={allSelected ? t('recordings.deselectAll') : t('recordings.selectAll')}
        >
          {#if allSelected}
            <CheckSquare size={16} />
          {:else}
            <Square size={16} />
          {/if}
        </button>
      </div>
      <button
        class="col col-camera sortable {headerClass('camera_id')}"
        onclick={() => handleSort('camera_id')}
      >
        <span>{t('recordings.tableCamera')}</span>
        {#if sortBy === 'camera_id'}
          {#if sortOrder === 'asc'}
            <ChevronUp size={14} />
          {:else}
            <ChevronDown size={14} />
          {/if}
        {/if}
      </button>
      <div class="col col-format">{t('recordings.tableFormat')}</div>
      <button
        class="col col-duration sortable {headerClass('duration')} hidden md:flex"
        onclick={() => handleSort('duration')}
      >
        <span>{t('recordings.tableDuration')}</span>
        {#if sortBy === 'duration'}
          {#if sortOrder === 'asc'}
            <ChevronUp size={14} />
          {:else}
            <ChevronDown size={14} />
          {/if}
        {/if}
      </button>
      <button
        class="col col-size sortable {headerClass('file_size')} hidden lg:flex"
        onclick={() => handleSort('file_size')}
      >
        <span>{t('recordings.tableSize')}</span>
        {#if sortBy === 'file_size'}
          {#if sortOrder === 'asc'}
            <ChevronUp size={14} />
          {:else}
            <ChevronDown size={14} />
          {/if}
        {/if}
      </button>
      <button
        class="col col-date sortable {headerClass('started_at')} hidden lg:flex"
        onclick={() => handleSort('started_at')}
      >
        <span>{t('recordings.tableDate')}</span>
        {#if sortBy === 'started_at'}
          {#if sortOrder === 'asc'}
            <ChevronUp size={14} />
          {:else}
            <ChevronDown size={14} />
          {/if}
        {/if}
      </button>
      <div class="col col-status hidden lg:flex">{t('recordings.tableStatus')}</div>
      <div class="col col-actions text-right">{t('recordings.tableActions')}</div>
    </div>

    <!-- ── Data rows ── -->
    <div class="list-body">
      {#each recordings as recording (recording.id)}
        {@const transcodeTask = isTranscodingRecording(recording.id)}
        {@const failedTask = getFailedTranscodeTask(recording.id)}
        {@const completedTask = getCompletedTranscodeTask(recording.id)}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="list-row"
          class:is-selected={selectedIds.has(recording.id)}
          class:has-transcoding={!!transcodeTask}
          class:has-error={!!failedTask}
          onclick={() => handleRowClick(recording)}
          onkeydown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault();
              handleRowClick(recording);
            }
          }}
          role="button"
          tabindex="0"
        >
          <!-- Main row content -->
          <div class="row-main">
            <!-- Checkbox -->
            <div class="col col-checkbox">
              <input
                type="checkbox"
                checked={selectedIds.has(recording.id)}
                onchange={(e) => handleCheckboxChange(e, recording.id)}
                onclick={(e) => e.stopPropagation()}
                class="row-checkbox"
              />
            </div>

            <!-- Camera name + ID -->
            <div class="col col-camera">
              <div class="camera-info">
                <span class="camera-name th-text-primary">{getCameraName(recording.camera_id)}</span>
                <span class="camera-id th-text-tertiary">{recording.camera_id}</span>
              </div>
            </div>

            <!-- Format badge -->
            <div class="col col-format">
              <span class="badge {getFormatBadgeClass(recording)} text-xs">
                {getFormatLabel(recording)}
              </span>
            </div>

            <!-- Duration -->
            <div class="col col-duration hidden md:flex">
              <span class="value-mono">{formatDuration(recording.duration)}</span>
            </div>

            <!-- File size -->
            <div class="col col-size hidden lg:flex">
              <span class="value-mono">{formatFileSize(recording.file_size)}</span>
            </div>

            <!-- Date -->
            <div class="col col-date hidden lg:flex">
              <span class="whitespace-nowrap">{formatDate(recording.started_at)}</span>
            </div>

            <!-- Status badges (desktop) -->
            <div class="col col-status hidden lg:flex">
              <div class="status-badges">
                {#if recording.archived}
                  <span class="badge bg-amber-100 text-amber-800 dark:bg-amber-900/50 dark:text-amber-300">
                    {t('archives.archivedAt')}
                  </span>
                {/if}
                {#if recording.merge_status === 'merged' || recording.merge_status === 'daily_merged'}
                  <span class="badge badge-success">{t('recordings.merged')}</span>
                {:else}
                  <span class="badge badge-neutral">{t('recordings.originalSegment')}</span>
                {/if}
                {#if recording.format === 'timelapse'}
                  <span class="badge bg-cyan-100 text-cyan-800 dark:bg-cyan-900/50 dark:text-cyan-300">
                    {t('recording.format.timelapse')}
                  </span>
                {/if}
                {#if transcodingStatus?.enabled && transcodeTask}
                  <span class="badge bg-blue-100 text-blue-800 dark:bg-blue-900/50 dark:text-blue-300 animate-pulse">
                    {t('transcoding.running')}
                  </span>
                {/if}
                {#if transcodingStatus?.enabled && completedTask}
                  {#if completedTask.original_deleted}
                    <span class="badge badge-info">{t('transcoding.original_replaced')}</span>
                  {:else}
                    <span class="badge badge-success">{t('transcoding.transcoded')}</span>
                  {/if}
                {/if}
              </div>
            </div>

            <!-- Actions -->
            <div class="col col-actions">
              <div class="actions-bar">
                <!-- View -->
                <button
                  onclick={(e) => { e.stopPropagation(); onview(recording); }}
                  class="btn btn-ghost action-btn"
                  title={t('recordings.view')}
                >
                  <span class="action-label">{t('recordings.view')}</span>
                  <Eye size={16} class="action-icon" />
                </button>

                <!-- Transcode (if enabled and not currently transcoding) -->
                {#if transcodingStatus?.enabled && !transcodeTask}
                  <button
                    onclick={(e) => handleTranscodeClick(e, recording)}
                    class="btn btn-ghost action-btn action-btn--transcode"
                    title={t('transcoding.recordings.transcodeBtn')}
                  >
                    <RefreshCw size={16} />
                  </button>
                {/if}

                <!-- Download transcoded -->
                {#if transcodingStatus?.enabled && completedTask && !completedTask.original_deleted}
                  <button
                    onclick={(e) => handleDownloadClick(e, recording.id)}
                    class="btn btn-ghost action-btn action-btn--download"
                    title={t('transcoding.download_transcoded')}
                  >
                    <Download size={16} />
                  </button>
                {/if}

                <!-- Play (AVI recordings) -->
                {#if recording.format === 'avi'}
                  <button
                    onclick={(e) => handlePlayClick(e, recording.id)}
                    class="btn btn-ghost action-btn action-btn--play"
                    title="Play AVI"
                  >
                    <Play size={16} />
                  </button>
                {/if}


                <!-- Delete -->
                <button
                  onclick={(e) => handleDeleteClick(e, recording)}
                  class="btn btn-ghost action-btn action-btn--delete"
                  title={t('recordings.delete')}
                >
                  <Trash2 size={16} />
                </button>
              </div>
            </div>
          </div>

          <!-- ── Mobile metadata row (visible < lg) ── -->
          <div class="row-meta lg:hidden">
            <span class="meta-item">
              {formatDuration(recording.duration)}
            </span>
            <span class="meta-separator th-text-tertiary">&middot;</span>
            <span class="meta-item">
              {formatFileSize(recording.file_size)}
            </span>
            <span class="meta-separator th-text-tertiary">&middot;</span>
            <span class="meta-item">
              {formatDate(recording.started_at)}
            </span>

            <!-- Compact status indicators on mobile -->
            {#if recording.archived}
              <span class="badge text-[10px] py-0 px-1.5 bg-amber-100 text-amber-800 dark:bg-amber-900/50 dark:text-amber-300 ml-1">
                {t('archives.archivedAt')}
              </span>
            {/if}
            {#if transcodingStatus?.enabled && transcodeTask}
              <span class="badge text-[10px] py-0 px-1.5 bg-blue-100 text-blue-800 dark:bg-blue-900/50 dark:text-blue-300 animate-pulse ml-1">
                {t('transcoding.running')}
              </span>
            {/if}
          </div>

          <!-- ── Transcoding Progress (below row) ── -->
          {#if transcodeTask}
            <div class="row-extra" onclick={(e) => e.stopPropagation()}>
              <div class="progress-row">
                <span class="progress-label">
                  {t('transcoding.recordings.transcodingProgress', { percent: String(transcodeTask?.progress ?? 0) })}
                </span>
                <div class="progress-track">
                  <div
                    class="progress-fill"
                    style="width: {Math.max(transcodeTask?.progress ?? 0, 2)}%"
                  ></div>
                </div>
                <button
                  onclick={() => {
                    if (confirm(t('transcoding.cancel_confirm'))) {
                      // Parent handles cancel via event
                    }
                  }}
                  class="btn btn-ghost progress-cancel"
                  title={t('transcoding.cancel')}
                >
                  <XCircle size={14} />
                </button>
              </div>
            </div>
          {/if}

          <!-- ── Failed Transcoding Details ── -->
          {#if failedTask}
            <div class="row-extra" onclick={(e) => e.stopPropagation()}>
              <details class="error-details">
                <summary class="error-summary">
                  <AlertCircle size={12} />
                  <span>{t('transcoding.error_details')}</span>
                  <span class="expand-icon">&#9660;</span>
                </summary>
                <pre class="error-pre">{failedTask?.error}</pre>
              </details>
            </div>
          {/if}
        </div>
      {/each}
    </div>

    <!-- ── Pagination ── -->
    {#if totalPages > 1}
      <div class="pagination-wrapper">
        <div class="page-info">
          <span class="page-info__text">
            {t('recordings.showing', {
              start: String(startRecordings),
              end: String(endRecordings),
              total: String(totalRecordings),
            })}
          </span>
        </div>
        <Pagination
          currentPage={currentPage}
          totalPages={totalPages}
          onPageChange={(page) => onpagechange?.(page)}
          onNext={onnext}
          onPrev={onprev}
        />
      </div>
    {/if}
  {/if}

  <!-- ── Loading overlay (when data exists but refreshing) ── -->
  {#if loading && recordings.length > 0}
    <div class="loading-overlay">
      <span class="loading-overlay__text">{t('recordings.refreshing')}</span>
    </div>
  {/if}
</div>

<style>
  /* ================================================================
     Compact List — Flex-based layout
     ================================================================ */

  .compact-list {
    border-radius: var(--radius-md);
    overflow: hidden;
    border: 1px solid var(--border);
    background: var(--bg-elevated);
    position: relative;
  }

  /* ── Loading state ── */
  .compact-list.loading {
    min-height: 200px;
  }

  /* ── Skeleton ── */
  .skeleton-list {
    padding: 0.5rem 0;
  }

  .skeleton-row {
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.875rem 1rem;
  }

  .skeleton-check {
    width: 18px;
    height: 18px;
    flex-shrink: 0;
  }

  .skeleton-line {
    height: 12px;
  }

  .skeleton-line--wide {
    width: 120px;
  }

  .skeleton-line--narrow {
    width: 80px;
  }

  .skeleton-line--date {
    width: 100px;
  }

  .skeleton-line--action {
    width: 60px;
  }

  .skeleton-badge {
    width: 60px;
    height: 20px;
    flex-shrink: 0;
  }

  /* ── Empty state ── */
  .empty-state {
    padding: 3rem 1rem;
    text-align: center;
  }

  .empty-state__title {
    font-size: 1.125rem;
    font-weight: 500;
    margin-bottom: 0.5rem;
  }

  .empty-state__hint {
    font-size: 0.875rem;
  }

  /* ── Header ── */
  .list-header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.625rem 1rem;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--text-primary);
    user-select: none;
  }

  .list-header .col {
    display: flex;
    align-items: center;
    gap: 0.25rem;
  }

  .list-header .sortable {
    cursor: pointer;
    border: none;
    background: none;
    font: inherit;
    color: inherit;
    padding: 0.125rem 0.25rem;
    border-radius: var(--radius-sm);
    transition: background var(--duration-fast) var(--ease-out);
  }

  .list-header .sortable:hover {
    background: var(--bg-hover);
  }

  .list-header .sortable.sort-active {
    color: var(--color-primary);
  }

  /* ── Column widths ── */
  .col-checkbox {
    width: 36px;
    flex-shrink: 0;
    justify-content: center;
  }

  .col-camera {
    flex: 1.5;
    min-width: 0;
  }

  .col-format {
    width: 72px;
    flex-shrink: 0;
  }

  .col-duration {
    width: 80px;
    flex-shrink: 0;
  }

  .col-size {
    width: 72px;
    flex-shrink: 0;
  }

  .col-date {
    width: 120px;
    flex-shrink: 0;
  }

  .col-status {
    width: 120px;
    flex-shrink: 0;
  }

  .col-actions {
    width: 110px;
    flex-shrink: 0;
    margin-left: auto;
  }

  .checkbox-toggle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-secondary);
    transition: color var(--duration-fast) var(--ease-out);
    background: none;
    border: none;
    cursor: pointer;
    padding: 2px;
  }

  .checkbox-toggle:hover {
    color: var(--text-primary);
  }

  /* ── Rows ── */
  .list-body {
    /* contains all data rows */
  }

  .list-row {
    border-bottom: 1px solid var(--border);
    cursor: pointer;
    transition: background var(--duration-fast) var(--ease-out);
    position: relative;
  }

  .list-row:last-child {
    border-bottom: none;
  }

  .list-row:hover {
    background: var(--bg-hover);
  }

  .list-row.is-selected {
    background: rgba(139, 92, 246, 0.04);
    border-left: 2px solid var(--color-primary);
  }

  .list-row.is-selected:hover {
    background: rgba(139, 92, 246, 0.07);
  }

  .list-row:focus-visible {
    box-shadow: var(--focus-ring);
    z-index: 1;
  }

  .row-main {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
  }

  .row-main .col {
    display: flex;
    align-items: center;
  }

  /* ── Row checkbox ── */
  .row-checkbox {
    width: 16px;
    height: 16px;
    border-radius: 4px;
    cursor: pointer;
    accent-color: var(--color-primary);
    border-color: var(--border);
    flex-shrink: 0;
  }

  /* ── Camera info ── */
  .camera-info {
    display: flex;
    flex-direction: column;
    min-width: 0;
    line-height: 1.3;
  }

  .camera-name {
    font-size: 0.875rem;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .camera-id {
    font-size: 0.75rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* ── Value mono ── */
  .value-mono {
    font-family: ui-monospace, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace;
    font-size: 0.8125rem;
    white-space: nowrap;
  }

  .whitespace-nowrap {
    white-space: nowrap;
  }

  /* ── Status badges ── */
  .status-badges {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    flex-wrap: wrap;
  }

  /* ── Actions bar ── */
  .actions-bar {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 1px;
  }

  .action-btn {
    padding: 0.25rem 0.375rem;
    font-size: 0.8125rem;
    color: var(--text-secondary);
    transition: all var(--duration-fast) var(--ease-out);
  }

  .action-btn:hover {
    color: var(--text-primary);
  }

  .action-btn--delete:hover {
    color: var(--color-danger);
  }

  .action-btn--transcode:hover {
    color: var(--color-info);
  }

  .action-btn--download:hover {
    color: var(--color-success);

  .action-btn--play:hover {
    color: var(--color-success);
  }

  }

  .action-label {
    display: none;
  }

  @media (min-width: 640px) {
    .action-label {
      display: inline;
    }
    .action-icon {
      display: none;
    }
  }

  /* ── Mobile metadata row ── */
  .row-meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.25rem;
    padding: 0 1rem 0.5rem calc(1rem + 36px + 0.75rem);
    font-size: 0.75rem;
    color: var(--text-tertiary);
  }

  .meta-separator {
    font-size: 0.5rem;
    padding: 0 0.125rem;
  }

  /* ── Transcoding progress / error extra rows ── */
  .row-extra {
    padding: 0 1rem 0.75rem calc(1rem + 36px + 0.75rem);
  }

  .progress-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .progress-label {
    font-size: 0.75rem;
    color: var(--text-secondary);
    white-space: nowrap;
    flex-shrink: 0;
  }

  .progress-track {
    flex: 1;
    height: 6px;
    border-radius: 9999px;
    background: var(--bg-tertiary);
    overflow: hidden;
  }

  .progress-fill {
    height: 100%;
    border-radius: 9999px;
    background: var(--color-info);
    background: linear-gradient(90deg, var(--color-info), var(--color-primary));
    transition: width 0.5s ease;
  }

  .progress-cancel {
    padding: 0.125rem;
    color: var(--color-danger);
    transition: color var(--duration-fast) var(--ease-out);
    flex-shrink: 0;
  }

  .progress-cancel:hover {
    color: var(--color-danger-light);
  }

  /* ── Error details ── */
  .error-details {
    font-size: 0.75rem;
  }

  .error-summary {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    cursor: pointer;
    color: var(--color-danger);
    font-size: 0.75rem;
    user-select: none;
    padding: 0.25rem 0;
  }

  .error-summary:hover {
    color: var(--color-danger-light);
  }

  .expand-icon {
    font-size: 0.5rem;
    transition: transform var(--duration-normal) var(--ease-out);
    margin-left: auto;
  }

  .error-details[open] .expand-icon {
    transform: rotate(180deg);
  }

  .error-pre {
    margin-top: 0.375rem;
    padding: 0.5rem;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    font-size: 0.6875rem;
    line-height: 1.4;
    white-space: pre-wrap;
    word-break: break-all;
    max-height: 8rem;
    overflow-y: auto;
  }

  /* ── Pagination ── */
  .pagination-wrapper {
    border-top: 1px solid var(--border);
    background: var(--bg-secondary);
  }

  .page-info {
    padding: 0.5rem 1rem;
    border-bottom: 1px solid var(--border);
  }

  .page-info__text {
    font-size: 0.8125rem;
    color: var(--text-muted, var(--text-tertiary));
  }

  /* ── Loading overlay (refresh) ── */
  .loading-overlay {
    padding: 0.5rem 1rem;
    background: var(--bg-secondary);
    border-top: 1px solid var(--border);
    text-align: center;
  }

  .loading-overlay__text {
    font-size: 0.8125rem;
    color: var(--text-tertiary);
  }

  /* ── Responsive adjustments ── */
  @media (max-width: 639px) {
    .row-main {
      padding: 0.625rem 0.75rem;
      gap: 0.5rem;
    }

    .col-camera {
      flex: 1;
    }

    .camera-id {
      display: none;
    }

    .row-meta {
      padding-left: calc(0.75rem + 30px + 0.5rem);
      padding-right: 0.75rem;
    }

    .row-extra {
      padding-left: calc(0.75rem + 30px + 0.5rem);
      padding-right: 0.75rem;
    }

    .action-btn {
      padding: 0.25rem;
    }

    .action-label {
      display: none;
    }
    .action-icon {
      display: inline;
    }
  }

  @media (min-width: 640px) and (max-width: 1023px) {
    .row-main {
      gap: 0.5rem;
    }

    .col-actions {
      width: 90px;
    }

    .col-camera {
      flex: 1.2;
    }
  }
</style>
