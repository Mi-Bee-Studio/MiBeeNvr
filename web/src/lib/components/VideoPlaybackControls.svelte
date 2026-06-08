<script lang="ts">
  import { t } from '$lib/i18n';
  import { Maximize } from 'lucide-svelte';

  interface Props {
    currentTime: number;
    duration: number;
    isPlaying: boolean;
    playbackRate: number;
    buffered: number;
    speeds?: number[];
    isLooping?: boolean;
    ontoggleplay: () => void;
    onseek: (ratio: number) => void;
    onsetspeed: (speed: number) => void;
    onfullscreen: () => void;
    ontoggleloop?: () => void;
    onarrowleft?: () => void;
    onarrowright?: () => void;
  }

  let {
    currentTime = 0,
    duration = 0,
    isPlaying = false,
    playbackRate = 1,
    buffered = 0,
    speeds = [0.5, 1, 1.5, 2],
    isLooping = false,
    ontoggleplay,
    onseek,
    onsetspeed,
    onfullscreen,
    ontoggleloop,
    onarrowleft,
    onarrowright,
  } = $props();

  let progressFillEl: HTMLDivElement | undefined = $state();
  let progressThumbEl: HTMLDivElement | undefined = $state();
  let timeDisplayEl: HTMLSpanElement | undefined = $state();

  // Direct DOM updates for smooth playback — avoids reactive re-renders on hot path
  export function updatePlaybackUI(time: number, dur: number) {
    const progress = dur > 0 ? (time / dur) * 100 : 0;
    if (progressFillEl) progressFillEl.style.width = `${progress}%`;
    if (progressThumbEl) progressThumbEl.style.left = `calc(${progress}% - 6px)`;
    if (timeDisplayEl) {
      timeDisplayEl.textContent = `${formatVideoTime(time)} / ${formatVideoTime(dur)}`;
    }
  }

  function handleSeekClick(e: MouseEvent) {
    if (duration === 0) return;
    const target = e.currentTarget as HTMLElement;
    const rect = target.getBoundingClientRect();
    const x = e.clientX - rect.left;
    onseek(Math.max(0, Math.min(1, x / rect.width)));
  }

  function handleProgressKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      ontoggleplay();
    } else if (e.key === 'ArrowLeft') {
      e.preventDefault();
      onarrowleft?.();
    } else if (e.key === 'ArrowRight') {
      e.preventDefault();
      onarrowright?.();
    } else if (e.key === 'Home') {
      e.preventDefault();
      onsetspeed(1);
    }
  }

  function formatVideoTime(seconds: number): string {
    if (!seconds || !isFinite(seconds)) return '00:00';
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = Math.floor(seconds % 60);
    if (h > 0) {
      return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
    }
    return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  }
</script>

<div class="th-bg-secondary border-t th-border">
  <!-- Progress bar + time display -->
  <div class="flex items-center gap-3 px-4 py-2">
    <div
      class="relative flex-1 h-2 th-bg-tertiary rounded cursor-pointer group"
      role="slider"
      tabindex="0"
      aria-label="Video progress"
      aria-valuemin={0}
      aria-valuemax={duration}
      aria-valuenow={currentTime}
      onclick={handleSeekClick}
      onkeydown={handleProgressKeydown}
    >
      <div class="absolute top-0 left-0 h-full rounded opacity-30 th-bg-accent" style="width: {buffered}%"></div>
      <div
        bind:this={progressFillEl}
        class="absolute top-0 left-0 h-full th-bg-accent rounded group-hover:th-bg-info transition-colors"
        style="width: {duration > 0 ? (currentTime / duration) * 100 : 0}%"
      ></div>
      <div
        bind:this={progressThumbEl}
        class="absolute top-1/2 -translate-y-1/2 w-3 h-3 th-bg-info rounded-full shadow group-hover:th-bg-accent transition-colors"
        style="left: calc({duration > 0 ? (currentTime / duration) * 100 : 0}% - 6px)"
      ></div>
    </div>
    <span bind:this={timeDisplayEl} class="text-xs font-mono th-text-secondary whitespace-nowrap">
      {formatVideoTime(currentTime)} / {formatVideoTime(duration)}
    </span>
  </div>

  <!-- Controls row -->
  <div class="flex items-center justify-between px-4 py-2">
    <div class="flex items-center gap-2">
      <button
        onclick={ontoggleplay}
        class="px-4 py-1.5 rounded text-sm font-medium text-white transition-colors"
        style="background-color: {isPlaying ? 'var(--color-danger)' : 'var(--color-info)'}"
      >
        {isPlaying ? t('detail.pause') : t('detail.play')}
      </button>
    </div>
    <div class="flex items-center gap-1">
      <span class="th-text-tertiary text-xs mr-1">{t('detail.speed')}</span>
      {#each speeds as speed}
        <button
          onclick={() => onsetspeed(speed)}
          class="px-2 py-1 rounded text-xs font-medium transition-colors"
          style="background-color: {playbackRate === speed ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {playbackRate === speed ? 'white' : 'var(--text-secondary)'}"
        >
          {speed}x
        </button>
      {/each}
    </div>
    <div class="flex items-center gap-1">
      <button
        onclick={ontoggleloop}
        class="px-2 py-1 rounded text-xs font-medium transition-colors"
        style="background-color: {isLooping ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {isLooping ? 'white' : 'var(--text-secondary)'}"
        title="Loop playback"
      >
        🔁
      </button>
      <button
        onclick={onfullscreen}
        class="px-2 py-1 rounded text-xs font-medium transition-colors th-bg-tertiary th-text-secondary flex items-center gap-1"
        title={t('live.fullscreen')}
      >
        <Maximize size={14} />
      </button>
    </div>
  </div>

  <!-- Keyboard shortcuts hint -->
  <div class="px-4 py-2 th-bg-tertiary">
    <p class="text-xs text-center th-text-muted">
      {t('detail.spacePlayPause')} | {t('detail.arrowSeek')} | Home {t('detail.homeReset')} | F {t('live.fullscreen')} | L {t('detail.loop')} | {t('detail.escapeBack')}
    </p>
  </div>
</div>
