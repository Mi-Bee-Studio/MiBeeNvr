<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { Maximize, Minimize, AlertCircle, RefreshCw } from 'lucide-svelte';
  import { createHlsConfig } from '$lib/hls-config';
  import {
    setupHlsErrorHandling,
    setupZombieDetector,
    destroyAndRecreate,
    handleVisibilityChange,
    checkStreamAvailable,
  } from '$lib/hls-errors';
  import type { StreamState } from '$lib/hls-errors';

  let {
    cameraId,
    cameraName,
    streamUrl,
    cameraProtocol,
    expanded = false,
  }: {
    cameraId: string;
    cameraName: string;
    streamUrl: string;
    cameraProtocol: string;
    expanded?: boolean;
  } = $props();

  let streamState: StreamState | 'loading' = $state('loading');
  let videoEl: HTMLVideoElement | undefined = $state();
  let hlsInstance: any = null;
  let HlsConstructor: any = null;
  let recreateAttempts = { value: 0 };
  let zombieCleanup: (() => void) | null = null;
  let visibilityCleanup: (() => void) | null = null;

  function dispatchStateChange(state: StreamState | 'loading') {
    // Svelte 5 custom events via bubbling — parent reads detail from DOM event
    const event = new CustomEvent('statechange', {
      bubbles: true,
      detail: { cameraId, state },
    });
    // Dispatch on the component's root element if available
    videoEl?.parentElement?.dispatchEvent(event);
  }

  // Watch streamState changes and dispatch
  $effect(() => {
    const _state = streamState;
    dispatchStateChange(_state);
  });

  function updateState(cameraId_: string, state: StreamState) {
    if (cameraId_ === cameraId) {
      streamState = state;
    }
  }

  function handleZombie(id: string) {
    if (id !== cameraId || !hlsInstance || !HlsConstructor || !videoEl) return;
    const config = buildErrorConfig();
    const newHls = destroyAndRecreate(
      hlsInstance,
      HlsConstructor,
      videoEl,
      streamUrl,
      config,
      recreateAttempts,
    );
    if (newHls) {
      hlsInstance = newHls;
    }
  }

  function handleReconnect() {
    recreateAttempts.value = 0;
    streamState = 'loading';
    destroyCurrentHls();
    initHls();
  }

  function buildErrorConfig() {
    return {
      cameraId,
      maxRetries: 3,
      retryDelays: [2000, 4000, 8000],
      onStateChange: updateState,
      onFallbackToSnapshot: () => {
        streamState = 'error';
      },
    };
  }

  function destroyCurrentHls() {
    if (zombieCleanup) {
      zombieCleanup();
      zombieCleanup = null;
    }
    if (hlsInstance) {
      try {
        hlsInstance.destroy();
      } catch {
        // Already destroyed
      }
      hlsInstance = null;
    }
    HlsConstructor = null;
  }

  async function initHls() {
    if (!videoEl || !streamUrl) return;

    // Check if stream endpoint is available
    const available = await checkStreamAvailable(streamUrl);
    if (!available) {
      streamState = 'error';
      return;
    }

    try {
      const HlsModule = await import('hls.js');
      const Hls = HlsModule.default;

      if (!Hls.isSupported()) {
        streamState = 'error';
        return;
      }

      HlsConstructor = Hls;
      const hls = new Hls(createHlsConfig());
      hlsInstance = hls;
      streamState = 'buffering';
      recreateAttempts.value = 0;

      const config = buildErrorConfig();
      setupHlsErrorHandling(hls, Hls, config);

      // Zombie detector
      zombieCleanup = setupZombieDetector(hls, Hls, videoEl, cameraId, handleZombie);

      hls.loadSource(streamUrl);
      hls.attachMedia(videoEl);

      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        videoEl?.play().catch(() => {});
      });
    } catch {
      streamState = 'error';
    }
  }

  // Main lifecycle effect — reinit when streamUrl changes
  $effect(() => {
    const _url = streamUrl;
    const _proto = cameraProtocol;
    if (!_url || !_proto) return;

    // Only HLS protocols
    const hlsProtocols = ['rtsp_h264', 'rtsp_h265', 'onvif', 'rtsp'];
    if (!hlsProtocols.includes(_proto)) return;

    destroyCurrentHls();
    streamState = 'loading';

    // Defer init to let videoEl bind
    const timer = setTimeout(() => initHls(), 50);
    return () => {
      clearTimeout(timer);
      destroyCurrentHls();
    };
  });

  // Visibility change handler — rebuild stream on foreground
  $effect(() => {
    const _url = streamUrl;
    if (!_url) return;

    visibilityCleanup = handleVisibilityChange(
      () => [cameraId],
      (id: string) => {
        if (id !== cameraId) return;
        recreateAttempts.value = 0;
        destroyCurrentHls();
        streamState = 'loading';
        initHls();
      },
    );

    return () => {
      if (visibilityCleanup) {
        visibilityCleanup();
        visibilityCleanup = null;
      }
    };
  });

  onDestroy(() => {
    destroyCurrentHls();
    if (visibilityCleanup) {
      visibilityCleanup();
      visibilityCleanup = null;
    }
  });

  // --- Derived ---
  let showOverlay = $derived(
    streamState === 'loading' || streamState === 'error' || streamState === 'buffering',
  );
  let overlayClass = $derived(
    streamState === 'loading'
      ? 'opacity-100'
      : streamState === 'error'
        ? 'opacity-100'
        : streamState === 'buffering'
          ? 'opacity-60'
          : 'opacity-0 pointer-events-none',
  );

  let dotColor = $derived(
    streamState === 'playing'
      ? 'bg-green-500'
      : streamState === 'buffering'
        ? 'bg-yellow-500 animate-pulse'
        : streamState === 'error'
          ? 'bg-red-500'
          : 'bg-gray-400',
  );
  let dotTitle = $derived(
    streamState === 'playing'
      ? t('dashboard.live')
      : streamState === 'buffering'
        ? t('dashboard.buffering')
        : streamState === 'error'
          ? t('dashboard.errorState')
          : t('dashboard.snapshotMode'),
  );
