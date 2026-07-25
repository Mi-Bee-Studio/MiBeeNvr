<script lang="ts">
  import { onDestroy, getContext, untrack } from 'svelte';
  import { t } from '$lib/i18n';
  import { AlertCircle, RefreshCw, ImageIcon } from 'lucide-svelte';
  import CameraAudioButton from './CameraAudioButton.svelte';
  import { getAuthHeader, getTokenForUrl } from '$lib/api';
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
    onProtocolFailed,
    hasAudio = false,
  }: {
    cameraId: string;
    cameraName: string;
    expanded?: boolean;
    tabVisible?: boolean;
    /** See FlvPlayer.onProtocolFailed — lets the grid demote to another
     *  real-time protocol before this player falls back to a snapshot. */
    onProtocolFailed?: () => boolean;
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
  }
  type WebrtcState = 'connecting' | 'connected' | 'disconnected' | 'failed';

  let streamState: StreamState | 'loading' = $state('loading');
  let webrtcState: WebrtcState = $state('connecting');
  let videoEl: HTMLVideoElement | undefined = $state();
  let pc: RTCPeerConnection | null = null;
  let sessionUrl: string | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectAttempts = 0;
  const maxReconnectAttempts = 5;
  const reconnectDelays = [2000, 4000, 8000, 16000, 32000];
  // Connection-establishment timeout: if a PeerConnection doesn't reach a real
  // frame (firstFramePlayed) within CONNECT_TIMEOUT_MS, treat it as failed and
  // scheduleReconnect(). Covers the case where ICE/WHEP succeeds enough to not
  // error, but no media ever flows (Xiaomi CS2), and neither zombie (needs
  // connected) nor WHEP-abort (needs hang) catches it.
  let connectTimeoutTimer: ReturnType<typeof setTimeout> | null = null;
  const CONNECT_TIMEOUT_MS = 15000;
  // True only after a real video frame has actually played. ICE 'connected'
  // fires even when the backend has no media to send (e.g. Xiaomi CS2 cameras
  // where the WHEP handler accepts the PeerConnection but produces no frames),
  // so we MUST NOT reset reconnectAttempts / restart the zombie detector on
  // 'connected' alone — that reset is what made the zombie loop infinite
  // (connected→reset→zombie→reconnect→connected→reset…). Reset only on a frame.
  let firstFramePlayed = false;

  // Zombie detection
  let zombieInterval: ReturnType<typeof setInterval> | null = null;
  let lastPlaybackTime = 0;
  let zombieCount = 0;
