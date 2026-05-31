<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings, getMergeSettings, updateMergeSettings, getFeatures, updateFeatures, getStats, listCameras, getStreamingSettings, updateStreamingSettings } from '$lib/api';
  import { getTranscodingCheck, getTranscodingStatus, getFFmpegStatus, downloadFFmpeg, retryDownload, getTranscodingSettings, updateTranscodingSettings } from '$lib/api/transcoding';
  import type { SelfCheckResult, DownloadStatus, HardwareCapabilities, ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import type { SettingsConfig, FeatureFlags, StorageStats, Camera, StreamingConfig } from '$lib/api';
  import { getItemsPerPage, setItemsPerPage, getAutoRefresh, setAutoRefresh } from '../lib/preferences';
  import { t } from '$lib/i18n';
  import { AlertCircle, Settings as SettingsIcon, RefreshCw, CircleDot, Download, Cpu, ChevronDown, ChevronUp, RotateCw } from 'lucide-svelte';
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

// Transcoding state
let transcodingEnabled = $state(false);
let transcodingMaxWorkers = $state(1);
let transcodingReplaceOriginal = $state(false);
let transcodingCheck = $state<SelfCheckResult | null>(null);
let ffmpegStatus = $state<DownloadStatus>({ status: 'not_installed', progress: 0, version: '', error: '', total_bytes: 0, downloaded_bytes: 0 });
let ffmpegDownloading = $state(false);
let hardwareInfo = $state<HardwareCapabilities | null>(null);
let checkingTranscoding = $state(false);
let showHardwareInfo = $state(false);
let ffmpegPollInterval = $state<ReturnType<typeof setInterval> | null>(null);
let transcodingCheckError = $state('');
let downloadStartTime = $state<number | null>(null);

// Derived download speed (bytes/s) and ETA (seconds)
let downloadInfo = $derived.by(() => {
  if (ffmpegStatus.status !== 'downloading' || !downloadStartTime || ffmpegStatus.downloaded_bytes <= 0) {
    return { speed: 0, eta: 0 };
  }
  const elapsed = (Date.now() - downloadStartTime) / 1000;
  if (elapsed <= 0) return { speed: 0, eta: 0 };
  const speed = ffmpegStatus.downloaded_bytes / elapsed;
  const remaining = ffmpegStatus.total_bytes - ffmpegStatus.downloaded_bytes;
  const eta = speed > 0 ? remaining / speed : 0;
  return { speed, eta };
});

function formatSpeed(bytesPerSec: number): string {
  if (bytesPerSec >= 1_048_576) {
    return (bytesPerSec / 1_048_576).toFixed(1) + ' MB/s';
  }
  return Math.round(bytesPerSec / 1024) + ' KB/s';
}

function formatEta(seconds: number): string {
  if (seconds >= 60) {
    const m = Math.floor(seconds / 60);
    const s = Math.round(seconds % 60);
    return m + 'm ' + s + 's';
  }
  return Math.round(seconds) + 's';
}

// Transcoding queue status state
let managerStatus = $state<ManagerStatus | null>(null);
let queuePollInterval = $state<ReturnType<typeof setInterval> | null>(null);

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

      // Load transcoding settings
      try {
        const transcodingCfg = await getTranscodingSettings();
        transcodingEnabled = transcodingCfg.enabled;
        transcodingMaxWorkers = transcodingCfg.max_workers || 1;
        transcodingReplaceOriginal = transcodingCfg.replace_original || false;
        if (transcodingEnabled) {
          // Load hardware info + FFmpeg status + queue polling
          try {
            const checkResult = await getTranscodingCheck();
            transcodingCheck = checkResult;
            hardwareInfo = {
              h264_encoder: checkResult.encoders.h264 || '',
              h265_encoder: checkResult.encoders.h265 || '',
              total_cores: checkResult.total_cores,
              total_memory_mb: checkResult.total_memory_mb,
              estimated_fps: checkResult.estimated_fps,
              max_concurrent_streams: checkResult.max_concurrent,
              h264_encoder_type: checkResult.h264_encoder_type,
              h265_encoder_type: checkResult.h265_encoder_type,
              devices: checkResult.devices,
              arch: '',
              ffmpeg_available: checkResult.supported,
            };
          } catch (e) {
            console.warn('Failed to load transcoding hardware info:', e);
          }
          refreshFfmpegStatus();
          startQueuePolling();
        }
      } catch (e) {
        console.warn('Failed to load transcoding settings:', e);
      }

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
    stopFfmpegPolling();
    stopQueuePolling();
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

  // --- Transcoding ---

  async function handleTranscodingToggle() {
    if (transcodingEnabled) {
      // Disabling — persist to backend, no self-check needed
      try {
        await updateTranscodingSettings({ enabled: false });
        transcodingEnabled = false;
        stopFfmpegPolling();
        stopQueuePolling();
        managerStatus = null;
      } catch (e) {
        showToast(e instanceof Error ? e.message : 'Failed to disable transcoding', 'error');
      }
      return;
    }

    // Enabling — run self-check first
    checkingTranscoding = true;
    transcodingCheckError = '';
    try {
      const result = await getTranscodingCheck();
      transcodingCheck = result;
      // Build proper HardwareCapabilities from flat API response
      const hw: HardwareCapabilities = {
        h264_encoder: result.encoders.h264 || '',
        h265_encoder: result.encoders.h265 || '',
        total_cores: result.total_cores,
        total_memory_mb: result.total_memory_mb,
        estimated_fps: result.estimated_fps,
        max_concurrent_streams: result.max_concurrent,
        h264_encoder_type: result.h264_encoder_type,
        h265_encoder_type: result.h265_encoder_type,
        devices: result.devices,
        arch: '',
        ffmpeg_available: result.supported,
      };
      if (result.supported) {
        hardwareInfo = hw;
        // Persist enabled=true to backend
        await updateTranscodingSettings({ enabled: true, max_workers: transcodingMaxWorkers, replace_original: transcodingReplaceOriginal });
        transcodingEnabled = true;
        showToast(t('transcoding.self_check_passed') + ' — ' + t('transcoding.restart_required'), 'success');
        // Load current FFmpeg status
        await refreshFfmpegStatus();
        // Start queue polling
        startQueuePolling();
      } else {
        hardwareInfo = hw;
        transcodingEnabled = false;
        const warnings = result.warnings?.length ? result.warnings.join('; ') : t('transcoding.self_check_failed');
        transcodingCheckError = warnings;
        showToast(t('transcoding.self_check_failed'), 'error');
      }
    } catch (e) {
      transcodingEnabled = false;
      transcodingCheckError = e instanceof Error ? e.message : t('transcoding.self_check_failed');
      showToast(transcodingCheckError, 'error');
    } finally {
      checkingTranscoding = false;
    }
  }

  async function refreshFfmpegStatus() {
    try {
      const status = await getFFmpegStatus();
      ffmpegStatus = status;
      if (status.status === 'downloading') {
        ffmpegDownloading = true;
        if (downloadStartTime === null) {
          downloadStartTime = Date.now();
        }
        startFfmpegPolling();
      } else {
        ffmpegDownloading = false;
        downloadStartTime = null;
        stopFfmpegPolling();
      }
    } catch (e) {
      console.warn('Failed to get FFmpeg status:', e);
    }
  }

  function startFfmpegPolling() {
    stopFfmpegPolling();
    ffmpegPollInterval = setInterval(async () => {
      try {
        const status = await getFFmpegStatus();
        ffmpegStatus = status;
        if (status.status !== 'downloading') {
          ffmpegDownloading = false;
          downloadStartTime = null;
          stopFfmpegPolling();
          if (status.status === 'available') {
            showToast(t('transcoding.ffmpeg_available'), 'success');
          } else if (status.status === 'failed') {
            showToast(t('transcoding.ffmpeg_failed'), 'error');
          }
        }
      } catch (e) {
        stopFfmpegPolling();
        ffmpegDownloading = false;
        downloadStartTime = null;
      }
    }, 1000);
  }

  function stopFfmpegPolling() {
    if (ffmpegPollInterval) {
      clearInterval(ffmpegPollInterval);
      ffmpegPollInterval = null;
    }
  }

  async function handleDownloadFFmpeg() {
    ffmpegDownloading = true;
    ffmpegStatus = { ...ffmpegStatus, status: 'downloading', progress: 0, error: '' };
    downloadStartTime = Date.now();
    try {
      await downloadFFmpeg();
      startFfmpegPolling();
    } catch (e) {
      ffmpegDownloading = false;
      ffmpegStatus = { ...ffmpegStatus, status: 'failed', error: e instanceof Error ? e.message : 'Download failed' };
      showToast(t('transcoding.ffmpeg_failed'), 'error');
    }
  }

  async function handleRetryDownload() {
    ffmpegDownloading = true;
    ffmpegStatus = { ...ffmpegStatus, status: 'downloading', progress: 0, error: '' };
    downloadStartTime = Date.now();
    try {
      await retryDownload();
      startFfmpegPolling();
    } catch (e) {
      ffmpegDownloading = false;
      ffmpegStatus = { ...ffmpegStatus, status: 'failed', error: e instanceof Error ? e.message : 'Download failed' };
      showToast(t('transcoding.ffmpeg_failed'), 'error');
    }
  }

  // --- Transcoding Queue Status Polling ---

  function startQueuePolling() {
    stopQueuePolling();
    loadQueueStatus();
    queuePollInterval = setInterval(loadQueueStatus, 5000);
  }

  function stopQueuePolling() {
    if (queuePollInterval) {
      clearInterval(queuePollInterval);
      queuePollInterval = null;
    }
  }

  async function loadQueueStatus() {
    try {
      managerStatus = await getTranscodingStatus();
    } catch (e) {
      console.warn('Failed to load transcoding status:', e);
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

        <!-- Transcoding -->
        <div class="card p-8 border th-border">
          <div class="flex items-center justify-between mb-1">
            <div>
              <h3 class="text-lg font-semibold th-text-primary">{t('transcoding.title')}</h3>
              <p class="text-sm th-text-secondary mt-1">{t('transcoding.description')}</p>
            </div>
            <button
              type="button"
              class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {transcodingEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
              onclick={handleTranscodingToggle}
              onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTranscodingToggle(); } }}
              role="switch"
              aria-checked={transcodingEnabled}
              disabled={checkingTranscoding}
            >
              {#if checkingTranscoding}
                <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform translate-x-1">
                  <span class="spinner !w-4 !h-4 !border-2"></span>
                </span>
              {:else}
                <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {transcodingEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
              {/if}
            </button>
          </div>

          <!-- Self-check error -->
          {#if transcodingCheckError}
            <div class="mt-3 p-3 rounded-md bg-[var(--color-danger)]/10 border border-[var(--color-danger)]/30">
              <div class="flex items-center gap-2 text-sm text-[var(--color-danger-light)]">
                <AlertCircle size={16} />
                <span>{transcodingCheckError}</span>
              </div>
            </div>
          {/if}

          <!-- Self-check passed indicator -->
          {#if transcodingEnabled && transcodingCheck?.supported}
            <div class="mt-3 p-3 rounded-md bg-[var(--color-success)]/10 border border-[var(--color-success)]/30">
              <div class="flex items-center gap-2 text-sm text-[var(--color-success-light)]">
                <svg class="w-4 h-4" viewBox="0 0 20 20" fill="currentColor"><path fill-rule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clip-rule="evenodd"/></svg>
                <span>{t('transcoding.self_check_passed')}</span>
              </div>
            </div>
          {/if}

          <!-- FFmpeg Status Panel -->
          {#if transcodingEnabled}
            <div class="mt-4 pt-4 border-t th-border">
              <h4 class="text-sm font-semibold th-text-primary mb-3">{t('transcoding.ffmpeg_status')}</h4>

              <div class="p-4 rounded-md th-bg-hover border th-border">
                <!-- Status indicator -->
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-2">
                    {#if ffmpegStatus.status === 'available'}
                      <span class="w-2.5 h-2.5 rounded-full bg-[var(--color-success)]"></span>
                      <span class="text-sm th-text-primary">{t('transcoding.ffmpeg_available')}</span>
                      {#if ffmpegStatus.version}
                        <span class="text-xs th-text-secondary">{t('transcoding.ffmpeg_version', { version: ffmpegStatus.version })}</span>
                      {/if}
                    {:else if ffmpegStatus.status === 'downloading'}
                      <span class="w-2.5 h-2.5 rounded-full bg-[var(--color-info)] animate-pulse"></span>
                      <span class="text-sm th-text-primary">{t('transcoding.ffmpeg_downloading')}</span>
                    {:else if ffmpegStatus.status === 'failed'}
                      <span class="w-2.5 h-2.5 rounded-full bg-[var(--color-danger)]"></span>
                      <span class="text-sm th-text-primary">{t('transcoding.ffmpeg_failed')}</span>
                    {:else}
                      <span class="w-2.5 h-2.5 rounded-full bg-[var(--color-warning)]"></span>
                      <span class="text-sm th-text-primary">{t('transcoding.ffmpeg_not_installed')}</span>
                    {/if}
                  </div>

                  <!-- Action button -->
                  <div>
                    {#if ffmpegStatus.status === 'not_installed'}
                      <button
                        type="button"
                        class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-md bg-[var(--color-info)] text-white hover:opacity-90 transition-opacity"
                        onclick={handleDownloadFFmpeg}
                      >
                        <Download size={12} />
                        {t('transcoding.ffmpeg_download')}
                      </button>
                    {:else if ffmpegStatus.status === 'failed'}
                      <button
                        type="button"
                        class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-md bg-[var(--color-warning)] text-white hover:opacity-90 transition-opacity"
                        onclick={handleRetryDownload}
                      >
                        <RotateCw size={12} />
                        {t('transcoding.ffmpeg_retry')}
                      </button>
                    {:else if ffmpegStatus.status === 'available'}
                      <!-- no action needed -->
                    {:else}
                      <!-- downloading in progress -->
                    {/if}
                  </div>
                </div>

                <!-- Progress bar (downloading) -->
                {#if ffmpegDownloading || ffmpegStatus.status === 'downloading'}
                  <div class="mt-3">
                    <div class="flex items-center justify-between text-xs th-text-secondary mb-1">
                      <span>{t('transcoding.download_progress')}</span>
                      <span>{ffmpegStatus.progress}%</span>
                    </div>
                    <div class="w-full h-2 rounded-full th-bg-tertiary overflow-hidden">
                      <div
                        class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500"
                        style="width: {Math.max(ffmpegStatus.progress, 2)}%"
                      ></div>
                    </div>
                  </div>

                  <!-- Download speed + ETA -->
                  <div class="flex items-center gap-3 mt-2 text-xs th-text-secondary">
                    {#if downloadInfo.speed > 0}
                      <span>{t('transcoding.download_speed')}: {formatSpeed(downloadInfo.speed)}</span>
                    {/if}
                    {#if downloadInfo.eta > 0}
                      <span>{t('transcoding.download_eta')}: ~{formatEta(downloadInfo.eta)}</span>
                    {/if}
                  </div>
                {/if}

                <!-- Error detail -->
                {#if ffmpegStatus.status === 'failed' && ffmpegStatus.error}
                  <div class="mt-2 text-xs text-[var(--color-danger-light)]">{ffmpegStatus.error}</div>
                {/if}
              </div>

              <!-- Hardware Info Card -->
              {#if hardwareInfo}
                <button
                  type="button"
                  class="mt-3 flex items-center gap-1.5 text-sm font-medium th-text-secondary hover:th-text-primary transition-colors"
                  onclick={() => showHardwareInfo = !showHardwareInfo}
                >
                  <Cpu size={14} />
                  <span>{t('transcoding.hardware_info')}</span>
                  {#if showHardwareInfo}
                    <ChevronUp size={14} />
                  {:else}
                    <ChevronDown size={14} />
                  {/if}
                </button>

                <div class="mt-2 overflow-hidden transition-all duration-200 {showHardwareInfo ? 'max-h-[500px] opacity-100' : 'max-h-0 opacity-0'}" >
                  <div class="p-3 rounded-md th-bg-hover border th-border grid grid-cols-2 gap-3">
                    <div>
                      <div class="text-xs th-text-secondary">{t('transcoding.cpu_cores')}</div>
                      <div class="text-sm font-medium th-text-primary">{hardwareInfo.total_cores}</div>
                    </div>
                    <div>
                      <div class="text-xs th-text-secondary">{t('transcoding.memory')}</div>
                      <div class="text-sm font-medium th-text-primary">{Math.round(hardwareInfo.total_memory_mb)} MB</div>
                    </div>
                    <div>
                      <div class="text-xs th-text-secondary">{t('transcoding.encoder')}</div>
                      <div class="text-sm font-medium th-text-primary">{hardwareInfo.h264_encoder || 'software'}</div>
                    </div>
                    <div>
                      <div class="text-xs th-text-secondary">{t('transcoding.estimated_fps')}</div>
                      <div class="text-sm font-medium th-text-primary">{hardwareInfo.estimated_fps} FPS</div>
                    </div>
                    <div>
                      <div class="text-xs th-text-secondary">{t('transcoding.max_concurrent')}</div>
                      <div class="text-sm font-medium th-text-primary">{hardwareInfo.max_concurrent_streams}</div>
                    </div>
                  </div>

                  {#if hardwareInfo.estimated_fps < 15}
                    <div class="mt-2 p-2 rounded-md bg-[var(--color-warning)]/10 border border-[var(--color-warning)]/30">
                      <div class="flex items-center gap-1.5 text-xs text-[var(--color-warning-light)]">
                        <AlertTriangle size={12} />
                        <span>{t('transcoding.warning_hardware')}</span>
                      </div>
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          {/if}

          <!-- Transcoding Options -->
          {#if transcodingEnabled && ffmpegStatus.status === 'available'}
            <div class="mt-4 pt-4 border-t th-border">
              <h4 class="text-sm font-semibold th-text-primary mb-3">{t('transcoding.options')}</h4>

              <div class="space-y-3">
                <!-- Max Workers -->
                <div class="flex items-center justify-between">
                  <div>
                    <div class="text-sm th-text-primary">{t('transcoding.max_workers')}</div>
                    <div class="text-xs th-text-secondary">{t('transcoding.max_workers_desc')}</div>
                  </div>
                  <select
                    class="input w-20 text-center"
                    bind:value={transcodingMaxWorkers}
                    onchange={async () => { await updateTranscodingSettings({ enabled: true, max_workers: transcodingMaxWorkers, replace_original: transcodingReplaceOriginal }); showToast(t('common.saved'), 'success'); }}
                  >
                    <option value={1}>1</option>
                    <option value={2}>2</option>
                    <option value={3}>3</option>
                    <option value={4}>4</option>
                  </select>
                </div>

                <!-- Replace Original -->
                <div class="flex items-center justify-between">
                  <div>
                    <div class="text-sm th-text-primary">{t('transcoding.replace_original')}</div>
                    <div class="text-xs th-text-secondary">{t('transcoding.replace_original_desc')}</div>
                  </div>
                  <button
                    type="button"
                    class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors {transcodingReplaceOriginal ? 'bg-blue-600' : 'th-bg-tertiary'}"
                    onclick={async () => {
                      transcodingReplaceOriginal = !transcodingReplaceOriginal;
                      await updateTranscodingSettings({ enabled: true, max_workers: transcodingMaxWorkers, replace_original: transcodingReplaceOriginal });
                      showToast(t('common.saved'), 'success');
                    }}
                    role="switch"
                    aria-checked={transcodingReplaceOriginal}
                  >
                    <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {transcodingReplaceOriginal ? 'translate-x-6' : 'translate-x-1'}"></span>
                  </button>
                </div>
              </div>
            </div>
          {/if}

          <!-- Queue Status (when enabled) -->
          {#if transcodingEnabled && ffmpegStatus.status === 'available'}
            <div class="mt-4 pt-4 border-t th-border">
              <h4 class="text-sm font-semibold th-text-primary mb-3">{t('transcoding.queue_status')}</h4>

              {#if managerStatus}
                <!-- Active Jobs -->
                <div class="space-y-3">
                  {#each managerStatus.recent_results?.filter((t: TranscodeTask) => t.status === 'running') ?? [] as job}
                    <div class="p-3 rounded-md th-bg-hover border th-border">
                      <div class="flex items-center justify-between mb-2">
                        <div class="flex items-center gap-2">
                          <span class="w-2 h-2 rounded-full bg-[var(--color-info)] animate-pulse"></span>
                          <span class="text-sm font-medium th-text-primary">{job.camera_id}</span>
                        </div>
                        <span class="text-xs th-text-secondary">{t('transcoding.queue.codecConversion', { input: job.input_format?.toUpperCase() || '?', output: job.output_format?.toUpperCase() || '?' })}</span>
                      </div>
                      <div class="w-full h-2 rounded-full th-bg-tertiary overflow-hidden">
                        <div
                          class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500"
                          style="width: {Math.max(job.progress || 0, 2)}%"
                        ></div>
                      </div>
                      <div class="flex items-center justify-between mt-1">
                        <span class="text-xs th-text-secondary">{t('transcoding.progress')}</span>
                        <span class="text-xs font-medium th-text-primary">{job.progress || 0}%</span>
                      </div>
                    </div>
                  {/each}

                  {#if (managerStatus.recent_results?.filter((t: TranscodeTask) => t.status === 'running') ?? []).length === 0}
                    <div class="text-sm th-text-tertiary text-center py-2">{t('transcoding.queue.noActive')}</div>
                  {/if}
                </div>

                <!-- Queue Summary -->
                <div class="mt-3 grid grid-cols-3 gap-3">
                  <div class="p-3 rounded-md th-bg-hover border th-border text-center">
                    <div class="text-lg font-semibold th-text-primary">{managerStatus.queue_length || 0}</div>
                    <div class="text-xs th-text-secondary">{t('transcoding.pending_jobs')}</div>
                  </div>
                  <div class="p-3 rounded-md th-bg-hover border th-border text-center">
                    <div class="text-lg font-semibold th-text-primary">{managerStatus.active_jobs || 0}</div>
                    <div class="text-xs th-text-secondary">{t('transcoding.active_jobs')}</div>
                  </div>
                  <div class="p-3 rounded-md th-bg-hover border th-border text-center">
                    <div class="text-lg font-semibold th-text-primary">{managerStatus.recent_results?.filter((t: TranscodeTask) => t.status === 'completed').length ?? 0}<span class="text-xs th-color-danger ml-1">{managerStatus.recent_results?.filter((t: TranscodeTask) => t.status === 'failed').length ?? 0}✗</span></div>
                    <div class="text-xs th-text-secondary">{t('transcoding.recent_results')}</div>
                  </div>
                </div>

                <!-- Recent Results -->
                {#if managerStatus.recent_results && managerStatus.recent_results.length > 0}
                  <div class="mt-3 space-y-1.5">
                    {#each managerStatus.recent_results.slice(0, 5) as task}
                      <div class="py-1 px-2 rounded th-bg-hover">
                        <div class="flex items-center justify-between text-xs">
                          <div class="flex items-center gap-2">
                            {#if task.status === 'completed'}
                              <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success)]"></span>
                            {:else if task.status === 'failed'}
                              <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-danger)]"></span>
                            {:else if task.status === 'running'}
                              <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-info)] animate-pulse"></span>
                            {:else}
                              <span class="w-1.5 h-1.5 rounded-full th-bg-tertiary"></span>
                            {/if}
                            <span class="th-text-primary">{task.camera_id}</span>
                            <span class="th-text-tertiary">{t('transcoding.queue.codecConversion', { input: task.input_format?.toUpperCase() || '?', output: task.output_format?.toUpperCase() || '?' })}</span>
                          </div>
                          <div class="flex items-center gap-2">
                            {#if task.status === 'completed'}
                              <span class="text-[var(--color-success)]">{task.progress}%</span>
                            {:else if task.status === 'running'}
                              <span class="text-[var(--color-info)]">{task.progress}%</span>
                            {:else if task.status === 'failed'}
                              <span class="text-[var(--color-danger)]">{t('transcoding.failed')}</span>
                            {:else}
                              <span class="th-text-tertiary">{t(`transcoding.${task.status}`) || task.status}</span>
                            {/if}
                          </div>
                        </div>
                        {#if task.status === 'failed' && task.error}
                          <details class="mt-1 group">
                            <summary class="flex items-center gap-1 cursor-pointer text-[10px] th-color-danger select-none">
                              <span>{t('transcoding.error_details')}</span>
                              <span class="th-text-tertiary group-open:rotate-180 transition-transform">▼</span>
                            </summary>
                            <pre class="mt-0.5 p-1.5 rounded text-[10px] th-bg-tertiary th-text-secondary whitespace-pre-wrap break-all max-h-24 overflow-y-auto">{task.error}</pre>
                          </details>
                        {/if}
                      </div>
                    {/each}
                  </div>
                {:else}
                  <div class="mt-3 text-xs th-text-tertiary text-center">{t('transcoding.queue.noRecent')}</div>
                {/if}
              {:else}
                <div class="text-sm th-text-tertiary text-center py-2">{t('common.loading')}</div>
              {/if}
            </div>
          {/if}
        </div>
      {/if}
        <!-- Save button (visible on both tabs) -->
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