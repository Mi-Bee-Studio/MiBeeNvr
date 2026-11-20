<script lang="ts">
  // Inline timelapse-merge controls under the frame cycler (start button,
  // progress bar with ETA, cancel, failure retry) — extracted verbatim from
  // PlaybackPanel.svelte. Merge execution stays in the host's MergePanel;
  // this only renders its mirrored state.
  import { Play } from 'lucide-svelte';
  import { t } from '$lib/i18n';

  interface Props {
    mergeState: { inProgress: boolean; pct: number; eta: string; error: string };
    canMerge: boolean;
    onstart: () => void;
    oncancel: () => void;
  }

  let { mergeState, canMerge, onstart, oncancel } = $props();
</script>

{#if !mergeState.inProgress && canMerge}
  <div class="th-bg-secondary px-4 py-3 border-t th-border">
    <div class="flex items-center justify-center gap-2">
      <button onclick={onstart} class="btn btn-primary flex items-center gap-2">
        <Play size={16} /> {t('detail.mergeAndPlay')}
      </button>
    </div>
  </div>
{/if}
{#if mergeState.inProgress}
  <div class="th-bg-secondary px-4 py-3 border-t th-border">
    <div class="flex items-center gap-3 justify-center flex-wrap">
      <div class="w-32 h-1.5 rounded-full th-bg-tertiary overflow-hidden">
        <div class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500" style="width: {mergeState.pct}%"></div>
      </div>
      <span class="text-xs th-text-secondary">{t('detail.mergingProgress', { percent: String(mergeState.pct) })}</span>
      {#if mergeState.eta}
        <span class="text-xs th-text-muted">{mergeState.eta}</span>
      {/if}
      <button onclick={oncancel} class="btn btn-ghost btn-xs text-xs th-color-danger">
        {t('detail.cancelMerge')}
      </button>
    </div>
  </div>
{/if}
{#if mergeState.error}
  <div class="th-bg-secondary px-4 py-3 border-t th-border text-center">
    <div class="flex items-center gap-3 justify-center">
      <span class="text-xs th-color-danger">{t('detail.mergeFailed', { error: mergeState.error })}</span>
      <button onclick={onstart} class="btn btn-secondary btn-sm">{t('detail.mergeRetry')}</button>
    </div>
  </div>
{/if}
