<script lang="ts">
  import { getImagingSettings, setImagingSettings, getImagingOptions } from '$lib/api';
  import type { ImagingSettings, ImagingOptions } from '$lib/api';
  import { showToast } from '$lib/toast';
  import { t } from '$lib/i18n';
  let { cameraId }: { cameraId: string } = $props();

  let settings = $state<ImagingSettings | null>(null);
  let options = $state<ImagingOptions | null>(null);
  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);

  // Local mutable copies for sliders
  let brightness = $state(0);
  let contrast = $state(0);
  let saturation = $state(0);
  let sharpness = $state(0);
  let exposureMode = $state('AUTO');
  let exposureTime = $state(0);
  let gain = $state(0);
  let wbMode = $state('auto');
  let colorTemperature = $state(3500);

  let debounceTimer: ReturnType<typeof setTimeout> | null = null;

  $effect(() => {
    // React to cameraId changes
    cameraId;
    loadData();
  });

  async function loadData() {
    loading = true;
    error = '';
    try {
      const [s, o] = await Promise.all([
        getImagingSettings(cameraId),
        getImagingOptions(cameraId),
      ]);
      settings = s;
      options = o;
      populateLocal(s);
    } catch (e: any) {
      error = e.message || 'Failed to load imaging settings';
    } finally {
      loading = false;
    }
  }

  function populateLocal(s: ImagingSettings) {
    brightness = s.brightness ?? 0;
    contrast = s.contrast ?? 0;
    saturation = s.saturation ?? 0;
    sharpness = s.sharpness ?? 0;
    exposureMode = s.exposure?.mode ?? 'AUTO';
    exposureTime = s.exposure?.exposure_time ?? 0;
    gain = s.exposure?.gain ?? 0;
    wbMode = s.white_balance?.mode ?? 'auto';
    colorTemperature = s.white_balance?.color_temperature ?? 3500;
  }

  function debouncedSave() {
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => saveSettings(), 300);
  }

  async function saveSettings() {
    const payload: Partial<ImagingSettings> = {
      brightness,
      contrast,
      saturation,
      sharpness,
      exposure: {
        mode: exposureMode,
        exposure_time: exposureTime,
        gain,
      },
      white_balance: {
        mode: wbMode,
        color_temperature: colorTemperature,
      },
    };

    try {
      saving = true;
      await setImagingSettings(cameraId, payload);
      settings = { ...settings, ...payload } as ImagingSettings;
    } catch (e: any) {
      showToast(e.message || 'Failed to save imaging settings', 'error');
    } finally {
      saving = false;
    }
  }

  async function handleReset() {
    if (!settings) return;
    populateLocal(settings);
    await saveSettings();
  }
</script>

