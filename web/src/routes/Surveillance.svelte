<script lang="ts">
  import { onMount, setContext } from 'svelte';
  import { getDashboardCameras, getAuthHeader, listProtocols, getCameraProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, normalizeProtocol, getProtocolCapabilities, getHealthCameras, listCameraGroups, getStreamingSettings, API_BASE } from '$lib/api';
  import type { Camera, ProtocolInfo, CameraProtocolsResponse } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast.svelte';
  import { Loader2, AlertCircle, Video, VideoOff, X, Settings, ImageOff, CircleCheck, CirclePause, Search, Plus } from 'lucide-svelte';
  import PtzControl from '$lib/components/PtzControl.svelte';
  import CameraPlayer from '$lib/components/CameraPlayer.svelte';
  import { formatDate } from '$lib/format';
  import { createSnapshotManager } from '$lib/snapshot';
  import { createPlayerOrchestrator, makeRegistration, type PlayerOrchestrator } from '$lib/player/orchestrator.svelte';
  import { probeCaps } from '$lib/player/capabilities-cache';
  import { isAudioCapable, type ProtocolsResponse } from '$lib/stream-selection';
  import { getCameraProtocolOverride, getMaxGridCameras, setMaxGridCameras, isPhotoWallEnabled, setPhotoWallEnabled, getWallRotationSecs, setWallRotationSecs, WALL_ROTATION_OPTIONS, MAX_GRID_CAMERAS_OPTIONS } from '$lib/preferences';
  import { getGridQuality, toggleGridQuality } from '$lib/stream-quality.svelte';

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let expandedCameraId = $state<string | null>(null);

  // Drag-and-drop reorder state. We use a handle-initiated drag so it
  // doesn't collide with click/dblclick-to-expand. draggedIndex marks the
  // cell being dragged; dragOverIndex marks the cell under the cursor (for
  // the drop-target highlight). Both null when no drag is in progress.
  let draggedIndex = $state<number | null>(null);
  let dragOverIndex = $state<number | null>(null);

  // Page Visibility — pause/resume all players when tab hidden/visible
  let tabVisible = $state(true);

  let ptzOpenIndex = $state(-1);

  let allCameras = $state<Camera[]>([]);
  let configOpen = $state(false);
  let selectedCameraIds = $state<string[]>([]);
  let pendingCameraIds = $state<string[]>([]);

  // --- Grid wall capacity (#1072) ---
  // The preview count is an explicit opt-in above the calibrated default of 4
  // (RPi-class reference machine). The server's HLS preview budget further
  // clamps what this browser may pick: tiles on the HLS degrade path — plus
  // LiveView, expand and mobile clients — share that global budget, and
  // overbooking it triggers LRU eviction churn.
  let maxGridCameras = $state(getMaxGridCameras());
  let photoWall = $state(isPhotoWallEnabled());
  let serverBudget = $state(4);

  // Featured-tile rotation (#1072 P1): default off; hovering the featured
  // tile pauses it, and it never runs while the picker or expand view is up.
  let rotationSecs = $state(getWallRotationSecs());
  let rotationPaused = $state(false);

  function changeWallRotation(secs: number) {
    rotationSecs = secs;
    setWallRotationSecs(secs);
  }

  // Advance the carousel: the featured camera moves to the back, the next
  // one takes the 2×2 slot. Deliberately NOT persisted — rotation is a
  // view-layer cycle; the saved selection order stays what the user picked
  // (and what drag-and-drop reorders persist).
  function rotateFeatured() {
    if (cameras.length <= 1) return;
    cameras = [...cameras.slice(1), cameras[0]];
  }

  const gridCapacityOptions = $derived(
    MAX_GRID_CAMERAS_OPTIONS.filter((o) => o <= Math.max(serverBudget, 4)),
  );
  const effectiveMax = $derived(
    Math.min(maxGridCameras, gridCapacityOptions[gridCapacityOptions.length - 1] ?? 4),
  );
  // The photo-wall layout only exists above 4 tiles — at or below that the
  // classic grid (including the 1-big-2-small 3-tile form) is the better view.
  const photoWallActive = $derived(photoWall && cameras.length > 4 && !expandedCameraId);

  $effect(() => {
    if (rotationSecs <= 0 || !photoWallActive || rotationPaused || !tabVisible || expandedCameraId || configOpen) return;
    const timer = setInterval(rotateFeatured, rotationSecs * 1000);
    return () => clearInterval(timer);
  });

  function changeGridCapacity(n: number) {
    maxGridCameras = n;
    setMaxGridCameras(n);
    if (n <= 4) {
      photoWall = false;
      setPhotoWallEnabled(false);
    }
    if (pendingCameraIds.length > n) pendingCameraIds = pendingCameraIds.slice(0, n);
  }

  function togglePhotoWall() {
    photoWall = !photoWall;
    setPhotoWallEnabled(photoWall);
  }

  // Whenever the effective cap shrinks (budget fetched late, capacity lowered,
  // or a saved selection larger than the cap), trim the applied grid from the
  // tail and persist. Dropped cameras release their orchestrator registration.
  $effect(() => {
    if (cameras.length > effectiveMax) {
      const dropped = cameras.slice(effectiveMax);
      cameras = cameras.slice(0, effectiveMax);
      selectedCameraIds = cameras.map((c) => c.id);
      if (pendingCameraIds.length > effectiveMax) pendingCameraIds = pendingCameraIds.slice(0, effectiveMax);
      saveCameraIds(selectedCameraIds);
      for (const c of dropped) orchestrator.unregisterCamera(c.id);
    }
  });

  // Snapshot state
  let snapshotUrls = $state<Record<string, string>>({});
  let snapshotLoading = $state<Record<string, boolean>>({});
  let snapshotTransientErrors = $state<Record<string, boolean>>({});
  let healthScores = $state<Record<string, number>>({});

  // Snapshot manager — handles fetch, interval, and cleanup lifecycle
  const snapshotMgr = createSnapshotManager({
    intervalMs: 3000,
    getAuthHeader,
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

  // Per-camera protocol responses from GET /api/cameras/{id}/protocols.
  // Keyed by camera id. Fetched in parallel on mount and whenever the grid
  // selection changes; null means "not yet fetched or fetch failed" (the
  // orchestrator's buildCandidateChain then falls back to the universal HLS
  // candidate). This is what lets the grid auto-select the best protocol PER
  // CAMERA instead of applying one global protocol to a mixed fleet.
  let cameraProtocols = $state<Map<string, CameraProtocolsResponse | null>>(new Map());

  // Player orchestrator — owns per-camera protocol chains, adaptive degrade/
  // upgrade decisions, and the reconnect coordinator (thundering-herd control).
  // Provided to CameraPlayer (and thus every player component) via context.
  // WasmPlayer is now lazy-loaded INSIDE CameraPlayer on first wasm mount, so
  // this route does not manage the chunk import.
  const orchestrator: PlayerOrchestrator = createPlayerOrchestrator();
  setContext('player-orchestrator', orchestrator);

  // Surface orchestrator mode changes (degrade/upgrade) as toasts so the user
  // understands why a camera's protocol just changed.
  const _orchUnsub = orchestrator.onModeChange((cameraId, _from, to, reason) => {
    const cam = allCameras.find((c) => c.id === cameraId);
    const name = cam?.name || cameraId;
    if (reason === 'upgrade-reverted') {
      showToast(t('surveillance.upgradeReverted', { camera: name, protocol: protocolLabel(to) }), 'warning');
    } else if (reason.startsWith('upgrade')) {
      showToast(t('surveillance.upgraded', { camera: name, protocol: protocolLabel(to) }), 'success');
    } else {
      showToast(t('surveillance.protocolFallback', { protocol: protocolLabel(to) }), 'info');
    }
  });

  function protocolLabel(mode: string): string {
    switch (mode) {
      case 'wasm': return 'WebCodecs';
      case 'webrtc': return 'WebRTC';
      case 'flv': return 'FLV';
      case 'hls': return 'HLS';
      case 'mjpeg': return 'MJPEG';
      default: return mode;
    }
  }

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

  // Camera picker (config panel) state — search + online-only filter +
  // grouping. Sections mirror the management page's grouping: registry order,
  // derived-only labels appended (zh collation), ungrouped last. With many
  // cameras, scanning a flat checkbox list stops scaling; the filter + one
  // click per group brings picking 4 tiles back to seconds.
  let configSearch = $state('');
  let onlineOnly = $state(true);
  let registryGroups = $state<string[]>([]);

  function isCameraOnline(c: Camera): boolean {
    const st = (c.status || '').toLowerCase();
    return st === 'recording' || st === 'active';
  }

  const pickerSections = $derived.by(() => {
    const q = configSearch.trim().toLowerCase();
    const filtered = allCameras.filter(c => {
      if (q) {
        const name = (c.name || '').toLowerCase();
        const group = (c.group || '').trim().toLowerCase();
        if (!name.includes(q) && !group.includes(q)) return false;
      }
      // Online-only hides OFFLINE cameras, but a selected one stays visible
      // (so the user can always see and uncheck what's already in the grid).
      if (onlineOnly && !isCameraOnline(c) && !pendingCameraIds.includes(c.id)) return false;
      return true;
    });
    const byGroup = new Map<string, Camera[]>();
    for (const c of filtered) {
      const g = (c.group || '').trim();
      if (!byGroup.has(g)) byGroup.set(g, []);
      byGroup.get(g)!.push(c);
    }
    const inRegistry = new Set(registryGroups);
    const derived = [...byGroup.keys()]
      .filter(g => g !== '' && !inRegistry.has(g))
      .sort((a, b) => a.localeCompare(b, 'zh'));
    const sections: { name: string; cameras: Camera[] }[] = [];
    for (const n of registryGroups) {
      const cams = byGroup.get(n);
      if (cams && cams.length > 0) sections.push({ name: n, cameras: cams });
    }
    for (const n of derived) sections.push({ name: n, cameras: byGroup.get(n)! });
    const ungrouped = byGroup.get('');
    if (ungrouped && ungrouped.length > 0) sections.push({ name: '', cameras: ungrouped });
    return sections;
  });

  function addGroupToSelection(name: string): void {
    const sec = pickerSections.find(s => s.name === name);
    if (!sec) return;
    for (const c of sec.cameras) {
      if (pendingCameraIds.length >= effectiveMax) break;
      if (!pendingCameraIds.includes(c.id)) pendingCameraIds = [...pendingCameraIds, c.id];
    }
  }

  function toggleCameraSelection(cameraId: string) {
    if (pendingCameraIds.includes(cameraId)) {
      pendingCameraIds = pendingCameraIds.filter(id => id !== cameraId);
    } else if (pendingCameraIds.length < effectiveMax) {
      pendingCameraIds = [...pendingCameraIds, cameraId];
    }
  }

  function clearPendingSelection(): void {
    pendingCameraIds = [];
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
    // Unregister cameras that left the grid (clears their orchestrator timers/
    // cooldowns) and register the new set fresh. A camera demoted in a prior
    // session starts at the head of its chain again.
    const newIds = new Set(filtered.map((c) => c.id));
    for (const id of [...selectedCameraIds, ...pendingCameraIds]) {
      if (!newIds.has(id)) orchestrator.unregisterCamera(id);
    }
    syncOrchestrator();
    // Fetch per-camera protocol rankings for the newly selected cameras. This
    // is what drives auto-selection; runs in parallel and never blocks render.
    // (refreshCameraProtocols calls syncOrchestrator again once responses land.)
    void refreshCameraProtocols(filtered.map((c) => c.id));
  }

  function getStreamUrl(cameraId: string): string {
    return `${API_BASE}/cameras/${cameraId}/stream/index.m3u8`;
  }

  // Uniform column tiers (#1072): ≤4 keeps the classic two-column grid; above
  // that 3 columns up to 9 tiles and 4 columns beyond (lg+ only — narrow
  // screens stay single-column).
  function getGridClass(count: number): string {
    if (count <= 1) return 'grid-cols-1';
    if (count <= 4) return 'grid-cols-1 sm:grid-cols-2';
    if (count <= 9) return 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-3';
    return 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-4';
  }

  // Photo-wall layout (#1072): one featured 2×2 tile + dense-packed 1×1
  // tiles. Column tiers picked so the common counts tile exactly (6 → 3×3,
  // 9 → 4×3, 16 → 5×4); odd counts leave a tail hole that dense flow
  // minimizes.
  function getWallGridClass(count: number): string {
    const cols = count <= 6 ? 'sm:grid-cols-3' : count <= 9 ? 'lg:grid-cols-4' : 'lg:grid-cols-4 xl:grid-cols-5';
    return `grid-cols-2 ${cols} [grid-auto-flow:dense]`;
  }

  function getCellClass(camera: Camera, index: number, count: number): string {
    if (expandedCameraId) {
      return camera.id === expandedCameraId
        ? 'col-span-2 row-span-2'
        : 'hidden';
    }
    if (photoWallActive && index === 0) return 'col-span-2 row-span-2';
    if (count === 3 && index === 0) {
      return 'col-span-2';
    }
    return '';
  }

  // Cell height floor by layout row count: the classic grid holds two rows;
  // taller walls divide the viewport by their row count so tiles stay legible
  // and the page scrolls instead of shrinking cells text-thin.
  function getCellMinHeight(count: number): string {
    if (count <= 1) return 'calc(100vh - 140px)';
    if (photoWallActive) {
      const cols = count <= 6 ? 3 : count <= 9 ? 4 : 5;
      const rows = Math.ceil((count + 3) / cols); // featured 2×2 adds 3 cells
      return `calc((100vh - 160px) / ${rows})`;
    }
    if (count <= 4) return 'calc((100vh - 160px) / 2)';
    if (count <= 9) return 'calc((100vh - 160px) / 3)';
    return 'calc((100vh - 160px) / 4)';
  }

  // HD clamp (#1072): above 4 tiles the HD setting rides only the featured
  // (first) tile; every other tile stays on the sub stream. A fleet-wide main
  // switch at 9-16 tiles would multiply decoder cost past weak clients, and
  // the small 1×1 cells hide the resolution difference anyway. Cameras without
  // a sub stream stay on main automatically (effectiveQuality in CameraPlayer).
  function tileQuality(index: number): 'main' | 'sub' {
    const gq = getGridQuality();
    if (gq !== 'main' || cameras.length <= 4) return gq;
    return index === 0 ? 'main' : 'sub';
  }

  function getStatusBadge(camera: Camera): { class: string; label: string; icon: any; text: string } {
    const status = camera.status?.toLowerCase() || '';
    if (status === 'recording' || status === 'active') {
      // Live-only cameras connect and stream for preview/relay but write no
      // segments — show "Live only" instead of "Recording" so the grid matches
      // CameraCard and users can tell preview-only cameras at a glance.
      if (camera.recording_enabled === false) {
        return { class: 'badge-info', label: '●', icon: CircleCheck, text: t('cameras.statusLive') };
      }
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

  // Register (or refresh) every selected camera with the orchestrator. Called
  // after caps are probed and after each /protocols fetch resolves. The
  // orchestrator rebuilds each camera's candidate chain and owns degrade/
  // upgrade from here on.
  function syncOrchestrator(): void {
    for (const cam of cameras) {
      const resp = (cameraProtocols.get(cam.id) ?? null) as ProtocolsResponse | null;
      orchestrator.registerCamera(
        makeRegistration(cam, resp, {
          override: getCameraProtocolOverride(cam.id),
          isHlsCapable: isHlsSupported(cam),
          isUnsupported: snapshotMgr.isUnsupported(cam.id),
        }),
      );
    }
  }

  // The per-camera mode for the grid dispatcher. Real-time modes come from the
  // orchestrator; when it reports none (empty chain), we fall to snapshot or
  // unsupported — exactly the legacy behavior, now driven by the orchestrator.
  function getCameraMode(camera: Camera): 'wasm' | 'webrtc' | 'flv' | 'hls' | 'mjpeg' | 'snapshot' | 'unsupported' {
    const mode = orchestrator.activeMode(camera.id);
    if (mode) return mode;
    return snapshotMgr.isUnsupported(camera.id) ? 'unsupported' : 'snapshot';
  }

  // Fetch per-camera protocol rankings for the given camera ids, in parallel,
  // and cache them. Best-effort: failures store null so the picker falls back
  // to the legacy global default rather than blocking the grid.
  async function refreshCameraProtocols(ids: string[]): Promise<void> {
    if (ids.length === 0) return;
    const results = await Promise.allSettled(ids.map((id) => getCameraProtocols(id)));
    const next = new Map(cameraProtocols);
    // Track which cameras returned an EMPTY protocol list — these are the
    // "device temporarily unreachable / recorder not started" cases that need a
    // backoff re-fetch (issue #112). Without re-fetch, the orchestrator would
    // keep the camera on snapshot forever even after the device recovers.
    const emptyIds: string[] = [];
    for (let i = 0; i < ids.length; i++) {
      const id = ids[i];
      const r = results[i];
      const value = r.status === 'fulfilled' ? r.value : null;
      next.set(id, value);
      // Only an explicitly-empty response (resp non-null but protocols empty)
      // triggers re-fetch. A null resp (fetch threw) is handled by the
      // orchestrator's !resp legacy-default path and re-fetches naturally on
      // the next camera-list poll.
      if (value && value.protocols.length === 0) {
        emptyIds.push(id);
      }
    }
    cameraProtocols = next;
    // Rebuild chains now that we have fresh backend rankings.
    syncOrchestrator();
    // Arm backoff re-fetch for cameras whose backend reported nothing available.
    // They render as snapshot in the meantime (no real-time player, no storm).
    for (const id of emptyIds) {
      scheduleEmptyProtocolsRecheck(id);
    }
  }

  // ─── Empty-protocols backoff re-fetch (issue #112) ─────────────────────────
  // When /protocols returns an empty list (device down / recorder starting),
  // re-fetch with exponential backoff so the camera recovers automatically once
  // the backend can serve it again. Per-camera timers; cleared on unmount or
  // when a non-empty response arrives. Mirrors the auto-retry cadence used by
  // the HLS error handler (5s/10s/20s/40s, max 4 attempts).
  const PROTOCOLS_RECHECK_DELAYS = [5000, 10000, 20000, 40000];
  let protocolsRecheckTimers: Record<string, ReturnType<typeof setTimeout>> = {};

  function scheduleEmptyProtocolsRecheck(cameraId: string, attempt = 0): void {
    // Clear any existing timer for this camera (idempotent re-arm).
    const existing = protocolsRecheckTimers[cameraId];
    if (existing) clearTimeout(existing);
    if (attempt >= PROTOCOLS_RECHECK_DELAYS.length) return; // exhausted
    const delay = PROTOCOLS_RECHECK_DELAYS[attempt];
    protocolsRecheckTimers[cameraId] = setTimeout(async () => {
      delete protocolsRecheckTimers[cameraId];
      try {
        const resp = await getCameraProtocols(cameraId);
        cameraProtocols = new Map(cameraProtocols).set(cameraId, resp);
        syncOrchestrator();
        // If still empty, schedule the next backoff tick. A non-empty response
        // means recovery — stop re-fetching for this camera.
        if (resp && resp.protocols.length === 0) {
          scheduleEmptyProtocolsRecheck(cameraId, attempt + 1);
        }
      } catch {
        // Fetch failed — treat like an empty response and keep backing off.
        cameraProtocols = new Map(cameraProtocols).set(cameraId, null);
        syncOrchestrator();
        scheduleEmptyProtocolsRecheck(cameraId, attempt + 1);
      }
    }, delay);
  }

  function clearProtocolsRechecks(): void {
    for (const id of Object.keys(protocolsRecheckTimers)) {
      clearTimeout(protocolsRecheckTimers[id]);
    }
    protocolsRecheckTimers = {};
  }

  // (WasmPlayer preloading is handled by CameraPlayer, which lazy-imports the
  // WebCodecs/AI chunk on first wasm mount. No preload effect needed here.)

  // --- Expand / shrink ---

  function expandToHls(cameraId: string) {
    expandedCameraId = cameraId;
  }

  function shrinkToGrid() {
    expandedCameraId = null;
  }

  // --- Drag-and-drop reorder ---

  // Swapping two cells = swapping their position in the `cameras` array
  // (render order) AND the `selectedCameraIds` array (persisted order).
  // Camera identity is camera.id, so no player/orchestrator state is touched.
  function reorderCameras(from: number, to: number) {
    if (from === to || from < 0 || to < 0 || from >= cameras.length || to >= cameras.length) return;
    const next = [...cameras];
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    cameras = next;
    // Keep selectedCameraIds in the same new order so persistence reflects it.
    selectedCameraIds = next.map((c) => c.id);
    saveCameraIds(selectedCameraIds);
  }

  function handleDragStart(index: number, e: DragEvent) {
    draggedIndex = index;
    // Required by some browsers for the drag to actually start / carry data.
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      // Firefox/Chrome need data set, or the drag is cancelled.
      e.dataTransfer.setData('text/plain', String(index));
    }
  }

  function handleDragOver(index: number, e: DragEvent) {
    if (draggedIndex === null) return;
    e.preventDefault(); // allow drop
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    if (dragOverIndex !== index) dragOverIndex = index;
  }

  function handleDrop(index: number, e: DragEvent) {
    e.preventDefault();
    if (draggedIndex === null || draggedIndex === index) {
      draggedIndex = null;
      dragOverIndex = null;
      return;
    }
    reorderCameras(draggedIndex, index);
    draggedIndex = null;
    dragOverIndex = null;
  }

  function handleDragEnd() {
    draggedIndex = null;
    dragOverIndex = null;
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
    // Server HLS preview budget clamps the wall options (#1072). Non-blocking:
    // until it lands the options stay at the safe default of 4; the clamp
    // effect above trims an oversized saved selection if the budget is lower.
    void (async () => {
      try {
        const s = await getStreamingSettings();
        serverBudget = s.hls?.max_streams ?? 4;
      } catch {
        /* older binary or transient failure — keep the safe default */
      }
    })();
    try {
      const fetched = await getDashboardCameras();
      // Audio-only devices (v43, encoding "audio") have no video stream — they
      // cannot render in a video grid and would occupy a slot with a permanent
      // error tile. Exclude them from the grid and its picker.
      const activeFetched = fetched.filter((c) => c.encoding !== 'audio');
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
      // Probe device caps once (cached in sessionStorage) so the orchestrator
      // can build latency-optimal chains (WebCodecs/wasm lead when available).
      // This MUST happen before syncOrchestrator() reads the caps.
      await probeCaps();
      // Register the initial cameras with the orchestrator so the grid has a
      // real-time mode (or snapshot fallback) to render immediately, before the
      // per-camera /protocols responses arrive.
      syncOrchestrator();
      // Fetch per-camera protocol rankings so the grid can auto-select the best
      // protocol per camera. Non-blocking: cameras render immediately and
      // re-resolve once the responses arrive (refreshCameraProtocols calls
      // syncOrchestrator again with the fresh rankings).
      void refreshCameraProtocols(cameras.map((c) => c.id));
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
    // Camera groups for the picker's grouped sections (best-effort).
    try {
      registryGroups = await listCameraGroups();
    } catch (e) { console.warn('Failed to load camera groups:', e); }
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
    // Load protocol capabilities — needed for isHlsSupported(); re-sync the
    // orchestrator once known so non-HLS-capable cameras get an empty chain.
    try {
      const list = await listProtocols();
      if (list && list.length > 0) {
        protocolsMap = buildProtocolsMap(list);
        syncOrchestrator();
      }
    } catch (e) {
      console.warn('Failed to load protocol capabilities:', e);
    }
    document.addEventListener('fullscreenchange', handleFullscreenChange);

    // Page Visibility API: tell the orchestrator when the tab hides/shows so it
    // can pause players (release WS/RTCPeerConnection) and attempt upgrades on
    // return. The orchestrator owns visibility now — NOT the per-player effects
    // (those caused the WS reconnect storm).
    const visibilityHandler = () => {
      tabVisible = !document.hidden;
      orchestrator.setTabVisible(tabVisible);
    };
    document.addEventListener('visibilitychange', visibilityHandler);

    // Intercept fetch to detect backend pressure (HTTP 503 → global cooldown).
    // The orchestrator owns the reconnect coordinator, so forward 503s to it.
    const originalFetch = window.fetch;
    window.fetch = async function (...args: Parameters<typeof fetch>): Promise<Response> {
      const response = await originalFetch.apply(this, args);
      if (response.status === 503) {
        orchestrator.coordinator.reportBackendPressure();
      }
      return response;
    };


    return () => {
      document.removeEventListener('fullscreenchange', handleFullscreenChange);
      document.removeEventListener('visibilitychange', visibilityHandler);
      window.fetch = originalFetch;
      clearProtocolsRechecks();
      _orchUnsub();
      orchestrator.dispose();
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
      <div class="flex items-center gap-1">
        <!-- Grid preview quality (#513): sub tiles save decode/bandwidth;
             cameras without a sub stream silently stay on main. -->
        <button
          class="btn btn-ghost p-2 text-xs font-medium"
          onclick={toggleGridQuality}
          title={t('quality.tooltip')}
        >
          {#if getGridQuality() === 'sub'}
            {t('quality.sub')}
          {:else}
            {t('quality.main')}
          {/if}
        </button>
        <button
          class="btn btn-ghost p-2"
          onclick={() => { configOpen = !configOpen; pendingCameraIds = [...selectedCameraIds]; }}
          title={t('dashboard.configure')}
        >
          <Settings size={18} />
        </button>
      </div>
    </div>

    <!-- Camera configuration panel -->
    {#if configOpen}
      <div class="card p-4 mb-4">
        <div class="flex items-center justify-between mb-3">
          <h3 class="text-sm font-semibold th-text-primary">{t('dashboard.selectCameras')}</h3>
          <span class="text-xs th-text-muted">{pendingCameraIds.length} / {effectiveMax} · {t('dashboard.maxCameras')}</span>
        </div>

        <!-- Toolbar: search + online-only filter + clear -->
        <div class="flex flex-wrap items-center gap-2 mb-3">
          <div class="relative flex-1 min-w-[180px]">
            <Search size={14} class="absolute left-2.5 top-1/2 -translate-y-1/2 th-text-muted pointer-events-none" />
            <input
              type="text"
              class="input pl-8 py-1.5 text-sm"
              placeholder={t('dashboard.searchCameras')}
              bind:value={configSearch}
            />
          </div>
          <label class="flex items-center gap-1.5 text-xs th-text-secondary cursor-pointer whitespace-nowrap select-none">
            <input type="checkbox" bind:checked={onlineOnly} class="accent-[var(--color-primary)]" />
            {t('dashboard.onlineOnly')}
          </label>
          <!-- Wall capacity + photo-wall toggle (#1072): capacity is clamped by
               the server's HLS preview budget; the photo wall only exists
               above 4 tiles. -->
          <label class="flex items-center gap-1.5 text-xs th-text-secondary whitespace-nowrap select-none">
            {t('dashboard.gridCapacity')}
            <select
              class="input py-1 px-1.5 text-xs"
              value={maxGridCameras}
              onchange={(e) => changeGridCapacity(parseInt((e.target as HTMLSelectElement).value))}
            >
              {#each MAX_GRID_CAMERAS_OPTIONS as n (n)}
                <option value={n} disabled={n > Math.max(serverBudget, 4)}>{n}</option>
              {/each}
            </select>
          </label>
          <label
            class="flex items-center gap-1.5 text-xs th-text-secondary cursor-pointer whitespace-nowrap select-none {maxGridCameras > 4 ? '' : 'opacity-50 pointer-events-none'}"
            title={t('dashboard.photoWallHint')}
          >
            <input type="checkbox" checked={photoWall} onchange={togglePhotoWall} disabled={maxGridCameras <= 4} class="accent-[var(--color-primary)]" />
            {t('dashboard.photoWall')}
          </label>
          <label
            class="flex items-center gap-1.5 text-xs th-text-secondary whitespace-nowrap select-none {photoWall && maxGridCameras > 4 ? '' : 'opacity-50 pointer-events-none'}"
            title={t('dashboard.wallRotationHint')}
          >
            {t('dashboard.wallRotation')}
            <select
              class="input py-1 px-1.5 text-xs"
              value={rotationSecs}
              onchange={(e) => changeWallRotation(parseInt((e.target as HTMLSelectElement).value))}
              disabled={!(photoWall && maxGridCameras > 4)}
            >
              <option value={0}>{t('dashboard.wallRotation.off')}</option>
              <option value={30}>30s</option>
              <option value={60}>60s</option>
              <option value={300}>5min</option>
            </select>
          </label>
          {#if gridCapacityOptions.length < MAX_GRID_CAMERAS_OPTIONS.length}
            <p class="w-full text-[11px] th-text-muted">{t('dashboard.budgetHint', { budget: serverBudget })}</p>
          {/if}
          <button
            class="btn btn-ghost text-xs px-2 py-1"
            onclick={clearPendingSelection}
            disabled={pendingCameraIds.length === 0}
          >
            {t('dashboard.clearSelection')}
          </button>
        </div>

        <!-- Selected chips (ordered — grid position order) -->
        {#if pendingCameraIds.length > 0}
          <div class="flex flex-wrap gap-1.5 mb-3">
            {#each pendingCameraIds as id, i (id)}
              {@const chipCam = allCameras.find(c => c.id === id)}
              <span class="inline-flex items-center gap-1 pl-2 pr-1 py-0.5 rounded-full bg-[var(--color-primary)] text-white text-xs">
                <span class="opacity-70">{i + 1}.</span>
                {chipCam?.name || id}
                <button
                  type="button"
                  class="p-0.5 rounded-full hover:bg-white/20 transition-colors"
                  onclick={() => toggleCameraSelection(id)}
                  aria-label={t('dashboard.removeCamera')}
                  title={t('dashboard.removeCamera')}
                >
                  <X size={12} />
                </button>
              </span>
            {/each}
          </div>
        {/if}

        <!-- Grouped, filtered camera list -->
        <div class="space-y-0.5 max-h-72 overflow-y-auto mb-4">
          {#each pickerSections as sec (sec.name)}
            <div class="pt-1.5">
              <div class="flex items-center gap-2 px-2 py-1 sticky top-0 bg-[var(--bg-elevated)] z-[1]">
                <span class="text-[11px] font-semibold th-text-secondary tracking-wide">{sec.name || t('cameras.groupUngrouped')}</span>
                <span class="text-[11px] th-text-muted">{sec.cameras.length}</span>
                <div class="flex-1 border-t th-border opacity-50"></div>
                {#if pendingCameraIds.length < effectiveMax}
                  <button
                    type="button"
                    class="inline-flex items-center gap-0.5 text-[11px] hover:underline"
                    style="color: var(--color-primary);"
                    onclick={() => addGroupToSelection(sec.name)}
                    title={t('dashboard.addGroupHint')}
                  >
                    <Plus size={11} />
                    {t('dashboard.addGroup')}
                  </button>
                {/if}
              </div>
              {#each sec.cameras as camera (camera.id)}
                {@const online = isCameraOnline(camera)}
                <label class="flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-[var(--bg-tertiary)] cursor-pointer transition-colors">
                  <input
                    type="checkbox"
                    checked={pendingCameraIds.includes(camera.id)}
                    onchange={() => toggleCameraSelection(camera.id)}
                    disabled={!pendingCameraIds.includes(camera.id) && pendingCameraIds.length >= effectiveMax}
                    class="accent-[var(--color-primary)]"
                  />
                  <span class="w-1.5 h-1.5 rounded-full shrink-0 {online ? 'bg-emerald-500' : 'bg-gray-400'}"
                    title={online ? t('cameras.statusRecording') : t('cameras.statusStopped')}></span>
                  <span class="text-sm th-text-primary truncate">{camera.name || camera.id}</span>
                  <span class="text-xs th-text-muted ml-auto shrink-0">{camera.protocol}</span>
                </label>
              {/each}
            </div>
          {:else}
            <p class="text-xs th-text-muted px-2 py-6 text-center">{t('dashboard.noMatch')}</p>
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
        class="grid gap-2 sm:gap-3 {photoWallActive ? getWallGridClass(cameras.length) : getGridClass(cameras.length)}"
        onexpand={(e: CustomEvent) => expandToHls(e.detail.cameraId)}
        onshrink={(e: CustomEvent) => shrinkToGrid()}
      >
        {#each cameras as camera, index (camera.id)}
{@const status = getStatusBadge(camera)}
          {@const mode = getCameraMode(camera)}
          {@const StatusIcon = status.icon}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <!-- svelte-ignore a11y_mouse_events_have_key_events -->
          <div
            class="relative bg-black rounded-lg overflow-hidden group camera-grid-cell {getCellClass(camera, index, cameras.length)} {dragOverIndex === index && draggedIndex !== null && draggedIndex !== index ? 'cell-drop-target' : ''} {draggedIndex === index ? 'cell-dragging' : ''}"
            class:cell-expanded={expandedCameraId === camera.id}
            style="min-height: {getCellMinHeight(cameras.length)};"
            role="button"
            tabindex="0"
            aria-label="{camera.name || camera.id} — {status.text}"
            onclick={() => handleCellClick(camera, index)}
            onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleCellClick(camera, index); } }}
            ondblclick={() => handleCellDblClick(camera)}
            onmouseenter={() => { if (photoWallActive && index === 0) rotationPaused = true; }}
            onmouseleave={() => { if (rotationPaused) rotationPaused = false; }}
            draggable={cameras.length > 1 && !expandedCameraId ? 'true' : undefined}
            ondragstart={(e: DragEvent) => handleDragStart(index, e)}
            ondragover={(e: DragEvent) => handleDragOver(index, e)}
            ondrop={(e: DragEvent) => handleDrop(index, e)}
            ondragend={handleDragEnd}
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

            {:else if mode === 'unsupported'}
              <!-- Unsupported protocol (no snapshot, no real-time chain) -->
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
            {:else}
              <!-- Real-time mode (hls/webrtc/flv/wasm/mjpeg). CameraPlayer reads
                   the active mode from the orchestrator (context) and renders the
                   matching player, bridging its health back for adaptive degrade/
                   upgrade. The orchestrator owns all protocol-switching decisions. -->
              <CameraPlayer
                {camera}
                expanded={expandedCameraId === camera.id}
                {tabVisible}
                streamUrl={getStreamUrl(camera.id)}
                quality={tileQuality(index)}
              />
            {/if}

            <!-- Streaming protocol badge -->
            {#if mode !== 'unsupported' && mode !== 'snapshot'}
              {@const protocolLabel = mode === 'wasm' ? 'WebCodecs' : mode === 'webrtc' ? 'WebRTC' : mode === 'flv' ? 'FLV' : mode === 'hls' ? 'HLS' : mode === 'mjpeg' ? 'MJPEG' : 'JPEG'}
              {@const protocolColor = mode === 'wasm' ? 'bg-cyan-500/60' : mode === 'webrtc' ? 'bg-green-500/60' : mode === 'flv' ? 'bg-orange-500/60' : mode === 'hls' ? 'bg-blue-500/60' : mode === 'mjpeg' ? 'bg-amber-500/60' : 'bg-gray-500/60'}
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
                  <PtzControl cameraId={camera.id} enabled={true} protocol={camera.protocol} />
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

  /* Drop target highlight during drag — outline, not fill, so it stays visible
     over any video content. */
  .camera-grid-cell.cell-drop-target {
    outline: 2px dashed var(--color-primary, #8b5cf6);
    outline-offset: -4px;
  }

  /* The cell being dragged-from fades so the user sees it "lift". Driven by
     draggedIndex state, not a CSS attribute selector, so it tracks the active
     drag reliably even though the drag source is the handle, not the cell. */
  .camera-grid-cell.cell-dragging {
    opacity: 0.5;
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
