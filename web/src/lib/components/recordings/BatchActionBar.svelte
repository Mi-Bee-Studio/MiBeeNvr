<script lang="ts">
  /**
   * Floating batch action bar for the recordings page: shows the selection
   * count and offers delete / transcode / merge actions.
   *
   * Extracted from Recordings.svelte — the parent owns selection and the async
   * handlers; this component only renders and forwards intent.
   */
  import { t } from '$lib/i18n';

  let {
    count = 0,
    transcodingEnabled = false,
    showMergeButton = false,
    merging = false,
    mergeDuration = $bindable('24h'),
    onDelete = () => {},
    onTranscode = () => {},
    onMerge = () => {},
    onCancel = () => {},
  }: {
    count?: number;
    transcodingEnabled?: boolean;
    showMergeButton?: boolean;
    merging?: boolean;
    mergeDuration: string;
    onDelete?: () => void;
    onTranscode?: () => void;
    onMerge?: () => void;
    onCancel?: () => void;
  } = $props();
</script>

{#if count > 0}
  <div class="fixed bottom-6 left-1/2 -translate-x-1/2 z-40 flex items-center gap-3 px-4 py-2.5 rounded-lg shadow-lg border th-border th-bg-primary">
    <span class="text-sm font-medium th-text-primary">
      {t('recordings.selected', { count: String(count) })}
    </span>
    <button onclick={onDelete} class="btn btn-danger btn-sm">
      {t('recordings.deleteSelected')}
    </button>
    {#if transcodingEnabled}
      <button onclick={onTranscode} class="btn btn-primary btn-sm">
        {t('transcoding.transcode_selected')}
      </button>
    {/if}
    {#if showMergeButton}
      <select class="input input-sm w-auto" bind:value={mergeDuration} disabled={merging}>
        <option value="1h">{t('timelapse.mergeDuration1h')}</option>
        <option value="8h">{t('timelapse.mergeDuration8h')}</option>
        <option value="12h">{t('timelapse.mergeDuration12h')}</option>
        <option value="24h">{t('timelapse.mergeDuration24h')}</option>
        <option value="natural-day">{t('timelapse.mergeDurationNaturalDay')}</option>
        <option value="7d">{t('timelapse.mergeDuration7d')}</option>
        <option value="30d">{t('timelapse.mergeDuration30d')}</option>
      </select>
      <button onclick={onMerge} class="btn btn-primary btn-sm" disabled={merging}>
        {t('timelapse.batchMerge')}
      </button>
    {/if}
    <button onclick={onCancel} class="btn btn-ghost btn-sm">
      {t('recordings.cancel')}
    </button>
  </div>
{/if}
