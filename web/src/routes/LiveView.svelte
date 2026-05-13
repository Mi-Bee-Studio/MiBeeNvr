<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getCamera } from '$lib/api';
  import type { Camera } from '$lib/api';
  import { ArrowLeft, Maximize, Minimize, Play, Pause, Loader2, AlertCircle, RefreshCw } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';
  import { t } from '$lib/i18n';
  import { createHlsConfig } from '$lib/hls-config';
  import { setupHlsErrorHandling, checkStreamAvailable } from '$lib/hls-errors';
  import type { StreamState } from '$lib/hls-errors';

  let { cameraId = '' }: { cameraId?: string } = $props();

  let camera = $state<Camera | null>(null);
  let loading = $state(true);
  let error = $state('');
  let isPlaying = $state(false);
  let isFullscreen = $state(false);

  let videoEl: HTMLVideoElement | undefined = $state();
  let hls: any = null;
  let streamState = $state<StreamState>('buffering');

  function getStreamUrl(): string {
    return `/api/cameras/${cameraId}/stream/index.m3u8`;
  }

  async function loadCamera() {
    loading = true;
    error = '';
    try {
      camera = await getCamera(cameraId);
    } catch (e) {
      error = e instanceof Error ? e.message : t('live.failedLoadCamera');
      camera = null;
    } finally {
      loading = false;
    }
  }

  function initPlayer() {
    if (!videoEl || !camera) return;

    const protocol = camera.protocol;
    if (protocol !== 'rtsp_h264' && protocol !== 'rtsp_h265' && protocol !== 'onvif' && protocol !== 'rtsp') {
      return; // Handled in template
    }

    const url = getStreamUrl();
    streamState = 'buffering';

    // Check if stream endpoint is available (not 429)
    checkStreamAvailable(url).then((available) => {
      if (!available) {
        streamState = 'error';
        error = t('live.maxStreamsReached');
        return;
      }

      import('hls.js').then((HlsModule) => {
        const Hls = HlsModule.default;
        if (!Hls.isSupported()) {
          error = t('live.hlsNotSupported');
          return;
        }

        hls = new Hls(createHlsConfig());

        setupHlsErrorHandling(hls, Hls, {
          cameraId,
          maxRetries: 3,
          retryDelays: [2000, 4000, 8000],
          onStateChange: (_id, state) => {
            streamState = state;
            if (state === 'error') {
              error = t('live.streamErrorRetries');
            }
          },
          onFallbackToSnapshot: () => {
            streamState = 'error';
            error = t('live.streamUnavailable');
          },
        });

        hls.loadSource(url);
        hls.attachMedia(videoEl);

        hls.on(Hls.Events.MANIFEST_PARSED, () => {
          videoEl?.play();
        });
      }).catch(() => {
        error = t('live.failedLoadPlayer');
        streamState = 'error';
      });
    });
  }

  function togglePlay() {
    if (!videoEl) return;
    if (videoEl.paused) {
      videoEl.play();
    } else {
      videoEl.pause();
    }
  }

  function toggleFullscreen() {
    if (!videoEl) return;
    try {
      if (!document.fullscreenElement) {
        videoEl.requestFullscreen();
        isFullscreen = true;
      } else {
        document.exitFullscreen();
        isFullscreen = false;
      }
    } catch {
      // Fullscreen not supported
    }
  }

  function handlePlay() {
    isPlaying = true;
  }

  function handlePause() {
    isPlaying = false;
  }

  function goBack() {
    window.location.hash = '#/cameras';
  }

  function handleFullscreenChange() {
    isFullscreen = !!document.fullscreenElement;
  }

  onMount(() => {
    if (!cameraId) {
      error = t('live.cameraIdRequired');
      loading = false;
      return;
    }

    loadCamera();
    document.addEventListener('fullscreenchange', handleFullscreenChange);
  });

  onDestroy(() => {
    if (hls) {
      hls.destroy();
      hls = null;
    }
    document.removeEventListener('fullscreenchange', handleFullscreenChange);
  });

  // Initialize player after camera loads
  let playerInitialized = false;
  $effect(() => {
    if (camera && !loading && !error && videoEl && !playerInitialized) {
      const protocol = camera.protocol;
      if (protocol === 'rtsp_h264' || protocol === 'rtsp_h265' || protocol === 'onvif' || protocol === 'rtsp') {
        playerInitialized = true;
        setTimeout(() => initPlayer(), 50);
      }
    }
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Loading state -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="th-color-danger mb-4 flex justify-center"><AlertCircle size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <div class="flex justify-center gap-3">
          <button onclick={loadCamera} class="btn btn-primary btn-sm flex items-center gap-1">
            <RefreshCw size={14} />
            {t('common.retry')}
          </button>
          <button onclick={goBack} class="btn btn-secondary btn-sm">
            {t('detail.back')}
          </button>
        </div>
      </div>
    {:else if camera}
      <div class="space-y-4">
        <!-- Header with camera name -->
        <div class="flex items-center gap-3">
          <button onclick={goBack} class="btn btn-ghost btn-sm flex items-center gap-1">
            <ArrowLeft size={16} />
            {t('nav.cameras')}
          </button>
          <h2 class="text-xl font-bold th-text-primary truncate">
            {camera.name || camera.id}
          </h2>
          <span class="badge badge-neutral">{camera.protocol}</span>
        </div>

        {#if camera.protocol === 'rtsp_h264' || camera.protocol === 'rtsp_h265' || camera.protocol === 'onvif' || camera.protocol === 'rtsp'}
          <!-- HLS Player -->
          <div class="card border th-border overflow-hidden">
            <div class="relative bg-black">
              <video
                bind:this={videoEl}
                class="w-full max-h-[80vh]"
                autoplay
                muted
                playsinline
                onplay={handlePlay}
                onpause={handlePause}
              >
                {t('live.videoUnsupportedTag')}
              </video>

              <!-- Stream state indicator -->
              {#if streamState === 'playing'}
                <span class="absolute top-3 left-3 w-2.5 h-2.5 bg-green-500 rounded-full" title={t('dashboard.live')}></span>
              {:else if streamState === 'buffering'}
                <span class="absolute top-3 left-3 w-2.5 h-2.5 bg-yellow-500 rounded-full animate-pulse" title={t('dashboard.buffering')}></span>
              {:else if streamState === 'error'}
                <span class="absolute top-3 left-3 w-2.5 h-2.5 bg-red-500 rounded-full" title={t('dashboard.errorState')}></span>
              {:else if streamState === 'snapshot'}
                <span class="absolute top-3 left-3 w-2.5 h-2.5 bg-gray-400 rounded-full" title={t('dashboard.snapshotMode')}></span>
              {/if}
              <!-- Custom controls overlay -->
              <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/80 to-transparent p-4">
                <div class="flex items-center gap-3">
                  <button onclick={togglePlay} class="text-white hover:text-white/80 transition-colors">
                    {#if isPlaying}
                      <Pause size={24} />
                    {:else}
                      <Play size={24} />
                    {/if}
                  </button>
                  <div class="flex-1"></div>
                  <button onclick={toggleFullscreen} class="text-white hover:text-white/80 transition-colors">
                    {#if isFullscreen}
                      <Minimize size={20} />
                    {:else}
                      <Maximize size={20} />
                    {/if}
                  </button>
                </div>
              </div>
            </div>
          </div>
        {:else}
          <!-- Unsupported protocol -->
          <div class="card p-12 text-center">
            <div class="th-text-muted mb-4 flex justify-center"><AlertCircle size={48} /></div>
            <h3 class="text-lg font-medium th-text-primary mb-2">{t('live.notSupported')}</h3>
            <p class="th-text-secondary text-sm mb-4">
              {t('live.notSupportedDesc')}
              <span class="font-mono th-text-primary">{camera.protocol}</span>.
            </p>
            <button onclick={goBack} class="btn btn-secondary btn-sm">
              {t('live.backToCameras')}
            </button>
          </div>
        {/if}
        
        <!-- PTZ Control for ONVIF cameras -->
        {#if camera.protocol === 'onvif'}
          <div class="card">
            <PtzControl {cameraId} enabled={true} />
          </div>
        {/if}
      </div>
    {/if}
  </main>
</div>
