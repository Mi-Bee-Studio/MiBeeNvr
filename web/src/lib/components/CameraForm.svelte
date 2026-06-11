<script lang="ts">
    import { t } from '$lib/i18n';
    import {
        createCamera,
        updateCamera,
        getMergeConfig,
        updateMergeConfig,
        buildProtocolsMap,
        normalizeProtocol,
        testConnection,
        getDeviceCapabilities,
    } from '$lib/api';
    import type {
        Camera,
        CameraTranscodingConfig,
        CreateCameraRequest,
        UpdateCameraRequest,
        MergeConfig,
        ProtocolInfo,
        XiaomiDevice,
        TestConnectionResult,
        DeviceCapabilitiesInfo,
    } from '$lib/api';
    import { Eye, EyeOff, PlugZap } from 'lucide-svelte';
    import { showToast } from '$lib/toast';
    import MergeConfigEditor from '$lib/components/MergeConfigEditor.svelte';
    import TimelapseConfigEditor from '$lib/components/TimelapseConfigEditor.svelte';
    import DeviceCapabilities from '$lib/components/DeviceCapabilities.svelte';
    import ImagingPanel from '$lib/components/ImagingPanel.svelte';
    import PresetManager from '$lib/components/PresetManager.svelte';
    import ONVIFEvents from '$lib/components/ONVIFEvents.svelte';
    import DeviceManagement from '$lib/components/DeviceManagement.svelte';
    import { startBackfill, getUntranscodedRecordingCount } from '$lib/api/transcoding';
  interface Props {
    editingCamera: Camera | null;
    protocols: ProtocolInfo[];
    protocolsMap: Map<string, ProtocolInfo>;
    xiaomiDeviceList?: XiaomiDevice[];
    onsave: () => void;
    oncancel: () => void;
    globalTranscodingEnabled?: boolean;
    h265Available?: boolean;
    onbackfillneeded?: (info: { cameraId: string; count: number; targetCodec: string }) => Promise<boolean>;
  }

  let {
    editingCamera,
    protocols,
    protocolsMap,
    xiaomiDeviceList = [],
    globalTranscodingEnabled = false,
    h265Available = true,
    onsave,
    oncancel,
    onbackfillneeded,
  }: Props = $props();

  // Form state
  let formName = $state('');
  let formProtocol = $state('rtsp');
  let formEncoding = $state('h264');
  let formUrl = $state('');
  let formUsername = $state('');
  let formPassword = $state('');
  let showPassword = $state(false);
  let saving = $state(false);
  let formDescription = $state('');
  let formLocation = $state('');
  let formBrand = $state('');
  let formModel = $state('');
  let formSerialNumber = $state('');
  let formRetentionDays = $state(0);
  let formStreamEncoding = $state('');
  // Transcoding config
  let formTranscodingEnabled = $state(false);
  let formTranscodingCodec = $state('h264');
  let formTranscodingPreset = $state('ultrafast');
