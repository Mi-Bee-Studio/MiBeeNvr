<script lang="ts">
  import { startTwoWayAudio, stopTwoWayAudio, getAudioUpstreamWS } from '$lib/api';
  import { Mic, MicOff } from 'lucide-svelte';
  import { t } from '$lib/i18n';

  let {
    cameraId,
    enabled = false,
    cameraName = '',
  }: { cameraId: string; enabled?: boolean; cameraName?: string } = $props();

  let pressed = $state(false);
  let error = $state('');
  let audioContext: AudioContext | null = null;
  let mediaStream: MediaStream | null = null;
  let ws: WebSocket | null = null;
  let wsConnected = $state(false);

  /** Capture PCM from microphone via AudioWorklet (fallback to ScriptProcessorNode). */
  async function startCapture() {
    if (!enabled || !cameraId) return;
    error = '';

    try {
      // 1. POST start two-way audio session
      await startTwoWayAudio(cameraId);

      // 2. Get mic access
      mediaStream = await navigator.mediaDevices.getUserMedia({ audio: true });

      // 3. Open WebSocket to backend
      ws = new WebSocket(getAudioUpstreamWS(cameraId));
      ws.binaryType = 'arraybuffer';

      await new Promise<void>((resolve, reject) => {
        const onOpen = () => { ws!.onopen = null; ws!.onerror = null; resolve(); };
        const onError = () => { ws!.onopen = null; ws!.onerror = null; reject(new Error('WebSocket connection failed')); };
        ws!.onopen = onOpen;
        ws!.onerror = onError;
        // Safety timeout
        setTimeout(() => { if (!wsConnected) reject(new Error('WebSocket connection timeout')); }, 5000);
      });

      wsConnected = true;

      // 4. Create AudioContext at 8kHz
      audioContext = new AudioContext({ sampleRate: 8000 });
      const source = audioContext.createMediaStreamSource(mediaStream);

      // 5. Capture PCM via AudioWorklet or ScriptProcessorNode fallback
      try {
        await setupAudioWorklet(audioContext, source);
      } catch (workletErr) {
        console.warn('[TwoWayAudio] AudioWorklet failed, falling back to ScriptProcessor', workletErr);
        setupScriptProcessor(audioContext, source);
      }

      pressed = true;
    } catch (e: any) {
      console.error('[TwoWayAudio] start failed:', e);
      error = e instanceof DOMException && e.name === 'NotAllowedError'
        ? t('xiaomi.twoWayAudioMicDenied') || 'Microphone access denied'
        : e.message || 'Failed to start audio';
      cleanup();
    }
  }

  /** AudioWorklet approach: register inline processor in a blob. */
  async function setupAudioWorklet(ctx: AudioContext, source: MediaStreamAudioSourceNode) {
    const processorCode = `
      class PCMCaptureProcessor extends AudioWorkletProcessor {
        process(inputs: Float32Array[][], outputs: Float32Array[][], parameters: Record<string, Float32Array>) {
          const input = inputs[0];
          if (input && input[0]) {
            const channel = input[0];
            const pcm = new Int16Array(channel.length);
            for (let i = 0; i < channel.length; i++) {
              const s = Math.max(-1, Math.min(1, channel[i]));
              pcm[i] = s < 0 ? s * 32768 : s * 32767;
            }
            this.port.postMessage(pcm.buffer, [pcm.buffer]);
          }
          return true;
        }
      }
      registerProcessor('pcm-capture', PCMCaptureProcessor);
    `;

    const blob = new Blob([processorCode], { type: 'application/javascript' });
    const url = URL.createObjectURL(blob);
    await ctx.audioWorklet.addModule(url);
    URL.revokeObjectURL(url);

    const workletNode = new AudioWorkletNode(ctx, 'pcm-capture');
    let pcmBuffer: number[] = [];

    workletNode.port.onmessage = (event: MessageEvent) => {
      const pcm = new Int16Array(event.data);
      for (let i = 0; i < pcm.length; i++) {
        pcmBuffer.push(pcm[i]);
      }
      flushPCM();
    };

    function flushPCM() {
      while (pcmBuffer.length >= 320) {
        const frame = new Int16Array(pcmBuffer.splice(0, 320));
        const frameBuffer = new ArrayBuffer(641);
        const view = new Uint8Array(frameBuffer);
        view[0] = 0; // reserved byte
        for (let i = 0; i < 320; i++) {
          view[1 + i * 2] = frame[i] & 0xff;
          view[1 + i * 2 + 1] = (frame[i] >> 8) & 0xff;
        }
        if (ws?.readyState === WebSocket.OPEN) {
          ws.send(frameBuffer);
        }
      }
    }

    source.connect(workletNode);
    workletNode.connect(ctx.destination);
  }

  /** ScriptProcessorNode fallback for browsers without AudioWorklet blob support. */
  function setupScriptProcessor(ctx: AudioContext, source: MediaStreamAudioSourceNode) {
    const bufferSize = 2048;
    const processor = ctx.createScriptProcessor(bufferSize, 1, 1);
    let pcmBuffer: number[] = [];

    processor.onaudioprocess = (event: AudioProcessingEvent) => {
      const input = event.inputBuffer.getChannelData(0);
      for (let i = 0; i < input.length; i++) {
        const s = Math.max(-1, Math.min(1, input[i]));
        pcmBuffer.push(s < 0 ? s * 32768 : s * 32767);
      }
      flushPCM();
    };

    function flushPCM() {
      while (pcmBuffer.length >= 320) {
        const frame = new Int16Array(pcmBuffer.splice(0, 320));
        const frameBuffer = new ArrayBuffer(641);
        const view = new Uint8Array(frameBuffer);
        view[0] = 0;
        for (let i = 0; i < 320; i++) {
          view[1 + i * 2] = frame[i] & 0xff;
          view[1 + i * 2 + 1] = (frame[i] >> 8) & 0xff;
        }
        if (ws?.readyState === WebSocket.OPEN) {
          ws.send(frameBuffer);
        }
      }
    }

    source.connect(processor);
    processor.connect(ctx.destination);
  }

  function stopCapture() {
    cleanup();
    pressed = false;
    wsConnected = false;
    if (cameraId) {
      stopTwoWayAudio(cameraId).catch(() => {});
    }
  }

  function cleanup() {
    if (ws) {
      try { ws.close(); } catch { /* ignore */ }
      ws = null;
    }
    if (mediaStream) {
      mediaStream.getTracks().forEach((t) => t.stop());
      mediaStream = null;
    }
    if (audioContext) {
      audioContext.close().catch(() => {});
      audioContext = null;
    }
  }
