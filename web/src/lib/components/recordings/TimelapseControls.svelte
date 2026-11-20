<script lang="ts">
  // Frame-cycler control bar (scrub slider, prev/play/next, speed ladder,
  // loop/fullscreen) — extracted verbatim from PlaybackPanel.svelte's
  // timelapse mode. State is bindable; actions come back as callbacks.
  import { ChevronLeft, ChevronRight, Pause, Play } from 'lucide-svelte';
  import { t } from '$lib/i18n';

  interface Props {
    frameCount: number;
    currentIndex: number;
    playing: boolean;
    speed: number;
    loop: boolean;
    speeds: number[];
    timestamp: string;
    showAviToggle: boolean;
    onseekindex: (index: number) => void;
    ontoggleplay: () => void;
    onsetspeed: (speed: number) => void;
    ontoggleloop: () => void;
    onfullscreen: () => void;
    onavi: () => void;
  }

  let {
    frameCount,
    currentIndex = $bindable(0),
    playing = $bindable(false),
    speed = $bindable(1),
    loop = $bindable(false),
    speeds,
    timestamp,
    showAviToggle,
    onseekindex,
    ontoggleplay,
    onsetspeed,
    ontoggleloop,
    onfullscreen,
    onavi,
  } = $props();
</script>

<div class="th-bg-secondary px-4 py-3 space-y-2">
  <div
    class="relative h-2 th-bg-tertiary rounded cursor-pointer group"
    role="slider"
    tabindex="0"
    aria-label={t('detail.frameCounter', { current: String(currentIndex + 1), total: String(frameCount) })}
    aria-valuenow={currentIndex}
    aria-valuemin={0}
    aria-valuemax={frameCount - 1}
    onclick={(e) => {
      const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
      const ratio = (e.clientX - rect.left) / rect.width;
      onseekindex(Math.round(ratio * (frameCount - 1)));
    }}
    onkeydown={(e) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); }
      else if (e.key === 'ArrowLeft') { e.preventDefault(); onseekindex(currentIndex - 1); }
      else if (e.key === 'ArrowRight') { e.preventDefault(); onseekindex(currentIndex + 1); }
    }}
  >
    <div
      class="absolute top-0 left-0 h-full th-bg-accent rounded group-hover:th-bg-info transition-colors"
      style="width: {frameCount > 1 ? (currentIndex / (frameCount - 1)) * 100 : 100}%"
    ></div>
    <div
      class="absolute top-1/2 -translate-y-1/2 w-3 h-3 th-bg-info rounded-full shadow group-hover:th-bg-accent transition-colors"
      style="left: calc({frameCount > 1 ? (currentIndex / (frameCount - 1)) * 100 : 100}% - 6px)"
    ></div>
  </div>

  <div class="flex items-center justify-between">
    <div class="flex items-center gap-2">
      <button
        onclick={() => onseekindex(currentIndex - 1)}
        disabled={currentIndex === 0 || playing}
        class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
        style="color: {currentIndex === 0 || playing ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {currentIndex === 0 || playing ? 'transparent' : 'var(--bg-tertiary)'}"
      >
        <ChevronLeft size={16} />
      </button>
      <button
        onclick={ontoggleplay}
        class="px-4 py-1.5 rounded text-sm font-medium text-white transition-colors flex items-center gap-1"
        style="background-color: {playing ? 'var(--color-danger)' : 'var(--color-info)'}"
      >
        {#if playing}
          <Pause size={14} /> {t('detail.pause')}
        {:else}
          <Play size={14} /> {t('detail.play')}
        {/if}
      </button>
      <button
        onclick={() => onseekindex(currentIndex + 1)}
        disabled={currentIndex >= frameCount - 1 || playing}
        class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
        style="color: {currentIndex >= frameCount - 1 || playing ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {currentIndex >= frameCount - 1 || playing ? 'transparent' : 'var(--bg-tertiary)'}"
      >
        <ChevronRight size={16} />
      </button>
    </div>

    <div class="flex items-center gap-3">
      {#if showAviToggle}
        <button
          onclick={onavi}
          class="px-2 py-1 rounded text-xs font-medium th-bg-tertiary th-text-secondary"
          title={t('detail.aviRealtimeTitle')}>
          {t('detail.aviRealtime')}
        </button>
      {/if}
      <span class="th-text-secondary text-sm font-mono">
        {currentIndex + 1} / {frameCount}
      </span>
      <span class="th-text-tertiary text-xs font-mono">
        {timestamp}
      </span>
    </div>

    <div class="flex items-center gap-1">
      <span class="th-text-tertiary text-xs mr-1">{t('detail.speed')}</span>
      {#each speeds as sp}
        <button
          onclick={() => onsetspeed(sp)}
          class="px-2 py-1 rounded text-xs font-medium transition-colors"
          style="background-color: {speed === sp ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {speed === sp ? 'white' : 'var(--text-secondary)'}"
        >
          {sp}x
        </button>
      {/each}
    </div>
    <div class="flex items-center gap-2">
      <button
        onclick={ontoggleloop}
        class="px-2 py-1 rounded text-xs font-medium transition-colors"
        style="background-color: {loop ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {loop ? 'white' : 'var(--text-secondary)'}"
        title="Loop playback"
      >
        🔁 Loop
      </button>
      <button
        onclick={onfullscreen}
        class="px-2 py-1 rounded text-xs font-medium transition-colors th-bg-tertiary th-text-secondary"
        title={t('live.fullscreen')}
      >
        ⛶ {t('live.fullscreen')}
      </button>
    </div>
  </div>
</div>
