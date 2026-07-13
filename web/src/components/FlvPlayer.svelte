<script lang="ts">
  import { onDestroy, getContext } from 'svelte';
  import { t } from '$lib/i18n';
  import { AlertCircle, RefreshCw, ImageIcon } from 'lucide-svelte';
  import CameraAudioButton from './CameraAudioButton.svelte';
  import { getAuthHeader } from '$lib/api';
  import { getSnapshotUrl } from '$lib/api/cameras';
  import { captureFrame } from '$lib/freeze-frame';
  import { sendTelemetry } from '$lib/telemetry';
  import type { StreamState } from '$lib/hls-errors';
  import type { ReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';

  let {
    cameraId,
    cameraName,
    expanded = false,
    tabVisible = true,
    hasAudio = false,
    onProtocolFailed,
  }: {
    cameraId: string;
    cameraName: string;
    expanded?: boolean;
    tabVisible?: boolean;
    hasAudio?: boolean;
    /** Called when the player exhausts reconnects. Return true if the parent
     *  is demoting to another real-time protocol (it will remount this player);
     *  return false (or omit) to let this player fall back to a snapshot. */
    onProtocolFailed?: () => boolean;
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
  }
  let streamState: StreamState | 'loading' = $state('loading');
  let videoEl: HTMLVideoElement | undefined = $state();
  let mpegtsPlayer: any = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectAttempts = 0;
  const maxReconnectAttempts = 5;
  const reconnectDelays = [2000, 4000, 8000, 16000, 32000];

  // Zombie detection
  let zombieInterval: ReturnType<typeof setInterval> | null = null;
  let lastPlaybackTime = 0;
  let zombieCount = 0;
let destroyed = false;
let videoEventAc: AbortController | null = null;

  // Freeze frame — prevents black flash during reconnection
  let frozenFrameUrl: string | null = $state(null);
  let showFrozenFrame = $state(false);
  let freezeClearTimer: ReturnType<typeof setTimeout> | null = null;

  // Snapshot fallback — degrades to static snapshots after max reconnect failures
  let snapshotMode = $state(false);
  let snapshotSrc = $state('');
  let snapshotRefreshTimer: ReturnType<typeof setInterval> | null = null;
  let snapshotRestoreTimer: ReturnType<typeof setTimeout> | null = null;
  const SNAPSHOT_REFRESH_MS = 2000;
  const SNAPSHOT_RESTORE_MS = 5 * 60 * 1000;

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
    const event = new CustomEvent('statechange', {
      bubbles: true,
      detail: { cameraId, state },
    });
    videoEl?.parentElement?.dispatchEvent(event);
  }

  $effect(() => {
    dispatchStateChange(streamState);
  });

  function getBackoffDelay(): number {
    if (reconnectAttempts >= maxReconnectAttempts) return reconnectDelays[reconnectDelays.length - 1];
    return reconnectDelays[reconnectAttempts];
  }

  function scheduleReconnect() {
    if (reconnectAttempts >= maxReconnectAttempts) {
      enterSnapshotMode();
      return;
    }
    captureFreezeFrame();
    reconnectAttempts++;

    const doReconnect = () => {
      reconnectTimer = null;
      destroyPlayer();
      initFlv();
    };

    if (coordinator) {
      // Use coordinator's delay instead of per-player backoff
      if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
      hasActiveCoordinatedReconnect = true;
      const delay = coordinator.requestReconnect(cameraId, (grantedDelay) => {
        coordinatedTimer = setTimeout(doReconnect, grantedDelay);
      });
      if (delay >= 0) {
        coordinatedTimer = setTimeout(doReconnect, delay);
      }
      return;
    }

    const delay = getBackoffDelay();
    reconnectTimer = setTimeout(doReconnect, delay);
  }

  function enterSnapshotMode() {
    if (snapshotMode) return;
    // Ask the grid to demote to the next real-time protocol first (e.g. FLV→HLS).
    // Only when it declines (chain exhausted) do we actually enter snapshot mode.
    if (onProtocolFailed?.() === true) return;
    console.warn(`FLV max retries reached for ${cameraId}, entering snapshot fallback`);
    sendTelemetry('stream_degradation', cameraId, undefined, { protocol: 'flv', target: 'snapshot' });
    snapshotMode = true;
    streamState = 'snapshot';
    destroyPlayer();
    refreshSnapshot();
    snapshotRefreshTimer = setInterval(refreshSnapshot, SNAPSHOT_REFRESH_MS);
    snapshotRestoreTimer = setTimeout(exitSnapshotMode, SNAPSHOT_RESTORE_MS);
  }

  function refreshSnapshot() {
    const authHeader = getAuthHeader();
    let url = getSnapshotUrl(cameraId);
    if (authHeader) {
      const token = authHeader.replace('Basic ', '');
      url += `?token=${encodeURIComponent(token)}`;
    }
    snapshotSrc = `${url}&_t=${Date.now()}`;
  }

  function exitSnapshotMode() {
    stopSnapshotMode();
    reconnectAttempts = 0;
    streamState = 'loading';
    initFlv();
  }

  function stopSnapshotMode() {
    snapshotMode = false;
    snapshotSrc = '';
    if (snapshotRefreshTimer) { clearInterval(snapshotRefreshTimer); snapshotRefreshTimer = null; }
    if (snapshotRestoreTimer) { clearTimeout(snapshotRestoreTimer); snapshotRestoreTimer = null; }
  }

  function destroyPlayer() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    if (zombieInterval) {
      clearInterval(zombieInterval);
      zombieInterval = null;
    }
    if (videoEventAc) {
      videoEventAc.abort();
      videoEventAc = null;
    }
    if (mpegtsPlayer) {
      try {
        mpegtsPlayer.pause();
        mpegtsPlayer.unload();
        mpegtsPlayer.detachMediaElement();
        mpegtsPlayer.destroy();
      } catch (e) { console.warn('mpegts.js destroy error:', e); }
      mpegtsPlayer = null;
    }
  }

  function startZombieDetector() {
    if (zombieInterval) clearInterval(zombieInterval);
    lastPlaybackTime = 0;
    zombieCount = 0;

    zombieInterval = setInterval(() => {
      if (!videoEl) return;

      if (videoEl.readyState === 0) {
        zombieCount++;
      if (zombieCount >= 4) {
          console.warn(`FLV zombie detected for ${cameraId}, reconnecting`);
          zombieCount = 0;
          reconnectAttempts = 0;
          captureFreezeFrame();
          destroyPlayer();
          initFlv();
          return;
        }
      } else if (videoEl.currentTime !== lastPlaybackTime) {
        lastPlaybackTime = videoEl.currentTime;
        zombieCount = 0;
      } else {
        zombieCount++;
        if (zombieCount >= 12) {
          console.warn(`FLV zombie (no progress) for ${cameraId}, reconnecting`);
          zombieCount = 0;
          reconnectAttempts = 0;
          captureFreezeFrame();
          destroyPlayer();
          initFlv();
          return;
        }
      }
    }, 5000);
  }

  async function initFlv() {
    if (!videoEl) return;
    if (destroyed) return;

    streamState = 'loading';

    try {
      const mpegts = await import('mpegts.js');
      if (destroyed) return;

      if (!mpegts.default.isSupported()) {
        console.warn('mpegts.js not supported');
        streamState = 'error';
        return;
      }

      const url = `/api/cameras/${cameraId}/stream.flv`;
      const authHeader = getAuthHeader();

      const player = mpegts.default.createPlayer({
        type: 'flv',
        isLive: true,
        url,
        hasAudio,
        hasVideo: true,
        cors: false,
      }, {
        headers: authHeader ? { Authorization: authHeader } : undefined,
        enableStashBuffer: false,
        stashInitialSize: 128,
        lazyLoadMaxDuration: 3 * 60,
        seekType: 'range',
        liveBufferLatencyChasing: true,
        liveBufferLatencyChasingOnPaused: true,
        liveSyncDurationCount: 3,
        liveMaxLatencyDurationCount: 6,
      });

      mpegtsPlayer = player;
      streamState = 'buffering';
      reconnectAttempts = 0;

      player.attachMediaElement(videoEl);
      player.load();
      player.play().catch(() => {});

      player.on(mpegts.default.Events.ERROR, (_event: string, data: any) => {
        console.warn('mpegts.js error:', data);
        if (data && data.type === mpegts.default.ErrorTypes.NETWORK_ERROR) {
          scheduleReconnect();
        } else if (data && data.type === mpegts.default.ErrorTypes.MEDIA_ERROR) {
          // Try to recover
          try {
            player.pause();
            player.unload();
            player.load();
            player.play().catch(() => {});
          } catch {
            scheduleReconnect();
          }
        } else {
          scheduleReconnect();
        }
      });

      player.on(mpegts.default.Events.LOADING_COMPLETE, () => {
        streamState = 'error';
        scheduleReconnect();
      });

      player.on(mpegts.default.Events.STATISTICS_INFO, () => {
      // Detect playing state from video element
      if (videoEl && videoEl.readyState >= 2) {
        if (frozenFrameUrl) clearFreezeFrame();
        if (coordinator && hasActiveCoordinatedReconnect) {
          coordinator.completeReconnect(cameraId);
          hasActiveCoordinatedReconnect = false;
        }
        streamState = 'playing';
        startZombieDetector();
      }
      });

      // Fallback: detect playing via video events
      const ac = new AbortController();
      videoEventAc = ac;
      videoEl.addEventListener('playing', () => {
        if (frozenFrameUrl) clearFreezeFrame();
        if (coordinator && hasActiveCoordinatedReconnect) {
          coordinator.completeReconnect(cameraId);
          hasActiveCoordinatedReconnect = false;
        }
        streamState = 'playing';
        startZombieDetector();
      }, { signal: ac.signal });
      videoEl.addEventListener('waiting', () => {
        if (streamState === 'playing') captureFreezeFrame();
        if (streamState !== 'error') streamState = 'buffering';
      }, { signal: ac.signal });
    } catch (e) {
      console.warn('FLV init failed:', e);
      streamState = 'error';
      scheduleReconnect();
    }
  }

  function handleReconnect() {
    stopSnapshotMode();
    reconnectAttempts = 0;
    captureFreezeFrame();
    coordinatedReconnect(() => {
      destroyPlayer();
      initFlv();
    });
  }

  // Main lifecycle
  $effect(() => {
    const _id = cameraId;
    if (!_id) return;

    stopSnapshotMode();
    destroyPlayer();
    streamState = 'loading';

    const timer = setTimeout(() => initFlv(), 50);
    return () => {
      clearTimeout(timer);
      destroyPlayer();
    };
  });

  // Coordinated visibility — pause when tab hidden, resume when visible
  // Replaces per-player visibilitychange listener; Dashboard owns the signal
  $effect(() => {
    const visible = tabVisible;
    if (!visible) {
      // Tab hidden — pause player to release decode resources
      if (mpegtsPlayer && !destroyed) {
        try { mpegtsPlayer.pause(); } catch { /* ignore */ }
      }
    } else {
      // Tab visible — resume: rebuild stream for fresh state
      if (!destroyed && streamState !== 'loading') {
        stopSnapshotMode();
        reconnectAttempts = 0;
        captureFreezeFrame();
        destroyPlayer();
        initFlv();
      }
    }
  });

  onDestroy(() => {
    destroyed = true;
    stopSnapshotMode();
    if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
    if (coordinator) coordinator.cancelRequest(cameraId);
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    frozenFrameUrl = null;
    destroyPlayer();
  });

  // Derived
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
          : streamState === 'snapshot'
            ? 'opacity-0 pointer-events-none'
            : 'opacity-0 pointer-events-none',
  );
  let dotColor = $derived(
    streamState === 'playing'
      ? 'bg-green-500'
      : streamState === 'buffering'
        ? 'bg-yellow-500 animate-pulse'
        : streamState === 'snapshot'
          ? 'bg-blue-500 animate-pulse'
          : streamState === 'error'
            ? 'bg-red-500'
            : 'bg-gray-400',
  );
  let dotTitle = $derived(
    streamState === 'playing'
      ? t('dashboard.live')
      : streamState === 'buffering'
        ? t('dashboard.buffering')
        : streamState === 'snapshot'
          ? t('dashboard.snapshotMode')
          : streamState === 'error'
            ? t('dashboard.errorState')
            : t('live.flv.connecting'),
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

  {#if snapshotMode}
    <img
      src={snapshotSrc}
      alt="{cameraName} snapshot"
      class="absolute inset-0 w-full h-full object-contain"
    />
    <!-- Snapshot mode indicator -->
    <div class="absolute top-8 left-2 flex items-center gap-1.5 px-2 py-1 rounded bg-blue-500/20 border border-blue-400/30 z-20">
      <ImageIcon size={12} class="text-blue-400" />
      <span class="text-blue-300 text-xs">{t('live.snapshot.fallback')}</span>
    </div>
    <button
      onclick={handleReconnect}
      class="absolute bottom-14 left-1/2 -translate-x-1/2 flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-white/10 text-white/80 text-xs hover:bg-white/20 transition-colors z-20"
    >
      <RefreshCw size={12} />
      {t('common.retry')}
    </button>
  {:else}
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
      <div class="relative flex items-center gap-2">
        <div class="w-3 h-3 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
        <span class="text-white/50 text-xs">{t('live.loading')}</span>
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
      <span class="text-white/50 text-xs">HTTP-FLV</span>
    </div>
  </div>

  <!-- Audio button (top-right, before expand) — hidden for video-only cameras -->
  {#if hasAudio}
    <CameraAudioButton {cameraId} />
  {/if}

  <!-- Expand/Shrink -->
  {#if expanded}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        videoEl?.parentElement?.dispatchEvent(new CustomEvent('shrink', { bubbles: true, detail: { cameraId } }));
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
        videoEl?.parentElement?.dispatchEvent(new CustomEvent('expand', { bubbles: true, detail: { cameraId } }));
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
