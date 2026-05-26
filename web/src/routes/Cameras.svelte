<script lang="ts">
  import { onMount } from 'svelte';
  import { listCameras, deleteCamera, startCamera, stopCamera, updateCamera, xiaomiDevices, listProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, ApiRequestError, enableCamera, disableCamera, listArchives, setArchiveRetention, deleteArchiveGroup, listArchiveRecordings, deleteArchiveRecording, getHealthStatus } from '$lib/api';
  import type { Camera, XiaomiDevice, ProtocolInfo, ArchiveGroup, Recording, CameraHealth, HealthStatusResponse } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { formatFileSize, formatDate, formatDuration } from '$lib/format';
  import { AlertCircle, Camera as CameraIcon, Plus, Archive as ArchiveIcon, Trash2, ExternalLink, Clock, HardDrive, Play, Download, ChevronDown, ChevronRight, Video, Settings } from 'lucide-svelte';
  import DiscoveryPanel from '$lib/components/DiscoveryPanel.svelte';
  import CameraForm from '$lib/components/CameraForm.svelte';
  import CameraCard from '$lib/components/CameraCard.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import ArchiveConfirmDialog from '$lib/components/ArchiveConfirmDialog.svelte';
  import OnboardingOverlay from '$lib/components/OnboardingOverlay.svelte';
  import Tab from '$lib/components/Tab.svelte';
  import Pagination from '../components/Pagination.svelte';

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let activeTab = $state('active');
  let archives = $state<ArchiveGroup[]>([]);
  let archiveConfirm = $state<Camera | null>(null);
  let archiveLoading = $state(false);
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

  // Form state
  let showForm = $state(false);
  let editingCamera = $state<Camera | null>(null);

  // Confirmation dialog state
  let confirmAction = $state<{ camera: Camera; action: 'stop' | 'restart' } | null>(null);

  // Xiaomi
  let xiaomiDeviceList = $state<XiaomiDevice[]>([]);

  // Protocol info
  let protocols = $state<ProtocolInfo[]>(DEFAULT_PROTOCOLS);
  let protocolsMap = $state<Map<string, ProtocolInfo>>(buildProtocolsMap(DEFAULT_PROTOCOLS));

  // Discovery panel
  let discoveryPanel: ReturnType<typeof DiscoveryPanel> | null = $state(null);
  let activeDiscoveryProtocol = $state<string | null>(null);
  let showDiscoveryMenu = $state(false);

  let discoverableProtocols = $derived(protocols.filter(p => p.capabilities.discovery));

  // Onboarding state
  let showOnboarding = $state(false);

  let tabItems = $derived([
    { id: 'active', label: t('cameras.tab.active'), icon: CameraIcon, count: cameras.filter(c => c.enabled).length },
    { id: 'archived', label: t('cameras.tab.archived'), icon: ArchiveIcon, count: archives.length },
  ]);

  function friendlyError(e: unknown, fallback: string): string {
    if (e instanceof ApiRequestError && e.code) {
      const keyed = t(`errors.${e.code}`);
      if (keyed !== `errors.${e.code}`) return keyed;
    }
    if (e instanceof Error) return e.message || fallback;
    return fallback;
  }

  async function loadArchives() {
    try {
      const res = await listArchives();
      archives = res.archives || [];
    } catch (e) {
      console.warn('Failed to load archives:', e);
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
    } catch (e) {
      showToast(t('cameras.failedArchive'), 'error');
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
    const url = `/api/archives/${expandedArchiveId}/recordings/${rec.id}/download`;
    const encoded = localStorage.getItem('mibee_nvr_auth');
    if (encoded) {
      fetch(url, {
        headers: { 'Authorization': `Basic ${encoded}` }
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
      error = friendlyError(e, t('cameras.failedLoad'));
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
    showForm = true;
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
        } catch (e: any) { showToast(e.message || t('cameras.failedStop'), 'error'); }
        break;
      case 'restart':
        try {
          await stopCamera(camera.id);
          await startCamera(camera.id);
          showToast(t('cameras.cameraUpdated'), 'success');
          await loadCameras();
        } catch (e: any) { showToast(e.message || t('cameras.failedStart'), 'error'); }
        break;
    }
  }

  async function handleStartCamera(camera: Camera) {
    try {
      await startCamera(camera.id);
      showToast(t('cameras.started'), 'success');
      await loadCameras();
    } catch (e: any) {
      showToast(e.message || t('cameras.failedStart'), 'error');
    }
  }

  async function handleStopCamera(camera: Camera) {
    confirmAction = { camera, action: 'stop' };
  }

  async function handleRestartCamera(camera: Camera) {
    confirmAction = { camera, action: 'restart' };
  }

  async function handleToggleCamera(camera: Camera) {
    await loadCameras();
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
    loadHealth();
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

    const healthInterval = window.setInterval(() => loadHealth(), 30000);
    return () => clearInterval(healthInterval);
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

        <!-- Add/Edit Form -->
        {#if showForm}
          <CameraForm
            {editingCamera}
            {protocols}
            {protocolsMap}
            {xiaomiDeviceList}
            onsave={handleFormSave}
            oncancel={handleFormCancel}
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
          <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 mt-6">
            {#each cameras as camera (camera.id)}
              <CameraCard
                {camera}
                {protocolsMap}
                health={healthData[camera.id]}
                onedit={openEditForm}
                ondelete={(c) => archiveConfirm = c}
                onstart={handleStartCamera}
                onstop={handleStopCamera}
                onrestart={handleRestartCamera}
                ontoggle={handleToggleCamera}
                onsaveName={handleSaveName}
              />
            {/each}
          </div>
        {/if}
      {:else}
        <!-- Archived Tab -->
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
                                    </button>
                                    <button
                                      class="btn btn-ghost px-2 py-1.5 text-sm"
                                      onclick={() => downloadRecording(rec)}
                                      title={t('archives.download')}
                                    >
                                      <Download size={16} />
                                    </button>
                                    <button
                                      class="btn btn-ghost px-2 py-1.5 text-sm th-color-danger"
                                      onclick={() => deleteRecordingConfirm = rec}
                                      title={t('archives.delete')}
                                    >
                                      <Trash2 size={16} />
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
      recordingCount={0}
      totalSize="N/A"
      loading={archiveLoading}
      onconfirm={async () => {
        archiveLoading = true;
        try {
          await deleteCamera(archiveConfirm!.id);
          showToast(t('cameras.cameraArchived'), 'success');
          archiveConfirm = null;
          await loadCameras();
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

  <!-- Onboarding overlay for first-time users -->
  {#if showOnboarding && cameras.length === 0}
    <OnboardingOverlay
      onaddcamera={() => { showOnboarding = false; openAddForm(); }}
      oncomplete={() => { showOnboarding = false; window.location.hash = '#/recordings'; }}
      onskip={() => { showOnboarding = false; }}
    />
  {/if}
</div>