</script>

<!-- svelte-ignore binding_property_non_reactive -->
<div class="relative w-full h-full bg-black overflow-hidden group">
  <!-- Video element -->
  <video
    bind:this={videoEl}
    class="w-full h-full object-contain"
    autoplay
    muted
    playsinline
  >
    {t('live.videoUnsupportedTag')}
  </video>

  <!-- Overlay layer with CSS transition -->
  <div
    class="absolute inset-0 flex items-center justify-center transition-opacity duration-200 {overlayClass}"
  >
    {#if streamState === 'loading'}
      <!-- Shimmer loading animation -->
      <div class="absolute inset-0 overflow-hidden">
        <div
          class="absolute inset-0"
          style="background: linear-gradient(90deg, transparent 0%, rgba(255,255,255,0.04) 40%, rgba(255,255,255,0.08) 50%, rgba(255,255,255,0.04) 60%, transparent 100%); background-size: 200% 100%; animation: shimmer 1.8s ease-in-out infinite;"
        ></div>
      </div>
    {:else if streamState === 'error'}
      <!-- Error overlay -->
      <div class="absolute inset-0 bg-black/70"></div>
      <div class="relative flex flex-col items-center gap-3 z-10">
        <AlertCircle size={28} class="text-red-400" />
        <span class="text-white/70 text-xs">{t('live.streamErrorRetries')}</span>
        <button
          onclick={handleReconnect}
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-white/10 text-white/80 text-xs hover:bg-white/20 transition-colors"
        >
          <RefreshCw size={12} />
          {t('common.retry')}
        </button>
      </div>
    {:else if streamState === 'buffering'}
      <!-- Semi-transparent buffering — small indicator, don't fully block video -->
      <div class="relative flex items-center gap-2">
        <div class="w-3 h-3 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
        <span class="text-white/50 text-xs">{t('live.loading')}</span>
      </div>
    {/if}
  </div>

  <!-- Stream state indicator dot (top-left) -->
  <span
    class="absolute top-2 left-2 w-2 h-2 {dotColor} rounded-full z-10"
    title={dotTitle}
  ></span>

  <!-- Camera name + status bar (bottom) -->
  <div
    class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2 z-10"
  >
    <div class="flex items-center gap-2">
      <span class="text-white text-sm font-medium truncate">{cameraName || cameraId}</span>
    </div>
  </div>

  <!-- Expand/Shrink button (top-right) -->
  {#if expanded}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        videoEl?.parentElement?.dispatchEvent(new CustomEvent('shrink', { bubbles: true, detail: { cameraId } }));
      }}
      class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all z-10"
      title={t('dashboard.backToGrid')}
    >
      <Minimize size={16} />
    </button>
  {:else}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        videoEl?.parentElement?.dispatchEvent(new CustomEvent('expand', { bubbles: true, detail: { cameraId } }));
      }}
      class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all opacity-0 group-hover:opacity-100 z-10"
      title={t('dashboard.fullscreen')}
    >
      <Maximize size={16} />
    </button>
  {/if}
</div>

<style>
  @keyframes shimmer {
    0% {
      background-position: -200% 0;
    }
    100% {
      background-position: 200% 0;
    }
  }
</style>
