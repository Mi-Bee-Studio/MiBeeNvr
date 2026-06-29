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

    ws = new WebSocket(buildUrl());
    ws.binaryType = 'arraybuffer';

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

    ws.onerror = () => {
      connecting = false;
    };

    ws.onclose = () => {
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
      // First click: connect and initialize
      connect();
      // Wait for audio codec info, then init on next click
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

{#if hasAudio || connecting}
  <button
    onclick={toggleMute}
    class="absolute top-2 right-10 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all z-10 {className}"
    title={muted ? '取消静音' : '静音'}
  >
    {#if muted}
      <VolumeX size={16} />
    {:else}
      <Volume2 size={16} />
    {/if}
  </button>
{/if}
