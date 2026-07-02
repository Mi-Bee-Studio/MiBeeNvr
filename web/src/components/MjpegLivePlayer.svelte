<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { AlertCircle, RefreshCw, ImageIcon } from 'lucide-svelte';
  import { getAuthHeader, API_BASE } from '$lib/api';

  interface Props {
    cameraId: string;
    cameraName?: string;
    expanded?: boolean;
    onError?: (msg: string) => void;
    onLoad?: () => void;
  }

  let {
    cameraId,
    cameraName = '',
    expanded = false,
    onError,
    onLoad,
  }: Props = $props();

  type MjpegState = 'loading' | 'playing' | 'frozen' | 'error';

  let streamState: MjpegState = $state('loading');
  let imgEl: HTMLImageElement | undefined = $state();
  let destroyed = $state(false);
  let pollTimer: ReturnType<typeof setInterval> | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectAttempts = 0;
  const maxReconnectAttempts = 5;
  const reconnectDelays = [2000, 4000, 8000, 16000, 32000];
  const FROZEN_TIMEOUT_MS = 15000;
  const FROZEN_CHECK_INTERVAL_MS = 3000;
  const POLL_INTERVAL_MS = 500;
  let lastLoadTime = 0;
  let frozenCheckTimer: ReturnType<typeof setInterval> | null = null;
  let currentBlobUrl: string | null = null;
  let consecutiveErrors = 0;
  const MAX_CONSECUTIVE_ERRORS = 3;

  function revokeBlobUrl() {
    if (currentBlobUrl) {
      URL.revokeObjectURL(currentBlobUrl);
      currentBlobUrl = null;
    }
  }

  async function pollFrame() {
    if (destroyed || !cameraId) return;

    try {
      const authHeader = getAuthHeader();
      const headers: Record<string, string> = {};
      if (authHeader) headers['Authorization'] = authHeader;

      const resp = await fetch(`${API_BASE}/cameras/${cameraId}/latest-frame`, {
        headers,
        cache: 'no-store',
      });

      if (destroyed) return;

      if (!resp.ok) {
        consecutiveErrors++;
        if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
          // Transient failure — retry with backoff instead of dying for good.
          // (scheduleReconnect gives up -> permanent 'error' only after 5 cycles.)
          consecutiveErrors = 0;
          scheduleReconnect();
        }
        return;
      }

      const blob = await resp.blob();
      if (destroyed || blob.size === 0) return;

      revokeBlobUrl();
      currentBlobUrl = URL.createObjectURL(blob);

      if (imgEl) {
        imgEl.src = currentBlobUrl;
      }

      consecutiveErrors = 0;
      lastLoadTime = Date.now();

      if (streamState === 'loading' || streamState === 'frozen') {
        streamState = 'playing';
        reconnectAttempts = 0;
        startFrozenDetection();
        onLoad?.();
      }
    } catch {
      if (destroyed) return;
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        consecutiveErrors = 0;
        scheduleReconnect();
      }
    }
  }

  function startPolling() {
    stopPolling();
    consecutiveErrors = 0;
    // Immediate first poll, then interval
    pollFrame();
    pollTimer = setInterval(pollFrame, POLL_INTERVAL_MS);
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function startFrozenDetection() {
    if (frozenCheckTimer) clearInterval(frozenCheckTimer);
    lastLoadTime = Date.now();
    frozenCheckTimer = setInterval(() => {
      if (streamState !== 'playing') return;
      const elapsed = Date.now() - lastLoadTime;
      if (elapsed > FROZEN_TIMEOUT_MS) {
        streamState = 'frozen';
        onError?.(t('live.mjpegPlayer.frozen'));
      }
    }, FROZEN_CHECK_INTERVAL_MS);
  }

  function stopFrozenDetection() {
    if (frozenCheckTimer) {
      clearInterval(frozenCheckTimer);
      frozenCheckTimer = null;
    }
  }

  function scheduleReconnect() {
    if (reconnectAttempts >= maxReconnectAttempts) {
      stopPolling();
      stopFrozenDetection();
      streamState = 'error';
      onError?.(t('live.mjpegPlayer.error'));
      return;
    }
    reconnectAttempts++;
    const base = reconnectDelays[Math.min(reconnectAttempts - 1, reconnectDelays.length - 1)];
    // ±25% jitter desynchronizes cameras that died together after a shared
    // transient backend hiccup, avoiding a thundering-herd retry stampede.
    const delay = Math.round(base * (0.75 + Math.random() * 0.5));
    streamState = 'loading';
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      startPolling();
    }, delay);
  }

  function handleReconnect() {
    stopFrozenDetection();
    stopPolling();
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    reconnectAttempts = 0;
    consecutiveErrors = 0;
    streamState = 'loading';
    startPolling();
  }

  function handleFrozenRetry() {
    streamState = 'playing';
    lastLoadTime = Date.now();
    startFrozenDetection();
  }

  // Main lifecycle
  $effect(() => {
    const _id = cameraId;
    if (!_id) return;

    stopPolling();
    stopFrozenDetection();
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    reconnectAttempts = 0;
    consecutiveErrors = 0;
    streamState = 'loading';

    // Stagger the first poll across cameras (100-500ms) so the fixed 500ms poll
    // cycles stay phase-offset: a short backend hiccup won't push every camera
    // past the consecutive-error threshold in the same window.
    const timer = setTimeout(() => startPolling(), 100 + Math.random() * 400);
    return () => {
      clearTimeout(timer);
    };
  });

  onDestroy(() => {
    destroyed = true;
    stopPolling();
    stopFrozenDetection();
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    revokeBlobUrl();
    if (imgEl) {
      imgEl.src = '';
    }
  });

  // Derived display states
  let showOverlay = $derived(
    streamState === 'loading' || streamState === 'error' || streamState === 'frozen',
  );
  let overlayClass = $derived(
    streamState === 'loading'
      ? 'opacity-100'
      : streamState === 'error'
        ? 'opacity-100'
        : streamState === 'frozen'
          ? 'opacity-80'
          : 'opacity-0 pointer-events-none',
  );
  let dotColor = $derived(
    streamState === 'playing'
      ? 'bg-green-500'
      : streamState === 'frozen'
        ? 'bg-amber-500 animate-pulse'
        : streamState === 'error'
          ? 'bg-red-500'
          : 'bg-gray-400',
  );
  let dotTitle = $derived(
    streamState === 'playing'
      ? t('dashboard.live')
      : streamState === 'frozen'
        ? t('live.mjpegPlayer.frozen')
        : streamState === 'error'
          ? t('dashboard.errorState')
          : t('live.mjpegPlayer.connecting'),
  );
