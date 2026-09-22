<script lang="ts">
    import { t } from '$lib/i18n';

  interface Props {
    /** Per-camera transcode fields — owned by the parent form (save payload reads them). */
    formTranscodingEnabled?: boolean;
    formTranscodingCodec?: string;
    formTranscodingPreset?: string;
    formTranscodingBitrate?: string;
    formTranscodingCRF?: number;
    globalTranscodingEnabled: boolean;
    h265Available: boolean;
  }

  let {
    formTranscodingEnabled = $bindable(false),
    formTranscodingCodec = $bindable('h264'),
    formTranscodingPreset = $bindable('ultrafast'),
    formTranscodingBitrate = $bindable('2M'),
    formTranscodingCRF = $bindable(0),
    globalTranscodingEnabled,
    h265Available,
  }: Props = $props();
</script>

{#if globalTranscodingEnabled}
  <details class="mt-6 border th-border rounded-lg" open={formTranscodingEnabled ? true : undefined}>
    <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
      {t('transcoding.per_camera_config')}
      {#if formTranscodingEnabled}
        <span class="text-xs th-text-muted ml-2">{t('transcoding.enabled')}</span>
      {:else}
        <span class="text-xs th-text-muted ml-2">{t('merge.usingDefault')}</span>
      {/if}
    </summary>

    <div class="px-4 pb-4 pt-2">
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- Enabled toggle -->
        <div class="md:col-span-2 flex items-center gap-2">
          <input
            id="transcode-enabled"
            type="checkbox"
            class="accent-[var(--color-accent)]"
            bind:checked={formTranscodingEnabled}
          />
          <label for="transcode-enabled" class="th-text-secondary text-sm">{t('transcoding.enabled')}</label>
        </div>

        {#if formTranscodingEnabled}
          <!-- Target Codec -->
          <div>
            <label for="transcode-codec" class="input-label">{t('transcoding.target_codec')}</label>
            <select id="transcode-codec" class="input" bind:value={formTranscodingCodec}>
              <option value="h264">{t('transcoding.codec_h264')}</option>
              <option value="h265" disabled={!h265Available}>{t('transcoding.codec_h265')}{!h265Available ? ` (${t('transcoding.unavailable')})` : ''}</option>
            </select>
            {#if !h265Available}
              <p class="mt-1 text-xs text-[var(--color-danger)]">{t('transcoding.h265_not_available')}</p>
            {:else if formTranscodingCodec === 'h265'}
              <p class="mt-1 text-xs text-[var(--color-warning)]">{t('transcoding.warning_h265_slow')}</p>
            {/if}
          </div>

          <!-- Preset -->
          <div>
            <label for="transcode-preset" class="input-label">{t('transcoding.preset')}</label>
            <select id="transcode-preset" class="input" bind:value={formTranscodingPreset}>
              <option value="ultrafast">{t('transcoding.preset_ultrafast')}</option>
              <option value="faster">{t('transcoding.preset_faster')}</option>
              <option value="medium">{t('transcoding.preset_medium')}</option>
            </select>
          </div>

          <!-- Bitrate -->
          <div>
            <label for="transcode-bitrate" class="input-label">{t('transcoding.bitrate')}</label>
            <input
              id="transcode-bitrate"
              type="text"
              class="input"
              bind:value={formTranscodingBitrate}
              placeholder="2M"
            />
          </div>

          <!-- CRF (Quality) -->
          <div>
            <label for="transcode-crf" class="input-label">{t('transcoding.crf')} <span class="text-xs th-text-muted">({t('transcoding.crfHint')})</span></label>
            <input
              id="transcode-crf"
              type="number"
              min="0"
              max="51"
              class="input"
              bind:value={formTranscodingCRF}
              placeholder="0"
            />
          </div>
        {/if}
      </div>
    </div>
  </details>
{:else}
  <div class="mt-6 p-3 rounded-md th-bg-hover border th-border text-sm th-text-muted flex items-center gap-2">
    <svg class="w-4 h-4 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
    {t('transcoding.warning_global_disabled')}
  </div>
{/if}
