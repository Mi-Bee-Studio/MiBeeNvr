<script lang="ts">
  // Advanced recording options (dark-frame filter + recording schedule) —
  // collapsed by default, auto-opens when either option is already on.
  // Extracted verbatim from CameraForm.svelte; all state stays bindable so
  // the parent's payload builders and init-from-camera logic are unchanged.
  import { t } from '$lib/i18n';

  // Recording schedule: one row per time range; no days selected = every day.
  export type ScheduleRange = { start: string; end: string; days: number[] };

  interface Props {
    darkFrameEnabled: boolean;
    darkFrameThreshold: number;
    scheduleEnabled: boolean;
    scheduleRanges: ScheduleRange[];
  }

  let {
    darkFrameEnabled = $bindable(false),
    darkFrameThreshold = $bindable(15),
    scheduleEnabled = $bindable(false),
    scheduleRanges = $bindable([]),
  }: Props = $props();

  const defaultScheduleRange = (): ScheduleRange => ({ start: '06:00', end: '22:00', days: [] });

  // Monday-first display order; `day` is the days_of_week value (0 = Sunday).
  const SCHEDULE_WEEKDAYS = [
    { day: 1, key: 'calendar.weekdayMon' },
    { day: 2, key: 'calendar.weekdayTue' },
    { day: 3, key: 'calendar.weekdayWed' },
    { day: 4, key: 'calendar.weekdayThu' },
    { day: 5, key: 'calendar.weekdayFri' },
    { day: 6, key: 'calendar.weekdaySat' },
    { day: 0, key: 'calendar.weekdaySun' },
  ];

  function toggleScheduleDay(index: number, day: number) {
    const range = scheduleRanges[index];
    range.days = range.days.includes(day) ? range.days.filter((d) => d !== day) : [...range.days, day];
  }
</script>

<details class="md:col-span-2 border th-border rounded-lg" open={darkFrameEnabled || scheduleEnabled ? true : undefined}>
  <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
    {t('cameras.advancedRecording')}
  </summary>
  <div class="px-4 pb-4 space-y-4">
    <!-- Dark frame filtering (MJPEG/AVI cameras only) -->
    <div class="space-y-2">
      <div class="flex items-center gap-2">
        <input id="cam-dark-frame" type="checkbox" class="checkbox"
          bind:checked={darkFrameEnabled}
        />
        <label for="cam-dark-frame" class="input-label cursor-pointer">
          {t('cameras.darkFrameFilter') || 'Dark frame filter'}
          <span class="text-xs th-text-muted ml-1">({t('cameras.darkFrameFilterHint') || 'skip night/dark segments'})</span>
        </label>
      </div>
      {#if darkFrameEnabled}
        <div class="flex items-center gap-2 pl-6">
          <label class="text-sm th-text-muted whitespace-nowrap">{t('cameras.brightnessThreshold') || 'Brightness threshold'}</label>
          <input type="range" min="5" max="50" bind:value={darkFrameThreshold} class="range range-sm w-32" />
          <span class="text-sm font-mono w-8">{darkFrameThreshold}</span>
        </div>
      {/if}
    </div>

    <!-- Recording schedule -->
    <div class="space-y-2">
      <div class="flex items-center gap-2">
        <input id="cam-rec-schedule" type="checkbox" class="checkbox"
          bind:checked={scheduleEnabled}
        />
        <label for="cam-rec-schedule" class="input-label cursor-pointer">
          {t('cameras.recordingSchedule') || 'Recording schedule'}
          <span class="text-xs th-text-muted ml-1">({t('cameras.recordingScheduleHint') || 'time-based recording'})</span>
        </label>
      </div>
      {#if scheduleEnabled}
        <div class="space-y-2 pl-6">
          {#each scheduleRanges as range, i}
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1" data-testid="schedule-range">
              <div class="flex items-center gap-2">
                <label class="text-sm th-text-muted whitespace-nowrap">{t('cameras.recordFrom') || 'Record from'}</label>
                <input type="time" bind:value={range.start} class="input w-28 py-1" />
                <label class="text-sm th-text-muted whitespace-nowrap">{t('cameras.recordTo') || 'to'}</label>
                <input type="time" bind:value={range.end} class="input w-28 py-1" />
                {#if range.end <= range.start}
                  <span class="text-xs th-text-muted">{t('cameras.recordingScheduleOvernight')}</span>
                {/if}
              </div>
              <div class="flex flex-wrap items-center gap-2">
                {#each SCHEDULE_WEEKDAYS as wd}
                  <label class="flex items-center gap-1 text-sm th-text-secondary cursor-pointer">
                    <input type="checkbox" class="accent-[var(--color-accent)]"
                      checked={range.days.includes(wd.day)}
                      onchange={() => toggleScheduleDay(i, wd.day)}
                    />
                    {t(wd.key)}
                  </label>
                {/each}
              </div>
              {#if scheduleRanges.length > 1}
                <button type="button" class="text-xs th-text-danger hover:underline"
                  onclick={() => scheduleRanges.splice(i, 1)}>
                  {t('common.remove')}
                </button>
              {/if}
            </div>
          {/each}
          <button type="button" class="text-xs th-text-accent hover:underline"
            onclick={() => scheduleRanges.push(defaultScheduleRange())}>
            + {t('cameras.recordingScheduleAddRange')}
          </button>
          <p class="text-xs th-text-muted">{t('cameras.recordingScheduleDaysHint')}</p>
        </div>
      {/if}
    </div>
  </div>
</details>
