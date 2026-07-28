<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getMergeSettings, updateMergeSettings, getStreamingSettings, updateStreamingSettings, getStats } from '$lib/api';
  import type { StorageStats } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';
  // SettingsStreamingCard import removed (#153) — WebRTC/FLV cards removed.

  let loading = $state(true);
  let saving = $state(false);

  // Merge settings state
  let mergeEnabled = $state(true);

  // WebDAV settings
  let webdavEnabled = $state(false);
  let webdavReadWrite = $state(false);

  // Streaming settings state — only RTMP/SRT remain (external ingest).
  // WebRTC/FLV viewer counts + timeouts removed (#153).
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
      mergeEnabled,
      webdavEnabled, webdavReadWrite,
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
      mergeEnabled,
      webdavEnabled, webdavReadWrite,
      streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
    });
  }

  async function loadMergeSettings() {
    try {
      const mergeSettings = await getMergeSettings();
      mergeEnabled = mergeSettings.enabled ?? true;
    } catch (e) {
      console.warn('Failed to load merge settings:', e);
    }
  }

  async function loadStreamingConfig() {
    try {
      const config = await getStreamingSettings();
      // WebRTC/FLV viewer counts + timeouts removed (#153) — backend defaults.
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
      // Save merge settings — only the enabled toggle; internal parameters
      // (check_interval, window_size, etc.) use backend defaults (#153).
      await updateMergeSettings({
        enabled: mergeEnabled,
      });

      // Save streaming settings. WebRTC/FLV viewer counts and timeouts use
      // backend defaults (#153). Only RTMP/SRT ports + stream keys are saved.
      await updateStreamingSettings({
        webrtc: {
          enabled: true,
          max_viewers: 4,
          idle_timeout: '5m',
        },
        flv: {
          enabled: true,
          max_viewers: 10,
          idle_timeout: '5m',
        },
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
    <div class="flex items-center justify-between">
      <div>
        <span class="text-sm font-medium th-text-primary">{t('merge.enableMerge')}</span>
        <p class="text-xs th-text-tertiary mt-0.5">{mergeEnabled ? t('merge.enabledState') : t('merge.disabledState')}</p>
      </div>
      <Toggle checked={mergeEnabled} onChange={(v) => { mergeEnabled = v; }} label={t('merge.enableMerge')} />
    </div>
    <!-- Merge internal parameters (check_interval, window_size, min_segments,
         min_segment_age, batch_limit) removed (#153): backend defaults are
         optimal for all deployments. Exposing them added cognitive load with
         no user-perceivable benefit. -->
  </SettingsCard>

  <!-- WebDAV Settings -->
  <SettingsCard
    title={t('settings.webdav')}
    subtitle={t('settings.advanced.webdav.description')}
    defaultOpen={false}
  >
    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <!-- Enable WebDAV -->
      <div>
        <span class="input-label">{t('settings.webdavEnabled')}</span>
        <div class="flex items-center gap-3 mt-2">
          <Toggle checked={webdavEnabled} onChange={(v) => { webdavEnabled = v; }} label={t('settings.webdavEnabled')} />
          <span class="text-sm th-text-secondary">{webdavEnabled ? t('settings.webdavEnabledOn') : t('settings.webdavEnabledOff')}</span>
        </div>
      </div>

      <!-- Read-Write Mode -->
      <div>
        <span class="input-label">{t('settings.webdavReadWrite')}</span>
        <div class="flex items-center gap-3 mt-2">
          <Toggle checked={webdavReadWrite} onChange={(v) => { webdavReadWrite = v; }} label={t('settings.webdavReadWrite')} />
          <span class="text-sm th-text-secondary">{webdavReadWrite ? t('settings.webdavReadWriteOn') : t('settings.webdavReadWriteOff')}</span>
        </div>
        <p class="text-xs th-text-tertiary mt-2">{t('settings.webdavReadWriteHint')}</p>
      </div>
    </div>
    <!-- WebDAV path prefix removed from default view (#153): /dav is almost
         never changed. Still saved if previously set; backend keeps the value. -->
  </SettingsCard>

  <!-- WebRTC/FLV viewer-count + idle-timeout cards removed (#153): these are
       internal performance parameters. The backend auto-selects reasonable
       limits based on hardware. The protocols themselves are always available —
       the Player Orchestrator enables them on demand per camera. -->

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
