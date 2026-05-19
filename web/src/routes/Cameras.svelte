<script lang="ts">
  import { onMount } from 'svelte';
  import { listCameras, deleteCamera, startCamera, stopCamera, updateCamera, xiaomiSync, xiaomiDevices, listProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, ApiRequestError, enableCamera, disableCamera, listArchives, setArchiveRetention, deleteArchiveGroup } from '$lib/api';
  import type { Camera, XiaomiDevice, ProtocolInfo, ArchiveGroup } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatFileSize } from '$lib/format';
  import { AlertCircle, Camera as CameraIcon, RefreshCw, Plus, Archive as ArchiveIcon, Trash2, ExternalLink, Clock, HardDrive } from 'lucide-svelte';
  import DiscoveryPanel from '$lib/components/DiscoveryPanel.svelte';
  import CameraForm from '$lib/components/CameraForm.svelte';
  import CameraCard from '$lib/components/CameraCard.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import ArchiveConfirmDialog from '$lib/components/ArchiveConfirmDialog.svelte';
  import OnboardingOverlay from '$lib/components/OnboardingOverlay.svelte';
  import Tab from '$lib/components/Tab.svelte';

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let activeTab = $state('active');
  let archives = $state<ArchiveGroup[]>([]);
  let archiveConfirm = $state<Camera | null>(null);
  let confirmDeleteArchive = $state<string | null>(null);

  // Form state
  let showForm = $state(false);
  let editingCamera = $state<Camera | null>(null);

  // Confirmation dialog state
  let confirmAction = $state<{ camera: Camera; action: 'stop' | 'restart' } | null>(null);

  // Xiaomi
  let xiaomiDeviceList = $state<XiaomiDevice[]>([]);
  let syncing = $state(false);

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
    try {
      await deleteArchiveGroup(archiveId);
      showToast(t('cameras.archive.deleteAllSuccess'), 'success');
      confirmDeleteArchive = null;
      await loadArchives();
    } catch (e) {
      showToast(t('cameras.failedArchive'), 'error');
    }
  }

  async function loadCameras() {
    loading = true;
    error = '';
    try {
      cameras = await listCameras();
    } catch (e) {
      error = friendlyError(e, t('cameras.failedLoad'));
    } finally {
      loading = false;
      if (!loading && cameras.length === 0 && !sessionStorage.getItem('mibee_nvr_onboarding_dismissed')) {
        showOnboarding = true;
      }
    }
    loadArchives();
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

  async function handleSyncCloud() {
    syncing = true;
    try {
      const result = await xiaomiSync();
      showToast(t('cameras.syncedCameras').replace('{count}', String(result.synced)), 'success');
      await loadCameras();
    } catch (e: any) {
      showToast(e.message || t('cameras.syncFailed'), 'error');
    } finally {
      syncing = false;
    }
  }

  onMount(async () => {
    loadCameras();
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
        <button onclick={handleSyncCloud} class="btn btn-ghost" disabled={syncing}>
          <RefreshCw size={16} class={syncing ? 'animate-spin' : ''} />
          {t('cameras.syncCloud')}
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
          <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 mt-6">
            {#each archives as archive (archive.id)}
              <div class="card border th-border p-4 flex flex-col">
                <!-- Header: Name + Date -->
                <div class="flex items-start justify-between gap-2 mb-3">
                  <h4 class="font-medium th-text-primary">{archive.name}</h4>
                  <span class="text-xs th-text-tertiary whitespace-nowrap">
                    {t('cameras.archive.archivedAt', { date: new Date(archive.archived_at).toLocaleDateString() })}
                  </span>
                </div>

                <!-- Stats: Recordings + Size -->
                <div class="space-y-1.5 mb-3">
                  <div class="flex items-center gap-2 text-sm th-text-secondary">
                    <HardDrive size={14} class="th-text-tertiary" />
                    {t('cameras.archive.recordings', { count: archive.recording_count })}
                  </div>
                  <div class="flex items-center gap-2 text-sm th-text-secondary">
                    <Clock size={14} class="th-text-tertiary" />
                    {t('cameras.archive.size', { size: formatFileSize(archive.total_size) })}
                  </div>
                </div>

                <!-- Retention -->
                <div class="flex items-center gap-2 mb-3">
                  <label class="text-xs th-text-tertiary">{t('cameras.archive.retentionDays')}</label>
                  <input
                    type="number"
                    class="input py-0.5 px-2 text-sm w-20"
                    value={archive.archive_retention_days || 0}
                    min="0"
                    onchange={(e) => {
                      const val = parseInt((e.target as HTMLInputElement).value) || 0;
                      handleRetentionChange(archive.id, val);
                    }}
                  />
                  <span class="text-xs th-text-tertiary">{t('archives.retentionDays')}</span>
                </div>

                <!-- Actions -->
                <div class="flex items-center gap-2 mt-auto pt-3 border-t th-border">
                  <a
                    href="#/archives/{archive.id}"
                    class="btn btn-ghost px-2 py-1 text-sm flex items-center gap-1"
                  >
                    <ExternalLink size={14} />
                    {t('cameras.action.viewRecordings')}
                  </a>
                  <button
                    class="btn btn-ghost px-2 py-1 text-sm th-color-danger flex items-center gap-1 ml-auto"
                    onclick={() => confirmDeleteArchive = archive.id}
                  >
                    <Trash2 size={14} />
                    {t('cameras.action.deleteAll')}
                  </button>
                </div>
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
      onconfirm={async () => {
        try {
          await deleteCamera(archiveConfirm!.id);
          showToast(t('cameras.cameraArchived'), 'success');
          archiveConfirm = null;
          await loadCameras();
        } catch (e) {
          showToast(t('cameras.failedArchive'), 'error');
        }
      }}
      oncancel={() => archiveConfirm = null}
    />
  {/if}

  <!-- Archive Delete Confirm Dialog -->
  {#if confirmDeleteArchive}
    <ConfirmDialog
      title={t('cameras.action.deleteAll')}
      message={t('cameras.archive.deleteAllConfirm')}
      onconfirm={() => handleDeleteArchive(confirmDeleteArchive!)}
      oncancel={() => confirmDeleteArchive = null}
      variant="danger"
    />
  {/if}

  <!-- Onboarding overlay for first-time users -->
  {#if showOnboarding && cameras.length === 0}
    <OnboardingOverlay
      onaddcamera={openAddForm}
      oncomplete={() => { showOnboarding = false; window.location.hash = '#/recordings'; }}
      onskip={() => { showOnboarding = false; }}
    />
  {/if}
</div>
