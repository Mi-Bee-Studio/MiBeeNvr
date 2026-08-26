<script lang="ts">
  import { onDestroy, getContext } from 'svelte';
  import { t } from '$lib/i18n';
  import { Maximize, Minimize, AlertCircle, RefreshCw } from 'lucide-svelte';
  import CameraAudioButton from './CameraAudioButton.svelte';
  import { createHlsConfig } from '$lib/hls-config';
  import {
    setupHlsErrorHandling,
    setupZombieDetector,
    destroyAndRecreate,
    checkStreamAvailable,
    createAutoRetryScheduler,
  } from '$lib/hls-errors';
  import type { StreamState } from '$lib/hls-errors';
  import { captureFrame } from '$lib/freeze-frame';
  import type { ReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';
  import { createStateDispatcher } from '$lib/player/dispatch';
  import { LiveLatencyTracker, latencyBadgeClass } from '$lib/live-latency.svelte';

  let {
    cameraId,
    cameraName,
    streamUrl,
    cameraProtocol,
    expanded = false,
    protocol = 'hls',
    tabVisible = true,
    hasAudio = false,
  }: {
    cameraId: string;
    cameraName: string;
    streamUrl: string;
    cameraProtocol: string;
    expanded?: boolean;
    protocol?: string;
    tabVisible?: boolean;
    /** Whether this camera can produce an audio track. When false the audio
     *  button is hidden (MJPEG/JPEG cameras are video-only; audio_enabled off). */
    hasAudio?: boolean;
  } = $props();

  // Reconnection coordinator from Dashboard context
  const coordinator = getContext<ReconnectCoordinator | undefined>('reconnect-coordinator');
  let coordinatedTimer: ReturnType<typeof setTimeout> | null = null;
  let hasActiveCoordinatedReconnect = false;

  function coordinatedReconnect(reconnectFn: () => void) {
    if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
    if (!coordinator) {
      reconnectFn();
      return;
    }
    hasActiveCoordinatedReconnect = true;
    const delay = coordinator.requestReconnect(cameraId, (grantedDelay) => {
      coordinatedTimer = setTimeout(() => {
        coordinatedTimer = null;
        reconnectFn();
      }, grantedDelay);
    });
    if (delay >= 0) {
      coordinatedTimer = setTimeout(() => {
        coordinatedTimer = null;
        reconnectFn();
      }, delay);
    }
    // If -1, queued — callback will fire when slot opens
  }
  let streamState: StreamState | 'loading' = $state('loading');
  let videoEl: HTMLVideoElement | undefined = $state();
  let hlsInstance: any = null;
  // Approximate live latency (#481): HLS has no in-band ingest stamp —
  // hls.js `latency` (playlist edge − playhead, i.e. buffered segments ×
  // duration) is the accepted approximation. Live protocol only.
  const latency = new LiveLatencyTracker(cameraId, 'hls');
  let latencyTimer: ReturnType<typeof setInterval> | null = null;

  function startLatencyTracking() {
    if (latencyTimer || !hlsInstance) return;
    latencyTimer = setInterval(() => {
      const l = hlsInstance?.latency;
      if (typeof l === 'number' && l > 0) latency.trackLatencyMs(l * 1000);
    }, 2000);
  }

  function stopLatencyTracking() {
    if (latencyTimer) clearInterval(latencyTimer);
    latencyTimer = null;
  }
  let HlsConstructor: any = null;
  let recreateAttempts = { value: 0 };
  let zombieCleanup: (() => void) | null = null;
  let autoRetry: ReturnType<typeof createAutoRetryScheduler> | null = null;
  let destroyed = false;
  // streamGaveUp: the auto-retry budget was exhausted for this stream URL.
  // Until the streamUrl/camera changes, we do NOT rebuild HLS — doing so on
  // every visibilitychange (tab refocus) reset recreateAttempts and restarted
  // the death-loop against a stream the backend can't serve (issue #112).
  let streamGaveUp = false;

  // Freeze frame — prevents black flash during reconnection
  let frozenFrameUrl: string | null = $state(null);
  let showFrozenFrame = $state(false);
  let freezeClearTimer: ReturnType<typeof setTimeout> | null = null;

  function captureFreezeFrame() {
    if (frozenFrameUrl) return;
    const frame = captureFrame(videoEl ?? null);
    if (frame) {
      frozenFrameUrl = frame;
      showFrozenFrame = true;
    }
  }

  function clearFreezeFrame() {
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    showFrozenFrame = false;
    freezeClearTimer = setTimeout(() => {
      frozenFrameUrl = null;
      freezeClearTimer = null;
    }, 350);
  }

  function dispatchStateChange(state: StreamState | 'loading') {
    // Svelte 5 custom events via bubbling — parent reads detail from DOM event.
    // Routes through the debounced+deduped dispatcher so hls.js's sub-second
    // buffering↔playing oscillation (issue #107) collapses to ~1 event/window
    // instead of thousands/sec that froze the console.
    stateDispatcher.report(state);
  }

  // Watch streamState changes and dispatch. The dispatcher itself dedupes
  // identical states and debounces non-recovery states, so even though this
  // effect re-runs on every assignment, the actual DOM dispatch is bounded.
  // (Assignment-side dedupe in updateState below prevents most re-runs.)
  $effect(() => {
    const _state = streamState;
    dispatchStateChange(_state);
  });

  // Per-instance dispatcher. Emits the real CustomEvent on the component root.
  // Trailing-edge debounce is cleared on destroy so no stale event fires post-unmount.
  const stateDispatcher = createStateDispatcher((state) => {
    const event = new CustomEvent('statechange', {
      bubbles: true,
      detail: { cameraId, state },
    });
    videoEl?.parentElement?.dispatchEvent(event);
  });

  function updateState(cameraId_: string, state: StreamState) {
    if (cameraId_ === cameraId) {
      // Capture frame before leaving 'playing' state
      if (streamState === 'playing' && state !== 'playing') {
        captureFreezeFrame();
      }
      // Fade out freeze frame after stream resumes
      if (state === 'playing' && frozenFrameUrl) {
        clearFreezeFrame();
      }
      if (state === 'playing' && autoRetry) {
        autoRetry.clear();
        autoRetry = null;
      }
      if (state === 'playing' && coordinator && hasActiveCoordinatedReconnect) {
        coordinator.completeReconnect(cameraId);
        hasActiveCoordinatedReconnect = false;
      }
      // NOTE: no manual dedupe guard here — Svelte 5 treats reassigning an
      // identical primitive to a $state as a no-op (no effect re-run), so
      // redundant hls.js callbacks don't churn the dispatcher. An earlier
      // revision added `if (state === streamState) return;` here, but that
      // changed timing in a way that contributed to effect_update_depth_exceeded
      // when combined with the dispatcher; main has no guard and works, so we
      // keep main's behavior and let Svelte's primitive equality do the dedupe.
      streamState = state;
    }
  }

  function handleZombie(id: string) {
    if (id !== cameraId || !hlsInstance || !HlsConstructor || !videoEl) return;
    captureFreezeFrame();
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
    if (autoRetry) { autoRetry.clear(); autoRetry = null; }
    captureFreezeFrame();
    recreateAttempts.value = 0;
    streamState = 'loading';
    coordinatedReconnect(() => {
      destroyCurrentHls();
      initHls();
    });
  }

  function buildErrorConfig() {
    return {
      cameraId,
      maxRetries: 3,
      retryDelays: [2000, 4000, 8000],
      onStateChange: updateState,
      videoEl: videoEl || undefined,
      onFallbackToSnapshot: () => {
        streamState = 'error';
        if (coordinator) {
          // Use coordinator instead of per-player auto-retry
          coordinatedReconnect(() => {
            streamState = 'loading';
            destroyCurrentHls();
            initHls();
          });
          return;
        }
        if (!autoRetry) {
          autoRetry = createAutoRetryScheduler(
            () => {
              streamState = 'loading';
              destroyCurrentHls();
              initHls();
            },
            () => {
              // Retry budget exhausted — give up for this stream URL so a later
              // visibilitychange doesn't reset recreateAttempts and restart the
              // loop. The streamState stays 'error', which CameraPlayer reports
              // to the orchestrator as a fatal failure (demote to next chain
              // entry, or snapshot if the chain is exhausted).
              streamGaveUp = true;
            },
          );
        }
        autoRetry.schedule();
      },
    };
  }

  function destroyCurrentHls() {
    if (zombieCleanup) {
      zombieCleanup();
      zombieCleanup = null;
    }
    if (autoRetry) { autoRetry.clear(); autoRetry = null; }
    if (hlsInstance) {
      try {
        hlsInstance.destroy();
      } catch (e) { console.warn('HLS destroy error (already destroyed?):', e); }
      hlsInstance = null;
    }
    HlsConstructor = null;
  }

  async function initHls() {
    if (!videoEl || !streamUrl) return;
    if (destroyed) return;

    // Check if stream endpoint is available
    const available = await checkStreamAvailable(streamUrl);
    if (destroyed) return;
    if (!available) {
      streamState = 'error';
      return;
    }

    try {
      const HlsModule = await import('hls.js');
      if (destroyed) return;
      const Hls = HlsModule.default;

      if (!Hls.isSupported()) {
        streamState = 'error';
        return;
      }

      HlsConstructor = Hls;
      const hls = new Hls(createHlsConfig(protocol));
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
        if (protocol === 'hls') startLatencyTracking();
      });
    } catch (e) { console.warn('HLS init failed:', e);
streamState = 'error';
}
  }

  // Main lifecycle effect — reinit when streamUrl changes
  $effect(() => {
    const _url = streamUrl;
    const _proto = cameraProtocol;
    if (!_url || !_proto) return;

    // Only HLS protocols
    const hlsProtocols = ['rtsp_h264', 'rtsp_h265', 'onvif', 'rtsp', 'xiaomi'];
    if (!hlsProtocols.includes(_proto)) return;

    // New stream URL = fresh retry budget. Reset the give-up flag so a camera
    // that previously exhausted retries (e.g. was unreachable, now recovered
    // and got a new stream URL) gets a clean slate.
    streamGaveUp = false;
    destroyCurrentHls();
    streamState = 'loading';

    // Defer init to let videoEl bind
    const timer = setTimeout(() => initHls(), 50);
    return () => {
      clearTimeout(timer);
      destroyCurrentHls();
    };
  });

  // Coordinated visibility — pause when tab hidden, resume when visible
  // Replaces handleVisibilityChange() per-player listener; Dashboard owns the signal
  $effect(() => {
    const visible = tabVisible;
    const _url = streamUrl;
    if (!_url) return;

    if (!visible) {
      // Tab hidden — destroy HLS to release decode/network resources
      if (hlsInstance && !destroyed) {
        try { hlsInstance.destroy(); } catch { /* ignore */ }
        hlsInstance = null;
        if (zombieCleanup) { zombieCleanup(); zombieCleanup = null; }
      }
    } else {
      // Tab visible — resume: rebuild HLS stream. BUT not if this stream already
      // exhausted its retry budget (streamGaveUp) — rebuilding would reset
      // recreateAttempts and restart the death-loop (issue #112). Only a
      // streamUrl/camera change legitimately resets streamGaveUp (see the
      // streamUrl $effect prelude).
      if (!destroyed && !streamGaveUp && streamState !== 'loading' && !hlsInstance) {
        captureFreezeFrame();
        recreateAttempts.value = 0;
        streamState = 'loading';
        initHls();
      }
    }
  });

  onDestroy(() => {
    destroyed = true;
    if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
    if (coordinator) coordinator.cancelRequest(cameraId);
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    frozenFrameUrl = null;
    stateDispatcher.dispose();
    stopLatencyTracking();
    destroyCurrentHls();
    destroyCurrentHls();
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
  <!-- Freeze frame — last good frame shown during reconnection -->
  {#if frozenFrameUrl}
    <img
      src={frozenFrameUrl}
      alt=""
      class="absolute inset-0 w-full h-full object-contain transition-opacity duration-300 {showFrozenFrame ? 'opacity-100' : 'opacity-0 pointer-events-none'}"
      aria-hidden="true"
    />
  {/if}

  <!-- Video element -->
  <video
    bind:this={videoEl}
    class="w-full h-full object-contain"
    autoplay
    muted
    playsinline
    aria-label="{cameraName} — {dotTitle}"
  >
    {t('live.videoUnsupportedTag')}
  </video>
  {#if protocol === 'hls' && latency.value != null}
    <span
      class="absolute top-1.5 left-2 text-xs tabular-nums z-20 {latencyBadgeClass(latency.value)}"
      title={t('flow.liveLatency')}
    >
      ≈{(latency.value / 1000).toFixed(1)}s
    </span>
  {/if}

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

  <!-- Audio button (top-right, before expand) — hidden for video-only cameras -->
  {#if hasAudio}
    <CameraAudioButton {cameraId} />
  {/if}

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
