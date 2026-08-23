<script lang="ts">
  /**
   * Per-recording activity heat strip (#470 follow-up): shows the recording's
   * own motion score plus a day-wide heat strip of the SAME camera's segments
   * (green = calm → red = busy, identical coloring to the Recordings day
   * timeline), with the current recording outlined. Clicking another band
   * navigates to that recording's detail page — the activity context the
   * Recordings page has, now available while watching a specific recording.
   */
  import { onMount } from 'svelte';
  import { getRecordingsTimeline } from '$lib/api';
  import type { Recording, RecordingTimelineSegment } from '$lib/api';
  import { t } from '$lib/i18n';
  import { Flame } from 'lucide-svelte';

  let { recording }: { recording: Recording } = $props();

  let segments = $state<RecordingTimelineSegment[]>([]);
  let dayStart = $state(0);
  let dayEnd = $state(0);
  let failed = $state(false);

  // Same heat coloring as DayTimeline.svelte (green→red by motion_score).
  function heatColor(score: number): string {
    const hue = Math.max(0, Math.round(120 - Math.min(1, Math.max(0, score)) * 120));
    return `hsl(${hue} 65% 42%)`;
  }

  function isVideo(fmt?: string): boolean {
    return fmt === 'h264' || fmt === 'h265';
  }

  function bandStyle(seg: RecordingTimelineSegment): string {
    const start = new Date(seg.started_at).getTime();
    const end = new Date(seg.ended_at).getTime();
    const left = ((start - dayStart) / (dayEnd - dayStart)) * 100;
    const width = Math.max(0.15, ((end - start) / (dayEnd - dayStart)) * 100);
    let bg = 'var(--text-tertiary, #9ca3af)';
    if (isVideo(seg.format) && (seg.motion_score ?? -1) >= 0) {
      bg = heatColor(seg.motion_score!);
    } else if (isVideo(seg.format)) {
      bg = 'rgba(96, 165, 250, 0.45)'; // analyzed-pending video band
    }
    return `left: ${left}%; width: ${width}%; background: ${bg};`;
  }

  function isCurrent(seg: RecordingTimelineSegment): boolean {
    return seg.id === recording.id;
  }

  function fmtTime(iso: string): string {
    return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  function go(seg: RecordingTimelineSegment): void {
    if (!isCurrent(seg)) {
      window.location.hash = `#/recordings/${seg.id}`;
    }
  }

  onMount(async () => {
    // Local-midight-to-midnight window containing this recording.
    const d = new Date(recording.started_at);
    const start = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    const end = new Date(start.getTime() + 24 * 3600 * 1000);
    dayStart = start.getTime();
    dayEnd = end.getTime();
    try {
      const resp = await getRecordingsTimeline({
        camera_id: recording.camera_id,
        start: start.toISOString(),
        end: end.toISOString(),
      });
      segments = resp.segments ?? [];
    } catch {
      failed = true; // strip silently hidden — non-critical context UI
    }
  });
</script>

{#if !failed && segments.length > 0}
  <div class="card p-4 border th-border space-y-3">
    <div class="flex items-center gap-3 flex-wrap">
      <span class="text-sm font-medium th-text-primary flex items-center gap-1.5">
        <Flame size={14} /> {t('detail.activity.title')}
      </span>
      {#if isVideo(recording.format)}
        {#if (recording.motion_score ?? -1) >= 0}
          <span
            class="text-xs font-semibold px-2 py-0.5 rounded-full tabular-nums text-white"
            style="background: {heatColor(recording.motion_score!)}"
            title={t('detail.activity.score')}
          >
            {recording.motion_score!.toFixed(2)}
          </span>
          {#if recording.activity_flags}
            <span class="text-xs th-text-secondary">{recording.activity_flags}</span>
          {/if}
        {:else}
          <span class="text-xs th-text-muted">{t('detail.activity.noScore')}</span>
        {/if}
      {/if}
      <span class="text-xs th-text-muted ml-auto">{t('detail.activity.hint')}</span>
    </div>

    <div class="heat-strip" role="img" aria-label={t('detail.activity.title')}>
      {#each segments as seg (seg.id)}
        <button
          type="button"
          class="band {isCurrent(seg) ? 'band-current' : ''}"
          style={bandStyle(seg)}
          title="{fmtTime(seg.started_at)}–{fmtTime(seg.ended_at)} · {t('detail.activity.score')}: {seg.motion_score != null && seg.motion_score >= 0 ? seg.motion_score.toFixed(2) : t('detail.activity.noScore')}{isCurrent(seg) ? ' · ' + t('detail.activity.current') : ''}"
          onclick={() => go(seg)}
        ></button>
      {/each}
      <div class="now-marker" style="left: {(Math.min(Date.now(), dayEnd) - dayStart) / (dayEnd - dayStart) * 100}%"></div>
    </div>
    <div class="flex justify-between text-[10px] th-text-muted tabular-nums">
      <span>00:00</span><span>06:00</span><span>12:00</span><span>18:00</span><span>24:00</span>
    </div>
  </div>
{/if}

<style>
  .heat-strip {
    position: relative;
    height: 22px;
    border-radius: 6px;
    background: rgba(128, 128, 128, 0.12);
    overflow: hidden;
  }
  .band {
    position: absolute;
    top: 0;
    bottom: 0;
    border: none;
    padding: 0;
    cursor: pointer;
    min-width: 1px;
    opacity: 0.85;
  }
  .band:hover {
    opacity: 1;
  }
  .band-current {
    outline: 2px solid currentColor;
    outline-offset: -2px;
    opacity: 1;
    z-index: 2;
    cursor: default;
  }
  .now-marker {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 1px;
    background: rgba(255, 255, 255, 0.7);
    z-index: 3;
    pointer-events: none;
  }
</style>
