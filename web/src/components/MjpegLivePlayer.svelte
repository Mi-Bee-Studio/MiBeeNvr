<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { AlertCircle, RefreshCw, ImageIcon } from 'lucide-svelte';
  import { getAuthHeader, getTokenForUrl, API_BASE } from '$lib/api';

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
  // No cap on reconnect attempts: a deploy-induced outage (service restart +
  // ~60s camera reconnect) outlasts any finite cap, and a capped player gives
  // up just before the camera comes back, requiring manual refresh. The
  // backoff array below already rate-limits retries to ≤1 per 32s per camera,
  // so unbounded retries are cheap (9 cameras × 1 req/32s = 0.3 req/s).
  const reconnectDelays = [2000, 4000, 8000, 16000, 32000];
  const FROZEN_TIMEOUT_MS = 15000;
  const FROZEN_CHECK_INTERVAL_MS = 3000;
  // 0.10.1: poll every 1000ms instead of 500ms — the server now supports ETag
  // conditional requests (304 Not Modified when frame unchanged), so bandwidth
  // is near-zero when the scene is static. 1s = 1fps live preview, sufficient
  // for MJPEG cameras (ESP32 MiBeeCam typically delivers 2-5fps anyway).
  const POLL_INTERVAL_MS = 1000;
  let lastLoadTime = 0;
  let frozenCheckTimer: ReturnType<typeof setInterval> | null = null;
  let currentBlobUrl: string | null = null;
  let consecutiveErrors = 0;
  const MAX_CONSECUTIVE_ERRORS = 3;
  // Track the last ETag to send If-None-Match on subsequent polls.
  let lastEtag: string | null = null;
  let wsConnection: WebSocket | null = null;
  let useWebSocket = true; // Try WebSocket first, fall back to HTTP polling
  // Silence-watchdog state: a WS that stays open without any video frame is
  // treated as dead (recorder-side hub went stale) → fall back to polling.
  const WS_SILENCE_FALLBACK_MS = 5000;
  let wsSilenceTimer: ReturnType<typeof setTimeout> | null = null;
  let renderedOnce = false; // at least one frame has been painted
  let frameSeenSinceOpen = false;

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
      // Send ETag from last successful fetch — server returns 304 if unchanged.
      if (lastEtag) headers['If-None-Match'] = lastEtag;

      const resp = await fetch(`${API_BASE}/cameras/${cameraId}/latest-frame`, {
        headers,
        cache: 'no-cache',
      });

      if (destroyed) return;

      // 304 Not Modified — frame unchanged, keep current image, just update timestamp.
      if (resp.status === 304) {
        consecutiveErrors = 0;
        lastLoadTime = Date.now();
        // A 304 is only usable when the player ALREADY rendered a frame. With
        // cache:'no-cache' the browser can answer 304 from its own cache on
        // the very first poll (recorder-side frame cache frozen), leaving the
        // <img> empty forever — force one unconditional fetch so the viewer
        // at least sees the cached frame, and live motion resumes when the
        // recorder starts producing frames again.
        if (!renderedOnce) {
          renderedOnce = true; // guard against loops
          try {
            const fresh = await fetch(`${API_BASE}/cameras/${cameraId}/latest-frame`, { cache: 'reload', headers: authHeader ? { Authorization: authHeader } : {} });
            if (fresh.ok && !destroyed) {
              const blob = await fresh.blob();
              if (blob.size > 0) {
                revokeBlobUrl();
                currentBlobUrl = URL.createObjectURL(blob);
                if (imgEl) imgEl.src = currentBlobUrl;
              }
            }
          } catch { /* best-effort */ }
        }
        return;
      }

      if (!resp.ok) {
        consecutiveErrors++;
        if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
          consecutiveErrors = 0;
          scheduleReconnect();
        }
        return;
      }

      // Save ETag for next poll's conditional request.
      const etag = resp.headers.get('ETag');
      if (etag) lastEtag = etag;

      const blob = await resp.blob();
      if (destroyed || blob.size === 0) return;

      revokeBlobUrl();
      currentBlobUrl = URL.createObjectURL(blob);

      if (imgEl) {
        imgEl.src = currentBlobUrl;
      }
      renderedOnce = true;

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

  // ── WebSocket streaming (preferred over HTTP polling) ──────────────────
  // Connects to the wsstream WebSocket endpoint, which pushes JPEG frames
  // in real-time. Falls back to HTTP polling if WebSocket fails.
  function startWebSocket() {
    stopWebSocket();
    if (!cameraId) return;

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    // wsstream endpoint accepts ?token= for auth. The token is the bare session
    // token (mbs_...), NOT a "Bearer ..." header value.
    let wsUrl = `${proto}//${window.location.host}${API_BASE}/cameras/${cameraId}/stream/ws`;
    const token = getTokenForUrl();
    if (token) {
      wsUrl += `?token=${encodeURIComponent(token)}`;
    }

    try {
      wsConnection = new WebSocket(wsUrl);
    } catch {
      useWebSocket = false;
      startPolling();
      return;
    }

    wsConnection.binaryType = 'arraybuffer';

    wsConnection.onopen = () => {
      consecutiveErrors = 0;
      // Silence watchdog: the socket can be OPEN yet carry zero frames (e.g.
      // the recorder reconnected and the server-side subscription went stale).
      // A connected-but-mute socket must fall back to HTTP polling like a
      // failed one, or the player sits on "connecting" forever.
      clearTimeout(wsSilenceTimer);
      wsSilenceTimer = setTimeout(() => {
        if (!destroyed && useWebSocket && !frameSeenSinceOpen) {
          useWebSocket = false;
          stopWebSocket();
          startPolling();
        }
      }, WS_SILENCE_FALLBACK_MS);
      frameSeenSinceOpen = false;
    };

    wsConnection.onmessage = async (event) => {
      if (destroyed || !(event.data instanceof ArrayBuffer)) return;
      const data = new Uint8Array(event.data);
      if (data.length < 1) return;

      const msgType = data[0];
      if (msgType === 0x02) {
        frameSeenSinceOpen = true;
        clearTimeout(wsSilenceTimer);
      }
      // Video frame (0x02): nalus[0] contains the complete JPEG
      if (msgType === 0x02 && data.length >= 12) {
        const naluCount = (data[10] << 8) | data[11];
        if (naluCount < 1) return;
        let off = 12;
        const naluLen = (data[off] << 24) | (data[off + 1] << 16) | (data[off + 2] << 8) | data[off + 3];
        off += 4;
        if (off + naluLen > data.length) return;
        const jpegData = data.slice(off, off + naluLen);

        try {
          const blob = new Blob([jpegData], { type: 'image/jpeg' });
          revokeBlobUrl();
          currentBlobUrl = URL.createObjectURL(blob);
          if (imgEl) imgEl.src = currentBlobUrl;
          renderedOnce = true;

          lastLoadTime = Date.now();
          if (streamState === 'loading' || streamState === 'frozen') {
            streamState = 'playing';
            reconnectAttempts = 0;
            startFrozenDetection();
            onLoad?.();
          }
        } catch {
          // Corrupt JPEG — skip
        }
      }
    };

    wsConnection.onerror = () => {
      // WebSocket failed — fall back to HTTP polling
      if (useWebSocket) {
        useWebSocket = false;
        stopWebSocket();
        startPolling();
      }
    };

    wsConnection.onclose = () => {
      if (destroyed || !useWebSocket) return;
      // Reconnect with backoff (same pattern as polling)
      consecutiveErrors++;
      if (consecutiveErrors >= MAX_CONSECUTIVE_ERRORS) {
        consecutiveErrors = 0;
        useWebSocket = false;
        stopWebSocket();
        startPolling();
      } else {
        scheduleWsReconnect();
      }
    };
  }

  function stopWebSocket() {
    clearTimeout(wsSilenceTimer);
    wsSilenceTimer = null;
    if (wsConnection) {
      wsConnection.onopen = null;
      wsConnection.onmessage = null;
      wsConnection.onerror = null;
      wsConnection.onclose = null;
      try {
        wsConnection.close();
      } catch { /* already closed */ }
      wsConnection = null;
    }
  }

  function scheduleWsReconnect() {
    const base = reconnectDelays[Math.min(reconnectAttempts - 1, reconnectDelays.length - 1)];
    const delay = Math.round(base * (0.75 + Math.random() * 0.5));
    streamState = 'loading';
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (useWebSocket) startWebSocket();
    }, delay);
  }

  function startPolling() {
    stopPolling();
    consecutiveErrors = 0;
    lastEtag = null; // Reset ETag on new polling session
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
    reconnectAttempts++;
    const base = reconnectDelays[Math.min(reconnectAttempts - 1, reconnectDelays.length - 1)];
    const delay = Math.round(base * (0.75 + Math.random() * 0.5));
    streamState = 'loading';
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (useWebSocket) startWebSocket();
      else startPolling();
    }, delay);
  }

  function handleReconnect() {
    stopFrozenDetection();
    stopPolling();
    stopWebSocket();
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    reconnectAttempts = 0;
    consecutiveErrors = 0;
    useWebSocket = true;
    streamState = 'loading';
    startWebSocket();
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
    stopWebSocket();
    stopFrozenDetection();
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    reconnectAttempts = 0;
    consecutiveErrors = 0;
    useWebSocket = true;
    streamState = 'loading';

    // Stagger the first poll across cameras (100-500ms) so the fixed poll
    // cycles stay phase-offset: a short backend hiccup won't push every camera
    // past the consecutive-error threshold in the same window.
    const timer = setTimeout(() => {
      if (useWebSocket) startWebSocket();
      else startPolling();
    }, 100 + Math.random() * 400);
    return () => {
      clearTimeout(timer);
    };
  });

  onDestroy(() => {
    destroyed = true;
    stopPolling();
    stopWebSocket();
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
