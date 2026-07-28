<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings, getStats } from '$lib/api';
  import type { SettingsConfig, StorageStats } from '$lib/api';
  import { getItemsPerPage, setItemsPerPage, getAutoRefresh, setAutoRefresh } from '$lib/preferences';
  import { t } from '$lib/i18n';
  import { CircleDot, AlertCircle } from 'lucide-svelte';
  import { showToast } from '$lib/toast';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  let settings = $state<SettingsConfig | null>(null);
  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);

  // Form state
  let retentionDays = $state(30);
  let diskThresholdPercent = $state(90);
  let checkInterval = $state('1h');
  let selectedTimezone = $state('Local');
  let timezoneOptions = $derived([
    { value: 'Local', label: t('settings.timezoneLocal') },
    { value: 'UTC', label: 'UTC' },
    { value: 'Asia/Shanghai', label: 'Asia/Shanghai' },
    { value: 'Asia/Hong_Kong', label: 'Asia/Hong_Kong' },
    { value: 'Asia/Singapore', label: 'Asia/Singapore' },
    { value: 'Asia/Tokyo', label: 'Asia/Tokyo' },
    { value: 'America/New_York', label: 'America/New_York' },
    { value: 'America/Los_Angeles', label: 'America/Los_Angeles' },
    { value: 'America/Chicago', label: 'America/Chicago' },
    { value: 'Europe/London', label: 'Europe/London' },
    { value: 'Europe/Berlin', label: 'Europe/Berlin' },
    { value: 'Europe/Paris', label: 'Europe/Paris' },
    { value: 'Australia/Sydney', label: 'Australia/Sydney' },
    { value: 'Pacific/Auckland', label: 'Pacific/Auckland' },
  ]);
  let itemsPerPage = $state(getItemsPerPage());
  let autoRefresh = $state(getAutoRefresh());
  // NOTE: there is no global "fallback protocol" selector here — the Player
  // Orchestrator auto-selects the best protocol per camera (probes
  // /protocols, folds in codec + browser capability, demotes on health
  // failure). Per-camera overrides remain available via the Protocol Switcher
  // on each camera's LiveView page.

  // Disk info from stats API
  let diskInfo = $state<StorageStats | null>(null);

  // Validation
  let validationErrors = $state<Record<string, string>>({});

  // Confirmation dialog for destructive changes
  let showConfirmDialog = $state(false);

  // Original values snapshot for dirty tracking
  let originalSnapshot = $state('');
  let originalRetentionDays = $state(0);

  // Unsaved changes navigation guard
  let showNavGuard = $state(false);
  let pendingHash = $state('');

  // Derived: is any setting dirty?
  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({
      retentionDays, diskThresholdPercent, selectedTimezone,
    });
    return current !== originalSnapshot;
  });

  // Disk GB estimation
  let diskGbEstimate = $derived.by(() => {
    if (!diskInfo || diskInfo.total_bytes === 0) return '';
    const remainingPct = (100 - diskThresholdPercent) / 100;
    const remainingBytes = diskInfo.total_bytes * remainingPct;
    const gb = remainingBytes / (1024 * 1024 * 1024);
    if (gb >= 1) return `≈ ${gb.toFixed(0)} GB`;
    const mb = remainingBytes / (1024 * 1024);
    return `≈ ${mb.toFixed(0)} MB`;
  });

  // Validation
  function validateField(field: string, value: string) {
    const val = parseInt(value);
    if (field === 'retention_days') {
      if (isNaN(val) || val < 0) {
        validationErrors['retention_days'] = t('settings.invalidRetentionDays');
      } else {
        delete validationErrors['retention_days'];
      }
    } else if (field === 'disk_threshold') {
      if (isNaN(val) || val < 0 || val > 100) {
        validationErrors['disk_threshold'] = t('settings.invalidDiskThreshold');
      } else {
        delete validationErrors['disk_threshold'];
      }
    }
  }

  function validate(): boolean {
    validationErrors = {};
    if (retentionDays < 1) {
      validationErrors['retention_days'] = t('settings.validationRetention');
    }
    if (diskThresholdPercent < 0 || diskThresholdPercent > 100) {
      validationErrors['disk_threshold'] = t('settings.validationThreshold');
    }
    return Object.keys(validationErrors).length === 0;
  }

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({
      retentionDays, diskThresholdPercent, selectedTimezone,
    });
    originalRetentionDays = retentionDays;
  }

  async function loadSettings() {
    loading = true;
    error = '';
    try {
      settings = await getSettings();
      retentionDays = settings.cleanup.retention_days;
      diskThresholdPercent = settings.cleanup.disk_threshold_percent;
      // check_interval removed from UI (#153) — backend default (1h) is optimal.
      selectedTimezone = settings.timezone || 'Local';
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function loadDiskInfo() {
    try {
      diskInfo = await getStats();
    } catch (e) { /* non-critical */ }
  }

  // loadStreamingConfig() removed — the fallback-protocol selector is gone
  // (the orchestrator auto-selects). No streaming state to load here anymore.

  async function save() {
    if (!validate()) return;
    // Check if we're reducing retention (destructive change)
    if (retentionDays < originalRetentionDays && originalRetentionDays > 0) {
      showConfirmDialog = true;
      return;
    }
    await performSave();
  }

  async function performSave() {
    saving = true;
    try {
      const payload: SettingsConfig = {
        cleanup: {
          retention_days: retentionDays,
          disk_threshold_percent: diskThresholdPercent,
        },
        timezone: selectedTimezone,
      };
      await updateSettings(payload);

      // Refresh state
      settings = await getSettings();
      captureSnapshot();
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
    } finally {
      saving = false;
    }
  }

  function confirmSave() {
    showConfirmDialog = false;
    performSave();
  }

  function cancelSave() {
    showConfirmDialog = false;
  }

  function handleItemsPerPageChange() {
    setItemsPerPage(itemsPerPage);
  }

  function handleAutoRefreshChange(event: Event) {
    const select = event.target as HTMLSelectElement;
    setAutoRefresh(select.value);
  }

  // Unsaved changes navigation guard
  function handleHashChange(e: HashChangeEvent) {
    const dirty = isDirty;
    if (dirty && !showNavGuard) {
      e.preventDefault();
      pendingHash = window.location.hash;
      showNavGuard = true;
    }
  }

  function confirmNavigation() {
    showNavGuard = false;
    window.removeEventListener('hashchange', handleHashChange);
    if (pendingHash) window.location.hash = pendingHash;
    window.addEventListener('hashchange', handleHashChange);
  }

  function cancelNavigation() {
    showNavGuard = false;
    pendingHash = '';
  }

  onMount(() => {
    loadSettings();
    loadDiskInfo();
    window.addEventListener('hashchange', handleHashChange);
  });

  onDestroy(() => {
    window.removeEventListener('hashchange', handleHashChange);
  });
</script>

<!-- Error message -->
{#if error}
  <div class="card border th-border-danger p-8 text-center">
    <div class="flex justify-center mb-4 th-color-danger">
      <AlertCircle size={48} />
    </div>
    <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
    <p class="th-text-secondary mb-4">{error}</p>
    <button onclick={loadSettings} class="btn btn-primary btn-sm">{t('common.retry')}</button>
  </div>
{:else if loading}
  <div class="card border th-border">
    <div class="p-6 space-y-4">
      <div class="h-6 w-40 th-bg-tertiary rounded animate-pulse"></div>
      <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
      <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div class="space-y-2">
          <div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div>
          <div class="h-10 th-bg-tertiary rounded animate-pulse"></div>
        </div>
        <div class="space-y-2">
          <div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div>
          <div class="h-3 w-full th-bg-tertiary rounded animate-pulse"></div>
          <div class="h-10 th-bg-tertiary rounded animate-pulse"></div>
        </div>
        <div class="space-y-2">
          <div class="h-4 w-28 th-bg-tertiary rounded animate-pulse"></div>
          <div class="h-10 th-bg-tertiary rounded animate-pulse"></div>
        </div>
      </div>
      <div class="flex items-center gap-4 pt-2">
        <div class="h-10 w-28 th-bg-tertiary rounded animate-pulse"></div>
      </div>
    </div>
  </div>
{:else}
  <!-- Timezone -->
  <div class="card p-8 border th-border">
    <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.timezone')}</h3>
    <p class="text-sm th-text-tertiary mb-8">{t('settings.timezoneDesc')}</p>
    <div class="max-w-sm">
      <label for="timezone" class="input-label">{t('settings.timezone')}</label>
      <select id="timezone" class="input" bind:value={selectedTimezone}>
        {#each timezoneOptions as opt}
          <option value={opt.value}>{opt.label}</option>
        {/each}
      </select>
    </div>
  </div>

  <!-- Cleanup Policy -->
  <div class="card p-8 border th-border">
    <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.cleanup')}</h3>
    <p class="text-sm th-text-tertiary mb-8">{t('settings.cleanupDesc')}</p>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div>
        <label for="retention" class="input-label">{t('settings.retentionDays')}</label>
        <input
          id="retention"
          type="number"
          class="input {validationErrors['retention_days'] ? 'border-red-500' : ''}"
          bind:value={retentionDays}
          min="1"
          onblur={() => validateField('retention_days', String(retentionDays))}
          oninput={() => { if (validationErrors['retention_days']) delete validationErrors['retention_days']; }}
        />
        {#if validationErrors['retention_days']}
          <p class="th-color-danger text-xs mt-1" aria-live="polite">{validationErrors['retention_days']}</p>
        {/if}
      </div>

      <div>
        <label for="threshold" class="input-label">{t('settings.diskThreshold', { percent: String(diskThresholdPercent) })}</label>
        <input
          id="threshold"
          type="number"
          class="input {validationErrors['disk_threshold'] ? 'border-red-500' : ''}"
          bind:value={diskThresholdPercent}
          min="0"
          max="100"
          onblur={() => validateField('disk_threshold', String(diskThresholdPercent))}
          oninput={() => { if (validationErrors['disk_threshold']) delete validationErrors['disk_threshold']; }}
        />
        {#if validationErrors['disk_threshold']}
          <p class="th-color-danger text-xs mt-1" aria-live="polite">{validationErrors['disk_threshold']}</p>
        {/if}
        {#if diskGbEstimate}
          <p class="text-xs th-text-muted mt-1">{diskThresholdPercent}% {t('settings.diskRemaining')} {diskGbEstimate}</p>
        {/if}
      </div>
    </div>

    <!-- Check interval removed (#153): backend default (1h) is optimal;
         exposing it added cognitive load with no user-perceivable benefit. -->
  </div>

  <!-- Frontend Preferences -->
  <div class="card p-8 border th-border">
    <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.frontendPrefs')}</h3>
    <p class="text-sm th-text-tertiary mb-8">{t('settings.frontendPrefsDesc')}</p>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div>
        <label for="itemsPerPage" class="input-label">{t('settings.itemsPerPage')}</label>
        <select id="itemsPerPage" class="input" bind:value={itemsPerPage} onchange={handleItemsPerPageChange}>
          <option value={20}>20</option>
          <option value={50}>50</option>
          <option value={100}>100</option>
        </select>
      </div>

      <div>
        <label for="autoRefresh" class="input-label">{t('settings.autoRefresh')}</label>
        <select id="autoRefresh" class="input" bind:value={autoRefresh} onchange={handleAutoRefreshChange}>
          <option value="30s">{t('settings.every30s')}</option>
          <option value="60s">{t('settings.every60s')}</option>
          <option value="120s">{t('settings.every2m')}</option>
          <option value="off">{t('settings.off')}</option>
        </select>
      </div>
    </div>
  </div>

  <!-- Fallback Protocol selector removed — the Player Orchestrator now
       auto-selects the best protocol per camera based on codec + browser
       capabilities. There is no streaming protocol for a user to pick on this
       page; per-camera overrides remain available via the Protocol Switcher on
       each camera's live view. -->

  <!-- Save button -->
  <div class="flex items-center gap-4 pt-2">
    <button onclick={save} class="btn btn-primary" disabled={saving || !isDirty}>
      {#if saving}
        <span class="spinner mr-2"></span>
        {t('settings.saving')}
      {:else}
        <span class="flex items-center gap-1">
          {t('settings.save')}
          {#if isDirty}
            <CircleDot size={12} class="th-color-warning" />
          {/if}
        </span>
      {/if}
    </button>
  </div>
{/if}

<!-- Destructive change confirmation -->
{#if showConfirmDialog}
  <ConfirmDialog
    title={t('settings.confirmSaveTitle')}
    message={t('settings.confirmSaveMessage')}
    confirmText={t('settings.confirmSave')}
    onconfirm={confirmSave}
    oncancel={cancelSave}
    variant="danger"
  />
{/if}

<!-- Unsaved changes navigation guard -->
{#if showNavGuard}
  <ConfirmDialog
    title={t('settings.unsavedTitle')}
    message={t('settings.unsavedMessage')}
    confirmText={t('settings.unsavedDiscard')}
    onconfirm={confirmNavigation}
    oncancel={cancelNavigation}
    variant="danger"
  />
{/if}
