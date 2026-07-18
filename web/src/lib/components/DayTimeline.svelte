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
  import { t } from '$lib/i18n';
  import { Clock } from 'lucide-svelte';

  interface Props {
    cameras: Camera[];
    recordings: Recording[];
    selectedDate: string; // YYYY-MM-DD
    onseek: (recordingId: string, offsetSeconds: number) => void;
    aiEvents?: unknown[]; // reserved for stage E (AI event markers)
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
  }

  const DAY_SECONDS = 86400;
  const dayStartMs = $derived(parseDayStart(selectedDate));

  const rows = $derived.by<CameraRow[]>(() => {
    // Group recordings by camera
    const byCam = new Map<string, Recording[]>();
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
          merged: !!r.merged || r.merge_status === 'merged',
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
                style="left: {(band.startSec / DAY_SECONDS) * 100}%; width: {((band.endSec - band.startSec) / DAY_SECONDS) * 100}%"
                onmouseenter={(e) => onBandEnter(e, band, row.camera.id)}
                onmouseleave={onBandLeave}
                role="presentation"
              ></div>
            {/each}

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
    {bandTimeRange(hoveredBand)} · {formatLength(hoveredBand.endSec - hoveredBand.startSec)} · {hoveredBand.format}
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
