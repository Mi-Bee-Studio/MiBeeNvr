<script lang="ts">
  // Timelapse preset-range toolbar (Today / 8h / 24h / 7d / 30d): plays an
  // existing periodic-merge output covering the named window, or offers to
  // generate one and tracks progress. Extracted from Recordings.svelte.
  import { Hourglass } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast.svelte';
  import { computePresetWindowStart } from '$lib/recordings-windows';
  import {
    listTimelapseMerges,
    triggerTimelapseMerge,
    subscribeTimelapseMergeProgress,
  } from '$lib/api';
  import type { Camera } from '$lib/api';

  interface Props {
    cameras: Camera[];
    /** The host's selected day — completing a generation refreshes its strips. */
    selectedDate: string | null;
    /** Fired when a preset generation completes (host marks history dirty). */
    ongenerated: () => void;
    // selectedPresetCamera is the camera the preset buttons act on. Defaults
    // to the URL ?camera= param or the first camera in the list. Bindable:
    // the merge-history card keys off the same camera.
    selectedPresetCamera: string;
  }

  let { cameras, selectedDate, ongenerated, selectedPresetCamera = $bindable('') }: Props = $props();

  let presetGenerating = $state(false);
  let presetMergeProgress = $state(0); // 0-100, polled during generation
  let presetProgressAbort: AbortController | null = null;

  // Each preset maps to a (duration_label, window_start_fn, window_end_fn) tuple.
  // window_start/end are computed relative to NOW at click time, in the user's
  // local timezone — the backend aligns them to the named window via ParseMergeDuration.
  const timelapsePresets: Array<{ key: string; labelKey: string; duration: string }> = [
    { key: 'today', labelKey: 'timelapseMerge.presetToday', duration: 'natural-day' },
    { key: '8h', labelKey: 'timelapseMerge.preset8h', duration: '8h' },
    { key: '24h', labelKey: 'timelapseMerge.preset24h', duration: '24h' },
    { key: '7d', labelKey: 'timelapseMerge.preset7d', duration: '7d' },
    { key: '30d', labelKey: 'timelapseMerge.preset30d', duration: '30d' },
  ];

  // handlePresetClick finds an existing periodic-merge output covering the
  // current time window for the selected camera; if none exists, offers to
  // generate one on demand.
  async function handlePresetClick(preset: { key: string; duration: string }) {
    const cameraId = selectedPresetCamera || cameras[0]?.id;
    if (!cameraId) {
      showToast(t('timelapseMerge.noMergeForRange'), 'error');
      return;
    }
    const now = new Date();
    try {
      // Look for a completed merge for this camera whose window_start is on
      // or after the start of the named window (so e.g. "7d" matches a merge
      // started within the last 7 days). The backend lists DESC by window_start.
      const start = computePresetWindowStart(preset.duration, now);
      const resp = await listTimelapseMerges({
        camera_id: cameraId,
        duration: preset.duration,
        start: start.toISOString(),
        status: 'completed',
        limit: 1,
      });
      if (resp.merges.length > 0) {
        // Play the most recent matching merge.
        window.location.hash = `#/timelapse-merge/${resp.merges[0].id}`;
        return;
      }
      // No existing merge → offer to generate one. Use confirm() for simplicity.
      const ok = window.confirm(
        `${t('timelapseMerge.noMergeForRange')}\n${t('timelapseMerge.generatePrompt')}\n\n` +
        `camera: ${cameras.find(c => c.id === cameraId)?.name ?? cameraId}\n` +
        `duration: ${preset.duration}`
      );
      if (!ok) return;
      await generateAndPlayPreset(cameraId, preset.duration, now);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    }
  }

  // generateAndPlayPreset triggers a merge for the given camera/duration and
  // polls progress until completion, then navigates to the player route.
  async function generateAndPlayPreset(cameraId: string, duration: string, refTime: Date) {
    if (presetGenerating) return;
    presetGenerating = true;
    presetMergeProgress = 0;
    try {
      // triggerTimelapseMerge runs the merge for the window containing refTime.
      // The handler returns 202 immediately; the merge runs in the background.
      const dateStr = refTime.toISOString().slice(0, 10); // YYYY-MM-DD
      await triggerTimelapseMerge(cameraId, dateStr, duration);
      showToast(t('detail.mergeStarted'), 'success');
      // Subscribe to SSE progress for this camera.
      presetProgressAbort?.abort();
      presetProgressAbort = new AbortController();
      await new Promise<void>((resolve, reject) => {
        const signal = presetProgressAbort!.signal;
        const ac = subscribeTimelapseMergeProgress(
          cameraId,
          (data) => {
            if (signal.aborted) return;
            presetMergeProgress = data.progress ?? 0;
            if (data.status === 'completed') {
              ongenerated(); // refresh the history list if still mounted
              // Find the merge row for this window+duration and navigate to it.
              void findAndPlayMerge(cameraId, duration, refTime).then(resolve, reject);
            } else if (data.status === 'failed') {
              reject(new Error(data.error || t('timelapseMerge.statusFailed')));
            }
          },
          (err) => {
            if (!signal.aborted) reject(err);
          },
        );
        // Stash the AbortController returned by subscribe so cleanup works.
        presetProgressAbort = ac;
      });
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    } finally {
      presetGenerating = false;
      presetProgressAbort?.abort();
      presetProgressAbort = null;
    }
  }

  // findAndPlayMerge locates the timelapse_merges row for the just-completed
  // window and navigates to the player. Retries a few times because the DB
  // write happens just before the SSE 'completed' event.
  async function findAndPlayMerge(cameraId: string, duration: string, refTime: Date) {
    const start = computePresetWindowStart(duration, refTime);
    for (let attempt = 0; attempt < 5; attempt++) {
      const resp = await listTimelapseMerges({
        camera_id: cameraId,
        duration,
        start: start.toISOString(),
        status: 'completed',
        limit: 1,
      });
      if (resp.merges.length > 0) {
        window.location.hash = `#/timelapse-merge/${resp.merges[0].id}`;
        return;
      }
      await new Promise(r => setTimeout(r, 500));
    }
    // Fallback: couldn't find the row — send the user to the recordings list.
    showToast(t('timelapseMerge.notReady'), 'warning');
    window.location.hash = '#/recordings';
  }
</script>

<div class="ml-auto flex items-center gap-2 flex-wrap">
  <span class="text-xs th-text-tertiary">{t('timelapseMerge.title')}:</span>
  <!-- Camera picker for presets -->
  <select
    class="input input-xs w-auto"
    bind:value={selectedPresetCamera}
    disabled={presetGenerating}
    aria-label={t('timelapseMerge.camera')}
  >
    {#each cameras as cam}
      <option value={cam.id}>{cam.name || cam.id}</option>
    {/each}
  </select>
  {#each timelapsePresets as preset}
    <button
      class="btn btn-sm {presetGenerating ? 'btn-ghost' : 'btn-secondary'}"
      disabled={presetGenerating || cameras.length === 0}
      onclick={() => handlePresetClick(preset)}
      title={t(preset.labelKey)}
    >
      {t(preset.labelKey)}
    </button>
  {/each}
  {#if presetGenerating}
    <span class="text-xs th-text-secondary flex items-center gap-1">
      <Hourglass size={12} class="animate-pulse" />
      {presetMergeProgress}%
    </span>
  {/if}
</div>
