<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getMergeSettings, updateMergeSettings, getStreamingSettings, updateStreamingSettings, getStats } from '$lib/api';
  import type { StorageStats } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import SettingsStreamingCard from './SettingsStreamingCard.svelte';

  let loading = $state(true);
  let saving = $state(false);

  // Merge settings state
  let mergeEnabled = $state(true);
  let mergeCheckInterval = $state('1h');
  let mergeWindowSize = $state('1h');
  let mergeMinSegments = $state(3);
  let mergeMinSegmentAge = $state('10m');
  let mergeBatchLimit = $state(100);

  // WebDAV settings
  let webdavEnabled = $state(false);
  let webdavPathPrefix = $state('/dav');
  let webdavReadWrite = $state(false);

  // Streaming settings state
  let streamingWebrtcEnabled = $state(true);
  let streamingWebrtcMaxViewers = $state(4);
  let streamingWebrtcIdleTimeout = $state('5m');
  let streamingFlvEnabled = $state(true);
  let streamingFlvMaxViewers = $state(10);
  // NOTE: streamingHlsLlHls (LL-HLS toggle) removed — HLS is always low-latency
  // now (hls-config.ts lowLatencyMode:true is on for every HLS mount, and the
  // LL-HLS/HLS buffer distinction was collapsed). The backend hls.low_latency
  // field is still saved (hard-coded true below) for backward compat.
  let streamingRtmpEnabled = $state(false);
  let streamingRtmpPort = $state(1935);
  let streamingSrtEnabled = $state(false);
  let streamingSrtPort = $state(9000);

  // RTMP stream key mappings
  let rtmpStreamKeys = $state<{key: string, cameraId: string}[]>([]);

  // SRT stream configurations
  let srtStreams = $state<{streamId: string, cameraId: string, mode: string, address: string, passphrase: string}[]>([]);

  // Disk info from stats API
  let diskInfo = $state<StorageStats | null>(null);

  // Original values snapshot for dirty tracking
  let originalSnapshot = $state('');

  // Validation
  let validationErrors = $state<Record<string, string>>({});

  // Confirmation dialog
  let showConfirmDialog = $state(false);

  // Unsaved changes navigation guard
  let showNavGuard = $state(false);
  let pendingHash = $state('');

  // Derived: is any setting dirty?
  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({
      mergeEnabled, mergeCheckInterval, mergeWindowSize,
      mergeMinSegments, mergeMinSegmentAge, mergeBatchLimit,
      webdavEnabled, webdavPathPrefix, webdavReadWrite,
      streamingWebrtcEnabled, streamingWebrtcMaxViewers,
      streamingWebrtcIdleTimeout, streamingFlvEnabled, streamingFlvMaxViewers,
      streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
    });
    return current !== originalSnapshot;
  });

  // Disk GB estimation
  let diskGbEstimate = $derived.by(() => {
    if (!diskInfo || diskInfo.total_bytes === 0) return '';
    const remainingPct = (100 - 0) / 100;
    const remainingBytes = diskInfo.total_bytes * remainingPct;
    const gb = remainingBytes / (1024 * 1024 * 1024);
    if (gb >= 1) return `≈ ${gb.toFixed(0)} GB`;
    const mb = remainingBytes / (1024 * 1024);
    return `≈ ${mb.toFixed(0)} MB`;
  });

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({
      mergeEnabled, mergeCheckInterval, mergeWindowSize,
      mergeMinSegments, mergeMinSegmentAge, mergeBatchLimit,
      webdavEnabled, webdavPathPrefix, webdavReadWrite,
      streamingWebrtcEnabled, streamingWebrtcMaxViewers,
      streamingWebrtcIdleTimeout, streamingFlvEnabled, streamingFlvMaxViewers,
      streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
    });
  }

  async function loadMergeSettings() {
    try {
      const mergeSettings = await getMergeSettings();
      mergeEnabled = mergeSettings.enabled ?? true;
      mergeCheckInterval = mergeSettings.check_interval ?? '1h';
      mergeWindowSize = mergeSettings.window_size ?? '1h';
      mergeMinSegments = mergeSettings.min_segments_to_merge ?? 3;
      mergeMinSegmentAge = mergeSettings.min_segment_age ?? '10m';
      mergeBatchLimit = mergeSettings.batch_limit ?? 100;
    } catch (e) {
      console.warn('Failed to load merge settings:', e);
    }
  }

  async function loadStreamingConfig() {
    try {
      const config = await getStreamingSettings();
      streamingWebrtcEnabled = config.webrtc?.enabled ?? true;
      streamingWebrtcMaxViewers = config.webrtc?.max_viewers ?? 4;
      streamingWebrtcIdleTimeout = config.webrtc?.idle_timeout || '5m';
      streamingFlvEnabled = config.flv?.enabled ?? true;
      streamingFlvMaxViewers = config.flv?.max_viewers ?? 10;
      // LL-HLS toggle removed — HLS is always low-latency now. (hls.low_latency
      // is hard-coded true on save for backward compat with the backend field.)
      streamingRtmpEnabled = config.rtmp?.enabled ?? false;
      streamingRtmpPort = config.rtmp?.port ?? 1935;
      const rtmpKeys = config.rtmp?.stream_keys;
      rtmpStreamKeys = rtmpKeys
        ? Object.entries(rtmpKeys).map(([key, cameraId]) => ({ key, cameraId: String(cameraId) }))
        : [];
      streamingSrtEnabled = config.srt?.enabled ?? false;
      streamingSrtPort = config.srt?.port ?? 9000;
      const srtStreamList = config.srt?.streams;
      srtStreams = srtStreamList
        ? srtStreamList.map((s) => ({
            streamId: s.stream_id || '',
            cameraId: s.camera_id || '',
            mode: s.mode || 'listener',
            address: s.address || '',
            passphrase: s.passphrase || '',
          }))
        : [];
    } catch (e) { console.warn('Failed to load streaming settings:', e); }
  }

  async function loadAllSettings() {
    loading = true;
    try {
      await loadMergeSettings();
      await loadStreamingConfig();
      diskInfo = await getStats().catch(() => null);
      captureSnapshot();
    } finally {
      loading = false;
    }
  }

  async function performSave() {
    saving = true;
    try {
      // Save merge settings
      await updateMergeSettings({
        enabled: mergeEnabled,
        check_interval: mergeCheckInterval,
        window_size: mergeWindowSize,
        min_segments_to_merge: mergeMinSegments,
        min_segment_age: mergeMinSegmentAge,
        batch_limit: mergeBatchLimit,
      });

      // Save streaming settings (preserve default_protocol from existing config)
      const existingStreaming = await getStreamingSettings().catch(() => ({ default_protocol: 'hls' }));
      await updateStreamingSettings({
        default_protocol: existingStreaming.default_protocol || 'hls',
        webrtc: {
          enabled: streamingWebrtcEnabled,
          max_viewers: streamingWebrtcMaxViewers,
          idle_timeout: streamingWebrtcIdleTimeout,
        },
        flv: {
          enabled: streamingFlvEnabled,
          max_viewers: streamingFlvMaxViewers,
          idle_timeout: '5m',
        },
        // HLS is always low-latency now (the LL-HLS/HLS distinction was
        // collapsed — hls-config.ts uses lowLatencyMode:true for every mount).
        // Persist true so the backend field stays consistent.
        hls: { low_latency: true },
        rtmp: {
          enabled: streamingRtmpEnabled,
          port: streamingRtmpPort,
          stream_keys: Object.fromEntries(rtmpStreamKeys.map(sk => [sk.key, sk.cameraId])),
        },
        srt: {
          enabled: streamingSrtEnabled,
          port: streamingSrtPort,
          streams: srtStreams.map(s => ({
            stream_id: s.streamId,
            camera_id: s.cameraId,
            mode: s.mode,
            address: s.address,
            passphrase: s.passphrase,
          })),
        },
      });

      captureSnapshot();
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
    } finally {
      saving = false;
    }
  }

  function save() {
    performSave();
  }

  // Unsaved changes navigation guard
  function handleHashChange(e: HashChangeEvent) {
    const dirty = isDirty;
    if (dirty && !showNavGuard) {
      e.preventDefault();
      pendingHash = window.location.hash;
      showNavGuard = true;
    }
  }

  function confirmNavigation() {
    showNavGuard = false;
    window.removeEventListener('hashchange', handleHashChange);
    if (pendingHash) window.location.hash = pendingHash;
    window.addEventListener('hashchange', handleHashChange);
  }

  function cancelNavigation() {
    showNavGuard = false;
    pendingHash = '';
  }

  function confirmSave() {
    showConfirmDialog = false;
    performSave();
  }

  function cancelSave() {
    showConfirmDialog = false;
  }

  onMount(() => {
    loadAllSettings();
    window.addEventListener('hashchange', handleHashChange);
  });

  onDestroy(() => {
    window.removeEventListener('hashchange', handleHashChange);
  });