let destroyed = false;

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

  function updateStreamState() {
    const prevState = streamState;
    if (webrtcState === 'connected') {
      streamState = 'playing';
      if (prevState !== 'playing' && frozenFrameUrl) clearFreezeFrame();
      if (coordinator && hasActiveCoordinatedReconnect) {
        coordinator.completeReconnect(cameraId);
        hasActiveCoordinatedReconnect = false;
      }
    } else if (webrtcState === 'connecting') {
      if (prevState === 'playing') captureFreezeFrame();
      streamState = 'buffering';
    } else if (webrtcState === 'disconnected') {
      if (prevState === 'playing') captureFreezeFrame();
      streamState = 'buffering';
    } else {
      if (prevState === 'playing') captureFreezeFrame();
      streamState = 'error';
    }
  }

  $effect(() => {
    updateStreamState();
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
      // Reset the frame flag for the new connection — a fresh PeerConnection
      // hasn't delivered a frame yet. firstFramePlayed is set true again only
      // when videoEl fires 'playing' for this new stream.
      firstFramePlayed = false;
      destroyPeerConnection();
      initWebRTC();
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
    // Ask the grid to demote to the next real-time protocol first (e.g. WebRTC→FLV).
    // Only when it declines (chain exhausted) do we actually enter snapshot mode.
    if (onProtocolFailed?.() === true) return;
    console.warn(`WebRTC max retries reached for ${cameraId}, entering snapshot fallback`);
    sendTelemetry('stream_degradation', cameraId, undefined, { protocol: 'webrtc', target: 'snapshot' });
    snapshotMode = true;
    streamState = 'snapshot';
    destroyPeerConnection();
    refreshSnapshot();
    snapshotRefreshTimer = setInterval(refreshSnapshot, SNAPSHOT_REFRESH_MS);
    snapshotRestoreTimer = setTimeout(exitSnapshotMode, SNAPSHOT_RESTORE_MS);
  }

  function refreshSnapshot() {
    let url = getSnapshotUrl(cameraId);
    // ?token= carries the bare session token (mbs_...), NOT a "Bearer ..." header.
    const token = getTokenForUrl();
    if (token) {
      url += `?token=${encodeURIComponent(token)}`;
    }
    snapshotSrc = `${url}&_t=${Date.now()}`;
  }

  function exitSnapshotMode() {
    stopSnapshotMode();
    reconnectAttempts = 0;
    streamState = 'loading';
    webrtcState = 'connecting';
    initWebRTC();
  }

  function stopSnapshotMode() {
    snapshotMode = false;
    snapshotSrc = '';
    if (snapshotRefreshTimer) { clearInterval(snapshotRefreshTimer); snapshotRefreshTimer = null; }
    if (snapshotRestoreTimer) { clearTimeout(snapshotRestoreTimer); snapshotRestoreTimer = null; }
  }

  function destroyPeerConnection() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    if (connectTimeoutTimer) {
      clearTimeout(connectTimeoutTimer);
      connectTimeoutTimer = null;
    }
    if (zombieInterval) {
      clearInterval(zombieInterval);
      zombieInterval = null;
    }

    // Send DELETE to WHEP session for cleanup
    if (sessionUrl) {
      const url = sessionUrl;
      const authHeader = getAuthHeader();
      fetch(url, {
        method: 'DELETE',
        headers: authHeader ? { Authorization: authHeader } : {},
      }).catch(() => {});
      sessionUrl = null;
    }

    if (pc) {
      try {
        pc.close();
      } catch (e) { console.warn('WebRTC close error:', e); }
      pc = null;
    }
  }

  function startZombieDetector() {
    if (zombieInterval) clearInterval(zombieInterval);
    lastPlaybackTime = Date.now();
    zombieCount = 0;

    zombieInterval = setInterval(() => {
      if (!videoEl || !pc) return;
      const now = Date.now();

      // Check if video is stuck (readyState 0 for >20s or no playback progress for >60s)
      if (videoEl.readyState === 0) {
        zombieCount++;
        if (zombieCount >= 4) {
          // ~20s stuck — reconnect. Route through scheduleReconnect() so this
          // counts toward reconnectAttempts and eventually triggers
          // enterSnapshotMode() → onProtocolFailed() → orchestrator demote.
          // (Previously this reset zombieCount but never incremented
          // reconnectAttempts, so a camera WebRTC can't serve — e.g. a Xiaomi
          // CS2 camera where the WHEP endpoint 404s — looped here forever,
          // ~90 "zombie detected" logs and never degraded.)
          console.warn(`WebRTC zombie detected for ${cameraId}, reconnecting`);
          zombieCount = 0;
          scheduleReconnect();
          return;
        }
      } else if (videoEl.currentTime !== lastPlaybackTime) {
        lastPlaybackTime = videoEl.currentTime;
        zombieCount = 0;
      } else {
        zombieCount++;
        if (zombieCount >= 12) {
          // ~60s no progress — reconnect (also counts toward the limit).
          console.warn(`WebRTC zombie (no progress) for ${cameraId}, reconnecting`);
          zombieCount = 0;
          scheduleReconnect();
          return;
        }
      }
    }, 5000);
  }

  async function initWebRTC() {
    if (!videoEl) return;

    // Clear any stale zombie detector
    if (zombieInterval) { clearInterval(zombieInterval); zombieInterval = null; }

    if (!videoEl) return;
    if (destroyed) return;

    streamState = 'loading';
    webrtcState = 'connecting';

    // Arm the connection-establishment timeout: if no real frame arrives within
    // CONNECT_TIMEOUT_MS, abort and reconnect. Cleared on first frame or when
    // the connection fails (both lead to scheduleReconnect or success).
    if (connectTimeoutTimer) { clearTimeout(connectTimeoutTimer); connectTimeoutTimer = null; }
    connectTimeoutTimer = setTimeout(() => {
      connectTimeoutTimer = null;
      if (!firstFramePlayed && !destroyed) {
        webrtcState = 'failed';
        scheduleReconnect();
      }
    }, CONNECT_TIMEOUT_MS);

    try {
      const iceServers: RTCConfiguration = {
        iceServers: [], // WHEP typically uses no STUN/TURN for LAN
      };

      const peerConnection = new RTCPeerConnection(iceServers);
      pc = peerConnection;

      // Add recvonly transceiver for video
      peerConnection.addTransceiver('video', { direction: 'recvonly' });
      // Add recvonly transceiver for audio (will be rejected by server if no audio)
      peerConnection.addTransceiver('audio', { direction: 'recvonly' });

      // Handle incoming tracks
      peerConnection.ontrack = (event) => {
        if (event.streams && event.streams[0] && videoEl) {
          videoEl.srcObject = event.streams[0];
          // A frame actually playing is the ONLY reliable signal that this
          // protocol works for this camera. Mark firstFramePlayed so the zombie
          // detector + reconnectAttempts reset can proceed. ICE 'connected'
          // alone is NOT sufficient (Xiaomi CS2: PeerConnection connects but
          // the backend never produces media).
          const onPlaying = () => {
            firstFramePlayed = true;
            reconnectAttempts = 0;
            // A real frame arrived — clear the connection-establishment timeout.
            if (connectTimeoutTimer) { clearTimeout(connectTimeoutTimer); connectTimeoutTimer = null; }
            videoEl?.removeEventListener('playing', onPlaying);
          };
          videoEl.addEventListener('playing', onPlaying);
          videoEl.play().catch(() => {});
        }
      };

      // Handle connection state changes
      peerConnection.onconnectionstatechange = () => {
        const state = peerConnection.connectionState;
        switch (state) {
          case 'connected':
            webrtcState = 'connected';
            // Do NOT reset reconnectAttempts or start zombie detection here —
            // 'connected' fires even with no media (Xiaomi CS2). Wait for a
            // real frame (firstFramePlayed, set in ontrack's 'playing' handler)
            // before trusting the connection. Start zombie detection anyway so
            // a frame-less 'connected' gets caught and demoted.
            if (firstFramePlayed) {
              reconnectAttempts = 0;
            }
            startZombieDetector();
            break;
          case 'disconnected':
            webrtcState = 'disconnected';
            scheduleReconnect();
            break;
          case 'failed':
            webrtcState = 'failed';
            scheduleReconnect();
            break;
          case 'closed':
            break;
        }
      };

      // Create offer
      const offer = await peerConnection.createOffer();
      await peerConnection.setLocalDescription(offer);
      if (destroyed) return;

      // Wait for ICE gathering to complete
      await new Promise<void>((resolve) => {
        if (peerConnection.iceGatheringState === 'complete') {
          resolve();
        } else {
          const iceAc = new AbortController();
          peerConnection.addEventListener('icegatheringstatechange', () => {
            if (peerConnection.iceGatheringState === 'complete') {
              iceAc.abort();
              resolve();
            }
          }, { signal: iceAc.signal });
          // Timeout fallback (5s)
          setTimeout(resolve, 5000);
        }
      });
      if (destroyed) return;

      // Send SDP offer to WHEP endpoint
      const authHeader = getAuthHeader();
      const headers: Record<string, string> = {
        'Content-Type': 'application/sdp',
      };
      if (authHeader) headers['Authorization'] = authHeader;

      // Send SDP offer to WHEP endpoint. Abort after 10s — without this, a
      // backend that hangs on the WHEP POST (or accepts it but never responds)
      // leaves initWebRTC() awaiting forever: no onconnectionstatechange, no
      // zombie detector, no reconnect → silent permanent black screen (the
      // user-reported "WebRTC badge but black, no errors, no degradation").
      const whepAbort = new AbortController();
      const whepTimeout = setTimeout(() => whepAbort.abort(), 10000);
      let response: Response;
      try {
        response = await fetch(`/api/cameras/${cameraId}/stream/webrtc`, {
          method: 'POST',
          headers,
          body: peerConnection.localDescription!.sdp,
          signal: whepAbort.signal,
        });
      } finally {
        clearTimeout(whepTimeout);
      }

      if (!response.ok) {
        const errorText = await response.text().catch(() => 'Unknown error');
        throw new Error(`WHEP server error: ${response.status} ${errorText}`);
      }
      if (destroyed) return;

      // Parse Location header for session URL (for DELETE on cleanup)
      const location = response.headers.get('Location');
      if (location) {
        // Make absolute if relative
        sessionUrl = location.startsWith('/') ? location : `/api/cameras/${cameraId}/stream/webrtc/${location}`;
      }

      // Parse SDP answer
      const answerSDP = await response.text();
      if (destroyed) return;
      const answer = new RTCSessionDescription({
        type: 'answer',
        sdp: answerSDP,
      });

      await peerConnection.setRemoteDescription(answer);
    } catch (e) {
      console.warn('WebRTC init failed:', e);
      webrtcState = 'failed';
      scheduleReconnect();
    }
  }

  function handleReconnect() {
    stopSnapshotMode();
    reconnectAttempts = 0;
    webrtcState = 'connecting';
    captureFreezeFrame();
    coordinatedReconnect(() => {
      destroyPeerConnection();
      initWebRTC();
    });
  }

  // Main lifecycle
  $effect(() => {
    const _id = cameraId;
    if (!_id) return;

    stopSnapshotMode();
    destroyPeerConnection();
    streamState = 'loading';
    webrtcState = 'connecting';

    const timer = setTimeout(() => initWebRTC(), 50);
    return () => {
      clearTimeout(timer);
      destroyPeerConnection();
    };
  });

  // Visibility change handler
  // Coordinated visibility — pause when tab hidden, resume when visible
  // Replaces per-player visibilitychange listener; Dashboard owns the signal
  $effect(() => {
    const visible = tabVisible;
    if (!visible) {
      // Tab hidden — close peer connection to release decode resources
      if (pc && !destroyed) {
        try { pc.close(); } catch { /* ignore */ }
        pc = null;
      }
    } else {
      // Tab visible — resume: rebuild WebRTC connection
      // Use untrack to avoid re-triggering on streamState changes (would cause infinite loop)
      untrack(() => {
        if (!destroyed && streamState !== 'loading') {
          stopSnapshotMode();
          reconnectAttempts = 0;
          captureFreezeFrame();
          destroyPeerConnection();
          initWebRTC();
        }
      });
    }
  });

  onDestroy(() => {
    destroyed = true;
    stopSnapshotMode();
    if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
    if (coordinator) coordinator.cancelRequest(cameraId);
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    frozenFrameUrl = null;
    destroyPeerConnection();
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
            : t('live.webrtc.connecting'),
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
      <span class="text-white/50 text-xs">WebRTC</span>
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
