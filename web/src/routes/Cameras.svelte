<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { listCameras, createCamera, updateCamera, deleteCamera, discoverONVIFDevices, getMergeConfig, updateMergeConfig, deleteCameraMergeConfig, getONVIFDeviceDetail } from '$lib/api';
  import type { Camera, CreateCameraRequest, UpdateCameraRequest, DiscoveredDevice, DeviceProfile, MergeConfig } from '$lib/api';
  import { t } from '$lib/i18n';
  import { Eye, EyeOff, Pencil, Camera as CameraIcon, AlertCircle } from 'lucide-svelte';
  import { showToast } from '$lib/toast';

  function formatTimeAgo(lastSeen: string | null | undefined): { text: string; color: string } {
    if (!lastSeen) return { text: t('cameras.neverRecorded'), color: 'badge-neutral' };
    const now = Date.now();
    const then = new Date(lastSeen).getTime();
    if (isNaN(then)) return { text: t('cameras.neverRecorded'), color: 'badge-neutral' };
    const diffMs = now - then;
    const diffMin = Math.floor(diffMs / 60000);
    if (diffMin < 5) {
      return { text: t('cameras.active') + ' ' + diffMin + t('cameras.minutesAgo'), color: 'badge-success' };
    } else if (diffMin < 30) {
      return { text: diffMin + t('cameras.minutesAgo'), color: 'badge-warning' };
    } else {
      const diffHours = Math.floor(diffMin / 60);
      if (diffHours < 1) {
        return { text: diffMin + t('cameras.minutesAgo'), color: 'badge-error' };
      }
      return { text: diffHours + t('cameras.hoursAgo'), color: 'badge-error' };
    }
  }

  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let feedback = $state('');
  let feedbackType = $state<'success' | 'error'>('success');

  // Form state
  let showForm = $state(false);
  let editingCamera = $state<Camera | null>(null);
  let formName = $state('');
  let formProtocol = $state('rtsp');
  let formEncoding = $state('h264');
  let formUrl = $state('');
  let formUsername = $state('');
  let formPassword = $state('');
  let showPassword = $state(false);
  let formEnabled = $state(true);
  let saving = $state(false);
  let formDescription = $state('');
  let formLocation = $state('');
  let formBrand = $state('');
  let formModel = $state('');
  let formSerialNumber = $state('');
  let formRetentionDays = $state(0);
  let formStreamEncoding = $state('');  // For ONVIF cameras: '' = auto, 'H264', 'H265'

  $effect(() => {
    // When protocol changes, auto-select appropriate encoding
    if (formProtocol === 'http') {
      formEncoding = 'jpeg';
    } else if (formProtocol === 'onvif') {
      formEncoding = '';
    } else if (formProtocol === 'rtsp' && (formEncoding === 'jpeg' || formEncoding === '')) {
      formEncoding = 'h264';
    }
  });

  // Inline name edit
  let editingNameId = $state<string | null>(null);
  let inlineName = $state('');
  let validationErrors = $state<Record<string, string>>({});

  // Delete confirmation
  let deletingCamera = $state<Camera | null>(null);

  // ONVIF discovery
  let scanning = $state(false);
  let scanDone = $state(false);
  let scanError = $state('');
  let discoveredDevices = $state<DiscoveredDevice[]>([]);
  let addingDeviceId = $state<string | null>(null);
  let probeIP = $state('');
  let selectedDevice = $state<DiscoveredDevice | null>(null);
  let deviceDetail = $state<{ device_info: any; profiles: DeviceProfile[] } | null>(null);
  let detailLoading = $state(false);
  let selectedProfileToken = $state('');
  let onvifUsername = $state('');
  let onvifPassword = $state('');

  // Merge config (per-camera)
  let mergeConfig = $state<MergeConfig | null>(null);
  let mergeConfigLoading = $state(false);


  function showFeedback(msg: string, type: 'success' | 'error') {
    showToast(msg, type);
  }

  function validateField(field: string, value: string) {
    if (field === 'name' && !value.trim()) {
      validationErrors['name'] = t('cameras.nameRequired');
    } else if (field === 'url' && !value.trim()) {
      validationErrors['url'] = t('cameras.urlRequired');
    } else {
      delete validationErrors[field];
    }
  }

  function validate(): boolean {
    validationErrors = {};
    if (!formName.trim()) validationErrors['name'] = t('cameras.nameRequired');
    if (!formProtocol) validationErrors['protocol'] = t('cameras.protocolRequired');
    if (!formUrl.trim()) validationErrors['url'] = t('cameras.urlRequired');
    return Object.keys(validationErrors).length === 0;
  }

  function resetForm() {
    showForm = false;
    editingCamera = null;
    formName = '';
    formProtocol = 'rtsp';
    formEncoding = 'h264';
    formUrl = '';
    formUsername = '';
    formPassword = '';
    showPassword = false;
    formEnabled = true;
    formDescription = '';
    formLocation = '';
    formBrand = '';
    formModel = '';
    formSerialNumber = '';
    formRetentionDays = 0;
    validationErrors = {};
    mergeConfig = null;
    mergeConfigLoading = false;
  }

  function openAddForm() {
    resetForm();
    showForm = true;
  }

  async function openEditForm(camera: Camera) {
    editingCamera = camera;
    formName = camera.name;
    formProtocol = camera.protocol;
    formEncoding = camera.encoding || '';
    // Handle legacy combined protocols from existing cameras
    if (camera.protocol === 'rtsp_h264') { formProtocol = 'rtsp'; formEncoding = 'h264'; }
    else if (camera.protocol === 'rtsp_h265') { formProtocol = 'rtsp'; formEncoding = 'h265'; }
    else if (camera.protocol === 'rtsp_mjpeg') { formProtocol = 'rtsp'; formEncoding = 'mjpeg'; }
    else if (camera.protocol === 'http_jpeg') { formProtocol = 'http'; formEncoding = 'jpeg'; }
    formUrl = camera.url || '';
    formUsername = camera.username || '';
    formPassword = '';
    showPassword = false;
    formEnabled = camera.enabled;
    formDescription = camera.description || '';
    formLocation = camera.location || '';
    formBrand = camera.brand || '';
    formModel = camera.model || '';
    formSerialNumber = camera.serial_number || '';
    formRetentionDays = camera.retention_days || 0;
    formStreamEncoding = (camera as any).stream_encoding || '';
    validationErrors = {};
    showForm = true;

    // Load per-camera merge config
    mergeConfig = null;
    mergeConfigLoading = true;
    try {
      mergeConfig = await getMergeConfig(camera.id);
    } catch {
      mergeConfig = null;
    } finally {
      mergeConfigLoading = false;
    }
  }

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

  async function handleSubmit() {
    if (!validate()) return;
    saving = true;

    try {
      if (editingCamera) {
        const data: UpdateCameraRequest = {
          name: formName,
          protocol: formProtocol,
          url: formUrl,
          enabled: formEnabled,
          description: formDescription || undefined,
          location: formLocation || undefined,
          brand: formBrand || undefined,
          model: formModel || undefined,
          serial_number: formSerialNumber || undefined,
          retention_days: formRetentionDays,
          stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
          encoding: formEncoding
        };
        // Only send username if changed from original
        if (formUsername && formUsername !== editingCamera.username) {
          data.username = formUsername;
        }
        // Only send password if user typed a new one
        if (formPassword) {
          if (!data.username && formUsername === editingCamera.username) {
            data.username = formUsername;
          }
          data.password = formPassword;
        }

        // Save per-camera merge config if editing
        if (mergeConfig) {
          try {
            await updateMergeConfig(editingCamera.id, mergeConfig);
          } catch { /* ignore merge config save errors */ }
        }
        await updateCamera(editingCamera.id, data);
        showFeedback(t('cameras.cameraUpdated'), 'success');
      } else {
        const data: CreateCameraRequest = {
          name: formName,
          protocol: formProtocol,
          url: formUrl,
          enabled: formEnabled,
          description: formDescription || undefined,
          location: formLocation || undefined,
          brand: formBrand || undefined,
          model: formModel || undefined,
          serial_number: formSerialNumber || undefined,
          stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
          encoding: formEncoding
        };
        if (formUsername) data.username = formUsername;
        if (formPassword) data.password = formPassword;
        await createCamera(data);
        showFeedback(t('cameras.cameraAdded'), 'success');
      }
      resetForm();
      await loadCameras();
    } catch (e) {
      showFeedback(
        editingCamera ? t('cameras.failedUpdate') : t('cameras.failedAdd'),
        'error'
      );
    } finally {
      saving = false;
    }
  }

  async function handleDelete() {
    if (!deletingCamera) return;
    try {
      await deleteCamera(deletingCamera.id);
      showFeedback(t('cameras.cameraDeleted'), 'success');
      deletingCamera = null;
      await loadCameras();
    } catch (e) {
      showFeedback(t('cameras.failedDelete'), 'error');
      deletingCamera = null;
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
    } catch (e) {
      showToast(t('cameras.failedUpdate'), 'error');
    }
    editingNameId = null;
  }

  function cancelInlineEdit() {
    editingNameId = null;
  }

  async function scanONVIF() {
    scanning = true;
    scanError = '';
    discoveredDevices = [];
    scanDone = false;
    try {
      const results = await discoverONVIFDevices(5);
      // Filter out devices that are already added as ONVIF cameras
      const existingEndpoints = new Set(
        cameras.filter(c => c.protocol === 'onvif' && c.url).map(c => c.url)
      );
      discoveredDevices = results.filter(d => {
        const ep = d.endpoint || (d.xaddrs.length > 0 ? d.xaddrs[0] : '');
        return !existingEndpoints.has(ep);
      });
    } catch (e) {
      scanError = e instanceof Error ? e.message : String(e);
    } finally {
      scanning = false;
      scanDone = true;
    }
  }

  async function addDiscoveredDevice(device: DiscoveredDevice) {
    addingDeviceId = device.uuid;
    try {
      await createCamera({
        name: device.name || t('onvif.deviceName'),
        protocol: 'onvif',
        url: device.endpoint || (device.xaddrs.length > 0 ? device.xaddrs[0] : ''),
        enabled: true,
        username: onvifUsername || undefined,
        password: onvifPassword || undefined,
      });
      showToast(t('cameras.cameraAdded'), 'success');
      discoveredDevices = discoveredDevices.filter(d => d.uuid !== device.uuid);
      await loadCameras();
    } catch (e) {
      showToast(t('cameras.failedAdd'), 'error');
    } finally {
      addingDeviceId = null;
    }
  }

  async function viewDeviceDetail(device: DiscoveredDevice) {
    selectedDevice = device;
    detailLoading = true;
    deviceDetail = null;
    try {
      // Extract IP from endpoint or xaddrs
      const url = new URL(device.endpoint || device.xaddrs[0]);
      const ip = url.hostname;
      deviceDetail = await getONVIFDeviceDetail(ip);
      if (deviceDetail?.profiles?.length) {
        selectedProfileToken = deviceDetail.profiles[0].token;
      }
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('onvif.failedGetDetails'), 'error');
    } finally {
      detailLoading = false;
    }
  }

  onMount(() => {
    loadCameras();
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="flex items-center justify-between mb-6">
      <h2 class="text-2xl font-bold th-text-primary">{t('cameras.title')}</h2>
      <div class="flex gap-3">
        <button on:click={scanONVIF} class="btn btn-ghost" disabled={scanning}>
          {#if scanning}
            <span class="spinner mr-2"></span>{t('onvif.discovering')}
          {:else}
            {t('onvif.discover')}
          {/if}
        </button>
        <button on:click={openAddForm} class="btn btn-primary">
          + {t('cameras.addCamera')}
        </button>
      </div>
    </div>

    <!-- Feedback -->
    {#if feedback}
      <div class="mb-4 p-3 rounded-md border {feedbackType === 'success'
        ? 'bg-[rgba(16,185,129,0.3)] border-[rgba(16,185,129,0.3)] th-color-success'
        : 'bg-[rgba(239,68,68,0.3)] th-border-danger th-color-danger'}">
        {feedback}
      </div>
    {/if}

    <!-- Error -->
    {#if error}
      <div class="card border th-border-danger p-8 text-center">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button on:click={loadCameras} class="btn btn-primary btn-sm">{t('common.retry')}</button>
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

        <!-- ONVIF Discovery Panel -->
        {#if scanning || scanDone}
          <div class="card p-6 border th-border">
            <h3 class="text-lg font-semibold th-text-primary mb-4">
              {t('onvif.discover')}
            </h3>
            <!-- ONVIF Credentials -->
            <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-4 items-end">
              <div>
                <label class="input-label text-xs">{t('onvif.username')}</label>
                <input type="text" class="input py-1 text-sm" bind:value={onvifUsername} placeholder="admin" />
              </div>
              <div>
                <label class="input-label text-xs">{t('onvif.password')}</label>
                <input type="password" class="input py-1 text-sm" bind:value={onvifPassword} placeholder="******" />
              </div>
              <div class="flex items-center">
                <span class="text-xs th-text-muted">{t('onvif.credentialsHint')}</span>
              </div>
            </div>
            {#if scanning}
              <div class="flex items-center gap-3 th-text-secondary py-4">
                <span class="spinner"></span>
                <span>{t('onvif.discovering')}</span>
              </div>
            {:else if scanError}
              <div class="th-color-danger text-sm py-2">{scanError}</div>
            {:else if discoveredDevices.length === 0}
              <p class="th-text-secondary text-sm py-2">{t('onvif.noDevices')}</p>
            {:else}
              <div class="space-y-3">
                {#each discoveredDevices as device (device.uuid)}
                  <div class="flex items-center justify-between p-4 rounded-md th-bg-hover border th-border">
                    <div class="min-w-0 flex-1 mr-4">
                      <div class="font-medium th-text-primary truncate">{device.name || t('onvif.deviceName')}</div>
                      <div class="text-sm th-text-secondary truncate">{device.endpoint}</div>
                      {#if device.hardware}
                        <div class="text-xs th-text-muted mt-0.5">{device.hardware}</div>
                      {/if}
                    </div>
                    <button
                      on:click={() => addDiscoveredDevice(device)}
                      class="btn btn-primary btn-sm shrink-0"
                      disabled={addingDeviceId === device.uuid}
                    >
                      {#if addingDeviceId === device.uuid}
                        <span class="spinner mr-1"></span>
                      {/if}
                      {t('onvif.addCamera')}
                    </button>
                  </div>
                {/each}
              </div>
            {/if}
            {#if !scanning && scanDone}
              <div class="mt-4 flex justify-end">
                <button on:click={scanONVIF} class="btn btn-ghost btn-sm">
                  {t('onvif.discover')}
                </button>
              </div>
            {/if}
          </div>
        {/if}

        <!-- Add/Edit Form -->
        {#if showForm}
          <div class="card p-6 border th-border">
            <h3 class="text-lg font-semibold th-text-primary mb-4">
              {editingCamera ? t('cameras.editCamera') : t('cameras.addCamera')}
            </h3>

            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
              <!-- Name -->
              <div>
                <label for="cam-name" class="input-label">{t('cameras.name')}</label>
                <input id="cam-name" type="text" class="input {validationErrors['name'] ? 'border-red-500' : ''}" bind:value={formName} on:blur={() => validateField('name', formName)} on:input={() => { if (validationErrors['name']) delete validationErrors['name']; }} />
                {#if validationErrors['name']}
                  <p class="th-color-danger text-xs mt-1">{validationErrors['name']}</p>
                {/if}
              </div>

              <!-- Protocol -->
              <div>
                <label for="cam-protocol" class="input-label">{t('cameras.protocol')}</label>
                <select id="cam-protocol" class="input" bind:value={formProtocol}>
                  <option value="rtsp">RTSP</option>
                  <option value="http">HTTP</option>
                  <option value="onvif">ONVIF</option>
                </select>
                {#if validationErrors['protocol']}
                  <p class="th-color-danger text-xs mt-1">{validationErrors['protocol']}</p>
                {/if}
              </div>

              <!-- Encoding -->
              <div>
                <label for="cam-encoding" class="input-label">{t('cameras.tableEncoding')}</label>
                <select id="cam-encoding" class="input" bind:value={formEncoding}>
                  {#if formProtocol === 'rtsp'}
                    <option value="h264">H.264</option>
                    <option value="h265">H.265</option>
                    <option value="mjpeg">MJPEG</option>
                  {:else if formProtocol === 'http'}
                    <option value="jpeg">JPEG</option>
                  {:else if formProtocol === 'onvif'}
                    <option value="">{t('cameras.autoDetect')}</option>
                    <option value="h264">H.264</option>
                    <option value="h265">H.265</option>
                  {/if}
                </select>
              </div>

              <!-- URL -->
              <div class="md:col-span-2">
                <label for="cam-url" class="input-label">
                  {t('cameras.url')}
                  {#if formProtocol === 'onvif'}
                    <span class="text-xs th-text-muted ml-1">({t('cameras.onvifEndpoint')})</span>
                  {/if}
                </label>
                <input id="cam-url" type="text" class="input {validationErrors['url'] ? 'border-red-500' : ''}" bind:value={formUrl}
                  placeholder={formProtocol === 'onvif' ? 'http://192.168.1.100:80/onvif/device_service' : 'rtsp://...'}
                  on:blur={() => validateField('url', formUrl)} on:input={() => { if (validationErrors['url']) delete validationErrors['url']; }} />
                {#if validationErrors['url']}
                  <p class="th-color-danger text-xs mt-1">{validationErrors['url']}</p>
                {/if}
              </div>

              <!-- Username -->
              <div>
                <label for="cam-user" class="input-label">{t('cameras.username')}</label>
                <input id="cam-user" type="text" class="input" bind:value={formUsername} placeholder={editingCamera ? (editingCamera.username || t('cameras.notSet')) : ''} />
              </div>

              <!-- Password -->
              <div>
                <label for="cam-pass" class="input-label">{t('cameras.password')}</label>
                <div class="relative">
                  <input
                    id="cam-pass"
                    type={showPassword ? 'text' : 'password'}
                    class="input pr-10"
                    bind:value={formPassword}
                    placeholder={editingCamera ? (editingCamera.has_password ? t('cameras.passwordSet') : t('cameras.notSet')) : ''}
                  />
                  <button
                    type="button"
                    class="absolute right-2 top-1/2 -translate-y-1/2 th-text-tertiary hover:th-text-primary transition-colors"
                    on:click={() => showPassword = !showPassword}
                    aria-label={showPassword ? t('common.hidePassword') : t('common.showPassword')}
                  >
                    {#if showPassword}
                      <EyeOff class="w-4 h-4" />
                    {:else}
                      <Eye class="w-4 h-4" />
                    {/if}
                  </button>
                </div>
              </div>

              <!-- Enabled -->
              <div class="md:col-span-2 flex items-center gap-2">
                <input id="cam-enabled" type="checkbox" class="accent-[var(--color-accent)]" bind:checked={formEnabled} />
                <label for="cam-enabled" class="th-text-secondary text-sm">{t('cameras.enabledToggle')}</label>
              </div>


              <!-- Description -->

              <!-- Description -->
              <div class="md:col-span-2">
                <label for="cam-desc" class="input-label">{t('cameras.description')}</label>
                <textarea id="cam-desc" class="input" rows="2" bind:value={formDescription} placeholder={t('cameras.descriptionPlaceholder')}></textarea>
              </div>

              <!-- Location -->
              <div>
                <label for="cam-location" class="input-label">{t('cameras.location')}</label>
                <input id="cam-location" type="text" class="input" bind:value={formLocation} placeholder={t('cameras.locationPlaceholder')} />
              </div>

              <!-- Brand -->
              <div>
                <label for="cam-brand" class="input-label">{t('cameras.brand')}</label>
                <input id="cam-brand" type="text" class="input" bind:value={formBrand} />
              </div>

              <!-- Model -->
              <div>
                <label for="cam-model" class="input-label">{t('cameras.model')}</label>
                <input id="cam-model" type="text" class="input" bind:value={formModel} />
              </div>

              <!-- Serial Number -->
              <div>
                <label for="cam-serial" class="input-label">{t('cameras.serialNumber')}</label>
                <input id="cam-serial" type="text" class="input" bind:value={formSerialNumber} />
              </div>

              <!-- Retention Days -->
              <div>
                <label for="cam-retention" class="input-label">{t('cameras.retentionDays')}</label>
                <input id="cam-retention" type="number" min="0" class="input" bind:value={formRetentionDays} />
                <p class="th-text-muted text-xs mt-1">{t('cameras.retentionDaysHint')}</p>
              </div>
            </div>

            <!-- Merge Config (edit mode only) -->
            {#if editingCamera}
              <details class="mt-6 border th-border rounded-lg"
                open={mergeConfig ? true : undefined}
              >
                <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
                  {t('merge.title')}
                  {#if mergeConfig}
                    <span class="text-xs th-text-muted ml-2">{t('merge.customized')}</span>
                  {:else}
                    <span class="text-xs th-text-muted ml-2">{t('merge.usingDefault')}</span>
                  {/if}
                </summary>

                <div class="px-4 pb-4 pt-2">
                  {#if mergeConfigLoading}
                    <div class="flex items-center gap-2 py-4 th-text-muted">
                      <span class="spinner"></span>
                      <span class="text-sm">{t('common.loading')}</span>
                    </div>
                  {:else}
                    <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
                      <!-- Enabled -->
                      <div class="flex items-center gap-2">
                        <input
                          id="merge-enabled"
                          type="checkbox"
                          class="accent-[var(--color-accent)]"
                          checked={mergeConfig?.enabled !== false}
                          on:change={(e) => {
                            if (!mergeConfig) mergeConfig = {};
                            mergeConfig.enabled = (e.target as HTMLInputElement).checked;
                          }}
                        />
                        <label for="merge-enabled" class="th-text-secondary text-sm">{t('merge.enableMerge')}</label>
                      </div>

                      <!-- Check Interval -->
                      <div>
                        <label for="merge-check-interval" class="input-label">{t('merge.checkInterval')}</label>
                        <select
                          id="merge-check-interval"
                          class="input"
                          value={mergeConfig?.check_interval || '1h'}
                          on:change={(e) => {
                            if (!mergeConfig) mergeConfig = {};
                            mergeConfig.check_interval = (e.target as HTMLSelectElement).value;
                          }}
                        >
                          <option value="30m">{t('merge.30m')}</option>
                          <option value="1h">{t('merge.1h')}</option>
                          <option value="2h">{t('merge.2h')}</option>
                          <option value="6h">{t('merge.6h')}</option>
                        </select>
                      </div>

                      <!-- Window Size -->
                      <div>
                        <label for="merge-window" class="input-label">{t('merge.windowSize')}</label>
                        <select
                          id="merge-window"
                          class="input"
                          value={mergeConfig?.window_size || '30m'}
                          on:change={(e) => {
                            if (!mergeConfig) mergeConfig = {};
                            mergeConfig.window_size = (e.target as HTMLSelectElement).value;
                          }}
                        >
                          <option value="30m">{t('merge.30m')}</option>
                          <option value="1h">{t('merge.1h')}</option>
                          <option value="2h">{t('merge.2h')}</option>
                        </select>
                      </div>

                      <!-- Batch Limit -->
                      <div>
                        <label for="merge-batch" class="input-label">{t('merge.batchLimit')}</label>
                        <input
                          id="merge-batch"
                          type="number"
                          class="input"
                          min="10"
                          max="1000"
                          value={mergeConfig?.batch_limit || 100}
                          on:input={(e) => {
                            if (!mergeConfig) mergeConfig = {};
                            mergeConfig.batch_limit = Number((e.target as HTMLInputElement).value);
                          }}
                        />
                      </div>

                      <!-- Min Segment Age -->
                      <div>
                        <label for="merge-age" class="input-label">{t('merge.minSegmentAge')}</label>
                        <select
                          id="merge-age"
                          class="input"
                          value={mergeConfig?.min_segment_age || '5m'}
                          on:change={(e) => {
                            if (!mergeConfig) mergeConfig = {};
                            mergeConfig.min_segment_age = (e.target as HTMLSelectElement).value;
                          }}
                        >
                          <option value="5m">{t('merge.5m')}</option>
                          <option value="10m">{t('merge.10m')}</option>
                          <option value="30m">{t('merge.30m')}</option>
                          <option value="1h">{t('merge.1h')}</option>
                        </select>
                      </div>

                      <!-- Min Segments to Merge -->
                      <div>
                        <label for="merge-min-segments" class="input-label">{t('merge.minSegmentsToMerge')}</label>
                        <input
                          id="merge-min-segments"
                          type="number"
                          class="input"
                          min="2"
                          max="50"
                          value={mergeConfig?.min_segments_to_merge || 3}
                          on:input={(e) => {
                            if (!mergeConfig) mergeConfig = {};
                            mergeConfig.min_segments_to_merge = Number((e.target as HTMLInputElement).value);
                          }}
                        />
                      </div>
                    </div>

                    <!-- Clear override button -->
                    <div class="mt-4 flex justify-end">
                      <button
                        type="button"
                        class="btn btn-ghost btn-sm"
                        on:click={async () => {
                          if (!editingCamera) return;
                          try {
                            await deleteCameraMergeConfig(editingCamera.id);
                            mergeConfig = null;
                            showToast(t('merge.restoredDefault'), 'success');
                          } catch {
                            showToast(t('merge.operationFailed'), 'error');
                          }
                        }}
                      >
                        {t('merge.useGlobalDefault')}
                      </button>
                    </div>
                  {/if}
                </div>
              </details>
            {/if}

            <div class="flex items-center gap-3 mt-6">
              <button on:click={handleSubmit} class="btn btn-primary" disabled={saving}>
                {#if saving}
                  <span class="spinner mr-2"></span>
                {/if}
                {t('cameras.save')}
              </button>
              <button on:click={resetForm} class="btn btn-ghost">
                {t('cameras.cancel')}
              </button>
            </div>
          </div>
        {/if}

        <!-- Delete Confirmation -->
        {#if deletingCamera}
          <div class="card p-6 border th-border-danger bg-[rgba(239,68,68,0.2)]">
            <h3 class="text-lg font-semibold th-color-danger mb-2">{t('cameras.deleteTitle')}</h3>
            <p class="th-text-secondary mb-4">
              {t('cameras.deleteMessage', { name: deletingCamera.name })}
            </p>
            <div class="flex items-center gap-3">
              <button on:click={handleDelete} class="px-4 py-2 th-bg-danger hover:th-bg-danger-light text-white rounded-md transition-colors">
                {t('cameras.deleteConfirm')}
              </button>
              <button on:click={() => deletingCamera = null} class="btn btn-ghost">
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
              <button on:click={openAddForm} class="btn btn-primary btn-sm">+ {t('cameras.addCamera')}</button>
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
                    <tr class="hover:th-bg-hover transition-colors">
                      <td class="px-6 py-4 whitespace-nowrap text-sm">
                        {#if editingNameId === camera.id}
                          <input
                            type="text"
                            class="input py-0.5 px-2 text-sm w-40"
                            bind:value={inlineName}
                            on:keydown={(e) => {
                              if (e.key === 'Enter') saveInlineEdit(camera);
                              if (e.key === 'Escape') cancelInlineEdit();
                            }}
                            on:blur={() => saveInlineEdit(camera)}
                            focus
                          />
                        {:else}
                          <button
                            class="font-medium th-text-primary hover:underline cursor-pointer flex items-center gap-1"
                            on:click={() => startInlineEdit(camera)}
                            title={t('cameras.editName')}
                          >
                            {camera.name}
                            <Pencil size={12} class="th-text-tertiary" />
                          </button>
                        {/if}
                      </td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm">
                        <span class="badge {formatTimeAgo(camera.last_seen).color}">{formatTimeAgo(camera.last_seen).text}</span>
                        {#if camera.status}
                          <div class="text-xs th-text-muted mt-0.5">{camera.status}</div>
                        {/if}
                      </td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm th-text-secondary">{t('cameras.protocol.' + camera.protocol) || camera.protocol}</td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm th-text-secondary">{camera.encoding ? (t('cameras.encoding.' + camera.encoding) || camera.encoding) : '-'}</td>
                      <td class="px-6 py-4 text-sm th-text-secondary max-w-xs truncate">{camera.url}</td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm">
                        <div class="flex gap-2 items-center">
                          {#if camera.protocol === 'rtsp' || camera.protocol === 'onvif' || camera.protocol === 'rtsp_h264' || camera.protocol === 'rtsp_h265'}
                            <a
                              href="#/live/{camera.id}"
                              class="btn btn-primary px-2 py-1 text-sm flex items-center gap-1"
                              title={t('cameras.live')}
                            >
                              <Eye size={14} />
                              {t('cameras.live')}
                            </a>
                          {/if}
                          <button
                            on:click={() => openEditForm(camera)}
                            class="btn btn-ghost px-2 py-1 text-sm transition-all duration-200"
                          >{t('cameras.edit')}</button>
                          <button
                            on:click={() => deletingCamera = camera}
                            class="btn btn-ghost px-2 py-1 text-sm th-color-danger transition-all duration-200"
                          >{t('cameras.delete')}</button>
                        </div>
                      </td>
                    </tr>
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
