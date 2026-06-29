<script lang="ts">
  import { ChevronLeft, ChevronRight, Video } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import type { Recording, Camera } from '$lib/api';
  import RecordingCard from '../library/RecordingCard.svelte';

  let {
    selectedDate = $bindable(null),
    recordings = [],
    cameras = [],
    onselectRecording = (r: Recording) => {},
    selectedIds = $bindable([]) as string[],
    ontoggleselect = (r: Recording) => {},
    selectMode = false,
    ondeleteRecording = (r: Recording) => {},
    onplay = undefined,
  }: {
    selectedDate?: string | null;
    recordings: Recording[];
    cameras: Camera[];
    onselectRecording?: (recording: Recording) => void;
    selectedIds?: string[];
    ontoggleselect?: (recording: Recording) => void;
    selectMode?: boolean;
    ondeleteRecording?: (recording: Recording) => void;
    onplay?: (recordingId: string) => void;
  } = $props();

  function pad(n: number): string {
    return String(n).padStart(2, '0');
  }

  function localDateFromISO(iso: string): string {
    const d = new Date(iso);
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  }

  let filteredRecordings = $derived.by(() => {
    if (!selectedDate) return [];
    return recordings.filter((r) => localDateFromISO(r.started_at) === selectedDate);
  });

  function prevDay() {
    if (!selectedDate) return;
    const d = new Date(selectedDate + 'T12:00:00');
    d.setDate(d.getDate() - 1);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    selectedDate = `${y}-${m}-${day}`;
  }

  function nextDay() {
    if (!selectedDate) return;
    const d = new Date(selectedDate + 'T12:00:00');
    d.setDate(d.getDate() + 1);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    selectedDate = `${y}-${m}-${day}`;
  }

  function handleView(recording: Recording) {
    if (selectMode) {
      toggleSelection(recording);
    } else {
      onselectRecording(recording);
    }
  }

  function toggleSelection(recording: Recording) {
    if (selectedIds.includes(recording.id)) {
      selectedIds = selectedIds.filter((id) => id !== recording.id);
    } else {
      selectedIds = [...selectedIds, recording.id];
    }
    ontoggleselect(recording);
  }
</script>

<div id="recording-gallery" class="mb-6">
  {#if selectedDate}
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-2">
        <button
          onclick={prevDay}
          class="btn btn-ghost btn-sm px-2"
          aria-label="Previous day"
        >
          <ChevronLeft size={16} />
        </button>
        <h2 class="text-lg font-semibold th-text-primary">
          {new Date(selectedDate + 'T12:00:00').toLocaleDateString(
            document.documentElement.lang === 'zh' ? 'zh-CN' : 'en-US',
            { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' }
          )}
        </h2>
        <button
          onclick={nextDay}
          class="btn btn-ghost btn-sm px-2"
          aria-label="Next day"
        >
          <ChevronRight size={16} />
        </button>
      </div>
      <span class="text-sm th-text-muted">
        {filteredRecordings.length} {t('timelapse.gallery.recordings')}
      </span>
    </div>

    {#if filteredRecordings.length === 0}
      <div class="card p-12 text-center border th-border">
        <div class="flex justify-center mb-4 th-text-muted">
          <Video size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">
          {t('recordings.noRecordings')}
        </h3>
        <p class="text-sm th-text-muted">
          {t('recordings.noRecordingsHint')}
        </p>
      </div>
    {:else}
      <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-4">
        {#each filteredRecordings as recording}
          <RecordingCard
            {recording}
            {cameras}
            selected={selectedIds.includes(recording.id)}
            onview={handleView}
            onselect={toggleSelection}
            ondelete={ondeleteRecording}
            {onplay}

          />
        {/each}
      </div>
    {/if}
  {:else}
    <div class="card p-12 text-center border th-border">
      <div class="flex justify-center mb-4 th-text-muted">
        <Video size={48} />
      </div>
      <h3 class="text-lg font-medium th-text-primary mb-2">
        {t('timelapse.gallery.selectDay')}
      </h3>
      <p class="text-sm th-text-muted">
        {t('timelapse.gallery.selectDayHint')}
      </p>
    </div>
  {/if}
</div>
