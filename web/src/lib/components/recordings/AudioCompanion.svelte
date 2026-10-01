<script lang="ts">
  // Audio companion panel for a video recording (form-① association UX).
  // When the recording's camera has a linked audio-source device
  // (audio_link_camera_id), this panel fetches the audio segments around the
  // video's time window, draws them on a shared timeline, and can play the
  // audio track in sync with the visible playback <video>:
  //
  //   t_audio = recording.start + video.currentTime + offset
  //
  // The offset slider (+/- 5s, persisted per camera pair in localStorage)
  // compensates for clock skew between the camera and the microphone — the
  // two devices timestamp independently, so a fixed manual offset is the
  // honest v1 alignment story. Only G.711 segments are sync-playable (the
  // WAV transcode endpoint); AAC segments render grayed-out and are skipped.
  import { listCameras, listRecordings, appendAuthToken, API_BASE } from '$lib/api';
  import type { Camera, Recording } from '$lib/api';
  import { parseServerDate } from '$lib/format';
  import { t } from '$lib/i18n';
  import { AudioLines, Volume2, VolumeX } from 'lucide-svelte';

  interface Props {
    recording: Recording;
  }
  let { recording }: Props = $props();

  interface AudioSeg {
    id: string;
    startMs: number;
    endMs: number;
    unsupported?: boolean; // WAV endpoint refused (AAC/Opus) — skipped in sync
  }

  const PAD_MS = 120_000; // context padding around the video window
  const OFFSETS_KEY = 'mibee_nvr_audio_sync_offsets'; // { "<videoCam|audioCam>": seconds }
  const DRIFT_RESYNC_S = 0.4;
  const TICK_MS = 300;

  let audioCam = $state<Camera | null>(null);
  let linked = $state(false);
  let segs = $state<AudioSeg[]>([]);
  let loading = $state(true);

  let syncOn = $state(false);
  let muted = $state(false);
  let offsetSec = $state(0);
  let statusKey = $state('idle'); // i18n suffix under detail.audioCompanion.status.*
  let currentSegId = $state('');
  let auditioning = $state(false); // manual segment play (sync disengaged)
  let audioEl: HTMLAudioElement | undefined = $state();

  let recStartMs = $derived(parseServerDate(recording.started_at).getTime());
  let recEndMs = $derived(parseServerDate(recording.ended_at).getTime());
  let winLo = $state(0);
  let winHi = $state(1);

  $effect(() => {
    const id = recording.id;
    void load(id);
  });

  async function load(_id: string) {
    loading = true;
    linked = false;
    segs = [];
    syncOn = false;
    auditioning = false;
    currentSegId = '';
    try {
      const cams = await listCameras();
      const ac = cams.find(
        (c) => c.encoding === 'audio' && c.audio_link_camera_id === recording.camera_id
      );
      if (!ac) return;
      linked = true;
      audioCam = ac;
      offsetSec = loadOffset(recording.camera_id, ac.id);
      const resp = await listRecordings({
        camera_id: ac.id,
        start: new Date(recStartMs - PAD_MS).toISOString(),
        end: new Date(recEndMs + PAD_MS).toISOString(),
        limit: 500,
        sort_by: 'started_at',
        order: 'asc',
      });
      segs = (resp.recordings || []).map((r) => ({
        id: r.id,
        startMs: parseServerDate(r.started_at).getTime(),
        endMs: parseServerDate(r.ended_at).getTime(),
      }));
      winLo = Math.min(recStartMs - PAD_MS, segs.length ? segs[0].startMs : Infinity);
      winHi = Math.max(recEndMs + PAD_MS, segs.length ? segs[segs.length - 1].endMs : -Infinity);
    } catch (e) {
      console.warn('AudioCompanion: load failed', e);
    }
    loading = false;
  }

  // --- Sync engine ---

  $effect(() => {
    if (syncOn && linked) {
      const timer = setInterval(tick, TICK_MS);
      return () => clearInterval(timer);
    }
  });

  // Pause the element whenever sync/audition stops (navigation, toggle-off).
  $effect(() => {
    if ((!syncOn && !auditioning) || !linked) {
      audioEl?.pause();
    }
    if (audioEl) audioEl.muted = muted;
  });

  function visibleVideo(): HTMLVideoElement | null {
    for (const v of document.querySelectorAll('video')) {
      const el = v as HTMLVideoElement;
      if (el.offsetParent !== null) return el; // first visible video = the playback panel's
    }
    return null;
  }

  function tick() {
    const v = visibleVideo();
    if (!v) {
      statusKey = 'waitingVideo';
      audioEl?.pause();
      return;
    }
    if (v.paused) {
      statusKey = 'videoPaused';
      audioEl?.pause();
      return;
    }
    const tAbs = recStartMs + v.currentTime * 1000 + offsetSec * 1000;
    const inWindow = segs.find((s) => tAbs >= s.startMs && tAbs < s.endMs);
    const seg = inWindow && !inWindow.unsupported ? inWindow : undefined;
    if (!seg) {
      statusKey = inWindow ? 'unsupported' : 'gap';
      audioEl?.pause();
      currentSegId = '';
      return;
    }
    if (seg.id !== currentSegId) {
      currentSegId = seg.id;
      attach(seg.id);
    }
    if (!audioEl) return;
    const posSec = (tAbs - seg.startMs) / 1000;
    if (Math.abs(audioEl.currentTime - posSec) > DRIFT_RESYNC_S) {
      try {
        audioEl.currentTime = posSec;
      } catch {} // pre-metadata seek — next tick retries once readyState advances
    }
    if (audioEl.paused) void audioEl.play().catch(() => {});
    statusKey = 'playing';
  }

  function attach(segId: string) {
    if (!audioEl) return;
    audioEl.src = appendAuthToken(`${API_BASE}/recordings/${segId}/audio.wav`);
    audioEl.load();
  }

  function onAudioError() {
    // Most likely a 406 (AAC/Opus refused by the WAV endpoint) — mark the
    // segment so sync skips it instead of retrying every tick.
    const seg = segs.find((s) => s.id === currentSegId);
    if (seg) seg.unsupported = true;
    if (auditioning) {
      auditioning = false;
      statusKey = 'unsupported';
    }
  }

  function toggleSync() {
    auditioning = false;
    syncOn = !syncOn;
    statusKey = syncOn ? 'playing' : 'idle';
  }

  function audition(seg: AudioSeg) {
    syncOn = false;
    auditioning = true;
    currentSegId = seg.id;
    statusKey = 'audition';
    attach(seg.id);
    void audioEl?.play().catch(() => {});
  }

  // --- Offset persistence ---

  function loadOffset(videoCam: string, audioCamId: string): number {
    try {
      const raw = localStorage.getItem(OFFSETS_KEY);
      const map = raw ? JSON.parse(raw) : {};
      const v = map[`${videoCam}|${audioCamId}`];
      return typeof v === 'number' && v >= -5 && v <= 5 ? v : 0;
    } catch {
      return 0;
    }
  }

  function saveOffset(videoCam: string, audioCamId: string, v: number) {
    try {
      const raw = localStorage.getItem(OFFSETS_KEY);
      const map = raw ? JSON.parse(raw) : {};
      map[`${videoCam}|${audioCamId}`] = v;
      localStorage.setItem(OFFSETS_KEY, JSON.stringify(map));
    } catch {}
  }

  function onOffsetInput(e: Event) {
    offsetSec = parseFloat((e.target as HTMLInputElement).value) || 0;
    if (audioCam) saveOffset(recording.camera_id, audioCam.id, offsetSec);
  }

  // --- Timeline geometry ---

  function pct(ms: number): number {
    const span = winHi - winLo;
    return span > 0 ? ((ms - winLo) / span) * 100 : 0;
  }

  let videoLeftPct = $derived(pct(recStartMs));
  let videoWidthPct = $derived(Math.max(pct(recEndMs) - pct(recStartMs), 0.5));

  function fmtClock(ms: number): string {
    return new Date(ms).toLocaleTimeString([], { hour12: false });
  }

  function fmtOffset(): string {
    return `${offsetSec > 0 ? '+' : ''}${offsetSec.toFixed(1)}s`;
  }
