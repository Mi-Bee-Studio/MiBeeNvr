<script lang="ts">
  // Cloud-archive section (remote offload, issue #874 batch 2): evicted-
  // locally / remote-only items for the selected day, played through the NVR
  // range proxy. Extracted from Recordings.svelte — state + fetch + list live
  // here; the host just feeds the selected day.
  import { Cloud, Download } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { formatDate, formatFileSize } from '$lib/format';
  import { remoteDayBounds } from '$lib/recordings-windows';
  import { listOffloadRecordings, offloadObjectURL } from '$lib/api/offload';
  import type { OffloadRemoteItem } from '$lib/api/offload';

  interface Props {
    selectedDate: string | null;
  }

  let { selectedDate }: Props = $props();

  let showRemote = $state(false);
  let remoteItems = $state<OffloadRemoteItem[]>([]);
  let remoteLoading = $state(false);
  let remoteError = $state('');
  let remoteAbort: AbortController | undefined;
  let playingRemoteId = $state<number | null>(null);

  async function loadRemoteArchive(date: string) {
    if (remoteAbort) remoteAbort.abort();
    remoteAbort = new AbortController();
    remoteLoading = true;
    remoteError = '';
    try {
      const { start, end } = remoteDayBounds(date);
      remoteItems = await listOffloadRecordings({ start, end, limit: 200 }, remoteAbort.signal);
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      remoteError = t('recordings.remoteLoadFailed');
      remoteItems = [];
    } finally {
      remoteLoading = false;
    }
  }

  function toggleRemoteSection() {
    showRemote = !showRemote;
    if (showRemote && selectedDate) void loadRemoteArchive(selectedDate);
  }

  // Every day switch while the section is visible refreshes the list.
  $effect(() => {
    if (showRemote && selectedDate) void loadRemoteArchive(selectedDate);
  });
</script>

<button
  class="btn btn-sm {showRemote ? 'btn-primary' : 'btn-ghost'} ml-auto"
  onclick={toggleRemoteSection}
  aria-expanded={showRemote}
>
  <Cloud size={16} class="mr-1" />
  {t('recordings.remoteSection')}
</button>

{#if showRemote}
  <div class="card border th-border p-4 mb-4">
    <div class="flex items-center gap-2 mb-2">
      <Cloud size={16} class="th-text-secondary" />
      <span class="text-sm font-medium th-text-primary">{t('recordings.remoteSection')}</span>
      <span class="text-xs th-text-tertiary">{t('recordings.remoteSectionHint')}</span>
    </div>
    {#if remoteLoading}
      <p class="text-sm th-text-secondary py-4 text-center">{t('common.loading')}</p>
    {:else if remoteError}
      <p class="text-sm th-color-danger py-2">{remoteError}</p>
    {:else if remoteItems.length === 0}
      <p class="text-sm th-text-muted py-4 text-center">{t('recordings.remoteEmpty')}</p>
    {:else}
      <div class="divide-y th-border">
        {#each remoteItems as item (item.id)}
          {@const playable = item.format === 'h264' || item.format === 'h265'}
          <div class="py-2">
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span class="text-sm font-medium th-text-primary font-mono">{item.camera_id}</span>
              <span class="text-xs th-text-secondary">{formatDate(item.started_at)}</span>
              <span class="text-xs th-text-muted">
                {Math.round(item.duration / 60)} min · {formatFileSize(item.uploaded_size)} · {item.format}
              </span>
              <div class="ml-auto flex items-center gap-2">
                {#if playable}
                  <button
                    class="btn btn-ghost btn-sm"
                    onclick={() => (playingRemoteId = playingRemoteId === item.id ? null : item.id)}
                  >
                    {t('recordings.remotePlay')}
                  </button>
                {:else}
                  <span class="text-xs th-text-muted">{t('recordings.remoteMjpegHint')}</span>
                {/if}
                <a
                  class="btn btn-ghost btn-sm"
                  href={offloadObjectURL(item)}
                  download="{item.camera_id}-{item.recording_id}.{item.format === 'mjpeg' || item.format === 'mjpa' ? 'mp4' : 'mp4'}"
                >
                  <Download size={14} class="mr-1" />
                  {t('recordings.remoteDownload')}
                </a>
              </div>
            </div>
            {#if playingRemoteId === item.id}
              <!-- Remote playback rides the range proxy
                   (/api/offload/objects/{id}, authenticated via the
                   ?token= query appended by offloadObjectURL); H.264
                   plays natively, H.265 depends on the browser's HEVC
                   support. -->
              <video
                class="w-full max-h-[480px] mt-2 rounded-lg bg-black"
                controls
                preload="metadata"
                src={offloadObjectURL(item)}
              ></video>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </div>
{/if}
