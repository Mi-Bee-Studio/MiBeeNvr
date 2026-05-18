<script lang="ts">
  import { t } from '$lib/i18n';
  import {
    createCamera,
    updateCamera,
    getMergeConfig,
    updateMergeConfig,
    buildProtocolsMap,
    normalizeProtocol,
  } from '$lib/api';
  import type {
    Camera,
    CreateCameraRequest,
    UpdateCameraRequest,
    MergeConfig,
    ProtocolInfo,
    XiaomiDevice,
  } from '$lib/api';
  import { Eye, EyeOff } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import MergeConfigEditor from '$lib/components/MergeConfigEditor.svelte';

  interface Props {
    editingCamera: Camera | null;
    protocols: ProtocolInfo[];
    protocolsMap: Map<string, ProtocolInfo>;
    xiaomiDeviceList?: XiaomiDevice[];
    onsave: () => void;
    oncancel: () => void;
  }

  let {
    editingCamera,
    protocols,
    protocolsMap,
    xiaomiDeviceList = [],
    onsave,
    oncancel,
  }: Props = $props();

  // Form state
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
  let formStreamEncoding = $state('');
  let validationErrors = $state<Record<string, string>>({});

  // Merge config
  let mergeConfig = $state<MergeConfig | null>(null);
  let mergeConfigLoading = $state(false);

  // Auto-select encoding when protocol changes
  $effect(() => {
    const proto = protocolsMap.get(formProtocol);
    if (!proto) return;
    const encodings = proto.encodings;
    if (!encodings.includes(formEncoding)) {
      if (formProtocol === 'onvif') {
        formEncoding = '';
      } else if (formProtocol === 'http') {
        formEncoding = 'jpeg';
      } else if (encodings.length > 0) {
        formEncoding = encodings[0];
      } else {
        formEncoding = '';
      }
    }
  });

  // Populate form when editingCamera changes
  $effect(() => {
    if (editingCamera) {
      populateForm(editingCamera);
      loadMergeConfig(editingCamera.id);
    } else {
      resetFormFields();
      mergeConfig = null;
      mergeConfigLoading = false;
    }
  });

  function resetFormFields() {
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
    formStreamEncoding = '';
    validationErrors = {};
  }

  function populateForm(camera: Camera) {
    formName = camera.name;
    formProtocol = camera.protocol;
    formEncoding = camera.encoding || '';
    // Handle legacy combined protocols
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
    formStreamEncoding = camera.stream_encoding || '';
    validationErrors = {};
  }

  async function loadMergeConfig(cameraId: string) {
    mergeConfig = null;
    mergeConfigLoading = true;
    try {
      mergeConfig = await getMergeConfig(cameraId);
    } catch {
      mergeConfig = null;
    } finally {
      mergeConfigLoading = false;
    }
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
        if (formUsername && formUsername !== editingCamera.username) {
          data.username = formUsername;
        }
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
        showToast(t('cameras.cameraUpdated'), 'success');
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
        showToast(t('cameras.cameraAdded'), 'success');
      }
      onsave();
    } catch {
      showToast(
        editingCamera ? t('cameras.failedUpdate') : t('cameras.failedAdd'),
        'error'
      );
    } finally {
      saving = false;
    }
  }
</script>

<div class="card p-6 border th-border">
  <h3 class="text-lg font-semibold th-text-primary mb-4">
    {editingCamera ? t('cameras.editCamera') : t('cameras.addCamera')}
  </h3>

  <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
    <!-- Name -->
    <div>
      <label for="cam-name" class="input-label">{t('cameras.name')}</label>
      <input id="cam-name" type="text" class="input {validationErrors['name'] ? 'border-red-500' : ''}" bind:value={formName} onblur={() => validateField('name', formName)} oninput={() => { if (validationErrors['name']) delete validationErrors['name']; }} />
      {#if validationErrors['name']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['name']}</p>
      {/if}
    </div>

    <!-- Protocol -->
    <div>
      <label for="cam-protocol" class="input-label">{t('cameras.protocol')}</label>
      <select id="cam-protocol" class="input" bind:value={formProtocol}>
        {#each protocols as proto (proto.id)}
          <option value={proto.id}>{proto.label}</option>
        {/each}
      </select>
      {#if validationErrors['protocol']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['protocol']}</p>
      {/if}
    </div>

    <!-- Encoding -->
    <div>
      <label for="cam-encoding" class="input-label">{t('cameras.tableEncoding')}</label>
      <select id="cam-encoding" class="input" bind:value={formEncoding}>
        {#if formProtocol === 'onvif'}
          <option value="">{t('cameras.autoDetect')}</option>
        {/if}
        {#each (protocolsMap.get(formProtocol)?.encodings || [formEncoding]) as enc}
          <option value={enc}>{t('cameras.encoding.' + enc) || enc.toUpperCase()}</option>
        {/each}
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
        placeholder={formProtocol === 'xiaomi' ? 'xiaomi://device_id' : formProtocol === 'onvif' ? 'http://192.168.1.100:80/onvif/device_service' : 'rtsp://...'}
        onblur={() => validateField('url', formUrl)} oninput={() => { if (validationErrors['url']) delete validationErrors['url']; }} />
      {#if validationErrors['url']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['url']}</p>
      {/if}
    </div>

    {#if formProtocol === 'xiaomi'}
      {#if editingCamera?.protocol === 'xiaomi' && xiaomiDeviceList.length > 0}
        {@const matchDid = formUrl.replace('xiaomi://', '')}
        {@const matchedDevice = xiaomiDeviceList.find(d => d.did === matchDid)}
        {#if matchedDevice}
          <div class="p-3 rounded-md th-bg-hover border th-border text-sm">
            <div class="font-medium th-text-primary">{matchedDevice.name}</div>
            <div class="th-text-secondary">{matchedDevice.model} · {matchedDevice.localip}</div>
            <div class="{matchedDevice.isOnline ? 'th-color-success' : 'th-text-muted'}">
              {matchedDevice.isOnline ? t('xiaomi.online') : t('xiaomi.offline')}
            </div>
          </div>
        {/if}
      {/if}
    {/if}

    {#if protocolsMap.get(formProtocol)?.capabilities?.auth}
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
            onclick={() => showPassword = !showPassword}
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
    {:else if protocolsMap.get(formProtocol)}
      <div class="md:col-span-2 text-sm th-text-secondary">
        {t('cameras.authManagedExternally')}
      </div>
    {/if}

    <!-- Enabled -->
    <div class="md:col-span-2 flex items-center gap-2">
      <input id="cam-enabled" type="checkbox" class="accent-[var(--color-accent)]" bind:checked={formEnabled} />
      <label for="cam-enabled" class="th-text-secondary text-sm">{t('cameras.enabledToggle')}</label>
    </div>

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
    <MergeConfigEditor
      cameraId={editingCamera.id}
      {mergeConfig}
      {mergeConfigLoading}
      onchange={(config) => mergeConfig = config}
      ondelete={() => mergeConfig = null}
    />
  {/if}

  <div class="flex items-center gap-3 mt-6">
    <button onclick={handleSubmit} class="btn btn-primary" disabled={saving}>
      {#if saving}
        <span class="spinner mr-2"></span>
      {/if}
      {t('cameras.save')}
    </button>
    <button onclick={oncancel} class="btn btn-ghost">
      {t('cameras.cancel')}
    </button>
  </div>
</div>