</script>

{#if linked && !loading}
  <div class="card border th-border p-4">
    <div class="flex items-center justify-between gap-3 flex-wrap mb-3">
      <div class="flex items-center gap-2 min-w-0">
        <AudioLines size={16} class="th-text-secondary shrink-0" />
        <h3 class="text-sm font-medium th-text-primary truncate">{t('detail.audioCompanion.title')}</h3>
        <span class="text-xs th-text-tertiary truncate">{audioCam?.name}</span>
      </div>
      <div class="flex items-center gap-2">
        <button
          class="btn btn-ghost btn-sm"
          onclick={() => (muted = !muted)}
          title={muted ? t('detail.audioCompanion.unmute') : t('detail.audioCompanion.mute')}
          aria-label={muted ? t('detail.audioCompanion.unmute') : t('detail.audioCompanion.mute')}
        >
          {#if muted}
            <VolumeX size={14} />
          {:else}
            <Volume2 size={14} />
          {/if}
        </button>
        <button
          class="btn btn-sm {syncOn ? 'btn-primary' : 'btn-secondary'}"
          onclick={toggleSync}
          disabled={segs.length === 0}
        >
          {t('detail.audioCompanion.sync')}
        </button>
      </div>
    </div>

    {#if segs.length === 0}
      <p class="text-xs th-text-muted">{t('detail.audioCompanion.empty')}</p>
    {:else}
      <!-- Shared timeline: highlighted band = the video recording's window,
           bars = audio segments (gray = not sync-playable). Click a bar to
           audition that segment standalone. -->
      <div class="relative h-8 rounded th-bg-tertiary overflow-hidden mb-2">
        <div
          class="absolute inset-y-0 bg-[var(--color-primary)]/10 border-x-2 border-[var(--color-primary)]/40"
          style="left: {videoLeftPct}%; width: {videoWidthPct}%"
          title={t('detail.audioCompanion.videoWindow')}
        ></div>
        {#each segs as seg (seg.id)}
          <button
            class="absolute inset-y-1 rounded-sm transition-colors {seg.unsupported
              ? 'th-bg-hover'
              : seg.id === currentSegId
                ? 'bg-[var(--color-primary)]'
                : 'bg-[var(--color-primary)]/45 hover:bg-[var(--color-primary)]/70'}"
            style="left: {pct(seg.startMs)}%; width: {Math.max(pct(seg.endMs) - pct(seg.startMs), 0.4)}%"
            onclick={() => !seg.unsupported && audition(seg)}
            title="{fmtClock(seg.startMs)} – {fmtClock(seg.endMs)}"
            aria-label="{t('detail.audioCompanion.segment')} {fmtClock(seg.startMs)}"
          ></button>
        {/each}
      </div>

      <div class="flex items-center justify-between gap-3 flex-wrap text-xs th-text-tertiary">
        <span class="font-mono tabular-nums">{fmtClock(winLo)}</span>
        <label class="flex items-center gap-2" title={t('detail.audioCompanion.offsetHint')}>
          <span>{t('detail.audioCompanion.offset')}</span>
          <input
            type="range"
            min="-5"
            max="5"
            step="0.1"
            value={offsetSec}
            oninput={onOffsetInput}
            class="w-32"
          />
          <span class="font-mono tabular-nums th-text-secondary">{fmtOffset()}</span>
        </label>
        <span class="font-mono tabular-nums">{fmtClock(winHi)}</span>
      </div>

      <p class="text-xs th-text-muted mt-2">
        {t('detail.audioCompanion.status.' + statusKey)}
        {#if segs.some((s) => s.unsupported)}
          <span class="ml-1">{t('detail.audioCompanion.unsupportedNote')}</span>
        {/if}
      </p>
    {/if}

    <audio bind:this={audioEl} class="hidden" onerror={onAudioError}></audio>
  </div>
{/if}
