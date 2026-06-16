<script lang="ts">
  /**
   * TimelineBar — DVR-style timeline for browsing a camera's recordings.
   *
   * Auto-scales to the actual span of recordings (not fixed 24h) so that
   * segments are always wide enough to see and click. If recordings span
   * only 5 minutes, the timeline shows those 5 minutes; if they span the
   * whole day, it shows 24h.
   *
   * Each recording segment is drawn as a colored block proportional to its
   * duration. Clicking/dragging seeks to that wall-clock moment:
   *   - Hit a segment → onSeek(recordingId, offsetSecondsWithinSegment)
   *   - Hit a gap (no recording) → snap to nearest segment edge + warn
   */
  import { listRecordings, type Recording } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatDate } from '$lib/format';
  import { findSegmentAt, formatLength as formatLengthUtil, type TimelineSegment } from '$lib/timeline-utils';

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

  // --- Dynamic time window (auto-scale to actual recordings) ---

  /** Start of the visible window in epoch-ms */
  let windowStartMs = $state(0);
  /** End of the visible window in epoch-ms */
  let windowEndMs = $state(0);
  /** Span of the visible window in seconds */
  const windowSpanSec = $derived(Math.max(1, (windowEndMs - windowStartMs) / 1000));

  // Current playback position in epoch-ms
  const currentEpochMs = $derived.by(() => {
    if (!currentRecording || !currentRecording.started_at) return null;
    const startedMs = Date.parse(currentRecording.started_at);
    if (isNaN(startedMs)) return null;
    return startedMs + currentVideoTime * 1000;
  });

  // Cursor position as percentage of visible window (0-100), or null if outside
  const cursorPct = $derived.by(() => {
    if (currentEpochMs == null || windowSpanSec <= 0) return null;
    const offsetSec = (currentEpochMs - windowStartMs) / 1000;
    if (offsetSec < 0 || offsetSec > windowSpanSec) return null;
    return (offsetSec / windowSpanSec) * 100;
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

      const dayStartMs = Date.UTC(y, m - 1, dd, 0, 0, 0);

      segments = resp.recordings
        .map((rec) => {
          const sMs = Date.parse(rec.started_at);
          const eMs = rec.ended_at ? Date.parse(rec.ended_at) : sMs + rec.duration * 1000;
          if (isNaN(sMs) || isNaN(eMs)) return null;
          return {
            rec,
            // Store as epoch-ms for dynamic windowing
            startSec: sMs,
            endSec: eMs,
          };
        })
        .filter((x): x is { rec: Recording; startSec: number; endSec: number } => x !== null);

      // --- Auto-compute visible window ---
      if (segments.length > 0) {
        const earliest = segments[0].startSec;
        const latest = segments[segments.length - 1].endSec;
        const span = latest - earliest;
        // Add 10% padding on each side (min 30s, max 1h)
        const padMs = Math.max(30_000, Math.min(3_600_000, span * 0.1));
        windowStartMs = earliest - padMs;
        windowEndMs = latest + padMs;
      } else {
        // No recordings — show 1h window around current recording
        const center = currentRecording ? Date.parse(currentRecording.started_at) : dayStartMs;
        windowStartMs = center - 1800_000;
        windowEndMs = center + 1800_000;
      }
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to load timeline';
      segments = [];
    } finally {
      loading = false;
    }
  }

  // Total recorded duration
  const totalRecordedSec = $derived(
    segments.reduce((sum, s) => sum + (s.endSec - s.startSec) / 1000, 0),
  );

  // Find segment containing targetMs (epoch-ms), or nearest
  function findSegment(targetMs: number): { seg: TimelineSegment | null; offset: number; snapped: boolean } {
    if (segments.length === 0) return { seg: null, offset: 0, snapped: false };

    const tls: TimelineSegment[] = segments.map((s) => ({
      id: s.rec.id,
      startSec: s.startSec / 1000, // convert to seconds for findSegmentAt
      endSec: s.endSec / 1000,
    }));
    const targetSec = targetMs / 1000;
    return findSegmentAt(tls, targetSec);
  }

  // Convert a click X position to epoch-ms
  function clickXtoMs(clientX: number, rect: DOMRect): number {
    const pct = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    return windowStartMs + pct * (windowEndMs - windowStartMs);
  }

  // Format epoch-ms as HH:MM:SS for the current display
  function msToLabel(ms: number): string {
    const d = new Date(ms);
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    const ss = String(d.getSeconds()).padStart(2, '0');
    return `${hh}:${mm}:${ss}`;
  }

  function handleClick(e: MouseEvent) {
    const target = e.currentTarget as HTMLElement;
    const rect = target.getBoundingClientRect();
    const targetMs = clickXtoMs(e.clientX, rect);

    const { seg, offset, snapped } = findSegment(targetMs);
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
    const target = e.currentTarget as HTMLElement;
    const rect = target.getBoundingClientRect();
    const targetMs = clickXtoMs(e.clientX, rect);
    hoverInfo = { x: e.clientX - rect.left, label: msToLabel(targetMs) };
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
    const targetMs = clickXtoMs(e.clientX, rect);
    const { seg, offset } = findSegment(targetMs);
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

  // Generate tick marks — adaptive based on window span
  const ticks = $derived.by(() => {
    const spanMin = windowSpanSec / 60;
    let intervalMin: number;
    let count: number;
    if (spanMin <= 10) { intervalMin = 1; }
    else if (spanMin <= 30) { intervalMin = 5; }
    else if (spanMin <= 120) { intervalMin = 15; }
    else if (spanMin <= 480) { intervalMin = 60; }
    else { intervalMin = 180; }

    const result: { pct: number; label: string }[] = [];
    const intervalMs = intervalMin * 60 * 1000;
    // Align to interval boundary
    const startAligned = Math.ceil(windowStartMs / intervalMs) * intervalMs;
    for (let ms = startAligned; ms <= windowEndMs; ms += intervalMs) {
      const pct = ((ms - windowStartMs) / (windowEndMs - windowStartMs)) * 100;
      if (pct >= 0 && pct <= 100) {
        result.push({ pct, label: msToLabel(ms) });
      }
    }
    return result;
  });

  function msToPct(ms: number): number {
    return ((ms - windowStartMs) / (windowEndMs - windowStartMs)) * 100;
  }

  function formatLength(sec: number): string {
    return formatLengthUtil(sec);
  }

  // Window label (human-readable span)
  const windowLabel = $derived.by(() => {
    const start = msToLabel(windowStartMs);
    const end = msToLabel(windowEndMs);
    const span = windowSpanSec;
    if (span < 3600) return `${start} – ${end} (${formatLength(span)})`;
    return `${start} – ${end}`;
  });
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
        {segments.length} {t('timeline.segments')} · {formatLength(totalRecordedSec)} · {windowLabel}
      {/if}
    </span>
  </div>

  {#if !loading && !error}
    <div class="timeline-track-wrapper">
      <!-- Hour/time labels -->
      <div class="hour-labels">
        {#each ticks as tick}
          <span class="hour-label" style="left: {tick.pct}%">
            {tick.label}
          </span>
        {/each}
      </div>

      <!-- Track -->
      <div
        id="timeline-bar-track"
        class="timeline-track"
        role="slider"
        aria-label={t('timeline.title')}
        tabindex="0"
        onmousedown={handleMouseDown}
        onmousemove={handleMouseMove}
        onmouseleave={handleMouseLeave}
      >
        <!-- Recording segments -->
        {#each segments as seg (seg.rec.id)}
          <div
            class="timeline-segment"
            style="left: {msToPct(seg.startSec)}%; width: {Math.max(0.5, msToPct(seg.endSec) - msToPct(seg.startSec))}%; background: {formatColor[seg.rec.format] || '#6b7280'};"
            title="{formatDate(seg.rec.started_at)} · {seg.rec.format} · {formatLength((seg.endSec - seg.startSec) / 1000)}"
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
    flex-wrap: wrap;
    gap: 0.25rem;
  }
  .timeline-title {
    font-size: 0.8rem;
    font-weight: 600;
    color: var(--text-muted, #6b7280);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .timeline-summary {
    font-size: 0.7rem;
    color: var(--text-muted, #9ca3af);
    font-variant-numeric: tabular-nums;
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
    white-space: nowrap;
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
    min-width: 3px;
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
</style>
