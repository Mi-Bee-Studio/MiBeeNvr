<script lang="ts">
  /**
   * Single camera player dispatcher.
   *
   * Replaces the inline `{#if mode === 'hls'}…{:else if mode === 'webrtc'}…`
   * branches that were duplicated between Surveillance.svelte and LiveView.svelte.
   * Reads the active mode from the {@link PlayerOrchestrator} (provided via
   * context) and renders the matching player component, wiring that player's
   * health signals back to the orchestrator.
   *
   * How health flows: every player dispatches a bubbling DOM `statechange`
   * CustomEvent with `detail: { cameraId, state }` whenever its internal
   * streamState changes (see each player's `dispatchStateChange`). This wrapper
   * listens ONCE on its container and forwards the normalized {@link HealthState}
   * to the orchestrator — no per-player `onStateChange` prop wiring needed, so
   * the player components stay untouched.
   *
   * Non-real-time modes (snapshot, unsupported) are handled by the parent route:
   * when the orchestrator reports no real-time mode (chain empty), this
   * component renders nothing and the parent's snapshot/unsupported branch takes
   * over. The parent must call `orchestrator.registerCamera()` for each camera
   * and read `activeMode()` to decide whether to mount `<CameraPlayer>` at all.
   *
   * The orchestrator owns degrade/upgrade decisions; players keep their own
   * internal resilience (HLS recreate, FLV retry, WebRTC ICE restart). This
   * component only forwards the player's normalized health so the orchestrator
   * can demote to the next chain entry when a player exhausts its own retries.
   */
  import { getContext, onMount } from 'svelte';
  import VideoPlayer from './VideoPlayer.svelte';
  import WebRTCPlayer from './WebRTCPlayer.svelte';
  import FlvPlayer from './FlvPlayer.svelte';
  import MjpegLivePlayer from './MjpegLivePlayer.svelte';
  import { isAudioCapable } from '$lib/stream-selection';
  import type { Camera } from '$lib/api';
  import type { PlayerOrchestrator } from '$lib/player/orchestrator.svelte';
  import { healthFromStreamState, healthFromConnectionState, type HealthState } from '$lib/player/health';

  let {
    camera,
    expanded = false,
    tabVisible = true,
    streamUrl,
  }: {
    camera: Camera;
    expanded?: boolean;
    tabVisible?: boolean;
    /** HLS playlist URL (used only for the HLS mode). */
    streamUrl?: string;
  } = $props();

  const orchestrator = getContext<PlayerOrchestrator | undefined>('player-orchestrator');

  // The orchestrator's active mode is reactive (it's a $state read), so reading
  // it inside `$derived` re-runs whenever the orchestrator demotes/promotes.
  let mode = $derived(orchestrator?.activeMode(camera.id) ?? null);

  // Derived per-camera fields so they stay correct if the parent passes a
  // refreshed `camera` object (e.g. after a camera-list reload). Referencing
  // `camera.id` directly in module scope would capture only the initial value.
  let cameraId = $derived(camera.id);
  let cameraName = $derived(camera.name || camera.id);
  let codec = $derived((camera.encoding || camera.stream_encoding || '').toLowerCase());
  let audioCapable = $derived(isAudioCapable(camera));

  function report(h: HealthState): void {
    orchestrator?.reportHealth(cameraId, h);
  }

  /**
   * Single DOM-event listener on the wrapper div. Every player bubbles a
   * `statechange` CustomEvent with `detail.state`. WasmPlayer's states are the
   * ConnectionState set (includes 'disconnected'/'offline'); the others use the
   * StreamState set ('loading'|'buffering'|'playing'|'error'|'snapshot'). We
   * distinguish by membership and map accordingly.
   */
  function handleStateChange(e: Event): void {
    const detail = (e as CustomEvent).detail as { cameraId?: string; state?: string } | undefined;
    if (!detail || detail.cameraId !== cameraId || !detail.state) return;
    // 'disconnected'/'offline' only appear in WasmPlayer's ConnectionState;
    // route through the connection-state mapper for those, else stream-state.
    if (detail.state === 'disconnected' || detail.state === 'offline') {
      report(healthFromConnectionState(detail.state));
    } else {
      report(healthFromStreamState(detail.state));
    }
  }

  // A player has exhausted its own reconnects (FLV/WebRTC onProtocolFailed, or
  // WasmPlayer persistent decode errors). Report terminal failure so the
  // orchestrator demotes to the next chain entry — unless the chain is
  // exhausted, in which case returning false lets the player fall to snapshot.
  function onPlayerFailed(): boolean {
    report(healthFromStreamState('error'));
    const slot = orchestrator?.slot(cameraId);
    if (slot && slot.activeIndex + 1 < slot.chain.length) return true; // grid will demote
    return false; // chain exhausted — player may snapshot
  }

  // ─── Lazy WasmPlayer load ──────────────────────────────────────────────────
  let WasmPlayerComponent = $state<any>(null);
  let wasmLoadFailed = $state(false);

  async function ensureWasmPlayer(): Promise<void> {
    if (WasmPlayerComponent || wasmLoadFailed) return;
    try {
      const mod = await import('./WasmPlayer.svelte');
      WasmPlayerComponent = mod.default;
    } catch {
      wasmLoadFailed = true;
    }
  }

  // When the orchestrator selects wasm, ensure the chunk is loaded. The render
  // below waits on WasmPlayerComponent before mounting.
  $effect(() => {
    if (mode === 'wasm') {
      void ensureWasmPlayer();
    }
  });

  // If the WasmPlayer chunk fails to load, report a fatal failure so the
  // orchestrator demotes to the next chain entry (webrtc/flv/hls). Reported in
  // an effect (not the template) to avoid render-time side effects.
  $effect(() => {
    if (mode === 'wasm' && wasmLoadFailed) {
      report(healthFromStreamState('error'));
    }
  });

  onMount(() => {
    return () => {
      // On unmount, drop this camera from the orchestrator so its timers/cooldowns
      // don't leak. The parent route owns the initial register() call.
      orchestrator?.unregisterCamera(cameraId);
    };
  });

  function getHlsStreamUrl(): string {
    return streamUrl ?? `/api/cameras/${cameraId}/stream/index.m3u8`;
  }
