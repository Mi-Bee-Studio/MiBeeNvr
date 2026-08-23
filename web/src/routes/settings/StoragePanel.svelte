<script lang="ts">
  // 存储设置 (Storage) — recording root (#395) + retention + disk threshold + WebDAV.
  // Part of the unified settings shell (#153): no save button here; the
  // shell drives save/reset via the settingsForm coordinator.
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings, getStats, getStorageCandidates, addStorageCandidate, removeStorageCandidate, startStorageMigrate, getStorageMigrateStatus } from '$lib/api';
  import type { SettingsConfig, StorageStats, StorageCandidatesResponse, StorageMigrateStatusResponse, MigrationJob } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';

  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);

  // Form state — recording root (#395)
  let storageRoot = $state('');
  let storageCandidates = $state<StorageCandidatesResponse | null>(null);
  let storageRestartRequired = $state(false);

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
  let originalStorageRoot = $state('');
  let originalWebdavEnabled = $state(false);
  let originalWebdavReadWrite = $state(false);

  // Derived: is any storage setting dirty?
  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({
      storageRoot, retentionDays, diskThresholdPercent, webdavEnabled, webdavReadWrite,
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
      storageRoot, retentionDays, diskThresholdPercent, webdavEnabled, webdavReadWrite,
    });
    originalRetentionDays = retentionDays;
    originalStorageRoot = storageRoot;
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
      storageRoot = settings.storage?.root_dir ?? '';
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
    // Candidates + disk info are non-critical — load separately so their
    // failure doesn't block the rest of the panel.
    storageCandidates = await getStorageCandidates().catch(() => null);
    if (storageCandidates && !storageRoot) storageRoot = storageCandidates.current;
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
        // Recording root (#395): partial-PUT only when this panel actually
        // changed it — the backend applies it on the NEXT start and answers
        // restart_required=true, which we surface as a toast below.
        ...(storageRoot !== originalStorageRoot && storageRoot
          ? { storage: { root_dir: storageRoot } }
          : {}),
        cleanup: {
          retention_days: retentionDays,
          disk_threshold_percent: diskThresholdPercent,
          // check_interval intentionally omitted: this panel no longer exposes
          // it (1h backend default is optimal). Sending "" used to 400 the save
          // (#294) because the server runs time.ParseDuration("") on it. The
          // server treats an absent field as "keep current" (partial PUT).
        },
        webdav: {
          enabled: webdavEnabled,
          read_write: webdavReadWrite,
          // path_prefix preserved by backend when blank.
          path_prefix: '',
        },
      };
      const res = await updateSettings(payload);
      storageRestartRequired = !!res.restart_required;
      await loadAll();
      if (storageRestartRequired) {
        showToast(t('settings.storageRootRestartHint'), 'success');
        storageRestartRequired = false;
      } else {
        showToast(t('settings.saved'), 'success');
      }
    } catch (e) {
      // Surface to the unified shell (#160): saveAll's contract is "throws on
      // first error" so the shell can keep the dirty bar visible and report
      // failure. Without re-throwing, the shell would think this panel saved
      // OK and clear its dirty state, hiding a backend failure from the user.
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e;
    } finally {
      saving = false;
    }
  }

  function resetForm() {
    // Restore from the last captured snapshot.
    retentionDays = originalRetentionDays;
    storageRoot = originalStorageRoot;
    webdavEnabled = originalWebdavEnabled;
    webdavReadWrite = originalWebdavReadWrite;
    // Threshold original isn't tracked separately — reparse from snapshot.
    try {
      const snap = JSON.parse(originalSnapshot);
      if (typeof snap.diskThresholdPercent === 'number') diskThresholdPercent = snap.diskThresholdPercent;
    } catch { /* ignore */ }
    validationErrors = {};
  }

  // ── Runtime candidate management (#395): add a mounted path / drop an
  // unused one WITHOUT restarting — the dropdown picks changes up live.
  let newCandidatePath = $state('');
  let addingCandidate = $state(false);

  async function addCandidate() {
    const path = newCandidatePath.trim();
    if (!path || addingCandidate) return;
    addingCandidate = true;
    try {
      await addStorageCandidate(path);
      newCandidatePath = '';
      storageCandidates = await getStorageCandidates().catch(() => storageCandidates);
      showToast(t('settings.storageAdded'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.storageAddFailed'), 'error');
    } finally {
      addingCandidate = false;
    }
  }

  async function removeCandidate(path: string) {
    try {
      await removeStorageCandidate(path);
      storageCandidates = await getStorageCandidates().catch(() => storageCandidates);
      if (storageRoot === path) storageRoot = storageCandidates?.current ?? storageRoot;
      showToast(t('settings.storageRemoved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.storageRemoveFailed'), 'error');
    }
  }

  // ── Storage migration (#395 rework) ──
  // Batch entry: switching the default is hot; per-camera history moves in
  // the background (idle-time, rate-limited) — the user is never blocked.
  let migrating = $state(false);
  let migrateDeleteSource = $state(true);
  let migrateJobs = $state<MigrationJob[]>([]);
  let migrateError = $state('');
  let migratePoll: ReturnType<typeof setInterval> | undefined;

  // The batch action can only target a candidate that differs from the saved root.
  let migrateTargetSelected = $derived.by(() =>
    !!storageRoot && !!originalStorageRoot && storageRoot !== originalStorageRoot,
  );

  function applyMigrateStatus(st: StorageMigrateStatusResponse) {
    migrateJobs = st.jobs.filter((j) => j.state !== 'done' && j.state !== 'failed');
    if (st.state === 'idle') {
      migrating = false;
      if (migratePoll) {
        clearInterval(migratePoll);
        migratePoll = undefined;
      }
      const failed = st.jobs.filter((j) => j.state === 'failed');
      if (failed.length) {
        migrateError = failed[0].error || t('settings.migrateFailed');
      }
    }
  }

  async function pollMigration() {
    try {
      applyMigrateStatus(await getStorageMigrateStatus());
    } catch {
      /* transient poll failure — the next tick retries */
    }
  }

  async function startMigration() {
    if (!storageRoot || storageRoot === originalStorageRoot || migrating) return;
    migrating = true;
    migrateError = '';
    migrateJobs = [];
    try {
      await startStorageMigrate(storageRoot, migrateDeleteSource);
      showToast(t('settings.migrateStarted'), 'success');
      await loadAll();
      migratePoll = setInterval(pollMigration, 1000);
      pollMigration();
    } catch (e) {
      migrating = false;
      migrateError = e instanceof Error ? e.message : t('settings.migrateFailed');
    }
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

  onDestroy(() => {
    unregister?.();
    if (migratePoll) clearInterval(migratePoll);
  });
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
  <!-- Recording root (#395): current + host-granted candidates -->
  <SettingsCard title={t('settings.storageRoot')} subtitle={t('settings.storageRootDesc')}>
    <div class="max-w-xl">
      <select class="input" bind:value={storageRoot} aria-label={t('settings.storageRoot')}>
        {#if storageCandidates}
          {#each storageCandidates.candidates as c (c.path)}
            <option value={c.path}>
              {c.path}{c.label === 'current' ? ` (${t('settings.storageRootCurrent')})` : ''}
            </option>
          {/each}
        {:else if storageRoot}
          <option value={storageRoot}>{storageRoot}</option>
        {/if}
      </select>
      {#if storageCandidates && storageCandidates.candidates.length <= 1}
        <p class="text-xs th-text-muted mt-2">{t('settings.storageRootNoCandidates')}</p>
      {:else}
        <p class="text-xs th-text-muted mt-2">{storageCandidates?.restart_hint}</p>
      {/if}
      <!-- Runtime candidate management: add a mounted path without a restart -->
      <div class="mt-3 flex gap-2">
        <input
          class="input flex-1 font-mono text-xs"
          placeholder="/mnt/disk2/nvr"
          bind:value={newCandidatePath}
          aria-label={t('settings.storageAddPath')}
          onkeydown={(e) => { if (e.key === 'Enter') addCandidate(); }}
        />
        <button class="btn btn-sm shrink-0" disabled={!newCandidatePath.trim() || addingCandidate} onclick={addCandidate}>
          {t('settings.storageAdd')}
        </button>
      </div>
      <p class="text-xs th-text-muted mt-1">{t('settings.storageAddPathHint')}</p>
      {#if storageCandidates?.env_managed}
        <p class="text-xs th-text-muted mt-1">{t('settings.storageEnvManagedHint')}</p>
      {/if}
      {#if storageCandidates && storageCandidates.candidates.length > 1}
        <div class="mt-2 flex flex-wrap gap-1.5">
          {#each storageCandidates.candidates as c (c.path)}
            {#if c.path !== storageCandidates.current}
              <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs th-bg-secondary border th-border font-mono">
                {c.path}
                <button
                  class="th-text-muted hover:th-color-danger"
                  aria-label={t('settings.storageRemove', { path: c.path })}
                  onclick={() => removeCandidate(c.path)}
                >✕</button>
              </span>
            {/if}
          {/each}
        </div>
      {/if}
      {#if migrateTargetSelected}
        <div class="mt-4 p-3 rounded-lg th-bg-secondary border th-border">
          <p class="text-xs th-text-secondary">{t('settings.migrateDesc')}</p>
          <div class="flex items-center gap-3 mt-2 flex-wrap">
            <button class="btn btn-primary btn-sm" disabled={migrating} onclick={startMigration}>
              {migrating ? t('settings.migrateRunning') : t('settings.migrateButton')}
            </button>
            <label class="flex items-center gap-1.5 text-xs th-text-secondary cursor-pointer">
              <input type="checkbox" bind:checked={migrateDeleteSource} disabled={migrating} />
              {t('settings.migrateDeleteSource')}
            </label>
          </div>
          {#if migrating && migrateJobs.length > 0}
            <div class="mt-3 space-y-2">
              {#each migrateJobs as job (job.camera_id)}
                <div>
                  <div class="flex justify-between text-xs th-text-muted">
                    <span>{job.camera_id}{#if job.state === 'paused'} · {t('cameras.storageMigrationPaused')}{/if}</span>
                    <span>{t('settings.migrateProgress', {
                      done: String(job.done_files ?? 0),
                      total: String(job.total_files ?? 0),
                      mb: ((job.done_bytes ?? 0) / (1024 * 1024)).toFixed(1),
                    })}</span>
                  </div>
                  <progress class="w-full" max={Math.max(job.total_files ?? 1, 1)} value={job.done_files ?? 0}></progress>
                </div>
              {/each}
            </div>
          {/if}
          {#if migrateError}
            <p class="th-color-danger text-xs mt-2" aria-live="polite">{migrateError}</p>
          {/if}
        </div>
      {/if}
    </div>
  </SettingsCard>

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
