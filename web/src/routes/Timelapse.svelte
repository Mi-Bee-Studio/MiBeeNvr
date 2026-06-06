<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listRecordings,
    listCameras,
    apiRequestBlob,
    getAuthHeader,
    API_BASE
  } from '$lib/api';
  import type { Recording, Camera } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import {
    ChevronLeft,
    ChevronRight,
    Calendar,
    Video,
    Clock,
    HardDrive,
    Camera as CameraIcon,
    AlertCircle,
    Loader2
  } from 'lucide-svelte';

  // --- State ---
  let cameras = $state<Camera[]>([]);
  let selectedCamera = $state('');
  let currentMonth = $state(new Date());
  let selectedDate = $state<string | null>(null);
  let recordings = $state<Recording[]>([]);
  let loading = $state(false);
  let error = $state('');
  let timeRange = $state<'week' | 'month' | '3months'>('month');
  let thumbnailCache = $state<Map<string, string>>(new Map());
  let abortController: AbortController | null = null;

  // --- Helpers ---
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

  function getCameraName(cameraId: string): string {
    const cam = cameras.find(c => c.id === cameraId);
    return cam ? cam.name : cameraId;
  }

  // --- Calendar helpers ---
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

  let selectedRecordings = $derived.by(() => {
    if (!selectedDate) return [];
    return recordings.filter(r => r.started_at.slice(0, 10) === selectedDate);
  });

  // --- Timeline data ---
  let timelineData = $derived.by(() => {
    const now = new Date();
    const today = formatDateStr(now);
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

  // --- Data loading ---
  async function loadData() {
    // Determine date range: union of calendar month + timeline range
    const calStart = getMonthStart(currentMonth.getFullYear(), currentMonth.getMonth());
    const calEnd = getMonthEnd(currentMonth.getFullYear(), currentMonth.getMonth());

    const now = new Date();
    let tlStart: Date;
    switch (timeRange) {
      case 'week':
        tlStart = new Date(now);
        tlStart.setDate(tlStart.getDate() - 6);
        break;
      case 'month':
        tlStart = calStart;
        break;
      case '3months':
        tlStart = new Date(now);
        tlStart.setMonth(tlStart.getMonth() - 3);
        break;
    }
    const tlEnd = timeRange === 'month' ? calEnd : now;

    const rangeStart = tlStart < calStart ? tlStart : calStart;
    const rangeEnd = tlEnd > calEnd ? tlEnd : calEnd;

    // Abort previous
    if (abortController) {
      abortController.abort();
    }
    abortController = new AbortController();

    loading = true;
    error = '';

    try {
      const resp = await listRecordings({
        format: 'timelapse',
        camera_id: selectedCamera || undefined,
        start: rangeStart.toISOString(),
        end: rangeEnd.toISOString(),
        limit: 1000,
        signal: abortController.signal
      });
      recordings = resp.recordings;
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      error = e instanceof Error ? e.message : t('timelapse.gallery.error');
    } finally {
      loading = false;
    }
  }

  async function loadCameras() {
    try {
      cameras = await listCameras();
    } catch (e) {
      console.error('Failed to load cameras:', e);
    }
  }

  // --- Actions ---
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
    // Scroll to gallery
    setTimeout(() => {
      document.getElementById('timelapse-gallery')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }, 100);
  }

  function viewRecording(recording: Recording) {
    window.location.hash = `#/recordings/${recording.id}`;
  }

  function changeTimeRange(range: 'week' | 'month' | '3months') {
    timeRange = range;
  }

  // --- Thumbnail lazy loading ---
  function loadThumbnail(recordingId: string): Promise<string | null> {
    if (thumbnailCache.has(recordingId)) {
      return Promise.resolve(thumbnailCache.get(recordingId)!);
    }
    return apiRequestBlob(`/timelapse/${recordingId}/thumbnail`)
      .then(blob => {
        const url = URL.createObjectURL(blob);
        thumbnailCache.set(recordingId, url);
        thumbnailCache = new Map(thumbnailCache);
        return url;
      })
      .catch(() => null);
  }

  function lazyThumbnail(node: HTMLImageElement, recordingId: string) {
    let destroyed = false;
    let loadedUrl: string | null = null;

    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0].isIntersecting && !destroyed) {
          loadThumbnail(recordingId).then(url => {
            if (!destroyed && url) {
              node.src = url;
              loadedUrl = url;
            }
          });
          observer.disconnect();
        }
      },
      { rootMargin: '200px' }
    );

    observer.observe(node);

    return {
      destroy() {
        destroyed = true;
        observer.disconnect();
        if (loadedUrl) {
          URL.revokeObjectURL(loadedUrl);
        }
      }
    };
  }

  // --- Lifecycle ---
  $effect(() => {
    // Reload data when camera, month, or time range changes
    const _ = [selectedCamera, currentMonth.getTime(), timeRange];
    loadData();
  });

  onMount(() => {
    loadCameras();
    return () => {
      if (abortController) abortController.abort();
      thumbnailCache.forEach(url => URL.revokeObjectURL(url));
      thumbnailCache = new Map();
    };
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Page Header -->
    <div class="mb-6 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
      <div class="flex items-center gap-3">
        <Calendar size={24} class="th-text-primary" />
        <h1 class="text-2xl font-bold th-text-primary">{t('timelapse.gallery.title')}</h1>
      </div>
      <div class="flex items-center gap-3">
        <label for="camera-filter" class="text-sm th-text-secondary sr-only">{t('timelapse.gallery.filterByCamera')}</label>
        <select
          id="camera-filter"
          class="input min-w-[160px]"
          bind:value={selectedCamera}
        >
          <option value="">{t('timelapse.gallery.allCameras')}</option>
          {#each cameras as camera}
            <option value={camera.id}>{camera.name}</option>
          {/each}
        </select>
      </div>
    </div>

    <!-- Loading State -->
    {#if loading && recordings.length === 0}
      <div class="flex flex-col items-center justify-center py-20">
        <Loader2 size={40} class="animate-spin th-text-muted mb-4" />
        <p class="th-text-muted">{t('timelapse.gallery.loading')}</p>
      </div>

    <!-- Error State -->
    {:else if error}
      <div class="card border th-border-danger p-8 text-center">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadData} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>

    <!-- Content -->
    {:else}
      <!-- Calendar Section -->
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

      <!-- Gallery Section -->
      <div id="timelapse-gallery" class="mb-6">
        {#if selectedDate}
          <div class="flex items-center justify-between mb-4">
            <h2 class="text-lg font-semibold th-text-primary">
              {new Date(selectedDate + 'T12:00:00').toLocaleDateString(
                document.documentElement.lang === 'zh' ? 'zh-CN' : 'en-US',
                { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' }
              )}
            </h2>
            <span class="text-sm th-text-muted">
              {selectedRecordings.length} {t('timelapse.gallery.recordings')}
            </span>
          </div>

          {#if selectedRecordings.length === 0}
            <div class="card p-12 text-center border th-border">
              <div class="flex justify-center mb-4 th-text-muted">
                <Video size={48} />
              </div>
              <h3 class="text-lg font-medium th-text-primary mb-2">{t('timelapse.gallery.noTimelapses')}</h3>
              <p class="text-sm th-text-muted">{t('timelapse.gallery.noTimelapsesHint')}</p>
            </div>
          {:else}
            <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-4">
              {#each selectedRecordings as recording}
                <button
                  onclick={() => viewRecording(recording)}
                  class="card border th-border overflow-hidden text-left transition-all duration-200 hover:shadow-md hover:-translate-y-0.5 group cursor-pointer"
                >
                  <!-- Thumbnail -->
                  <div class="aspect-video th-bg-tertiary overflow-hidden relative">
                    {#if thumbnailCache.has(recording.id)}
                      <img
                        src={thumbnailCache.get(recording.id)}
                        alt={recording.started_at}
                        class="w-full h-full object-cover"
                      />
                    {:else}
                      <img
                        use:lazyThumbnail={recording.id}
                        alt={recording.started_at}
                        class="w-full h-full object-cover"
                      />
                      <div class="absolute inset-0 flex items-center justify-center">
                        <CameraIcon size={24} class="th-text-muted opacity-50" />
                      </div>
                    {/if}
                  </div>
                  <!-- Info -->
                  <div class="p-3 space-y-1.5">
                    <p class="text-sm font-medium th-text-primary truncate">
                      {getCameraName(recording.camera_id)}
                    </p>
                    <p class="text-xs th-text-tertiary">
                      {formatDate(recording.started_at)}
                    </p>
                    <div class="flex items-center gap-3 text-xs th-text-muted">
                      <span class="inline-flex items-center gap-1">
                        <Clock size={12} />
                        {formatDuration(recording.duration)}
                      </span>
                      <span class="inline-flex items-center gap-1">
                        <HardDrive size={12} />
                        {formatFileSize(recording.file_size)}
                      </span>
                    </div>
                  </div>
                </button>
              {/each}
            </div>
          {/if}
        {:else}
          <!-- No day selected -->
          <div class="card p-12 text-center border th-border">
            <div class="flex justify-center mb-4 th-text-muted">
              <Calendar size={48} />
            </div>
            <h3 class="text-lg font-medium th-text-primary mb-2">{t('timelapse.gallery.selectDay')}</h3>
            <p class="text-sm th-text-muted">{t('timelapse.gallery.selectDayHint')}</p>
          </div>
        {/if}
      </div>

      <!-- Timeline Section -->
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
                onclick={() => {
                  // Navigate calendar to the clicked day's month
                  const d = new Date(day.date + 'T12:00:00');
                  const calMonth = new Date(d.getFullYear(), d.getMonth());
                  currentMonth = calMonth;
                  selectDay(day.date);
                }}
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
    {/if}
  </main>
</div>

<style>
  /* Prevent grid gap from creating visible borders on calendar */
  .th-bg-border\/50 {
    background-color: color-mix(in srgb, var(--border) 50%, transparent);
  }
</style>
