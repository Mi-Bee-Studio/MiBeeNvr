<script lang="ts">
    import { t } from '$lib/i18n';
    import {
        getStorageCandidates,
        getCameraStorageRoot,
        setCameraStorageRoot,
    } from '$lib/api';
    import type { Camera, MigrationJob } from '$lib/api';
    import { onDestroy } from 'svelte';
    import { showToast } from '$lib/toast';

  interface Props {
    camera: Camera;
  }

  let { camera }: Props = $props();

  // Per-camera storage location (hot switch + background migration)
  let camStorageRoot = $state('');
  let camStorageDefault = $state('');
  let camStorageCandidates = $state<Array<{ path: string; label: string }>>([]);
  let camStorageMigrate = $state(true);
  let camStorageDeleteSource = $state(true);
  let camStorageSwitching = $state(false);
  let camStorageMigration = $state<MigrationJob | null>(null);
  let camStoragePoll: ReturnType<typeof setInterval> | undefined;

  $effect(() => {
    loadCameraStorage(camera.id);
  });

  onDestroy(() => {
    if (camStoragePoll) clearInterval(camStoragePoll);
  });

  async function loadCameraStorage(cameraId: string) {
    camStorageRoot = '';
    camStorageDefault = '';
    camStorageCandidates = [];
    camStorageMigration = null;
    try {
      const [info, cands] = await Promise.all([
        getCameraStorageRoot(cameraId),
        getStorageCandidates(),
      ]);
      camStorageRoot = info.override_root || '';
      camStorageDefault = info.default_root;
      camStorageCandidates = cands.candidates.filter((c) => c.path !== info.default_root);
      if (info.migration) startStoragePoll(cameraId);
    } catch { /* panel degrades silently */ }
  }

  function startStoragePoll(cameraId: string) {
    if (camStoragePoll) clearInterval(camStoragePoll);
    camStoragePoll = setInterval(async () => {
      try {
        const info = await getCameraStorageRoot(cameraId);
        camStorageMigration = info.migration ?? null;
        if (!info.migration && camStoragePoll) {
          clearInterval(camStoragePoll);
          camStoragePoll = undefined;
        }
      } catch { /* transient */ }
    }, 1500);
  }

  async function applyCameraStorage(cam: Camera) {
    if (camStorageSwitching) return;
    camStorageSwitching = true;
    try {
      const res = await setCameraStorageRoot(cam.id, camStorageRoot, camStorageMigrate, camStorageDeleteSource);
      if (res.migration) {
        camStorageMigration = res.migration;
        startStoragePoll(cam.id);
      }
      import('$lib/toast').then(({ showToast }) =>
        showToast(t('cameras.storageSwitched'), 'success'));
    } catch (e) {
      import('$lib/toast').then(({ showToast }) =>
        showToast(e instanceof Error ? e.message : t('cameras.storageSwitchFailed'), 'error'));
    } finally {
      camStorageSwitching = false;
    }
  }
</script>

<!-- Storage location: hot per-camera switch + background migration -->
<div class="md:col-span-2 border-t th-border pt-4 mt-2">
  <label class="input-label">{t('cameras.storageLocation')}</label>
  <div class="flex items-center gap-3 mt-2 flex-wrap">
    <select class="input max-w-xs" bind:value={camStorageRoot} disabled={camStorageSwitching}>
      <option value="">{t('cameras.storageDefaultOption')}</option>
      {#each camStorageCandidates as c (c.path)}
        <option value={c.path}>{c.path}</option>
      {/each}
    </select>
    <button
      type="button"
      class="btn btn-primary btn-sm"
      disabled={camStorageSwitching}
      onclick={() => applyCameraStorage(camera)}
    >
      {camStorageSwitching ? t('common.saving') : t('cameras.storageApply')}
    </button>
  </div>
  {#if camStorageCandidates.length === 0}
    <p class="text-xs th-text-muted mt-1">{t('cameras.storageNoCandidatesHint')}</p>
  {:else}
    <label class="flex items-center gap-1.5 text-xs th-text-secondary mt-2 cursor-pointer">
      <input type="checkbox" bind:checked={camStorageMigrate} disabled={camStorageSwitching} />
      {t('cameras.storageMigrateHistory')}
    </label>
    {#if camStorageMigrate && camStorageRoot}
      <label class="flex items-center gap-1.5 text-xs th-text-secondary ml-5 cursor-pointer">
        <input type="checkbox" bind:checked={camStorageDeleteSource} disabled={camStorageSwitching} />
        {t('settings.migrateDeleteSource')}
      </label>
    {/if}
    {#if camStorageMigration && (camStorageMigration.state === 'running' || camStorageMigration.state === 'queued' || camStorageMigration.state === 'paused')}
      <div class="mt-3">
        <progress class="w-full" max={Math.max(camStorageMigration.total_files ?? 1, 1)} value={camStorageMigration.done_files ?? 0}></progress>
        <p class="text-xs th-text-muted mt-1">
          {t('settings.migrateProgress', {
            done: String(camStorageMigration.done_files ?? 0),
            total: String(camStorageMigration.total_files ?? 0),
            mb: ((camStorageMigration.done_bytes ?? 0) / (1024 * 1024)).toFixed(1),
          })}
          {#if camStorageMigration.state === 'paused'}· {t('cameras.storageMigrationPaused')}{/if}
        </p>
      </div>
    {/if}
  {/if}
</div>
