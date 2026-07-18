<script lang="ts">
  import { onMount } from 'svelte';
  import { getFeatures, updateFeatures, getAiSettings, saveAiSettings, detectAiBackend, listCameras, getFFmpegStatus, getAiStatus, updateAiConfig } from '$lib/api';
  import { getPerCameraAiSettings, savePerCameraAiSettings, getAIZones, createAIZone, deleteAIZone } from '$lib/api';
  import { getSettings, generateAPIKey, revokeAPIKey } from '$lib/api';
  import { getAutoDiscoverSettings, updateAutoDiscoverSettings, type AutoDiscoverSettings } from '$lib/api/settings';
  import { refreshMiBeeVisionStatus } from '$lib/mibeevision-status.svelte';
  import type { Camera, DownloadStatus, Zone, ZoneList, PerCameraAiState } from '$lib/api';
  import type { MiBeeVisionConfig } from '$lib/api';
  import { t } from '$lib/i18n';
  import { AlertTriangle, ChevronDown, Plus, Trash2, X, Copy, Check } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import { friendlyError } from '$lib/errors';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import SettingsTranscodingCard from './SettingsTranscodingCard.svelte';

  // AI Detection state
  let aiEnabled = $state(false);
  let aiConfidenceThreshold = $state(0.5);
  let aiFrameSkip = $state(3);
  let aiDetectedBackend = $state('');
  let aiSettingsSaving = $state(false);

  // Feature toggles state
  let featureFlags = $state<Record<string, boolean>>({});
  let featuresLoading = $state(true);
  let featuresSaving = $state(false);
  let originalFeatureFlags = $state<Record<string, boolean>>({});

  // Camera list for feature toggle affected count
  let allCameras = $state<Camera[]>([]);

  // FFmpeg status for Transcoding badge
  let ffmpegStatus = $state<DownloadStatus | null>(null);

  // --- MiBeeVision Integration ---
  let mibeeVisionKeys = $state<Array<{ name: string; prefix: string; revoked: boolean }>>([]);
  let mibeeVisionLoading = $state(false);
  let newKeyName = $state('');
  let generatingKey = $state(false);
  let newlyGeneratedKey = $state<string | null>(null);
  let copiedKey = $state(false);

  const hasMiBeeVisionKey = $derived(mibeeVisionKeys.length > 0);

  // --- Auto-Discover ---
  let adSettings = $state<AutoDiscoverSettings | null>(null);
  let adOriginal = $state<AutoDiscoverSettings | null>(null);
  let adSaving = $state(false);
  // Local form fields (so the toggle/inputs are reactive before save).
  let adEnabled = $state(false);
  let adScanInterval = $state(60);
  let adListenForHello = $state(true);
  let adNetworkInterface = $state('');
  let adDefaultUsername = $state('');
  let adDefaultPassword = $state(''); // only sent when the user types a new one
  let adIgnoreScopes = $state(''); // comma-separated in the UI
  let adHasPassword = $state(false);
  // Dirty when any field diverges from the loaded config.
  let adDirty = $derived(
    !!adSettings && (
      adEnabled !== adSettings.enabled ||
      Number(adScanInterval) !== adSettings.scan_interval ||
      adListenForHello !== adSettings.listen_for_hello ||
      adNetworkInterface !== adSettings.network_interface ||
      adDefaultUsername !== adSettings.default_username ||
      adIgnoreScopes !== (adSettings.ignore_scopes ?? []).join(', ') ||
      adDefaultPassword !== ''
    )
  );

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
  async function loadAiSettings() {
    try {
      const status = await getAiStatus();
      aiEnabled = status.enabled;
      aiConfidenceThreshold = status.confidence_threshold;
      aiFrameSkip = status.frame_skip_rate;
    } catch (e) {
      console.warn('Failed to load AI status from backend, falling back to localStorage:', e);
      const settings = getAiSettings();
      aiEnabled = settings.enabled;
      aiConfidenceThreshold = settings.confidenceThreshold;
      aiFrameSkip = settings.frameSkip;
    }
    aiDetectedBackend = detectAiBackend();
  }

  async function saveAiSettingsLocal() {
    aiSettingsSaving = true;
    try {
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
      showToast(t('settings.ai.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.ai.saveError'), 'error');
    } finally {
      aiSettingsSaving = false;
    }
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
    showToast(t('settings.ai.zoneValidationRequired'), 'error');
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
      showToast(t('settings.ai.createZoneFailed', { error: t('settings.ai.zoneMinPoints') }), 'error');
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
        : t('settings.ai.createZoneFailed', { error: t('settings.ai.unknownError') }),
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

  // --- MiBeeVision Integration ---
  async function loadMiBeeVisionKeys() {
    mibeeVisionLoading = true;
    try {
      const settings = await getSettings();
      mibeeVisionKeys = settings.mibeevision?.api_keys || [];
    } catch (e) { /* non-critical */ } finally {
      mibeeVisionLoading = false;
    }
  }

  async function handleGenerateKey() {
    generatingKey = true;
    try {
      const name = newKeyName.trim() || 'mibeevision';
      const result = await generateAPIKey(name);
      newlyGeneratedKey = result.key;
      newKeyName = '';
      await loadMiBeeVisionKeys();
      await refreshMiBeeVisionStatus();
      showToast(t('settings.mibeevision.keyGenerated'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.mibeevision.keyGenFailed'), 'error');
    } finally {
      generatingKey = false;
    }
  }

  async function handleRevokeKey(name: string) {
    if (!confirm(t('settings.mibeevision.confirmRevoke', { name }))) return;
    try {
      await revokeAPIKey(name);
      await loadMiBeeVisionKeys();
      await refreshMiBeeVisionStatus();
      showToast(t('settings.mibeevision.keyRevoked'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.mibeevision.keyRevokeFailed'), 'error');
    }
  }

  async function copyKey() {
    if (!newlyGeneratedKey) return;
    try {
      await navigator.clipboard.writeText(newlyGeneratedKey);
      copiedKey = true;
      setTimeout(() => { copiedKey = false; }, 2000);
    } catch { /* clipboard may not be available */ }
  }

  // --- Auto-Discover load/save ---
  async function loadAutoDiscover() {
    try {
      const cfg = await getAutoDiscoverSettings();
      adSettings = cfg;
      adOriginal = cfg;
      adEnabled = cfg.enabled;
      adScanInterval = cfg.scan_interval;
      adListenForHello = cfg.listen_for_hello;
      adNetworkInterface = cfg.network_interface;
      adDefaultUsername = cfg.default_username;
      adHasPassword = cfg.has_default_password;
      adIgnoreScopes = (cfg.ignore_scopes ?? []).join(', ');
      adDefaultPassword = ''; // never pre-fill; only sent when the user types
    } catch (e: any) {
      showToast(friendlyError(e, 'settings.autoDiscover.loadFailed'), 'error');
    }
  }

  async function saveAutoDiscover() {
    if (adSaving) return;
    adSaving = true;
    try {
      const payload: Record<string, unknown> = {
        enabled: adEnabled,
        scan_interval: Number(adScanInterval),
        listen_for_hello: adListenForHello,
        network_interface: adNetworkInterface,
        default_username: adDefaultUsername,
        ignore_scopes: adIgnoreScopes.split(',').map((s) => s.trim()).filter(Boolean),
      };
      // Only send the password when the user typed a new one (empty = unchanged).
      if (adDefaultPassword) payload.default_password = adDefaultPassword;
      await updateAutoDiscoverSettings(payload);
      showToast(t('settings.autoDiscover.saved'), 'success');
      adDefaultPassword = '';
      await loadAutoDiscover(); // refresh has_default_password
    } catch (e: any) {
      showToast(friendlyError(e, 'settings.autoDiscover.saveFailed'), 'error');
    } finally {
      adSaving = false;
    }
  }

  onMount(() => {
    loadFeatures();
    loadAiSettings();
    loadCameraList();
    loadPerCameraAiSettings();
    loadZones();
    loadFFmpegStatus();
    loadMiBeeVisionKeys();
    loadAutoDiscover();
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
        <button type="button" class="btn btn-primary" onclick={saveAiSettingsLocal} disabled={aiSettingsSaving}>
          {#if aiSettingsSaving}
            <span class="spinner mr-2"></span>
          {/if}
          {t('settings.save')}
        </button>
      </div>
    </div>
  {/if}
</SettingsCard>

<!-- Auto-Discover Cameras -->
<SettingsCard
  title={t('settings.autoDiscover.title')}
  subtitle={t('settings.autoDiscover.description')}
  badge={adEnabled
    ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
    : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }}
>
  <div class="flex items-center justify-between mb-4">
    <span class="text-sm th-text-secondary">{t('settings.autoDiscover.enabled')}</span>
    <button
      aria-label={t('settings.autoDiscover.enabled')}
      type="button"
      class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {adEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
      onclick={() => { adEnabled = !adEnabled; }}
      onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); adEnabled = !adEnabled; } }}
      role="switch"
      aria-checked={adEnabled}
    >
      <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {adEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
    </button>
  </div>

  {#if adEnabled}
    <div class="space-y-6">
      <!-- Scan interval -->
      <div>
        <label class="input-label" for="ad-scan">{t('settings.autoDiscover.scanInterval')}</label>
        <input id="ad-scan" type="number" min="30" step="10" class="input mt-1 w-full" bind:value={adScanInterval} />
        <p class="text-xs th-text-tertiary mt-1">{t('settings.autoDiscover.scanIntervalHint')}</p>
      </div>

      <!-- Listen for Hello -->
      <div class="flex items-center justify-between">
        <div>
          <span class="text-sm th-text-primary">{t('settings.autoDiscover.listenForHello')}</span>
          <p class="text-xs th-text-tertiary mt-0.5">{t('settings.autoDiscover.listenForHelloHint')}</p>
        </div>
        <button
          aria-label={t('settings.autoDiscover.listenForHello')}
          type="button"
          class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none {adListenForHello ? 'bg-blue-600' : 'th-bg-tertiary'}"
          onclick={() => { adListenForHello = !adListenForHello; }}
          role="switch" aria-checked={adListenForHello}
        >
          <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {adListenForHello ? 'translate-x-6' : 'translate-x-1'}"></span>
        </button>
      </div>

      <!-- Network interface -->
      <div>
        <label class="input-label" for="ad-iface">{t('settings.autoDiscover.networkInterface')}</label>
        <input id="ad-iface" type="text" class="input mt-1 w-full" bind:value={adNetworkInterface} placeholder="eth0" />
        <p class="text-xs th-text-tertiary mt-1">{t('settings.autoDiscover.networkInterfaceHint')}</p>
      </div>

      <!-- Default credentials -->
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <label class="input-label" for="ad-user">{t('settings.autoDiscover.defaultUsername')}</label>
          <input id="ad-user" type="text" class="input mt-1 w-full" bind:value={adDefaultUsername} autocomplete="username" />
          <p class="text-xs th-text-tertiary mt-1">{t('settings.autoDiscover.defaultUsernameHint')}</p>
        </div>
        <div>
          <label class="input-label" for="ad-pass">{t('settings.autoDiscover.defaultPassword')}</label>
          <input id="ad-pass" type="password" class="input mt-1 w-full" bind:value={adDefaultPassword} autocomplete="new-password" placeholder={adHasPassword ? '••••••••' : ''} />
          <p class="text-xs th-text-tertiary mt-1">{adHasPassword ? t('settings.autoDiscover.passwordSet') : t('settings.autoDiscover.defaultPasswordHint')}</p>
        </div>
      </div>

      <!-- Ignore scopes -->
      <div>
        <label class="input-label" for="ad-scopes">{t('settings.autoDiscover.ignoreScopes')}</label>
        <input id="ad-scopes" type="text" class="input mt-1 w-full" bind:value={adIgnoreScopes} placeholder="hardware/LegacyCam" />
        <p class="text-xs th-text-tertiary mt-1">{t('settings.autoDiscover.ignoreScopesHint')}</p>
      </div>

      <!-- Save -->
      <div class="flex justify-end">
        <button type="button" class="btn btn-primary" onclick={saveAutoDiscover} disabled={adSaving || !adDirty}>
          {#if adSaving}<span class="spinner mr-2"></span>{/if}
          {t('settings.autoDiscover.save')}
        </button>
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
    : { text: t('settings.mibeevision.notConnected'), color: 'neutral' as const }
  }
>
  <div class="space-y-4">
    <p class="text-sm th-text-secondary">{t('settings.mibeevision.setupGuide')}</p>

    <!-- Newly generated key (shown once) -->
    {#if newlyGeneratedKey}
      <div class="p-4 rounded-md border border-green-500/50 th-bg-success-light space-y-2">
        <div class="flex items-center gap-2 text-green-700 dark:text-green-400 font-medium text-sm">
          <Check size={16} />
          {t('settings.mibeevision.keyGenerated')}
        </div>
        <p class="text-xs th-color-warning">{t('settings.mibeevision.copyWarning')}</p>
        <div class="flex items-center gap-2">
          <code class="flex-1 p-2 rounded bg-black/10 dark:bg-white/10 text-sm font-mono break-all">
            {newlyGeneratedKey}
          </code>
          <button type="button" class="btn btn-secondary btn-sm" onclick={copyKey}>
            {#if copiedKey}<Check size={14} />{:else}<Copy size={14} />{/if}
          </button>
        </div>
        <button type="button" class="btn btn-ghost btn-sm text-xs" onclick={() => newlyGeneratedKey = null}>
          {t('common.dismiss')}
        </button>
      </div>
    {/if}

    <!-- Existing keys -->
    {#if mibeeVisionLoading}
      <div class="flex items-center gap-2 py-2 th-text-muted">
        <span class="spinner"></span>
        <span class="text-sm">{t('common.loading')}</span>
      </div>
    {:else if mibeeVisionKeys.length > 0}
      <div class="space-y-2">
        {#each mibeeVisionKeys as keyInfo (keyInfo.name)}
          <div class="flex items-center justify-between p-3 rounded-md th-bg-hover border th-border">
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium th-text-primary">{keyInfo.name}</div>
              <code class="text-xs th-text-tertiary font-mono">{keyInfo.prefix}</code>
            </div>
            <button
              type="button"
              class="btn-ghost p-1 text-xs th-color-danger hover:th-bg-danger-light"
              onclick={() => handleRevokeKey(keyInfo.name)}
              title={t('settings.mibeevision.revoke')}
            >
              <Trash2 size={14} />
            </button>
          </div>
        {/each}
      </div>
    {/if}

    <!-- Generate new key -->
    <div class="border-t th-border pt-4">
      <label class="input-label" for="new-api-key-name">{t('settings.mibeevision.keyName')}</label>
      <div class="flex gap-2 mt-1">
        <input
          id="new-api-key-name"
          type="text"
          class="input flex-1"
          placeholder="mibeevision"
          bind:value={newKeyName}
          onkeydown={(e) => { if (e.key === 'Enter') handleGenerateKey(); }}
        />
        <button type="button" class="btn btn-primary" onclick={handleGenerateKey} disabled={generatingKey}>
          {#if generatingKey}<span class="spinner mr-2"></span>{/if}
          {t('settings.mibeevision.generate')}
        </button>
      </div>
    </div>
  </div>
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