</script>

<div class="relative w-full h-full bg-black overflow-hidden group">
  <!-- MJPEG image display -->
  {#if streamState !== 'error'}
    <img
      bind:this={imgEl}
      src=""
      alt="{cameraName || cameraId}"
      class="w-full h-full object-contain"
      aria-label="{cameraName || cameraId} — {dotTitle}"
    />
  {/if}

  <!-- Overlay -->
  <div
    class="absolute inset-0 flex items-center justify-center transition-opacity duration-200 {overlayClass}"
  >
    {#if streamState === 'loading'}
      <div class="absolute inset-0 overflow-hidden">
        <div
          class="absolute inset-0"
          style="background: linear-gradient(90deg, transparent 0%, rgba(255,255,255,0.04) 40%, rgba(255,255,255,0.08) 50%, rgba(255,255,255,0.04) 60%, transparent 100%); background-size: 200% 100%; animation: shimmer 1.8s ease-in-out infinite;"
        ></div>
      </div>
    {:else if streamState === 'error'}
      <div class="absolute inset-0 bg-black/70"></div>
      <div class="relative flex flex-col items-center gap-3 z-10">
        <AlertCircle size={28} class="text-red-400" />
        <span class="text-white/70 text-xs">{t('live.mjpegPlayer.error')}</span>
        <button
          onclick={handleReconnect}
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-white/10 text-white/80 text-xs hover:bg-white/20 transition-colors"
        >
          <RefreshCw size={12} />
          {t('live.mjpegPlayer.retry')}
        </button>
      </div>
    {:else if streamState === 'frozen'}
      <div class="absolute inset-0 bg-black/40"></div>
      <div class="relative flex flex-col items-center gap-3 z-10">
        <ImageIcon size={28} class="text-amber-400" />
        <span class="text-white/70 text-xs">{t('live.mjpegPlayer.frozen')}</span>
        <button
          onclick={handleFrozenRetry}
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-white/10 text-white/80 text-xs hover:bg-white/20 transition-colors"
        >
          <RefreshCw size={12} />
          {t('live.mjpegPlayer.retry')}
        </button>
      </div>
    {/if}
  </div>

  <!-- State dot -->
  <span
    class="absolute top-2 left-2 w-2 h-2 {dotColor} rounded-full z-10"
    title={dotTitle}
  ></span>

  <!-- Camera name bar -->
  <div
    class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2 z-10"
  >
    <div class="flex items-center gap-2">
      <span class="text-white text-sm font-medium truncate">{cameraName || cameraId}</span>
      <span class="text-white/50 text-xs">MJPEG</span>
    </div>
  </div>

  <!-- Expand/Shrink -->
  {#if expanded}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        imgEl?.parentElement?.dispatchEvent(new CustomEvent('shrink', { bubbles: true, detail: { cameraId } }));
      }}
      class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all z-10"
      title={t('dashboard.backToGrid')}
    >
      <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"></polyline><polyline points="20 10 14 10 14 4"></polyline><line x1="14" y1="10" x2="21" y2="3"></line><line x1="3" y1="21" x2="10" y2="14"></line></svg>
    </button>
  {:else}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        imgEl?.parentElement?.dispatchEvent(new CustomEvent('expand', { bubbles: true, detail: { cameraId } }));
      }}
      class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all opacity-0 group-hover:opacity-100 z-10"
      title={t('dashboard.fullscreen')}
    >
      <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"></polyline><polyline points="9 21 3 21 3 15"></polyline><line x1="21" y1="3" x2="14" y2="10"></line><line x1="3" y1="21" x2="10" y2="14"></line></svg>
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
