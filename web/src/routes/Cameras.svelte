<script lang="ts">
  import { onMount } from 'svelte';
  import { listCameras, deleteCamera, startCamera, stopCamera, updateCamera, xiaomiSync, xiaomiDevices, listProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap } from '$lib/api';
  import type { Camera, XiaomiDevice, ProtocolInfo } from '$lib/api';
  import { t } from '$lib/i18n';
  import { AlertCircle, Camera as CameraIcon, RefreshCw } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import DiscoveryPanel from '$lib/components/DiscoveryPanel.svelte';
  import CameraForm from '$lib/components/CameraForm.svelte';
  import CameraCard from '$lib/components/CameraCard.svelte';

  function formatTimeAgo(lastSeen: string | null | undefined): { text: string; color: string } {
    if (!lastSeen) return { text: t('cameras.neverRecorded'), color: 'badge-neutral' };
    const diffMin = Math.floor((Date.now() - new Date(lastSeen).getTime()) / 60000);
    if (isNaN(diffMin)) return { text: t('cameras.neverRecorded'), color: 'badge-neutral' };
    if (diffMin < 5) return { text: t('cameras.active') + ' ' + diffMin + t('cameras.minutesAgo'), color: 'badge-success' };
    if (diffMin < 30) return { text: diffMin + t('cameras.minutesAgo'), color: 'badge-warning' };
    const h = Math.floor(diffMin / 60);
    return { text: (h < 1 ? diffMin + t('cameras.minutesAgo') : h + t('cameras.hoursAgo')), color: 'badge-error' };
  }

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');

  // Form state
  let showForm = $state(false);
  let editingCamera = $state<Camera | null>(null);

  // Inline name edit
  let editingNameId = $state<string | null>(null);
  let inlineName = $state('');

  // Delete confirmation
  let deletingCamera = $state<Camera | null>(null);

  // Xiaomi (minimal — only device list for form display + sync)
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

  async function loadCameras() {
    loading = true;
    error = '';
    try {
      cameras = await listCameras();
    } catch (e) {
      error = e instanceof Error ? e.message : t('cameras.failedLoad');
    } finally {
      loading = false;
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
    loadCameras();
  }

  function handleFormCancel() {
    showForm = false;
    editingCamera = null;
  }

  async function handleDelete() {
    if (!deletingCamera) return;
    try {
      await deleteCamera(deletingCamera.id);
      showToast(t('cameras.cameraDeleted'), 'success');
      deletingCamera = null;
      await loadCameras();
    } catch {
      showToast(t('cameras.failedDelete'), 'error');
      deletingCamera = null;
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
    try {
      await stopCamera(camera.id);
      showToast(t('cameras.stopped'), 'success');
      await loadCameras();
    } catch (e: any) {
      showToast(e.message || t('cameras.failedStop'), 'error');
    }
  }

  async function handleRestartCamera(camera: Camera) {
    try {
      await stopCamera(camera.id);
      await startCamera(camera.id);
      showToast(t('cameras.cameraUpdated'), 'success');
      await loadCameras();
    } catch (e: any) {
      showToast(e.message || t('cameras.failedStart'), 'error');
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

  function startInlineEdit(camera: Camera) {
    editingNameId = camera.id;
    inlineName = camera.name;
  }

  async function saveInlineEdit(camera: Camera) {
    if (!inlineName.trim()) { editingNameId = null; return; }
    try {
      await updateCamera(camera.id, { name: inlineName.trim() });
      camera.name = inlineName.trim();
      showToast(t('cameras.nameUpdated'), 'success');
    } catch {
      showToast(t('cameras.failedUpdate'), 'error');
    }
    editingNameId = null;
  }

  function cancelInlineEdit() {
    editingNameId = null;
  }

  onMount(async () => {
    loadCameras();
    try {
      const list = await listProtocols();
      if (list && list.length > 0) {
        protocols = list;
        protocolsMap = buildProtocolsMap(list);
      }
    } catch {
      // Use DEFAULT_PROTOCOLS already initialized
    }
    // Probe xiaomi auth status for device list (used by CameraForm)
    try {
      const res = await xiaomiDevices();
      if (res.devices && res.devices.length > 0) {
        xiaomiDeviceList = res.devices;
      }
    } catch {
      // Not authenticated — DiscoveryPanel handles login
    }
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">

  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="flex items-center justify-between mb-6">
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

    <!-- Error -->
    {#if error}
      <div class="card border th-border-danger p-8 text-center">
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
      <div class="card border th-border">
        <div class="p-6 space-y-4">
          {#each Array(3) as _}
            <div class="flex gap-4 items-center">
              <div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 w-20 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 w-16 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 w-40 th-bg-tertiary rounded animate-pulse"></div>
            </div>
          {/each}
        </div>
      </div>
    {:else}
      <div class="space-y-6">

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

        <!-- Delete Confirmation -->
        {#if deletingCamera}
          <div class="card p-6 border th-border-danger bg-[rgba(239,68,68,0.2)]">
            <h3 class="text-lg font-semibold th-color-danger mb-2">{t('cameras.deleteTitle')}</h3>
            <p class="th-text-secondary mb-4">
              {t('cameras.deleteMessage', { name: deletingCamera.name })}
            </p>
            <div class="flex items-center gap-3">
              <button onclick={handleDelete} class="px-4 py-2 th-bg-danger hover:th-bg-danger-light text-white rounded-md transition-colors">
                {t('cameras.deleteConfirm')}
              </button>
              <button onclick={() => deletingCamera = null} class="btn btn-ghost">
                {t('cameras.cancel')}
              </button>
            </div>
          </div>
        {/if}

        <!-- Camera Table -->
        <div class="card border th-border overflow-hidden">
          {#if cameras.length === 0}
            <div class="p-12 text-center">
              <div class="flex justify-center mb-4 th-text-muted">
                <CameraIcon size={48} />
              </div>
              <h3 class="text-lg font-medium th-text-primary mb-2">{t('cameras.noCameras')}</h3>
              <p class="text-sm th-text-muted mb-4">{t('cameras.noCamerasHint')}</p>
              <button onclick={openAddForm} class="btn btn-primary btn-sm">+ {t('cameras.addCamera')}</button>
            </div>
          {:else}
            <div class="overflow-x-auto">
              <table class="min-w-full divide-y divide-[var(--border)]">
                <thead>
                  <tr>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableName')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableStatus')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableProtocol')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableEncoding')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableUrl')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableActions')}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-[var(--border)]">
                  {#each cameras as camera (camera.id)}
                    <CameraCard
                      {camera}
                      {protocolsMap}
                      isEditingName={editingNameId === camera.id}
                      {inlineName}
                      {formatTimeAgo}
                      onstart={handleStartCamera}
                      onstop={handleStopCamera}
                      onrestart={handleRestartCamera}
                      onedit={openEditForm}
                      ondelete={(c) => deletingCamera = c}
                      onsavename={saveInlineEdit}
                      onstarteditname={startInlineEdit}
                      oncanceleditname={cancelInlineEdit}
                      onnamedinput={(v) => inlineName = v}
                    />
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        </div>

      </div>
    {/if}
  </main>
</div>
