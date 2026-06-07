<script lang="ts">
  import { Video, Camera as CameraIcon, Clock, HardDrive } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { apiRequestBlob } from '$lib/api';
  import type { Recording, Camera } from '$lib/api';

  let {
    selectedDate = null,
    recordings = [],
    cameras = [],
    onselectRecording = (recording: Recording) => {},
  }: {
    selectedDate: string | null;
    recordings: Recording[];
    cameras: Camera[];
    onselectRecording?: (recording: Recording) => void;
  } = $props();

  let selectedRecordings = $derived.by(() => {
    if (!selectedDate) return [];
    return recordings.filter(r => r.started_at.slice(0, 10) === selectedDate);
  });

  function getCameraName(cameraId: string): string {
    const cam = cameras.find(c => c.id === cameraId);
    return cam ? cam.name : cameraId;
  }

  // --- Thumbnail lazy loading ---
  let thumbnailCache = $state<Map<string, string>>(new Map());

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

  // Cleanup on destroy
  $effect(() => {
    return () => {
      thumbnailCache.forEach(url => URL.revokeObjectURL(url));
      thumbnailCache = new Map();
    };
  });
</script>

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
            onclick={() => onselectRecording(recording)}
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
        <Video size={48} />
      </div>
      <h3 class="text-lg font-medium th-text-primary mb-2">{t('timelapse.gallery.selectDay')}</h3>
      <p class="text-sm th-text-muted">{t('timelapse.gallery.selectDayHint')}</p>
    </div>
  {/if}
</div>
