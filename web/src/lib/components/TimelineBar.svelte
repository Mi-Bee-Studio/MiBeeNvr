<script lang="ts">
  /**
   * TimelineBar — DVR-style 24h timeline for browsing a camera's recordings.
   *
   * Renders a horizontal 24-hour timeline for a given camera + date. Each
   * recording segment is drawn as a colored block proportional to its
   * duration. Clicking/dragging seeks to that wall-clock moment:
   *   - Hit a segment → onSeek(recordingId, offsetSecondsWithinSegment)
   *   - Hit a gap (no recording) → snap to nearest segment edge + warn
   *
   * Granularity: per-segment (not per-keyframe). Intra-segment seek uses
   * the native <video> currentTime (handled by the parent).
   */
  import { listRecordings, type Recording } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatDate } from '$lib/format';
  import { findSegmentAt, parseDayStart as parseDayStartUtil, formatLength as formatLengthUtil, type TimelineSegment } from '$lib/timeline-utils';

  let {
    cameraId,
    date,
    currentRecording,
    currentVideoTime = 0,
    onseek,
  }: {
    cameraId: string;
    date: string; // YYYY-MM-DD
    currentRecording: Recording | null;
    currentVideoTime?: number;
    onseek: (recordingId: string, offsetSeconds: number) => void;
  } = $props();

  let segments = $state<Array<{ rec: Recording; startSec: number; endSec: number }>>([]);
  let loading = $state(false);
  let error = $state('');
  let hoverInfo = $state<{ x: number; label: string } | null>(null);
  let snapNotice = $state('');

  // Format → color class
  const formatColor: Record<string, string> = {
    h264: '#3b82f6', // blue
    h265: '#a855f7', // purple
    mjpeg: '#f97316', // orange
    timelapse: '#10b981', // emerald
  };

  // Day boundaries in seconds-from-midnight
  const dayStart = $derived(parseDayStart(date));

  function parseDayStart(dateStr: string): number {
    return parseDayStartUtil(dateStr);
  }

  // Current playback position in epoch-ms
  const currentEpochMs = $derived.by(() => {
    if (!currentRecording || !currentRecording.started_at) return null;
    const startedMs = Date.parse(currentRecording.started_at);
    if (isNaN(startedMs)) return null;
    return startedMs + currentVideoTime * 1000;
  });

  // Cursor position as percentage of 24h (0-100)
  const cursorPct = $derived.by(() => {
    if (currentEpochMs == null) return null;
    const elapsed = (currentEpochMs - dayStart) / 1000;
    if (elapsed < 0 || elapsed > 86400) return null;
    return (elapsed / 86400) * 100;
  });

  // Load segments for the camera + date
  $effect(() => {
    const cid = cameraId;
    const d = date;
    if (!cid || !d) return;
    void loadSegments(cid, d);
  });

  async function loadSegments(cid: string, d: string) {
    loading = true;
    error = '';
    snapNotice = '';
    try {
      const [y, m, dd] = d.split('-').map(Number);
      const startISO = new Date(Date.UTC(y, m - 1, dd, 0, 0, 0)).toISOString();
      const endISO = new Date(Date.UTC(y, m - 1, dd, 23, 59, 59)).toISOString();
      const resp = await listRecordings({
        camera_id: cid,
        start: startISO,
        end: endISO,
        sort_by: 'started_at',
        order: 'asc',
        limit: 500,
      });
      segments = resp.recordings
        .map((rec) => {
          const sMs = Date.parse(rec.started_at);
          const eMs = rec.ended_at ? Date.parse(rec.ended_at) : sMs + rec.duration * 1000;
          if (isNaN(sMs) || isNaN(eMs)) return null;
          return {
            rec,
            startSec: (sMs - dayStart) / 1000,
            endSec: (eMs - dayStart) / 1000,
          };
        })
        .filter((x): x is { rec: Recording; startSec: number; endSec: number } => x !== null)
        .filter((x) => x.endSec > 0 && x.startSec < 86400); // clamp to visible day
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to load timeline';
      segments = [];
    } finally {
      loading = false;
    }
  }

  // Total recorded duration today
  const totalRecordedSec = $derived(
    segments.reduce((sum, s) => sum + (s.endSec - s.startSec), 0),
  );

  // Convert loaded segments to TimelineSegment shape for findSegmentAt
  function findSegment(targetSec: number): { seg: TimelineSegment | null; offset: number; snapped: boolean } {
    const tls: TimelineSegment[] = segments.map((s) => ({ id: s.rec.id, startSec: s.startSec, endSec: s.endSec }));
    return findSegmentAt(tls, targetSec);
  }

  function handleClick(e: MouseEvent) {
    const target = (e.currentTarget as HTMLElement);
    const rect = target.getBoundingClientRect();
    const pct = (e.clientX - rect.left) / rect.width;
    const targetSec = pct * 86400;

    const { seg, offset, snapped } = findSegment(targetSec);
    if (!seg) {
      snapNotice = t('timeline.noRecordings');
      return;
    }
    if (snapped) {
      snapNotice = t('timeline.snappedToNearest');
    } else {
      snapNotice = '';
    }
    onseek(seg.id, offset);
  }

  function handleMouseMove(e: MouseEvent) {
    const target = (e.currentTarget as HTMLElement);
    const rect = target.getBoundingClientRect();
    const pct = (e.clientX - rect.left) / rect.width;
    const targetSec = pct * 86400;
    const hh = Math.floor(targetSec / 3600);
    const mm = Math.floor((targetSec % 3600) / 60);
    const ss = Math.floor(targetSec % 60);
    const label = `${String(hh).padStart(2, '0')}:${String(mm).padStart(2, '0')}:${String(ss).padStart(2, '0')}`;
    hoverInfo = { x: e.clientX - rect.left, label };
  }

  function handleMouseLeave() {
    hoverInfo = null;
  }

  // Drag state
  let dragging = $state(false);
  function handleMouseDown(e: MouseEvent) {
    dragging = true;
    handleClick(e);
  }
  function handleWindowMouseMove(e: MouseEvent) {
    if (!dragging) return;
    const bar = document.getElementById('timeline-bar-track');
    if (!bar) return;
    const rect = bar.getBoundingClientRect();
    const pct = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    const targetSec = pct * 86400;
    const { seg, offset } = findSegment(targetSec);
    if (seg) {
      onseek(seg.id, offset);
      snapNotice = '';
    }
  }
  function handleWindowMouseUp() {
    dragging = false;
  }

  $effect(() => {
    if (!dragging) return;
    window.addEventListener('mousemove', handleWindowMouseMove);
    window.addEventListener('mouseup', handleWindowMouseUp);
    return () => {
      window.removeEventListener('mousemove', handleWindowMouseMove);
      window.removeEventListener('mouseup', handleWindowMouseUp);
    };
  });

  // Hour ticks
  const hours = Array.from({ length: 25 }, (_, i) => i);

  function secToPct(sec: number): number {
    return (sec / 86400) * 100;
  }

  function formatLength(sec: number): string {
    return formatLengthUtil(sec);
  }
