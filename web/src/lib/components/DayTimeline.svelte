<script lang="ts">
  /**
   * DayTimeline — a 24h multi-camera timeline view.
   *
   * Each camera is one row with a fixed 00:00–24:00 axis. Recording coverage is
   * drawn as colored bands (by format); gaps (≥30s) are left blank. Clicking or
   * dragging resolves to a (recordingId, offset) via the shared findSegmentAt
   * helper and fires onseek. This is the natural interaction model for 24/7
   * continuous recording, as opposed to the per-segment card waterfall in the
   * gallery (which only suits sparse event clips).
   */
  import { findSegmentAt, parseDayStart, epochMsToDaySec, formatLength, type TimelineSegment } from '$lib/timeline-utils';
  import type { Recording, Camera } from '$lib/api';
  import { classLabel, eventTypeLabel } from '$lib/ai-labels';
  import { t } from '$lib/i18n';
import { parseServerDate } from '$lib/format';
  import { effectiveMotion } from '$lib/motion';
  import { Clock } from 'lucide-svelte';

  // Minimal shape DayTimeline actually reads from each recording. Accepting this
  // subset (instead of the full Recording) lets the page pass the lightweight
  // RecordingTimelineSegment[] from /api/recordings/timeline (issue #115) without
  // a cast — both Recording and RecordingTimelineSegment satisfy it.
  type TimelineRecording = Pick<
    Recording,
    'id' | 'camera_id' | 'started_at' | 'ended_at' | 'duration' | 'format' | 'merge_status' | 'motion_score'
  >;

  // Minimal shape of an AI event used for timeline markers. Decoupled from the
  // full AIEvent so Recordings.svelte can pass a projected subset and the
  // component stays independent of the ai-events API module.
  export interface TimelineAIEvent {
    id: number;
    camera_id: string;
    created_at: string; // ISO timestamp
    event_type: string;
    severity: 'info' | 'warning' | 'critical';
    class_name?: string;
    confidence?: number;
    recording_id?: string;
  }

  interface Props {
    cameras: Camera[];
    recordings: TimelineRecording[];
    selectedDate: string; // YYYY-MM-DD
    onseek: (recordingId: string, offsetSeconds: number) => void;
    aiEvents?: TimelineAIEvent[]; // optional AI event markers overlay
  }

  let { cameras, recordings, selectedDate, onseek, aiEvents }: Props = $props();

  // ── Derived: group recordings by camera, build TimelineSegment[] per camera ──
  interface CameraRow {
    camera: Camera;
    segments: TimelineSegment[]; // seconds-from-midnight, for findSegmentAt
    bands: CoverageBand[]; // render data
    coverageSec: number; // total recorded seconds
    pendingCount: number; // un-merged segments (still fragments)
  }

  interface CoverageBand {
    recordingId: string;
    startSec: number; // seconds from midnight
    endSec: number;
    format: string;
    merged: boolean;
    motionScore: number; // -1 = not analyzed (#435)
    motionConfidence: number; // -1 = pre-#634 row (#634)
  }

  const DAY_SECONDS = 86400;
  const dayStartMs = $derived(parseDayStart(selectedDate));

  const rows = $derived.by<CameraRow[]>(() => {
    // Group recordings by camera
    const byCam = new Map<string, TimelineRecording[]>();
    for (const r of recordings) {
      const list = byCam.get(r.camera_id) ?? [];
      list.push(r);
      byCam.set(r.camera_id, list);
    }
    const result: CameraRow[] = [];
    for (const cam of cameras) {
      const recs = (byCam.get(cam.id) ?? []).slice().sort(
        (a, b) => new Date(a.started_at).getTime() - new Date(b.started_at).getTime()
      );
      const bands: CoverageBand[] = [];
      const segs: TimelineSegment[] = [];
      let coverageSec = 0;
      let pendingCount = 0;
      for (const r of recs) {
        const startMs = new Date(r.started_at).getTime();
        const endMs = r.ended_at ? new Date(r.ended_at).getTime() : startMs + (r.duration || 0) * 1000;
        const startSec = clampDay(epochMsToDaySec(startMs, dayStartMs));
        const endSec = clampDay(epochMsToDaySec(endMs, dayStartMs));
        if (endSec <= startSec) continue;
        bands.push({
          recordingId: r.id,
          startSec,
          endSec,
          format: r.format,
          merged: r.merge_status === 'merged' || r.merge_status === 'daily_merged',
          motionScore: r.motion_score ?? -1,
          motionConfidence: r.motion_confidence ?? -1,
        });
        segs.push({ id: r.id, startSec, endSec });
        coverageSec += endSec - startSec;
        if (r.merge_status === 'pending' || r.merge_status === '') pendingCount++;
      }
      result.push({ camera: cam, segments: segs, bands, coverageSec, pendingCount });
    }
    return result;
  });

  const hasAnyRecordings = $derived(rows.some((r) => r.bands.length > 0));

  // ── AI event markers per camera row ──
  // Each event maps to a position on the 24h axis (seconds-from-midnight). High-
  // frequency detection (e.g. person every second) can produce thousands of
  // events/day on one camera; rendering each as a DOM node would both overwhelm
  // the row and merge into an unreadable solid bar. We cluster events within a
  // small time window (CLUSTER_SEC) into a single marker carrying a count badge.
  const CLUSTER_SEC = 2; // events ≤2s apart collapse into one marker

  interface AIMarker {
    /** A stable render key (first event id in cluster, or cluster start sec) */
    key: string;
    /** seconds-from-midnight of the cluster representative */
    sec: number;
    /** cluster size (1 if standalone) */
    count: number;
    /** representative event for color/tooltip */
    event: TimelineAIEvent;
    /** epoch-ms of the representative event (for offset computation on click) */
    epochMs: number;
  }

  const aiMarkersByCam = $derived.by<Map<string, AIMarker[]>>(() => {
    const m = new Map<string, AIMarker[]>();
    if (!aiEvents || aiEvents.length === 0) return m;
    // Bucket by camera first.
    const byCam = new Map<string, TimelineAIEvent[]>();
    for (const e of aiEvents) {
      const list = byCam.get(e.camera_id) ?? [];
      list.push(e);
      byCam.set(e.camera_id, list);
    }
    for (const [camId, evs] of byCam) {
      // Sort by time ascending so clustering is order-independent.
      const sorted = evs
        .map((e) => ({ e, ms: parseServerDate(e.created_at).getTime() }))
        .filter((x) => Number.isFinite(x.ms))
        .sort((a, b) => a.ms - b.ms);
      const markers: AIMarker[] = [];
      let cluster: typeof sorted = [];
      const flush = () => {
        if (cluster.length === 0) return;
        const rep = cluster[0];
        markers.push({
          key: `ai-${rep.e.id}`,
          sec: clampDay(epochMsToDaySec(rep.ms, dayStartMs)),
          count: cluster.length,
          event: rep.e,
          epochMs: rep.ms,
        });
        cluster = [];
      };
      for (const item of sorted) {
        if (cluster.length === 0) {
          cluster = [item];
          continue;
        }
        const last = cluster[cluster.length - 1];
        if ((item.ms - last.ms) / 1000 <= CLUSTER_SEC) {
          cluster.push(item);
        } else {
          flush();
          cluster = [item];
        }
      }
      flush();
      m.set(camId, markers);
    }
    return m;
  });

  // Event marker color: severity first, then class_name, then default.
  // Mirrors TimelineBar.eventColor so the two timelines read consistently.
  function eventColor(evt: TimelineAIEvent): string {
    if (evt.severity === 'critical') return '#ef4444'; // red
    if (evt.severity === 'warning') return '#eab308';  // yellow
    const cn = (evt.class_name || '').toLowerCase();
    if (cn === 'person') return '#22c55e';       // green
    if (['car', 'vehicle', 'truck', 'bus', 'motorcycle', 'bicycle'].includes(cn)) return '#3b82f6'; // blue
    if (['cat', 'dog', 'animal', 'bird', 'horse'].includes(cn)) return '#f97316'; // orange
    return '#3b82f6'; // default blue (info)
  }

  // Resolve a marker click → (recordingId, offset). Prefer the event's own
  // recording_id if it lands on one of this row's segments; otherwise snap with
  // findSegmentAt. Returns null when there is no segment to jump to.
  function resolveMarkerTarget(
    row: CameraRow,
    marker: AIMarker,
  ): { recordingId: string; offset: number } | null {
    // Fast path: event carries a recording_id that belongs to this row.
    if (marker.event.recording_id) {
      const seg = row.segments.find((s) => s.id === marker.event.recording_id);
      if (seg) {
        const off = Math.floor((marker.epochMs - dayStartMs) / 1000 - seg.startSec);
        return { recordingId: seg.id, offset: Math.max(0, off) };
      }
    }
    // Fallback: snap to nearest segment at the marker's day-second.
    const res = findSegmentAt(row.segments, marker.sec);
    if (res.seg) return { recordingId: res.seg.id, offset: Math.max(0, Math.floor(res.offset)) };
    return null;
  }

  function isMarkerReachable(row: CameraRow, marker: AIMarker): boolean {
    return resolveMarkerTarget(row, marker) !== null;
  }

  function onMarkerClick(e: MouseEvent, row: CameraRow, marker: AIMarker) {
    // Stop the click from also triggering the underlying band seek.
    e.stopPropagation();
    const target = resolveMarkerTarget(row, marker);
    if (target) onseek(target.recordingId, target.offset);
  }

  // ── Marker hover tooltip ──
  let hoveredMarker = $state<AIMarker | null>(null);
  let hoveredMarkerCam = $state<string>('');
  let markerTooltipX = $state(0);
  let markerTooltipY = $state(0);

  function onMarkerEnter(e: MouseEvent, marker: AIMarker, camId: string) {
    e.stopPropagation();
    hoveredMarker = marker;
    hoveredMarkerCam = camId;
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    markerTooltipX = rect.left + rect.width / 2;
    markerTooltipY = rect.top;
  }
  function onMarkerLeave() {
    hoveredMarker = null;
    hoveredMarkerCam = '';
  }

  function markerTooltipLabel(marker: AIMarker): string {
    const parts = [secToClock(marker.sec), eventTypeLabel(marker.event.event_type)];
    if (marker.event.class_name) parts.push(classLabel(marker.event.class_name));
    if (marker.count > 1) parts.push(`×${marker.count}`);
    return parts.join(' · ');
  }

  // ── Motion heat (#435) ──
  // When on, video bands with an analyzed motion_score are colored by activity
  // (green = calm → red = busy) instead of the flat per-format color, turning
  // each row into an activity heat strip. Bands without a score (or non-video
  // formats like timelapse/mjpeg) keep their format color.
  let showHeat = $state(true);
  const rowsHaveHeat = $derived(rows.some((r) => r.bands.some((b) => b.motionScore >= 0)));

  function heatColor(score: number): string {
    const hue = Math.max(0, Math.round(120 - Math.min(1, Math.max(0, score)) * 120));
    return `hsl(${hue} 65% 42%)`;
  }
  function bandHeatStyle(band: CoverageBand): string | undefined {
    if (!showHeat || band.motionScore < 0) return undefined;
    if (band.format !== 'h264' && band.format !== 'h265') return undefined;
    // #634: confidence-discounted score — a bitrate-starved segment's raw
    // score is rate-control jitter, not activity.
    const eff = effectiveMotion({ motion_score: band.motionScore, motion_confidence: band.motionConfidence }) ?? 0;
    return `background: ${heatColor(eff)}`;
  }

  // ── Format → color (matches gallery card format conventions) ──
  function bandClass(band: CoverageBand): string {
    const fmt = band.format;
    // blue=h264/h265, purple=avi, cyan=timelapse, gray=mjpeg
    if (fmt === 'h264' || fmt === 'h265') return 'band-video';
    if (fmt === 'avi') return 'band-avi';
    if (fmt === 'timelapse') return 'band-timelapse';
    if (fmt === 'mjpeg') return 'band-mjpeg';
    return 'band-video';
  }

  function coveragePct(row: CameraRow): number {
    return Math.min(100, Math.round((row.coverageSec / DAY_SECONDS) * 100));
  }

  // ── Hover tooltip ──
  let hoveredBand = $state<CoverageBand | null>(null);
  let hoveredCam = $state<string>('');
  let tooltipX = $state(0);
  let tooltipY = $state(0);

  function bandTimeRange(band: CoverageBand): string {
    return `${secToClock(band.startSec)} – ${secToClock(band.endSec)}`;
  }
  function secToClock(sec: number): string {
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
  }

  // ── Click/drag seek ──
  let trackEl: HTMLElement | null = $state(null);
  let dragging = $state(false);

  function xToSec(clientX: number): number {
    if (!trackEl) return 0;
    const rect = trackEl.getBoundingClientRect();
    const ratio = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    return ratio * DAY_SECONDS;
  }

  function handleSeek(clientX: number, segments: TimelineSegment[]) {
    const targetSec = xToSec(clientX);
    const result = findSegmentAt(segments, targetSec);
    if (result.seg) {
      onseek(result.seg.id, Math.max(0, Math.floor(result.offset)));
    }
  }

  function onTrackClick(e: MouseEvent, row: CameraRow) {
    trackEl = e.currentTarget as HTMLElement;
    handleSeek(e.clientX, row.segments);
  }

  function onTrackMouseDown(e: MouseEvent, row: CameraRow) {
    trackEl = e.currentTarget as HTMLElement;
    dragging = true;
    const moveHandler = (ev: MouseEvent) => {
      if (!dragging) return;
      handleSeek(ev.clientX, row.segments);
    };
    const upHandler = () => {
      dragging = false;
      window.removeEventListener('mousemove', moveHandler);
      window.removeEventListener('mouseup', upHandler);
    };
    window.addEventListener('mousemove', moveHandler);
    window.addEventListener('mouseup', upHandler);
  }

  function onBandEnter(e: MouseEvent, band: CoverageBand, camId: string) {
    hoveredBand = band;
    hoveredCam = camId;
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    tooltipX = rect.left + rect.width / 2;
    tooltipY = rect.top;
  }
  function onBandLeave() {
    hoveredBand = null;
    hoveredCam = '';
  }

  // Hour tick marks: 00, 06, 12, 18, 24
  const hourTicks = [0, 6, 12, 18, 24];

  function cameraName(cam: Camera): string {
    return cam.name || cam.id;
  }

  function clampDay(sec: number): number {
    return Math.max(0, Math.min(DAY_SECONDS, sec));
  }