let formTranscodingBitrate = $state('2M');
let validationErrors = $state<Record<string, string>>({});

  // Test connection state
  let testing = $state(false);
  let testResult = $state<TestConnectionResult | null>(null);

  // Merge config
  let mergeConfig = $state<MergeConfig | null>(null);
  let mergeConfigLoading = $state(false);

  // ONVIF capabilities
  let deviceCaps = $state<DeviceCapabilitiesInfo | null>(null);
  let capsLoading = $state(false);

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
      loadCapabilities(editingCamera);
    } else {
      resetFormFields();
      mergeConfig = null;
      mergeConfigLoading = false;
      deviceCaps = null;
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
    formDescription = '';
    formLocation = '';
    formBrand = '';
    formModel = '';
    formSerialNumber = '';
    formRetentionDays = 0;
    formStreamEncoding = '';
    formTranscodingEnabled = false;
    formTranscodingCodec = 'h264';
    formTranscodingPreset = 'ultrafast';
    formTranscodingBitrate = '2M';
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
    formDescription = camera.description || '';
    formLocation = camera.location || '';
    formBrand = camera.brand || '';
    formModel = camera.model || '';
    formSerialNumber = camera.serial_number || '';
    formRetentionDays = camera.retention_days || 0;
    formStreamEncoding = camera.stream_encoding || '';
    formTranscodingEnabled = camera.transcoding?.enabled ?? false;
    formTranscodingCodec = !h265Available ? 'h264' : (camera.transcoding?.target_codec || 'h264');
    formTranscodingPreset = camera.transcoding?.preset || 'ultrafast';
    formTranscodingBitrate = camera.transcoding?.bitrate || '2M';
    validationErrors = {};
  }

  async function loadMergeConfig(cameraId: string) {
    mergeConfig = null;
    mergeConfigLoading = true;
    try {
      mergeConfig = await getMergeConfig(cameraId);
    } catch (e) { console.warn('Failed to load merge config:', e); mergeConfig = null; } finally {
      mergeConfigLoading = false;
    }
  }

  async function loadCapabilities(cam: Camera) {
    if (normalizeProtocol(cam.protocol) !== 'onvif') {
      deviceCaps = null;
      return;
    }
    capsLoading = true;
    try {
      deviceCaps = await getDeviceCapabilities(cam.id);
    } catch (e) {
      console.warn('Failed to load device capabilities:', e);
      deviceCaps = null;
    } finally {
      capsLoading = false;
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
  async function handleTestConnection() {
    if (!formUrl.trim()) return;
    testing = true;
    testResult = null;
    try {
      testResult = await testConnection({
        protocol: formProtocol,
        url: formUrl,
        username: formUsername || undefined,
        password: formPassword || undefined,
        encoding: formEncoding || undefined,
        onvif_endpoint: formProtocol === 'onvif' ? formUrl : undefined,
      });
    } catch (e: any) {
      testResult = { success: false, message: e.message || t('cameras.testFailed', { error: '' }), latency_ms: 0 };
    } finally {
      testing = false;
    }
  }

async function handleSubmit() {
    if (!validate()) return;
    saving = true;

    // Check if transcoding is being newly enabled for an existing camera
    const isEnablingTranscoding = editingCamera && formTranscodingEnabled && !editingCamera.transcoding?.enabled;

    if (isEnablingTranscoding) {
        try {
            const countRes = await getUntranscodedRecordingCount(editingCamera.id);
            if (countRes.count > 0) {
                saving = false;
                const confirmed = await onbackfillneeded?.({
                    cameraId: editingCamera.id,
                    count: countRes.count,
                    targetCodec: formTranscodingCodec,
                }) ?? false;

                if (confirmed) {
                    // Save camera then start backfill
                    saving = true;
                    await performCameraSave();
                    const result = await startBackfill(editingCamera.id);
                    showToast(t('transcoding.backfill.success', { count: String(result.enqueued) }), 'success');
                    saving = false;
                    onsave();
                } else {
                    formTranscodingEnabled = false;
                }
                return;
            }
        } catch (e) {
            console.warn('Failed to check untranscoded recordings:', e);
            // Proceed with save anyway
        }
    }

    try {
        await performCameraSave();
        onsave();
    } catch (e) { console.warn('Failed to save camera:', e); showToast(
        editingCamera ? t('cameras.failedUpdate') : t('cameras.failedAdd'),
        'error'
    );
    } finally {
        saving = false;
    }
}

async function performCameraSave() {
    if (editingCamera) {
        const data: UpdateCameraRequest = {
            name: formName,
            protocol: formProtocol,
            url: formUrl,
            description: formDescription || undefined,
            location: formLocation || undefined,
            brand: formBrand || undefined,
            model: formModel || undefined,
            serial_number: formSerialNumber || undefined,
            retention_days: formRetentionDays,
            stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
            encoding: formEncoding,
            transcoding: {
                enabled: formTranscodingEnabled,
                target_codec: formTranscodingCodec,
                preset: formTranscodingPreset,
                bitrate: formTranscodingBitrate,
            },
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
            } catch (e) { console.warn('Failed to save merge config:', e); }
        }
        await updateCamera(editingCamera.id, data);
        showToast(t('cameras.cameraUpdated'), 'success');
    } else {
        const data: CreateCameraRequest = {
            name: formName,
            protocol: formProtocol,
            url: formUrl,
            description: formDescription || undefined,
            location: formLocation || undefined,
            brand: formBrand || undefined,
            model: formModel || undefined,
            serial_number: formSerialNumber || undefined,
            retention_days: formRetentionDays,
            stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
            encoding: formEncoding,
            transcoding: {
                enabled: formTranscodingEnabled,
                target_codec: formTranscodingCodec,
                preset: formTranscodingPreset,
                bitrate: formTranscodingBitrate,
            },
        };
        if (formUsername) data.username = formUsername;
        if (formPassword) data.password = formPassword;
        await createCamera(data);
        showToast(t('cameras.cameraAdded'), 'success');
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
      <div class="flex gap-2">
        <input id="cam-url" type="text" class="input flex-1 {validationErrors['url'] ? 'border-red-500' : ''}" bind:value={formUrl}
          placeholder={formProtocol === 'xiaomi' ? 'xiaomi://device_id' : formProtocol === 'onvif' ? 'http://192.168.1.100:80/onvif/device_service' : 'rtsp://...'}
          onblur={() => validateField('url', formUrl)} oninput={() => { if (validationErrors['url']) delete validationErrors['url']; testResult = null; }} />
        {#if formProtocol !== 'xiaomi'}
          <button
            type="button"
            onclick={handleTestConnection}
            disabled={testing || !formUrl.trim()}
            class="btn btn-ghost px-3 py-2 flex items-center gap-1.5 whitespace-nowrap"
            title={t('cameras.testConnection')}
          >
            <PlugZap size={14} />
            {#if testing}
              <span class="spinner mr-1"></span>{t('cameras.testing')}
            {:else}
              {t('cameras.testConnection')}
            {/if}
          </button>
        {/if}
      </div>
      {#if testResult}
        <p class="text-xs mt-1 {testResult.success ? 'th-color-success' : 'th-color-danger'}">
          {testResult.success
            ? t('cameras.testSuccess').replace('{latency}', String(testResult.latency_ms))
            : t('cameras.testFailed').replace('{error}', testResult.message)}
        </p>
      {/if}
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


  </div>

  <details class="mt-6 border th-border rounded-lg">
    <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
      {t('cameras.form.advancedSettings')}
    </summary>
    <div class="px-4 pb-4 pt-2">
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
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
    </div>
  </details>

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

  <!-- Transcoding Config (edit mode only, when global enabled) -->
  {#if editingCamera}
    {#if globalTranscodingEnabled}
      <details class="mt-6 border th-border rounded-lg" open={formTranscodingEnabled ? true : undefined}>
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('transcoding.per_camera_config')}
          {#if formTranscodingEnabled}
            <span class="text-xs th-text-muted ml-2">{t('transcoding.enabled')}</span>
          {:else}
            <span class="text-xs th-text-muted ml-2">{t('merge.usingDefault')}</span>
          {/if}
        </summary>

        <div class="px-4 pb-4 pt-2">
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <!-- Enabled toggle -->
            <div class="md:col-span-2 flex items-center gap-2">
              <input
                id="transcode-enabled"
                type="checkbox"
                class="accent-[var(--color-accent)]"
                bind:checked={formTranscodingEnabled}
              />
              <label for="transcode-enabled" class="th-text-secondary text-sm">{t('transcoding.enabled')}</label>
            </div>

            {#if formTranscodingEnabled}
              <!-- Target Codec -->
              <div>
                <label for="transcode-codec" class="input-label">{t('transcoding.target_codec')}</label>
                <select id="transcode-codec" class="input" bind:value={formTranscodingCodec}>
                  <option value="h264">{t('transcoding.codec_h264')}</option>
                  <option value="h265" disabled={!h265Available}>{t('transcoding.codec_h265')}{!h265Available ? ` (${t('transcoding.unavailable')})` : ''}</option>
                </select>
                {#if !h265Available}
                  <p class="mt-1 text-xs text-[var(--color-danger)]">{t('transcoding.h265_not_available')}</p>
                {:else if formTranscodingCodec === 'h265'}
                  <p class="mt-1 text-xs text-[var(--color-warning)]">{t('transcoding.warning_h265_slow')}</p>
                {/if}
              </div>

              <!-- Preset -->
              <div>
                <label for="transcode-preset" class="input-label">{t('transcoding.preset')}</label>
                <select id="transcode-preset" class="input" bind:value={formTranscodingPreset}>
                  <option value="ultrafast">{t('transcoding.preset_ultrafast')}</option>
                  <option value="faster">{t('transcoding.preset_faster')}</option>
                  <option value="medium">{t('transcoding.preset_medium')}</option>
                </select>
              </div>

              <!-- Bitrate -->
              <div>
                <label for="transcode-bitrate" class="input-label">{t('transcoding.bitrate')}</label>
                <input
                  id="transcode-bitrate"
                  type="text"
                  class="input"
                  bind:value={formTranscodingBitrate}
                  placeholder="2M"
                />
              </div>
            {/if}
          </div>
        </div>
      </details>
    {:else}
      <div class="mt-6 p-3 rounded-md th-bg-hover border th-border text-sm th-text-muted flex items-center gap-2">
        <svg class="w-4 h-4 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
        {t('transcoding.warning_global_disabled')}
</div>
  {/if}
{/if}

  <!-- Timelapse Config (edit mode only) -->
  {#if editingCamera}
    <TimelapseConfigEditor cameraId={editingCamera.id} />
  {/if}

  <!-- ONVIF Device Settings (edit mode only, ONVIF cameras) -->
  {#if editingCamera && normalizeProtocol(editingCamera.protocol) === 'onvif' && !capsLoading}
    <div class="mt-6 space-y-4">
      <h4 class="text-sm font-semibold th-text-secondary uppercase tracking-wide">ONVIF</h4>

      <!-- Device Capabilities -->
      <DeviceCapabilities cameraId={editingCamera.id} />

      <!-- Imaging Panel (if supported) -->
      {#if deviceCaps?.imaging}
        <details class="border th-border rounded-lg" open>
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.imaging.title')}
          </summary>
          <div class="px-4 pb-4">
            <ImagingPanel cameraId={editingCamera.id} />
          </div>
        </details>
      {/if}

      <!-- Preset Manager (if PTZ supported) -->
      {#if deviceCaps?.ptz}
        <details class="border th-border rounded-lg" open>
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.presets.title')}
          </summary>
          <div class="px-4 pb-4">
            <PresetManager cameraId={editingCamera.id} />
          </div>
        </details>
      {/if}

      <!-- ONVIF Events (if supported) -->
      {#if deviceCaps?.events}
        <details class="border th-border rounded-lg">
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.events.title')}
          </summary>
          <div class="px-4 pb-4">
            <ONVIFEvents cameraId={editingCamera.id} maxEvents={50} />
          </div>
        </details>
      {/if}

      <!-- Device Management -->
      <details class="border th-border rounded-lg">
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('onvif.device.title')}
        </summary>
        <div class="px-4 pb-4">
          <DeviceManagement cameraId={editingCamera.id} cameraName={editingCamera.name} />
        </div>
      </details>
    </div>
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