</script>

<div class="timeline-container">
  <div class="timeline-header">
    <span class="timeline-title">{t('timeline.title')}</span>
    <span class="timeline-summary">
      {#if loading}
        {t('timeline.loading')}
      {:else if error}
        <span class="th-color-danger">{error}</span>
      {:else}
        {formatDate(date)} · {segments.length} {t('timeline.segments')} · {formatLength(totalRecordedSec)}
      {/if}
    </span>
  </div>

  {#if !loading && !error}
    <div class="timeline-track-wrapper">
      <!-- Hour labels -->
      <div class="hour-labels">
        {#each hours as h}
          {#if h % 3 === 0}
            <span class="hour-label" style="left: {secToPct(h * 3600)}%">
              {String(h).padStart(2, '0')}:00
            </span>
          {/if}
        {/each}
      </div>

      <!-- Track -->
      <div
        id="timeline-bar-track"
        class="timeline-track"
        role="slider"
        aria-label={t('timeline.title')}
        aria-valuemin="0"
        aria-valuemax="86400"
        tabindex="0"
        onmousedown={handleMouseDown}
        onmousemove={handleMouseMove}
        onmouseleave={handleMouseLeave}
      >
        <!-- Recording segments -->
        {#each segments as seg (seg.rec.id)}
          <div
            class="timeline-segment"
            style="left: {secToPct(Math.max(0, seg.startSec))}%; width: {secToPct(Math.min(86400, seg.endSec) - Math.max(0, seg.startSec))}%; background: {formatColor[seg.rec.format] || '#6b7280'};"
            title="{formatDate(seg.rec.started_at)} · {seg.rec.format} · {formatLength(seg.endSec - seg.startSec)}"
          ></div>
        {/each}

        <!-- Current playback cursor -->
        {#if cursorPct != null}
          <div class="timeline-cursor" style="left: {cursorPct}%;">
            <div class="timeline-cursor-line"></div>
            <div class="timeline-cursor-head"></div>
          </div>
        {/if}

        <!-- Hover indicator -->
        {#if hoverInfo}
          <div class="timeline-hover" style="left: {hoverInfo.x}px;">
            <div class="timeline-hover-label">{hoverInfo.label}</div>
          </div>
        {/if}
      </div>

      <!-- Grid lines -->
      <div class="hour-grid">
        {#each hours.filter((h) => h % 3 === 0 && h > 0 && h < 24) as h}
          <div class="hour-grid-line" style="left: {secToPct(h * 3600)}%"></div>
        {/each}
      </div>
    </div>

    {#if snapNotice}
      <div class="timeline-notice">⚠️ {snapNotice}</div>
    {/if}

    <!-- Legend -->
    <div class="timeline-legend">
      {#each Object.entries(formatColor) as [fmt, color]}
        {#if segments.some((s) => s.rec.format === fmt)}
          <span class="legend-item">
            <span class="legend-dot" style="background: {color};"></span>
            {fmt.toUpperCase()}
          </span>
        {/if}
      {/each}
    </div>
  {/if}
</div>

<style>
  .timeline-container {
    padding: 0.75rem 1rem;
    border-top: 1px solid var(--border, #e5e7eb);
    user-select: none;
  }
  .timeline-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }
  .timeline-title {
    font-size: 0.8rem;
    font-weight: 600;
    color: var(--text-muted, #6b7280);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .timeline-summary {
    font-size: 0.75rem;
    color: var(--text-muted, #9ca3af);
  }
  .timeline-track-wrapper {
    position: relative;
    padding-top: 1.25rem;
  }
  .hour-labels {
    position: relative;
    height: 1rem;
    margin-bottom: 0.25rem;
  }
  .hour-label {
    position: absolute;
    transform: translateX(-50%);
    font-size: 0.65rem;
    color: var(--text-muted, #9ca3af);
    font-variant-numeric: tabular-nums;
  }
  .timeline-track {
    position: relative;
    height: 2.5rem;
    background: var(--bg-tertiary, #f3f4f6);
    border-radius: 0.375rem;
    cursor: pointer;
    overflow: visible;
  }
  .timeline-segment {
    position: absolute;
    top: 2px;
    bottom: 2px;
    border-radius: 2px;
    min-width: 1px;
    opacity: 0.85;
    transition: opacity 0.15s;
  }
  .timeline-segment:hover {
    opacity: 1;
  }
  .timeline-cursor {
    position: absolute;
    top: -4px;
    bottom: -4px;
    pointer-events: none;
    z-index: 10;
  }
  .timeline-cursor-line {
    position: absolute;
    top: 0;
    bottom: 0;
    left: -1px;
    width: 2px;
    background: #ef4444;
  }
  .timeline-cursor-head {
    position: absolute;
    top: -2px;
    left: -5px;
    width: 10px;
    height: 10px;
    background: #ef4444;
    border-radius: 50%;
    border: 2px solid white;
    box-shadow: 0 1px 3px rgba(0,0,0,0.3);
  }
  .timeline-hover {
    position: absolute;
    top: 0;
    bottom: 0;
    pointer-events: none;
    z-index: 5;
  }
  .timeline-hover-label {
    position: absolute;
    top: -1.5rem;
    transform: translateX(-50%);
    font-size: 0.65rem;
    color: var(--text-primary, #374151);
    background: var(--bg-secondary, #fff);
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--border, #e5e7eb);
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
  .hour-grid {
    position: relative;
    height: 0;
    margin-top: 0;
  }
  .hour-grid-line {
    position: absolute;
    top: -2.5rem;
    height: 2.5rem;
    width: 1px;
    background: var(--border, #d1d5db);
    opacity: 0.4;
    pointer-events: none;
  }
  .timeline-notice {
    margin-top: 0.4rem;
    font-size: 0.7rem;
    color: var(--text-muted, #9ca3af);
  }
  .timeline-legend {
    display: flex;
    gap: 0.75rem;
    margin-top: 0.4rem;
    flex-wrap: wrap;
  }
  .legend-item {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.65rem;
    color: var(--text-muted, #6b7280);
  }
  .legend-dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 2px;
  }

  @media (max-width: 640px) {
    .timeline-track-wrapper {
      overflow-x: auto;
    }
    .timeline-track {
      min-width: 600px;
    }
  }
</style>
