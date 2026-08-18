<script lang="ts">
  // GB28181 device-side recordings panel (#337): query the device's RecordInfo
  // index for a time range, then fetch any entry into the normal local
  // recordings library (playback INVITE). While a fetch runs, MANSRTSP
  // controls (pause/resume/speed) are available.
  import { onMount, onDestroy } from 'svelte';
  import {
    queryGB28181Records,
    startGB28181Playback,
    startGB28181Download,
    gb28181PlaybackStatus,
    stopGB28181Playback,
    controlGB28181Playback,
  } from '$lib/api';
  import type { GB28181DeviceRecord, GB28181PlaybackStatus } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { Search, Download, FileDown, Pause, Play, Square, HardDriveDownload, Loader2 } from 'lucide-svelte';

  let { channelId }: { channelId: string } = $props();

  // Query window — defaults: today 00:00 → now.
  function toLocalInput(d: Date): string {
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  let rangeStart = $state(toLocalInput(new Date(new Date().setHours(0, 0, 0, 0))));
  let rangeEnd = $state(toLocalInput(new Date()));

  let records = $state<GB28181DeviceRecord[]>([]);
  let loading = $state(false);
  let error = $state('');
  let queried = $state(false);

  // Active fetch state (one per channel, polled).
  let playback = $state<GB28181PlaybackStatus | null>(null);
  let scale = $state(1);
  let pollTimer: ReturnType<typeof setInterval> | undefined;

  const SCALE_OPTIONS = [0.25, 0.5, 1, 2, 4, 8, 16];

  async function query() {
    const start = new Date(rangeStart);
    const end = new Date(rangeEnd);
    if (isNaN(start.getTime()) || isNaN(end.getTime()) || end <= start) {
      showToast(t('gb28181.records.invalidRange'), 'error');
      return;
    }
    loading = true;
    error = '';
    try {
      const resp = await queryGB28181Records(channelId, start.toISOString(), end.toISOString());
      records = resp.records ?? [];
      queried = true;
      if (records.length === 0) showToast(t('gb28181.records.empty'), 'info');
    } catch (e: any) {
      error = e.message || t('gb28181.records.queryFailed');
      records = [];
    } finally {
      loading = false;
    }
  }

  async function pollStatus() {
    try {
      const st = await gb28181PlaybackStatus(channelId);
      playback = st.active ? st : null;
    } catch {
      playback = null;
    }
  }

  async function fetchRecord(rec: GB28181DeviceRecord) {
    try {
      await startGB28181Playback(channelId, rec.start_time, rec.end_time);
      showToast(t('gb28181.records.fetchStarted'), 'success');
      await pollStatus();
    } catch (e: any) {
      showToast(e.message || t('gb28181.records.fetchFailed'), 'error');
    }
  }

  async function downloadRecord(rec: GB28181DeviceRecord) {
    try {
      await startGB28181Download(channelId, rec.start_time, rec.end_time);
      showToast(t('gb28181.records.downloadStarted'), 'success');
      await pollStatus();
    } catch (e: any) {
      showToast(e.message || t('gb28181.records.downloadFailed'), 'error');
    }
  }

  async function pause() {
    try {
      await controlGB28181Playback(channelId, 'pause');
      await pollStatus();
    } catch (e: any) {
      showToast(e.message || t('gb28181.records.controlFailed'), 'error');
    }
  }

  async function resume() {
    try {
      await controlGB28181Playback(channelId, 'resume', { scale });
      await pollStatus();
    } catch (e: any) {
      showToast(e.message || t('gb28181.records.controlFailed'), 'error');
    }
  }

  async function stopFetch() {
    try {
      await stopGB28181Playback(channelId);
      showToast(t('gb28181.records.fetchStopped'), 'success');
      playback = null;
    } catch (e: any) {
      showToast(e.message || t('gb28181.records.stopFailed'), 'error');
    }
  }

  function fmtDur(rec: GB28181DeviceRecord): string {
    const ms = new Date(rec.end_time).getTime() - new Date(rec.start_time).getTime();
    if (isNaN(ms) || ms <= 0) return '—';
    const m = Math.floor(ms / 60000);
    const s = Math.floor((ms % 60000) / 1000);
    return `${m}m ${String(s).padStart(2, '0')}s`;
  }

  function fmtTime(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString();
  }

  onMount(() => {
    pollStatus();
    pollTimer = setInterval(pollStatus, 3000);
  });
  onDestroy(() => {
    if (pollTimer) clearInterval(pollTimer);
  });
</script>

<div class="space-y-4">
  <!-- Query bar -->
  <div class="card border th-border p-4">
    <div class="flex flex-wrap items-end gap-3">
      <div>
        <label class="input-label" for="gb-records-start">{t('gb28181.records.start')}</label>
        <input
          id="gb-records-start"
          type="datetime-local"
          class="input"
          bind:value={rangeStart}
        />
      </div>
      <div>
        <label class="input-label" for="gb-records-end">{t('gb28181.records.end')}</label>
        <input
          id="gb-records-end"
          type="datetime-local"
          class="input"
          bind:value={rangeEnd}
        />
      </div>
      <button class="btn btn-primary btn-sm" onclick={query} disabled={loading}>
        {#if loading}
          <Loader2 size={14} class="animate-spin" />
        {:else}
          <Search size={14} />
        {/if}
        {t('gb28181.records.query')}
      </button>
    </div>
    {#if error}
      <p class="text-xs th-text-danger mt-2">{error}</p>
    {/if}
  </div>

  <!-- Active fetch panel -->
  {#if playback}
    <div class="card border th-border p-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-2">
          <HardDriveDownload size={16} class="th-text-primary animate-pulse" />
          <span class="text-sm font-medium th-text-primary">
            {playback.kind === 'download' ? t('gb28181.records.downloading') : t('gb28181.records.fetching')}
          </span>
          <span class="text-xs th-text-tertiary">
            {playback.frames ?? 0} {t('gb28181.records.frames')}
            · {fmtTime(playback.start ?? '')} → {fmtTime(playback.end ?? '')}
          </span>
        </div>
        <div class="flex items-center gap-2">
          {#if playback.paused}
            <button class="btn btn-ghost btn-sm" onclick={resume}>
              <Play size={14} />
              {t('gb28181.records.resume')}
            </button>
          {:else}
            <button class="btn btn-ghost btn-sm" onclick={pause}>
              <Pause size={14} />
              {t('gb28181.records.pause')}
            </button>
          {/if}
          {#if playback.kind !== 'download'}
            <select class="input w-24 text-xs" bind:value={scale} aria-label={t('gb28181.records.scale')}>
              {#each SCALE_OPTIONS as opt}
                <option value={opt}>{opt}×</option>
              {/each}
            </select>
          {/if}
          <button class="btn btn-danger btn-sm" onclick={stopFetch}>
            <Square size={14} />
            {t('gb28181.records.stop')}
          </button>
        </div>
      </div>
      <p class="text-xs th-text-tertiary mt-2">{t('gb28181.records.fetchHint')}</p>
    </div>
  {/if}

  <!-- Records list -->
  {#if records.length > 0}
    <div class="card border th-border overflow-hidden">
      <table class="w-full text-sm">
        <thead>
          <tr class="th-bg-secondary text-xs th-text-tertiary uppercase">
            <th class="text-left px-4 py-2">{t('gb28181.records.name')}</th>
            <th class="text-left px-4 py-2">{t('gb28181.records.start')}</th>
            <th class="text-left px-4 py-2">{t('gb28181.records.end')}</th>
            <th class="text-left px-4 py-2">{t('gb28181.records.duration')}</th>
            <th class="text-right px-4 py-2"></th>
          </tr>
        </thead>
        <tbody>
          {#each records as rec, i (rec.start_time + i)}
            <tr class="border-t th-border">
              <td class="px-4 py-2 th-text-primary">{rec.name || '—'}</td>
              <td class="px-4 py-2 th-text-secondary">{fmtTime(rec.start_time)}</td>
              <td class="px-4 py-2 th-text-secondary">{fmtTime(rec.end_time)}</td>
              <td class="px-4 py-2 th-text-secondary">{fmtDur(rec)}</td>
              <td class="px-4 py-2 text-right whitespace-nowrap">
                <button
                  class="btn btn-ghost btn-sm"
                  onclick={() => fetchRecord(rec)}
                  disabled={!!playback}
                  title={t('gb28181.records.fetch')}
                >
                  <Download size={14} />
                  {t('gb28181.records.fetch')}
                </button>
                <button
                  class="btn btn-ghost btn-sm ml-1"
                  onclick={() => downloadRecord(rec)}
                  disabled={!!playback}
                  title={t('gb28181.records.download')}
                >
                  <FileDown size={14} />
                  {t('gb28181.records.download')}
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if queried && !loading}
    <div class="card border th-border p-8 text-center">
      <p class="th-text-secondary">{t('gb28181.records.emptyHint')}</p>
    </div>
  {/if}
</div>
