<script lang="ts">
  import { onMount } from 'svelte';
  import { getFeatures, updateFeatures, getAiSettings, saveAiSettings, detectAiBackend, listCameras, getFFmpegStatus } from '$lib/api';
  import { getPerCameraAiSettings, savePerCameraAiSettings, getAIZones, createAIZone, deleteAIZone } from '$lib/api';
  import type { Camera, DownloadStatus, Zone, ZoneList, PerCameraAiState } from '$lib/api';
  import { t } from '$lib/i18n';
  import { AlertTriangle, ChevronDown, Plus, Trash2, X } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import SettingsTranscodingCard from './SettingsTranscodingCard.svelte';

  // AI Detection state
  let aiEnabled = $state(false);
  let aiConfidenceThreshold = $state(0.5);
  let aiFrameSkip = $state(3);
  let aiDetectedBackend = $state('');

  // Feature toggles state
  let featureFlags = $state<Record<string, boolean>>({});
  let featuresLoading = $state(true);
  let featuresSaving = $state(false);
  let originalFeatureFlags = $state<Record<string, boolean>>({});

  // Camera list for feature toggle affected count
  let allCameras = $state<Camera[]>([]);

  // FFmpeg status for Transcoding badge
  let ffmpegStatus = $state<DownloadStatus | null>(null);

  // Feature flags dirty tracking
  let featuresDirty = $derived(JSON.stringify(featureFlags) !== JSON.stringify(originalFeatureFlags));

  // Derived badges
  let aiBadge = $derived(aiEnabled
    ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
    : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }
  );

  let transcodingBadge = $derived.by(() => {
    if (!ffmpegStatus) return undefined;
    if (ffmpegStatus.status === 'available') return { text: t('settings.featureToggles.available'), color: 'success' as const };
    if (ffmpegStatus.status === 'downloading') return { text: t('common.loading'), color: 'info' as const };
    if (ffmpegStatus.status === 'failed') return { text: t('common.error'), color: 'danger' as const };
    return { text: t('settings.featureToggles.notInstalled'), color: 'warning' as const };
  });

  // Affected camera count for a protocol
  function getAffectedCameraCount(protocol: string): number {
    return allCameras.filter(c => c.protocol === protocol || c.protocol.startsWith(protocol)).length;
  }

  async function loadFeatures() {
    featuresLoading = true;
    try {
      const data = await getFeatures();
      featureFlags = data.protocols;
      originalFeatureFlags = { ...data.protocols };
    } catch (e) { console.warn('Failed to load feature flags:', e); featureFlags = {}; } finally {
      featuresLoading = false;
    }
  }

  async function saveFeatures() {
    featuresSaving = true;
    try {
      await updateFeatures({ protocols: featureFlags });
      originalFeatureFlags = { ...featureFlags };
      showToast(t('settings.featureToggles.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.featureToggles.error'), 'error');
    } finally {
      featuresSaving = false;
    }
  }

  async function loadCameraList() {
    try {
      allCameras = await listCameras();
    } catch (e) { /* non-critical */ }
  }

  // --- AI Detection ---
  function loadAiSettings() {
    const settings = getAiSettings();
    aiEnabled = settings.enabled;
    aiConfidenceThreshold = settings.confidenceThreshold;
    aiFrameSkip = settings.frameSkip;
    aiDetectedBackend = detectAiBackend();
  }

  function saveAiSettingsLocal() {
    saveAiSettings({
      enabled: aiEnabled,
      confidenceThreshold: aiConfidenceThreshold,
      frameSkip: aiFrameSkip,
    });
    showToast(t('settings.ai.saved'), 'success');
  }

// --- Per-Camera AI ---
let perCameraAIConfig = $state<PerCameraAiState>({});
let expandedCameras = $state<Record<string, boolean>>({});
let zones = $state<Zone[]>([]);
let perCamSaving = $state(false);
let zonesLoading = $state(false);

// Zone create dialog state
let showCreateZone = $state(false);
let newZoneCamera = $state('');
let newZoneName = $state('');
let newZonePoints = $state('');
let zoneCreating = $state(false);

function loadPerCameraAiSettings() {
  perCameraAIConfig = getPerCameraAiSettings();
}

async function loadZones() {
  zonesLoading = true;
  try {
    const data = await getAIZones();
    zones = data.zones || [];
  } catch (e) {
    console.warn('Failed to load zones:', e);
    zones = [];
  } finally {
    zonesLoading = false;
  }
}

