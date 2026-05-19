<script lang="ts">
  import { onMount } from 'svelte';
  import { getDashboardCameras, getCredentials, listProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, normalizeProtocol, getProtocolCapabilities } from '$lib/api';
  import type { Camera, ProtocolInfo } from '$lib/api';
  import { t } from '$lib/i18n';
  import { Loader2, AlertCircle, Video, VideoOff, X, Settings, ImageOff, CircleCheck, CirclePause, CircleAlert } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import { formatDate } from '$lib/format';
  import { createSnapshotManager } from '$lib/snapshot';

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let expandedCameraId = $state<string | null>(null);

  let ptzOpenIndex = $state(-1);

  let allCameras = $state<Camera[]>([]);
  let configOpen = $state(false);
  let selectedCameraIds = $state<string[]>([]);
  let pendingCameraIds = $state<string[]>([]);

  // Snapshot state
  let snapshotUrls = $state<Record<string, string>>({});
  let snapshotLoading = $state<Record<string, boolean>>({});
  let snapshotTransientErrors = $state<Record<string, boolean>>({});

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
    configOpen = false;
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
      return { class: 'badge-error', label: '●', icon: CircleAlert, text: t('cameras.statusError') };
    }
    return { class: 'badge-neutral', label: '●', icon: CirclePause, text: t('cameras.statusStopped') };
  }

  function isHlsSupported(camera: Camera): boolean {
    return getProtocolCapabilities(camera.protocol, protocolsMap).hls;
  }

  type CameraMode = 'snapshot' | 'hls' | 'unsupported';

  function getCameraMode(camera: Camera): CameraMode {
    if (isHlsSupported(camera)) return 'hls';
    if (snapshotMgr.isUnsupported(camera.id)) return 'unsupported';
    return 'snapshot';
  }

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
    if (isHlsSupported(camera)) {
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
    try {
      const fetched = await getDashboardCameras();
      allCameras = fetched;
      const savedIds = loadSavedCameraIds();
      if (savedIds.length > 0) {
        const available = new Map(fetched.map(c => [c.id, c]));
        const filtered = savedIds
          .map(id => available.get(id))
          .filter((c): c is Camera => c !== undefined);
        selectedCameraIds = filtered.map(c => c.id);
        cameras = filtered;
      } else {
        cameras = fetched.slice(0, 4);
        selectedCameraIds = cameras.map(c => c.id);
      }
      pendingCameraIds = [...selectedCameraIds];
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
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
    document.addEventListener('fullscreenchange', handleFullscreenChange);

    return () => {
      document.removeEventListener('fullscreenchange', handleFullscreenChange);
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
        {t('dashboard.title')}
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
                    <svelte:component this={status.icon} size={10} />
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
                expanded={expandedCameraId === camera.id}
              />

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