</script>

<button
  class="two-way-btn"
  class:active={pressed}
  class:disabled={!enabled}
  disabled={!enabled}
  onpointerdown={startCapture}
  onpointerup={stopCapture}
  onpointerleave={stopCapture}
  ontouchend={(e) => { e.preventDefault(); stopCapture(); }}
  ontouchcancel={stopCapture}
  aria-label={enabled ? t('xiaomi.twoWayAudio') || 'Two-way audio' : t('xiaomi.twoWayAudioDisabled') || 'Two-way audio unavailable'}
>
  {#if error}
    <span class="btn-label">{error}</span>
  {:else}
    <span class="btn-icon">
      {#if pressed}
        <Mic size={18} />
      {:else}
        <MicOff size={18} />
      {/if}
    </span>
    <span class="btn-label">
      {#if pressed}
        {cameraName || t('xiaomi.twoWayAudioSpeaking') || 'Speaking...'}
      {:else}
        {t('xiaomi.twoWayAudioPressToTalk') || 'Push to Talk'}
      {/if}
    </span>
  {/if}
</button>

<style>
  .two-way-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 1rem;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    background-color: var(--bg-elevated);
    color: var(--text-secondary);
    cursor: pointer;
    font-size: 0.8125rem;
    font-weight: 500;
    transition: all var(--duration-fast) var(--ease-out);
    user-select: none;
    touch-action: none;
    white-space: nowrap;
  }

  .two-way-btn:hover:not(.disabled) {
    background-color: var(--bg-hover);
    color: var(--text-primary);
    border-color: var(--border-hover);
  }

  .two-way-btn.active {
    background-color: rgba(239, 68, 68, 0.15);
    border-color: var(--color-danger);
    color: var(--color-danger);
    animation: pulse-active 0.8s ease-in-out infinite alternate;
  }

  .two-way-btn.disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .btn-icon {
    display: flex;
    align-items: center;
  }

  .btn-label {
    line-height: 1;
  }

  @keyframes pulse-active {
    from { opacity: 0.7; }
    to { opacity: 1; }
  }
</style>
