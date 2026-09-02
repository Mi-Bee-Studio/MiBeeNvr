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
  import { getRecordingsTimeline, type Recording, type RecordingTimelineSegment } from '$lib/api';
  import { listAIEvents, type AIEvent } from '$lib/api/ai-events';
  import { t } from '$lib/i18n';
  import { formatDate, parseServerDate } from '$lib/format';
  import { findSegmentAt, formatLength as formatLengthUtil, type TimelineSegment } from '$lib/timeline-utils';
  import { effectiveMotion } from '$lib/motion';

  let {
    cameraId,
    date,
    currentRecording,
    currentVideoTime = 0,
    onseek,
    showEvents = false,
  }: {
    cameraId: string;
    date: string; // YYYY-MM-DD
    currentRecording: Recording | null;
    currentVideoTime?: number;
    onseek: (recordingId: string, offsetSeconds: number) => void;
    showEvents?: boolean;
  } = $props();

  // Each segment carries the lightweight RecordingTimelineSegment projection
  // (7 fields) from /api/recordings/timeline — issue #115. The old path used
  // /api/recordings with limit=10000, but the backend clamped that to 500 full
  // rows, silently dropping the afternoon on fragmented cameras. The lightweight
  // endpoint caps at 10k and the rows are ~10x smaller.
  let segments = $state<Array<{ rec: RecordingTimelineSegment; startSec: number; endSec: number }>>([]);
  let aiEvents = $state<AIEvent[]>([]);
  let loading = $state(false);
  let error = $state('');
  let hoverInfo = $state<{ x: number; label: string } | null>(null);
  let snapNotice = $state('');

  // Split segments into regular recordings vs timelapse for dual-track display
  const regularSegments = $derived(segments.filter((s) => s.rec.format !== 'timelapse'));
  const timelapseSegments = $derived(segments.filter((s) => s.rec.format === 'timelapse'));
  const hasTimelapse = $derived(timelapseSegments.length > 0);

  // Compute gaps between consecutive segments (>30s = frame drop / 断帧)
  interface Gap {
    startSec: number;
    endSec: number;
    duration: number;
  }
  const gaps = $derived.by(() => {
    const result: Gap[] = [];
    const segs = [...regularSegments, ...timelapseSegments].sort((a, b) => a.startSec - b.startSec);
    for (let i = 1; i < segs.length; i++) {
      const gapDur = (segs[i].startSec - segs[i - 1].endSec) / 1000;
      if (gapDur >= 30) {
        result.push({
          startSec: segs[i - 1].endSec,
          endSec: segs[i].startSec,
          duration: gapDur,
        });
      }
    }
    return result;
  });

  // Format → color class (only used for formats that never get motion analysis;
  // video segments are filled by their heat color below)
  const formatColor: Record<string, string> = {
    mjpeg: '#f97316', // orange
  };

  // Heat coloring (#435): video segments are filled by their motion score
  // (green = calm → red = busy) — identical to DayTimeline and the former
  // standalone activity strip, so every day-axis in the app reads the same.
  // The codec of a segment stays available in its tooltip and in the player
  // header; only the h264-vs-h265 at-a-glance distinction is given up.
  const UNANALYZED_COLOR = 'rgba(96, 165, 250, 0.55)';

  function heatColor(score: number): string {
    const hue = Math.max(0, Math.round(120 - Math.min(1, Math.max(0, score)) * 120));
    return `hsl(${hue} 65% 42%)`;
  }

  function isVideoFormat(fmt?: string): boolean {
    return fmt === 'h264' || fmt === 'h265' || fmt === 'timelapse';
  }

  function segmentFill(rec: RecordingTimelineSegment): string {
    if (isVideoFormat(rec.format)) {
      const eff = effectiveMotion(rec);
      return eff !== null ? heatColor(eff) : UNANALYZED_COLOR;
    }
    return formatColor[rec.format] || '#6b7280';
  }

  function segmentScoreLabel(rec: RecordingTimelineSegment): string {
    if (!isVideoFormat(rec.format)) return '';
    const eff = effectiveMotion(rec);
    if (eff === null) return ` · ${t('timeline.score')}: ${t('timeline.unanalyzed')}`;
    const conf = rec.motion_confidence ?? -1;
    // #634: flag low-confidence (bitrate-starved) segments in the tooltip.
    const lowConf = conf >= 0 && conf < 0.5 ? ' ⚠' : '';
    return ` · ${t('timeline.score')}: ${eff.toFixed(2)}${lowConf}`;
  }

  // Current recording's activity badge (moved from the standalone activity strip)
  const currentScore = $derived.by(() => {
    if (!currentRecording || !isVideoFormat(currentRecording.format)) return null;
    return effectiveMotion(currentRecording);
  });
  const hasUnanalyzed = $derived(
    segments.some((s) => isVideoFormat(s.rec.format) && (s.rec.motion_score ?? -1) < 0),
  );

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
    if (showEvents) void loadAIEvents(cid, d);
  });

  async function loadAIEvents(cid: string, d: string) {
    try {
      const [y, m, dd] = d.split('-').map(Number);
      // Local-midnight range: a "day" is the user's calendar day, so recordings
      // from local 00:00–08:00 (UTC previous-day 16:00–24:00) are included.
      const startISO = new Date(y, m - 1, dd, 0, 0, 0).toISOString();
      const endISO = new Date(y, m - 1, dd, 23, 59, 59).toISOString();
      // High-frequency detection (e.g. person every second) can produce thousands
      // of events/day. asc + a low cap would silently drop evening event markers.
      const resp = await listAIEvents({ camera_id: cid, start: startISO, end: endISO, asc: true, limit: 2000 });
      aiEvents = resp.events || [];
    } catch {
      aiEvents = []; // silent fail — events are an overlay, not critical
    }
  }

  async function loadSegments(cid: string, d: string) {
    loading = true;
    error = '';
    snapNotice = '';
    try {
      const [y, m, dd] = d.split('-').map(Number);
      // Local-midnight range (see loadAIEvents comment).
      const startISO = new Date(y, m - 1, dd, 0, 0, 0).toISOString();
      const endISO = new Date(y, m - 1, dd, 23, 59, 59).toISOString();
      // Lightweight timeline endpoint (issue #115): the old /api/recordings call
      // with limit=10000 was silently clamped to 500 full rows by the backend,
      // dropping the afternoon on fragmented cameras. This endpoint caps at 10k
      // 7-column rows and ships ~10x less data per day.
      const resp = await getRecordingsTimeline({
        camera_id: cid,
        start: startISO,
        end: endISO,
      });

      const dayStartMs = new Date(y, m - 1, dd).getTime();

      segments = resp.segments
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
        .filter((x): x is { rec: RecordingTimelineSegment; startSec: number; endSec: number } => x !== null);

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
    const bar = document.getElementById('timeline-bar-track') || document.getElementById('timeline-bar-track-tl');
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

  // Event marker color: severity takes priority, then class_name, then default.
  function eventColor(evt: AIEvent): string {
    if (evt.severity === 'critical') return '#ef4444'; // red
    if (evt.severity === 'warning') return '#eab308';  // yellow
    // Map common class names to colors (matching AiOverlay palette).
    const cn = (evt.class_name || '').toLowerCase();
    if (cn === 'person') return '#22c55e';       // green
    if (['car', 'vehicle', 'truck', 'bus', 'motorcycle', 'bicycle'].includes(cn)) return '#3b82f6'; // blue
    if (['cat', 'dog', 'animal', 'bird', 'horse'].includes(cn)) return '#f97316'; // orange
    return '#3b82f6'; // default blue (info)
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
    <div class="timeline-meta">
      <span class="timeline-summary">
        {#if loading}
          {t('timeline.loading')}
        {:else if error}
          <span class="th-color-danger">{error}</span>
        {:else}
          {segments.length} {t('timeline.segments')} · {formatLength(totalRecordedSec)}{#if gaps.length > 0} · <span class="th-color-danger">⚠ {gaps.length} {t('timeline.frameDrops', { default: '断帧' })}</span>{/if} · {windowLabel}
        {/if}
      </span>
      {#if currentScore != null}
        <span
          class="score-badge"
          style="background: {heatColor(currentScore)}"
          title={t('timeline.score')}
        >
          {currentScore.toFixed(2)}
        </span>
        {#if currentRecording?.activity_flags}
          <span class="flags-label">{currentRecording.activity_flags}</span>
        {/if}
      {/if}
    </div>
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

      <!-- Regular recordings track -->
      {#if regularSegments.length > 0 || !hasTimelapse}
        {#if hasTimelapse}
          <div class="track-label">{t('timeline.recordings')}</div>
        {/if}
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
          {#each regularSegments as seg (seg.rec.id)}
            <div
              class="timeline-segment"
              style="left: {msToPct(seg.startSec)}%; width: {Math.max(0.5, msToPct(seg.endSec) - msToPct(seg.startSec))}%; background: {segmentFill(seg.rec)};"
              title="{formatDate(seg.rec.started_at)} · {seg.rec.format} · {formatLength((seg.endSec - seg.startSec) / 1000)}{segmentScoreLabel(seg.rec)}"
            ></div>
          {/each}

          <!-- Gap markers (断帧 / frame drops) -->
          {#each gaps as gap, i}
            <div
              class="timeline-gap"
              style="left: {msToPct(gap.startSec)}%; width: {Math.max(0.3, msToPct(gap.endSec) - msToPct(gap.startSec))}%;"
              title="⚠ {t('timeline.frameDrop', { default: '断帧' })} · {formatLength(gap.duration)}"
            ></div>
          {/each}

          <!-- AI event markers -->
          {#if showEvents}
            {#each aiEvents as evt}
              {@const evtMs = parseServerDate(evt.created_at).getTime()}
              {@const evtColor = eventColor(evt)}
              <div
                class="timeline-event-marker"
                style="left: {msToPct(evtMs)}%; background-color: {evtColor};"
                title="{evt.class_name || evt.event_type} · {evt.severity} · {Math.round(evt.confidence * 100)}% · {formatDate(evt.created_at)}"
              ></div>
            {/each}
          {/if}

          <!-- Current playback cursor (only on this track if not timelapse) -->
          {#if cursorPct != null && (!currentRecording || currentRecording.format !== 'timelapse')}
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
      {/if}

      <!-- Timelapse track (parallel) -->
      {#if hasTimelapse}
        <div class="track-label">{t('timeline.timelapse')}</div>
        <div
          id="timeline-bar-track-tl"
          class="timeline-track timeline-track-tl"
          role="slider"
          aria-label={t('timeline.timelapse')}
          tabindex="0"
          onmousedown={handleMouseDown}
          onmousemove={handleMouseMove}
          onmouseleave={handleMouseLeave}
        >
          <!-- Timelapse segments -->
          {#each timelapseSegments as seg (seg.rec.id)}
            <div
              class="timeline-segment"
              style="left: {msToPct(seg.startSec)}%; width: {Math.max(0.5, msToPct(seg.endSec) - msToPct(seg.startSec))}%; background: {segmentFill(seg.rec)};"
              title="{formatDate(seg.rec.started_at)} · timelapse · {formatLength((seg.endSec - seg.startSec) / 1000)}{segmentScoreLabel(seg.rec)}"
            ></div>
          {/each}

          <!-- Current playback cursor (only on this track if timelapse) -->
          {#if cursorPct != null && currentRecording?.format === 'timelapse'}
            <div class="timeline-cursor" style="left: {cursorPct}%;">
              <div class="timeline-cursor-line"></div>
              <div class="timeline-cursor-head"></div>
            </div>
          {/if}
        </div>
      {/if}
    </div>

    {#if snapNotice}
      <div class="timeline-notice">⚠️ {snapNotice}</div>
    {/if}

    <!-- Legend -->
    <div class="timeline-legend">
      <span class="legend-item">
        {t('timeline.heatCalm')}
        <span class="legend-gradient"></span>
        {t('timeline.heatBusy')}
      </span>
      {#if hasUnanalyzed}
        <span class="legend-item">
          <span class="legend-dot" style="background: {UNANALYZED_COLOR};"></span>
          {t('timeline.unanalyzed')}
        </span>
      {/if}
      {#if segments.some((s) => s.rec.format === 'mjpeg')}
        <span class="legend-item">
          <span class="legend-dot" style="background: {formatColor.mjpeg};"></span>
          MJPEG
        </span>
      {/if}
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
  .timeline-meta {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .score-badge {
    font-size: 0.65rem;
    font-weight: 600;
    color: #fff;
    padding: 0 0.4rem;
    border-radius: 9999px;
    font-variant-numeric: tabular-nums;
  }
  .flags-label {
    font-size: 0.65rem;
    color: var(--text-muted, #9ca3af);
  }
  .timeline-track-wrapper {
    position: relative;
    padding-top: 1.25rem;
  }
  .track-label {
    font-size: 0.6rem;
    color: var(--text-muted, #9ca3af);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin-top: 0.5rem;
    margin-bottom: 0.15rem;
    padding-left: 0.25rem;
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
    margin-bottom: 0.25rem;
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
  .timeline-gap {
    position: absolute;
    top: 2px;
    bottom: 2px;
    min-width: 2px;
    background: repeating-linear-gradient(
      45deg,
      rgba(239, 68, 68, 0.25),
      rgba(239, 68, 68, 0.25) 3px,
      rgba(239, 68, 68, 0.08) 3px,
      rgba(239, 68, 68, 0.08) 6px
    );
    border-left: 1px dashed rgba(239, 68, 68, 0.5);
    border-right: 1px dashed rgba(239, 68, 68, 0.5);
    z-index: 5;
    pointer-events: auto;
    cursor: help;
  }
  .timeline-event-marker {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 3px;
    min-width: 2px;
    border-radius: 1px;
    opacity: 0.8;
    z-index: 8;
    pointer-events: auto;
    cursor: help;
    transition: opacity 0.15s;
  }
  .timeline-event-marker:hover {
    opacity: 1;
    width: 4px;
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
  .legend-gradient {
    display: inline-block;
    width: 36px;
    height: 8px;
    border-radius: 4px;
    background: linear-gradient(
      90deg,
      hsl(120 65% 42%),
      hsl(60 65% 42%),
      hsl(0 65% 42%)
    );
  }
</style>
