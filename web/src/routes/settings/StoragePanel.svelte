<script lang="ts">
  // 存储设置 (Storage) — retention + disk threshold + WebDAV.
  // Part of the unified settings shell (#153): no save button here; the
  // shell drives save/reset via the settingsForm coordinator.
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings, getStats } from '$lib/api';
  import type { SettingsConfig, StorageStats } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';

  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);

  // Form state — cleanup
  let retentionDays = $state(30);
  let diskThresholdPercent = $state(90);

  // Form state — WebDAV
  let webdavEnabled = $state(false);
  let webdavReadWrite = $state(false);

  // Disk info from stats API
  let diskInfo = $state<StorageStats | null>(null);

  // Validation
  let validationErrors = $state<Record<string, string>>({});

  // Originals snapshot for dirty tracking + destructive detection
  let originalSnapshot = $state('');
  let originalRetentionDays = $state(0);
  let originalWebdavEnabled = $state(false);
  let originalWebdavReadWrite = $state(false);

  // Derived: is any storage setting dirty?
  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({
      retentionDays, diskThresholdPercent, webdavEnabled, webdavReadWrite,
    });
    return current !== originalSnapshot;
  });

  // Disk GB estimate remaining after the cleanup threshold is applied.
  let diskGbEstimate = $derived.by(() => {
    if (!diskInfo || diskInfo.total_bytes === 0) return '';
    const remainingPct = (100 - diskThresholdPercent) / 100;
    const remainingBytes = diskInfo.total_bytes * remainingPct;
    const gb = remainingBytes / (1024 * 1024 * 1024);
    if (gb >= 1) return `≈ ${gb.toFixed(0)} GB`;
    const mb = remainingBytes / (1024 * 1024);
    return `≈ ${mb.toFixed(0)} MB`;
  });

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({
      retentionDays, diskThresholdPercent, webdavEnabled, webdavReadWrite,
    });
    originalRetentionDays = retentionDays;
    originalWebdavEnabled = webdavEnabled;
    originalWebdavReadWrite = webdavReadWrite;
  }

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
    if (retentionDays < 1 || retentionDays > 3650) {
      validationErrors['retention_days'] = t('settings.validationRetention');
    }
    if (diskThresholdPercent < 0 || diskThresholdPercent > 100) {
      validationErrors['disk_threshold'] = t('settings.validationThreshold');
    }
    return Object.keys(validationErrors).length === 0;
  }

  async function loadAll() {
    loading = true;
    error = '';
    try {
      const settings = await getSettings();
      retentionDays = settings.cleanup.retention_days;
      diskThresholdPercent = settings.cleanup.disk_threshold_percent;
      webdavEnabled = settings.webdav.enabled;
      webdavReadWrite = settings.webdav.read_write;
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
    // Disk info is non-critical — load separately so a stats failure doesn't
    // block the rest of the panel.
    diskInfo = await getStats().catch(() => null);
  }

  async function performSave() {
    if (!validate()) return;
    saving = true;
    try {
      // The PUT /settings endpoint accepts cleanup + webdav + timezone in a
      // single payload. We only send the fields this panel owns; timezone is
      // omitted (server preserves the existing value).
      const payload: SettingsConfig = {
        cleanup: {
          retention_days: retentionDays,
          disk_threshold_percent: diskThresholdPercent,
          // check_interval is required by the type but handled by backend
          // defaults; pass an empty string so the server keeps its current value.
          check_interval: '',
        },
        webdav: {
          enabled: webdavEnabled,
          read_write: webdavReadWrite,
          // path_prefix preserved by backend when blank.
          path_prefix: '',
        },
      };
      await updateSettings(payload);
      await loadAll();
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      // Swallow + toast, matching the other settings tabs. The unified shell's
      // saveAll iterates panels and surfaces its own error handling.
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
    } finally {
      saving = false;
    }
  }

  function resetForm() {
    // Restore from the last captured snapshot.
    retentionDays = originalRetentionDays;
    webdavEnabled = originalWebdavEnabled;
    webdavReadWrite = originalWebdavReadWrite;
    // Threshold original isn't tracked separately — reparse from snapshot.
    try {
      const snap = JSON.parse(originalSnapshot);
      if (typeof snap.diskThresholdPercent === 'number') diskThresholdPercent = snap.diskThresholdPercent;
    } catch { /* ignore */ }
    validationErrors = {};
  }

  let unregister: (() => void) | undefined;
  onMount(() => {
    loadAll();
    unregister = settingsForm.register('storage', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        // Destructive if: reducing retention days, disabling WebDAV, or
        // changing WebDAV read-write mode.
        if (retentionDays < originalRetentionDays && originalRetentionDays > 0)
          return t('settings.destructive.retentionReduce', { days: String(retentionDays) });
        if (originalWebdavEnabled && !webdavEnabled)
          return t('settings.destructive.webdavDisable');
        if (originalWebdavReadWrite !== webdavReadWrite && webdavEnabled)
          return t('settings.destructive.webdavRWChange');
        return null;
      },
    });
  });

  onDestroy(() => unregister?.());
