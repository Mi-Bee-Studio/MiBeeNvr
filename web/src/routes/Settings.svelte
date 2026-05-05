<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings } from '$lib/api';
  import { getItemsPerPage, setItemsPerPage, getAutoRefresh, setAutoRefresh } from '../lib/preferences';
  import type { SettingsConfig } from '$lib/api';
  import { t } from '$lib/i18n';
  import { AlertCircle, Settings as SettingsIcon } from 'lucide-svelte';
  import { showToast } from '$lib/toast';

  let settings = $state<SettingsConfig | null>(null);
  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);

// Form state
let retentionDays = $state(30);
let diskThresholdPercent = $state(90);
let checkInterval = $state('1h');
let itemsPerPage = $state(getItemsPerPage());
  let autoRefresh = $state(getAutoRefresh());
let webdavEnabled = $state(false);
let webdavPathPrefix = $state('/dav');
let webdavReadWrite = $state(false);
  
  // Original values for change tracking
  let originalRetentionDays = $state(30);

  // Validation
  let validationErrors = $state<Record<string, string>>({});


  // Confirmation dialog
  let showConfirmDialog = $state(false);
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

  async function loadSettings() {
    loading = true;
    error = '';

    try {
      settings = await getSettings();
retentionDays = settings.cleanup.retention_days;
diskThresholdPercent = settings.cleanup.disk_threshold_percent;
      checkInterval = settings.cleanup.check_interval;
      originalRetentionDays = settings.cleanup.retention_days;
      webdavEnabled = settings.webdav?.enabled ?? false;
      webdavPathPrefix = settings.webdav?.path_prefix ?? '/dav';
      webdavReadWrite = settings.webdav?.read_write ?? false;
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

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
          check_interval: checkInterval,
        },
        webdav: {
          enabled: webdavEnabled,
          path_prefix: webdavPathPrefix,
          read_write: webdavReadWrite,
        },
      };

      const result = await updateSettings(payload);
      settings = await getSettings();
      originalRetentionDays = settings.cleanup.retention_days;
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

  onMount(() => {
    loadSettings();
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold th-text-primary">{t('settings.title')}</h2>
    </div>

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
    {/if}

    <!-- Loading state -->
    {#if loading}
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
      <div class="space-y-6">
        <!-- Cleanup Policy -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.cleanup')}</h3>
          <p class="text-sm th-text-tertiary mb-8">{t('settings.cleanupDesc')}</p>

          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <!-- Retention Days -->
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

            <!-- Disk Threshold -->
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
            </div>

            <!-- Check Interval -->
            <div>
              <label for="interval" class="input-label">{t('settings.checkInterval')}</label>
              <select id="interval" class="input" bind:value={checkInterval}>
                <option value="30m">{t('settings.every30m')}</option>
                <option value="1h">{t('settings.every1h')}</option>
                <option value="6h">{t('settings.every6h')}</option>
                <option value="24h">{t('settings.every24h')}</option>
              </select>
            </div>
          </div>
        </div>

        <!-- WebDAV Settings -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.webdav')}</h3>
          <p class="text-sm th-text-tertiary mb-8">{t('settings.webdavDesc')}</p>

          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <!-- Enable WebDAV -->
            <div>
              <label class="input-label">{t('settings.webdavEnabled')}</label>
              <div class="flex items-center gap-3 mt-2">
                <button
                  type="button"
                  class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {webdavEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
                  onclick={() => { webdavEnabled = !webdavEnabled; }}
                  role="switch"
                  aria-checked={webdavEnabled}
                >
                  <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {webdavEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
                </button>
                <span class="text-sm th-text-secondary">{webdavEnabled ? t('settings.webdavEnabledOn') : t('settings.webdavEnabledOff')}</span>
              </div>
            </div>

            <!-- Path Prefix -->
            <div>
              <label for="webdavPrefix" class="input-label">{t('settings.webdavPathPrefix')}</label>
              <input
                id="webdavPrefix"
                type="text"
                class="input"
                bind:value={webdavPathPrefix}
                placeholder="/dav"
              />
            </div>

            <!-- Read-Write Mode -->
            <div>
              <label class="input-label">{t('settings.webdavReadWrite')}</label>
              <div class="flex items-center gap-3 mt-2">
                <button
                  type="button"
                  class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {webdavReadWrite ? 'bg-blue-600' : 'th-bg-tertiary'}"
                  onclick={() => { webdavReadWrite = !webdavReadWrite; }}
                  role="switch"
                  aria-checked={webdavReadWrite}
                >
                  <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {webdavReadWrite ? 'translate-x-6' : 'translate-x-1'}"></span>
                </button>
                <span class="text-sm th-text-secondary">{webdavReadWrite ? t('settings.webdavReadWriteOn') : t('settings.webdavReadWriteOff')}</span>
              </div>
              <p class="text-xs th-text-tertiary mt-2">{t('settings.webdavReadWriteHint')}</p>
            </div>
          </div>
        </div>

        <!-- Frontend Preferences -->
        <div class="card p-8 border th-border">
          <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.frontendPrefs')}</h3>
          <p class="text-sm th-text-tertiary mb-8">{t('settings.frontendPrefsDesc')}</p>

          <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            <!-- Items Per Page -->
            <div>
              <label for="itemsPerPage" class="input-label">{t('settings.itemsPerPage')}</label>
              <select id="itemsPerPage" class="input" bind:value={itemsPerPage} onchange={handleItemsPerPageChange}>
                <option value={20}>20</option>
                <option value={50}>50</option>
                <option value={100}>100</option>
              </select>
            </div>

            <!-- Auto Refresh -->
            <div>
              <label for="autoRefresh" class="input-label">{t('settings.autoRefresh')}</label>
              <select id="autoRefresh" class="input" bind:value={autoRefresh} onchange={handleAutoRefreshChange}>
                <option value="10s">{t('settings.every10s')}</option>
                <option value="30s">{t('settings.every30s')}</option>
                <option value="60s">{t('settings.every60s')}</option>
                <option value="off">{t('settings.off')}</option>
              </select>
            </div>
          </div>
        </div>

        <!-- Save button -->
        <div class="flex items-center gap-4 pt-2">
          <button
            onclick={save}
            class="btn btn-primary"
            disabled={saving}
          >
            {#if saving}
              <span class="spinner mr-2"></span>
              {t('settings.saving')}
            {:else}
              {t('settings.save')}
            {/if}
          </button>
        </div>
      </div>
    {/if}
  </main>

  <!-- Save confirmation modal -->
  {#if showConfirmDialog}
    <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <div class="card max-w-md w-full p-6">
        <h3 class="text-lg font-semibold th-text-primary mb-4">{t('settings.confirmSaveTitle')}</h3>
        <p class="th-text-secondary mb-6">
          {t('settings.confirmSaveMessage')}
        </p>
        <div class="flex gap-3 justify-end">
          <button
            onclick={cancelSave}
            class="btn btn-secondary"
          >
            {t('recordings.cancel')}
          </button>
          <button
            onclick={confirmSave}
            class="btn btn-danger"
          >
            {t('settings.save')}
          </button>
</div>
      </div>
    </div>
  {/if}
</div>