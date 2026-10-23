<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Mic, MicOff, Loader2 } from 'lucide-svelte';
  import { getTokenForUrl, API_BASE } from '$lib/api';
  import { t } from '$lib/i18n';
  import { encodeAlawItu, decodeAlaw } from '$lib/alaw-encoder';

  let {
    cameraId,
    class: className = '',
  }: {
    cameraId: string;
    class?: string;
  } = $props();

  // Talk states: idle → connecting → talking; error reverts to idle.
  let state = $state<'idle' | 'connecting' | 'talking'>('idle');
  let errorHint = $state('');

  let ws: WebSocket | null = null;
  let ctx: AudioContext | null = null;
  let playCtx: AudioContext | null = null;
  let playTime = 0;
  let stream: MediaStream | null = null;
  let workletNode: AudioWorkletNode | null = null;

  const WORKLET_SRC = `
class TalkDownsampler extends AudioWorkletProcessor {
  constructor() {
    super();
    this.ratio = sampleRate / 8000; // input → 8 kHz
    this.pos = 0;                   // absolute fractional input index
    this.base = 0;                  // absolute index of current block start
    this.out = new Float32Array(320); // 40 ms at 8 kHz
    this.fill = 0;
  }
  process(inputs) {
    const input = inputs[0] && inputs[0][0];
    if (input) {
      while (this.pos < this.base + input.length - 1) {
        const i = this.pos - this.base;
        const i0 = Math.floor(i);
        const frac = i - i0;
        this.out[this.fill++] = input[i0] + (input[i0 + 1] - input[i0]) * frac;
        if (this.fill === this.out.length) {
          this.port.postMessage(this.out.slice(0));
          this.fill = 0;
        }
        this.pos += this.ratio;
      }
      this.base += input.length;
    }
    return true;
  }
}
registerProcessor('talk-downsampler', TalkDownsampler);
`;

  function buildUrl(): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    let url = `${proto}//${location.host}${API_BASE}/cameras/${cameraId}/gb28181/talk`;
    const token = getTokenForUrl();
    if (token) {
      url += `?token=${encodeURIComponent(token)}`;
    }
    return url;
  }

  async function startTalk() {
    errorHint = '';
    state = 'connecting';
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
        },
      });
    } catch {
      errorHint = t('gb28181.talk.micDenied');
      state = 'idle';
      return;
    }

    try {
      ws = new WebSocket(buildUrl());
      ws.binaryType = 'arraybuffer';
    } catch {
      cleanup();
      errorHint = t('gb28181.talk.wsFailed');
      state = 'idle';
      return;
    }

    ws.onopen = async () => {
      try {
        ctx = new AudioContext();
        await ctx.audioWorklet.addModule(
          URL.createObjectURL(new Blob([WORKLET_SRC], { type: 'application/javascript' })),
        );
        const src = ctx.createMediaStreamSource(stream!);
        workletNode = new AudioWorkletNode(ctx, 'talk-downsampler');
        workletNode.port.onmessage = (e: MessageEvent) => {
          const pcm = e.data as Float32Array;
          if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(encodeAlawItu(pcm));
          }
        };
        src.connect(workletNode);
        // Mic path is deliberately NOT connected to ctx.destination — no
        // local playback of your own voice.

        // Device return audio (sendrecv uplink leg, standard A-law 8 kHz)
        // plays on a dedicated context; mic echo cancellation is on.
        playCtx = new AudioContext({ sampleRate: 8000 });
        playTime = 0;
        ws.onmessage = (e: MessageEvent) => {
          if (typeof e.data !== 'string' && playCtx) enqueue(decodeAlaw(new Uint8Array(e.data)));
        };
        await Promise.all([ctx.resume(), playCtx.resume()]);
        state = 'talking';
      } catch (err) {
        console.error('[GB28181Talk] pipeline error:', err);
        cleanup();
        errorHint = t('gb28181.talk.wsFailed');
        state = 'idle';
      }
    };

    ws.onclose = () => {
      // Server closed the intercom (BYE / teardown).
      if (state === 'talking') {
        cleanup();
        state = 'idle';
      }
    };

    ws.onerror = () => {
      if (state !== 'talking') {
        cleanup();
        errorHint = t('gb28181.talk.wsFailed');
        state = 'idle';
      }
    };
  }

  function stopTalk() {
    cleanup();
    state = 'idle';
  }

  // Gapless playback queue for the return leg; if the device stalls or the
  // network hiccups, resync instead of letting scheduled latency grow.
  function enqueue(pcm: Float32Array) {
    if (!playCtx) return;
    const buf = playCtx.createBuffer(1, pcm.length, 8000);
    buf.copyToChannel(pcm, 0);
    const node = playCtx.createBufferSource();
    node.buffer = buf;
    node.connect(playCtx.destination);
    const now = playCtx.currentTime;
    if (playTime < now) playTime = now + 0.02;
    else if (playTime - now > 0.4) playTime = now + 0.02;
    node.start(playTime);
    playTime += pcm.length / 8000;
  }

  function cleanup() {
    if (ws) {
      ws.onclose = null;
      ws.onerror = null;
      ws.onmessage = null;
      try {
        ws.close();
      } catch {
        /* already closed */
      }
      ws = null;
    }
    if (workletNode) {
      workletNode.port.onmessage = null;
      workletNode.disconnect();
      workletNode = null;
    }
    if (ctx) {
      ctx.close().catch(() => undefined);
      ctx = null;
    }
    if (playCtx) {
      playCtx.close().catch(() => undefined);
      playCtx = null;
    }
    if (stream) {
      for (const track of stream.getTracks()) track.stop();
      stream = null;
    }
  }

  function toggle(e: MouseEvent) {
    e.stopPropagation();
    if (state === 'idle') {
      void startTalk();
    } else if (state === 'talking') {
      stopTalk();
    }
    // connecting: ignore rapid double-clicks
  }

  onDestroy(() => cleanup());
</script>

<button
  onclick={toggle}
  disabled={state === 'connecting'}
  class="p-1.5 rounded-md transition-all z-10 {state === 'talking'
    ? 'bg-red-600/80 text-white hover:bg-red-500'
    : 'bg-black/50 text-white/70 hover:text-white hover:bg-black/70'} {className}"
  title={errorHint || (state === 'talking' ? t('gb28181.talk.stop') : t('gb28181.talk.start'))}
>
  {#if state === 'connecting'}
    <Loader2 size={16} class="animate-spin" />
  {:else if state === 'talking'}
    <MicOff size={16} />
  {:else}
    <Mic size={16} />
  {/if}
</button>
