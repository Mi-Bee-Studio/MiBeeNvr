<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings, getMergeSettings, updateMergeSettings, getFeatures, updateFeatures, getStats, listCameras, getStreamingSettings, updateStreamingSettings } from '$lib/api';
  import type { SettingsConfig, FeatureFlags, StorageStats, Camera, StreamingConfig } from '$lib/api';
  import { getItemsPerPage, setItemsPerPage, getAutoRefresh, setAutoRefresh } from '../lib/preferences';
  import { t } from '$lib/i18n';
  import { AlertCircle, Settings as SettingsIcon, RefreshCw, CircleDot } from 'lucide-svelte';
  import { AlertTriangle } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Tab from '$lib/components/Tab.svelte';
  let settings = $state<SettingsConfig | null>(null);
  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);

// Form state
let retentionDays = $state(30);
let diskThresholdPercent = $state(90);
let checkInterval = $state('1h');
let itemsPerPage = $state(getItemsPerPage());
  let autoRefresh = $state(getAutoRefresh());
let webdavEnabled = $state(false);
let webdavPathPrefix = $state('/dav');
let webdavReadWrite = $state(false);

// Merge settings state
let mergeEnabled = $state(true);
let mergeCheckInterval = $state('1h');
let mergeWindowSize = $state('1h');
let mergeMinSegments = $state(3);
let mergeMinSegmentAge = $state('10m');
let mergeBatchLimit = $state(100);

// Streaming settings state
let streamingDefaultProtocol = $state('hls');
let streamingWebrtcEnabled = $state(true);
let streamingWebrtcMaxViewers = $state(4);
let streamingWebrtcIdleTimeout = $state('5m');
let streamingFlvEnabled = $state(true);
let streamingFlvMaxViewers = $state(10);
let streamingHlsLlHls = $state(false);
let streamingRtmpEnabled = $state(false);
let streamingRtmpPort = $state(1935);
let streamingSrtEnabled = $state(false);
let streamingSrtPort = $state(9000);
let streamingSaving = $state(false);
let expandedProtocolDoc = $state<string | null>(null);

// RTMP stream key mappings
let rtmpStreamKeys = $state<{key: string, cameraId: string}[]>([]);

// SRT stream configurations
let srtStreams = $state<{streamId: string, cameraId: string, mode: string, address: string, passphrase: string}[]>([]);


// Feature toggles state
let featureFlags = $state<Record<string, boolean>>({});
let featuresLoading = $state(true);
let featuresSaving = $state(false);

// Camera list for feature toggle affected count
let allCameras = $state<Camera[]>([]);

// Disk info from stats API
let diskInfo = $state<StorageStats | null>(null);

// Original values snapshot for dirty tracking (cleanup + webdav + merge + streaming + features)
let originalSnapshot = $state('');
let originalRetentionDays = $state(0);
let originalFeatureFlags = $state<Record<string, boolean>>({});

// Settings tab state
let activeSettingsTab = $state('general');
let settingsTabs = $derived([
  { id: 'general', label: t('settings.tabs.general') },
  { id: 'advanced', label: t('settings.tabs.advanced') },
]);

// Derived: is any setting dirty?
let isDirty = $derived(() => {
    if (loading) return false;
    const current = JSON.stringify({
      retentionDays, diskThresholdPercent, checkInterval,
      webdavEnabled, webdavPathPrefix, webdavReadWrite,
      mergeEnabled, mergeCheckInterval, mergeWindowSize,
      mergeMinSegments, mergeMinSegmentAge, mergeBatchLimit,
      streamingDefaultProtocol, streamingWebrtcEnabled, streamingWebrtcMaxViewers,
      streamingWebrtcIdleTimeout, streamingFlvEnabled, streamingFlvMaxViewers,
      streamingHlsLlHls, streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
    });
    if (current !== originalSnapshot) return true;
    if (JSON.stringify(featureFlags) !== JSON.stringify(originalFeatureFlags)) return true;
    return false;
  });

// Unsaved changes navigation guard
let showNavGuard = $state(false);
let pendingHash = $state('');

function handleHashChange(e: HashChangeEvent) {
    const dirty = isDirty();
    if (dirty && !showNavGuard) {
      e.preventDefault();
      pendingHash = window.location.hash;
      showNavGuard = true;
    }
  }

function confirmNavigation() {
    showNavGuard = false;
    // Allow navigation
    window.removeEventListener('hashchange', handleHashChange);
    if (pendingHash) window.location.hash = pendingHash;
    window.addEventListener('hashchange', handleHashChange);
  }