<div class="imaging-panel">
  <div class="imaging-header">
    <span class="imaging-title">{t('onvif.imaging.title')}</span>
    {#if saving}
      <span class="imaging-saving"><span class="spinner"></span></span>
    {/if}
  </div>

  {#if loading}
    <div class="imaging-loading">
      <span class="spinner"></span>
    </div>
  {:else if error}
    <div class="imaging-error">{error || t('onvif.imaging.loadError')}</div>
  {:else}
    <div class="imaging-grid">
      <!-- Brightness -->
      {#if options?.brightness}
        <label class="imaging-field">
          <span class="imaging-label">{t('onvif.imaging.brightness')}</span>
          <div class="imaging-slider-row">
            <input
              type="range"
              min={options.brightness.min}
              max={options.brightness.max}
              step="0.01"
              bind:value={brightness}
              oninput={debouncedSave}
              class="imaging-slider"
            />
            <span class="imaging-value">{brightness.toFixed(2)}</span>
          </div>
        </label>
      {/if}

      <!-- Contrast -->
      {#if options?.contrast}
        <label class="imaging-field">
          <span class="imaging-label">{t('onvif.imaging.contrast')}</span>
          <div class="imaging-slider-row">
            <input
              type="range"
              min={options.contrast.min}
              max={options.contrast.max}
              step="0.01"
              bind:value={contrast}
              oninput={debouncedSave}
              class="imaging-slider"
            />
            <span class="imaging-value">{contrast.toFixed(2)}</span>
          </div>
        </label>
      {/if}

      <!-- Saturation -->
      {#if options?.saturation}
        <label class="imaging-field">
          <span class="imaging-label">{t('onvif.imaging.saturation')}</span>
          <div class="imaging-slider-row">
            <input
              type="range"
              min={options.saturation.min}
              max={options.saturation.max}
              step="0.01"
              bind:value={saturation}
              oninput={debouncedSave}
              class="imaging-slider"
            />
            <span class="imaging-value">{saturation.toFixed(2)}</span>
          </div>
        </label>
      {/if}

      <!-- Sharpness -->
      {#if options?.sharpness}
        <label class="imaging-field">
          <span class="imaging-label">{t('onvif.imaging.sharpness')}</span>
          <div class="imaging-slider-row">
            <input
              type="range"
              min={options.sharpness.min}
              max={options.sharpness.max}
              step="0.01"
              bind:value={sharpness}
              oninput={debouncedSave}
              class="imaging-slider"
            />
            <span class="imaging-value">{sharpness.toFixed(2)}</span>
          </div>
        </label>
      {/if}

      <!-- Exposure -->
      <div class="imaging-section">
        <span class="imaging-section-title">{t('onvif.imaging.exposure')}</span>
        <div class="imaging-mode-toggle">
          <button
            class="imaging-mode-btn"
            class:imaging-mode-btn-active={exposureMode === 'AUTO'}
            onclick={() => { exposureMode = 'AUTO'; debouncedSave(); }}
          >{t('onvif.imaging.auto')}</button>
          <button
            class="imaging-mode-btn"
            class:imaging-mode-btn-active={exposureMode === 'MANUAL'}
            onclick={() => { exposureMode = 'MANUAL'; debouncedSave(); }}
          >{t('onvif.imaging.manual')}</button>
        </div>

        {#if exposureMode === 'MANUAL'}
          <div class="imaging-sub-fields">
            {#if options?.exposure_time}
              <label class="imaging-field">
                <span class="imaging-label">{t('onvif.imaging.exposureTime')}</span>
                <div class="imaging-slider-row">
                  <input
                    type="range"
                    min={options.exposure_time.min}
                    max={options.exposure_time.max}
                    step="0.001"
                    bind:value={exposureTime}
                    oninput={debouncedSave}
                    class="imaging-slider"
                  />
                  <span class="imaging-value">{exposureTime.toFixed(3)}</span>
                </div>
              </label>
            {/if}
            {#if options?.gain}
              <label class="imaging-field">
                <span class="imaging-label">{t('onvif.imaging.gain')}</span>
                <div class="imaging-slider-row">
                  <input
                    type="range"
                    min={options.gain.min}
                    max={options.gain.max}
                    step="0.01"
                    bind:value={gain}
                    oninput={debouncedSave}
                    class="imaging-slider"
                  />
                  <span class="imaging-value">{gain.toFixed(2)}</span>
                </div>
              </label>
            {/if}
          </div>
        {/if}
      </div>

      <!-- White Balance -->
      <div class="imaging-section">
        <span class="imaging-section-title">{t('onvif.imaging.whiteBalance')}</span>
        <div class="imaging-mode-toggle">
          <button
            class="imaging-mode-btn"
            class:imaging-mode-btn-active={wbMode === 'auto'}
            onclick={() => { wbMode = 'auto'; debouncedSave(); }}
          >{t('onvif.imaging.auto')}</button>
          <button
            class="imaging-mode-btn"
            class:imaging-mode-btn-active={wbMode === 'manual'}
            onclick={() => { wbMode = 'manual'; debouncedSave(); }}
          >{t('onvif.imaging.manual')}</button>
        </div>

        {#if wbMode === 'manual' && options?.color_temperature}
          <label class="imaging-field">
            <span class="imaging-label">{t('onvif.imaging.colorTemperature')}</span>
            <div class="imaging-slider-row">
              <input
                type="range"
                min={options.color_temperature.min}
                max={options.color_temperature.max}
                step="1"
                bind:value={colorTemperature}
                oninput={debouncedSave}
                class="imaging-slider"
              />
              <span class="imaging-value">{colorTemperature}K</span>
            </div>
          </label>
        {/if}
      </div>
    </div>

    <div class="imaging-actions">
      <button class="btn btn-ghost" onclick={handleReset} disabled={saving}>
        {t('onvif.imaging.resetDefaults')}
      </button>
    </div>
  {/if}
</div>

<style>
  .imaging-panel {
    padding: 0.75rem;
    background-color: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
  }

  .imaging-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 0.75rem;
  }

  .imaging-title {
    font-size: 0.75rem;
    font-weight: 500;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .imaging-saving {
    display: flex;
    align-items: center;
  }

  .imaging-loading {
    display: flex;
    justify-content: center;
    padding: 1.5rem 0;
  }

  .imaging-error {
    font-size: 0.75rem;
    color: var(--color-danger);
    text-align: center;
    padding: 0.25rem 0.5rem;
    background-color: rgba(239, 68, 68, 0.1);
    border-radius: var(--radius-sm);
  }

  .imaging-grid {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .imaging-field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .imaging-label {
    font-size: 0.75rem;
    font-weight: 500;
    color: var(--text-secondary);
  }

  .imaging-slider-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .imaging-slider {
    flex: 1;
    height: 4px;
    appearance: none;
    background: var(--border);
    border-radius: 2px;
    outline: none;
    cursor: pointer;
  }

  .imaging-slider::-webkit-slider-thumb {
    appearance: none;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--color-primary);
    cursor: pointer;
    transition: transform var(--duration-fast) var(--ease-out);
  }

  .imaging-slider::-webkit-slider-thumb:hover {
    transform: scale(1.2);
  }

  .imaging-slider::-moz-range-thumb {
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--color-primary);
    cursor: pointer;
    border: none;
  }

  .imaging-value {
    font-size: 0.6875rem;
    color: var(--text-tertiary);
    min-width: 3.5rem;
    text-align: right;
    font-variant-numeric: tabular-nums;
  }

  .imaging-section {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding-top: 0.5rem;
    border-top: 1px solid var(--border);
  }

  .imaging-section-title {
    font-size: 0.6875rem;
    font-weight: 600;
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .imaging-mode-toggle {
    display: flex;
    gap: 4px;
    background-color: var(--bg-tertiary);
    border-radius: var(--radius-sm);
    padding: 2px;
  }

  .imaging-mode-btn {
    flex: 1;
    padding: 0.25rem 0.5rem;
    font-size: 0.6875rem;
    font-weight: 500;
    border-radius: 6px;
    border: none;
    background: transparent;
    color: var(--text-tertiary);
    cursor: pointer;
    transition: all var(--duration-fast) var(--ease-out);
  }

  .imaging-mode-btn-active {
    background-color: var(--color-primary);
    color: #ffffff;
  }

  .imaging-sub-fields {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .imaging-actions {
    display: flex;
    justify-content: flex-end;
    margin-top: 0.75rem;
    padding-top: 0.5rem;
    border-top: 1px solid var(--border);
  }
</style>
