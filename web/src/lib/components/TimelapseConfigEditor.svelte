<script lang="ts">
  import { t } from '$lib/i18n';
  import { getTimelapseConfig, updateTimelapseConfig } from '$lib/api';
  import type { TimelapseConfig, ScheduleConfig } from '$lib/api';
  import { showToast } from '$lib/toast';

  interface Props {
    cameraId: string;
  }

  let { cameraId }: Props = $props();

  let config = $state<TimelapseConfig | null>(null);
  let loading = $state(true);
  let saving = $state(false);
  let saveTimer: ReturnType<typeof setTimeout> | null = null;

  const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

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

  function initSchedule() {
    if (!config) return;
    config = {
      ...config,
      schedule: {
        time_ranges: [{ start: '00:00', end: '23:59' }],
        days_of_week: [0, 1, 2, 3, 4, 5, 6],
      },
    };
    debouncedSave();
  }

  function updateSchedule(fn: (s: ScheduleConfig) => ScheduleConfig) {
    if (!config?.schedule) return;
    config = { ...config, schedule: fn({ ...config.schedule, time_ranges: [...config.schedule.time_ranges] }) };
    debouncedSave();
  }

  function toggleDay(day: number) {
    if (!config?.schedule) return;
    const days = config.schedule.days_of_week;
    const newDays = days.includes(day)
      ? days.filter(d => d !== day)
      : [...days, day].sort();
    updateSchedule(s => ({ ...s, days_of_week: newDays }));
  }

  function updateTimeRange(index: number, field: 'start' | 'end', value: string) {
    if (!config?.schedule) return;
    const ranges = config.schedule.time_ranges.map((r, i) => i === index ? { ...r, [field]: value } : r);
    updateSchedule(s => ({ ...s, time_ranges: ranges }));
  }

  function addTimeRange() {
    if (!config?.schedule) return;
    const ranges = [...config.schedule.time_ranges, { start: '00:00', end: '23:59' }];
    updateSchedule(s => ({ ...s, time_ranges: ranges }));
  }

  function removeTimeRange(index: number) {
    if (!config?.schedule || config.schedule.time_ranges.length <= 1) return;
    const ranges = config.schedule.time_ranges.filter((_, i) => i !== index);
    updateSchedule(s => ({ ...s, time_ranges: ranges }));
  }

  async function togglePause() {
    if (!config) return;
    config = { ...config, paused: !config.paused };
    await saveConfig();
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
    {#if config?.paused}
      <span class="text-xs th-text-muted ml-2">({t('timelapse.paused')})</span>
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

          <!-- Frame Source -->
          <div>
            <label for="timelapse-frame-source" class="input-label">{t('timelapse.frameSource')}</label>
            <select
              id="timelapse-frame-source"
              class="input"
              value={config.frame_source}
              onchange={(e) => updateField('frame_source', (e.target as HTMLSelectElement).value)}
            >
              <option value="auto">Auto</option>
              <option value="snapshot">Snapshot</option>
              <option value="rtsp_keyframe">RTSP Keyframe</option>
              <option value="mjpeg">MJPEG</option>
            </select>
          </div>

          <!-- Snapshot URL (shown only when frame_source is 'snapshot') -->
          {#if config.frame_source === 'snapshot'}
            <div class="md:col-span-2">
              <label for="timelapse-snapshot-url" class="input-label">{t('timelapse.snapshotUrl')}</label>
              <input
                id="timelapse-snapshot-url"
                type="text"
                class="input"
                value={config.snapshot_url}
                oninput={(e) => updateField('snapshot_url', (e.target as HTMLInputElement).value)}
              />
            </div>
          {/if}

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

          <!-- Schedule Section -->
          <div class="md:col-span-2 border-t th-border pt-4 mt-2">
            <div class="flex items-center justify-between mb-3">
              <p class="text-sm font-medium th-text-secondary">{t('timelapse.schedule')}</p>
              {#if !config.schedule}
                <button class="text-xs th-text-accent hover:underline" onclick={initSchedule}>
                  {t('common.configure')}
                </button>
              {/if}
            </div>

            {#if config.schedule}
              <!-- Day of week checkboxes (Sun=0 .. Sat=6) -->
              <div class="flex flex-wrap gap-3 mb-3">
                {#each DAYS as day, i}
                  <label class="flex items-center gap-1 text-sm th-text-secondary cursor-pointer">
                    <input
                      type="checkbox"
                      class="accent-[var(--color-accent)]"
                      checked={config.schedule.days_of_week.includes(i)}
                      onchange={() => toggleDay(i)}
                    />
                    {day}
                  </label>
                {/each}
              </div>

              <!-- Time ranges -->
              {#each config.schedule.time_ranges as range, i}
                <div class="flex items-center gap-2 mb-2">
                  <input
                    type="time"
                    class="input text-sm w-32"
                    value={range.start}
                    oninput={(e) => updateTimeRange(i, 'start', (e.target as HTMLInputElement).value)}
                  />
                  <span class="th-text-muted text-xs">to</span>
                  <input
                    type="time"
                    class="input text-sm w-32"
                    value={range.end}
                    oninput={(e) => updateTimeRange(i, 'end', (e.target as HTMLInputElement).value)}
                  />
                  {#if config.schedule.time_ranges.length > 1}
                    <button class="text-xs th-text-danger hover:underline ml-1" onclick={() => removeTimeRange(i)}>
                      {t('common.remove')}
                    </button>
                  {/if}
                </div>
              {/each}
              <button class="text-xs th-text-accent hover:underline mt-1" onclick={addTimeRange}>
                + {t('common.add')}
              </button>
            {/if}
          </div>

          <!-- Pause/Resume -->
          <div class="md:col-span-2 border-t th-border pt-4 mt-2">
            <button
              class="px-3 py-1.5 text-sm rounded th-bg-surface th-border th-border-secondary hover:th-bg-hover transition-colors"
              onclick={togglePause}
            >
              {config.paused ? t('common.resume') || 'Resume' : t('common.pause') || 'Pause'}
            </button>
          </div>

          <!-- Merge Settings -->
          <div class="md:col-span-2 border-t th-border pt-4 mt-2">
            <p class="text-sm font-medium th-text-secondary mb-3">{t('timelapse.mergeSettings')}</p>
          </div>

          <!-- Merge Mode -->
          <div>
            <label for="timelapse-merge-mode" class="input-label">{t('timelapse.mergeMode')}</label>
            <select
              id="timelapse-merge-mode"
              class="input"
              value={config.merge_mode || 'auto'}
              onchange={(e) => updateField('merge_mode', (e.target as HTMLSelectElement).value)}
            >
              <option value="auto">{t('timelapse.mergeModeAuto')}</option>
              <option value="mp4">{t('timelapse.mergeModeMp4')}</option>
              <option value="jpeg">{t('timelapse.mergeModeJpeg')}</option>
            </select>
          </div>

          <!-- Daily Merge -->
          <div class="flex items-center gap-2">
            <input
              id="timelapse-daily-merge"
              type="checkbox"
              class="accent-[var(--color-accent)]"
              checked={config.daily_merge ?? true}
              onchange={(e) => updateField('daily_merge', (e.target as HTMLInputElement).checked)}
            />
            <label for="timelapse-daily-merge" class="th-text-secondary text-sm">{t('timelapse.dailyMerge')}</label>
          </div>

          <!-- Merge Output FPS -->
          <div>
            <label for="timelapse-merge-fps" class="input-label">{t('timelapse.mergeOutputFps')}</label>
            <input
              id="timelapse-merge-fps"
              type="number"
              class="input"
              min="1"
              max="60"
              value={config.merge_output_fps || 30}
              oninput={(e) => updateField('merge_output_fps', Number((e.target as HTMLInputElement).value))}
            />
            <p class="th-text-muted text-xs mt-1">{t('timelapse.mergeOutputFpsHint')}</p>
          </div>

        {/if}
      </div>
    {/if}
  </div>
</details>