function toggleCameraExpand(cameraId: string) {
  const current = expandedCameras[cameraId];
  expandedCameras[cameraId] = !current;
  expandedCameras = expandedCameras;
}

function getCameraConfig(cameraId: string) {
  return perCameraAIConfig[cameraId] || { enabled: false, confidenceThreshold: 0.5, frameSkip: 3 };
}

function setCameraEnabled(cameraId: string, enabled: boolean) {
  const config = { ...getCameraConfig(cameraId), enabled };
  perCameraAIConfig[cameraId] = config;
  perCameraAIConfig = perCameraAIConfig;
}

function setCameraConfidence(cameraId: string, value: number) {
  const config = { ...getCameraConfig(cameraId), confidenceThreshold: value };
  perCameraAIConfig[cameraId] = config;
  perCameraAIConfig = perCameraAIConfig;
}

function setCameraFrameSkip(cameraId: string, value: number) {
  const config = { ...getCameraConfig(cameraId), frameSkip: value };
  perCameraAIConfig[cameraId] = config;
  perCameraAIConfig = perCameraAIConfig;
}

async function savePerCameraAiSettingsLocal() {
  perCamSaving = true;
  try {
    savePerCameraAiSettings(perCameraAIConfig);
    showToast(t('settings.ai.perCameraSaved'), 'success');
  } catch (e) {
    showToast(e instanceof Error ? e.message : t('settings.ai.saveFailed'), 'error');
  } finally {
    perCamSaving = false;
  }
}

async function handleCreateZone() {
  if (!newZoneCamera || !newZoneName || !newZonePoints) {
    showToast('Camera, name, and points are required', 'error');
    return;
  }
  zoneCreating = true;
  try {
    const pointRegex = /\[([0-9.]+),([0-9.]+)\]/g;
    const points: number[][] = [];
    let match;
    while ((match = pointRegex.exec(newZonePoints)) !== null) {
      points.push([parseFloat(match[1]), parseFloat(match[2])]);
    }
    if (points.length < 3) {
      showToast(t('settings.ai.createZoneFailed', { error: 'At least 3 points required' }), 'error');
      return;
    }
    await createAIZone({
      camera_id: newZoneCamera,
      name: newZoneName,
      points,
      enabled: true,
    });
    showToast(t('settings.ai.zoneCreated'), 'success');
    showCreateZone = false;
    newZoneCamera = '';
    newZoneName = '';
    newZonePoints = '';
    await loadZones();
  } catch (e) {
    showToast(
      e instanceof Error
        ? t('settings.ai.createZoneFailed', { error: e.message })
        : t('settings.ai.createZoneFailed', { error: 'Unknown error' }),
      'error',
    );
  } finally {
    zoneCreating = false;
  }
}

async function handleDeleteZone(zoneName: string) {
  try {
    await deleteAIZone(zoneName);
    showToast(t('settings.ai.zoneDeleted'), 'success');
    await loadZones();
  } catch (e) {
    showToast(t('settings.ai.deleteZoneFailed'), 'error');
  }
}

  async function loadFFmpegStatus() {
    try {
      ffmpegStatus = await getFFmpegStatus();
    } catch (e) { /* non-critical */ }
  }

  onMount(() => {
    loadFeatures();
    loadAiSettings();
    loadCameraList();
    loadPerCameraAiSettings();
    loadZones();
    loadFFmpegStatus();
  });
</script>

<!-- AI Detection -->
<SettingsCard
  title={t('settings.ai.title')}
  subtitle={t('settings.ai.description')}
  badge={aiBadge}
