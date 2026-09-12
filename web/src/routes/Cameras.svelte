<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { listCameras, deleteCamera, startCamera, stopCamera, updateCamera, xiaomiDevices, listProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, listArchives, setArchiveRetention, deleteArchiveGroup, listArchiveRecordings, deleteArchiveRecording, getArchiveCleanupStatus, getHealthStatus, getTranscodingStatus, getTranscodingSettings, getTranscodingCheck, getCameraRecordingStats, rediscoverCamera, activateCamera, getAuthHeader, ApiRequestError, API_BASE, listCameraGroups, createCameraGroup, renameCameraGroup, deleteCameraGroup, setCameraGroupsOrder } from '$lib/api';
  import type { Camera, XiaomiDevice, ProtocolInfo, ArchiveGroup, Recording, CameraHealth, HealthStatusResponse, ArchiveCleanupTask, ArchiveCleanupStatus } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { friendlyError } from '$lib/errors';
  import { formatFileSize, formatDate, formatDuration } from '$lib/format';
  import { AlertCircle, Camera as CameraIcon, Plus, Archive as ArchiveIcon, Trash2, ExternalLink, Clock, HardDrive, Play, Download, ChevronDown, ChevronRight, Video, Settings, Loader2, FolderPlus, Pencil } from 'lucide-svelte';
  import DiscoveryPanel from '$lib/components/DiscoveryPanel.svelte';
  import CameraForm from '$lib/components/CameraForm.svelte';
  import CameraCard from '$lib/components/CameraCard.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import ArchiveConfirmDialog from '$lib/components/ArchiveConfirmDialog.svelte';
  import OnboardingOverlay from '$lib/components/OnboardingOverlay.svelte';
  import Tab from '$lib/components/Tab.svelte';
  import Pagination from '../components/Pagination.svelte';
  import { startBackfill, getUntranscodedRecordingCount } from '$lib/api/transcoding';

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let activeTab = $state('active');
  let archives = $state<ArchiveGroup[]>([]);
  let archiveConfirm = $state<Camera | null>(null);
  let archiveLoading = $state(false);
  let archiveConfirmCount = $state<number>(0);
  let archiveConfirmSize = $state<number>(0);
  let archiveConfirmStatsLoading = $state(false);
  let confirmDeleteArchive = $state<string | null>(null);
  let deleteArchiveLoading = $state(false);
  // Archive expansion state
  let expandedArchiveId = $state<string | null>(null);
  let archiveRecordings = $state<Recording[]>([]);
  let archiveRecordingsTotal = $state(0);
  let archiveRecordingsOffset = $state(0);
  let archiveRecordingsLimit = $state(20);
  let archiveRecordingsLoading = $state(false);
  let deleteRecordingConfirm = $state<Recording | null>(null);
  let showRetDialog = $state(false);
  let selectedArchiveGroup = $state<ArchiveGroup | null>(null);
  let retentionDays = $state(30);
  let healthData = $state<Record<string, CameraHealth>>({});
  let cleanupTasks = $state<ArchiveCleanupTask[]>([]);
  let cleanupPolling = $state<number | null>(null);

  // Form state
  let showForm = $state(false);
  let editingCamera = $state<Camera | null>(null);

  // Confirmation dialog state
  let confirmAction = $state<{ camera: Camera; action: 'stop' | 'restart' } | null>(null);

  // Backfill dialog state
  let backfillInfo = $state<{ cameraId: string; count: number; targetCodec: string; resolve: (value: boolean) => void } | null>(null);
  let backfillLoading = $state(false);

  // Xiaomi
  let xiaomiDeviceList = $state<XiaomiDevice[]>([]);

  // Protocol info
  let protocols = $state<ProtocolInfo[]>(DEFAULT_PROTOCOLS);
  let protocolsMap = $state<Map<string, ProtocolInfo>>(buildProtocolsMap(DEFAULT_PROTOCOLS));

  // Global transcoding state
  let globalTranscodingEnabled = $state(false);
  let h265Available = $state(true);

  // Camera grouping (v37). Groups are plain labels on each camera (assigned
  // via drag & drop, the form, or a page-level create); an EMPTY group lives
  // in the server-side registry until a camera joins it. Collapse state
  // persists in localStorage so a reload keeps folded groups folded. With no
  // group at all, rendering is exactly the legacy flat grid (no headers).
  const GROUPS_COLLAPSED_KEY = 'mibee_nvr_camera_groups_collapsed';
  let collapsedGroups = $state<Set<string>>(loadCollapsedGroups());
  let registryGroups = $state<string[]>([]);
  // New-group dialog
  let showNewGroup = $state(false);
  let newGroupName = $state('');
  // Inline header rename
  let renamingGroup = $state<string | null>(null);
  let renameValue = $state('');
  // Delete-group confirmation
  let deleteGroupConfirm = $state<string | null>(null);
  // Drag & drop: which section a camera is currently dragged over ('' = ungrouped).
  let dragOverGroup = $state<string | null>(null);

  function loadCollapsedGroups(): Set<string> {
    try {
      const raw = localStorage.getItem(GROUPS_COLLAPSED_KEY);
      const arr = raw ? JSON.parse(raw) : [];
      return Array.isArray(arr) ? new Set(arr.filter(x => typeof x === 'string')) : new Set();
    } catch {
      return new Set();
    }
  }

  function persistCollapsedGroups(): void {
    localStorage.setItem(GROUPS_COLLAPSED_KEY, JSON.stringify([...collapsedGroups]));
  }

  function toggleGroupCollapse(name: string): void {
    const next = new Set(collapsedGroups);
    if (next.has(name)) next.delete(name); else next.add(name);
    collapsedGroups = next;
    persistCollapsedGroups();
  }

  async function loadGroups(): Promise<void> {
    try {
      registryGroups = await listCameraGroups();
    } catch (e) {
      console.warn('Failed to load camera groups:', e);
    }
  }

  // Ordered union of registry + camera-derived labels — drives section
  // rendering (and the form datalist). The registry carries the user's
  // drag-to-reorder positions; derived-only labels append zh-sorted at the end.
  const knownGroups = $derived.by(() => {
    const ordered = [...registryGroups];
    const seen = new Set(ordered);
    const derived = [...new Set(cameras.map(c => (c.group || '').trim()).filter(Boolean))]
      .sort((a, b) => a.localeCompare(b, 'zh'));
    for (const g of derived) {
      if (!seen.has(g)) {
        ordered.push(g);
        seen.add(g);
      }
    }
    return ordered;
  });
  const hasGroups = $derived(knownGroups.length > 0);

  // Sectioned view: named groups first (sorted), ungrouped bucket last.
  // A single all-ungrouped bucket keeps the legacy flat grid.
  const groupedCameras = $derived.by(() => {
    const map = new Map<string, Camera[]>();
    for (const c of cameras) {
      const g = (c.group || '').trim();
      if (!map.has(g)) map.set(g, []);
      map.get(g)!.push(c);
    }
    const sections: { name: string; cameras: Camera[] }[] =
      knownGroups.map(name => ({ name, cameras: map.get(name) ?? [] }));
    const ungrouped = map.get('');
    if (ungrouped && ungrouped.length > 0) sections.push({ name: '', cameras: ungrouped });
    return sections;
  });

  function autofocus(el: HTMLInputElement): void {
    el.focus();
    el.select();
  }

  async function createGroup(): Promise<void> {
    const name = newGroupName.trim();
    if (!name) {
      showToast(t('cameras.groupNameEmpty'), 'error');
      return;
    }
    try {
      await createCameraGroup(name);
      showToast(t('cameras.groupCreatedToast'), 'success');
      showNewGroup = false;
      newGroupName = '';
      await loadGroups();
    } catch (e) {
      showToast(friendlyError(e), 'error');
    }
  }

  function startRename(name: string): void {
    renamingGroup = name;
    renameValue = name;
  }

  async function commitRename(): Promise<void> {
    const oldName = renamingGroup;
    if (!oldName) return;
    const newName = renameValue.trim();
    renamingGroup = null;
    if (!newName || newName === oldName) return;
    try {
      await renameCameraGroup(oldName, newName);
      showToast(t('cameras.groupRenamedToast'), 'success');
      // Keep the collapse state under the new name.
      if (collapsedGroups.has(oldName)) {
        const next = new Set([...collapsedGroups].map(g => (g === oldName ? newName : g)));
        collapsedGroups = next;
        persistCollapsedGroups();
      }
      await Promise.all([loadGroups(), loadCameras()]);
    } catch (e) {
      showToast(friendlyError(e), 'error');
    }
  }

  async function confirmDeleteGroup(): Promise<void> {
    const name = deleteGroupConfirm;
    if (!name) return;
    deleteGroupConfirm = null;
    try {
      await deleteCameraGroup(name);
      const next = new Set(collapsedGroups);
      next.delete(name);
      collapsedGroups = next;
      persistCollapsedGroups();
      showToast(t('cameras.groupDeletedToast'), 'success');
      await Promise.all([loadGroups(), loadCameras()]);
    } catch (e) {
      showToast(friendlyError(e), 'error');
    }
  }

  // Drag & drop — section drop handlers. Foreign drags (file drops etc.) are
  // ignored by checking the custom MIME type.
  function onSectionDragOver(e: DragEvent, group: string): void {
    if (!e.dataTransfer) return;
    if (!(e.dataTransfer.types || []).includes('application/x-mibee-camera')) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    dragOverGroup = group;
  }

  function onSectionDragLeave(e: DragEvent, group: string): void {
    const related = e.relatedTarget as Node | null;
    if (related && e.currentTarget.contains(related)) return;
    if (dragOverGroup === group) dragOverGroup = null;
  }

  async function onSectionDrop(e: DragEvent, group: string): Promise<void> {
    e.preventDefault();
    dragOverGroup = null;
    const id = e.dataTransfer?.getData('application/x-mibee-camera');
    if (!id) return;
    await moveCameraToGroup(id, group);
  }

  async function moveCameraToGroup(id: string, group: string): Promise<void> {
    const cam = cameras.find(c => c.id === id);
    if (!cam) return;
    const prev = (cam.group || '').trim();
    if (prev === group) return;
    // Optimistic move; revert on failure.
    cameras = cameras.map(c => (c.id === id ? { ...c, group } : c));
    try {
      await updateCamera(id, { group });
      showToast(t('cameras.groupMovedToast', {
        camera: cam.name,
        group: group || t('cameras.groupUngrouped'),
      }), 'success');
    } catch (e) {
      cameras = cameras.map(c => (c.id === id ? { ...c, group: prev } : c));
      showToast(friendlyError(e), 'error');
    }
  }

  // Group reorder (v38): drag a group header onto another header = insert
  // BEFORE it; onto the ungrouped header = move to last. Distinct MIME type
  // from the camera drag so the two drop handlers never cross-fire.
  let reorderDragOver = $state<string | null>(null);

  function onHeaderDragStart(e: DragEvent, group: string): void {
    if (!e.dataTransfer) return;
    e.dataTransfer.setData('application/x-mibee-group', group);
    e.dataTransfer.effectAllowed = 'move';
  }

  function onHeaderDragOver(e: DragEvent, target: string): void {
    if (!e.dataTransfer) return;
    if (!(e.dataTransfer.types || []).includes('application/x-mibee-group')) return;
    const dragged = e.dataTransfer.getData('application/x-mibee-group');
    if (dragged === target) return; // no-op slot
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    reorderDragOver = target;
  }

  function onHeaderDragLeave(e: DragEvent, target: string): void {
    const related = e.relatedTarget as Node | null;
    if (related && e.currentTarget.contains(related)) return;
    if (reorderDragOver === target) reorderDragOver = null;
  }

  async function onHeaderDrop(e: DragEvent, target: string): Promise<void> {
    e.preventDefault();
    e.stopPropagation(); // don't let the section's camera-drop handler run
    reorderDragOver = null;
    const dragged = e.dataTransfer?.getData('application/x-mibee-group');
    if (!dragged || dragged === target) return;
    await reorderGroup(dragged, target);
  }

  async function reorderGroup(dragged: string, target: string): Promise<void> {
    const names = knownGroups.slice();
    const from = names.indexOf(dragged);
    if (from < 0) return;
    names.splice(from, 1);
    if (target === '') {
      names.push(dragged); // dropped on 未分组 → last named slot
    } else {
      const to = names.indexOf(target);
      if (to < 0) names.push(dragged);
      else names.splice(to, 0, dragged);
    }
    if (names.every((n, i) => n === knownGroups[i])) return; // no change
    // Optimistic reorder; reload from server truth on completion/failure.
    registryGroups = names;
    try {
      await setCameraGroupsOrder(names);
    } catch (e) {
      showToast(friendlyError(e), 'error');
    }
    await loadGroups();
  }

  // Discovery panel
  let discoveryPanel: ReturnType<typeof DiscoveryPanel> | null = $state(null);
  let activeDiscoveryProtocol = $state<string | null>(null);
  let showDiscoveryMenu = $state(false);

  let discoverableProtocols = $derived(protocols.filter(p => p.capabilities.discovery));

  // Onboarding state
  let showOnboarding = $state(false);

  let tabItems = $derived([
    { id: 'active', label: t('cameras.tab.active'), icon: CameraIcon, count: cameras.length },
    { id: 'archived', label: t('cameras.tab.archived'), icon: ArchiveIcon, count: archives.length },
  ]);

  async function loadArchives() {
    try {
      const res = await listArchives();
      archives = res.archives || [];
    } catch (e) {
      console.warn('Failed to load archives:', e);
    }
  }

  async function loadCleanupStatus() {
    try {
      const res = await getArchiveCleanupStatus();
      cleanupTasks = [...res.active, ...res.recent.filter(t => t.status === 'failed')];
      // Stop polling when no active tasks remain
      if (res.active.length === 0 && cleanupPolling) {
        clearInterval(cleanupPolling);
        cleanupPolling = null;
        if (res.recent.some(t => t.status === 'done')) {
          showToast(t('cameras.archive.cleanup.allDone'), 'success');
        }
      }
    } catch (e) { /* silent fail — polling is best-effort */ }
  }

  async function openArchiveConfirm(camera: Camera) {
    archiveConfirm = camera;
    archiveConfirmCount = 0;
    archiveConfirmSize = 0;
    archiveConfirmStatsLoading = true;
    try {
      const res = await getCameraRecordingStats(camera.id);
      archiveConfirmCount = res.recording_count || 0;
      archiveConfirmSize = res.total_size || 0;
    } catch (e) {
      console.warn('Failed to load archive stats:', e);
    } finally {
      archiveConfirmStatsLoading = false;
    }
  }

  async function handleRetentionChange(archiveId: string, days: number) {
    try {
      await setArchiveRetention(archiveId, days);
      showToast(t('cameras.archive.retentionUpdateSuccess'), 'success');
      await loadArchives();
    } catch (e) {
      showToast(t('cameras.failedArchive'), 'error');
    }
  }

  async function handleDeleteArchive(archiveId: string) {
    deleteArchiveLoading = true;
    try {
      await deleteArchiveGroup(archiveId);
      showToast(t('cameras.archive.deleteAllSuccess'), 'success');
      confirmDeleteArchive = null;
      await loadArchives();
      // Start polling for cleanup status
      if (!cleanupPolling) {
        await loadCleanupStatus();
        cleanupPolling = window.setInterval(loadCleanupStatus, 3000);
      }
    } catch (e) {
      showToast(t('cameras.archive.cleanup.deleteFailed'), 'error');
    } finally {
      deleteArchiveLoading = false;
    }
  }

  // Archive recording functions
  async function loadArchiveRecordings(cameraId: string) {
    archiveRecordingsLoading = true;
    try {
      const response = await listArchiveRecordings(cameraId, {
        offset: archiveRecordingsOffset,
        limit: archiveRecordingsLimit
      });
      archiveRecordings = response.recordings || [];
      archiveRecordingsTotal = response.total || 0;
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    } finally {
      archiveRecordingsLoading = false;
    }
  }

  function toggleArchive(group: ArchiveGroup) {
    if (expandedArchiveId === group.id) {
      expandedArchiveId = null;
      archiveRecordings = [];
      archiveRecordingsTotal = 0;
      archiveRecordingsOffset = 0;
    } else {
      expandedArchiveId = group.id;
      archiveRecordingsOffset = 0;
      loadArchiveRecordings(group.id);
    }
  }

  function playRecording(rec: Recording) {
    window.location.hash = `#/recordings/${rec.id}`;
  }

  function downloadRecording(rec: Recording) {
    const url = `${API_BASE}/archives/${expandedArchiveId}/recordings/${rec.id}/download`;
    const authHeader = getAuthHeader();
    if (authHeader) {
      fetch(url, {
        headers: { 'Authorization': authHeader }
      })
        .then(res => {
          if (!res.ok) throw new Error(`HTTP ${res.status}`);
          return res.blob();
        })
        .then(blob => {
          const objectUrl = URL.createObjectURL(blob);
          const link = document.createElement('a');
          link.href = objectUrl;
          link.download = `archive_${rec.camera_id}_${rec.id}.mp4`;
          document.body.appendChild(link);
          link.click();
          document.body.removeChild(link);
          URL.revokeObjectURL(objectUrl);
        })
        .catch(() => {
          showToast(t('common.error'), 'error');
        });
      return;
    }
    const a = document.createElement('a');
    a.href = url;
    a.download = `archive_${rec.camera_id}_${rec.id}.mp4`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }

  async function confirmDeleteRecordingFn() {
    if (!deleteRecordingConfirm || !expandedArchiveId) return;
    try {
      await deleteArchiveRecording(expandedArchiveId, deleteRecordingConfirm.id);
      archiveRecordings = archiveRecordings.filter(r => r.id !== deleteRecordingConfirm!.id);
      archiveRecordingsTotal--;
      showToast(t('archives.deleteRecordingSuccess'), 'success');
      deleteRecordingConfirm = null;
      loadArchives();
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    }
  }

  function openRetDialog(group: ArchiveGroup) {
    selectedArchiveGroup = group;
    retentionDays = group.archive_retention_days;
    showRetDialog = true;
  }

  async function confirmSetRetention() {
    if (!selectedArchiveGroup) return;
    try {
      await setArchiveRetention(selectedArchiveGroup.id, retentionDays);
      archives = archives.map(g =>
        g.id === selectedArchiveGroup!.id ? { ...g, archive_retention_days: retentionDays } : g
      );
      showToast(t('archives.retentionUpdated'), 'success');
      showRetDialog = false;
      selectedArchiveGroup = null;
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(t('common.error')), 'error');
    }
  }

  function formatRetention(days: number): string {
    if (days === 0) return t('archives.keepForever');
    return `${days} ${t('archives.retentionDays')}`;
  }

  let currentArchivePage = $derived(Math.floor(archiveRecordingsOffset / archiveRecordingsLimit) + 1);
  let totalArchivePages = $derived(Math.ceil(archiveRecordingsTotal / archiveRecordingsLimit));

  function handleArchivePageChange(newPage: number) {
    archiveRecordingsOffset = (newPage - 1) * archiveRecordingsLimit;
    if (expandedArchiveId) {
      loadArchiveRecordings(expandedArchiveId);
    }
  }

  async function loadCameras() {
    loading = true;
    error = '';
    try {
      cameras = await listCameras();
      const tutkCameras = cameras.filter(c => c.error_type === 'tutk_incompatible');
      if (tutkCameras.length === 1) {
        showToast(tutkCameras[0].name + ': ' + t('cameras.tutkIncompatible'), 'warning');
      } else if (tutkCameras.length > 1) {
        showToast(tutkCameras.length + ' ' + t('cameras.tutkToastTitle'), 'warning');
      }
    } catch (e) {
      error = friendlyError(e, 'cameras.failedLoad');
    } finally {
      loading = false;
      if (!loading && cameras.length === 0 && !sessionStorage.getItem('mibee_nvr_onboarding_dismissed')) {
        showOnboarding = true;
      }
    }
    loadArchives();
    loadHealth();
  }

  async function loadHealth() {
    try {
      const res = await getHealthStatus();
      healthData = res;
    } catch (e) {
      console.warn('Failed to load health:', e);
    }
  }

  function openAddForm() {
    editingCamera = null;
    showForm = true;
  }

  function openEditForm(camera: Camera) {
    editingCamera = camera;
  }

  function handleFormSave() {
    showForm = false;
    editingCamera = null;
    showOnboarding = false;
    loadCameras();
  }

  function handleFormCancel() {
    showForm = false;
    editingCamera = null;
  }

  async function handleBackfillNeeded(info: { cameraId: string; count: number; targetCodec: string }): Promise<boolean> {
    return new Promise<boolean>((resolve) => {
      backfillInfo = { ...info, resolve };
    });
  }

  async function handleBackfillConfirm() {
    if (!backfillInfo) return;
    backfillLoading = true;
    try {
      const result = await startBackfill(backfillInfo.cameraId);
      showToast(t('transcoding.backfill.success', { count: String(result.enqueued) }), 'success');
      backfillInfo.resolve(true);
      backfillInfo = null;
    } catch (e) {
      console.warn('Backfill failed:', e);
      showToast(t('transcoding.backfill.error'), 'error');
    } finally {
      backfillLoading = false;
    }
  }

  function handleBackfillCancel() {
    if (!backfillInfo) return;
    backfillInfo.resolve(false);
    backfillInfo = null;
  }

  async function executeConfirmAction() {
    if (!confirmAction) return;
    const { camera, action } = confirmAction;
    confirmAction = null;
    switch (action) {
      case 'stop':
        try {
          await stopCamera(camera.id);
          showToast(t('cameras.stopped'), 'success');
          await loadCameras();
        } catch (e: any) { showToast(friendlyError(e, 'cameras.failedStop'), 'error'); }
        break;
      case 'restart':
        try {
          await stopCamera(camera.id);
          await startCamera(camera.id);
          showToast(t('cameras.cameraUpdated'), 'success');
          await loadCameras();
        } catch (e: any) { showToast(friendlyError(e, 'cameras.failedStart'), 'error'); }
        break;
    }
  }

  async function handleStartCamera(camera: Camera) {
    try {
      await startCamera(camera.id);
      showToast(t('cameras.started'), 'success');
      await loadCameras();
    } catch (e: any) {
      showToast(friendlyError(e, 'cameras.failedStart'), 'error');
    }
  }

  async function handleStopCamera(camera: Camera) {
    confirmAction = { camera, action: 'stop' };
  }

  async function handleRestartCamera(camera: Camera) {
    confirmAction = { camera, action: 'restart' };
  }

  // Track which cameras are currently being re-located (the scan can take ~30s).
  let rediscovering = $state<Set<string>>(new Set());

  async function handleRediscoverCamera(camera: Camera) {
    if (rediscovering.has(camera.id)) return;
    rediscovering = new Set([...rediscovering, camera.id]);
    showToast(t('cameras.action.rediscoverScanning'), 'info');
    try {
      const res = await rediscoverCamera(camera.id);
      if (res.found) {
        showToast(t('cameras.action.rediscoverFound'), 'success');
      } else {
        showToast(t('cameras.action.rediscoverNotFound'), 'warning');
      }
      await loadCameras();
    } catch (e: any) {
      showToast(friendlyError(e, 'cameras.action.rediscoverFailed'), 'error');
    } finally {
      const next = new Set(rediscovering);
      next.delete(camera.id);
      rediscovering = next;
    }
  }

  // Activate a pending_activation camera (auto-discovered, credentials unknown)
  // by supplying ONVIF credentials. The CameraCard dialog collects them and
  // calls this via the onactivate prop.
  async function handleActivateCamera(camera: Camera, credentials: { username: string; password: string }) {
    try {
      await activateCamera(camera.id, credentials);
      showToast(t('cameras.activateSuccess'), 'success');
      await loadCameras();
    } catch (e: any) {
      // The recorder auto-restored (e.g. on NVR restart) and raced with this
      // request — the camera is already in the desired state, so treat it as
      // success rather than a scary red error. Backend returns 409 with
      // code CAMERA_ALREADY_RUNNING (mirrors the /start endpoint).
      if (e instanceof ApiRequestError && e.code === 'CAMERA_ALREADY_RUNNING') {
        showToast(t('errors.CAMERA_ALREADY_RUNNING'), 'info');
        await loadCameras();
        return; // don't re-throw: close the dialog, status is already aligned
      }
      showToast(friendlyError(e, 'cameras.activateFailed'), 'error');
      throw e; // re-throw so CameraCard keeps the dialog open on failure
    }
  }


  async function handleSaveName(camera: Camera, name: string) {
    try {
      await updateCamera(camera.id, { name });
      showToast(t('cameras.nameUpdated'), 'success');
      await loadCameras();
    } catch (e) {
      console.warn('Failed to update camera name:', e);
      showToast(t('cameras.failedUpdate'), 'error');
    }
  }


  onMount(async () => {
    loadCameras();
    loadGroups();
    loadHealth();
    loadArchives().then(() => loadCleanupStatus().then(() => {
      if (cleanupTasks.some(t => t.status === 'pending' || t.status === 'running')) {
        cleanupPolling = window.setInterval(loadCleanupStatus, 3000);
      }
    }));
    try {
      const list = await listProtocols();
      if (list && list.length > 0) {
        protocols = list;
        protocolsMap = buildProtocolsMap(list);
      }
    } catch (e) { console.warn('Failed to load protocols:', e); }
    try {
      const res = await xiaomiDevices();
      if (res.devices && res.devices.length > 0) {
        xiaomiDeviceList = res.devices;
      }
    } catch (e) { console.warn('Xiaomi not authenticated:', e); }
    try {
      const ts = await getTranscodingSettings();
      globalTranscodingEnabled = ts.enabled;
      // Check hardware H.265 capability
      if (ts.enabled) {
        try {
          const check = await getTranscodingCheck();
          h265Available = check.h265_encoder_type !== 'software';
        } catch (e) { h265Available = false; }
      }
    } catch (e) { /* Transcoding may not be available */ }

    const healthInterval = window.setInterval(() => loadHealth(), 30000);
    return () => clearInterval(healthInterval);
  });

  onDestroy(() => { if (cleanupPolling) clearInterval(cleanupPolling); });

  // Live auto-discover notifications: when the backend's auto-discover service
  // adds a camera, it publishes a 'camera.added' SSE event. Refresh the list and
  // toast so the user sees the new camera immediately (mirrors Hikvision NVR
  // plug-and-play feedback). The EventSource uses named events (the backend
  // emits event: camera.added), so we addEventListener rather than onmessage.
  $effect(() => {
    let es: EventSource | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout>;
    function connect() {
      es = new EventSource(`${API_BASE}/events?filter=camera.`);
      es.addEventListener('camera.added', (e: MessageEvent) => {
        try {
          const d = JSON.parse(e.data) as { name?: string; activation_state?: string };
          loadCameras();
          if (d.activation_state === 'pending_activation') {
            showToast(t('cameras.autoDiscoveredPending', { name: d.name ?? '' }), 'warning');
          } else {
            showToast(t('cameras.autoDiscovered', { name: d.name ?? '' }), 'success');
          }
        } catch { /* ignore malformed event */ }
      });
      es.onerror = () => {
        es?.close();
        es = null;
        // Reconnect after a delay (the events endpoint may have been briefly
        // unavailable, e.g. during a redeploy).
        reconnectTimer = setTimeout(connect, 5000);
      };
    }
    connect();
    return () => {
      es?.close();
      clearTimeout(reconnectTimer);
    };
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Page Header -->
    <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between mb-6 gap-3">
      <h2 class="text-2xl font-bold th-text-primary">{t('cameras.title')}</h2>
      <div class="flex gap-3">
        {#if discoverableProtocols.length > 0}
          <div class="relative">
            <button onclick={() => {
              if (discoverableProtocols.length === 1) {
                activeDiscoveryProtocol = discoverableProtocols[0].id;
              } else {
                showDiscoveryMenu = !showDiscoveryMenu;
              }
            }} class="btn btn-ghost">
              {t('discovery.scanDevices')}
            </button>
            {#if showDiscoveryMenu && discoverableProtocols.length > 1}
              <div class="absolute right-0 top-full mt-1 card border th-border rounded-md shadow-lg z-10 py-1 min-w-[140px]">
                {#each discoverableProtocols as proto}
                  <button
                    class="w-full text-left px-4 py-2 text-sm th-text-primary hover:th-bg-hover transition-colors"
                    onclick={() => { activeDiscoveryProtocol = proto.id; showDiscoveryMenu = false; }}
                  >
                    {proto.label}
                  </button>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
        <button onclick={() => { showNewGroup = true; newGroupName = ''; }} class="btn btn-secondary flex items-center gap-1">
          <FolderPlus size={16} />
          {t('cameras.createGroup')}
        </button>
        <button onclick={openAddForm} class="btn btn-primary">
          + {t('cameras.addCamera')}
        </button>
      </div>
    </div>

    <!-- Tab Bar -->
    <Tab tabs={tabItems} {activeTab} onchange={(id) => activeTab = id} />

    <!-- Error -->
    {#if error}
      <div class="card border th-border-danger p-8 text-center mt-6">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadCameras} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    <!-- Loading -->
    {#if loading}
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 mt-6">
        {#each Array(6) as _}
          <div class="card border th-border p-4 space-y-3 animate-pulse">
            <div class="flex items-center justify-between">
              <div class="h-4 w-28 th-bg-tertiary rounded"></div>
              <div class="h-5 w-16 th-bg-tertiary rounded-full"></div>
            </div>
            <div class="h-3 w-20 th-bg-tertiary rounded"></div>
            <div class="h-3 w-full th-bg-tertiary rounded"></div>
            <div class="border-t th-border pt-3 flex justify-between">
              <div class="h-6 w-10 th-bg-tertiary rounded-full"></div>
              <div class="flex gap-1">
                <div class="h-6 w-6 th-bg-tertiary rounded"></div>
                <div class="h-6 w-6 th-bg-tertiary rounded"></div>
              </div>
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <!-- Active Tab -->
      {#if activeTab === 'active'}
        <!-- Discovery Panel -->
        {#if activeDiscoveryProtocol}
          <DiscoveryPanel
            bind:this={discoveryPanel}
            protocol={activeDiscoveryProtocol}
            {cameras}
            oncameraadded={loadCameras}
          />
        {/if}

        <!-- Add Form (only for new camera) -->
        {#if showForm && !editingCamera}
          <CameraForm
            editingCamera={null}
            {protocols}
            {protocolsMap}
            {xiaomiDeviceList}
            globalTranscodingEnabled={globalTranscodingEnabled}
            h265Available={h265Available}
            knownGroups={knownGroups}
            onsave={handleFormSave}
            oncancel={handleFormCancel}
            onbackfillneeded={handleBackfillNeeded}
          />
        {/if}

        <!-- Confirmation Dialog (stop/restart) -->
        {#if confirmAction}
          <ConfirmDialog
            title={confirmAction.action === 'stop' ? t('cameras.stopTitle') : t('cameras.restartTitle')}
            message={confirmAction.action === 'stop'
              ? t('cameras.stopMessage', { name: confirmAction.camera.name })
              : t('cameras.restartMessage', { name: confirmAction.camera.name })}
            confirmText={t('common.confirm')}
            onconfirm={executeConfirmAction}
            oncancel={() => confirmAction = null}
            variant="primary"
          />
        {/if}

        <!-- Camera Grid -->
        {#if cameras.length === 0}
          <div class="card border th-border p-12 text-center mt-6">
            <div class="flex justify-center mb-4 th-text-muted">
              <CameraIcon size={48} />
            </div>
            <h3 class="text-lg font-medium th-text-primary mb-2">{t('cameras.noCameras')}</h3>
            <p class="text-sm th-text-muted mb-4">{t('cameras.noCamerasHint')}</p>
            <button onclick={openAddForm} class="btn btn-primary btn-sm">+ {t('cameras.addCamera')}</button>
          </div>
        {:else}
          <!-- Grouped camera grid (v37): named group sections with collapsible
               headers (inline rename + delete), drag & drop between sections,
               ungrouped bucket last. When no group exists at all, hasGroups is
               false and this renders the legacy flat grid. -->
          {#each groupedCameras as grp (grp.name)}
            {@const collapsed = collapsedGroups.has(grp.name)}
            <div
              class="camera-group-section {hasGroups && dragOverGroup === grp.name ? 'camera-group-section--drop' : ''}"
              role="group"
              aria-label={grp.name || t('cameras.groupUngrouped')}
              ondragover={hasGroups ? (e) => onSectionDragOver(e, grp.name) : undefined}
              ondragleave={hasGroups ? (e) => onSectionDragLeave(e, grp.name) : undefined}
              ondrop={hasGroups ? (e) => onSectionDrop(e, grp.name) : undefined}
            >
              {#if hasGroups}
                {#if renamingGroup === grp.name}
                  <div class="mt-6 card border th-border rounded-lg px-3 py-2 flex items-center gap-2">
                    <input
                      type="text"
                      class="input h-8 max-w-[260px] text-sm"
                      bind:value={renameValue}
                      use:autofocus
                      onkeydown={(e) => {
                        if (e.key === 'Enter') commitRename();
                        else if (e.key === 'Escape') renamingGroup = null;
                      }}
                      onblur={() => { if (renamingGroup === grp.name) commitRename(); }}
                      placeholder={t('cameras.groupNamePlaceholder')}
                    />
                    <button class="btn btn-primary btn-sm" onclick={commitRename}>{t('cameras.save')}</button>
                    <button class="btn btn-secondary btn-sm" onclick={() => { renamingGroup = null; }}>{t('common.cancel')}</button>
                  </div>
                {:else}
                  <div
                    class="mt-6 card border th-border rounded-lg px-3 py-2 flex items-center gap-2 select-none {reorderDragOver === grp.name ? 'camera-group-header--insert' : ''}"
                    ondragover={hasGroups ? (e) => onHeaderDragOver(e, grp.name) : undefined}
                    ondragleave={hasGroups ? (e) => onHeaderDragLeave(e, grp.name) : undefined}
                    ondrop={hasGroups ? (e) => onHeaderDrop(e, grp.name) : undefined}
                  >
                    <button type="button"
                      class="flex items-center gap-2 flex-1 min-w-0 text-left {grp.name ? 'cursor-grab active:cursor-grabbing' : ''}"
                      title={grp.name ? t('cameras.groupReorderHint') : undefined}
                      draggable={grp.name ? 'true' : undefined}
                      ondragstart={grp.name ? (e) => onHeaderDragStart(e, grp.name) : undefined}
                      onclick={() => toggleGroupCollapse(grp.name)}
                      aria-expanded={!collapsed}>
                      {#if collapsed}
                        <ChevronRight size={16} class="th-text-secondary shrink-0" />
                      {:else}
                        <ChevronDown size={16} class="th-text-secondary shrink-0" />
                      {/if}
                      <span class="font-semibold th-text-primary truncate">
                        {grp.name || t('cameras.groupUngrouped')}
                      </span>
                      <span class="badge badge-neutral shrink-0">{grp.cameras.length}</span>
                    </button>
                    {#if grp.name}
                      <button type="button" class="btn btn-ghost btn-sm shrink-0 p-1.5"
                        title={t('cameras.renameGroup')} aria-label={t('cameras.renameGroup')}
                        onclick={() => startRename(grp.name)}>
                        <Pencil size={14} />
                      </button>
                      <button type="button" class="btn btn-ghost btn-sm shrink-0 p-1.5 th-color-danger"
                        title={t('cameras.deleteGroup')} aria-label={t('cameras.deleteGroup')}
                        onclick={() => { deleteGroupConfirm = grp.name; }}>
                        <Trash2 size={14} />
                      </button>
                    {/if}
                  </div>
                {/if}
              {/if}
              {#if !hasGroups || !collapsed}
                {#if hasGroups && grp.cameras.length === 0}
                  <!-- Empty group (registered but no member yet): drop hint -->
                  <div class="mt-3 border-2 border-dashed th-border rounded-lg p-8 text-center text-sm th-text-muted">
                    {t('cameras.groupDropHint')}
                  </div>
                {:else}
                  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 {hasGroups ? 'mt-3' : 'mt-6'}">
                    {#each grp.cameras as camera (camera.id)}
                      <CameraCard
                        {camera}
                        {protocolsMap}
                        health={healthData[camera.id]}
                        groupDraggable={true}
                        onedit={openEditForm}
                        ondelete={openArchiveConfirm}
                        onstart={handleStartCamera}
                        onstop={handleStopCamera}
                        onrestart={handleRestartCamera}
                        onrediscover={handleRediscoverCamera}
                        onactivate={handleActivateCamera}
                        onsaveName={handleSaveName}
                      />
                      <!-- Inline Edit Form for this camera -->
                      {#if editingCamera && editingCamera.id === camera.id}
                        <div class="col-span-1 sm:col-span-2 lg:col-span-3 animate-slide-down">
                          <CameraForm
                            {editingCamera}
                            {protocols}
                            {protocolsMap}
                            {xiaomiDeviceList}
                            globalTranscodingEnabled={globalTranscodingEnabled}
                            h265Available={h265Available}
                            knownGroups={knownGroups}
                            onsave={handleFormSave}
                            oncancel={handleFormCancel}
                            onbackfillneeded={handleBackfillNeeded}
                          />
                        </div>
                      {/if}
                    {/each}
                  </div>
                {/if}
              {/if}
            </div>
          {/each}
        {/if}
      {:else}
        <!-- Archived Tab -->
        {#if cleanupTasks.length > 0}
          <div class="card border th-border p-4 mb-4 flex items-center gap-3">
            <Loader2 size={20} class="animate-spin th-text-secondary shrink-0" />
            <div class="flex-1 min-w-0">
              <p class="text-sm th-text-primary font-medium">
                {t('cameras.archive.cleanup.inProgress', { count: String(cleanupTasks.filter(t => t.status === 'pending' || t.status === 'running').length) })}
              </p>
              <div class="mt-1 space-y-0.5">
                {#each cleanupTasks as task (task.camera_id)}
                  <p class="text-xs th-text-muted">
                    {t('cameras.archive.cleanup.taskItem', {
                      name: task.camera_name,
                      count: String(task.recording_count),
                      size: formatFileSize(task.total_size),
                      status: t('cameras.archive.cleanup.status' + task.status.charAt(0).toUpperCase() + task.status.slice(1))
                    })}
                  </p>
                {/each}
              </div>
            </div>
          </div>
        {/if}
        {#if archives.length === 0}
          <div class="card border th-border p-12 text-center mt-6">
            <div class="flex justify-center mb-4 th-text-muted">
              <ArchiveIcon size={48} />
            </div>
            <h3 class="text-lg font-medium th-text-primary mb-2">{t('cameras.archive.noArchives')}</h3>
            <p class="text-sm th-text-muted mb-4">{t('cameras.archive.noArchivesHint')}</p>
          </div>
        {:else}
          <div class="space-y-3 mt-6">
            {#each archives as group (group.id)}
              <div class="card border th-border overflow-hidden">
                <!-- Group header -->
                <div
                  class="w-full p-5 text-left hover:th-bg-hover transition-colors duration-200 cursor-pointer"
                  onclick={() => toggleArchive(group)}
                  role="button"
                  tabindex="0"
                  onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleArchive(group); } }}
                >
                  <div class="flex items-center justify-between gap-4">
                    <div class="flex items-center gap-3 min-w-0">
                      {#if expandedArchiveId === group.id}
                        <ChevronDown size={20} class="th-text-secondary shrink-0" />
                      {:else}
                        <ChevronRight size={20} class="th-text-secondary shrink-0" />
                      {/if}
                      <div class="min-w-0">
                        <h3 class="font-semibold th-text-primary truncate">{group.name}</h3>
                        <div class="flex flex-wrap gap-x-5 gap-y-1 mt-1.5 text-sm th-text-secondary">
                          <span class="flex items-center gap-1.5">
                            <Video size={14} />
                            {group.recording_count} {t('archives.recordings')}
                          </span>
                          <span class="flex items-center gap-1.5">
                            <ArchiveIcon size={14} />
                            {formatFileSize(group.total_size)}
                          </span>
                          <span class="flex items-center gap-1.5">
                            <Clock size={14} />
                            {t('archives.archivedAt')}: {formatDate(group.archived_at)}
                          </span>
                          <span class="flex items-center gap-1.5">
                            <Settings size={14} />
                            {formatRetention(group.archive_retention_days)}
                          </span>
                        </div>
                      </div>
                    </div>
                    <div class="flex items-center gap-2 shrink-0" role="group" aria-label={t('archives.actions')}>
                      <button
                        class="btn btn-ghost btn-sm"
                        onclick={(e) => { e.stopPropagation(); openRetDialog(group); }}
                        title={t('archives.setRetention')}
                      >
                        <Clock size={16} />
                      </button>
                      <button
                        class="btn btn-ghost btn-sm th-color-danger"
                        onclick={(e) => { e.stopPropagation(); confirmDeleteArchive = group.id; }}
                        title={t('archives.deleteGroup')}
                      >
                        <Trash2 size={16} />
                      </button>
                    </div>
                  </div>
                </div>

                <!-- Expanded recordings -->
                {#if expandedArchiveId === group.id}
                  <div class="border-t th-border">
                    {#if archiveRecordingsLoading}
                      <div class="p-6 space-y-3">
                        {#each Array(3) as _}
                          <div class="flex gap-4 items-center">
                            <div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div>
                            <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
                            <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
                            <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse ml-auto"></div>
                          </div>
                        {/each}
                      </div>
                    {:else if archiveRecordings.length === 0}
                      <div class="p-6 text-center th-text-muted text-sm">
                        {t('archives.noArchives')}
                      </div>
                    {:else}
                      <div class="table-container">
                        <table class="table">
                          <thead>
                            <tr>
                              <th>{t('archives.camera')}</th>
                              <th>{t('archives.date')}</th>
                              <th>{t('archives.duration')}</th>
                              <th>{t('archives.size')}</th>
                              <th class="text-right">{t('archives.actions')}</th>
                            </tr>
                          </thead>
                          <tbody>
                            {#each archiveRecordings as rec (rec.id)}
                              <tr class="transition-all duration-200 hover:th-bg-hover">
                                <td>
                                  <span class="font-mono text-xs th-text-tertiary">{rec.camera_id}</span>
                                </td>
                                <td class="whitespace-nowrap">{formatDate(rec.started_at)}</td>
                                <td class="font-mono text-sm">{formatDuration(rec.duration)}</td>
                                <td>{formatFileSize(rec.file_size)}</td>
                                <td class="text-right">
                                  <div class="flex justify-end gap-1">
                                    <button
                                      class="btn btn-ghost px-2 py-1.5 text-sm"
                                      onclick={() => playRecording(rec)}
                                      title={t('archives.play')}
                                    >
                                      <Play size={16} />
                                      <span class="hidden sm:inline-flex ml-1 text-xs">{t('cameras.action.viewLabel')}</span>
                                    </button>
                                    <button
                                      class="btn btn-ghost px-2 py-1.5 text-sm"
                                      onclick={() => downloadRecording(rec)}
                                      title={t('archives.download')}
                                    >
                                      <Download size={16} />
                                      <span class="hidden sm:inline-flex ml-1 text-xs">{t('cameras.action.downloadLabel')}</span>
                                    </button>
                                    <button
                                      class="btn btn-ghost px-2 py-1.5 text-sm th-color-danger"
                                      onclick={() => deleteRecordingConfirm = rec}
                                      title={t('archives.delete')}
                                    >
                                      <Trash2 size={16} />
                                      <span class="hidden sm:inline-flex ml-1 text-xs">{t('cameras.action.deleteLabel')}</span>
                                    </button>
                                  </div>
                                </td>
                              </tr>
                            {/each}
                          </tbody>
                        </table>
                      </div>

                      {#if totalArchivePages > 1}
                        <div class="px-4 py-2 border-t th-border">
                          <span class="text-sm th-text-muted">
                            {t('recordings.showing', {
                              start: String(archiveRecordingsOffset + 1),
                              end: String(Math.min(archiveRecordingsOffset + archiveRecordings.length, archiveRecordingsTotal)),
                              total: String(archiveRecordingsTotal)
                            })}
                          </span>
                        </div>
                        <Pagination
                          currentPage={currentArchivePage}
                          totalPages={totalArchivePages}
                          onPageChange={handleArchivePageChange}
                        />
                      {/if}
                    {/if}
                  </div>
                {/if}
              </div>
            {/each}
          </div>
      {/if}
    {/if}
    {/if}
  </main>

  <!-- Archive Confirm Dialog -->
  {#if archiveConfirm}
    <ArchiveConfirmDialog
      cameraName={archiveConfirm.name}
      recordingCount={archiveConfirmCount}
      totalSize={archiveConfirmStatsLoading ? '...' : formatFileSize(archiveConfirmSize)}
      loading={archiveLoading}
      onconfirm={async () => {
        archiveLoading = true;
        try {
          await deleteCamera(archiveConfirm!.id);
          showToast(t('cameras.cameraArchived'), 'success');
          archiveConfirm = null;
          await Promise.all([loadCameras(), loadArchives()]);
        } catch (e) {
          showToast(t('cameras.failedArchive'), 'error');
        } finally {
          archiveLoading = false;
        }
      }}
      oncancel={() => { if (!archiveLoading) archiveConfirm = null; }}
    />
  {/if}

  <!-- Archive Delete Confirm Dialog -->
  {#if confirmDeleteArchive}
    <ConfirmDialog
      title={t('cameras.action.deleteAll')}
      message={t('cameras.archive.deleteAllConfirm')}
      onconfirm={() => handleDeleteArchive(confirmDeleteArchive!)}
      oncancel={() => { if (!deleteArchiveLoading) confirmDeleteArchive = null; }}
      variant="danger"
      loading={deleteArchiveLoading}
    />
  {/if}
  <!-- Retention dialog -->
  {#if showRetDialog && selectedArchiveGroup}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog" aria-modal="true">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('archives.setRetention')}</h3>
        <p class="th-text-secondary mb-4">{selectedArchiveGroup.name}</p>
        <div class="mb-6">
          <label for="retention-select" class="input-label">{t('archives.retention')}</label>
          <select id="retention-select" class="input mt-1" bind:value={retentionDays}>
            <option value={0}>{t('archives.keepForever')}</option>
            <option value={7}>7 {t('archives.retentionDays')}</option>
            <option value={14}>14 {t('archives.retentionDays')}</option>
            <option value={30}>30 {t('archives.retentionDays')}</option>
            <option value={60}>60 {t('archives.retentionDays')}</option>
            <option value={90}>90 {t('archives.retentionDays')}</option>
            <option value={180}>180 {t('archives.retentionDays')}</option>
            <option value={365}>365 {t('archives.retentionDays')}</option>
          </select>
        </div>
        <div class="flex gap-3 justify-end">
          <button onclick={() => { showRetDialog = false; selectedArchiveGroup = null; }} class="btn btn-secondary">
            {t('recordings.cancel')}
          </button>
          <button onclick={confirmSetRetention} class="btn btn-primary">
            {t('archives.setRetention')}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Delete recording dialog -->
  {#if deleteRecordingConfirm}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog" aria-modal="true">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('archives.delete')}</h3>
        <p class="th-text-secondary mb-6">
          {t('archives.confirmDeleteRecording')}
        </p>
        <div class="flex gap-3 justify-end">
          <button onclick={() => { deleteRecordingConfirm = null; }} class="btn btn-secondary">
            {t('recordings.cancel')}
          </button>
          <button onclick={confirmDeleteRecordingFn} class="btn btn-danger">
            {t('recordings.deleteConfirm')}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Backfill Confirm Dialog -->
  {#if backfillInfo}
    <ConfirmDialog
      title={t('transcoding.backfill.confirm_title', { camera: editingCamera?.name || '' })}
      message={t('transcoding.backfill.confirm_message', {
        count: String(backfillInfo.count),
        codec: backfillInfo.targetCodec.toUpperCase(),
      })}
      confirmText={t('transcoding.backfill.confirm_button')}
      cancelText={t('recordings.cancel')}
      onconfirm={handleBackfillConfirm}
      oncancel={handleBackfillCancel}
      variant="primary"
      loading={backfillLoading}
    />
  {/if}

  <!-- New Camera Group Dialog (v37) -->
  {#if showNewGroup}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog" aria-modal="true">
      <div class="card max-w-sm w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('cameras.createGroup')}</h3>
        <input
          type="text"
          class="input mb-5"
          bind:value={newGroupName}
          use:autofocus
          placeholder={t('cameras.groupNamePlaceholder')}
          onkeydown={(e) => {
            if (e.key === 'Enter') createGroup();
            else if (e.key === 'Escape') showNewGroup = false;
          }}
        />
        <div class="flex justify-end gap-2">
          <button class="btn btn-secondary btn-sm" onclick={() => { showNewGroup = false; }}>{t('common.cancel')}</button>
          <button class="btn btn-primary btn-sm" onclick={createGroup}>{t('cameras.createGroup')}</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Delete Camera Group Confirm -->
  {#if deleteGroupConfirm}
    {@const groupCameraCount = groupedCameras.find(g => g.name === deleteGroupConfirm)?.cameras.length ?? 0}
    <ConfirmDialog
      title={t('cameras.deleteGroupTitle')}
      message={t('cameras.deleteGroupMessage', {
        name: deleteGroupConfirm,
        count: String(groupCameraCount),
      })}
      confirmText={t('common.confirm')}
      cancelText={t('common.cancel')}
      onconfirm={confirmDeleteGroup}
      oncancel={() => { deleteGroupConfirm = null; }}
      variant="danger"
    />
  {/if}

  <!-- Onboarding overlay for first-time users -->
  {#if showOnboarding && cameras.length === 0}
    <OnboardingOverlay
      onaddcamera={() => { showOnboarding = false; openAddForm(); }}
      oncomplete={() => { showOnboarding = false; window.location.hash = '#/recordings'; }}
      onskip={() => { showOnboarding = false; }}
    />
  {/if}
</div>

<style>
  /* Drop-target highlight while a camera card is dragged over a group section */
  .camera-group-section {
    border-radius: var(--radius-md, 0.5rem);
    transition: background-color 120ms ease-out;
  }
  .camera-group-section--drop {
    background-color: color-mix(in srgb, var(--color-primary, #7c3aed) 8%, transparent);
    outline: 2px dashed var(--color-primary, #7c3aed);
    outline-offset: 2px;
  }

  /* Insertion indicator: a group header being dragged over shows where the
     dragged group would land (above the highlighted header). */
  .camera-group-header--insert {
    border-top: 3px solid var(--color-primary, #7c3aed);
    box-shadow: 0 -2px 8px color-mix(in srgb, var(--color-primary, #7c3aed) 25%, transparent);
  }
</style>
