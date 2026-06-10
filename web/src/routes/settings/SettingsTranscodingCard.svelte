<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getTranscodingCheck, getTranscodingStatus, getFFmpegStatus, downloadFFmpeg, retryDownload, getTranscodingSettings, updateTranscodingSettings } from '$lib/api/transcoding';
  import type { SelfCheckResult, DownloadStatus, HardwareCapabilities, ManagerStatus, TranscodeTask } from '$lib/api/transcoding';
  import { t } from '$lib/i18n';
  import { AlertCircle, AlertTriangle, Download, RotateCw, Cpu, ChevronDown, ChevronUp } from 'lucide-svelte';
  import { showToast } from '$lib/toast';

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
            onchange={async () => { await updateTranscodingSettings({ enabled: true, max_workers: transcodingMaxWorkers }); showToast(t('common.saved'), 'success'); }}
          >
            <option value={1}>1</option>
            <option value={2}>2</option>
            <option value={3}>3</option>
            <option value={4}>4</option>
          </select>
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
