<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Volume2, VolumeX } from 'lucide-svelte';
  import { getAuthHeader } from '$lib/api';
  import { AudioPlayer } from '$lib/audio-player';
  import {
    MsgType,
    decodeAudioCodecInfo,
    decodeAudioFrame,
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
    if (ws) return;
    try {
      ws = new WebSocket(buildUrl());
      ws.binaryType = 'arraybuffer';
    } catch (err) {
      console.error('[CameraAudioButton] WS error:', err);
      return;
    }

    ws.onmessage = (event: MessageEvent) => {
      if (!(event.data instanceof ArrayBuffer)) return;
      const data = event.data as ArrayBuffer;
      if (data.byteLength < 1) return;

      const msgType = new Uint8Array(data)[0];

      if (msgType === MsgType.AudioCodecInfo) {
        try {
          const info = decodeAudioCodecInfo(data);
          hasAudio = true;
          audioPlayer = new AudioPlayer(info.codec, info.sampleRate, info.channels);
          // Auto-init on arrival — user gesture was the click that triggered connect()
          audioPlayer.init().then(() => {
            muted = false;
            audioPlayer?.setMuted(false);
          });
        } catch (e) {
          console.error('[CameraAudioButton] codec info parse error:', e);
        }
      } else if (msgType === MsgType.AudioFrame) {
        if (audioPlayer?.initialized && !muted) {
          try {
            const frame = decodeAudioFrame(data);
            audioPlayer.pushFrame(frame.data);
          } catch {
            // parse error — skip
          }
        }
      } else if (msgType === MsgType.EOS) {
        cleanup();
      }
    };

    ws.onerror = () => {
      console.error('[CameraAudioButton] WS error');
    };

    ws.onclose = (ev: CloseEvent) => {
      console.log('[CameraAudioButton] WS closed:', ev.code);
      ws = null;
    };
  }

  function cleanup() {
    if (ws) { ws.close(); ws = null; }
    if (audioPlayer) { audioPlayer.destroy(); audioPlayer = null; }
    hasAudio = false;
    muted = true;
  }

  async function toggleMute(e: MouseEvent) {
    e.stopPropagation();

    if (!ws) {
      // First click: connect WS. Audio auto-starts when codec info arrives.
      connect();
      return;
    }

    // WS connected: toggle mute
    muted = !muted;
    if (audioPlayer) {
      if (!audioPlayer.initialized) {
        await audioPlayer.init();
      }
      audioPlayer.setMuted(muted);
    }
  }

  onDestroy(() => cleanup());
</script>

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
