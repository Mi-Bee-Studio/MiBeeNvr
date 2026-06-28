<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getTranscodingCheck, getTranscodingStatus, getFFmpegStatus, downloadFFmpeg, retryDownload, getTranscodingSettings, updateTranscodingSettings } from '$lib/api/transcoding';
  import type { SelfCheckResult, DownloadStatus, HardwareCapabilities, ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import { t } from '$lib/i18n';
  import { AlertCircle, AlertTriangle, Download, RotateCw, Cpu, ChevronDown, ChevronUp, XCircle } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  const FFMPEG_DOWNLOAD_TIMEOUT = 5 * 60 * 1000; // 5 minutes in ms

  // Transcoding state
  let transcodingEnabled = $state(false);
  let transcodingMaxWorkers = $state(1);
  let transcodingCheck = $state<SelfCheckResult | null>(null);
  let ffmpegStatus = $state<DownloadStatus>({ status: 'not_installed', progress: 0, version: '', error: '', total_bytes: 0, downloaded_bytes: 0 });
  let ffmpegDownloading = $state(false);
  let hardwareInfo = $state<HardwareCapabilities | null>(null);
  let checkingTranscoding = $state(false);
  let showHardwareInfo = $state(false);
  let ffmpegPollInterval = $state<ReturnType<typeof setInterval> | null>(null);
  let transcodingCheckError = $state('');
  let downloadStartTime = $state<number | null>(null);
  let downloadPollStartTime = $state<number | null>(null);

  // Transcoding queue status state
  let managerStatus = $state<ManagerStatus | null>(null);
  let queuePollInterval = $state<ReturnType<typeof setInterval> | null>(null);

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

  // Display state machine: derive current UI state from API responses
  let displayState = $derived.by(() => {
    if (ffmpegStatus.status === 'downloading') return 'downloading';
    if (ffmpegStatus.status === 'failed') return 'failed';
    if (transcodingEnabled && ffmpegStatus.status === 'available') return 'enabled';
    if (ffmpegStatus.status === 'available') return 'available';
    return 'not_installed';
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

  async function loadTranscodingSettings() {
    try {
      const transcodingCfg = await getTranscodingSettings();
      transcodingEnabled = transcodingCfg.enabled;
      transcodingMaxWorkers = transcodingCfg.max_workers || 1;
      if (transcodingEnabled) {
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
  }

  async function handleEnable() {
    checkingTranscoding = true;
    transcodingCheckError = '';
    try {
      const result = await getTranscodingCheck();
      transcodingCheck = result;
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
        await updateTranscodingSettings({ enabled: true, max_workers: transcodingMaxWorkers });
        transcodingEnabled = true;
        showToast(t('transcoding.self_check_passed') + ' — ' + t('transcoding.restart_required'), 'success');
        await refreshFfmpegStatus();
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

  async function handleDisable() {
    try {
      await updateTranscodingSettings({ enabled: false });
      transcodingEnabled = false;
      stopFfmpegPolling();
      stopQueuePolling();
      managerStatus = null;
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('transcoding.disable_failed'), 'error');
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
    downloadPollStartTime = Date.now();
    ffmpegPollInterval = setInterval(async () => {
      try {
        const status = await getFFmpegStatus();
        ffmpegStatus = status;
        if (status.status !== 'downloading') {
          ffmpegDownloading = false;
          downloadStartTime = null;
          downloadPollStartTime = null;
          stopFfmpegPolling();
          if (status.status === 'available') {
            showToast(t('transcoding.ffmpeg_available'), 'success');
          } else if (status.status === 'failed') {
            showToast(t('transcoding.ffmpeg_failed'), 'error');
          }
        } else {
          // Check for download timeout
          if (downloadPollStartTime && (Date.now() - downloadPollStartTime) >= FFMPEG_DOWNLOAD_TIMEOUT) {
            downloadPollStartTime = null;
            ffmpegDownloading = false;
            downloadStartTime = null;
            stopFfmpegPolling();
            ffmpegStatus = { ...ffmpegStatus, status: 'failed', error: t('transcoding.timeout') };
            showToast(t('transcoding.timeout'), 'error');
          }
        }
      } catch (e) {
        stopFfmpegPolling();
        ffmpegDownloading = false;
        downloadStartTime = null;
        downloadPollStartTime = null;
        ffmpegStatus = { ...ffmpegStatus, status: 'failed', error: t('transcoding.network_error') };
        showToast(t('transcoding.network_error'), 'error');
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
    downloadPollStartTime = Date.now();
    try {
      await downloadFFmpeg();
      startFfmpegPolling();
    } catch (e) {
      ffmpegDownloading = false;
      ffmpegStatus = { ...ffmpegStatus, status: 'failed', error: e instanceof Error ? e.message : t('transcoding.download_failed') };
      showToast(t('transcoding.ffmpeg_failed'), 'error');
    }
  }

  async function handleRetryDownload() {
    ffmpegDownloading = true;
    ffmpegStatus = { ...ffmpegStatus, status: 'downloading', progress: 0, error: '' };
    downloadStartTime = Date.now();
    downloadPollStartTime = Date.now();
    try {
      await retryDownload();
      startFfmpegPolling();
    } catch (e) {
      ffmpegDownloading = false;
      ffmpegStatus = { ...ffmpegStatus, status: 'failed', error: e instanceof Error ? e.message : t('transcoding.download_failed') };
      showToast(t('transcoding.ffmpeg_failed'), 'error');
    }
  }

  function handleCancelDownload() {
    stopFfmpegPolling();
    ffmpegDownloading = false;
    downloadStartTime = null;
    ffmpegStatus = { ...ffmpegStatus, status: 'not_installed', progress: 0, error: '' };
  }

  // Transcoding Queue Status Polling
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

  onMount(() => {
    loadTranscodingSettings();
  });

  onDestroy(() => {
    stopFfmpegPolling();
    stopQueuePolling();
  });
</script>

{#if displayState === 'not_installed'}
  <!-- State: Not Installed — FFmpeg not available, prompt download -->
  <div class="card p-8 border th-border">
    <div class="flex items-center justify-between mb-1">
      <div>
        <h3 class="text-lg font-semibold th-text-primary">{t('transcoding.title')}</h3>
        <p class="text-sm th-text-secondary mt-1">{t('transcoding.description')}</p>
      </div>
      <span class="px-2 py-0.5 text-xs font-medium rounded-full bg-[var(--color-danger)]/10 text-[var(--color-danger)]">
        {t('transcoding.state.not_installed')}
      </span>
    </div>

    <p class="mt-4 text-sm th-text-secondary">{t('transcoding.info.download_hint')}</p>

    <div class="mt-4">
      <button
        type="button"
        class="w-full inline-flex items-center justify-center gap-2 px-4 py-2.5 text-sm font-medium rounded-lg bg-[var(--color-info)] text-white hover:opacity-90 transition-opacity"
        onclick={handleDownloadFFmpeg}
      >
        <Download size={16} />
        {t('transcoding.action.download')}
      </button>
    </div>

    <!-- Hardware info collapsible (collapsed by default) -->
    {#if hardwareInfo}
      <button
        type="button"
        class="mt-4 flex items-center gap-1.5 text-sm font-medium th-text-secondary hover:th-text-primary transition-colors"
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
      <div class="mt-2 overflow-hidden transition-all duration-200 {showHardwareInfo ? 'max-h-[500px] opacity-100' : 'max-h-0 opacity-0'}">
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

{:else if displayState === 'downloading'}
  <!-- State: Downloading — FFmpeg download in progress with progress bar -->
  <div class="card p-8 border th-border">
    <div class="flex items-center justify-between mb-1">
      <div>
        <h3 class="text-lg font-semibold th-text-primary">{t('transcoding.title')}</h3>
        <p class="text-sm th-text-secondary mt-1">{t('transcoding.description')}</p>
      </div>
      <span class="px-2 py-0.5 text-xs font-medium rounded-full bg-[var(--color-info)]/10 text-[var(--color-info)]">
        {t('transcoding.state.downloading')}
      </span>
    </div>

    <p class="mt-4 text-sm th-text-secondary">{t('transcoding.info.downloading_hint')}</p>

    <!-- Progress bar -->
    <div class="mt-4">
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

    <!-- Cancel button (one primary action) -->
    <div class="mt-4">
      <button
        type="button"
        class="w-full inline-flex items-center justify-center gap-2 px-4 py-2.5 text-sm font-medium rounded-lg border th-border th-text-primary th-bg-hover hover:opacity-80 transition-opacity"
        onclick={handleCancelDownload}
      >
        <XCircle size={16} />
        {t('transcoding.action.cancel')}
      </button>
    </div>
  </div>

{:else if displayState === 'available'}
  <!-- State: Available — FFmpeg ready, one-click to enable transcoding -->
  <div class="card p-8 border th-border">
    <div class="flex items-center justify-between mb-1">
      <div>
        <h3 class="text-lg font-semibold th-text-primary">{t('transcoding.title')}</h3>
        <p class="text-sm th-text-secondary mt-1">{t('transcoding.description')}</p>
      </div>
      <span class="px-2 py-0.5 text-xs font-medium rounded-full bg-[var(--color-warning)]/10 text-[var(--color-warning)]">
        {t('transcoding.state.available')}
      </span>
    </div>

    <p class="mt-4 text-sm th-text-secondary">{t('transcoding.info.ready_hint')}</p>

    <!-- Enable button (one primary action) -->
    <div class="mt-4">
      <button
        type="button"
        class="w-full inline-flex items-center justify-center gap-2 px-4 py-2.5 text-sm font-medium rounded-lg bg-[var(--color-success)] text-white hover:opacity-90 transition-opacity disabled:opacity-60"
        onclick={handleEnable}
        disabled={checkingTranscoding}
      >
        {#if checkingTranscoding}
          <span class="spinner !w-4 !h-4 !border-2"></span>
          {t('transcoding.self_check_running')}
        {:else}
          {t('transcoding.action.enable')}
        {/if}
      </button>
    </div>

    <!-- Hardware capabilities always visible -->
    {#if hardwareInfo}
      <div class="mt-4 p-3 rounded-md th-bg-hover border th-border grid grid-cols-2 gap-3">
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
    {/if}
  </div>

{:else if displayState === 'enabled'}
  <!-- State: Enabled — Transcoding active with queue monitoring and controls -->
  <div class="card p-8 border th-border">
    <div class="flex items-center justify-between mb-1">
      <div>
        <h3 class="text-lg font-semibold th-text-primary">{t('transcoding.title')}</h3>
        <p class="text-sm th-text-secondary mt-1">{t('transcoding.description')}</p>
      </div>
      <span class="px-2 py-0.5 text-xs font-medium rounded-full bg-[var(--color-success)]/10 text-[var(--color-success)]">
        {t('transcoding.state.enabled')}
      </span>
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
    {#if transcodingCheck?.supported}
      <div class="mt-3 p-3 rounded-md bg-[var(--color-success)]/10 border border-[var(--color-success)]/30">
        <div class="flex items-center gap-2 text-sm text-[var(--color-success-light)]">
          <svg class="w-4 h-4" viewBox="0 0 20 20" fill="currentColor"><path fill-rule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clip-rule="evenodd"/></svg>
          <span>{t('transcoding.self_check_passed')}</span>
        </div>
      </div>
    {/if}

    <!-- Max Workers -->
    <div class="mt-4">
      <div class="flex items-center justify-between">
        <div>
          <div class="text-sm th-text-primary">{t('transcoding.max_workers')}</div>
          <div class="text-xs th-text-secondary">{t('transcoding.max_workers_desc')}</div>
        </div>
        <select
          class="input w-20 text-center"
          bind:value={transcodingMaxWorkers}
          onchange={async () => { await updateTranscodingSettings({ enabled: true, max_workers: transcodingMaxWorkers }); showToast(t('common.saved'), 'success'); }}
        >
          <option value={1}>1</option>
          <option value={2}>2</option>
          <option value={3}>3</option>
          <option value={4}>4</option>
        </select>
      </div>
    </div>

    <!-- Queue Status -->
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
            <div class="text-lg font-semibold th-text-primary">{managerStatus.recent_results?.filter((t: TranscodeTask) => t.status === 'completed').length ?? 0}<span class="text-xs text-[var(--color-danger)] ml-1">{managerStatus.recent_results?.filter((t: TranscodeTask) => t.status === 'failed').length ?? 0}✗</span></div>
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
                    <summary class="flex items-center gap-1 cursor-pointer text-[10px] text-[var(--color-danger)] select-none">
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

    <!-- Hardware info collapsible (collapsed by default) -->
    {#if hardwareInfo}
      <button
        type="button"
        class="mt-4 flex items-center gap-1.5 text-sm font-medium th-text-secondary hover:th-text-primary transition-colors"
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
      <div class="mt-2 overflow-hidden transition-all duration-200 {showHardwareInfo ? 'max-h-[500px] opacity-100' : 'max-h-0 opacity-0'}">
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

    <!-- Disable button (separated at bottom) -->
    <div class="mt-6 pt-4 border-t th-border">
      <button
        type="button"
        class="w-full inline-flex items-center justify-center gap-2 px-4 py-2.5 text-sm font-medium rounded-lg border border-[var(--color-danger)]/30 text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10 transition-colors"
        onclick={handleDisable}
      >
        {t('transcoding.action.disable')}
      </button>
    </div>
  </div>

{:else if displayState === 'failed'}
  <!-- State: Failed — Download/check failed, show error and retry -->
  <div class="card p-8 border th-border">
    <div class="flex items-center justify-between mb-1">
      <div>
        <h3 class="text-lg font-semibold th-text-primary">{t('transcoding.title')}</h3>
        <p class="text-sm th-text-secondary mt-1">{t('transcoding.description')}</p>
      </div>
      <span class="px-2 py-0.5 text-xs font-medium rounded-full bg-[var(--color-danger)]/10 text-[var(--color-danger)]">
        {t('transcoding.state.failed')}
      </span>
    </div>

    <!-- Error message card -->
    <div class="mt-4 p-3 rounded-md bg-[var(--color-danger)]/10 border border-[var(--color-danger)]/30">
      <div class="flex items-center gap-2 text-sm text-[var(--color-danger-light)]">
        <AlertCircle size={16} />
        <span>{ffmpegStatus.error || t('transcoding.ffmpeg_failed')}</span>
      </div>
    </div>

    <!-- Error details collapsible -->
    {#if ffmpegStatus.error}
      <details class="mt-2 group">
        <summary class="flex items-center gap-1 cursor-pointer text-xs th-text-secondary select-none">
          <span>{t('transcoding.error_details')}</span>
          <span class="group-open:rotate-180 transition-transform">▼</span>
        </summary>
        <pre class="mt-1 p-2 rounded text-xs th-bg-tertiary th-text-secondary whitespace-pre-wrap break-all max-h-32 overflow-y-auto">{ffmpegStatus.error}</pre>
      </details>
    {/if}

    <p class="mt-4 text-sm th-text-secondary">
      {#if transcodingCheckError}
        {transcodingCheckError}
      {:else}
        {t('transcoding.ffmpeg_failed')}
      {/if}
    </p>

    <!-- Retry button (one primary action) -->
    <div class="mt-4">
      <button
        type="button"
        class="w-full inline-flex items-center justify-center gap-2 px-4 py-2.5 text-sm font-medium rounded-lg bg-[var(--color-warning)] text-white hover:opacity-90 transition-opacity"
        onclick={handleRetryDownload}
      >
        <RotateCw size={16} />
        {t('transcoding.action.retry')}
      </button>
    </div>
  </div>
{/if}