</script>

{#if loading}
  <div class="card border th-border">
    <div class="p-6 space-y-4">
      <div class="h-6 w-40 th-bg-tertiary rounded animate-pulse"></div>
      <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
      <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div class="space-y-2"><div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
        <div class="space-y-2"><div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div><div class="h-3 w-full th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
        <div class="space-y-2"><div class="h-4 w-28 th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
      </div>
    </div>
  </div>
{:else}
  <!-- Merge Strategy -->
  <SettingsCard
    title={t('merge.title')}
    subtitle={t('settings.advanced.merge.description')}
    defaultOpen={false}
  >
    <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
      <!-- Enable Merge -->
      <div>
        <label class="input-label" for="merge-toggle">{t('merge.enableMerge')}</label>
        <div class="flex items-center gap-3 mt-2">
          <button
            id="merge-toggle" aria-label={t('merge.enableMerge')}
            type="button"
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {mergeEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
            onclick={() => { mergeEnabled = !mergeEnabled; }}
            onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); mergeEnabled = !mergeEnabled; } }}
            role="switch"
            aria-checked={mergeEnabled}
          >
            <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {mergeEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
          </button>
          <span class="text-sm th-text-secondary">{mergeEnabled ? t('merge.enabledState') : t('merge.disabledState')}</span>
        </div>
      </div>

      <!-- Check Interval -->
      <div>
        <label for="mergeInterval" class="input-label">{t('merge.checkInterval')}</label>
        <select id="mergeInterval" class="input" bind:value={mergeCheckInterval}>
          <option value="30m">{t('merge.30m')}</option>
          <option value="1h">{t('merge.1h')}</option>
          <option value="2h">{t('merge.2h')}</option>
          <option value="6h">{t('merge.6h')}</option>
        </select>
      </div>

      <!-- Window Size -->
      <div>
        <label for="mergeWindow" class="input-label">{t('merge.windowSize')}</label>
        <select id="mergeWindow" class="input" bind:value={mergeWindowSize}>
          <option value="30m">{t('merge.30m')}</option>
          <option value="1h">{t('merge.1h')}</option>
          <option value="2h">{t('merge.2h')}</option>
        </select>
      </div>

      <!-- Min Segments -->
      <div>
        <label for="mergeMinSegs" class="input-label">{t('merge.minSegments')}</label>
        <input id="mergeMinSegs" type="number" class="input" bind:value={mergeMinSegments} min="2" max="50" />
      </div>

      <!-- Min Segment Age -->
      <div>
        <label for="mergeMinAge" class="input-label">{t('merge.minAge')}</label>
        <select id="mergeMinAge" class="input" bind:value={mergeMinSegmentAge}>
          <option value="5m">{t('merge.5m')}</option>
          <option value="10m">{t('merge.10m')}</option>
          <option value="30m">{t('merge.30m')}</option>
          <option value="1h">{t('merge.1h')}</option>
        </select>
      </div>

      <!-- Batch Limit -->
      <div>
        <label for="mergeBatch" class="input-label">{t('merge.batchLimitLabel')}</label>
        <input id="mergeBatch" type="number" class="input" bind:value={mergeBatchLimit} min="10" max="1000" />
      </div>
    </div>
  </SettingsCard>

  <!-- WebDAV Settings -->
  <SettingsCard
    title={t('settings.webdav')}
    subtitle={t('settings.advanced.webdav.description')}
    defaultOpen={false}
  >
    <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
      <!-- Enable WebDAV -->
      <div>
        <label class="input-label" for="webdav-toggle">{t('settings.webdavEnabled')}</label>
        <div class="flex items-center gap-3 mt-2">
          <button
            id="webdav-toggle" aria-label={t('settings.webdavEnabled')}
            type="button"
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {webdavEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
            onclick={() => { webdavEnabled = !webdavEnabled; }}
            onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); webdavEnabled = !webdavEnabled; } }}
            role="switch"
            aria-checked={webdavEnabled}
          >
            <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {webdavEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
          </button>
          <span class="text-sm th-text-secondary">{webdavEnabled ? t('settings.webdavEnabledOn') : t('settings.webdavEnabledOff')}</span>
        </div>
      </div>

      <!-- Path Prefix -->
      <div>
        <label for="webdavPrefix" class="input-label">{t('settings.webdavPathPrefix')}</label>
        <input id="webdavPrefix" type="text" class="input" bind:value={webdavPathPrefix} placeholder="/dav" />
      </div>

      <!-- Read-Write Mode -->
      <div>
        <label class="input-label" for="webdav-rw-toggle">{t('settings.webdavReadWrite')}</label>
        <div class="flex items-center gap-3 mt-2">
          <button
            id="webdav-rw-toggle" aria-label={t('settings.webdavReadWrite')}
            type="button"
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {webdavReadWrite ? 'bg-blue-600' : 'th-bg-tertiary'}"
            onclick={() => { webdavReadWrite = !webdavReadWrite; }}
            onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); webdavReadWrite = !webdavReadWrite; } }}
            role="switch"
            aria-checked={webdavReadWrite}
          >
            <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {webdavReadWrite ? 'translate-x-6' : 'translate-x-1'}"></span>
          </button>
          <span class="text-sm th-text-secondary">{webdavReadWrite ? t('settings.webdavReadWriteOn') : t('settings.webdavReadWriteOff')}</span>
        </div>
        <p class="text-xs th-text-tertiary mt-2">{t('settings.webdavReadWriteHint')}</p>
      </div>
    </div>
  </SettingsCard>

  <!-- WebRTC -->
  <SettingsCard
    title={t('settings.streaming.webrtc')}
    subtitle={t('settings.advanced.streaming.description')}
    defaultOpen={false}
  >
    <SettingsStreamingCard
      title={t('settings.streaming.webrtc')}
      protocol="webrtc"
      enabled={streamingWebrtcEnabled}
      onEnabledChange={(val) => streamingWebrtcEnabled = val}
      maxViewers={streamingWebrtcMaxViewers}
      onMaxViewersChange={(val) => streamingWebrtcMaxViewers = val}
      idleTimeout={streamingWebrtcIdleTimeout}
      onIdleTimeoutChange={(val) => streamingWebrtcIdleTimeout = val}
    />
  </SettingsCard>

  <!-- HTTP-FLV -->
  <SettingsCard
    title={t('settings.streaming.flv')}
    subtitle={t('settings.advanced.streaming.description')}
    defaultOpen={false}
  >
    <SettingsStreamingCard
      title={t('settings.streaming.flv')}
      protocol="flv"
      enabled={streamingFlvEnabled}
      onEnabledChange={(val) => streamingFlvEnabled = val}
      maxViewers={streamingFlvMaxViewers}
      onMaxViewersChange={(val) => streamingFlvMaxViewers = val}
    />
  </SettingsCard>

  <!-- HLS card removed — HLS is always low-latency now and has no user-facing
       knobs (low_latency:true is hard-coded on save; hls-config.ts uses
       lowLatencyMode:true for every mount). The backend hls handler stays on. -->

  <!-- RTMP Ingest -->
  <SettingsCard
    title={t('settings.streaming.rtmp')}
    subtitle={t('settings.advanced.streaming.description')}
    defaultOpen={false}
  >
    <SettingsStreamingCard
      title={t('settings.streaming.rtmp')}
      protocol="rtmp"
      enabled={streamingRtmpEnabled}
      onEnabledChange={(val) => streamingRtmpEnabled = val}
      port={streamingRtmpPort}
      onPortChange={(val) => streamingRtmpPort = val}
    />
  </SettingsCard>

  <!-- SRT -->
  <SettingsCard
    title={t('settings.streaming.srt')}
    subtitle={t('settings.advanced.streaming.description')}
    defaultOpen={false}
  >
    <SettingsStreamingCard
      title={t('settings.streaming.srt')}
      protocol="srt"
      enabled={streamingSrtEnabled}
      onEnabledChange={(val) => streamingSrtEnabled = val}
      port={streamingSrtPort}
      onPortChange={(val) => streamingSrtPort = val}
    />
  </SettingsCard>

  <!-- Resource Usage Estimates -->
  <SettingsCard
    title={t('settings.streaming.resourceEstimate')}
    subtitle={t('settings.streaming.resourceEstimateDesc')}
    defaultOpen={false}
  >
    <div class="space-y-2">
      <div class="flex items-center gap-2 text-xs th-text-secondary">
        <span class="w-2 h-2 rounded-full bg-[var(--color-danger)]"></span>
        <span>{t('settings.streaming.resource.webrtc')}</span>
      </div>
      <div class="flex items-center gap-2 text-xs th-text-secondary">
        <span class="w-2 h-2 rounded-full bg-[var(--color-warning)]"></span>
        <span>{t('settings.streaming.resource.flv')}</span>
      </div>
      <div class="flex items-center gap-2 text-xs th-text-secondary">
        <span class="w-2 h-2 rounded-full bg-[var(--color-success)]"></span>
        <span>{t('settings.streaming.resource.hls')}</span>
      </div>
    </div>
  </SettingsCard>

  <!-- Save button -->
  <div class="flex items-center gap-4 pt-2">
    <button onclick={save} class="btn btn-primary" disabled={saving || !isDirty}>
      {#if saving}
        <span class="spinner mr-2"></span>
        {t('settings.saving')}
      {:else}
        <span class="flex items-center gap-1">
          {t('settings.save')}
          {#if isDirty}
            <span class="text-xs font-normal th-color-warning ml-2 inline-flex items-center gap-1">
              <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><circle cx="12" cy="12" r="3"/></svg>
              {t('settings.unsavedChanges')}
            </span>
          {/if}
        </span>
      {/if}
    </button>
  </div>
{/if}

<!-- Confirmation dialog -->
{#if showConfirmDialog}
  <ConfirmDialog
    title={t('settings.confirmSaveTitle')}
    message={t('settings.confirmSaveMessage')}
    confirmText={t('settings.confirmSave')}
    onconfirm={confirmSave}
    oncancel={cancelSave}
    variant="danger"
  />
{/if}

<!-- Unsaved changes navigation guard -->
{#if showNavGuard}
  <ConfirmDialog
    title={t('settings.unsavedTitle')}
    message={t('settings.unsavedMessage')}
    confirmText={t('settings.unsavedDiscard')}
    onconfirm={confirmNavigation}
    oncancel={cancelNavigation}
    variant="danger"
  />
{/if}
