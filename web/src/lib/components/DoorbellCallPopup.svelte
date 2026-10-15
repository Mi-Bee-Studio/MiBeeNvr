<script lang="ts">
  // DoorbellCallPopup — global incoming-call popup (#971). Subscribes to the
  // event SSE for doorbell.* topics; a ring opens the popup (caller name,
  // ring snapshot, answer/reject). Answering POSTs the answer and opens the
  // G.711 talk WebSocket: mic → A-law (standard form) → binary frames out,
  // binary frames in → A-law → 8 kHz playback. Talk is genuinely two-way
  // (doorbell intercom), so local mic is NOT played back.
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import { apiRequest, appendAuthToken, getTokenForUrl, API_BASE } from '$lib/api/client';
  import { encodeAlawStandard, decodeAlaw } from '$lib/alaw-encoder';
  import { Phone, PhoneOff, X, BellRing } from 'lucide-svelte';

  interface DoorbellEvt {
    call_id: string;
    user: string;
    name?: string;
    camera_id?: string;
    snapshot_path?: string;
    reason?: string;
  }

  let ringing = $state<DoorbellEvt | null>(null);
  let phase = $state<'ringing' | 'answering' | 'talking'>('ringing');
  let errorHint = $state('');

  let es: EventSource | undefined;
  let ws: WebSocket | undefined;
  let micCtx: AudioContext | undefined;
  let playCtx: AudioContext | undefined;
  let micStream: MediaStream | undefined;
  let workletNode: AudioWorkletNode | undefined;
  let playTime = 0;

  const WORKLET_SRC = `
class TalkDownsampler extends AudioWorkletProcessor {
  constructor() {
    super();
    this.ratio = sampleRate / 8000;
    this.pos = 0;
    this.base = 0;
    this.out = new Float32Array(320);
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
registerProcessor('doorbell-talk-downsampler', TalkDownsampler);
`;

  function talkUrl(callId: string): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    let url = `${proto}//${location.host}${API_BASE}/doorbell/calls/${callId}/talk`;
    const token = getTokenForUrl();
    if (token) url += `?token=${encodeURIComponent(token)}`;
    return url;
  }

  function callerName(evt: DoorbellEvt): string {
    return evt.name || evt.user;
  }

  function snapshotUrl(callId: string): string | null {
    if (!ringing?.snapshot_path) return null;
    return appendAuthToken(`${API_BASE}/doorbell/calls/${callId}/snapshot`);
  }

  // ── SSE lifecycle ──
  onMount(() => {
    connect();
    return () => teardown();
  });

  // The events endpoint wraps every payload as {"Topic":...,"Data":{...}}
  // (verified live against /api/events) — unwrap to the inner object.
  function doorbellPayload(e: Event): DoorbellEvt {
    const raw = JSON.parse((e as MessageEvent).data);
    return (raw.Data ?? raw.data) as DoorbellEvt;
  }

  function connect() {
    es = new EventSource(appendAuthToken(`${API_BASE}/events?filter=doorbell.`));
    es.addEventListener('doorbell.ring', (e) => {
      const evt = doorbellPayload(e);
      // A second ring replaces the current popup (the old call was superseded
      // or the popup was left over).
      if (phase === 'talking' || phase === 'answering') return;
      ringing = evt;
      phase = 'ringing';
    });
    es.addEventListener('doorbell.answered', (e) => {
      const evt = doorbellPayload(e);
      // Our own answer keeps the popup (it transitions to talking); an
      // answer from another client dismisses ours.
      if (ringing?.call_id === evt.call_id && phase === 'ringing') dismiss();
    });
    const gone = (e: Event) => {
      const evt = doorbellPayload(e);
      if (ringing?.call_id === evt.call_id) {
        cleanupTalk();
        dismiss();
      }
    };
    es.addEventListener('doorbell.rejected', gone);
    es.addEventListener('doorbell.ended', gone);
    es.onerror = () => {
      // EventSource auto-reconnects; nothing to reset.
    };
  }

  function dismiss() {
    ringing = null;
    phase = 'ringing';
    errorHint = '';
  }

  function teardown() {
    es?.close();
    es = undefined;
    cleanupTalk();
  }

  // ── Answer / reject ──
  async function answer() {
    if (!ringing) return;
    errorHint = '';
    phase = 'answering';
    try {
      await apiRequest<void>(`/doorbell/calls/${ringing.call_id}/answer`, { method: 'POST' });
    } catch {
      errorHint = t('doorbell.answerFailed');
      phase = 'ringing';
      return;
    }
    try {
      await startTalk(ringing.call_id);
      phase = 'talking';
    } catch {
      errorHint = t('doorbell.talkFailed');
      phase = 'ringing';
    }
  }

  async function reject() {
    if (!ringing) return;
    const id = ringing.call_id;
    dismiss();
    try {
      await apiRequest<void>(`/doorbell/calls/${id}/reject`, { method: 'POST' });
    } catch {
      // The ring event's ended/rejected SSE closes any lingering state.
    }
  }

  async function hangup() {
    if (!ringing) return;
    const id = ringing.call_id;
    cleanupTalk();
    dismiss();
    try {
      await apiRequest<void>(`/doorbell/calls/${id}/bye`, { method: 'POST' });
    } catch {
      // Server-side sweep ends the call regardless.
    }
  }

  // ── Talk bridge ──
  async function startTalk(callId: string): Promise<void> {
    micStream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });

    ws = new WebSocket(talkUrl(callId));
    ws.binaryType = 'arraybuffer';

    const opened = new Promise<void>((resolve, reject) => {
      ws!.onopen = () => resolve();
      ws!.onerror = () => reject(new Error('ws'));
    });
    await opened;

    // Mic → 8 kHz A-law → binary frames (same worklet pattern as the
    // GB28181 talk button; standard-form A-law for SIP hardware).
    micCtx = new AudioContext();
    await micCtx.audioWorklet.addModule(
      URL.createObjectURL(new Blob([WORKLET_SRC], { type: 'application/javascript' })),
    );
    const src = micCtx.createMediaStreamSource(micStream);
    workletNode = new AudioWorkletNode(micCtx, 'doorbell-talk-downsampler');
    workletNode.port.onmessage = (e: MessageEvent) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(encodeAlawStandard(e.data as Float32Array));
      }
    };
    src.connect(workletNode);

    // Doorbell → 8 kHz playback queue.
    playCtx = new AudioContext({ sampleRate: 8000 });
    playTime = 0;
    ws.onmessage = (e: MessageEvent) => {
      if (typeof e.data !== 'string' && playCtx) enqueue(decodeAlaw(new Uint8Array(e.data)));
    };
    ws.onclose = () => {
      if (phase === 'talking') {
        cleanupTalk();
        dismiss();
      }
    };

    await micCtx.resume();
    await playCtx.resume();
  }

  function enqueue(pcm: Float32Array) {
    const buf = playCtx!.createBuffer(1, pcm.length, 8000);
    buf.copyToChannel(pcm, 0);
    const node = playCtx!.createBufferSource();
    node.buffer = buf;
    node.connect(playCtx!.destination);
    const now = playCtx!.currentTime;
    if (playTime < now) playTime = now + 0.02;
    node.start(playTime);
    playTime += pcm.length / 8000;
  }

  function cleanupTalk() {
    if (ws) {
      ws.onclose = null;
      ws.onmessage = null;
      try {
        ws.close();
      } catch {
        /* already closed */
      }
      ws = undefined;
    }
    if (workletNode) {
      workletNode.port.onmessage = null;
      workletNode.disconnect();
      workletNode = undefined;
    }
    if (micCtx) {
      micCtx.close().catch(() => undefined);
      micCtx = undefined;
    }
    if (playCtx) {
      playCtx.close().catch(() => undefined);
      playCtx = undefined;
    }
    if (micStream) {
      for (const track of micStream.getTracks()) track.stop();
      micStream = undefined;
    }
  }
