<script lang="ts">
  // Completed-merge history (collapsible): every generated merge for the
  // selected camera is reachable here, not just the latest one per preset —
  // the archive behind the timeline's top strips. Extracted from
  // Recordings.svelte.
  import { Hourglass } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { formatFileSize, formatMergeWindowLabel, mergeDurationI18nKeys } from '$lib/format';
  import { listTimelapseMerges } from '$lib/api';
  import type { TimelapseMerge } from '$lib/api';

  interface Props {
    cameraId: string;
    cameraName: string;
    /** A merge completed since the last load (host flips it via bind). */
    dirty: boolean;
  }

  let { cameraId, cameraName, dirty = $bindable(false) }: Props = $props();

  let mergeHistory = $state<TimelapseMerge[]>([]);
  let mergeHistoryLoading = $state(false);
  let mergeHistoryLoadedFor = $state(''); // camera the loaded list belongs to
  let historyOpen = $state(
    (() => {
      try { return localStorage.getItem('mibee_nvr_tl_history_open') === '1'; } catch { return false; }
    })(),
  );

  function toggleHistoryOpen() {
    historyOpen = !historyOpen;
    try { localStorage.setItem('mibee_nvr_tl_history_open', historyOpen ? '1' : '0'); } catch {}
  }

  function mergeDurationLabel(durationLabel: string): string {
    const key = mergeDurationI18nKeys[durationLabel];
    return key ? t(key) : durationLabel;
  }

  async function loadMergeHistory(cameraId: string) {
    mergeHistoryLoading = true;
    try {
      const resp = await listTimelapseMerges({ camera_id: cameraId, status: 'completed', limit: 30 });
      mergeHistory = resp.merges;
      mergeHistoryLoadedFor = cameraId;
      dirty = false;
    } catch {
      // Non-fatal: the list stays empty until the camera/view changes again.
    } finally {
      mergeHistoryLoading = false;
    }
  }

  // Load (or refresh) the history whenever the card is opened, the preset
  // camera changes, or a preset generation just completed.
  $effect(() => {
    if (!historyOpen) return;
    if (!cameraId) return;
    if (!dirty && mergeHistoryLoadedFor === cameraId) return;
    void loadMergeHistory(cameraId);
  });
</script>

<div class="card border th-border mt-3">
  <button
    type="button"
    class="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-black/5 dark:hover:bg-white/5 rounded-t"
    onclick={toggleHistoryOpen}
    aria-expanded={historyOpen}
  >
    <span class="text-xs font-medium th-text-secondary">
      {t('timelapseMerge.historyTitle')} · {cameraName}
    </span>
    <span class="ml-auto flex items-center gap-2">
      {#if mergeHistoryLoading}
        <span class="text-[10px] th-text-tertiary flex items-center gap-1">
          <Hourglass size={10} class="animate-pulse" />
          {t('common.loading')}
        </span>
      {/if}
      <span class="text-[10px] th-text-tertiary">{historyOpen ? '▾' : '▸'}</span>
    </span>
  </button>
  {#if historyOpen}
    <div class="px-3 pb-3">
      {#if !mergeHistoryLoading && mergeHistory.length === 0}
        <p class="text-xs th-text-tertiary">{t('timelapseMerge.historyEmpty')}</p>
      {:else}
        <div class="flex flex-col gap-1 max-h-72 overflow-y-auto">
          {#each mergeHistory as m (m.id)}
            <button
              class="flex items-center justify-between gap-2 w-full px-2 py-1.5 rounded text-left hover:bg-black/5 dark:hover:bg-white/10"
              onclick={() => {
                window.location.hash = `#/timelapse-merge/${m.id}`;
              }}
            >
              <span class="flex items-center gap-2 min-w-0">
                <span class="text-xs font-mono th-text-secondary shrink-0">
                  {formatMergeWindowLabel(m.window_start, m.duration_label)}
                </span>
                <span class="text-[10px] th-text-tertiary shrink-0">{mergeDurationLabel(m.duration_label)}</span>
              </span>
              <span class="text-[10px] th-text-tertiary shrink-0">
                {m.frame_count} {t('timelapseMerge.framesUnit')} · {formatFileSize(m.file_size)}
              </span>
            </button>
          {/each}
        </div>
      {/if}
      <p class="text-[10px] th-text-tertiary mt-2">
        {t('timelapseMerge.generatePrompt')}
      </p>
    </div>
  {/if}
</div>
