<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listTimelapseRecordings,
    listCameras,
  } from '$lib/api';
  import type { Recording, Camera } from '$lib/api';
  import { t } from '$lib/i18n';
  import {
    Calendar,
    AlertCircle,
    Loader2
  } from 'lucide-svelte';

  import CalendarView from '../components/timelapse/CalendarView.svelte';
  import GalleryGrid from '../components/timelapse/GalleryGrid.svelte';
  import TimelineBar from '../components/timelapse/TimelineBar.svelte';

  // --- State ---
  let cameras = $state<Camera[]>([]);
  let selectedCamera = $state('');
  let currentMonth = $state(new Date());
  let selectedDate = $state<string | null>(null);
  let recordings = $state<Recording[]>([]);
  let loading = $state(false);
  let error = $state('');
  let timeRange = $state<'week' | 'month' | '3months'>('month');
  let abortController: AbortController | null = null;

  // --- Calendar helpers (needed by loadData) ---
  function getMonthStart(year: number, month: number): Date {
    return new Date(year, month, 1);
  }

  function getMonthEnd(year: number, month: number): Date {
    return new Date(year, month + 1, 0, 23, 59, 59, 999);
  }

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
      const resp = await listTimelapseRecordings({
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
  function viewRecording(recording: Recording) {
    window.location.hash = `#/recordings/${recording.id}`;
  }

  function onTimelineSelectDay(date: string) {
    // Navigate calendar to the clicked day's month
    const d = new Date(date + 'T12:00:00');
    const calMonth = new Date(d.getFullYear(), d.getMonth());
    currentMonth = calMonth;
    selectedDate = date;
  }

  // --- Lifecycle ---
  // Scroll to gallery when date is selected (from calendar or timeline)
  $effect(() => {
    if (selectedDate) {
      setTimeout(() => {
        document.getElementById('timelapse-gallery')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }, 100);
    }
  });

  // Reload data when camera, month, or time range changes
  $effect(() => {
    const _ = [selectedCamera, currentMonth.getTime(), timeRange];
    loadData();
  });

  onMount(() => {
    loadCameras();
    return () => {
      if (abortController) abortController.abort();
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
      <CalendarView bind:currentMonth bind:selectedDate {recordings} />
      <GalleryGrid {selectedDate} {recordings} {cameras} onselectRecording={viewRecording} />
      <TimelineBar {recordings} {currentMonth} bind:timeRange onselectDay={onTimelineSelectDay} />
    {/if}
  </main>
</div>