function cancelNavigation() {
    showNavGuard = false;
    pendingHash = '';
  }

// Disk GB estimation
let diskGbEstimate = $derived(() => {
    if (!diskInfo || diskInfo.total_bytes === 0) return '';
    const remainingPct = (100 - diskThresholdPercent) / 100;
    const remainingBytes = diskInfo.total_bytes * remainingPct;
    const gb = remainingBytes / (1024 * 1024 * 1024);
    if (gb >= 1) return `≈ ${gb.toFixed(0)} GB`;
    const mb = remainingBytes / (1024 * 1024);
    return `≈ ${mb.toFixed(0)} MB`;
  });

// Affected camera count for a protocol
function getAffectedCameraCount(protocol: string): number {
    return allCameras.filter(c => c.protocol === protocol || c.protocol.startsWith(protocol)).length;
  }

  // Validation
  let validationErrors = $state<Record<string, string>>({});


  // Confirmation dialog
  let showConfirmDialog = $state(false);
  function validateField(field: string, value: string) {
    const val = parseInt(value);
    if (field === 'retention_days') {
      if (isNaN(val) || val < 0) {
        validationErrors['retention_days'] = t('settings.invalidRetentionDays');
      } else {
        delete validationErrors['retention_days'];
      }
    } else if (field === 'disk_threshold') {
      if (isNaN(val) || val < 0 || val > 100) {
        validationErrors['disk_threshold'] = t('settings.invalidDiskThreshold');
      } else {
        delete validationErrors['disk_threshold'];
      }
    }
  }

  function validate(): boolean {
    validationErrors = {};

    if (retentionDays < 1) {
      validationErrors['retention_days'] = t('settings.validationRetention');
    }

    if (diskThresholdPercent < 0 || diskThresholdPercent > 100) {
      validationErrors['disk_threshold'] = t('settings.validationThreshold');
    }

    return Object.keys(validationErrors).length === 0;
  }

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({
      retentionDays, diskThresholdPercent, checkInterval,
      webdavEnabled, webdavPathPrefix, webdavReadWrite,
      mergeEnabled, mergeCheckInterval, mergeWindowSize,
      mergeMinSegments, mergeMinSegmentAge, mergeBatchLimit,
      streamingDefaultProtocol, streamingWebrtcEnabled, streamingWebrtcMaxViewers,
      streamingWebrtcIdleTimeout, streamingFlvEnabled, streamingFlvMaxViewers,
      streamingHlsLlHls, streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
    });
    originalRetentionDays = retentionDays;
    originalFeatureFlags = { ...featureFlags };
  }

  async function loadSettings() {
    loading = true;
    error = '';

    try {
      settings = await getSettings();
      retentionDays = settings.cleanup.retention_days;
      diskThresholdPercent = settings.cleanup.disk_threshold_percent;
      checkInterval = settings.cleanup.check_interval;
      webdavEnabled = settings.webdav?.enabled ?? false;
      webdavPathPrefix = settings.webdav?.path_prefix ?? '/dav';
      webdavReadWrite = settings.webdav?.read_write ?? false;

      // Load merge settings
      const mergeSettings = await getMergeSettings();
      mergeEnabled = mergeSettings.enabled ?? true;
      mergeCheckInterval = mergeSettings.check_interval ?? '1h';
      mergeWindowSize = mergeSettings.window_size ?? '1h';
      mergeMinSegments = mergeSettings.min_segments_to_merge ?? 3;
      mergeMinSegmentAge = mergeSettings.min_segment_age ?? '10m';
      mergeBatchLimit = mergeSettings.batch_limit ?? 100;

      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function save() {
    if (!validate()) return;

    // Check if we're reducing retention (destructive change)
    if (retentionDays < originalRetentionDays && originalRetentionDays > 0) {
      showConfirmDialog = true;
      return;
    }

    await performSave();
  }

  async function performSave() {
    saving = true;
    try {
      const payload: SettingsConfig = {
        cleanup: {
          retention_days: retentionDays,
          disk_threshold_percent: diskThresholdPercent,
          check_interval: checkInterval,
        },
        webdav: {
          enabled: webdavEnabled,
          path_prefix: webdavPathPrefix,
          read_write: webdavReadWrite,
        },
      };

      await updateSettings(payload);

      // Save merge settings
      await updateMergeSettings({
        enabled: mergeEnabled,
        check_interval: mergeCheckInterval,
        window_size: mergeWindowSize,
        min_segments_to_merge: mergeMinSegments,
        min_segment_age: mergeMinSegmentAge,
        batch_limit: mergeBatchLimit,
      });

      // Save streaming settings
      await updateStreamingSettings({
        default_protocol: streamingDefaultProtocol,
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
        hls: { low_latency: streamingHlsLlHls },
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

      // Save feature toggles
      await updateFeatures({ protocols: featureFlags });

      // Refresh state
      settings = await getSettings();
      captureSnapshot();
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
    } finally {
      saving = false;
    }
  }

  function confirmSave() {
    showConfirmDialog = false;
    performSave();
  }

  function cancelSave() {
    showConfirmDialog = false;
  }

  function handleItemsPerPageChange() {
    setItemsPerPage(itemsPerPage);
  }

  function handleAutoRefreshChange(event: Event) {
    const select = event.target as HTMLSelectElement;
    setAutoRefresh(select.value);
  }

  onMount(() => {
    loadSettings();
    loadFeatures();
    loadDiskInfo();
    loadCameraList();
    loadStreamingConfig();
    loadFeatures();
    loadDiskInfo();
    loadCameraList();
    window.addEventListener('hashchange', handleHashChange);
  });

  onDestroy(() => {
    window.removeEventListener('hashchange', handleHashChange);
  });


  async function loadFeatures() {
    featuresLoading = true;
    try {
      const data = await getFeatures();
      featureFlags = data.protocols;
      originalFeatureFlags = { ...data.protocols };
    } catch (e) { console.warn('Failed to load feature flags:', e); featureFlags = {}; } finally {
      featuresLoading = false;
    }
  }

  async function saveFeatures() {
    featuresSaving = true;
    try {
      await updateFeatures({ protocols: featureFlags });
      originalFeatureFlags = { ...featureFlags };
      showToast(t('settings.featureToggles.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.featureToggles.error'), 'error');
    } finally {
      featuresSaving = false;
    }
  }

  async function loadDiskInfo() {
    try {
      diskInfo = await getStats();
    } catch (e) { /* non-critical */ }
  }

  async function loadCameraList() {
    try {
      allCameras = await listCameras();
    } catch (e) { /* non-critical */ }
  }

  async function loadStreamingConfig() {
    try {
      const config = await getStreamingSettings();
      streamingDefaultProtocol = config.default_protocol || 'hls';
      streamingWebrtcEnabled = config.webrtc?.enabled ?? true;
      streamingWebrtcMaxViewers = config.webrtc?.max_viewers ?? 4;
      streamingWebrtcIdleTimeout = config.webrtc?.idle_timeout || '5m';
      streamingFlvEnabled = config.flv?.enabled ?? true;
      streamingFlvMaxViewers = config.flv?.max_viewers ?? 10;
      streamingHlsLlHls = config.hls?.low_latency ?? false;
      streamingRtmpEnabled = config.rtmp?.enabled ?? false;
      streamingRtmpPort = config.rtmp?.port ?? 1935;
      // Load RTMP stream keys from map to array
      const rtmpKeys = config.rtmp?.stream_keys;
      rtmpStreamKeys = rtmpKeys
        ? Object.entries(rtmpKeys).map(([key, cameraId]) => ({ key, cameraId: String(cameraId) }))
        : [];
      streamingSrtEnabled = config.srt?.enabled ?? false;
      streamingSrtPort = config.srt?.port ?? 9000;
      // Load SRT streams
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
    captureSnapshot();
  }

  async function saveStreamingSettings() {
    streamingSaving = true;
    try {
      await updateStreamingSettings({
        default_protocol: streamingDefaultProtocol,
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
        hls: { low_latency: streamingHlsLlHls },
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
      showToast(t('settings.streaming.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.streaming.error'), 'error');
    } finally {
      streamingSaving = false;
    }
  }

</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold th-text-primary">{t('settings.title')}
        {#if isDirty()}
          <span class="text-xs font-normal th-color-warning ml-2 inline-flex items-center gap-1"><CircleDot size={12} />{t('settings.unsavedChanges')}</span>
        {/if}
      </h2>
    </div>

    <!-- Error message -->
    {#if error}
      <div class="card border th-border-danger p-8 text-center">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadSettings} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    <!-- Loading state -->
    {#if loading}
      <div class="card border th-border">
        <div class="p-6 space-y-4">
          <div class="h-6 w-40 th-bg-tertiary rounded animate-pulse"></div>
          <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <div class="space-y-2">
              <div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-10 th-bg-tertiary rounded animate-pulse"></div>
            </div>
            <div class="space-y-2">
              <div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-3 w-full th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-10 th-bg-tertiary rounded animate-pulse"></div>
            </div>
            <div class="space-y-2">
              <div class="h-4 w-28 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-10 th-bg-tertiary rounded animate-pulse"></div>
            </div>
          </div>
          <div class="flex items-center gap-4 pt-2">
            <div class="h-10 w-28 th-bg-tertiary rounded animate-pulse"></div>
          </div>
        </div>
      </div>
    {:else}
      <Tab tabs={settingsTabs} activeTab={activeSettingsTab} onchange={(id) => activeSettingsTab = id} />
      <div class="space-y-6 mt-6">
      {#if activeSettingsTab === 'general'}
        <!-- Cleanup Policy -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.cleanup')}</h3>
          <p class="text-sm th-text-tertiary mb-8">{t('settings.cleanupDesc')}</p>

          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <!-- Retention Days -->
            <div>
              <label for="retention" class="input-label">{t('settings.retentionDays')}</label>
              <input
                id="retention"
                type="number"
                class="input {validationErrors['retention_days'] ? 'border-red-500' : ''}"
                bind:value={retentionDays}
                min="1"
                onblur={() => validateField('retention_days', String(retentionDays))}
                oninput={() => { if (validationErrors['retention_days']) delete validationErrors['retention_days']; }}
              />
              {#if validationErrors['retention_days']}
                <p class="th-color-danger text-xs mt-1" aria-live="polite">{validationErrors['retention_days']}</p>
              {/if}
            </div>

            <!-- Disk Threshold -->
            <div>
              <label for="threshold" class="input-label">{t('settings.diskThreshold', { percent: String(diskThresholdPercent) })}</label>
              <input
                id="threshold"
                type="number"
                class="input {validationErrors['disk_threshold'] ? 'border-red-500' : ''}"
                bind:value={diskThresholdPercent}
                min="0"
                max="100"
                onblur={() => validateField('disk_threshold', String(diskThresholdPercent))}
                oninput={() => { if (validationErrors['disk_threshold']) delete validationErrors['disk_threshold']; }}
              />
              {#if validationErrors['disk_threshold']}
                <p class="th-color-danger text-xs mt-1" aria-live="polite">{validationErrors['disk_threshold']}</p>
              {/if}
              {#if diskGbEstimate()}
                <p class="text-xs th-text-muted mt-1">{diskThresholdPercent}% {t('settings.diskRemaining')} {diskGbEstimate()}</p>
              {/if}
            </div>

            <!-- Check Interval -->
            <div>
              <label for="interval" class="input-label">{t('settings.checkInterval')}</label>
              <select id="interval" class="input" bind:value={checkInterval}>
                <option value="30m">{t('settings.every30m')}</option>
                <option value="1h">{t('settings.every1h')}</option>
                <option value="6h">{t('settings.every6h')}</option>
                <option value="24h">{t('settings.every24h')}</option>
              </select>
            </div>
          </div>
        </div>

        <!-- Frontend Preferences -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.frontendPrefs')}</h3>
          <p class="text-sm th-text-tertiary mb-8">{t('settings.frontendPrefsDesc')}</p>

          <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            <!-- Items Per Page -->
            <div>
              <label for="itemsPerPage" class="input-label">{t('settings.itemsPerPage')}</label>
              <select id="itemsPerPage" class="input" bind:value={itemsPerPage} onchange={handleItemsPerPageChange}>
                <option value={20}>20</option>
                <option value={50}>50</option>
                <option value={100}>100</option>
              </select>
            </div>

            <!-- Auto Refresh -->
            <div>
              <label for="autoRefresh" class="input-label">{t('settings.autoRefresh')}</label>
              <select id="autoRefresh" class="input" bind:value={autoRefresh} onchange={handleAutoRefreshChange}>
                <option value="30s">{t('settings.every30s')}</option>
                <option value="60s">{t('settings.every60s')}</option>
                <option value="120s">{t('settings.every2m')}</option>
                <option value="off">{t('settings.off')}</option>
              </select>
            </div>
          </div>
        </div>

        <!-- Default Protocol Selector -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.streaming.defaultProtocol')}</h3>
          <p class="text-sm th-text-tertiary mb-8">{t('settings.streaming.defaultProtocolHint')}</p>

          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <div>
              <label for="defaultProtocol" class="input-label">{t('settings.streaming.defaultProtocol')}</label>
              <select id="defaultProtocol" class="input" bind:value={streamingDefaultProtocol}>
                <option value="webrtc">WebRTC</option>
                <option value="flv">HTTP-FLV</option>
                <option value="hls">HLS</option>
                <option value="ll-hls">LL-HLS</option>
              </select>
              <p class="text-xs th-text-tertiary mt-1">{t('settings.streaming.defaultProtocolHint')}</p>
            </div>
          </div>
        </div>

        <!-- Protocol Guide -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.protocolDocs')}</h3>
          <p class="text-sm th-text-tertiary mb-6">{t('settings.protocolDocsDesc')}</p>

          <div class="space-y-3">
            {#each ['webrtc', 'flv', 'hls', 'llHls'] as docKey (docKey)}
              {@const isExpanded = expandedProtocolDoc === docKey}
              <div class="border th-border rounded-lg overflow-hidden">
                <button
                  onclick={() => { expandedProtocolDoc = isExpanded ? null : docKey; }}
                  class="w-full px-4 py-3 text-left flex items-center justify-between hover:th-bg-hover transition-colors"
                >
                  <span class="font-medium th-text-primary">{t(`settings.protocolDocs.${docKey}.title`)}</span>
                  <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="transition-transform {isExpanded ? 'rotate-180' : ''} th-text-tertiary"><polyline points="6 9 12 15 18 9"></polyline></svg>
                </button>
                {#if isExpanded}
                  <div class="px-4 pb-4 pt-0 space-y-3">
                    <p class="text-sm th-text-secondary">{t(`settings.protocolDocs.${docKey}.desc`)}</p>
                    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                      <div class="p-3 rounded-md bg-[var(--color-success)]/5 border border-[var(--color-success)]/20">
                        <div class="text-[10px] font-semibold uppercase tracking-wider text-[var(--color-success)] mb-1">Pros</div>
                        <p class="text-xs th-text-secondary">{t(`settings.protocolDocs.${docKey}.pros`)}</p>
                      </div>
                      <div class="p-3 rounded-md bg-[var(--color-danger)]/5 border border-[var(--color-danger)]/20">
                        <div class="text-[10px] font-semibold uppercase tracking-wider text-[var(--color-danger)] mb-1">Cons</div>
                        <p class="text-xs th-text-secondary">{t(`settings.protocolDocs.${docKey}.cons`)}</p>
                      </div>
                    </div>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        </div>
      {:else}
        <!-- Merge Strategy -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('merge.title')}</h3>
          <p class="text-sm th-text-secondary mt-1 mb-3">{t('settings.advanced.merge.description')}</p>

          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <!-- Enable Merge -->
            <div>
              <label class="input-label">{t('merge.enableMerge')}</label>
              <div class="flex items-center gap-3 mt-2">
                <button
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
              <input
                id="mergeMinSegs"
                type="number"
                class="input"
                bind:value={mergeMinSegments}
                min="2"
                max="50"
              />
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
              <input
                id="mergeBatch"
                type="number"
                class="input"
                bind:value={mergeBatchLimit}
                min="10"
                max="1000"
              />
            </div>
          </div>
        </div>

        <!-- WebDAV Settings -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.webdav')}</h3>
          <p class="text-sm th-text-secondary mt-1 mb-3">{t('settings.advanced.webdav.description')}</p>

          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <!-- Enable WebDAV -->
            <div>
              <label class="input-label">{t('settings.webdavEnabled')}</label>
              <div class="flex items-center gap-3 mt-2">
                <button
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
              <input
                id="webdavPrefix"
                type="text"
                class="input"
                bind:value={webdavPathPrefix}
                placeholder="/dav"
              />
            </div>

            <!-- Read-Write Mode -->
            <div>
              <label class="input-label">{t('settings.webdavReadWrite')}</label>
              <div class="flex items-center gap-3 mt-2">
                <button
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
        </div>

        <!-- Streaming Sub-protocol Details -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.streaming')}</h3>
          <p class="text-sm th-text-secondary mt-1 mb-3">{t('settings.advanced.streaming.description')}</p>

          <!-- WebRTC Settings -->
          <div class="mt-2 pt-2">
            <h4 class="text-sm font-semibold th-text-primary mb-1">{t('settings.streaming.webrtc')}</h4>
            <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.webrtcDesc')}</p>
            <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
              <div>
                <label class="input-label">{t('settings.streaming.webrtc')}</label>
                <div class="flex items-center gap-3 mt-2">
                  <button
                    type="button"
                    class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {streamingWebrtcEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
                    onclick={() => { streamingWebrtcEnabled = !streamingWebrtcEnabled; }}
                    onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); streamingWebrtcEnabled = !streamingWebrtcEnabled; } }}
                    role="switch"
                    aria-checked={streamingWebrtcEnabled}
                  >
                    <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {streamingWebrtcEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
                  </button>
                </div>
              </div>
              <div>
                <label for="webrtcMaxViewers" class="input-label">{t('settings.streaming.webrtc.maxViewers')}</label>
                <input id="webrtcMaxViewers" type="number" class="input" bind:value={streamingWebrtcMaxViewers} min="1" max="20" />
              </div>
              <div>
                <label for="webrtcIdleTimeout" class="input-label">{t('settings.streaming.webrtc.idleTimeout')}</label>
                <select id="webrtcIdleTimeout" class="input" bind:value={streamingWebrtcIdleTimeout}>
                  <option value="1m">1 min</option>
                  <option value="5m">5 min</option>
                  <option value="10m">10 min</option>
                  <option value="30m">30 min</option>
                </select>
                <p class="text-xs th-text-tertiary mt-1">{t('settings.streaming.webrtc.idleTimeoutHint')}</p>
              </div>
            </div>
          </div>

          <!-- FLV Settings -->
          <div class="mt-6 pt-6 border-t th-border">
            <h4 class="text-sm font-semibold th-text-primary mb-1">{t('settings.streaming.flv')}</h4>
            <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.flvDesc')}</p>
            <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
              <div>
                <label class="input-label">{t('settings.streaming.flv')}</label>
                <div class="flex items-center gap-3 mt-2">
                  <button
                    type="button"
                    class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {streamingFlvEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
                    onclick={() => { streamingFlvEnabled = !streamingFlvEnabled; }}
                    onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); streamingFlvEnabled = !streamingFlvEnabled; } }}
                    role="switch"
                    aria-checked={streamingFlvEnabled}
                  >
                    <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {streamingFlvEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
                  </button>
                </div>
              </div>
              <div>
                <label for="flvMaxViewers" class="input-label">{t('settings.streaming.flv.maxViewers')}</label>
                <input id="flvMaxViewers" type="number" class="input" bind:value={streamingFlvMaxViewers} min="1" max="50" />
              </div>
            </div>
          </div>

          <!-- HLS Settings -->
          <div class="mt-6 pt-6 border-t th-border">
            <h4 class="text-sm font-semibold th-text-primary mb-1">{t('settings.streaming.hls')}</h4>
            <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.hlsDesc')}</p>
            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
              <div>
                <label class="input-label">{t('settings.streaming.hls.llHls')}</label>
                <div class="flex items-center gap-3 mt-2">
                  <button
                    type="button"
                    class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {streamingHlsLlHls ? 'bg-blue-600' : 'th-bg-tertiary'}"
                    onclick={() => { streamingHlsLlHls = !streamingHlsLlHls; }}
                    onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); streamingHlsLlHls = !streamingHlsLlHls; } }}
                    role="switch"
                    aria-checked={streamingHlsLlHls}
                  >
                    <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {streamingHlsLlHls ? 'translate-x-6' : 'translate-x-1'}"></span>
                  </button>
                </div>
                <p class="text-xs th-text-tertiary mt-1">{t('settings.streaming.hls.llHlsHint')}</p>
              </div>
            </div>
          </div>

          <!-- RTMP Ingest -->
          <div class="mt-6 pt-6 border-t th-border">
            <h4 class="text-sm font-semibold th-text-primary mb-1">{t('settings.streaming.rtmp')}</h4>
            <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.rtmpDesc')}</p>
            <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
              <div>
                <label class="input-label">{t('settings.streaming.rtmp')}</label>
                <div class="flex items-center gap-3 mt-2">
                  <button
                    type="button"
                    class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {streamingRtmpEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
                    onclick={() => { streamingRtmpEnabled = !streamingRtmpEnabled; }}
                    onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); streamingRtmpEnabled = !streamingRtmpEnabled; } }}
                    role="switch"
                    aria-checked={streamingRtmpEnabled}
                  >
                    <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {streamingRtmpEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
                  </button>
                </div>
              </div>
              <div>
                <label for="rtmpPort" class="input-label">{t('settings.streaming.rtmp.port')}</label>
                <input id="rtmpPort" type="number" class="input" bind:value={streamingRtmpPort} min="1" max="65535" />
              </div>
              <div>
                <p class="text-xs th-text-tertiary mt-6">{t('settings.streaming.rtmp.pushHint')}</p>
              </div>
            </div>

            <!-- RTMP Stream Key Mappings (visible when enabled) -->
            {#if streamingRtmpEnabled}
              <div class="mt-4 pt-4 border-t th-border">
                <h5 class="text-sm font-medium th-text-primary mb-1">{t('settings.streaming.rtmp.streamKeys')}</h5>
                <p class="text-xs th-text-tertiary mb-3">{t('settings.streaming.rtmp.streamKeysHint')}</p>
                {#if rtmpStreamKeys.length > 0}
                  <div class="space-y-2">
                    {#each rtmpStreamKeys as entry, i}
                      <div class="flex items-center gap-2">
                        <div class="flex-1 grid grid-cols-2 gap-2">
                          <input type="text" class="input text-sm" placeholder={t('settings.streaming.rtmp.streamKey')} bind:value={entry.key} />
                          <input type="text" class="input text-sm" placeholder={t('settings.streaming.rtmp.cameraId')} bind:value={entry.cameraId} />
                        </div>
                        <button type="button" class="p-1.5 rounded-md th-text-tertiary hover:text-red-400 transition-colors" onclick={() => { rtmpStreamKeys.splice(i, 1); rtmpStreamKeys = [...rtmpStreamKeys]; }} title={t('common.dismiss')}>
                          <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/></svg>
                        </button>
                      </div>
                      {#if entry.key}
                        <p class="text-xs th-text-tertiary">{t('settings.streaming.rtmp.pushUrl')}: <code class="th-bg-tertiary px-1 py-0.5 rounded text-xs">rtmp://host:{streamingRtmpPort}/live/{entry.key}</code></p>
                      {/if}
                    {/each}
                  </div>
                {:else}
                  <p class="text-xs th-text-tertiary italic">{t('settings.streaming.rtmp.noKeys')}</p>
                {/if}
                <button type="button" class="mt-3 text-xs font-medium text-blue-500 hover:text-blue-400 transition-colors" onclick={() => { rtmpStreamKeys = [...rtmpStreamKeys, { key: '', cameraId: '' }]; }}>+ {t('settings.streaming.rtmp.addKey')}</button>
              </div>
            {/if}
          </div>

          <!-- SRT Receiver -->
          <div class="mt-6 pt-6 border-t th-border">
            <h4 class="text-sm font-semibold th-text-primary mb-1">{t('settings.streaming.srt')}</h4>
            <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.srtDesc')}</p>
            <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
              <div>
                <label class="input-label">{t('settings.streaming.srt')}</label>
                <div class="flex items-center gap-3 mt-2">
                  <button
                    type="button"
                    class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {streamingSrtEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
                    onclick={() => { streamingSrtEnabled = !streamingSrtEnabled; }}
                    onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); streamingSrtEnabled = !streamingSrtEnabled; } }}
                    role="switch"
                    aria-checked={streamingSrtEnabled}
                  >
                    <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {streamingSrtEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
                  </button>
                </div>
              </div>
              <div>
                <label for="srtPort" class="input-label">{t('settings.streaming.srt.port')}</label>
                <input id="srtPort" type="number" class="input" bind:value={streamingSrtPort} min="1" max="65535" />
              </div>
              <div>
                <p class="text-xs th-text-tertiary mt-6">{t('settings.streaming.srt.hint')}</p>
              </div>
            </div>

            <!-- SRT Stream Configurations (visible when enabled) -->
            {#if streamingSrtEnabled}
              <div class="mt-4 pt-4 border-t th-border">
                <h5 class="text-sm font-medium th-text-primary mb-1">{t('settings.streaming.srt.streams')}</h5>
                <p class="text-xs th-text-tertiary mb-3">{t('settings.streaming.srt.streamsHint')}</p>
                {#if srtStreams.length > 0}
                  <div class="space-y-3">
                    {#each srtStreams as stream, i}
                      <div class="p-3 rounded-lg th-bg-secondary border th-border">
                        <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
                          <div>
                            <label class="text-xs th-text-tertiary">{t('settings.streaming.srt.streamId')}</label>
                            <input type="text" class="input text-sm mt-1" placeholder="live/my-stream" bind:value={stream.streamId} />
                          </div>
                          <div>
                            <label class="text-xs th-text-tertiary">{t('settings.streaming.srt.cameraId')}</label>
                            <input type="text" class="input text-sm mt-1" placeholder="front-door" bind:value={stream.cameraId} />
                          </div>
                          <div>
                            <label class="text-xs th-text-tertiary">{t('settings.streaming.srt.mode')}</label>
                            <select class="input text-sm mt-1" bind:value={stream.mode}>
                              <option value="listener">{t('settings.streaming.srt.modeListener')}</option>
                              <option value="caller">{t('settings.streaming.srt.modeCaller')}</option>
                            </select>
                          </div>
                          {#if stream.mode === 'caller'}
                            <div>
                              <label class="text-xs th-text-tertiary">{t('settings.streaming.srt.address')}</label>
                              <input type="text" class="input text-sm mt-1" placeholder="192.168.1.100:9000" bind:value={stream.address} />
                            </div>
                          {/if}
                          <div>
                            <label class="text-xs th-text-tertiary">{t('settings.streaming.srt.passphrase')}</label>
                            <input type="password" class="input text-sm mt-1" placeholder="......" bind:value={stream.passphrase} />
                          </div>
                        </div>
                        <div class="flex justify-end mt-2">
                          <button type="button" class="text-xs th-text-tertiary hover:text-red-400 transition-colors" onclick={() => { srtStreams.splice(i, 1); srtStreams = [...srtStreams]; }}>{t('common.dismiss')}</button>
                        </div>
                      </div>
                    {/each}
                  </div>
                {:else}
                  <p class="text-xs th-text-tertiary italic">{t('settings.streaming.srt.noStreams')}</p>
                {/if}
                <button type="button" class="mt-3 text-xs font-medium text-blue-500 hover:text-blue-400 transition-colors" onclick={() => { srtStreams = [...srtStreams, { streamId: '', cameraId: '', mode: 'listener', address: '', passphrase: '' }]; }}>+ {t('settings.streaming.srt.addStream')}</button>
              </div>
            {/if}
          </div>

          <!-- Resource Usage Estimates -->
          <div class="mt-6 pt-6 border-t th-border">
            <h4 class="text-sm font-semibold th-text-primary mb-1">{t('settings.streaming.resourceEstimate')}</h4>
            <p class="text-xs th-text-tertiary mb-3">{t('settings.streaming.resourceEstimateDesc')}</p>
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
          </div>

        </div>

        <!-- Feature Toggles -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.featureToggles.title')}</h3>
          <p class="text-sm th-text-secondary mt-1 mb-3">{t('settings.advanced.features.description')}</p>

          {#if featuresLoading}
            <div class="flex items-center gap-2 py-4 th-text-muted">
              <span class="spinner"></span>
              <span class="text-sm">{t('common.loading')}</span>
            </div>
          {:else}
            <div class="space-y-4">
              {#each Object.entries(featureFlags) as [protocol, enabled] (protocol)}
                <div class="p-4 rounded-md th-bg-hover border th-border">
                  <div class="flex items-center justify-between">
                    <div class="min-w-0 flex-1">
                      <div class="font-medium th-text-primary">{t(`settings.featureToggles.protocols.${protocol}`)}</div>
                      {#if !enabled}
                        <div class="flex items-center gap-1 mt-1 text-xs th-color-warning">
                          <AlertTriangle size={12} />
                          <span>{t('settings.featureToggles.warning')}{#if getAffectedCameraCount(protocol) > 0} <span class="th-color-danger">({getAffectedCameraCount(protocol)} {t('cameras.title').toLowerCase()})</span>{/if}</span>
                        </div>
                      {/if}
                    </div>
                    <div class="flex items-center gap-3">
                      <button
                        type="button"
                        class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {enabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
                        onclick={() => { featureFlags[protocol] = !featureFlags[protocol]; featureFlags = featureFlags; }}
                        onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); featureFlags[protocol] = !featureFlags[protocol]; featureFlags = featureFlags; } }}
                        role="switch"
                        aria-checked={enabled}
                      >
                        <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {enabled ? 'translate-x-6' : 'translate-x-1'}"></span>
                      </button>
                    </div>
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
        <!-- Save button -->
        <div class="flex items-center gap-4 pt-2">
          <button
            onclick={save}
            class="btn btn-primary"
            disabled={saving || !isDirty()}
          >
            {#if saving}
              <span class="spinner mr-2"></span>
              {t('settings.saving')}
            {:else}
              {t('settings.save')}
            {/if}
          </button>
        </div>
      </div>
    {/if}
  </main>


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
</div>