</script>

{#if !hasAnyRecordings}
  <div class="card p-12 text-center border th-border">
    <div class="flex justify-center mb-4 th-text-tertiary">
      <Clock size={48} />
    </div>
    <p class="th-text-secondary">{t('library.timelineNoRecordings')}</p>
  </div>
{:else}
  <div class="space-y-1">
    <!-- Hour scale header -->
    <div class="flex items-center pl-[160px] sm:pl-[200px] pr-2 mb-1 select-none">
      {#if rowsHaveHeat}
        <button
          type="button"
          class="absolute -translate-x-full mr-2 text-[10px] px-1.5 py-0.5 rounded border th-border th-text-secondary whitespace-nowrap {showHeat ? 'heat-toggle-active' : ''}"
          style="position: relative; left: -8px"
          onclick={() => (showHeat = !showHeat)}
          title={t('recordings.heatToggleHint')}
        >
          🌡 {t('recordings.heatToggle')}
        </button>
      {/if}
      <div class="relative h-5 w-full">
        {#each hourTicks as h}
          <span
            class="absolute -translate-x-1/2 text-[10px] th-text-tertiary tabular-nums"
            style="left: {(h / 24) * 100}%"
          >
            {String(h).padStart(2, '0')}:00
          </span>
        {/each}
      </div>
    </div>

    {#each rows as row (row.camera.id)}
      {@const hasRecs = row.bands.length > 0}
      <div class="flex items-center gap-2 group">
        <!-- Camera label -->
        <div class="w-[150px] sm:w-[190px] shrink-0 pr-2 text-right">
          <div class="text-sm font-medium th-text-primary truncate" title={cameraName(row.camera)}>
            {cameraName(row.camera)}
          </div>
          <div class="text-[10px] th-text-tertiary tabular-nums">
            {coveragePct(row)}% · {row.bands.length}{#if row.pendingCount > 0} <span class="th-color-warning">({row.pendingCount} ⚠)</span>{/if}
          </div>
        </div>

        <!-- Timeline track -->
        <div
          class="relative flex-1 h-9 rounded track-bg cursor-pointer select-none border th-border"
          role="slider"
          tabindex="0"
          aria-label={cameraName(row.camera) + ' timeline'}
          aria-valuenow={coveragePct(row)}
          aria-valuemin={0}
          aria-valuemax={100}
          onclick={(e) => hasRecs && onTrackClick(e, row)}
          onmousedown={(e) => hasRecs && onTrackMouseDown(e, row)}
          onkeydown={(e) => {
            if (!hasRecs) return;
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault();
              // Seek to first segment on keyboard activate
              const first = row.segments[0];
              if (first) onseek(first.id, 0);
            }
          }}
        >
          {#if !hasRecs}
            <div class="absolute inset-0 flex items-center justify-center">
              <span class="text-[10px] th-text-tertiary opacity-50">—</span>
            </div>
          {:else}
            {#each row.bands as band (band.recordingId + band.startSec)}
              <div
                class="absolute top-0 bottom-0 band {bandClass(band)}"
                style="left: {(band.startSec / DAY_SECONDS) * 100}%; width: {((band.endSec - band.startSec) / DAY_SECONDS) * 100}%; {bandHeatStyle(band) ?? ''}"
                onmouseenter={(e) => onBandEnter(e, band, row.camera.id)}
                onmouseleave={onBandLeave}
                role="presentation"
              ></div>
            {/each}

            <!-- AI event markers overlay (stage E). Each marker sits above the
                 bands; click jumps to the recording at that moment. -->
            {#if aiMarkersByCam.get(row.camera.id)}
              {@const markers = aiMarkersByCam.get(row.camera.id)!}
              {#each markers as marker (marker.key)}
                {@const reachable = isMarkerReachable(row, marker)}
                <button
                  type="button"
                  class="ai-marker {reachable ? '' : 'ai-marker-unreachable'}"
                  style="left: {(marker.sec / DAY_SECONDS) * 100}%; background: {eventColor(marker.event)};"
                  title={markerTooltipLabel(marker) + (reachable ? '' : ' · ' + t('library.aiMarkerNoRecording'))}
                  aria-label={markerTooltipLabel(marker)}
                  onclick={(e) => reachable && onMarkerClick(e, row, marker)}
                  onmouseenter={(e) => onMarkerEnter(e, marker, row.camera.id)}
                  onmouseleave={onMarkerLeave}
                  disabled={!reachable}
                >
                  {#if marker.count > 1}
                    <span class="ai-marker-count">{marker.count}</span>
                  {/if}
                </button>
              {/each}
            {/if}

            <!-- Hour gridlines (overlay for readability on dense days) -->
            {#each [6, 12, 18] as h}
              <div
                class="absolute top-0 bottom-0 w-px bg-black/10 dark:bg-white/10 pointer-events-none"
                style="left: {(h / 24) * 100}%"
              ></div>
            {/each}
          {/if}

          <!-- Current time indicator (live "now" marker, only for today) -->
          {#if selectedDate === new Date().toLocaleDateString('en-CA')}
            {@const nowSec = (Date.now() - dayStartMs) / 1000}
            {#if nowSec >= 0 && nowSec <= DAY_SECONDS}
              <div
                class="absolute top-0 bottom-0 w-0.5 bg-red-500 pointer-events-none z-10"
                style="left: {(nowSec / DAY_SECONDS) * 100}%"
                title="now"
              ></div>
            {/if}
          {/if}
        </div>
      </div>
    {/each}

    <!-- Legend -->
    <div class="flex items-center gap-4 pl-[160px] sm:pl-[200px] pt-3 text-[10px] th-text-tertiary">
      <span class="flex items-center gap-1"><span class="band-legend band-video"></span>{t('library.legendVideo')}</span>
      <span class="flex items-center gap-1"><span class="band-legend band-avi"></span>AVI</span>
      <span class="flex items-center gap-1"><span class="band-legend band-timelapse"></span>Timelapse</span>
      <span class="flex items-center gap-1"><span class="band-legend band-mjpeg"></span>MJPEG</span>
      <span class="ml-auto">{t('library.timelineHint')}</span>
    </div>
  </div>
{/if}

<!-- Hover tooltip (portal to body to avoid clipping) -->
{#if hoveredBand}
  <div
    class="fixed z-50 pointer-events-none px-2 py-1 rounded bg-black/85 dark:bg-white/90 text-white dark:text-black text-[11px] tabular-nums shadow-lg"
    style="left: {tooltipX}px; top: {tooltipY - 36}px; transform: translateX(-50%)"
  >
    {bandTimeRange(hoveredBand)} · {formatLength(hoveredBand.endSec - hoveredBand.startSec)} · {hoveredBand.format}{#if hoveredBand.motionScore >= 0} · {t('recordings.motionShort')} {(effectiveMotion({ motion_score: hoveredBand.motionScore, motion_confidence: hoveredBand.motionConfidence }) ?? 0).toFixed(2)}{/if}
  </div>
{/if}

<!-- AI marker hover tooltip -->
{#if hoveredMarker}
  {@const hoveredRow = rows.find((r) => r.camera.id === hoveredMarkerCam)}
  <div
    class="fixed z-50 pointer-events-none px-2 py-1 rounded bg-black/85 dark:bg-white/90 text-white dark:text-black text-[11px] shadow-lg"
    style="left: {markerTooltipX}px; top: {markerTooltipY - 36}px; transform: translateX(-50%)"
  >
    {markerTooltipLabel(hoveredMarker)}
    {#if hoveredRow && !isMarkerReachable(hoveredRow, hoveredMarker)}
      <span class="opacity-70"> · {t('library.aiMarkerNoRecording')}</span>
    {/if}
  </div>
{/if}

<style>
  .track-bg {
    background: color-mix(in srgb, currentColor 4%, transparent);
  }
  .band {
    border-radius: 1px;
    transition: opacity 0.1s;
    min-width: 1px;
  }
  .band:hover {
    opacity: 0.75;
  }
  /* Motion-heat toggle (#435) */
  .heat-toggle-active {
    background: linear-gradient(90deg, hsl(120 65% 42%), hsl(60 65% 42%), hsl(0 65% 42%));
    color: #fff;
    border-color: transparent;
  }

  /* Format colors — distinguishable, accessible against track bg */
  .band-video {
    background: #3b82f6; /* blue-500 */
  }
  .band-avi {
    background: #8b5cf6; /* violet-500 */
  }
  .band-timelapse {
    background: #06b6d4; /* cyan-500 */
  }
  .band-mjpeg {
    background: #6b7280; /* gray-500 */
  }
  /* AI event markers — thin vertical bars with a triangular tip, sitting above
     the recording bands. Clickable (button) so keyboard/AT users can reach them. */
  .ai-marker {
    position: absolute;
    top: -3px;
    bottom: -3px;
    width: 3px;
    min-width: 3px;
    border: 0;
    padding: 0;
    margin-left: -1.5px; /* center the bar on its `left` anchor */
    border-radius: 1px;
    cursor: pointer;
    z-index: 5;
    opacity: 0.95;
    transition: opacity 0.1s, transform 0.1s;
    box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.25); /* outline for contrast on any band color */
  }
  .ai-marker:hover {
    opacity: 1;
    transform: scaleX(1.6);
    z-index: 6;
  }
  .ai-marker:focus-visible {
    outline: 2px solid #fff;
    outline-offset: 1px;
    z-index: 6;
  }
  .ai-marker-unreachable {
    opacity: 0.35;
    cursor: not-allowed;
    background: repeating-linear-gradient(
      45deg,
      transparent,
      transparent 1px,
      rgba(107, 114, 128, 0.9) 1px,
      rgba(107, 114, 128, 0.9) 2px
    ) !important;
  }
  .ai-marker-count {
    position: absolute;
    top: -10px;
    left: 50%;
    transform: translateX(-50%);
    font-size: 9px;
    line-height: 1;
    font-weight: 600;
    color: #fff;
    background: rgba(0, 0, 0, 0.7);
    border-radius: 6px;
    padding: 1px 3px;
    pointer-events: none;
    white-space: nowrap;
  }
  .band-legend {
    display: inline-block;
    width: 14px;
    height: 8px;
    border-radius: 1px;
  }
  /* Reduce motion: disable hover transition */
  @media (prefers-reduced-motion: reduce) {
    .band { transition: none; }
  }
</style>
