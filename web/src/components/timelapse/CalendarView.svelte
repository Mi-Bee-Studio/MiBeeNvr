<script lang="ts">
  import { ChevronLeft, ChevronRight } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import type { Recording } from '$lib/api';

  let {
    currentMonth = $bindable(),
    selectedDate = $bindable(),
    recordings = [],
  }: {
    currentMonth: Date;
    selectedDate: string | null;
    recordings: Recording[];
  } = $props();

  function pad(n: number): string {
    return String(n).padStart(2, '0');
  }

  function formatDateStr(d: Date): string {
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  }

  let calendarDays = $derived.by(() => {
    const year = currentMonth.getFullYear();
    const month = currentMonth.getMonth();
    const firstDay = new Date(year, month, 1).getDay();
    const daysInMonth = new Date(year, month + 1, 0).getDate();
    const daysInPrevMonth = new Date(year, month, 0).getDate();
    const today = formatDateStr(new Date());

    const days: Array<{
      day: number;
      date: string;
      isCurrentMonth: boolean;
      isToday: boolean;
      dateObj: Date;
    }> = [];

    // Previous month trailing days
    for (let i = firstDay - 1; i >= 0; i--) {
      const d = daysInPrevMonth - i;
      const date = new Date(year, month - 1, d);
      const ds = formatDateStr(date);
      days.push({ day: d, date: ds, isCurrentMonth: false, isToday: ds === today, dateObj: date });
    }

    // Current month days
    for (let i = 1; i <= daysInMonth; i++) {
      const date = new Date(year, month, i);
      const ds = formatDateStr(date);
      days.push({ day: i, date: ds, isCurrentMonth: true, isToday: ds === today, dateObj: date });
    }

    // Next month leading days to fill last row
    const remaining = (7 - (days.length % 7)) % 7;
    for (let i = 1; i <= remaining; i++) {
      const date = new Date(year, month + 1, i);
      const ds = formatDateStr(date);
      days.push({ day: i, date: ds, isCurrentMonth: false, isToday: ds === today, dateObj: date });
    }

    return days;
  });

  let dailyCounts = $derived.by(() => {
    const counts = new Map<string, number>();
    for (const rec of recordings) {
      const date = rec.started_at.slice(0, 10);
      counts.set(date, (counts.get(date) || 0) + 1);
    }
    return counts;
  });

  let monthLabel = $derived.by(() => {
    const year = currentMonth.getFullYear();
    const month = currentMonth.getMonth();
    const lang = document.documentElement.lang === 'zh' ? 'zh-CN' : 'en-US';
    return new Date(year, month).toLocaleDateString(lang, { year: 'numeric', month: 'long' });
  });

  function prevMonth() {
    const d = new Date(currentMonth);
    d.setMonth(d.getMonth() - 1);
    currentMonth = d;
  }

  function nextMonth() {
    const d = new Date(currentMonth);
    d.setMonth(d.getMonth() + 1);
    currentMonth = d;
  }

  function selectDay(date: string) {
    selectedDate = date;
  }
</script>

<div class="card p-5 mb-6 border th-border">
  <!-- Calendar Nav -->
  <div class="flex items-center justify-between mb-4">
    <button
      onclick={prevMonth}
      class="btn btn-ghost btn-sm flex items-center gap-1"
      aria-label={t('timelapse.gallery.prevMonth')}
    >
      <ChevronLeft size={18} />
      <span class="hidden sm:inline">{t('timelapse.gallery.prevMonth')}</span>
    </button>
    <h2 class="text-lg font-semibold th-text-primary">{monthLabel}</h2>
    <button
      onclick={nextMonth}
      class="btn btn-ghost btn-sm flex items-center gap-1"
      aria-label={t('timelapse.gallery.nextMonth')}
    >
      <span class="hidden sm:inline">{t('timelapse.gallery.nextMonth')}</span>
      <ChevronRight size={18} />
    </button>
  </div>

  <!-- Weekday Headers -->
  <div class="grid grid-cols-7 mb-2">
    {#each ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'] as day}
      <div class="text-center text-xs font-medium th-text-tertiary py-1">{day}</div>
    {/each}
  </div>

  <!-- Calendar Grid -->
  <div class="grid grid-cols-7 gap-px th-bg-border/50 rounded overflow-hidden">
    {#each calendarDays as day}
      <button
        class="relative flex flex-col items-center justify-center py-2 px-1 text-sm transition-colors
          {day.isCurrentMonth
            ? 'th-bg-primary hover:th-bg-hover cursor-pointer'
            : 'th-bg-secondary/50 th-text-tertiary cursor-pointer'}
          {selectedDate === day.date ? 'ring-2 ring-[var(--color-primary)] th-bg-primary/80' : ''}
          {day.isToday && selectedDate !== day.date ? 'ring-1 ring-[var(--color-info)]' : ''}"
        onclick={() => selectDay(day.date)}
      >
        <span class="text-sm {day.isToday ? 'font-bold th-text-accent' : ''}">{day.day}</span>
        {#if dailyCounts.has(day.date)}
          <span class="mt-0.5 text-[10px] leading-none px-1.5 py-0.5 rounded-full badge badge-info min-w-[18px] text-center">
            {dailyCounts.get(day.date)}
          </span>
        {/if}
      </button>
    {/each}
  </div>
</div>

<style>
  /* Prevent grid gap from creating visible borders on calendar */
  .th-bg-border\/50 {
    background-color: color-mix(in srgb, var(--border) 50%, transparent);
  }
</style>
