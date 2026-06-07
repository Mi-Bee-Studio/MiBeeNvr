<script lang="ts">
  import { t } from '$lib/i18n';
  import type { Recording } from '$lib/api';

  let {
    recordings = [],
    currentMonth = new Date(),
    timeRange = $bindable(),
    onselectDay = (date: string) => {},
  }: {
    recordings: Recording[];
    currentMonth: Date;
    timeRange: 'week' | 'month' | '3months';
    onselectDay?: (date: string) => void;
  } = $props();

  function pad(n: number): string {
    return String(n).padStart(2, '0');
  }

  function formatDateStr(d: Date): string {
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  }

  function getMonthStart(year: number, month: number): Date {
    return new Date(year, month, 1);
  }

  function getMonthEnd(year: number, month: number): Date {
    return new Date(year, month + 1, 0, 23, 59, 59, 999);
  }

  let timelineData = $derived.by(() => {
    const now = new Date();
    let startDate: Date;
    let endDate: Date;

    switch (timeRange) {
      case 'week':
        startDate = new Date(now);
        startDate.setDate(startDate.getDate() - 6);
        endDate = now;
        break;
      case 'month':
        startDate = getMonthStart(currentMonth.getFullYear(), currentMonth.getMonth());
        endDate = getMonthEnd(currentMonth.getFullYear(), currentMonth.getMonth());
        break;
      case '3months':
        startDate = new Date(now);
        startDate.setMonth(startDate.getMonth() - 3);
        endDate = now;
        break;
    }

    // Build daily counts map from recordings
    const counts = new Map<string, number>();
    let maxCount = 0;
    for (const rec of recordings) {
      const date = rec.started_at.slice(0, 10);
      if (date >= formatDateStr(startDate) && date <= formatDateStr(endDate)) {
        const c = (counts.get(date) || 0) + 1;
        counts.set(date, c);
        if (c > maxCount) maxCount = c;
      }
    }

    // Build timeline entries
    const days: Array<{ date: string; count: number; height: number; label: string }> = [];
    const cursor = new Date(startDate);
    while (cursor <= endDate) {
      const ds = formatDateStr(cursor);
      const count = counts.get(ds) || 0;
      days.push({
        date: ds,
        count,
        height: maxCount > 0 ? (count / maxCount) * 100 : 0,
        label: `${cursor.getMonth() + 1}/${cursor.getDate()}`
      });
      cursor.setDate(cursor.getDate() + 1);
    }

    return days;
  });

  function changeTimeRange(range: 'week' | 'month' | '3months') {
    timeRange = range;
  }
</script>

<div class="card p-5 border th-border">
  <div class="flex items-center justify-between mb-4">
    <h3 class="text-sm font-semibold th-text-primary">{t('timelapse.gallery.timeline')}</h3>
    <div class="flex gap-1">
      <button
        class="px-2.5 py-1 text-xs font-medium rounded transition-colors
          {timeRange === 'week'
            ? 'bg-[var(--color-primary)] text-white'
            : 'th-bg-tertiary th-text-secondary hover:th-bg-hover'}"
        onclick={() => changeTimeRange('week')}
      >
        {t('timelapse.gallery.week')}
      </button>
      <button
        class="px-2.5 py-1 text-xs font-medium rounded transition-colors
          {timeRange === 'month'
            ? 'bg-[var(--color-primary)] text-white'
            : 'th-bg-tertiary th-text-secondary hover:th-bg-hover'}"
        onclick={() => changeTimeRange('month')}
      >
        {t('timelapse.gallery.month')}
      </button>
      <button
        class="px-2.5 py-1 text-xs font-medium rounded transition-colors
          {timeRange === '3months'
            ? 'bg-[var(--color-primary)] text-white'
            : 'th-bg-tertiary th-text-secondary hover:th-bg-hover'}"
        onclick={() => changeTimeRange('3months')}
      >
        {t('timelapse.gallery.threeMonths')}
      </button>
    </div>
  </div>

  {#if timelineData.length > 0}
    <div class="relative h-24 flex items-end gap-[1px] overflow-x-auto pb-1">
      {#each timelineData as day}
        <button
          class="flex-1 min-w-[6px] flex flex-col items-center justify-end group cursor-pointer relative"
          onclick={() => onselectDay(day.date)}
          title="{day.date}: {day.count} {t('timelapse.gallery.recordings')}"
        >
          <div
            class="w-full rounded-t transition-all duration-150
              {day.count > 0
                ? 'bg-[var(--color-primary)] hover:bg-[var(--color-accent)]'
                : 'th-bg-tertiary'}"
            style="height: {Math.max(day.height, day.count > 0 ? 4 : 1)}%"
          ></div>
          <!-- Tooltip on hover -->
          <div class="absolute bottom-full mb-1 hidden group-hover:block z-10">
            <span class="text-[10px] px-1.5 py-0.5 rounded th-bg-elevated th-text-secondary whitespace-nowrap shadow">
              {day.label}: {day.count}
            </span>
          </div>
        </button>
      {/each}
    </div>
    <!-- X-axis labels (show every Nth label to avoid crowding) -->
    <div class="flex mt-1 text-[10px] th-text-tertiary">
      {#each timelineData as day, i}
        {@const step = Math.max(1, Math.floor(timelineData.length / 12))}
        {#if i % step === 0 || i === timelineData.length - 1}
          <span class="flex-1 text-center truncate">{day.label}</span>
        {/if}
      {/each}
    </div>
  {:else}
    <div class="h-24 flex items-center justify-center th-text-muted text-sm">
      {t('timelapse.gallery.noData')}
    </div>
  {/if}
</div>
