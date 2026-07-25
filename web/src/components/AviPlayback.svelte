<script lang="ts">
  import { AudioPlayer, AudioCodec } from '$lib/audio-player';
  import { getTokenForUrl } from '$lib/api';
  import { t } from '$lib/i18n';

  let {
    recordingId = '',
  }: { recordingId?: string } = $props();

  let playing = $state(false);
  let frameSrc = $state('');
  let audioReady = $state(false);
  let error = $state('');
  let audioPlayer: AudioPlayer | null = $state(null);

  let ws: WebSocket | null = null;
  let currentBlobUrl = '';

  const SAMPLE_RATE = 8000;

  // ── WS binary protocol parsing ──
  // Wire format: [type:1][pts:8 LE][len:4 LE][data...]
  // type 0x01 = video (MJPEG), 0x02 = audio (G.711 μ-law)
  function parseChunk(buffer: ArrayBuffer): { type: number; pts: number; data: Uint8Array } {
    const view = new DataView(buffer);
    if (view.byteLength < 13) throw new Error('Frame too short');
    const type = view.getUint8(0);
    const pts = Number(view.getBigUint64(1, true));
    const len = view.getUint32(9, true);
    if (13 + len > view.byteLength) throw new Error('Frame data truncated');
    const data = new Uint8Array(buffer, 13, len);
    return { type, pts, data };
  }

  function handleVideoFrame(data: Uint8Array) {
    const blob = new Blob([data], { type: 'image/jpeg' });
    if (currentBlobUrl) URL.revokeObjectURL(currentBlobUrl);
    currentBlobUrl = URL.createObjectURL(blob);
    frameSrc = currentBlobUrl;
  }

  function handleAudioFrame(data: Uint8Array) {
    if (!audioPlayer) {
      const player = new AudioPlayer(AudioCodec.MuLaw, SAMPLE_RATE, 1);
      player.init();
      audioPlayer = player;
      audioReady = true;
    }
    audioPlayer.pushFrame(data);
  }

  function handleWsMessage(event: MessageEvent) {
    const raw = event.data;
    if (raw instanceof ArrayBuffer) {
      processBuffer(raw);
    } else if (raw instanceof Blob) {
      raw.arrayBuffer().then((buf: ArrayBuffer) => processBuffer(buf));
    }
  }

  function processBuffer(buf: ArrayBuffer) {
    try {
      const chunk = parseChunk(buf);
      if (chunk.type === 0x01) handleVideoFrame(chunk.data);
      else if (chunk.type === 0x02) handleAudioFrame(chunk.data);
    } catch (e) {
      console.warn('[AviPlayback] parse error:', e);
    }
  }

  // ── WebSocket URL with auth ──
  function buildWsUrl(): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    let url = `${proto}//${location.host}/api/recordings/${recordingId}/playback`;
    // ?token= carries the bare session token (mbs_...), NOT a "Bearer ..." header.
    const token = getTokenForUrl();
    if (token) {
      url += `?token=${encodeURIComponent(token)}`;
    }
    return url;
  }

  // ── Control messages ──
  function sendControl(action: 'play' | 'pause' | 'stop') {
    if (ws && ws.readyState === 1 /* WebSocket.OPEN */) {
      ws.send(JSON.stringify({ action }));
    }
  }

  // ── Playback control ──
  function togglePlay() {
    if (!playing) startPlayback();
    else stopPlayback();
  }

  function startPlayback() {
    playing = true;
    connectWs();
  }

  function connectWs() {
    if (!recordingId) {
      error = t('avi.error.noId');
      return;
    }
    try {
      const socket = new WebSocket(buildWsUrl());
      socket.binaryType = 'arraybuffer';

      socket.onopen = () => {
        sendControl('play');
      };

      socket.onmessage = handleWsMessage;

      socket.onerror = () => {
        error = t('avi.error.wsConn');
      };

      socket.onclose = () => {
        ws = null;
        if (playing) playing = false;
      };

      ws = socket;
    } catch (e) {
      error = t('avi.error.wsCreate');
    }
  }

  function stopPlayback() {
    sendControl('stop');
    playing = false;
    if (ws) {
      ws.onmessage = null;
      ws.onerror = null;
      ws.onclose = null;
      ws.close();
      ws = null;
    }
    if (currentBlobUrl) {
      URL.revokeObjectURL(currentBlobUrl);
      currentBlobUrl = '';
      frameSrc = '';
    }
    if (audioPlayer) {
      audioPlayer.setMuted(true);
    }
  }

  // ── Pause (keep connection open, pause rendering) ──
  function pausePlayback() {
    sendControl('pause');
    playing = false;
    // Keep WS connection alive for resume
  }

  // ── Cleanup on unmount ──
  $effect(() => {
    return () => {
      stopPlayback();
      if (audioPlayer) {
        audioPlayer.destroy();
        audioPlayer = null;
      }
    };
  });
</script>

<div class="avi-playback">
  <div class="mjpeg-container">
    {#if frameSrc}
      <img src={frameSrc} alt={t('avi.title')} class="mjpeg-render" />
    {:else}
      <div class="placeholder">
        <span class="placeholder-text">{t('avi.noFrame')}</span>
      </div>
    {/if}
  </div>

  <div class="controls">
    <button onclick={togglePlay} class="play-btn">
      {playing ? '⏸ ' + t('common.pause') : '▶ ' + t('common.play')}
    </button>

    {#if audioReady}
      <span class="audio-indicator badge badge-info" data-testid="audio-indicator">
        🔊 {t('avi.audioHz', { rate: SAMPLE_RATE })}
      </span>
    {/if}

    {#if error}
      <span class="error badge badge-error" data-testid="error">{error}</span>
    {/if}
  </div>
</div>

<style>
  .avi-playback {
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: 100%;
  }
  .mjpeg-container {
    position: relative;
    width: 100%;
    aspect-ratio: 16 / 9;
    background: #000;
    border-radius: var(--radius-md);
    overflow: hidden;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .mjpeg-render {
    width: 100%;
    height: 100%;
    object-fit: contain;
  }
  .placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    height: 100%;
  }
  .placeholder-text {
    color: var(--text-tertiary);
    font-size: 0.875rem;
  }
  .controls {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 0;
  }
  .play-btn {
    padding: 6px 16px;
    border-radius: var(--radius-sm);
    background: var(--color-primary);
    color: #fff;
    border: none;
    cursor: pointer;
    font-size: 0.875rem;
    font-weight: 500;
    transition: opacity var(--duration-fast);
  }
  .play-btn:hover {
    opacity: 0.9;
  }
  .error {
    font-size: 0.75rem;
  }
  .audio-indicator {
    font-size: 0.75rem;
  }
</style>