</script>

<div class="contents" onstatechange={handleStateChange}>
  {#if mode === 'wasm'}
    {#if WasmPlayerComponent}
      {@const WasmPlayer = WasmPlayerComponent}
      <WasmPlayer
        {cameraId}
        {cameraName}
        {codec}
        {expanded}
        {tabVisible}
        onFallbackNeeded={() => report(healthFromStreamState('error'))}
      />
    {:else if !wasmLoadFailed}
      <!-- Loading the WebCodecs/AI chunk (~180KB) -->
      <div class="absolute inset-0 flex items-center justify-center bg-black/80">
        <div class="w-4 h-4 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
      </div>
    {:else}
      <!-- Chunk failed to load — the effect above reports failure so the
           orchestrator demotes to the next chain entry. -->
      <div class="absolute inset-0 flex items-center justify-center bg-black/80">
        <span class="text-white/40 text-xs">WebCodecs unavailable</span>
      </div>
    {/if}
  {:else if mode === 'webrtc'}
    <WebRTCPlayer
      {cameraId}
      {cameraName}
      {expanded}
      {tabVisible}
      hasAudio={audioCapable}
      onProtocolFailed={onPlayerFailed}
    />
  {:else if mode === 'flv'}
    <FlvPlayer
      {cameraId}
      {cameraName}
      {expanded}
      {tabVisible}
      hasAudio={audioCapable}
      onProtocolFailed={onPlayerFailed}
    />
  {:else if mode === 'hls'}
    <VideoPlayer
      {cameraId}
      {cameraName}
      streamUrl={getHlsStreamUrl()}
      cameraProtocol={camera.protocol}
      protocol="ll-hls"
      {expanded}
      {tabVisible}
      hasAudio={audioCapable}
    />
  {:else if mode === 'mjpeg'}
    <MjpegLivePlayer
      {cameraId}
      {cameraName}
      {expanded}
    />
  {/if}
</div>
