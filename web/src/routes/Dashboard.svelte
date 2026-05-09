<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getDashboardCameras, getCredentials } from '$lib/api';
  import type { Camera } from '$lib/api';
  import { t } from '$lib/i18n';
  import { Maximize, Minimize, Loader2, AlertCircle, Video, VideoOff, X } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let expandedIndex = $state(-1);

  let videoEls: Record<string, HTMLVideoElement> = {};
  let hlsInstances: Record<string, any> = {};
  let playerErrors = $state<Record<string, string>>({});
  let playerReady = $state<Record<string, boolean>>({});

  let ptzOpenIndex = $state(-1);  // which camera cell has PTZ overlay open

  function getStreamUrl(cameraId: string): string {
    return `/api/cameras/${cameraId}/stream/index.m3u8`;
  }

  function getGridStyle(count: number): string {
    if (count <= 1) return 'grid-template-columns: 1fr; grid-template-rows: 1fr;';
    if (count === 2) return 'grid-template-columns: 1fr 1fr; grid-template-rows: 1fr;';
    if (count === 3) return 'grid-template-columns: 1fr 1fr; grid-template-rows: 1fr 1fr;';
    return 'grid-template-columns: 1fr 1fr; grid-template-rows: 1fr 1fr;';
  }

  function getCellClass(index: number, count: number): string {
    if (expandedIndex >= 0) {
      return index === expandedIndex
        ? 'col-span-2 row-span-2'
        : 'hidden';
    }
    // 3 cameras: first one spans 2 columns
    if (count === 3 && index === 0) {
      return 'col-span-2';
    }
    return '';
  }

  function getStatusBadge(camera: Camera): { class: string; label: string } {
    const status = camera.recorder_status?.toLowerCase() || '';
    if (status === 'recording' || status === 'active') {
      return { class: 'badge-success', label: '●' };
    }
    if (status === 'error' || status === 'failed') {
      return { class: 'badge-error', label: '●' };
    }
    return { class: 'badge-neutral', label: '●' };
  }

  function isHlsSupported(camera: Camera): boolean {
    return camera.protocol === 'rtsp_h264' || camera.protocol === 'rtsp_h265';
  }

  function initPlayer(cameraId: string) {
    const videoEl = videoEls[cameraId];
    if (!videoEl) return;

    const url = getStreamUrl(cameraId);

    import('hls.js').then((HlsModule) => {
      const Hls = HlsModule.default;
      if (!Hls.isSupported()) {
        playerErrors[cameraId] = 'HLS not supported';
        return;
      }

      // Destroy existing instance if any
      const existing = hlsInstances[cameraId];
      if (existing) {
        existing.destroy();
      }

      const hls = new Hls({
        enableWorker: false,
        xhrSetup: (xhr: XMLHttpRequest, reqUrl: string) => {
          const creds = getCredentials();
          if (creds) {
            if (!xhr.readyState) {
              xhr.open('GET', reqUrl, true);
            }
            xhr.setRequestHeader('Authorization', 'Basic ' + btoa(`${creds.username}:${creds.password}`));
          }
        },
      });

      hlsInstances[cameraId] = hls;

      hls.loadSource(url);
      hls.attachMedia(videoEl);

      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        videoEl.play().catch(() => {});
        playerReady[cameraId] = true;
        delete playerErrors[cameraId];
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
              playerErrors[cameraId] = 'Stream error';
              hls.destroy();
              delete hlsInstances[cameraId];
              break;
          }
        }
      });
    }).catch(() => {
      playerErrors[cameraId] = 'Failed to load player';
    });
  }

  function destroyPlayer(cameraId: string) {
    const hls = hlsInstances[cameraId];
    if (hls) {
      hls.destroy();
      delete hlsInstances[cameraId];
    }
    delete playerErrors[cameraId];
    delete playerReady[cameraId];
  }

  function toggleExpand(index: number) {
    expandedIndex = expandedIndex === index ? -1 : index;
  }

  function handleFullscreenChange() {
    if (!document.fullscreenElement) {
      expandedIndex = -1;
    }
  }

  function handleCellClick(index: number, camera: Camera) {
    // Only toggle PTZ for ONVIF cameras
    if (camera.protocol === 'onvif') {
      ptzOpenIndex = ptzOpenIndex === index ? -1 : index;
    }
  }

  function closePtz() {
    ptzOpenIndex = -1;
  }

  onMount(async () => {
    try {
      cameras = (await getDashboardCameras()).slice(0, 4);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
    document.addEventListener('fullscreenchange', handleFullscreenChange);
  });

  onDestroy(() => {
    for (const id of Object.keys(hlsInstances)) {
      destroyPlayer(id);
    }
    document.removeEventListener('fullscreenchange', handleFullscreenChange);
  });

  // Initialize HLS players when cameras load
  $effect(() => {
    const _cameras = cameras;
    const _expanded = expandedIndex;
    const _loading = loading;
    if (_loading || _cameras.length === 0) return;

    // Cleanup players for cameras no longer visible
    for (const id of Object.keys(hlsInstances)) {
      const cam = _cameras.find(c => c.id === id);
      if (!cam || !isHlsSupported(cam)) {
        destroyPlayer(id);
      }
    }

    // Initialize players for visible HLS cameras
    for (const cam of _cameras) {
      if (isHlsSupported(cam)) {
        // Small delay to ensure video element is mounted
        setTimeout(() => initPlayer(cam.id), 50);
      }
    }
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="mx-auto px-3 sm:px-4 lg:px-6 py-4 sm:py-6" style="max-width: 100%;">
    <!-- Header -->
    <div class="flex items-center justify-between mb-4 sm:mb-6">
      <h1 class="text-lg sm:text-xl font-bold th-text-primary flex items-center gap-2">
        <Video size={20} class="text-accent" />
        {t('dashboard.title')}
      </h1>
    </div>

    <!-- Loading state -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="flex flex-col items-center gap-3">
          <div class="spinner spinner-lg"></div>
          <span class="text-sm th-text-secondary">{t('common.loading')}</span>
        </div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="th-color-danger mb-4 flex justify-center"><AlertCircle size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
      </div>
    {:else if cameras.length === 0}
      <!-- Empty state -->
      <div class="card p-8 sm:p-12 text-center">
        <div class="th-text-muted mb-4 flex justify-center"><VideoOff size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('dashboard.noCameras')}</h3>
        <p class="th-text-secondary text-sm">{t('dashboard.noCamerasHint')}</p>
      </div>
    {:else}
      <!-- Camera grid -->
      <div
        class="grid gap-2 sm:gap-3"
        style={getGridStyle(cameras.length)}
      >
        {#each cameras as camera, index}
          {@const status = getStatusBadge(camera)}
          {@const hasError = playerErrors[camera.id]}
          {@const isReady = playerReady[camera.id]}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="relative bg-black rounded-lg overflow-hidden group {getCellClass(index, cameras.length)}"
            style="min-height: {cameras.length === 1 ? 'calc(100vh - 140px)' : 'calc((100vh - 160px) / 2)'};"
            onclick={() => handleCellClick(index, camera)}
          >
            {#if isHlsSupported(camera)}
              <!-- HLS Player -->
              <!-- svelte-ignore binding_property_non_reactive -->
              <video
                bind:this={videoEls[camera.id]}
                class="w-full h-full object-contain"
                autoplay
                muted
                playsinline
              >
              </video>

              <!-- Loading overlay -->
              {#if !isReady && !hasError}
                <div class="absolute inset-0 flex items-center justify-center bg-black/40">
                  <div class="flex flex-col items-center gap-2">
                    <Loader2 size={24} class="text-white animate-spin" />
                    <span class="text-white/70 text-xs">{t('live.loading')}</span>
                  </div>
                </div>
              {/if}

              <!-- Error overlay -->
              {#if hasError}
                <div class="absolute inset-0 flex items-center justify-center bg-black/60">
                  <div class="flex flex-col items-center gap-2">
                    <AlertCircle size={24} class="text-red-400" />
                    <span class="text-white/70 text-xs">{hasError}</span>
                  </div>
                </div>
              {/if}

              <!-- Camera name + status overlay (bottom) -->
              <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2">
                <div class="flex items-center gap-2">
                  <span class="badge {status.class} text-[10px] px-1.5 py-0.5">{status.label}</span>
                  <span class="text-white text-sm font-medium truncate">{camera.name || camera.id}</span>
                </div>
              </div>

              <!-- Expand button (top-right) -->
              <button
                onclick={() => toggleExpand(index)}
                class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all opacity-0 group-hover:opacity-100"
                title={expandedIndex === index ? t('dashboard.exitFullscreen') : t('dashboard.fullscreen')}
              >
                {#if expandedIndex === index}
                  <Minimize size={16} />
                {:else}
                  <Maximize size={16} />
                {/if}
              </button>
            {:else}
              <!-- Unsupported protocol -->
              <div class="absolute inset-0 flex items-center justify-center">
                <div class="flex flex-col items-center gap-2 text-center px-4">
                  <VideoOff size={24} class="text-white/40" />
                  <span class="text-white/50 text-xs">{t('live.notSupported')}</span>
                  <span class="text-white/30 text-[10px] font-mono">{camera.protocol}</span>
                </div>
              </div>
              <!-- Camera name overlay -->
              <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2">
                <div class="flex items-center gap-2">
                  <span class="badge badge-neutral text-[10px] px-1.5 py-0.5">●</span>
                  <span class="text-white text-sm font-medium truncate">{camera.name || camera.id}</span>
                </div>
              </div>
            {/if}
            <!-- PTZ Overlay for ONVIF cameras -->
            {#if ptzOpenIndex === index && camera.protocol === 'onvif'}
              <div
                class="absolute top-2 left-2 z-10"
                onclick={(e: MouseEvent) => { e.stopPropagation(); }}
              >
                <div class="relative">
                  <button
                    class="absolute -top-1.5 -right-1.5 z-20 p-0.5 rounded-full bg-black/70 text-white/80 hover:text-white hover:bg-black/90 transition-all"
                    onclick={(e: MouseEvent) => { e.stopPropagation(); closePtz(); }}
                    aria-label={t('common.close')}
                  >
                    <X size={12} />
                  </button>
                  <PtzControl cameraId={camera.id} enabled={true} />
                </div>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </main>
</div>
