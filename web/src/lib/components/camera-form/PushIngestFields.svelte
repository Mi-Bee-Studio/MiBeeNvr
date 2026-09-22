<script lang="ts">
    import { t } from '$lib/i18n';
    import type { Camera } from '$lib/api';

  interface Props {
    formProtocol: string;
    /** Push/ingest identity fields — owned by the parent form (save payload reads them). */
    formGB28181DeviceID?: string;
    formGB28181ChannelID?: string;
    formStreamKey?: string;
    formSRTPassphrase?: string;
    formSRTStreamID?: string;
    formPushRetentionDays?: number | null;
    validationErrors: Record<string, string>;
    editingCamera: Camera | null;
  }

  let {
    formProtocol,
    formGB28181DeviceID = $bindable(''),
    formGB28181ChannelID = $bindable(''),
    formStreamKey = $bindable(''),
    formSRTPassphrase = $bindable(''),
    formSRTStreamID = $bindable(''),
    formPushRetentionDays = $bindable(null),
    validationErrors,
    editingCamera,
  }: Props = $props();
</script>

{#if formProtocol === 'gb28181'}
  <!-- GB28181: the camera is identified by its SIP DeviceID + ChannelID.
       The NVR invites the channel over SIP; there is no URL to dial. -->
  <div>
    <label for="cam-gb28181-device-id" class="input-label">{t('cameras.gb28181DeviceId')}</label>
    <input id="cam-gb28181-device-id" type="text" class="input {validationErrors['gb28181_device_id'] ? 'border-red-500' : ''}" bind:value={formGB28181DeviceID}
      placeholder={t('cameras.gb28181DeviceIdPlaceholder')}
      oninput={() => { if (validationErrors['gb28181_device_id']) delete validationErrors['gb28181_device_id']; }} />
    {#if validationErrors['gb28181_device_id']}
      <p class="th-color-danger text-xs mt-1">{validationErrors['gb28181_device_id']}</p>
    {/if}
  </div>
  <div>
    <label for="cam-gb28181-channel-id" class="input-label">{t('cameras.gb28181ChannelId')}</label>
    <input id="cam-gb28181-channel-id" type="text" class="input {validationErrors['gb28181_channel_id'] ? 'border-red-500' : ''}" bind:value={formGB28181ChannelID}
      placeholder={t('cameras.gb28181ChannelIdPlaceholder')}
      oninput={() => { if (validationErrors['gb28181_channel_id']) delete validationErrors['gb28181_channel_id']; }} />
    {#if validationErrors['gb28181_channel_id']}
      <p class="th-color-danger text-xs mt-1">{validationErrors['gb28181_channel_id']}</p>
    {/if}
  </div>
{/if}

{#if formProtocol === 'whip'}
  <!-- WHIP push (WebRTC): browser/OBS pushes to the NVR; show the endpoint -->
  <div>
    <label for="cam-stream-key" class="input-label">{t('cameras.streamKey')}</label>
    <input id="cam-stream-key" type="text" class="input" bind:value={formStreamKey}
      placeholder="front-door" />
    <p class="text-xs th-text-muted mt-1">
      {t('cameras.whipPushAddress')}: http{'<'}NVR-IP:PORT{'>'}/whip/{formStreamKey || '<key>'}
    </p>
    <p class="text-xs th-text-muted mt-1">{t('cameras.whipHint')}</p>
  </div>
{/if}

{#if formProtocol === 'rtmp'}
  <!-- RTMP push: publisher connects to NVR; show the ingest address -->
  <div>
    <label for="cam-stream-key" class="input-label">{t('cameras.streamKey')}</label>
    <input id="cam-stream-key" type="text" class="input" bind:value={formStreamKey}
      placeholder="front-door" />
    <p class="text-xs th-text-muted mt-1">
      {t('cameras.rtmpPushAddress')}: rtmp://{'<'}NVR-IP{'>'}:1935/live/{formStreamKey || '<key>'}
    </p>
  </div>
{/if}

{#if formProtocol === 'srt'}
  <!-- SRT push: publisher connects to NVR -->
  <div>
    <label for="cam-srt-stream-id" class="input-label">{t('cameras.srtStreamID')}</label>
    <input id="cam-srt-stream-id" type="text" class="input" bind:value={formSRTStreamID}
      placeholder="live/front-door" />
    <p class="text-xs th-text-muted mt-1">
      {t('cameras.srtPushAddress')}: srt://{'<'}NVR-IP{'>'}:9000?streamid={formSRTStreamID || editingCamera?.id || '<id>'}
    </p>
  </div>
  <div>
    <label for="cam-srt-passphrase" class="input-label">{t('cameras.srtPassphrase')}</label>
    <input id="cam-srt-passphrase" type="text" class="input" bind:value={formSRTPassphrase}
      placeholder="(optional AES passphrase)" />
    <p class="text-xs th-text-muted mt-1">{t('cameras.srtPassphraseHint')}</p>
  </div>
{/if}

{#if formProtocol === 'srt' || formProtocol === 'rtmp' || formProtocol === 'whip'}
  <!-- Push-in save policy: follow global / live-only / custom retention -->
  <div>
    <label for="cam-push-retention" class="input-label">{t('cameras.pushRetention')}</label>
    <select id="cam-push-retention" class="input" onchange={(e) => {
      const v = (e.target as HTMLSelectElement).value;
      formPushRetentionDays = v === '' ? null : v === 'live' ? 0 : parseInt(v, 10);
    }}>
      <option value="">{t('cameras.pushRetentionGlobal')}</option>
      <option value="live" selected={formPushRetentionDays === 0}>{t('cameras.pushRetentionLiveOnly')}</option>
      {#each [1, 3, 7, 14, 30, 90] as d}
        <option value={d} selected={formPushRetentionDays === d}>{d} {t('cameras.days')}</option>
      {/each}
    </select>
    <p class="text-xs th-text-muted mt-1">{t('cameras.pushRetentionHint')}</p>
  </div>
{/if}
