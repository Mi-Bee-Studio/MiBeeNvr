<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Volume2, VolumeX } from 'lucide-svelte';
  import { getAuthHeader } from '$lib/api';
  import { AudioPlayer } from '$lib/audio-player';
  import {
    MsgType,
    decodeAudioCodecInfo,
    decodeAudioFrame,
    type AudioCodecInfo,
    type AudioFrame,
  } from '$lib/webcodecs-player/protocol';

  let {
    cameraId,
    class: className = '',
  }: {
    cameraId: string;
    class?: string;
  } = $props();

  let ws: WebSocket | null = null;
  let audioPlayer: AudioPlayer | null = null;
  let hasAudio = $state(false);
  let muted = $state(true);
  let connecting = $state(false);

  function buildUrl(): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    let url = `${proto}//${location.host}/api/cameras/${cameraId}/stream/ws?audio_only=1`;
    const authHeader = getAuthHeader();
    if (authHeader) {
      const token = authHeader.startsWith('Basic ') ? authHeader.slice(6) : authHeader;
      url += `&token=${encodeURIComponent(token)}`;
    }
    return url;
  }

  function connect() {
    if (ws || connecting) return;
    connecting = true;
    const url = buildUrl();
    console.log('[CameraAudioButton] Connecting to', url);
    try {
      ws = new WebSocket(url);
    } catch (err) {
      console.error('[CameraAudioButton] WS constructor error:', err);
      connecting = false;
      return;
    }
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      console.log('[CameraAudioButton] WS connected');
    };

    ws.onmessage = (event: MessageEvent) => {
      if (!(event.data instanceof ArrayBuffer)) return;
      const data = event.data as ArrayBuffer;
      if (data.byteLength < 1) return;

      const msgType = new Uint8Array(data)[0];

      if (msgType === MsgType.AudioCodecInfo) {
        try {
          const info: AudioCodecInfo = decodeAudioCodecInfo(data);
          hasAudio = true;
          audioPlayer = new AudioPlayer(info.codec, info.sampleRate, info.channels);
        } catch {
          // parse error
        }
      } else if (msgType === MsgType.AudioFrame) {
        if (audioPlayer?.initialized) {
          try {
            const frame: AudioFrame = decodeAudioFrame(data);
            audioPlayer.pushFrame(frame.data);
          } catch {
            // parse error
          }
        }
      } else if (msgType === MsgType.EOS) {
        disconnect();
      }
    };

    ws.onerror = (ev: Event) => {
      console.error('[CameraAudioButton] WS error');
      connecting = false;
    };

    ws.onclose = (ev: CloseEvent) => {
      console.log('[CameraAudioButton] WS closed:', ev.code, ev.reason);
      connecting = false;
      ws = null;
    };
  }

  function disconnect() {
    if (ws) {
      ws.close();
      ws = null;
    }
    if (audioPlayer) {
      audioPlayer.destroy();
      audioPlayer = null;
    }
    hasAudio = false;
    muted = true;
  }

  async function toggleMute(e: MouseEvent) {
    e.stopPropagation();
    if (!hasAudio) {
      // First click: connect WS, wait for audio codec info
      connect();
      // Wait up to 3s for audio codec info to arrive
      for (let i = 0; i < 30 && !hasAudio; i++) {
        await new Promise(r => setTimeout(r, 100));
      }
      if (hasAudio && audioPlayer) {
        await audioPlayer.init();
        muted = false;
        audioPlayer.setMuted(false);
      }
      return;
    }
    if (!audioPlayer) return;
    if (!audioPlayer.initialized) {
      await audioPlayer.init();
    }
    muted = !muted;
    audioPlayer.setMuted(muted);
  }

  onDestroy(() => {
    disconnect();
  });
</script>

<button
  onclick={toggleMute}
  class="absolute top-2 right-10 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all z-10 {className}"
  title={muted ? '取消静音' : '静音'}
>
  {#if connecting}
    <span class="text-xs">...</span>
  {:else if muted}
    <VolumeX size={16} />
  {:else}
    <Volume2 size={16} />
  {/if}
</button>
