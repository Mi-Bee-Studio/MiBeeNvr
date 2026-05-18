<script lang="ts">
  import { t } from '$lib/i18n';
  import { deleteCameraMergeConfig } from '$lib/api';
  import type { MergeConfig } from '$lib/api';
  import { showToast } from '$lib/toast';

  interface Props {
    cameraId: string;
    mergeConfig: MergeConfig | null;
    mergeConfigLoading: boolean;
    onchange: (config: MergeConfig) => void;
    ondelete: () => void;
  }

  let { cameraId, mergeConfig, mergeConfigLoading, onchange, ondelete }: Props = $props();

  function updateConfig(partial: Partial<MergeConfig>) {
    const updated: MergeConfig = { ...mergeConfig, ...partial };
    onchange(updated);
  }

  async function handleDeleteConfig() {
    try {
      await deleteCameraMergeConfig(cameraId);
      showToast(t('merge.restoredDefault'), 'success');
      ondelete();
    } catch (e) { console.warn('Failed to delete merge config:', e); showToast(t('merge.operationFailed'), 'error'); }
  }
</script>

<details class="mt-6 border th-border rounded-lg"
  open={mergeConfig ? true : undefined}
>
  <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
    {t('merge.title')}
    {#if mergeConfig}
      <span class="text-xs th-text-muted ml-2">{t('merge.customized')}</span>
    {:else}
      <span class="text-xs th-text-muted ml-2">{t('merge.usingDefault')}</span>
    {/if}
  </summary>

  <div class="px-4 pb-4 pt-2">
    {#if mergeConfigLoading}
      <div class="flex items-center gap-2 py-4 th-text-muted">
        <span class="spinner"></span>
        <span class="text-sm">{t('common.loading')}</span>
      </div>
    {:else}
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
        <!-- Enabled -->
        <div class="flex items-center gap-2">
          <input
            id="merge-enabled"
            type="checkbox"
            class="accent-[var(--color-accent)]"
            checked={mergeConfig?.enabled !== false}
            onchange={(e) => updateConfig({ enabled: (e.target as HTMLInputElement).checked })}
          />
          <label for="merge-enabled" class="th-text-secondary text-sm">{t('merge.enableMerge')}</label>
        </div>

        <!-- Check Interval -->
        <div>
          <label for="merge-check-interval" class="input-label">{t('merge.checkInterval')}</label>
          <select
            id="merge-check-interval"
            class="input"
            value={mergeConfig?.check_interval || '1h'}
            onchange={(e) => updateConfig({ check_interval: (e.target as HTMLSelectElement).value })}
          >
            <option value="30m">{t('merge.30m')}</option>
            <option value="1h">{t('merge.1h')}</option>
            <option value="2h">{t('merge.2h')}</option>
            <option value="6h">{t('merge.6h')}</option>
          </select>
        </div>

        <!-- Window Size -->
        <div>
          <label for="merge-window" class="input-label">{t('merge.windowSize')}</label>
          <select
            id="merge-window"
            class="input"
            value={mergeConfig?.window_size || '30m'}
            onchange={(e) => updateConfig({ window_size: (e.target as HTMLSelectElement).value })}
          >
            <option value="30m">{t('merge.30m')}</option>
            <option value="1h">{t('merge.1h')}</option>
            <option value="2h">{t('merge.2h')}</option>
          </select>
        </div>

        <!-- Batch Limit -->
        <div>
          <label for="merge-batch" class="input-label">{t('merge.batchLimit')}</label>
          <input
            id="merge-batch"
            type="number"
            class="input"
            min="10"
            max="1000"
            value={mergeConfig?.batch_limit || 100}
            oninput={(e) => updateConfig({ batch_limit: Number((e.target as HTMLInputElement).value) })}
          />
        </div>

        <!-- Min Segment Age -->
        <div>
          <label for="merge-age" class="input-label">{t('merge.minSegmentAge')}</label>
          <select
            id="merge-age"
            class="input"
            value={mergeConfig?.min_segment_age || '5m'}
            onchange={(e) => updateConfig({ min_segment_age: (e.target as HTMLSelectElement).value })}
          >
            <option value="5m">{t('merge.5m')}</option>
            <option value="10m">{t('merge.10m')}</option>
            <option value="30m">{t('merge.30m')}</option>
            <option value="1h">{t('merge.1h')}</option>
          </select>
        </div>

        <!-- Min Segments to Merge -->
        <div>
          <label for="merge-min-segments" class="input-label">{t('merge.minSegmentsToMerge')}</label>
          <input
            id="merge-min-segments"
            type="number"
            class="input"
            min="2"
            max="50"
            value={mergeConfig?.min_segments_to_merge || 3}
            oninput={(e) => updateConfig({ min_segments_to_merge: Number((e.target as HTMLInputElement).value) })}
          />
        </div>
      </div>

      <!-- Clear override button -->
      <div class="mt-4 flex justify-end">
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          onclick={handleDeleteConfig}
        >
          {t('merge.useGlobalDefault')}
        </button>
      </div>
    {/if}
  </div>
</details>
