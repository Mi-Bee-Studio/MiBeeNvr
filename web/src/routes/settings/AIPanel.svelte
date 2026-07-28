<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getAiSettings, saveAiSettings, detectAiBackend, listCameras, getAiStatus, updateAiConfig } from '$lib/api';
  import { getPerCameraAiSettings, savePerCameraAiSettings, getAIZones, createAIZone, deleteAIZone } from '$lib/api';
  import { getSettings, generateAPIKey, revokeAPIKey } from '$lib/api';
  import { refreshMiBeeVisionStatus } from '$lib/mibeevision-status.svelte';
  import type { Camera, Zone, PerCameraAiState } from '$lib/api';
  import { t } from '$lib/i18n';
  import { Plus, Trash2, X, Copy, Check, ChevronDown } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';

  // AI Detection state
  let aiEnabled = $state(false);
  let originalAiEnabled = $state(false);
  let aiConfidenceThreshold = $state(0.5);
  let originalConfidence = $state(0.5);
  let aiFrameSkip = $state(3);
  let aiDetectedBackend = $state('');
  let loading = $state(true);

  // Per-camera AI
  let allCameras = $state<Camera[]>([]);
  let perCameraAIConfig = $state<PerCameraAiState>({});
  let expandedCameras = $state<Record<string, boolean>>({});
  let zones = $state<Zone[]>([]);
  let zonesLoading = $state(false);

  // Zone create dialog
  let showCreateZone = $state(false);
  let newZoneCamera = $state('');
  let newZoneName = $state('');
  let newZonePoints = $state('');
  let zoneCreating = $state(false);

  // MiBeeVision keys
  let mibeeVisionKeys = $state<Array<{ name: string; prefix: string; revoked: boolean }>>([]);
  let mibeeVisionLoading = $state(false);
  let newKeyName = $state('');
  let generatingKey = $state(false);
  let newlyGeneratedKey = $state<string | null>(null);
  let copiedKey = $state(false);

  const hasMiBeeVisionKey = $derived(mibeeVisionKeys.length > 0);

  let isDirty = $derived(
    !loading && (
      aiEnabled !== originalAiEnabled ||
      aiConfidenceThreshold !== originalConfidence
    )
  );

  let unregister: (() => void) | undefined;

  async function loadAiSettings() {
    try {
      const status = await getAiStatus();
      aiEnabled = status.enabled;
      originalAiEnabled = status.enabled;
      aiConfidenceThreshold = status.confidence_threshold;
      originalConfidence = status.confidence_threshold;
      aiFrameSkip = status.frame_skip_rate;
    } catch (e) {
      console.warn('Failed to load AI status from backend, falling back to localStorage:', e);
      const settings = getAiSettings();
      aiEnabled = settings.enabled;
      originalAiEnabled = settings.enabled;
      aiConfidenceThreshold = settings.confidenceThreshold;
      originalConfidence = settings.confidenceThreshold;
      aiFrameSkip = settings.frameSkip;
    }
    aiDetectedBackend = detectAiBackend();
  }

  async function performSave() {
    await updateAiConfig({
      enabled: aiEnabled,
      confidence_threshold: aiConfidenceThreshold,
      frame_skip_rate: aiFrameSkip,
    });
    saveAiSettings({
      enabled: aiEnabled,
      confidenceThreshold: aiConfidenceThreshold,
      frameSkip: aiFrameSkip,
    });
    originalAiEnabled = aiEnabled;
    originalConfidence = aiConfidenceThreshold;
    showToast(t('settings.saved'), 'success');
  }

  function resetForm() {
    aiEnabled = originalAiEnabled;
    aiConfidenceThreshold = originalConfidence;
  }

  // MiBeeVision key management
  async function loadMiBeeVisionKeys() {
    mibeeVisionLoading = true;
    try {
      const settings = await getSettings();
      const cfg = settings.mibeevision;
      if (cfg && cfg.api_keys) {
        mibeeVisionKeys = cfg.api_keys.map(k => ({ name: k.name, prefix: k.prefix, revoked: k.revoked }));
      }
    } catch (e) {
      console.warn('Failed to load MiBeeVision keys:', e);
    } finally {
      mibeeVisionLoading = false;
    }
  }

  async function handleGenerateKey() {
    if (!newKeyName.trim()) return;
    generatingKey = true;
    try {
      const result = await generateAPIKey(newKeyName.trim());
      newlyGeneratedKey = result.key;
      copiedKey = false;
      newKeyName = '';
      await loadMiBeeVisionKeys();
      refreshMiBeeVisionStatus();
      showToast(t('settings.mibeevision.keyGenerated'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.mibeevision.keyError'), 'error');
    } finally {
      generatingKey = false;
    }
  }

  async function handleRevokeKey(name: string) {
    try {
      await revokeAPIKey(name);
      await loadMiBeeVisionKeys();
      refreshMiBeeVisionStatus();
      showToast(t('settings.mibeevision.keyRevoked'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.mibeevision.keyError'), 'error');
    }
  }

  function copyKey() {
    if (newlyGeneratedKey) {
      navigator.clipboard.writeText(newlyGeneratedKey);
      copiedKey = true;
    }
  }

  // Zones
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

  async function handleCreateZone() {
    if (!newZoneCamera || !newZoneName.trim() || !newZonePoints.trim()) return;
    zoneCreating = true;
    try {
      const points = newZonePoints.split(';').map(p => {
        const [x, y] = p.split(',').map(Number);
        return [x, y];
      }).filter(p => p.length === 2 && !isNaN(p[0]) && !isNaN(p[1]));
      if (points.length < 3) {
        showToast(t('settings.ai.zones.needPoints'), 'error');
        return;
      }
      await createAIZone({
        camera_id: newZoneCamera,
        name: newZoneName.trim(),
        points,
      });
      showToast(t('settings.ai.zones.created'), 'success');
      showCreateZone = false;
      newZoneCamera = '';
      newZoneName = '';
      newZonePoints = '';
      await loadZones();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.ai.zones.createError'), 'error');
    } finally {
      zoneCreating = false;
    }
  }

  async function handleDeleteZone(zoneId: string) {
    try {
      await deleteAIZone(zoneId);
      showToast(t('settings.ai.zones.deleted'), 'success');
      await loadZones();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.ai.zones.deleteError'), 'error');
    }
  }

  function toggleCameraExpand(cameraId: string) {
    expandedCameras[cameraId] = !expandedCameras[cameraId];
    expandedCameras = expandedCameras;
  }

  function getCameraConfig(cameraId: string) {
    return perCameraAIConfig[cameraId] || { enabled: false, confidence: 0.5 };
  }

  function togglePerCameraAi(cameraId: string) {
    if (!perCameraAIConfig[cameraId]) {
      perCameraAIConfig[cameraId] = { enabled: false, confidence: 0.5 };
    }
    perCameraAIConfig[cameraId].enabled = !perCameraAIConfig[cameraId].enabled;
    perCameraAIConfig = perCameraAIConfig;
    savePerCameraAiSettings(perCameraAIConfig);
  }

  onMount(() => {
    Promise.all([loadAiSettings(), loadMiBeeVisionKeys(), loadZones()]).then(() => {
      allCameras = listCameras().catch(() => []) as any;
      perCameraAIConfig = getPerCameraAiSettings();
      loading = false;
    });
    unregister = settingsForm.register('ai', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        if (originalAiEnabled && !aiEnabled) {
          return t('settings.destructive.aiOff');
        }
        return null;
      },
    });
  });
  onDestroy(() => unregister?.());
</script>

{#if loading}
  <div class="card border th-border p-6">
    <div class="space-y-3">
      <div class="h-6 w-40 th-bg-tertiary rounded animate-pulse"></div>
      <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
    </div>
  </div>
{:else}
  <!-- AI Detection -->
  <SettingsCard
    title={t('settings.ai.title')}
    subtitle={t('settings.ai.description')}
    badge={aiEnabled
      ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
      : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }}
  >
    <div class="flex items-center justify-between mb-4">
      <span class="text-sm th-text-secondary">{t('settings.ai.enabled')}</span>
      <Toggle checked={aiEnabled} onChange={(v) => { aiEnabled = v; }} label={t('settings.ai.title')} />
    </div>

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
      </div>
    {/if}
  </SettingsCard>

  <!-- MiBeeVision Integration -->
  <SettingsCard
    title={t('settings.mibeevision.title')}
    subtitle={t('settings.mibeevision.description')}
    badge={hasMiBeeVisionKey
      ? { text: t('settings.mibeevision.connected'), color: 'success' as const }
      : { text: t('settings.mibeevision.notConnected'), color: 'warning' as const }}
  >
    <div class="space-y-4">
      {#if newlyGeneratedKey}
        <div class="p-4 rounded-md border th-border th-bg-hover">
          <div class="flex items-center justify-between mb-2">
            <span class="text-sm font-medium th-text-primary">{t('settings.mibeevision.yourKey')}</span>
            <button type="button" class="btn btn-ghost btn-sm" onclick={copyKey}>
              {#if copiedKey}<Check size={14} />{:else}<Copy size={14} />{/if}
              {copiedKey ? t('settings.mibeevision.copied') : t('settings.mibeevision.copy')}
            </button>
          </div>
          <code class="text-xs break-all th-text-secondary">{newlyGeneratedKey}</code>
          <p class="text-xs th-color-warning mt-2">{t('settings.mibeevision.keyWarning')}</p>
        </div>
      {/if}

      <!-- Generate new key -->
      <div class="flex gap-2">
        <input
          type="text"
          class="input flex-1"
          placeholder={t('settings.mibeevision.keyNamePlaceholder')}
          bind:value={newKeyName}
        />
        <button type="button" class="btn btn-primary" onclick={handleGenerateKey} disabled={generatingKey || !newKeyName.trim()}>
          {#if generatingKey}<span class="spinner mr-1"></span>{/if}
          {t('settings.mibeevision.generate')}
        </button>
      </div>

      <!-- Existing keys -->
      {#if mibeeVisionKeys.length > 0}
        <div class="space-y-2">
          {#each mibeeVisionKeys as key (key.name)}
            <div class="flex items-center justify-between p-3 rounded-md border th-border">
              <div>
                <span class="text-sm font-medium th-text-primary">{key.name}</span>
                <span class="text-xs th-text-tertiary ml-2">mbv_{key.prefix}...</span>
                {#if key.revoked}<span class="badge badge-error ml-2">{t('settings.mibeevision.revoked')}</span>{/if}
              </div>
              {#if !key.revoked}
                <button type="button" class="btn btn-ghost btn-sm th-color-danger" onclick={() => handleRevokeKey(key.name)}>
                  <Trash2 size={14} />
                </button>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </SettingsCard>

  <!-- Per-Camera AI -->
  <SettingsCard
    title={t('settings.ai.perCameraTitle')}
    subtitle={t('settings.ai.perCameraDesc')}
  >
    {#if allCameras.length === 0}
      <p class="text-sm th-text-tertiary py-4">{t('settings.noCameras')}</p>
    {:else}
      <div class="space-y-2">
        {#each allCameras as cam (cam.id)}
          <div class="border th-border rounded-md overflow-hidden">
            <button
              type="button"
              class="w-full flex items-center justify-between p-3 hover:th-bg-hover transition-colors"
              onclick={() => toggleCameraExpand(cam.id)}
            >
              <div class="flex items-center gap-2">
                <Toggle checked={getCameraConfig(cam.id).enabled} onChange={() => togglePerCameraAi(cam.id)} label={cam.name} />
                <span class="text-sm font-medium th-text-primary">{cam.name}</span>
              </div>
              <ChevronDown size={16} class="th-text-tertiary transition-transform {expandedCameras[cam.id] ? 'rotate-180' : ''}" />
            </button>
          </div>
        {/each}
      </div>
    {/if}

    <!-- ROI Zones -->
    {#if zones.length > 0}
      <div class="mt-4 pt-4 border-t th-border">
        <h4 class="text-sm font-medium th-text-primary mb-2">{t('settings.ai.zones.title')}</h4>
        <div class="space-y-2">
          {#each zones as zone (zone.id)}
            <div class="flex items-center justify-between p-2 rounded-md th-bg-hover">
              <span class="text-sm th-text-secondary">{zone.name} ({zone.camera_id})</span>
              <button type="button" class="btn btn-ghost btn-sm th-color-danger" onclick={() => handleDeleteZone(zone.id)}>
                <Trash2 size={14} />
              </button>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </SettingsCard>
{/if}