</script>

{#if loading}
  <div class="card border th-border">
    <div class="p-6 space-y-4">
      <div class="h-6 w-40 th-bg-tertiary rounded animate-pulse"></div>
      <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div class="space-y-2"><div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
        <div class="space-y-2"><div class="h-4 w-32 th-bg-tertiary rounded animate-pulse"></div><div class="h-3 w-full th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
      </div>
    </div>
  </div>
{:else if error}
  <div class="card border th-border-danger p-8 text-center">
    <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
    <p class="th-text-secondary mb-4">{error}</p>
    <button onclick={loadAll} class="btn btn-primary btn-sm">{t('common.retry')}</button>
  </div>
{:else}
  <!-- Cleanup Policy: retention + disk threshold -->
  <SettingsCard title={t('settings.cleanup')} subtitle={t('settings.cleanupDesc')}>
    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div>
        <label for="retention" class="input-label">{t('settings.retentionDays')}</label>
        <input
          id="retention"
          type="number"
          class="input {validationErrors['retention_days'] ? 'border-red-500' : ''}"
          bind:value={retentionDays}
          min="1"
          max="3650"
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
    <!-- check_interval removed (#153): backend default (1h) is optimal. -->
  </SettingsCard>

  <!-- WebDAV: enable + read-write toggles -->
  <SettingsCard title={t('settings.webdav')} subtitle={t('settings.advanced.webdav.description')}>
    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <!-- Enable WebDAV -->
      <div>
        <span class="input-label">{t('settings.webdavEnabled')}</span>
        <div class="flex items-center gap-3 mt-2">
          <Toggle checked={webdavEnabled} onChange={(v) => { webdavEnabled = v; }} label={t('settings.webdavEnabled')} />
          <span class="text-sm th-text-secondary">{webdavEnabled ? t('settings.webdavEnabledOn') : t('settings.webdavEnabledOff')}</span>
        </div>
      </div>

      <!-- Read-Write Mode -->
      <div>
        <span class="input-label">{t('settings.webdavReadWrite')}</span>
        <div class="flex items-center gap-3 mt-2">
          <Toggle checked={webdavReadWrite} onChange={(v) => { webdavReadWrite = v; }} label={t('settings.webdavReadWrite')} />
          <span class="text-sm th-text-secondary">{webdavReadWrite ? t('settings.webdavReadWriteOn') : t('settings.webdavReadWriteOff')}</span>
        </div>
        <p class="text-xs th-text-tertiary mt-2">{t('settings.webdavReadWriteHint')}</p>
      </div>
    </div>
    <!-- WebDAV path_prefix removed from UI (#153): /dav is almost never changed.
         Backend preserves the stored value when blank. -->
  </SettingsCard>
{/if}
