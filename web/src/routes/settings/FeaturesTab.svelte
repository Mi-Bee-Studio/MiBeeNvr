<script lang="ts">
  import { onMount } from 'svelte';
  import { getFeatures, updateFeatures, getAiSettings, saveAiSettings, detectAiBackend, listCameras } from '$lib/api';
  import type { Camera } from '$lib/api';
  import { t } from '$lib/i18n';
  import { AlertTriangle } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
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

  // Feature flags dirty tracking
  let featuresDirty = $derived(JSON.stringify(featureFlags) !== JSON.stringify(originalFeatureFlags));

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

  onMount(() => {
    loadFeatures();
    loadAiSettings();
    loadCameraList();
  });
</script>

<!-- AI Detection -->
<div class="card p-8 border th-border">
  <div class="flex items-center justify-between mb-1">
    <div>
      <h3 class="text-lg font-semibold th-text-primary">{t('settings.ai.title')}</h3>
      <p class="text-sm th-text-secondary mt-1">{t('settings.ai.description')}</p>
    </div>
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
  </div>

  {#if aiEnabled}
    <div class="mt-4 pt-4 border-t th-border space-y-6">
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
</div>

<!-- Feature Toggles -->
<div class="card p-8 border th-border">
  <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.featureToggles.title')}</h3>
  <p class="text-sm th-text-secondary mt-1 mb-3">{t('settings.advanced.features.description')}</p>

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
</div>

<!-- Transcoding -->
<SettingsTranscodingCard />
