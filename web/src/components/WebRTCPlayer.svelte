<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { AlertCircle, RefreshCw } from 'lucide-svelte';
  import { getAuthHeader } from '$lib/api';
  import type { StreamState } from '$lib/hls-errors';

  let {
    cameraId,
    cameraName,
    expanded = false,
  }: {
    cameraId: string;
    cameraName: string;
    expanded?: boolean;
  } = $props();

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

  // Zombie detection
  let zombieInterval: ReturnType<typeof setInterval> | null = null;
  let lastPlaybackTime = 0;
  let zombieCount = 0;

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
    if (webrtcState === 'connected') {
      streamState = 'playing';
    } else if (webrtcState === 'connecting') {
      streamState = 'buffering';
    } else if (webrtcState === 'disconnected') {
      streamState = 'buffering';
    } else {
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
    if (reconnectAttempts >= maxReconnectAttempts) return;
    reconnectAttempts++;
    const delay = getBackoffDelay();
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      destroyPeerConnection();
      initWebRTC();
    }, delay);
  }

  function destroyPeerConnection() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
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
          // ~20s stuck — reconnect
          console.warn(`WebRTC zombie detected for ${cameraId}, reconnecting`);
          zombieCount = 0;
          destroyPeerConnection();
          initWebRTC();
          return;
        }
      } else if (videoEl.currentTime !== lastPlaybackTime) {
        lastPlaybackTime = videoEl.currentTime;
        zombieCount = 0;
      } else {
        zombieCount++;
        if (zombieCount >= 12) {
          // ~60s no progress — reconnect
          console.warn(`WebRTC zombie (no progress) for ${cameraId}, reconnecting`);
          zombieCount = 0;
          reconnectAttempts = 0;
          destroyPeerConnection();
          initWebRTC();
          return;
        }
      }
    }, 5000);
  }

  async function initWebRTC() {
    if (!videoEl) return;

    streamState = 'loading';
    webrtcState = 'connecting';

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
          videoEl.play().catch(() => {});
        }
      };

      // Handle connection state changes
      peerConnection.onconnectionstatechange = () => {
        const state = peerConnection.connectionState;
        switch (state) {
          case 'connected':
            webrtcState = 'connected';
            reconnectAttempts = 0;
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

      // Wait for ICE gathering to complete
      await new Promise<void>((resolve) => {
        if (peerConnection.iceGatheringState === 'complete') {
          resolve();
        } else {
          const handler = () => {
            if (peerConnection.iceGatheringState === 'complete') {
              peerConnection.removeEventListener('icegatheringstatechange', handler);
              resolve();
            }
          };
          peerConnection.addEventListener('icegatheringstatechange', handler);

          // Timeout fallback (5s)
          setTimeout(resolve, 5000);
        }
      });

      // Send SDP offer to WHEP endpoint
      const authHeader = getAuthHeader();
      const headers: Record<string, string> = {
        'Content-Type': 'application/sdp',
      };
      if (authHeader) headers['Authorization'] = authHeader;

      const response = await fetch(`/api/cameras/${cameraId}/stream/webrtc`, {
        method: 'POST',
        headers,
        body: peerConnection.localDescription!.sdp,
      });

      if (!response.ok) {
        const errorText = await response.text().catch(() => 'Unknown error');
        throw new Error(`WHEP server error: ${response.status} ${errorText}`);
      }

      // Parse Location header for session URL (for DELETE on cleanup)
      const location = response.headers.get('Location');
      if (location) {
        // Make absolute if relative
        sessionUrl = location.startsWith('/') ? location : `/api/cameras/${cameraId}/stream/webrtc/${location}`;
      }

      // Parse SDP answer
      const answerSDP = await response.text();
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
    reconnectAttempts = 0;
    webrtcState = 'connecting';
    destroyPeerConnection();
    initWebRTC();
  }

  // Main lifecycle
  $effect(() => {
    const _id = cameraId;
    if (!_id) return;

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
  $effect(() => {
    let wasHidden = false;

    const handler = () => {
      if (document.hidden) {
        wasHidden = true;
      } else if (wasHidden) {
        wasHidden = false;
        reconnectAttempts = 0;
        destroyPeerConnection();
        initWebRTC();
      }
    };

    document.addEventListener('visibilitychange', handler);
    return () => {
      document.removeEventListener('visibilitychange', handler);
    };
  });

  onDestroy(() => {
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
          : t('live.webrtc.connecting'),
  );
</script>

<!-- svelte-ignore binding_property_non_reactive -->
<div class="relative w-full h-full bg-black overflow-hidden group">
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
