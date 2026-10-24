<script lang="ts">
  // Instant-start live listening (#1052 follow-up): the progressive-WAV
  // <audio> path needs 5-15s of browser pre-buffering before the first
  // sample; fetching the same stream and scheduling decoded PCM on Web Audio
  // starts as soon as the first chunk lands (~0.3s). Pacing is feed-driven:
  // audio is scheduled with a small margin and resyncs if the feed stalls.
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { appendAuthToken, API_BASE } from '$lib/api';
  import { getSharedAudioContext } from './AudioWaveform.svelte';
  import AudioWaveform from './AudioWaveform.svelte';
  import { Play, Square, Loader2, RotateCw } from 'lucide-svelte';

  interface Props {
    cameraId: string;
    /** Audio codec hint from the API, for the status line (e.g. "g711"). */
    codec?: string;
  }
  let { cameraId, codec = '' }: Props = $props();

  type State = 'idle' | 'connecting' | 'playing' | 'error';
  let phase: State = $state('idle');
  let elapsed = $state(0);
  let tapBus = $state<GainNode | null>(null);

  let ctrl: AbortController | null = null;
  let running = $state(false);
  let timer = 0;
  let tap: GainNode | null = null;
  let anchor = 0; // end-of-schedule on the ctx timeline
  const MARGIN_S = 0.06; // schedule slightly ahead of "now" to avoid underruns

  function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
    const out = new Uint8Array(a.length + b.length);
    out.set(a);
    out.set(b, a.length);
    return out;
  }

  async function start(): Promise<void> {
    if (running) return;
    running = true;
    phase = 'connecting';
    const startedAt = Date.now();
    timer = window.setInterval(() => (elapsed = Math.floor((Date.now() - startedAt) / 1000)), 1000);
    const ctx = getSharedAudioContext();
    if (!ctx) {
      phase = 'error';
      teardown();
      return;
    }
    if (ctx.state === 'suspended') await ctx.resume().catch(() => undefined);
    if (!tap) {
      tap = ctx.createGain();
      tap.connect(ctx.destination);
      tapBus = tap;
    }
    ctrl = new AbortController();
    anchor = 0;
    try {
      const resp = await fetch(appendAuthToken(`${API_BASE}/cameras/${cameraId}/audio/live`), {
        signal: ctrl.signal,
      });
      if (!resp.ok || !resp.body) throw new Error(`HTTP ${resp.status}`);
      const reader = resp.body.getReader();

      // Frame the 44-byte WAV header first: rate/channels for scheduling.
      let head: Uint8Array = new Uint8Array(0);
      while (head.length < 44) {
        const { done, value } = await reader.read();
        if (done || !value) throw new Error('stream ended before the WAV header');
        head = concat(head, value);
      }
      const view = new DataView(head.buffer, head.byteOffset, head.byteLength);
      const rate = view.getUint32(24, true) || 8000;
      const channels = Math.min(2, view.getUint16(22, true) || 1);
      const bytesPerSample = 2 * channels;

      phase = 'playing';
      let pcmBytes: Uint8Array = head.subarray(44);
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break; // clean end at the declared length — stop, user can replay
        if (value) pcmBytes = concat(pcmBytes, value);
        // Schedule whatever full frames we hold, keeping ~0.2s chunks.
        const frames = Math.floor(pcmBytes.length / bytesPerSample);
        if (frames >= Math.floor(rate * 0.2)) {
          schedule(pcmBytes.subarray(0, frames * bytesPerSample), frames, rate, channels);
          pcmBytes = pcmBytes.subarray(frames * bytesPerSample);
        }
      }
      if (pcmBytes.length >= bytesPerSample) {
        schedule(pcmBytes, Math.floor(pcmBytes.length / bytesPerSample), rate, channels);
      }
      stop(true);
    } catch (e) {
      if (ctrl?.signal.aborted) {
        // user stop — keep state as-is (stop() already ran)
      } else {
        phase = 'error';
      }
    } finally {
      teardown();
    }
  }

  function schedule(bytes: Uint8Array, frames: number, rate: number, channels: number): void {
    const ctx = getSharedAudioContext();
    if (!ctx || !tap) return;
    const buf = ctx.createBuffer(channels, frames, rate);
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    for (let ch = 0; ch < channels; ch++) {
      const out = buf.getChannelData(ch);
      for (let i = 0; i < frames; i++) out[i] = view.getInt16((i * channels + ch) * 2, true) / 32768;
    }
    const src = ctx.createBufferSource();
    src.buffer = buf;
    src.connect(tap);
    const now = ctx.currentTime;
    // Resync when the queue drained (feed stall or first chunk): never let
    // scheduled latency grow.
    if (anchor < now + MARGIN_S) anchor = now + MARGIN_S;
    src.start(anchor);
    anchor += frames / rate;
    src.onended = () => {
      src.disconnect();
    };
  }

  function teardown(): void {
    running = false;
    if (timer) {
      clearInterval(timer);
      timer = 0;
    }
  }

  function stop(keepIdle = false): void {
    ctrl?.abort();
    ctrl = null;
    if (!keepIdle) phase = 'idle';
    teardown();
  }

  function toggle(): void {
    if (running) stop();
    else void start();
  }

  onDestroy(() => stop(true));
</script>

<div class="w-full max-w-xl mx-auto">
  <div class="flex items-center gap-3 mb-2">
    <button
      type="button"
      class="btn {running ? 'btn-primary' : 'btn-secondary'} px-4"
      onclick={toggle}
      aria-label={running ? t('live.audioInstant.stop') : t('live.audioInstant.play')}>
      {#if phase === 'connecting'}
        <Loader2 size={16} class="animate-spin" />
      {:else if running}
        <Square size={16} />
      {:else}
        <Play size={16} />
      {/if}
      {running ? t('live.audioInstant.stop') : t('live.audioInstant.play')}
    </button>
    <span class="text-sm th-text-secondary">
      {#if phase === 'connecting'}
        <span class="inline-flex items-center gap-1.5">
          <span class="relative inline-flex w-2 h-2">
            <span class="absolute inline-flex w-2 h-2 rounded-full bg-yellow-500 opacity-75 animate-ping"></span>
            <span class="relative inline-flex w-2 h-2 rounded-full bg-yellow-500"></span>
          </span>
          {t('live.audioInstant.connecting')}
        </span>
      {:else if phase === 'playing'}
        <span class="inline-flex items-center gap-1.5 text-green-600 dark:text-green-400">
          <span class="w-2 h-2 rounded-full bg-green-500"></span>
          {t('live.audioInstant.listening')} · {Math.floor(elapsed / 60)}:{String(elapsed % 60).padStart(2, '0')}{codec ? ` · ${codec.toUpperCase()}` : ''}
        </span>
      {:else if phase === 'error'}
        <span class="text-red-500 inline-flex items-center gap-2">
          {t('live.audioInstant.failed')}
          <button type="button" class="btn btn-ghost btn-sm" onclick={() => void start()}>
            <RotateCw size={13} />
            {t('common.retry')}
          </button>
        </span>
      {:else}
        {t('live.audioInstant.idle')}
      {/if}
    </span>
  </div>
  <AudioWaveform node={tapBus} height={72} label={t('common.audioWaveform')} />
</div>
