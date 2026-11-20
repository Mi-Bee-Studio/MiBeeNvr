<script lang="ts">
  // Time sync (#time-sync): per-camera auto opt-in + live clock status with
  // manual corrections. Extracted verbatim from CameraForm.svelte — the
  // parent keeps formAutoTimeSync (payload) as a bindable and passes the
  // editing camera's id for the status endpoints.
  import { t } from '$lib/i18n';
  import { fmtSkew } from '$lib/camera-form-helpers';
  import { getCameraTime, syncCameraTime, pointCameraAtNVR } from '$lib/api';
  import { friendlyError } from '$lib/errors';
  import type { CameraTimeStatus } from '$lib/api';

  interface Props {
    cameraId: string;
    formAutoTimeSync: boolean;
  }

  let { cameraId, formAutoTimeSync = $bindable(true) }: Props = $props();

  let timeStatus = $state<CameraTimeStatus | null>(null);
  let timeBusy = $state(false);
  let timeMsg = $state('');

  // --- Time sync actions (#time-sync) ---
  async function refreshTimeStatus(): Promise<void> {
    timeBusy = true;
    timeMsg = '';
    try {
      timeStatus = await getCameraTime(cameraId);
    } catch (e) {
      timeMsg = friendlyError(e);
    } finally {
      timeBusy = false;
    }
  }

  async function syncTimeNow(): Promise<void> {
    timeBusy = true;
    timeMsg = '';
    try {
      const res = await syncCameraTime(cameraId);
      if (res.changed) {
        timeMsg = t('cameras.timeSyncSynced', { values: { s: res.after_seconds.toFixed(1) } });
      } else {
        timeMsg = t('cameras.timeSyncNoChange');
      }
      await refreshTimeStatus();
    } catch (e) {
      timeMsg = friendlyError(e);
    } finally {
      timeBusy = false;
    }
  }

  async function useNVRAsNTP(): Promise<void> {
    timeBusy = true;
    timeMsg = '';
    try {
      const res = await pointCameraAtNVR(cameraId);
      timeMsg = t('cameras.timeSyncNTPDone', { values: { server: res.server || 'NVR' } });
      await refreshTimeStatus();
    } catch (e) {
      timeMsg = friendlyError(e);
    } finally {
      timeBusy = false;
    }
  }
</script>

<details class="group rounded-lg border th-border-default">
  <summary class="flex cursor-pointer select-none items-center gap-2 px-3 py-2 text-sm font-medium">
    {t('cameras.timeSyncTitle')}
    {#if timeStatus?.available}
      <span class="text-xs th-text-muted">{t('cameras.timeSyncSkew')}: {fmtSkew(timeStatus.skew_seconds)}</span>
    {/if}
  </summary>
  <div class="space-y-2 px-3 pb-3">
    <div class="flex items-center gap-2">
      <input id="cam-auto-time-sync" type="checkbox" class="checkbox" bind:checked={formAutoTimeSync} />
      <label for="cam-auto-time-sync" class="input-label cursor-pointer">
        {t('cameras.timeSyncAuto')}
      </label>
    </div>
    <p class="text-xs th-text-muted">{t('cameras.timeSyncAutoHint')}</p>

    {#if timeStatus}
      {#if timeStatus.available}
        <div class="text-sm th-text-muted flex flex-wrap gap-x-4 gap-y-1">
          <span>{t('cameras.timeSyncSkew')}: <span class="font-mono">{fmtSkew(timeStatus.skew_seconds)}</span></span>
          {#if timeStatus.datetime_type}
            <span>{t('cameras.timeSyncMode')}: {timeStatus.datetime_type}</span>
          {/if}
          {#if timeStatus.timezone}
            <span>{t('cameras.timeSyncZone')}: {timeStatus.timezone}</span>
          {/if}
          <span>{timeStatus.sntp_enabled ? t('cameras.timeSyncSNTPOn') : t('cameras.timeSyncSNTPOff')}</span>
        </div>
      {:else}
        <p class="text-sm text-amber-500">{t('cameras.timeSyncUnavailable')}{timeStatus.error ? ` (${timeStatus.error})` : ''}</p>
      {/if}
      {#if !timeStatus.credentials}
        <p class="text-xs th-text-muted">{t('cameras.timeSyncNoCreds')}</p>
      {/if}
    {/if}
    {#if timeMsg}
      <p class="text-sm th-text-muted">{timeMsg}</p>
    {/if}
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-sm" disabled={timeBusy} onclick={refreshTimeStatus}>
        {t('cameras.timeSyncRefresh')}
      </button>
      <button type="button" class="btn btn-sm" disabled={timeBusy} onclick={syncTimeNow}>
        {t('cameras.timeSyncNow')}
      </button>
      <button type="button" class="btn btn-sm" disabled={timeBusy} onclick={useNVRAsNTP}>
        {t('cameras.timeSyncNTP')}
      </button>
    </div>
  </div>
</details>
