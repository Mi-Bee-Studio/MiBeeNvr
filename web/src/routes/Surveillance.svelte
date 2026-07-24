<script lang="ts">
  import { onMount, setContext } from 'svelte';
  import { getDashboardCameras, getCredentials, listProtocols, getCameraProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, normalizeProtocol, getProtocolCapabilities, getHealthCameras } from '$lib/api';
  import type { Camera, ProtocolInfo, CameraProtocolsResponse } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { Loader2, AlertCircle, Video, VideoOff, X, Settings, ImageOff, CircleCheck, CirclePause } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import WebRTCPlayer from '../components/WebRTCPlayer.svelte';
  import FlvPlayer from '../components/FlvPlayer.svelte';
  import MjpegLivePlayer from '../components/MjpegLivePlayer.svelte';
  // WasmPlayer is lazy-loaded to keep main bundle small (~180 KB WebCodecs/AI deps)
  import { getStreamingSettings } from '$lib/api/settings';
  import { formatDate } from '$lib/format';
  import { createSnapshotManager } from '$lib/snapshot';
  import { createReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';
  import { detectMSEH265, probeMSEH265, detectWebCodecs, detectWasmH265 } from '$lib/webcodecs-player/capabilities';
  import { pickCameraMode, nextAfter, isAudioCapable, type CameraMode, type BrowserCaps, type ProtocolsResponse } from '$lib/stream-selection';
  import { getCameraProtocolOverride } from '$lib/preferences';

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let expandedCameraId = $state<string | null>(null);

  // Page Visibility — pause/resume all players when tab hidden/visible
  let tabVisible = $state(true);

  let ptzOpenIndex = $state(-1);

  let allCameras = $state<Camera[]>([]);
  let configOpen = $state(false);
  let selectedCameraIds = $state<string[]>([]);
  let pendingCameraIds = $state<string[]>([]);

  // Snapshot state
  let snapshotUrls = $state<Record<string, string>>({});
  let snapshotLoading = $state<Record<string, boolean>>({});
  let snapshotTransientErrors = $state<Record<string, boolean>>({});
  let healthScores = $state<Record<string, number>>({});

  // Snapshot manager — handles fetch, interval, and cleanup lifecycle
  const snapshotMgr = createSnapshotManager({
    intervalMs: 3000,
    getCredentials,
    onUrlUpdate: (id, url) => { snapshotUrls[id] = url; },
    onUrlRevoke: (id) => {
      if (snapshotUrls[id]) { URL.revokeObjectURL(snapshotUrls[id]); delete snapshotUrls[id]; }
    },
    onLoadingChange: (id, val) => { snapshotLoading[id] = val; },
    onErrorChange: (id, val) => {
      if (val) { snapshotTransientErrors[id] = true; } else { delete snapshotTransientErrors[id]; }
    },
    onUnsupported: (id) => { /* tracked internally by manager */ },
  });

  // Protocol capabilities for capability-based checks
  let protocolsMap = $state<Map<string, ProtocolInfo>>(buildProtocolsMap(DEFAULT_PROTOCOLS));
  const STORAGE_KEY = 'dashboard-selected-cameras';

  // Default streaming protocol from settings — used ONLY as a legacy fallback
  // when the per-camera /protocols endpoint can't be reached. The primary
  // selection now comes from the backend's codec-aware per-camera ranking.
  let defaultProtocol = $state<string>('flv');

  // Per-camera protocol responses from GET /api/cameras/{id}/protocols.
  // Keyed by camera id. Fetched in parallel on mount and whenever the grid
  // selection changes; null means "not yet fetched or fetch failed" (the
  // picker then falls back to the legacy global default). This is what lets
  // the grid auto-select the best protocol PER CAMERA instead of applying
  // one global protocol to a mixed fleet.
  let cameraProtocols = $state<Map<string, CameraProtocolsResponse | null>>(new Map());

  // H.265/HEVC MSE support — detected once on mount. When the browser's
  // MediaSource cannot decode H.265 (common on Linux desktop, or Windows
  // without the HEVC Video Extensions pack), FLV players connect but render
  // a black screen. We use this to auto-degrade H.265 cameras to HLS, which
  // has broader native H.265 support on modern browsers.
  let browserSupportsH265MSE = $state(true);

  // WebCodecs (VideoDecoder) availability — enables the WASM player. Detected
  // once on mount and fed into pickCameraMode so it can promote/demote wasm.
  let browserSupportsWebCodecs = $state(false);

  // libde265 WASM H.265 soft-decoder availability — enables H.265 on plain HTTP
  // (where WebCodecs is unavailable) via Canvas2D rendering.
  let browserSupportsWasmH265 = $state(false);

  // Snapshot of the browser caps consumed by pickCameraMode. Recomputed from
  // the $state flags above so the picker stays a pure function.
  function browserCaps(): BrowserCaps {
    return { h265MSE: browserSupportsH265MSE, webCodecs: browserSupportsWebCodecs, wasmH265: browserSupportsWasmH265 };
  }

  // Lazy-loaded WasmPlayer component (only loads when 'wasm' protocol is selected)
  let WasmPlayerComponent = $state<any>(null);
  let wasmPlayerLoading = $state(false);
  let wasmPlayerError = $state('');

  async function loadWasmPlayer() {
    if (WasmPlayerComponent || wasmPlayerLoading) return;
    wasmPlayerLoading = true;
    wasmPlayerError = '';
    try {
      const mod = await import('../components/WasmPlayer.svelte');
      WasmPlayerComponent = mod.default;
    } catch (e) {
      console.error('Failed to load WasmPlayer:', e);
      wasmPlayerError = String(e);
      showToast(t('dashboard.wasmPlayerFailed'), 'error');
    } finally {
      wasmPlayerLoading = false;
    }
  }

  // Reconnection coordinator — limits concurrent reconnects, global exponential backoff,
  // and backend pressure detection (HTTP 503 triggers 10s global cooldown)
  const reconnectCoordinator = createReconnectCoordinator();
  setContext('reconnect-coordinator', reconnectCoordinator);

  function loadSavedCameraIds(): string[] {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (raw) {
        const ids: string[] = JSON.parse(raw);
        if (Array.isArray(ids)) return ids;
      }
    } catch (e) { console.warn('Failed to load saved camera IDs:', e); }
    return [];
  }

  function saveCameraIds(ids: string[]) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(ids));
  }

  function toggleCameraSelection(cameraId: string) {
    if (pendingCameraIds.includes(cameraId)) {
      pendingCameraIds = pendingCameraIds.filter(id => id !== cameraId);
    } else if (pendingCameraIds.length < 4) {
      pendingCameraIds = [...pendingCameraIds, cameraId];
    }
  }

  function applyCameraSelection() {
    selectedCameraIds = [...pendingCameraIds];
    saveCameraIds(selectedCameraIds);
    const available = new Map(allCameras.map(c => [c.id, c]));
    const filtered = selectedCameraIds
      .map(id => available.get(id))
      .filter((c): c is Camera => c !== undefined);
    cameras = filtered;
    // Clear per-camera runtime fallbacks when the grid selection changes, so a
    // camera that was demoted during a previous session starts fresh.
    runtimeFallback = {};
    configOpen = false;
    // Fetch per-camera protocol rankings for the newly selected cameras. This
    // is what drives auto-selection; runs in parallel and never blocks render.
    void refreshCameraProtocols(filtered.map((c) => c.id));
  }

  function getStreamUrl(cameraId: string): string {
    return `/api/cameras/${cameraId}/stream/index.m3u8`;
  }

  function getGridClass(count: number): string {
    if (count <= 1) return 'grid-cols-1';
    if (count === 2) return 'grid-cols-1 sm:grid-cols-2';
    return 'grid-cols-1 sm:grid-cols-2';
  }

  function getCellClass(camera: Camera, index: number, count: number): string {
    if (expandedCameraId) {
      return camera.id === expandedCameraId
        ? 'col-span-2 row-span-2'
        : 'hidden';
    }
    if (count === 3 && index === 0) {
      return 'col-span-2';
    }
    return '';
  }

  function getStatusBadge(camera: Camera): { class: string; label: string; icon: any; text: string } {
    const status = camera.status?.toLowerCase() || '';
    if (status === 'recording' || status === 'active') {
      return { class: 'badge-success', label: '●', icon: CircleCheck, text: t('cameras.statusRecording') };
    }
    if (status === 'error' || status === 'failed') {
      return { class: 'badge-error', label: '●', icon: AlertCircle, text: t('cameras.statusError') };
    }
    return { class: 'badge-neutral', label: '●', icon: CirclePause, text: t('cameras.statusStopped') };
  }

  function isHlsSupported(camera: Camera): boolean {
    return getProtocolCapabilities(camera.protocol, protocolsMap).hls;
  }

  // Per-camera protocol override forced at runtime by a failed-player fallback.
  // When a player exhausts its reconnect attempts, the grid demotes that camera
  // to the next available protocol in its backend-provided chain (webrtc→flv→
  // hls→mjpeg) rather than dropping straight to a static snapshot. Keyed by
  // camera id; cleared when the grid selection changes.
  let runtimeFallback = $state<Record<string, CameraMode>>({});

  // The primary per-camera mode resolver. Delegates to the pure pickCameraMode
  // helper (src/lib/stream-selection.ts), passing the cached backend protocol
  // ranking + browser caps. Falls back to the legacy global default when the
  // backend response isn't available (camera still connecting, endpoint down).
  function getCameraMode(camera: Camera): CameraMode {
    // A runtime fallback takes precedence over the auto-selected mode — the
    // camera already proved the primary choice unworkable.
    if (runtimeFallback[camera.id]) {
      return runtimeFallback[camera.id];
    }
    const resp = cameraProtocols.get(camera.id) ?? null;
    // A per-camera user override (set via the LiveView ProtocolSwitcher) wins
    // over the backend default, as long as it's still usable for this codec.
    const override = getCameraProtocolOverride(camera.id);
    return pickCameraMode(camera, resp as ProtocolsResponse | null, browserCaps(), {
      override,
      legacyDefault: defaultProtocol,
      isHlsCapable: isHlsSupported(camera),
      isUnsupported: snapshotMgr.isUnsupported(camera.id),
    });
  }

  // Called by a player when it has exhausted reconnects and would otherwise
  // fall back to a static snapshot. Instead, demote to the next real-time
  // protocol in the camera's backend-provided chain; only when the chain is
  // exhausted do we let the snapshot fallback take over (return false).
  function handleProtocolFailed(cameraId: string, current: CameraMode): boolean {
    const resp = cameraProtocols.get(cameraId) ?? null;
    const next = nextAfter(current, resp as ProtocolsResponse | null);
    if (next && next !== current) {
      runtimeFallback = { ...runtimeFallback, [cameraId]: next };
      showToast(
        t('surveillance.protocolFallback', { protocol: protocolLabel(next) }),
        'info',
      );
      return true; // tell the player the grid is handling it (don't snapshot yet)
    }
    return false; // chain exhausted — player may fall back to snapshot
  }

  function protocolLabel(mode: CameraMode): string {
    switch (mode) {
      case 'wasm': return 'WebCodecs';
      case 'webrtc': return 'WebRTC';
      case 'flv': return 'FLV';
      case 'hls': return 'HLS';
      case 'mjpeg': return 'MJPEG';
      default: return mode;
    }
  }

  // Fetch per-camera protocol rankings for the given camera ids, in parallel,
  // and cache them. Best-effort: failures store null so the picker falls back
  // to the legacy global default rather than blocking the grid.
  async function refreshCameraProtocols(ids: string[]): Promise<void> {
    if (ids.length === 0) return;
    const results = await Promise.allSettled(ids.map((id) => getCameraProtocols(id)));
    const next = new Map(cameraProtocols);
    for (let i = 0; i < ids.length; i++) {
      const id = ids[i];
      const r = results[i];
      next.set(id, r.status === 'fulfilled' ? r.value : null);
    }
    cameraProtocols = next;
  }

  // Preload WasmPlayer when any camera would use 'wasm' mode
  $effect(() => {
    if (cameras.some((c) => getCameraMode(c) === 'wasm')) {
      loadWasmPlayer();
    }
  });

  // --- Expand / shrink ---

  function expandToHls(cameraId: string) {
    expandedCameraId = cameraId;
  }

  function shrinkToGrid() {
    expandedCameraId = null;
  }

  function handleFullscreenChange() {
    if (!document.fullscreenElement) {
      shrinkToGrid();
    }
  }
  function handleCellClick(camera: Camera, index: number) {
    if (expandedCameraId === camera.id) {
      shrinkToGrid();
      return;
    }
    // Any playable camera (including MJPEG) can be expanded to fullscreen cell.
    // Snapshot-only and unsupported cameras stay locked to the grid.
    const mode = getCameraMode(camera);
    if (mode !== 'snapshot' && mode !== 'unsupported') {
      expandToHls(camera.id);
    }
  }
  function handleCellDblClick(camera: Camera) {
    if (expandedCameraId === camera.id) {
      shrinkToGrid();
    }
  }


  function closePtz() {
    ptzOpenIndex = -1;
  }


  // --- Lifecycle ---

  onMount(async () => {
    // Detect browser playback capabilities once — fed into pickCameraMode so it
    // can auto-degrade (H.265 FLV→HLS without MSE) or promote (wasm with WebCodecs).
    // probeMSEH265() is authoritative: isTypeSupported('hvc1') is a known false
    // positive on Chromium/Edge (MSE accepts the bytes but never buffers them →
    // black screen), so we probe by appending a real hvc1 init segment. Until the
    // probe resolves, detectMSEH265() conservatively returns false.
    browserSupportsH265MSE = await probeMSEH265();
    browserSupportsWebCodecs = detectWebCodecs();
    browserSupportsWasmH265 = detectWasmH265();
    try {
      const fetched = await getDashboardCameras();
      const activeFetched = fetched;
      allCameras = activeFetched;
      const savedIds = loadSavedCameraIds();
      if (savedIds.length > 0) {
        const available = new Map(activeFetched.map(c => [c.id, c]));
        const filtered = savedIds
          .map(id => available.get(id))
          .filter((c): c is Camera => c !== undefined);
        selectedCameraIds = filtered.map(c => c.id);
        cameras = filtered;
      } else {
        cameras = activeFetched.slice(0, 4);
        selectedCameraIds = cameras.map(c => c.id);
      }
      pendingCameraIds = [...selectedCameraIds];
      // Fetch per-camera protocol rankings so the grid can auto-select the best
      // protocol per camera. Non-blocking: cameras render immediately with the
      // legacy global default and re-resolve once the responses arrive.
      void refreshCameraProtocols(cameras.map((c) => c.id));
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
    // Fetch camera health scores (public, no auth)
    try {
      const healthData = await getHealthCameras();
      const scores: Record<string, number> = {};
      for (const [id, detail] of Object.entries(healthData)) {
        scores[id] = detail.score;
      }
      healthScores = scores;
    } catch (e) {
      console.warn('Failed to load camera health scores:', e);
    }
    // Load protocol capabilities
    try {
      const list = await listProtocols();
      if (list && list.length > 0) {
        protocolsMap = buildProtocolsMap(list);
      }
    } catch (e) {
      console.warn('Failed to load protocol capabilities:', e);
    }
    // Load default streaming protocol from settings
    try {
      const config = await getStreamingSettings();
      if (config.default_protocol) {
        defaultProtocol = config.default_protocol;
      }
    } catch (e) {
      console.warn('Failed to load streaming settings:', e);
    }
    document.addEventListener('fullscreenchange', handleFullscreenChange);

    // Page Visibility API: pause players when tab hidden, resume when visible
    const visibilityHandler = () => {
      tabVisible = !document.hidden;
    };
    document.addEventListener('visibilitychange', visibilityHandler);

    // Intercept fetch to detect backend pressure (HTTP 503 → global cooldown)
    const originalFetch = window.fetch;
    window.fetch = async function (...args: Parameters<typeof fetch>): Promise<Response> {
      const response = await originalFetch.apply(this, args);
      if (response.status === 503) {
        reconnectCoordinator.reportBackendPressure();
      }
      return response;
    };


    return () => {
      document.removeEventListener('fullscreenchange', handleFullscreenChange);
      document.removeEventListener('visibilitychange', visibilityHandler);
      window.fetch = originalFetch;
      reconnectCoordinator.dispose();
    };
  });

  let prevVisibleIds: Set<string> = new Set();

  // React to camera list changes — init/teardown snapshot cameras
  // Cleanup return ensures intervals are cleared on component destroy
  $effect(() => {
    const _cameras = cameras;
    const _loading = loading;
    if (_loading || _cameras.length === 0) return;

    const visibleIds = new Set(_cameras.map(c => c.id));

    // Cleanup snapshot cameras that were removed
    for (const id of prevVisibleIds) {
      if (!visibleIds.has(id)) {
        snapshotMgr.stopRefresh(id);
      }
    }

    // Init snapshot cameras that were added
    for (const cam of _cameras) {
      if (prevVisibleIds.has(cam.id)) continue;

      const mode = getCameraMode(cam);
      if (mode === 'snapshot') {
        snapshotMgr.startRefresh(cam.id);
      }
    }

    prevVisibleIds = visibleIds;

    // Cleanup: stop all snapshot refreshes when effect re-runs or component unmounts
    return () => {
      snapshotMgr.stopAll();
    };
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="mx-auto px-3 sm:px-4 lg:px-6 py-4 sm:py-6" style="max-width: 100%;">

    <!-- Header -->
    <div class="flex items-center justify-between mb-4 sm:mb-6">
      <h1 class="text-lg sm:text-xl font-bold th-text-primary flex items-center gap-2">
        <Video size={20} class="text-accent" />
        {t('surveillance.title')}
      </h1>
      <button
        class="btn btn-ghost p-2"
        onclick={() => { configOpen = !configOpen; pendingCameraIds = [...selectedCameraIds]; }}
        title={t('dashboard.configure')}
      >
        <Settings size={18} />
      </button>
    </div>

    <!-- Camera configuration panel -->
    {#if configOpen}
      <div class="card p-4 mb-4">
        <h3 class="text-sm font-semibold th-text-primary mb-3">{t('dashboard.selectCameras')}</h3>
        <p class="text-xs th-text-secondary mb-3">{t('dashboard.maxCameras')}</p>
        <div class="space-y-1 max-h-48 overflow-y-auto mb-4">
          {#each allCameras as camera}
            <label class="flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-[var(--bg-tertiary)] cursor-pointer transition-colors">
              <input
                type="checkbox"
                checked={pendingCameraIds.includes(camera.id)}
                onchange={() => toggleCameraSelection(camera.id)}
                disabled={!pendingCameraIds.includes(camera.id) && pendingCameraIds.length >= 4}
                class="accent-[var(--color-primary)]"
              />
              <span class="text-sm th-text-primary">{camera.name || camera.id}</span>
              <span class="text-xs th-text-muted ml-auto">{camera.protocol}</span>
            </label>
          {/each}
        </div>
        <div class="flex justify-end gap-2">
          <button
            class="btn btn-ghost text-sm px-3 py-1.5"
            onclick={() => configOpen = false}
          >
            {t('common.dismiss')}
          </button>
          <button
            class="btn btn-primary text-sm px-3 py-1.5"
            onclick={applyCameraSelection}
          >
            {t('dashboard.apply')}
          </button>
        </div>
      </div>
    {/if}

    <!-- Loading state -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="flex flex-col items-center gap-3">
          <div class="spinner spinner-lg"></div>
          <span class="text-sm th-text-secondary">{t('common.loading')}</span>
        </div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="th-color-danger mb-4 flex justify-center"><AlertCircle size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
      </div>
    {:else if cameras.length === 0}
      <!-- Empty state -->
      <div class="card p-8 sm:p-12 text-center">
        <div class="th-text-muted mb-4 flex justify-center"><VideoOff size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('dashboard.noCameras')}</h3>
        <p class="th-text-secondary text-sm">{t('dashboard.noCamerasHint')}</p>
      </div>
    {:else}
      <!-- Camera grid -->
      <div
        class="grid gap-2 sm:gap-3 {getGridClass(cameras.length)}"
        onexpand={(e: CustomEvent) => expandToHls(e.detail.cameraId)}
        onshrink={(e: CustomEvent) => shrinkToGrid()}
      >
        {#each cameras as camera, index}
{@const status = getStatusBadge(camera)}
          {@const mode = getCameraMode(camera)}
          {@const StatusIcon = status.icon}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="relative bg-black rounded-lg overflow-hidden group camera-grid-cell {getCellClass(camera, index, cameras.length)}"
            class:cell-expanded={expandedCameraId === camera.id}
            style="min-height: {cameras.length === 1 ? 'calc(100vh - 140px)' : 'calc((100vh - 160px) / 2)'};"
            role="button"
            tabindex="0"
            aria-label="{camera.name || camera.id} — {status.text}"
            onclick={() => handleCellClick(camera, index)}
            onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleCellClick(camera, index); } }}
            ondblclick={() => handleCellDblClick(camera)}
          >
            {#if mode === 'snapshot'}
              <!-- Snapshot thumbnail mode (HTTP_JPEG cameras) -->
              {#if snapshotLoading[camera.id] && !snapshotUrls[camera.id]}
                <!-- Initial loading -->
                <div class="absolute inset-0 flex items-center justify-center bg-black/40">
                  <div class="flex flex-col items-center gap-2">
                    <Loader2 size={24} class="text-white animate-spin" />
                    <span class="text-white/70 text-xs">{t('common.loading')}</span>
                  </div>
                </div>
              {:else if snapshotUrls[camera.id]}
                <!-- Snapshot image -->
                <img
                  src={snapshotUrls[camera.id]}
                  alt={camera.name || camera.id}
                  class="w-full h-full object-contain"
                />
                <!-- Transient error overlay (keeps last good image visible) -->
                {#if snapshotTransientErrors[camera.id]}
                  <div class="absolute inset-0 bg-black/30 flex items-center justify-center pointer-events-none">
                    <span class="text-white/50 text-xs">{t('dashboard.snapshotError')}</span>
                  </div>
                {/if}
              {:else if snapshotTransientErrors[camera.id]}
                <!-- Error with no previous image -->
                <div class="absolute inset-0 flex items-center justify-center">
                  <div class="flex flex-col items-center gap-2">
                    <ImageOff size={24} class="text-white/40" />
                    <span class="text-white/50 text-xs">{t('dashboard.snapshotError')}</span>
                  </div>
                </div>
              {/if}

              <!-- Camera name + status overlay -->
              <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2">
                <div class="flex items-center gap-2">
                  <span class="badge {status.class} text-[10px] px-1.5 py-0.5 flex items-center gap-1">

                    <StatusIcon size={10} />
                    {status.text}
                  </span>
                  <span class="text-white text-sm font-medium truncate">{camera.name || camera.id}</span>
                </div>
              </div>

            {:else if mode === 'hls'}
              <VideoPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                streamUrl={getStreamUrl(camera.id)}
                cameraProtocol={camera.protocol}
                protocol={defaultProtocol}
                expanded={expandedCameraId === camera.id}
                {tabVisible}
                hasAudio={isAudioCapable(camera)}
              />

            {:else if mode === 'webrtc'}
              <WebRTCPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                expanded={expandedCameraId === camera.id}
                {tabVisible}
                hasAudio={isAudioCapable(camera)}
                onProtocolFailed={() => handleProtocolFailed(camera.id, 'webrtc')}
              />

            {:else if mode === 'flv'}
              <FlvPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                expanded={expandedCameraId === camera.id}
                {tabVisible}
                hasAudio={isAudioCapable(camera)}
                onProtocolFailed={() => handleProtocolFailed(camera.id, 'flv')}
              />

            {:else if mode === 'mjpeg'}
              <MjpegLivePlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                expanded={expandedCameraId === camera.id}
              />
            {:else if mode === 'wasm'}
              {#if WasmPlayerComponent}
                {@const WasmPlayer = WasmPlayerComponent}
                <WasmPlayer
                  cameraId={camera.id}
                  cameraName={camera.name || camera.id}
                  codec={(camera.encoding || camera.stream_encoding || '').toLowerCase()}
                  expanded={expandedCameraId === camera.id}
                  tabVisible={tabVisible}
                  onFallbackNeeded={() => handleProtocolFailed(camera.id)}
                />
              {:else if wasmPlayerLoading}
                <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                  <div class="flex flex-col items-center gap-2">
                    <div class="w-4 h-4 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
                    <span class="text-white/50 text-xs">{t('dashboard.loadingWasmPlayer')}</span>
                  </div>
                </div>
              {:else}
                <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                  <div class="flex flex-col items-center gap-2">
                    <AlertCircle size={20} class="text-red-400/60" />
                    <span class="text-white/50 text-xs">{t('dashboard.wasmPlayerLoadError')}</span>
                    <button class="text-xs text-white/40 underline" onclick={loadWasmPlayer}>{t('live.retry') || 'Retry'}</button>
                  </div>
                </div>
              {/if}

            {:else}
              <!-- Unsupported protocol (no snapshot, no HLS) -->
              <div class="absolute inset-0 flex items-center justify-center">
                <div class="flex flex-col items-center gap-2 text-center px-4">
                  <VideoOff size={24} class="text-white/40" />
                  <span class="text-white/50 text-xs">{t('live.notSupported')}</span>
                  <span class="text-white/30 text-[10px] font-mono">{camera.protocol}</span>
                </div>
              </div>
              <!-- Camera name overlay -->
              <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2">
                <div class="flex items-center gap-2">
                  <span class="badge badge-neutral text-[10px] px-1.5 py-0.5 flex items-center gap-1">
                    <CirclePause size={10} />
                    {t('live.notSupported')}
                  </span>
                  <span class="text-white text-sm font-medium truncate">{camera.name || camera.id}</span>
                </div>
              </div>
            {/if}

            <!-- Streaming protocol badge -->
            {#if mode !== 'unsupported'}
              {@const protocolLabel = mode === 'wasm' ? 'WebCodecs' : mode === 'webrtc' ? 'WebRTC' : mode === 'flv' ? 'FLV' : mode === 'hls' ? (defaultProtocol === 'll-hls' ? 'LL-HLS' : 'HLS') : mode === 'mjpeg' ? 'MJPEG' : 'JPEG'}
              {@const protocolColor = mode === 'wasm' ? 'bg-cyan-500/60' : mode === 'webrtc' ? 'bg-green-500/60' : mode === 'flv' ? 'bg-orange-500/60' : mode === 'hls' ? (defaultProtocol === 'll-hls' ? 'bg-purple-500/60' : 'bg-blue-500/60') : mode === 'mjpeg' ? 'bg-amber-500/60' : 'bg-gray-500/60'}
              <span class="absolute top-2 right-2 z-10 {protocolColor} text-white text-[10px] font-medium px-2 py-0.5 rounded-full pointer-events-none select-none">
                {protocolLabel}
              </span>
            {/if}

            <!-- Health indicator dot + score -->
            {#if healthScores[camera.id] !== undefined}
              {@const hs = healthScores[camera.id]}
              {@const healthColor = hs >= 80 ? 'var(--color-success)' : hs >= 30 ? 'var(--color-warning)' : 'var(--color-danger)'}
              <span
                class="absolute top-2 left-2 z-10 flex items-center gap-1 bg-black/60 text-white text-[10px] font-medium px-1.5 py-0.5 rounded-full select-none"
                title={t('dashboard.healthScore', { score: hs })}
              >
                <span class="w-2 h-2 rounded-full flex-shrink-0" style="background-color: {healthColor}"></span>
                {hs}
              </span>
            {/if}

            <!-- PTZ Overlay for PTZ-capable cameras -->
            {#if ptzOpenIndex === index && getProtocolCapabilities(camera.protocol, protocolsMap).ptz}
              <div
                class="absolute top-2 left-2 z-10"
                onclick={(e: MouseEvent) => { e.stopPropagation(); }}
              >
                <div class="relative">
                  <button
                    class="absolute -top-1.5 -right-1.5 z-20 p-0.5 rounded-full bg-black/70 text-white/80 hover:text-white hover:bg-black/90 transition-all"
                    onclick={(e: MouseEvent) => { e.stopPropagation(); closePtz(); }}
                    aria-label={t('common.close')}
                  >
                    <X size={12} />
                  </button>
                  <PtzControl cameraId={camera.id} enabled={true} />
                </div>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </main>
</div>

<style>
  /* Grid cell expand/shrink transitions */
  .camera-grid-cell {
    transition: opacity var(--duration-normal) var(--ease-out),
                transform var(--duration-normal) var(--ease-out);
  }

  /* Subtle hover lift on grid cells */
  .camera-grid-cell:not(.hidden):hover {
    opacity: 0.92;
  }

  /* Fade-in + scale-up when a cell expands */
  .cell-expanded {
    animation: cell-expand var(--duration-normal) var(--ease-out);
  }

  @keyframes cell-expand {
    from {
      opacity: 0.3;
      transform: scale(0.96);
    }
    to {
      opacity: 1;
      transform: scale(1);
    }
  }

</style>
