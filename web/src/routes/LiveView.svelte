<script lang="ts">
  import { onMount, onDestroy, setContext } from 'svelte';
  import { getCamera, listProtocols, getCameraProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, normalizeProtocol, getProtocolCapabilities, getDeviceCapabilities } from '$lib/api';
  import type { Camera, ProtocolInfo, DeviceCapabilitiesInfo } from '$lib/api';
  import { ArrowLeft, Maximize, Minimize, AlertCircle, RefreshCw, ChevronDown, ChevronRight, Image, Move, Activity } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';
  import TwoWayAudioButton from '../components/TwoWayAudioButton.svelte';
  import CameraPlayer from '../components/CameraPlayer.svelte';
  import ProtocolSwitcher from '../components/ProtocolSwitcher.svelte';
  import type { StreamingProtocol } from '../components/ProtocolSwitcher.svelte';
  import SnapshotButton from '../components/SnapshotButton.svelte';
  import ImagingPanel from '$lib/components/ImagingPanel.svelte';
  import PresetManager from '$lib/components/PresetManager.svelte';
  import ONVIFEvents from '$lib/components/ONVIFEvents.svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { createPlayerOrchestrator, makeRegistration, type PlayerOrchestrator } from '$lib/player/orchestrator.svelte';
  import { probeCaps } from '$lib/player/capabilities-cache';
  import { getCameraProtocolOverride } from '$lib/preferences';
  import type { ProtocolsResponse } from '$lib/stream-selection';
  let { cameraId = '' }: { cameraId?: string } = $props();

  let camera = $state<Camera | null>(null);
  let loading = $state(true);
  let error = $state('');
  let isFullscreen = $state(false);
  let playerContainer: HTMLDivElement | undefined = $state();
  let protocolsMap = $state<Map<string, ProtocolInfo>>(buildProtocolsMap(DEFAULT_PROTOCOLS));
  let switchingProtocol = $state(false);

  // Player orchestrator — owns the candidate chain + adaptive degrade/upgrade
  // for this single camera. Provided to CameraPlayer via context.
  const orchestrator: PlayerOrchestrator = createPlayerOrchestrator();
  setContext('player-orchestrator', orchestrator);
  const _orchUnsub = orchestrator.onModeChange((_id, _from, to, reason) => {
    if (reason === 'upgrade-reverted') {
      showToast(t('surveillance.upgradeReverted', { camera: '', protocol: to }) || `Reverted to ${to}`, 'warning');
    } else if (reason.startsWith('upgrade')) {
      showToast(t('surveillance.upgraded', { camera: '', protocol: to }) || `Upgraded to ${to}`, 'success');
    } else {
      showToast(t('surveillance.protocolFallback', { protocol: to }), 'info');
    }
  });
  // The active mode the player renders, read reactively from the orchestrator.
  let activeMode = $derived(camera ? orchestrator.activeMode(camera.id) : null);

  // ONVIF capabilities
  let deviceCaps = $state<DeviceCapabilitiesInfo | null>(null);
  let capsLoading = $state(false);
  let showImaging = $state(false);
  let showPresets = $state(false);
  let showEvents = $state(false);

  function isHlsSupported(cam: Camera): boolean {
    return getProtocolCapabilities(cam.protocol, protocolsMap).hls;
  }
  function canStream(cam: Camera): boolean {
    // The camera can stream if the orchestrator has any real-time mode for it
    // (mjpeg included). This drives the protocol switcher / PTZ visibility.
    return orchestrator.activeMode(cam.id) !== null;
  }

  function isPtzSupported(cam: Camera): boolean {
    return getProtocolCapabilities(cam.protocol, protocolsMap).ptz;
  }

  function isOnvifCamera(cam: Camera): boolean {
    return normalizeProtocol(cam.protocol) === 'onvif';
  }

  function isXiaomiCamera(cam: Camera): boolean {
    return normalizeProtocol(cam.protocol) === 'xiaomi';
  }

  async function loadCapabilities() {
    if (!camera || !isOnvifCamera(camera)) {
      deviceCaps = null;
      return;
    }
    capsLoading = true;
    try {
      deviceCaps = await getDeviceCapabilities(camera.id);
    } catch (e) {
      console.warn('Failed to load device capabilities:', e);
      deviceCaps = null;
    } finally {
      capsLoading = false;
    }
  }

  async function loadCamera() {
    loading = true;
    error = '';
    try {
      camera = await getCamera(cameraId);
      // Probe caps + fetch the per-camera protocol ranking, then register the
      // camera with the orchestrator so it can build the candidate chain and
      // drive adaptive selection. A per-camera user override (from a prior
      // ProtocolSwitcher selection) is honored and pins the chain.
      await probeCaps();
      let resp: ProtocolsResponse | null = null;
      try {
        resp = (await getCameraProtocols(cameraId)) as unknown as ProtocolsResponse;
      } catch {
        resp = null; // /protocols unreachable — orchestrator falls back to HLS
      }
      if (camera) {
        orchestrator.registerCamera(
          makeRegistration(camera, resp, {
            override: getCameraProtocolOverride(camera.id),
            isHlsCapable: isHlsSupported(camera),
            isUnsupported: false,
          }),
        );
      }
    } catch (e) {
      error = e instanceof Error ? e.message : t('live.failedLoadCamera');
      camera = null;
    } finally {
      loading = false;
    }
  }

  function goBack() {
    window.location.hash = '#/cameras';
  }

  function toggleFullscreen() {
    if (!playerContainer) return;
    try {
      if (!document.fullscreenElement) {
        playerContainer.requestFullscreen();
        isFullscreen = true;
      } else {
        document.exitFullscreen();
        isFullscreen = false;
      }
    } catch (e) { console.warn('Fullscreen not supported:', e); }
  }

  function handleFullscreenChange() {
    isFullscreen = !!document.fullscreenElement;
  }

  // The ProtocolSwitcher reports the user's manual choice. We pin it via the
  // orchestrator (which rebuilds the chain to a single-element pinned list,
  // disabling auto-degrade/upgrade to respect the explicit selection). Passing
  // null clears the override → back to auto-selection. ProtocolSwitcher handles
  // the localStorage persistence itself (setCameraProtocolOverride).
  function handleProtocolChange(protocol: StreamingProtocol | null) {
    switchingProtocol = true;
    if (camera) {
      orchestrator.setOverride(camera.id, protocol);
    }
    // Brief delay to show switching state, then mount new player
    setTimeout(() => { switchingProtocol = false; }, 100);
  }

  // Fetch capabilities when camera loads
  $effect(() => {
    if (camera && isOnvifCamera(camera)) {
      loadCapabilities();
    }
  });

  onMount(() => {
    if (!cameraId) {
      error = t('live.cameraIdRequired');
      loading = false;
      return;
    }

    loadCamera();
    document.addEventListener('fullscreenchange', handleFullscreenChange);
    // Load protocol capabilities, then re-register the camera so the
    // orchestrator's chain reflects the (possibly now-non-HLS-capable) protocol.
    listProtocols().then(list => {
      if (list && list.length > 0) protocolsMap = buildProtocolsMap(list);
      if (camera) {
        orchestrator.registerCamera(
          makeRegistration(camera, null, {
            override: getCameraProtocolOverride(camera.id),
            isHlsCapable: isHlsSupported(camera),
            isUnsupported: false,
          }),
        );
      }
    }).catch((e) => { console.warn('Failed to load protocols:', e); });
  });

  onDestroy(() => {
    document.removeEventListener('fullscreenchange', handleFullscreenChange);
    _orchUnsub();
    if (camera) orchestrator.unregisterCamera(camera.id);
    orchestrator.dispose();
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Loading state -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="th-color-danger mb-4 flex justify-center"><AlertCircle size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <div class="flex justify-center gap-3">
          <button onclick={loadCamera} class="btn btn-primary btn-sm flex items-center gap-1">
            <RefreshCw size={14} />
            {t('common.retry')}
          </button>
          <button onclick={goBack} class="btn btn-secondary btn-sm">
            {t('detail.back')}
          </button>
        </div>
      </div>
    {:else if camera}
      <div class="space-y-4">
        <!-- Header with camera name -->
        <div class="flex items-center gap-3 flex-wrap">
          <button onclick={goBack} class="btn btn-ghost btn-sm flex items-center gap-1">
            <ArrowLeft size={16} />
            {t('nav.cameras')}
          </button>
          <h2 class="text-xl font-bold th-text-primary truncate">
            {camera.name || camera.id}
          </h2>
          <span class="badge badge-neutral">{protocolsMap.get(camera.protocol)?.label || camera.protocol}</span>

          <!-- ONVIF controls shown for all ONVIF cameras -->
          {#if isOnvifCamera(camera) && deviceCaps?.snapshot}
            <SnapshotButton cameraId={camera.id} />
          {/if}

          <!-- Two-way audio button for Xiaomi cameras with two_way_audio_enabled -->
          {#if isXiaomiCamera(camera) && camera.two_way_audio_enabled && canStream(camera)}
            <TwoWayAudioButton
              cameraId={camera.id}
              enabled={camera.status === 'recording' || camera.status === 'active'}
              cameraName={camera.name || camera.id}
            />
          {/if}

          {#if canStream(camera)}
            <div class="flex-1"></div>
            <!-- Protocol Switcher. `selected` reflects the orchestrator's current
                 active mode (auto-selected by default); the user's manual choice
                 pins the chain via setOverride. -->
            <ProtocolSwitcher
              cameraId={camera.id}
              cameraEncoding={camera.encoding || camera.stream_encoding || ''}
              selected={(activeMode ?? 'auto') as StreamingProtocol}
              onchange={handleProtocolChange}
            />
            <button onclick={toggleFullscreen} class="btn btn-ghost btn-sm flex items-center gap-1">
              {#if isFullscreen}
                <Minimize size={16} />
              {:else}
                <Maximize size={16} />
              {/if}
            </button>
          {/if}
        </div>

        {#if canStream(camera)}
          <!-- Player container -->
          <div
            class="card border th-border overflow-hidden"
            style="max-height: 80vh;"
            bind:this={playerContainer}
            onshrink={() => goBack()}
          >
            {#if switchingProtocol}
              <!-- Switching state -->
              <div class="relative w-full bg-black" style="aspect-ratio: 16/9;">
                <div class="absolute inset-0 flex items-center justify-center">
                  <div class="flex items-center gap-2">
                    <div class="w-3 h-3 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
                    <span class="text-white/50 text-xs">{t('live.protocol.switching')}</span>
                  </div>
                </div>
              </div>
            {:else}
              <!-- CameraPlayer reads the orchestrator's active mode and renders
                   the matching player. The orchestrator owns degrade/upgrade;
                   ProtocolSwitcher pins the chain on manual selection. -->
              <CameraPlayer
                {camera}
                expanded={true}
                tabVisible={true}
                streamUrl={`/api/cameras/${cameraId}/stream/index.m3u8`}
              />
            {/if}
          </div>
        {:else}
          <!-- Unsupported protocol -->
          <div class="card p-12 text-center">
            <div class="th-text-muted mb-4 flex justify-center"><AlertCircle size={48} /></div>
            <h3 class="text-lg font-medium th-text-primary mb-2">{t('live.notSupported')}</h3>
            <p class="th-text-secondary text-sm mb-4">
              {t('live.notSupportedDesc')}
              <span class="font-mono th-text-primary">{protocolsMap.get(normalizeProtocol(camera.protocol))?.label || camera.protocol}</span>.
            </p>
            <button onclick={goBack} class="btn btn-secondary btn-sm">
              {t('live.backToCameras')}
            </button>
          </div>
        {/if}
        
        <!-- PTZ Control for PTZ-capable ONVIF cameras. Default to hidden until
             capabilities confirm PTZ support — otherwise a fixed (non-PTZ) bullet
             camera shows a dead PTZ pad while caps are still loading. -->
        {#if isPtzSupported(camera) && (deviceCaps?.ptz ?? false)}
          <div class="card">
            <PtzControl {cameraId} enabled={true} protocol={camera.protocol} />
          </div>
        {/if}

        <!-- Xiaomi PTZ controls (separate from ONVIF) -->
        {#if isXiaomiCamera(camera)}
          <div class="card">
            <PtzControl {cameraId} enabled={true} protocol={camera.protocol} />
          </div>
        {/if}

        <!-- ONVIF collapsible panels -->
        {#if isOnvifCamera(camera) && !capsLoading}
          {#if deviceCaps}
            <!-- Imaging Panel (collapsible) -->
            {#if deviceCaps?.imaging}
              <details class="onvif-collapsible" bind:open={showImaging}>
                <summary class="onvif-collapsible-summary">
                  <div class="onvif-collapsible-title-row">
                    {#if showImaging}
                      <ChevronDown size={16} />
                    {:else}
                      <ChevronRight size={16} />
                    {/if}
                    <Image size={16} />
                    <span>{t('onvif.imaging.title')}</span>
                  </div>
                </summary>
                <div class="onvif-collapsible-body">
                  <ImagingPanel cameraId={camera.id} />
                </div>
              </details>
            {/if}

            <!-- Preset Manager (collapsible) -->
            {#if deviceCaps?.ptz}
              <details class="onvif-collapsible" bind:open={showPresets}>
                <summary class="onvif-collapsible-summary">
                  <div class="onvif-collapsible-title-row">
                    {#if showPresets}
                      <ChevronDown size={16} />
                    {:else}
                      <ChevronRight size={16} />
                    {/if}
                    <Move size={16} />
                    <span>{t('onvif.presets.title')}</span>
                  </div>
                </summary>
                <div class="onvif-collapsible-body">
                  <PresetManager cameraId={camera.id} />
                </div>
              </details>
            {/if}

            <!-- ONVIF Events (collapsible) -->
            {#if deviceCaps?.events}
              <details class="onvif-collapsible" bind:open={showEvents}>
                <summary class="onvif-collapsible-summary">
                  <div class="onvif-collapsible-title-row">
                    {#if showEvents}
                      <ChevronDown size={16} />
                    {:else}
                      <ChevronRight size={16} />
                    {/if}
                    <Activity size={16} />
                    <span>{t('onvif.events.title')}</span>
                  </div>
                </summary>
                <div class="onvif-collapsible-body">
                  <ONVIFEvents cameraId={camera.id} maxEvents={50} />
                </div>
              </details>
            {/if}
          {:else}
            <div class="text-xs th-text-muted p-3">
              {t('cameras.capabilities_unavailable')}
            </div>
          {/if}
        {/if}
      </div>
    {/if}
  </main>
</div>

<style>
  .onvif-collapsible {
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    overflow: hidden;
    background-color: var(--bg-elevated);
  }

  .onvif-collapsible[open] {
    border-color: var(--border-hover);
  }

  .onvif-collapsible-summary {
    display: flex;
    align-items: center;
    padding: 0.75rem 1rem;
    cursor: pointer;
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--text-primary);
    background-color: var(--bg-secondary);
    user-select: none;
    transition: background-color var(--duration-fast) var(--ease-out);
    list-style: none;
  }

  .onvif-collapsible-summary::-webkit-details-marker {
    display: none;
  }

  .onvif-collapsible-summary:hover {
    background-color: var(--bg-hover);
  }

  .onvif-collapsible-title-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--text-secondary);
  }

  .onvif-collapsible-body {
    padding: 0.75rem;
  }
</style>
