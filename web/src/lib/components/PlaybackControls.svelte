<script lang="ts">
  import { t } from '$lib/i18n';

  interface Props {
    isPlaying: boolean;
    currentFrameIndex: number;
    totalFrames: number;
    playSpeed: number;
    speeds: number[];
    ontoggleplay: () => void;
    onprev: () => void;
    onnext: () => void;
    onsetspeed: (speed: number) => void;
    onprogressclick: (ratio: number) => void;
  }

  let {
    isPlaying = false,
    currentFrameIndex = 0,
    totalFrames = 0,
    playSpeed = 1,
    speeds = [1, 2, 5],
    ontoggleplay,
    onprev,
    onnext,
    onsetspeed,
    onprogressclick,
  } = $props();

  let progressFillEl: HTMLDivElement | undefined = $state();
  let progressThumbEl: HTMLDivElement | undefined = $state();
  let frameCounterEl: HTMLSpanElement | undefined = $state();

  // Direct DOM updates for smooth playback — avoids reactive re-renders on hot path
  export function updatePlaybackUI(index: number, total: number) {
    const progress = total > 1 ? (index / (total - 1)) * 100 : 100;
    if (progressFillEl) {
      progressFillEl.style.width = `${progress}%`;
    }
    if (progressThumbEl) {
      progressThumbEl.style.left = `calc(${progress}% - 6px)`;
    }
    if (frameCounterEl) {
      frameCounterEl.textContent = t('detail.frameCounter', {
        current: String(index + 1),
        total: String(total)
      });
    }
  }

  function handleClick(e: MouseEvent) {
    if (totalFrames === 0) return;
    const target = e.currentTarget as HTMLElement;
    const rect = target.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const ratio = x / rect.width;
    onprogressclick(ratio);
  }

  const prevDisabled = $derived(currentFrameIndex === 0 || isPlaying);
  const nextDisabled = $derived(currentFrameIndex >= totalFrames - 1 || isPlaying);
</script>

<div class="th-bg-secondary px-4 py-3 space-y-2">
  <!-- Progress bar -->
  <div
    class="relative h-2 th-bg-tertiary rounded cursor-pointer group"
    onclick={handleClick}
    onkeydown={(e) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleClick(); }
      else if (e.key === 'ArrowLeft') { e.preventDefault(); onprev(); }
      else if (e.key === 'ArrowRight') { e.preventDefault(); onnext(); }
      else if (e.key === 'Home') { e.preventDefault(); onsetspeed(1); }
    }}
    role="slider"
    tabindex="0"
    aria-label={t('detail.frameCounter', { current: String(currentFrameIndex + 1), total: String(totalFrames) })}
    aria-valuenow={currentFrameIndex}
    aria-valuemin={0}
    aria-valuemax={totalFrames - 1}
  >
    <div
bind:this={progressFillEl}
      class="absolute top-0 left-0 h-full th-bg-accent rounded group-hover:th-bg-info transition-colors"
      style="width: {totalFrames > 1 ? (currentFrameIndex / (totalFrames - 1)) * 100 : 100}%"
    ></div>
    <div
      bind:this={progressThumbEl}
      class="absolute top-1/2 -translate-y-1/2 w-3 h-3 th-bg-info rounded-full shadow group-hover:th-bg-accent transition-colors"
      style="left: calc({totalFrames > 1 ? (currentFrameIndex / (totalFrames - 1)) * 100 : 100}% - 6px)"
    ></div>
  </div>

  <!-- Control buttons -->
  <div class="flex items-center justify-between">
    <div class="flex items-center gap-2">
      <button
        onclick={onprev}
        disabled={prevDisabled}
        class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
        style="color: {prevDisabled ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {prevDisabled ? 'transparent' : 'var(--bg-tertiary)'}"
      >
        {t('detail.prev')}
      </button>

      <button
        onclick={ontoggleplay}
        class="px-4 py-1.5 rounded text-sm font-medium text-white transition-colors"
        style="background-color: {isPlaying ? 'var(--color-danger)' : 'var(--color-info)'}"
      >
        {isPlaying ? t('detail.pause') : t('detail.play')}
      </button>
      <button
        onclick={onnext}
        disabled={nextDisabled}
        class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
        style="color: {nextDisabled ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {!nextDisabled ? 'var(--bg-tertiary)' : 'transparent'}"
      >
        {t('detail.next')}
      </button>
    </div>

    <!-- Frame counter -->
    <span bind:this={frameCounterEl} class="th-text-secondary text-sm font-mono">
      {t('detail.frameCounter', { current: String(currentFrameIndex + 1), total: String(totalFrames) })}
    </span>

    <!-- Speed control -->
    <div class="flex items-center gap-1">
      <span class="th-text-tertiary text-xs mr-1">{t('detail.speed')}</span>
      {#each speeds as speed}
        <button
          onclick={() => onsetspeed(speed)}
          class="px-2 py-1 rounded text-xs font-medium transition-colors"
          style="background-color: {playSpeed === speed ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {playSpeed === speed ? 'white' : 'var(--text-secondary)'}"
        >
          {speed}x
        </button>
      {/each}
    </div>
  </div>
</div>

<!-- Keyboard shortcuts hint -->
<div class="px-4 py-2 th-bg-tertiary">
  <p class="text-xs text-center th-text-muted">
    {t('detail.spacePlayPause')} | {t('detail.arrowSeek')} | {t('detail.escapeBack')}
  </p>
</div>