</script>

{#if ringing}
  <div class="fixed bottom-6 right-6 z-[1200] w-80 card border th-border shadow-2xl overflow-hidden" role="alertdialog" aria-label={t('doorbell.title')}>
    <div class="flex items-center justify-between px-4 py-2 th-bg-tertiary">
      <div class="flex items-center gap-2 text-sm font-medium th-text-primary">
        <BellRing size={15} class="animate-pulse" />
        {t('doorbell.title')}
      </div>
      <button class="btn btn-ghost btn-xs" onclick={() => dismiss()} aria-label={t('doorbell.dismiss')} title={t('doorbell.dismiss')}>
        <X size={13} />
      </button>
    </div>

    <div class="p-4 space-y-3">
      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="text-sm font-medium th-text-primary truncate">{callerName(ringing)}</p>
          <p class="text-xs th-text-tertiary">
            {phase === 'talking' ? t('doorbell.talking') : phase === 'answering' ? t('doorbell.answering') : t('doorbell.incoming')}
          </p>
        </div>
        {#if snapshotUrl(ringing.call_id)}
          <img
            src={snapshotUrl(ringing.call_id)!}
            alt={t('doorbell.snapshotAlt')}
            class="w-24 h-16 object-cover rounded th-border"
          />
        {/if}
      </div>

      {#if errorHint}
        <p class="text-xs th-color-danger">{errorHint}</p>
      {/if}

      <div class="flex gap-2">
        {#if phase === 'ringing'}
          <button class="btn btn-primary btn-sm flex-1" onclick={() => void answer()}>
            <Phone size={14} /> {t('doorbell.answer')}
          </button>
          <button class="btn btn-secondary btn-sm flex-1" onclick={() => void reject()}>
            <PhoneOff size={14} /> {t('doorbell.reject')}
          </button>
        {:else if phase === 'talking'}
          <button class="btn btn-secondary btn-sm flex-1" onclick={() => void hangup()}>
            <PhoneOff size={14} /> {t('doorbell.hangup')}
          </button>
        {:else}
          <button class="btn btn-sm flex-1" disabled>
            <span class="spinner"></span> {t('doorbell.answering')}
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}
