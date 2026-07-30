<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getAiSettings, saveAiSettings, detectAiBackend, listCameras, getAiStatus, updateAiConfig, listAiModels } from '$lib/api';
  import { getPerCameraAiSettings, savePerCameraAiSettings, getAIZones, createAIZone, deleteAIZone } from '$lib/api';
  import { getSettings, generateAPIKey, revokeAPIKey } from '$lib/api';
  import { refreshMiBeeVisionStatus } from '$lib/mibeevision-status.svelte';
  import type { Camera, Zone, PerCameraAiState, AiModelInfo } from '$lib/api';
  import { COCO_CLASSES } from '$lib/ai-detection/inference';
  import { t } from '$lib/i18n';
  import { Plus, Trash2, X, Copy, Check, ChevronDown } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import { copyText } from '$lib/clipboard';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';

  // AI Detection state
  let aiEnabled = $state(false);
  let originalAiEnabled = $state(false);
  let aiConfidenceThreshold = $state(0.5);
  let originalConfidence = $state(0.5);
  let aiFrameSkip = $state(3);
  let originalFrameSkip = $state(3);
  // Advanced detection params (#183/#184/#185)
  let aiEmaAlpha = $state(0.3);
  let originalEmaAlpha = $state(0.3);
  let aiMaxAge = $state(15);
  let originalMaxAge = $state(15);
  let aiEnabledClasses = $state<string[] | null>(null); // null = all classes
  let originalEnabledClasses = $state<string[] | null>(null);
  let aiModelUrl = $state('');
  let originalModelUrl = $state('');
  let aiModels = $state<AiModelInfo[]>([]);
  let showAdvanced = $state(false);
  let showClassFilter = $state(false);
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

  // Compare two enabled-classes values for isDirty (order-insensitive).
  function classesEqual(a: string[] | null, b: string[] | null): boolean {
    if (!a && !b) return true;
    if (!a || !b) return false;
    if (a.length !== b.length) return false;
    const sb = new Set(b);
    return a.every((c) => sb.has(c));
  }

  let isDirty = $derived(
    !loading && (
      aiEnabled !== originalAiEnabled ||
      aiConfidenceThreshold !== originalConfidence ||
      aiFrameSkip !== originalFrameSkip ||
      aiEmaAlpha !== originalEmaAlpha ||
      aiMaxAge !== originalMaxAge ||
      !classesEqual(aiEnabledClasses, originalEnabledClasses) ||
      aiModelUrl !== originalModelUrl
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
      originalFrameSkip = status.frame_skip_rate;
      aiEmaAlpha = status.ema_alpha ?? 0.3;
      originalEmaAlpha = aiEmaAlpha;
      aiMaxAge = status.max_age ?? 15;
      originalMaxAge = aiMaxAge;
      aiEnabledClasses =
        Array.isArray(status.enabled_classes) && status.enabled_classes.length > 0
          ? [...status.enabled_classes]
          : null;
      originalEnabledClasses = aiEnabledClasses ? [...aiEnabledClasses] : null;
      aiModelUrl = status.model_url ?? '';
      originalModelUrl = aiModelUrl;
    } catch (e) {
      console.warn('Failed to load AI status from backend, falling back to localStorage:', e);
      const settings = getAiSettings();
      aiEnabled = settings.enabled;
      originalAiEnabled = settings.enabled;
      aiConfidenceThreshold = settings.confidenceThreshold;
      originalConfidence = settings.confidenceThreshold;
      aiFrameSkip = settings.frameSkip;
      originalFrameSkip = settings.frameSkip;
      aiEmaAlpha = settings.emaAlpha ?? 0.3;
      originalEmaAlpha = aiEmaAlpha;
      aiMaxAge = settings.maxAge ?? 15;
      originalMaxAge = aiMaxAge;
      aiEnabledClasses = settings.enabledClasses ? [...settings.enabledClasses] : null;
      originalEnabledClasses = aiEnabledClasses ? [...aiEnabledClasses] : null;
      aiModelUrl = '';
      originalModelUrl = '';
    }
    aiDetectedBackend = detectAiBackend();
  }

  async function performSave() {
    await updateAiConfig({
      enabled: aiEnabled,
      confidence_threshold: aiConfidenceThreshold,
      frame_skip_rate: aiFrameSkip,
      ema_alpha: aiEmaAlpha,
      max_age: aiMaxAge,
      // Send the array as-is; null/empty means "all classes".
      enabled_classes: aiEnabledClasses && aiEnabledClasses.length > 0 ? aiEnabledClasses : [],
      model_url: aiModelUrl || undefined,
    });
    saveAiSettings({
      enabled: aiEnabled,
      confidenceThreshold: aiConfidenceThreshold,
      frameSkip: aiFrameSkip,
      emaAlpha: aiEmaAlpha,
      maxAge: aiMaxAge,
      enabledClasses: aiEnabledClasses && aiEnabledClasses.length > 0 ? aiEnabledClasses : null,
    });
    originalAiEnabled = aiEnabled;
    originalConfidence = aiConfidenceThreshold;
    originalFrameSkip = aiFrameSkip;
    originalEmaAlpha = aiEmaAlpha;
    originalMaxAge = aiMaxAge;
    originalEnabledClasses = aiEnabledClasses ? [...aiEnabledClasses] : null;
    originalModelUrl = aiModelUrl;
    showToast(t('settings.saved'), 'success');
  }

  function resetForm() {
    aiEnabled = originalAiEnabled;
    aiConfidenceThreshold = originalConfidence;
    aiFrameSkip = originalFrameSkip;
    aiEmaAlpha = originalEmaAlpha;
    aiMaxAge = originalMaxAge;
    aiEnabledClasses = originalEnabledClasses ? [...originalEnabledClasses] : null;
    aiModelUrl = originalModelUrl;
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

  async function copyKey() {
    if (newlyGeneratedKey) {
      // copyText falls back to execCommand('copy') on plain HTTP origins
      // (navigator.clipboard is undefined there — issue #197).
      copiedKey = await copyText(newlyGeneratedKey);
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
    // Field names MUST match PerCameraAiState ({ enabled, confidenceThreshold,
    // frameSkip }). Previously this returned { enabled, confidence } (wrong key),
    // which is why the per-camera values never reached the player (#180).
    return (
      perCameraAIConfig[cameraId] || { enabled: false, confidenceThreshold: 0.5, frameSkip: 3 }
    );
  }

  function togglePerCameraAi(cameraId: string) {
    if (!perCameraAIConfig[cameraId]) {
      perCameraAIConfig[cameraId] = { enabled: false, confidenceThreshold: 0.5, frameSkip: 3 };
    }
    perCameraAIConfig[cameraId].enabled = !perCameraAIConfig[cameraId].enabled;
    perCameraAIConfig = perCameraAIConfig;
    savePerCameraAiSettings(perCameraAIConfig);
  }

  /** Patch per-camera confidence/frameSkip and persist immediately (#180). */
  function updatePerCamera(
    cameraId: string,
    patch: Partial<{ confidenceThreshold: number; frameSkip: number }>,
  ) {
    if (!perCameraAIConfig[cameraId]) {
      perCameraAIConfig[cameraId] = { enabled: true, confidenceThreshold: 0.5, frameSkip: 3 };
    }
    if (patch.confidenceThreshold !== undefined) {
      perCameraAIConfig[cameraId].confidenceThreshold = patch.confidenceThreshold;
    }
    if (patch.frameSkip !== undefined) {
      perCameraAIConfig[cameraId].frameSkip = patch.frameSkip;
    }
    perCameraAIConfig = perCameraAIConfig;
    savePerCameraAiSettings(perCameraAIConfig);
  }

  // ─── Class filter (#184) ────────────────────────────────────────────────
  // Curated subset of COCO classes offered as checkboxes (the full 80 are too
  // many to scan; common surveillance targets cover most use cases). The full
  // label set remains the source of truth via COCO_CLASSES.
  const COMMON_CLASSES = [
    'person', 'bicycle', 'car', 'motorcycle', 'bus', 'truck',
    'cat', 'dog', 'bird', 'horse',
    'backpack', 'handbag', 'suitcase',
    'chair', 'clock',
  ];
  const CLASS_PRESETS: { label: string; classes: string[] | null }[] = [
    { label: 'settings.ai.classPresetAll', classes: null },
    { label: 'settings.ai.classPresetPerson', classes: ['person'] },
    { label: 'settings.ai.classPresetSecurity', classes: ['person', 'bicycle', 'car', 'motorcycle', 'bus', 'truck', 'dog', 'cat'] },
    { label: 'settings.ai.classPresetPersonVehicle', classes: ['person', 'bicycle', 'car', 'motorcycle', 'bus', 'truck'] },
  ];

  function applyClassPreset(classes: string[] | null) {
    aiEnabledClasses = classes ? [...classes] : null;
  }

  function toggleClass(label: string) {
    const set = new Set(aiEnabledClasses ?? []);
    if (set.has(label)) set.delete(label);
    else set.add(label);
    // Empty selection = all classes (more intuitive than "detect nothing").
    aiEnabledClasses = set.size > 0 ? [...set] : null;
  }

  function isClassEnabled(label: string): boolean {
    // null = all enabled; otherwise check membership.
    return aiEnabledClasses === null || aiEnabledClasses.includes(label);
  }

  // ─── Models (#185) ──────────────────────────────────────────────────────
  async function loadAiModels() {
    try {
      aiModels = await listAiModels();
    } catch {
      aiModels = [];
    }
  }

  // Human-readable model size for the dropdown.
  function formatBytes(bytes: number): string {
    if (!bytes) return '';
    if (bytes >= 1_000_000) return (bytes / 1_000_000).toFixed(1) + ' MB';
    if (bytes >= 1000) return (bytes / 1000).toFixed(0) + ' KB';
    return bytes + ' B';
  }

  onMount(() => {
    Promise.all([
      loadAiSettings(),
      loadMiBeeVisionKeys(),
      loadZones(),
      loadAiModels(),
      // listCameras is async (returns Promise<Camera[]>); it MUST be awaited,
      // not assigned directly — otherwise allCameras holds a Promise (truthy but
      // .length === undefined) and the per-camera list renders nothing (#180).
      listCameras().catch(() => []),
    ]).then(([, , , , cams]) => {
      allCameras = cams;
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
            max="0.99"
            step="0.01"
          />
          <p class="text-xs th-text-tertiary mt-1">{t('settings.ai.confidenceHint')}</p>
        </div>

        <!-- Frame Skip (#181) -->
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

        <!-- Class Filter (#184) -->
        <div>
          <div class="flex items-center justify-between mb-2">
            <label class="input-label">{t('settings.ai.classFilter')}</label>
            <span class="text-sm font-medium th-text-primary">
              {aiEnabledClasses === null ? t('settings.ai.allClasses') : `${aiEnabledClasses.length}`}
            </span>
          </div>
          <p class="text-xs th-text-tertiary mb-2">{t('settings.ai.classFilterHint')}</p>
          <!-- Presets -->
          <div class="flex flex-wrap gap-2 mb-2">
            {#each CLASS_PRESETS as preset}
              <button
                type="button"
                class="px-2 py-1 text-xs rounded-md border th-border th-bg-hover hover:th-bg-hover transition-colors {classesEqual(aiEnabledClasses, preset.classes) ? 'ring-1 ring-blue-500' : ''}"
                onclick={() => applyClassPreset(preset.classes)}
              >
                {t(preset.label)}
              </button>
            {/each}
          </div>
          <!-- Expandable detailed selection -->
          <button
            type="button"
            class="text-xs th-text-secondary hover:th-text-primary flex items-center gap-1"
            onclick={() => (showClassFilter = !showClassFilter)}
          >
            <ChevronDown size={12} class="transition-transform {showClassFilter ? 'rotate-180' : ''}" />
            {t('settings.ai.classFilterDetails')}
          </button>
          {#if showClassFilter}
            <div class="mt-2 p-3 rounded-md border th-border grid grid-cols-2 sm:grid-cols-3 gap-2 th-bg-hover">
              {#each COMMON_CLASSES as cls}
                <label class="flex items-center gap-2 text-xs th-text-secondary cursor-pointer">
                  <input
                    type="checkbox"
                    class="accent-blue-600"
                    checked={isClassEnabled(cls)}
                    onchange={() => toggleClass(cls)}
                  />
                  {cls}
                </label>
              {/each}
            </div>
          {/if}
        </div>

        <!-- Advanced (#183): EMA + MaxAge -->
        <div>
          <button
            type="button"
            class="text-xs th-text-secondary hover:th-text-primary flex items-center gap-1"
            onclick={() => (showAdvanced = !showAdvanced)}
          >
            <ChevronDown size={12} class="transition-transform {showAdvanced ? 'rotate-180' : ''}" />
            {t('settings.ai.advanced')}
          </button>
          {#if showAdvanced}
            <div class="mt-2 p-3 rounded-md border th-border space-y-4 th-bg-hover">
              <!-- EMA alpha -->
              <div>
                <div class="flex items-center justify-between mb-2">
                  <label class="input-label text-sm" for="ai-ema-alpha">{t('settings.ai.emaAlpha')}</label>
                  <span class="text-sm th-text-secondary">{aiEmaAlpha.toFixed(1)}</span>
                </div>
                <input
                  id="ai-ema-alpha"
                  type="range"
                  class="w-full h-2 rounded-full appearance-none cursor-pointer th-bg-tertiary accent-blue-600"
                  bind:value={aiEmaAlpha}
                  min="0.1"
                  max="0.9"
                  step="0.1"
                />
                <p class="text-xs th-text-tertiary mt-1">{t('settings.ai.emaAlphaHint')}</p>
              </div>
              <!-- Max age -->
              <div>
                <div class="flex items-center justify-between mb-2">
                  <label class="input-label text-sm" for="ai-max-age">{t('settings.ai.maxAge')}</label>
                  <span class="text-sm th-text-secondary">{aiMaxAge}</span>
                </div>
                <input
                  id="ai-max-age"
                  type="range"
                  class="w-full h-2 rounded-full appearance-none cursor-pointer th-bg-tertiary accent-blue-600"
                  bind:value={aiMaxAge}
                  min="3"
                  max="30"
                  step="1"
                />
                <!-- Show the equivalent dwell time so users understand maxAge in
                     real seconds rather than abstract "detection cycles" (#183). -->
                <p class="text-xs th-text-tertiary mt-1">
                  {t('settings.ai.maxAgeHint', { values: { secs: (aiMaxAge / (30 / aiFrameSkip)).toFixed(1) } })}
                </p>
              </div>
            </div>
          {/if}
        </div>

        <!-- Model selection (#185) + Backend Info -->
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <div class="p-3 rounded-md th-bg-hover border th-border">
            <div class="text-xs th-text-tertiary mb-1">{t('settings.ai.modelInfo')}</div>
            {#if aiModels.length > 0}
              <select class="input mt-1 text-sm" bind:value={aiModelUrl}>
                {#each aiModels as m (m.url)}
                  <option value={m.url}>{m.name} ({formatBytes(m.size)})</option>
                {/each}
              </select>
            {:else}
              <div class="text-sm font-medium th-text-primary">{t('settings.ai.modelName')}</div>
              <p class="text-xs th-text-tertiary mt-1">{t('settings.ai.noModelsHint')}</p>
            {/if}
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

            {#if expandedCameras[cam.id]}
              <div class="p-3 border-t th-border space-y-4 th-bg-hover">
                <p class="text-xs th-text-tertiary">{t('settings.ai.perCameraHint')}</p>
                <!-- Per-camera confidence -->
                <div>
                  <div class="flex items-center justify-between mb-2">
                    <label class="input-label text-sm" for="percam-conf-{cam.id}">{t('settings.ai.confidenceThreshold')}</label>
                    <span class="text-sm th-text-secondary">{Math.round((getCameraConfig(cam.id).confidenceThreshold ?? 0.5) * 100)}%</span>
                  </div>
                  <input
                    id="percam-conf-{cam.id}"
                    type="range"
                    class="w-full h-2 rounded-full appearance-none cursor-pointer th-bg-tertiary accent-blue-600"
                    min="0.1"
                    max="0.99"
                    step="0.01"
                    value={getCameraConfig(cam.id).confidenceThreshold ?? 0.5}
                    oninput={(e) => updatePerCamera(cam.id, { confidenceThreshold: parseFloat(e.currentTarget.value) })}
                  />
                </div>
                <!-- Per-camera frame skip -->
                <div>
                  <div class="flex items-center justify-between mb-2">
                    <label class="input-label text-sm" for="percam-skip-{cam.id}">{t('settings.ai.frameSkip')}</label>
                    <span class="text-sm th-text-secondary">{getCameraConfig(cam.id).frameSkip ?? 3}</span>
                  </div>
                  <input
                    id="percam-skip-{cam.id}"
                    type="range"
                    class="w-full h-2 rounded-full appearance-none cursor-pointer th-bg-tertiary accent-blue-600"
                    min="1"
                    max="10"
                    step="1"
                    value={getCameraConfig(cam.id).frameSkip ?? 3}
                    oninput={(e) => updatePerCamera(cam.id, { frameSkip: parseInt(e.currentTarget.value) })}
                  />
                </div>
              </div>
            {/if}
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
