<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getCamera, listProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, normalizeProtocol, getProtocolCapabilities } from '$lib/api';
  import type { Camera, ProtocolInfo } from '$lib/api';
  import { ArrowLeft, Maximize, Minimize, AlertCircle, RefreshCw } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import WebRTCPlayer from '../components/WebRTCPlayer.svelte';
  import FlvPlayer from '../components/FlvPlayer.svelte';
  import ProtocolSwitcher from '../components/ProtocolSwitcher.svelte';
  import type { StreamingProtocol } from '../components/ProtocolSwitcher.svelte';
  import { t } from '$lib/i18n';

  let { cameraId = '' }: { cameraId?: string } = $props();

  let camera = $state<Camera | null>(null);
  let loading = $state(true);
  let error = $state('');
  let isFullscreen = $state(false);
  let playerContainer: HTMLDivElement | undefined = $state();
  let protocolsMap = $state<Map<string, ProtocolInfo>>(buildProtocolsMap(DEFAULT_PROTOCOLS));
  let streamingProtocol = $state<StreamingProtocol>('hls');
  let switchingProtocol = $state(false);

  function isHlsSupported(cam: Camera): boolean {
    return getProtocolCapabilities(cam.protocol, protocolsMap).hls;
  }

  function isPtzSupported(cam: Camera): boolean {
    return getProtocolCapabilities(cam.protocol, protocolsMap).ptz;
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

  function goBack() {
    window.location.hash = '#/cameras';
  }

  function toggleFullscreen() {
    if (!playerContainer) return;
    try {
      if (!document.fullscreenElement) {
        playerContainer.requestFullscreen();
        isFullscreen = true;
      } else {
        document.exitFullscreen();
        isFullscreen = false;
      }
    } catch (e) { console.warn('Fullscreen not supported:', e); }
  }

  function handleFullscreenChange() {
    isFullscreen = !!document.fullscreenElement;
  }

  function handleProtocolChange(protocol: StreamingProtocol) {
    switchingProtocol = true;
    streamingProtocol = protocol;
    // Brief delay to show switching state, then mount new player
    setTimeout(() => { switchingProtocol = false; }, 100);
  }

  onMount(() => {
    if (!cameraId) {
      error = t('live.cameraIdRequired');
      loading = false;
      return;
    }

    loadCamera();
    document.addEventListener('fullscreenchange', handleFullscreenChange);
    // Load protocol capabilities
    listProtocols().then(list => {
      if (list && list.length > 0) protocolsMap = buildProtocolsMap(list);
    }).catch((e) => { console.warn('Failed to load protocols:', e); });
  });

  onDestroy(() => {
    document.removeEventListener('fullscreenchange', handleFullscreenChange);
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
          <span class="badge badge-neutral">{protocolsMap.get(camera.protocol)?.label || camera.protocol}</span>
          {#if isHlsSupported(camera)}
            <div class="flex-1"></div>
            <!-- Protocol Switcher -->
            <ProtocolSwitcher
              cameraId={camera.id}
              cameraEncoding={camera.encoding || camera.stream_encoding || ''}
              selected={streamingProtocol}
              onchange={handleProtocolChange}
            />
            <button onclick={toggleFullscreen} class="btn btn-ghost btn-sm flex items-center gap-1">
              {#if isFullscreen}
                <Minimize size={16} />
              {:else}
                <Maximize size={16} />
              {/if}
            </button>
          {/if}
        </div>

        {#if isHlsSupported(camera)}
          <!-- Player container -->
          <div
            class="card border th-border overflow-hidden"
            style="max-height: 80vh;"
            bind:this={playerContainer}
            onshrink={() => goBack()}
          >
            {#if switchingProtocol}
              <!-- Switching state -->
              <div class="relative w-full bg-black" style="aspect-ratio: 16/9;">
                <div class="absolute inset-0 flex items-center justify-center">
                  <div class="flex items-center gap-2">
                    <div class="w-3 h-3 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
                    <span class="text-white/50 text-xs">{t('live.protocol.switching')}</span>
                  </div>
                </div>
              </div>
            {:else if streamingProtocol === 'webrtc'}
              <WebRTCPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                expanded={true}
              />
            {:else if streamingProtocol === 'flv'}
              <FlvPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                expanded={true}
              />
            {:else}
              <VideoPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                streamUrl={`/api/cameras/${cameraId}/stream/index.m3u8`}
                cameraProtocol={camera.protocol}
                protocol={streamingProtocol}
                expanded={true}
              />
            {/if}
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
        
        <!-- PTZ Control for PTZ-capable cameras -->
        {#if isPtzSupported(camera)}
          <div class="card">
            <PtzControl {cameraId} enabled={true} />
          </div>
        {/if}
      </div>
    {/if}
  </main>
</div>