>
  <div class="flex items-center justify-between mb-4">
    <span class="text-sm th-text-secondary">{t('settings.ai.enabled')}</span>
    <button
      id="ai-toggle" aria-label={t('settings.ai.title')}
      type="button"
      class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {aiEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
      onclick={() => { aiEnabled = !aiEnabled; }}
      onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); aiEnabled = !aiEnabled; } }}
      role="switch"
      aria-checked={aiEnabled}
    >
      <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {aiEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
    </button>

  {#if aiEnabled}
    <div class="space-y-6">
      <!-- Confidence Threshold -->
      <div>
        <div class="flex items-center justify-between mb-2">
          <label class="input-label" for="ai-confidence-threshold">{t('settings.ai.confidenceThreshold')}</label>
          <span class="text-sm font-medium th-text-primary">{Math.round(aiConfidenceThreshold * 100)}%</span>
        </div>
        <input
          id="ai-confidence-threshold"
          type="range"
          class="w-full h-2 rounded-full appearance-none cursor-pointer th-bg-tertiary accent-blue-600"
          bind:value={aiConfidenceThreshold}
          min="0.1"
          max="0.9"
          step="0.1"
        />
        <p class="text-xs th-text-tertiary mt-1">{t('settings.ai.confidenceHint')}</p>
      </div>

      <!-- Frame Skip -->
      <div>
        <div class="flex items-center justify-between mb-2">
          <label class="input-label" for="ai-frame-skip">{t('settings.ai.frameSkip')}</label>
          <span class="text-sm font-medium th-text-primary">{aiFrameSkip}</span>
        </div>
        <input
          id="ai-frame-skip"
          type="range"
          class="w-full h-2 rounded-full appearance-none cursor-pointer th-bg-tertiary accent-blue-600"
          bind:value={aiFrameSkip}
          min="1"
          max="10"
          step="1"
        />
        <p class="text-xs th-text-tertiary mt-1">{t('settings.ai.frameSkipHint')}</p>
      </div>

      <!-- Model & Backend Info -->
      <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div class="p-3 rounded-md th-bg-hover border th-border">
          <div class="text-xs th-text-tertiary mb-1">{t('settings.ai.modelInfo')}</div>
          <div class="text-sm font-medium th-text-primary">{t('settings.ai.modelName')}</div>
        </div>
        <div class="p-3 rounded-md th-bg-hover border th-border">
          <div class="text-xs th-text-tertiary mb-1">{t('settings.ai.backendInfo')}</div>
          <div class="text-sm font-medium th-text-primary">{aiDetectedBackend}</div>
        </div>
      </div>

      <!-- Save AI Settings -->
      <div class="flex justify-end">
        <button type="button" class="btn btn-primary" onclick={saveAiSettingsLocal}>
          {t('settings.save')}
        </button>
      </div>
    </div>
  {/if}
</SettingsCard>

<!-- Per-Camera AI Settings -->
<SettingsCard
  title={t('settings.ai.perCameraTitle')}
  subtitle={t('settings.ai.perCameraDesc')}
  defaultOpen={false}
>
  {#if allCameras.length === 0}
    <div class="py-4 text-sm th-text-tertiary">{t('settings.ai.noCameras')}</div>
  {:else}
    <div class="space-y-3">
      {#each allCameras as camera (camera.id)}
        {@const camCfg = getCameraConfig(camera.id)}
        <div class="rounded-md border th-border overflow-hidden">
          <!-- Camera header (clickable) -->
          <button
            onclick={() => toggleCameraExpand(camera.id)}
            class="w-full flex items-center justify-between p-3 text-left hover:th-bg-hover transition-colors"
            aria-expanded={expandedCameras[camera.id]}
          >
            <div class="flex items-center gap-3 min-w-0 flex-1">
              <span class="font-medium text-sm th-text-primary truncate">{camera.name}</span>
              <span class="text-xs th-text-tertiary">({camera.id})</span>
              <span class="badge {camCfg.enabled ? 'badge-success' : 'badge-neutral'}">
                {camCfg.enabled ? t('settings.featureToggles.enabled') : t('settings.featureToggles.disabled')}
              </span>
            </div>
            <ChevronDown
              size={16}
              class="th-text-tertiary flex-shrink-0 transition-transform duration-200 {expandedCameras[camera.id] ? 'rotate-180' : ''}"
            />
          </button>

          <!-- Per-camera controls (collapsible) -->
          {#if expandedCameras[camera.id]}
            <div class="border-t th-border p-4 space-y-4">
              <!-- AI Toggle -->
              <div class="flex items-center justify-between">
                <span class="text-sm th-text-secondary">{t('settings.ai.cameraEnabled')}</span>
                <button
                  type="button"
                  class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {camCfg.enabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
                  onclick={() => setCameraEnabled(camera.id, !camCfg.enabled)}
                  role="switch"
                  aria-checked={camCfg.enabled}
                >
                  <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {camCfg.enabled ? 'translate-x-6' : 'translate-x-1'}"></span>
                </button>
              </div>

              <!-- Confidence Threshold override -->
              <div>
                <div class="flex items-center justify-between mb-2">
                  <label class="input-label" for="cam-conf-{camera.id}">{t('settings.ai.perCameraConfidence')}</label>
                  <span class="text-sm font-medium th-text-primary">{Math.round(camCfg.confidenceThreshold * 100)}%</span>
                </div>
                <input
                  id="cam-conf-{camera.id}"
                  type="range"
                  class="w-full h-2 rounded-full appearance-none cursor-pointer th-bg-tertiary accent-blue-600"
                  min="0"
                  max="1"
                  step="0.05"
                  value={camCfg.confidenceThreshold}
                  oninput={(e) => setCameraConfidence(camera.id, parseFloat(e.currentTarget.value))}
                />
              </div>

              <!-- Frame Skip override -->
              <div>
                <div class="flex items-center justify-between mb-2">
                  <label class="input-label" for="cam-skip-{camera.id}">{t('settings.ai.perCameraFrameSkip')}</label>
                  <input
                    id="cam-skip-{camera.id}"
                    type="number"
                    class="input w-20 text-center"
                    min="1"
                    max="30"
                    value={camCfg.frameSkip}
                    oninput={(e) => setCameraFrameSkip(camera.id, parseInt(e.currentTarget.value) || 3)}
                  />
                </div>
              </div>
            </div>
          {/if}
        </div>
      {/each}
    </div>

    <!-- Per-camera Save + Zone Section -->
    <div class="mt-4 space-y-4">
      <!-- Save button -->
      <div class="flex justify-end">
        <button type="button" class="btn btn-primary" onclick={savePerCameraAiSettingsLocal} disabled={perCamSaving}>
          {#if perCamSaving}
            <span class="spinner mr-2"></span>
          {/if}
          {t('settings.ai.savePerCamera')}
        </button>
      </div>

      <!-- ROI Zones Section -->
      <div class="border-t th-border pt-4">
        <h4 class="text-sm font-semibold th-text-primary mb-2">{t('settings.ai.zoneTitle')}</h4>
        <p class="text-xs th-text-tertiary mb-3">{t('settings.ai.zoneDesc')}</p>

        {#if zonesLoading}
          <div class="flex items-center gap-2 py-2 th-text-muted">
            <span class="spinner"></span>
            <span class="text-xs">{t('common.loading')}</span>
          </div>
        {:else if zones.length === 0}
          <div class="text-sm th-text-tertiary py-2">{t('settings.ai.zoneNoZones')}</div>
        {:else}
          <div class="space-y-2">
            {#each zones as zone (zone.name)}
              <div class="flex items-center justify-between p-2 rounded-md th-bg-hover border th-border">
                <div class="min-w-0 flex-1">
                  <div class="text-sm th-text-primary truncate">{zone.name}</div>
                  <div class="text-xs th-text-tertiary">
                    {zone.camera_id} &mdash; {t('settings.ai.zonePointsCount', { count: zone.points.length })}
                  </div>
                </div>
                <button
                  type="button"
                  class="btn-ghost p-1 text-xs th-color-danger hover:th-bg-danger-light"
                  onclick={() => handleDeleteZone(zone.name)}
                  title={t('settings.ai.zoneDelete')}
                >
                  <Trash2 size={14} />
                </button>
              </div>
            {/each}
          </div>
        {/if}

        <!-- Create Zone button -->
        <button
          type="button"
          class="btn btn-secondary mt-3 text-sm"
          onclick={() => { showCreateZone = true; }}
        >
          <Plus size={14} class="mr-1" />
          {t('settings.ai.zoneCreate')}
        </button>
      </div>
    </div>
  {/if}
</SettingsCard>

<!-- Create Zone Dialog Overlay -->
{#if showCreateZone}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/50"
    onclick={() => showCreateZone = false}
    role="dialog"
    aria-modal="true"
  >
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="card th-bg-elevated border th-border p-6 w-full max-w-md mx-4 shadow-lg"
      onclick={(e) => e.stopPropagation()}
      role="document"
    >
      <div class="flex items-center justify-between mb-4">
        <h3 class="text-lg font-semibold th-text-primary">{t('settings.ai.zoneCreateTitle')}</h3>
        <button
          type="button"
          class="btn-ghost p-1"
          onclick={() => showCreateZone = false}
        >
          <X size={18} />
        </button>
      </div>

      <div class="space-y-4">
        <!-- Camera select -->
        <div>
          <label class="input-label" for="zone-camera">{t('settings.ai.zoneCamera')}</label>
          <select
            id="zone-camera"
            class="input"
            bind:value={newZoneCamera}
          >
            <option value="">{t('settings.ai.zoneCamera')}</option>
            {#each allCameras as cam (cam.id)}
              <option value={cam.id}>{cam.name} ({cam.id})</option>
            {/each}
          </select>
        </div>

        <!-- Zone name -->
        <div>
          <label class="input-label" for="zone-name">{t('settings.ai.zoneName')}</label>
          <input
            id="zone-name"
            type="text"
            class="input"
            placeholder="entrance"
            bind:value={newZoneName}
          />
        </div>

        <!-- Points (JSON text) -->
        <div>
          <label class="input-label" for="zone-points">{t('settings.ai.zonePoints')}</label>
          <input
            id="zone-points"
            type="text"
            class="input"
            placeholder="[0.1,0.1],[0.5,0.1],[0.5,0.8],[0.1,0.8]"
            bind:value={newZonePoints}
          />
          <p class="text-xs th-text-tertiary mt-1">{t('settings.ai.zonePointsHint')}</p>
        </div>

        <div class="flex justify-end gap-2 pt-2">
          <button
            type="button"
            class="btn btn-secondary"
            onclick={() => showCreateZone = false}
          >
            {t('common.cancel')}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            onclick={handleCreateZone}
            disabled={zoneCreating}
          >
            {#if zoneCreating}
              <span class="spinner mr-2"></span>
            {/if}
            {t('settings.ai.zoneCreate')}
          </button>
        </div>
      </div>
    </div>
  </div>
{/if}

<!-- Feature Toggles -->
<SettingsCard
  title={t('settings.featureToggles.title')}
  subtitle={t('settings.advanced.features.description')}
>
  {#if featuresLoading}
    <div class="flex items-center gap-2 py-4 th-text-muted">
      <span class="spinner"></span>
      <span class="text-sm">{t('common.loading')}</span>
    </div>
  {:else}
    <div class="space-y-4">
      {#each Object.entries(featureFlags) as [protocol, enabled] (protocol)}
        <div class="p-4 rounded-md th-bg-hover border th-border">
          <div class="flex items-center justify-between">
            <div class="min-w-0 flex-1">
              <div class="font-medium th-text-primary">{t(`settings.featureToggles.protocols.${protocol}`)}</div>
              {#if !enabled}
                <div class="flex items-center gap-1 mt-1 text-xs th-color-warning">
                  <AlertTriangle size={12} />
                  <span>{t('settings.featureToggles.warning')}{#if getAffectedCameraCount(protocol) > 0} <span class="th-color-danger">({getAffectedCameraCount(protocol)} {t('cameras.title').toLowerCase()})</span>{/if}</span>
                </div>
              {/if}
            </div>
            <div class="flex items-center gap-3">
              <button
                id="protocol-toggle-{protocol}" aria-label={t(`settings.featureToggles.protocols.${protocol}`)}
                type="button"
                class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {enabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
                onclick={() => { featureFlags[protocol] = !featureFlags[protocol]; featureFlags = featureFlags; }}
                onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); featureFlags[protocol] = !featureFlags[protocol]; featureFlags = featureFlags; } }}
                role="switch"
                aria-checked={enabled}
              >
                <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {enabled ? 'translate-x-6' : 'translate-x-1'}"></span>
              </button>
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/if}

  {#if featuresDirty}
    <div class="flex justify-end mt-4 pt-4 border-t th-border">
      <button type="button" class="btn btn-primary" onclick={saveFeatures} disabled={featuresSaving}>
        {#if featuresSaving}
          <span class="spinner mr-2"></span>
          {t('settings.featureToggles.saving')}
        {:else}
          {t('settings.featureToggles.save')}
        {/if}
      </button>
    </div>
  {/if}
</SettingsCard>

<!-- Transcoding -->
<SettingsCard
  title={t('transcoding.title')}
  subtitle={t('transcoding.description')}
  badge={transcodingBadge}
>
  <SettingsTranscodingCard />
</SettingsCard>
