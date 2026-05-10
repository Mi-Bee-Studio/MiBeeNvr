<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getCamera, getCredentials } from '$lib/api';
  import type { Camera } from '$lib/api';
  import { ArrowLeft, Maximize, Minimize, Play, Pause, Loader2, AlertCircle, RefreshCw } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';

  let { cameraId = '' }: { cameraId?: string } = $props();

  let camera = $state<Camera | null>(null);
  let loading = $state(true);
  let error = $state('');
  let isPlaying = $state(false);
  let isFullscreen = $state(false);

  let videoEl: HTMLVideoElement | undefined = $state();
  let hls: any = null;

  function getStreamUrl(): string {
    return `/api/cameras/${cameraId}/stream/index.m3u8`;
  }

  async function loadCamera() {
    loading = true;
    error = '';
    try {
      camera = await getCamera(cameraId);
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to load camera';
      camera = null;
    } finally {
      loading = false;
    }
  }

  function initPlayer() {
    if (!videoEl || !camera) return;

    const protocol = camera.protocol;
    if (protocol !== 'rtsp_h264' && protocol !== 'rtsp_h265') {
      return; // Handled in template
    }

    const url = getStreamUrl();


    // hls.js
    import('hls.js').then((HlsModule) => {
      const Hls = HlsModule.default;
      if (!Hls.isSupported()) {
        error = 'HLS is not supported in this browser';
        return;
      }

      hls = new Hls({
        enableWorker: false,
        xhrSetup: (xhr: XMLHttpRequest, url: string) => {
          const creds = getCredentials();
          if (creds) {
            if (!xhr.readyState) {
              xhr.open('GET', url, true);
            }
            xhr.setRequestHeader('Authorization', 'Basic ' + btoa(`${creds.username}:${creds.password}`));
          }
        },
      });

      hls.loadSource(url);
      hls.attachMedia(videoEl);

      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        videoEl?.play();
      });

      hls.on(Hls.Events.ERROR, (_event: string, data: any) => {
        if (data.fatal) {
          switch (data.type) {
            case Hls.ErrorTypes.NETWORK_ERROR:
              hls.startLoad();
              break;
            case Hls.ErrorTypes.MEDIA_ERROR:
              hls.recoverMediaError();
              break;
            default:
              error = 'HLS stream error';
              hls.destroy();
              hls = null;
              break;
          }
        }
      });
    }).catch((e) => {
      error = 'Failed to load HLS player';
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
      error = 'Camera ID is required';
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
  $effect(() => {
    if (camera && !loading && !error && videoEl) {
      const protocol = camera.protocol;
      if (protocol === 'rtsp_h264' || protocol === 'rtsp_h265') {
        // Small delay to ensure video element is mounted
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
        <h3 class="text-lg font-medium th-text-primary mb-2">Error</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <div class="flex justify-center gap-3">
          <button onclick={loadCamera} class="btn btn-primary btn-sm flex items-center gap-1">
            <RefreshCw size={14} />
            Retry
          </button>
          <button onclick={goBack} class="btn btn-secondary btn-sm">
            Back
          </button>
        </div>
      </div>
    {:else if camera}
      <div class="space-y-4">
        <!-- Header with camera name -->
        <div class="flex items-center gap-3">
          <button onclick={goBack} class="btn btn-ghost btn-sm flex items-center gap-1">
            <ArrowLeft size={16} />
            Cameras
          </button>
          <h2 class="text-xl font-bold th-text-primary truncate">
            {camera.name || camera.id}
          </h2>
          <span class="badge badge-neutral">{camera.protocol}</span>
        </div>

        {#if camera.protocol === 'rtsp_h264' || camera.protocol === 'rtsp_h265'}
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
                Your browser does not support video playback.
              </video>

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
            <h3 class="text-lg font-medium th-text-primary mb-2">不支持实时播放</h3>
            <p class="th-text-secondary text-sm mb-4">
              Live streaming is only available for H.264/H.265 cameras.
              This camera uses <span class="font-mono th-text-primary">{camera.protocol}</span>.
            </p>
            <button onclick={goBack} class="btn btn-secondary btn-sm">
              Back to Cameras
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
