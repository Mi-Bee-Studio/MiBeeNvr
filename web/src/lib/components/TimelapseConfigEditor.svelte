<script lang="ts">
  import { t } from '$lib/i18n';
  import { getTimelapseConfig, updateTimelapseConfig } from '$lib/api';
  import type { TimelapseConfig } from '$lib/api';
  import { showToast } from '$lib/toast';

  interface Props {
    cameraId: string;
  }

  let { cameraId }: Props = $props();

  let config = $state<TimelapseConfig | null>(null);
  let loading = $state(true);
  let saving = $state(false);
  let saveTimer: ReturnType<typeof setTimeout> | null = null;

  async function loadConfig() {
    loading = true;
    try {
      config = await getTimelapseConfig(cameraId);
    } catch (e) {
      console.warn('Failed to load timelapse config:', e);
      config = null;
    } finally {
      loading = false;
    }
  }

  function updateField<K extends keyof TimelapseConfig>(key: K, value: TimelapseConfig[K]) {
    if (!config) return;
    config = { ...config, [key]: value };
    debouncedSave();
  }

  function debouncedSave() {
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => saveConfig(), 800);
  }

  async function saveConfig() {
    if (!config || saving) return;
    saving = true;
    try {
      await updateTimelapseConfig(cameraId, config);
    } catch (e) {
      console.warn('Failed to save timelapse config:', e);
      showToast(t('timelapse.saveFailed'), 'error');
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    if (cameraId) loadConfig();
    return () => { if (saveTimer) clearTimeout(saveTimer); };
  });
</script>

<details class="mt-6 border th-border rounded-lg"
  open={config?.enabled ? true : undefined}
>
  <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
    {t('timelapse.title')}
    {#if config?.enabled}
      <span class="text-xs th-text-muted ml-2">{t('timelapse.enabled')}</span>
    {:else}
      <span class="text-xs th-text-muted ml-2">{t('timelapse.disabled')}</span>
    {/if}
    {#if saving}
      <span class="spinner ml-2"></span>
    {/if}
  </summary>

  <div class="px-4 pb-4 pt-2">
    {#if loading}
      <div class="flex items-center gap-2 py-4 th-text-muted">
        <span class="spinner"></span>
        <span class="text-sm">{t('common.loading')}</span>
      </div>
    {:else if config}
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- Enabled toggle -->
        <div class="md:col-span-2 flex items-center gap-2">
          <input
            id="timelapse-enabled"
            type="checkbox"
            class="accent-[var(--color-accent)]"
            checked={config.enabled}
            onchange={(e) => updateField('enabled', (e.target as HTMLInputElement).checked)}
          />
          <label for="timelapse-enabled" class="th-text-secondary text-sm">{t('timelapse.enabled')}</label>
        </div>

        {#if config.enabled}
          <!-- Interval -->
          <div>
            <label for="timelapse-interval" class="input-label">{t('timelapse.interval')}</label>
            <input
              id="timelapse-interval"
              type="text"
              class="input"
              value={config.interval}
              oninput={(e) => updateField('interval', (e.target as HTMLInputElement).value)}
            />
            <p class="th-text-muted text-xs mt-1">{t('timelapse.intervalHint')}</p>
          </div>

          <!-- Output FPS -->
          <div>
            <label for="timelapse-fps" class="input-label">{t('timelapse.outputFps')}</label>
            <input
              id="timelapse-fps"
              type="number"
              class="input"
              min="1"
              max="60"
              value={config.output_fps}
              oninput={(e) => updateField('output_fps', Number((e.target as HTMLInputElement).value))}
            />
            <p class="th-text-muted text-xs mt-1">{t('timelapse.outputFpsHint')}</p>
          </div>

          <!-- Video Codec -->
          <div>
            <label for="timelapse-codec" class="input-label">{t('timelapse.videoCodec')}</label>
            <select
              id="timelapse-codec"
              class="input"
              value={config.video_codec}
              onchange={(e) => updateField('video_codec', (e.target as HTMLSelectElement).value)}
            >
              <option value="h264">{t('timelapse.codecH264')}</option>
              <option value="h265">{t('timelapse.codecH265')}</option>
            </select>
          </div>

          <!-- Delete Original -->
          <div class="flex items-center gap-2">
            <input
              id="timelapse-delete-original"
              type="checkbox"
              class="accent-[var(--color-accent)]"
              checked={config.delete_original}
              onchange={(e) => updateField('delete_original', (e.target as HTMLInputElement).checked)}
            />
            <label for="timelapse-delete-original" class="th-text-secondary text-sm">{t('timelapse.deleteOriginal')}</label>
          </div>
        {/if}
      </div>
    {/if}
  </div>
</details>
